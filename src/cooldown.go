package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The host persists its runtime cooldown state as one .cds file per auth in
// the auth directory (sdk/cliproxy/auth/cooldown_state.go). Usage records are
// not reliably delivered to RPC plugins on CPA v7.2.67 (the async dispatch
// aborts once the request context is canceled), so these files are the
// authoritative signal for upstream 429/401/403 cooldowns. Any quota or auth
// cooldown on one credential of a logical account is promoted to the whole
// account, which also excludes the sibling credential on the other protocol.

type cdsQuota struct {
	Exceeded bool   `json:"exceeded"`
	Reason   string `json:"reason"`
}

type cdsErrorDetail struct {
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status"`
}

type cdsRecord struct {
	AuthID         string          `json:"auth_id"`
	NextRetryAfter time.Time       `json:"next_retry_after"`
	Reason         string          `json:"reason"`
	Quota          cdsQuota        `json:"quota"`
	LastError      *cdsErrorDetail `json:"last_error"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type cdsFile struct {
	Records []cdsRecord `json:"records"`
}

func readCooldownRecords(dir string) []cdsRecord {
	var out []cdsRecord
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".cds") {
			return nil
		}
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			return nil
		}
		var file cdsFile
		if errUnmarshal := json.Unmarshal(raw, &file); errUnmarshal != nil {
			return nil
		}
		out = append(out, file.Records...)
		return nil
	})
	return out
}

func (r cdsRecord) isQuotaCooldown() bool {
	if r.Quota.Exceeded || strings.EqualFold(strings.TrimSpace(r.Quota.Reason), "quota") ||
		strings.EqualFold(strings.TrimSpace(r.Reason), "quota") {
		return true
	}
	return r.LastError != nil && r.LastError.HTTPStatus == 429
}

func (r cdsRecord) isAuthCooldown() bool {
	if r.LastError != nil && (r.LastError.HTTPStatus == 401 || r.LastError.HTTPStatus == 403) {
		return true
	}
	reason := strings.ToLower(r.Reason)
	return strings.Contains(reason, "unauthorized") || strings.Contains(reason, "forbidden")
}

// windowFromRecord infers which quota window a cooldown belongs to from the
// captured error text ("5 hour" / "weekly" / "monthly" limitName).
func (r cdsRecord) window() string {
	text := r.Reason
	if r.LastError != nil {
		text += " " + r.LastError.Message
	}
	if m := limitNamePattern.FindStringSubmatch(text); len(m) == 2 {
		if w := windowFromLimitName(m[1]); w != "" {
			return w
		}
	}
	if w := windowFromLimitName(text); w != "" {
		return w
	}
	return windowFiveHour
}

// refreshHostCooldowns applies the host's persisted cooldown state to the
// logical accounts.
func refreshHostCooldowns(p *pool) {
	p.mu.Lock()
	dir := p.cfg.AuthDir
	if dir == "" || len(p.byAuthID) == 0 {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	records := readCooldownRecords(dir)
	if len(records) == 0 {
		return
	}
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, record := range records {
		if !record.NextRetryAfter.After(now) {
			continue
		}
		acct, ok := p.byAuthID[record.AuthID]
		if !ok {
			continue
		}
		st := p.stateFor(acct)
		if !record.UpdatedAt.IsZero() && record.UpdatedAt.Before(st.CooldownSuppressedAt) {
			continue
		}
		switch {
		case record.isQuotaCooldown():
			w := st.Windows[record.window()]
			if record.NextRetryAfter.After(w.BlockedUntil) {
				w.BlockedUntil = record.NextRetryAfter
				if st.LastError == "" {
					st.LastError = "host cooldown: " + record.Reason
				}
			}
		case record.isAuthCooldown():
			if record.NextRetryAfter.After(st.SuspendedUntil) {
				st.SuspendedUntil = record.NextRetryAfter
				st.SuspendReason = "host cooldown: " + record.Reason
			}
		default:
			// Other cooldowns (model unsupported, transient 5xx) stay scoped
			// to the single credential; the host already excludes it.
		}
	}
}
