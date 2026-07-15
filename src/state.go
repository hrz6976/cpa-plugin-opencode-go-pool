package main

import (
	"sync"
	"time"
)

const (
	windowFiveHour = "5h"
	windowWeekly   = "weekly"
	windowMonthly  = "monthly"
)

var windowNames = [...]string{windowFiveHour, windowWeekly, windowMonthly}

// windowState tracks one quota window of a logical account.
type windowState struct {
	// UsagePercent is the last dashboard-reported percentage, -1 when unknown.
	UsagePercent int
	// ResetAt is the dashboard-reported reset time (zero when unknown).
	ResetAt time.Time
	// BlockedUntil is a hard block derived from an upstream 429.
	BlockedUntil time.Time
	// UpdatedAt is when dashboard data last refreshed this window.
	UpdatedAt time.Time
}

// accountState is the mutable runtime health of one logical account. It is
// keyed by key-hash so it survives reconfigure.
type accountState struct {
	Windows        map[string]*windowState
	SuspendedUntil time.Time
	SuspendReason  string
	LastError      string
	LastUsedAt     time.Time
	Success        int64
	Failed         int64

	DashboardRefreshedAt time.Time
	DashboardError       string

	// CooldownSuppressedAt makes a manual unblock stick: host cooldown
	// records not updated after this time are ignored.
	CooldownSuppressedAt time.Time
}

func newAccountState() *accountState {
	ws := make(map[string]*windowState, len(windowNames))
	for _, name := range windowNames {
		ws[name] = &windowState{UsagePercent: -1}
	}
	return &accountState{Windows: ws}
}

type stickyEntry struct {
	Account   string
	ExpiresAt time.Time
}

// pool is the plugin's global state.
type pool struct {
	mu sync.Mutex

	cfg      settings
	accounts []*account
	// byAuthID maps both credential IDs of every account to the account.
	byAuthID map[string]*account
	// states is keyed by account.KeySuffix + name-independent identity (the
	// OpenAI auth ID is stable per key, so use it as identity key).
	states map[string]*accountState

	sticky map[string]stickyEntry
	cursor map[string]int

	// uiSettings holds dashboard credentials entered through the management
	// UI, keyed by account identity key. They take precedence over the
	// config-file overrides.
	uiSettings map[string]uiAccountSettings

	configError string
}

var globalPool = &pool{
	byAuthID: make(map[string]*account),
	states:   make(map[string]*accountState),
	sticky:   make(map[string]stickyEntry),
	cursor:   make(map[string]int),
}

func currentPool() *pool {
	return globalPool
}

// identityKey returns the state key of an account: stable across restarts and
// config edits that don't change the API key.
func identityKey(acct *account) string {
	if acct.OpenAIID != "" {
		return acct.OpenAIID
	}
	return acct.ClaudeID
}

// reconfigure rebuilds account mapping from settings, keeping runtime state
// for accounts whose identity persists.
func (p *pool) reconfigure(cfg settings, accounts []*account, configErr error) {
	uiSettings := loadUISettings(pluginStateDir(cfg.AuthDir))
	if uiSettings == nil {
		uiSettings = make(map[string]uiAccountSettings)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
	p.accounts = accounts
	p.uiSettings = uiSettings
	p.byAuthID = make(map[string]*account, len(accounts)*2)
	if configErr != nil {
		p.configError = configErr.Error()
	} else {
		p.configError = ""
	}
	seen := make(map[string]bool, len(accounts))
	for _, acct := range accounts {
		if acct.OpenAIID != "" {
			p.byAuthID[acct.OpenAIID] = acct
		}
		if acct.ClaudeID != "" {
			p.byAuthID[acct.ClaudeID] = acct
		}
		id := identityKey(acct)
		seen[id] = true
		if _, ok := p.states[id]; !ok {
			p.states[id] = newAccountState()
		}
	}
	for id := range p.states {
		if !seen[id] {
			delete(p.states, id)
		}
	}
}

func (p *pool) stateFor(acct *account) *accountState {
	id := identityKey(acct)
	st, ok := p.states[id]
	if !ok {
		st = newAccountState()
		p.states[id] = st
	}
	return st
}

// dashboardCredentials returns the effective workspace ID and cookie source
// for an account: UI-entered settings win over config-file overrides. The
// second return is the literal cookie value ("" when a cookie file should be
// read instead). Caller must hold p.mu.
func (p *pool) dashboardCredentials(acct *account) (workspaceID, cookie, cookieFile string) {
	workspaceID = acct.WorkspaceID
	cookieFile = acct.CookieFile
	if ui, ok := p.uiSettings[identityKey(acct)]; ok {
		if ui.WorkspaceID != "" {
			workspaceID = ui.WorkspaceID
		}
		if ui.Cookie != "" {
			cookie = ui.Cookie
			cookieFile = ""
		}
	}
	return workspaceID, cookie, cookieFile
}

// blockReason reports why an account should not receive new requests, or ""
// when it is healthy. Caller must hold p.mu.
func (p *pool) blockReason(acct *account, now time.Time) string {
	if acct.Disabled {
		return "disabled"
	}
	st := p.stateFor(acct)
	if st.SuspendedUntil.After(now) {
		return "suspended: " + st.SuspendReason
	}
	for _, name := range windowNames {
		w := st.Windows[name]
		if w.BlockedUntil.After(now) {
			return "quota window " + name + " exhausted (429)"
		}
	}
	// Proactive dashboard threshold, only when data is fresh.
	if !st.DashboardRefreshedAt.IsZero() && now.Sub(st.DashboardRefreshedAt) <= p.cfg.StaleAfter {
		for _, name := range windowNames {
			w := st.Windows[name]
			if w.UsagePercent >= p.cfg.ThresholdPercent && (w.ResetAt.IsZero() || w.ResetAt.After(now)) {
				return "quota window " + name + " at threshold"
			}
		}
	}
	return ""
}

// stickyGet returns the bound account name for a session key when the binding
// is still valid. Caller must hold p.mu.
func (p *pool) stickyGet(sessionKey string, now time.Time) (string, bool) {
	entry, ok := p.sticky[sessionKey]
	if !ok || entry.ExpiresAt.Before(now) {
		if ok {
			delete(p.sticky, sessionKey)
		}
		return "", false
	}
	entry.ExpiresAt = now.Add(p.cfg.StickyTTL)
	p.sticky[sessionKey] = entry
	return entry.Account, true
}

// stickySet binds a session key to an account and lazily prunes expired
// entries. Caller must hold p.mu.
func (p *pool) stickySet(sessionKey, accountName string, now time.Time) {
	if sessionKey == "" {
		return
	}
	if len(p.sticky) > 4096 {
		for key, entry := range p.sticky {
			if entry.ExpiresAt.Before(now) {
				delete(p.sticky, key)
			}
		}
	}
	p.sticky[sessionKey] = stickyEntry{Account: accountName, ExpiresAt: now.Add(p.cfg.StickyTTL)}
}
