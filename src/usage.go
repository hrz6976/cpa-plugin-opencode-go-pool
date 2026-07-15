package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var limitNamePattern = regexp.MustCompile(`"limitName"\s*:\s*"([^"]+)"`)

// windowFromLimitName maps the OpenCode 429 metadata.limitName value to a
// plugin window key.
func windowFromLimitName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(name, "5 hour"), strings.Contains(name, "5-hour"), strings.Contains(name, "rolling"):
		return windowFiveHour
	case strings.Contains(name, "week"):
		return windowWeekly
	case strings.Contains(name, "month"):
		return windowMonthly
	default:
		return ""
	}
}

// parse429 extracts the exhausted window and reset delay from an OpenCode Go
// 429 response captured in a usage record.
func parse429(rec pluginapi.UsageRecord, fallback time.Duration) (window string, blockFor time.Duration) {
	body := rec.Failure.Body
	if body != "" {
		var parsed struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
			Metadata struct {
				LimitName string `json:"limitName"`
			} `json:"metadata"`
		}
		if errUnmarshal := json.Unmarshal([]byte(body), &parsed); errUnmarshal == nil && parsed.Metadata.LimitName != "" {
			window = windowFromLimitName(parsed.Metadata.LimitName)
		}
		if window == "" {
			if m := limitNamePattern.FindStringSubmatch(body); len(m) == 2 {
				window = windowFromLimitName(m[1])
			}
		}
	}

	blockFor = fallback
	if rec.ResponseHeaders != nil {
		if retryAfter := http.Header(rec.ResponseHeaders).Get("Retry-After"); retryAfter != "" {
			if seconds, errParse := strconv.ParseFloat(strings.TrimSpace(retryAfter), 64); errParse == nil && seconds > 0 {
				blockFor = time.Duration(seconds * float64(time.Second))
			}
		}
	}
	if blockFor > maxWindowBlock {
		blockFor = maxWindowBlock
	}
	return window, blockFor
}

// handleUsage synchronizes logical-account health from completed requests.
// A 429/401/403 observed on either credential of an account marks the whole
// account, so its sibling credential on the other protocol is excluded too.
func handleUsage(raw []byte) ([]byte, error) {
	var rec pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &rec); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	p := currentPool()
	p.mu.Lock()
	acct, ok := p.byAuthID[rec.AuthID]
	if !ok {
		p.mu.Unlock()
		return okEnvelope(struct{}{})
	}
	st := p.stateFor(acct)
	now := time.Now()
	st.LastUsedAt = now

	if !rec.Failed {
		st.Success++
		st.LastError = ""
		p.mu.Unlock()
		return okEnvelope(struct{}{})
	}

	st.Failed++
	var logMsg string
	fields := map[string]any{"account": acct.Name, "status": rec.Failure.StatusCode, "model": rec.Model}

	switch rec.Failure.StatusCode {
	case http.StatusTooManyRequests:
		window, blockFor := parse429(rec, p.cfg.FallbackCooldown)
		until := now.Add(blockFor)
		if window == "" {
			// Unknown window (or non-quota 429): apply a short conservative
			// block on the five-hour window slot.
			window = windowFiveHour
			if blockFor > p.cfg.FallbackCooldown {
				blockFor = p.cfg.FallbackCooldown
				until = now.Add(blockFor)
			}
		}
		w := st.Windows[window]
		if until.After(w.BlockedUntil) {
			w.BlockedUntil = until
		}
		st.LastError = "429 on " + window + " window"
		logMsg = "account blocked by 429"
		fields["window"] = window
		fields["blocked_until"] = until.Format(time.RFC3339)
	case http.StatusUnauthorized, http.StatusForbidden:
		st.SuspendedUntil = now.Add(p.cfg.SuspendDuration)
		st.SuspendReason = "auth error " + strconv.Itoa(rec.Failure.StatusCode)
		st.LastError = st.SuspendReason
		logMsg = "account suspended by auth error"
		fields["suspended_until"] = st.SuspendedUntil.Format(time.RFC3339)
	default:
		st.LastError = "upstream error " + strconv.Itoa(rec.Failure.StatusCode)
	}
	p.mu.Unlock()

	if logMsg != "" {
		hostLog("warn", logMsg, fields)
	}
	return okEnvelope(struct{}{})
}
