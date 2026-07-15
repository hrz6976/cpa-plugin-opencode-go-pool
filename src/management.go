package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	errAccountRequired = errors.New("account is required")
	errUnknownAccount  = errors.New("unknown account")
)

const (
	pluginID           = "opencode-go-pool"
	managementBasePath = "/plugins/" + pluginID
)

func handleManagementRegister() ([]byte, error) {
	return okEnvelope(pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementBasePath + "/status"},
			{Method: http.MethodPost, Path: managementBasePath + "/refresh"},
			{Method: http.MethodPost, Path: managementBasePath + "/unblock"},
			{Method: http.MethodPost, Path: managementBasePath + "/account-config"},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/status",
				Menu:        "OpenCode Go Pool",
				Description: "Quota and health of the OpenCode Go subscription pool.",
			},
		},
	})
}

type windowStatus struct {
	UsagePercent int    `json:"usage_percent"`
	ResetAt      string `json:"reset_at,omitempty"`
	BlockedUntil string `json:"blocked_until,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type accountStatus struct {
	Name                 string                  `json:"name"`
	KeySuffix            string                  `json:"key_suffix"`
	OpenAIAuthID         string                  `json:"openai_auth_id,omitempty"`
	ClaudeAuthID         string                  `json:"claude_auth_id,omitempty"`
	Disabled             bool                    `json:"disabled,omitempty"`
	Blocked              string                  `json:"blocked,omitempty"`
	SuspendedUntil       string                  `json:"suspended_until,omitempty"`
	SuspendReason        string                  `json:"suspend_reason,omitempty"`
	LastError            string                  `json:"last_error,omitempty"`
	LastUsedAt           string                  `json:"last_used_at,omitempty"`
	Success              int64                   `json:"success"`
	Failed               int64                   `json:"failed"`
	Windows              map[string]windowStatus `json:"windows"`
	DashboardConfigured  bool                    `json:"dashboard_configured"`
	WorkspaceID          string                  `json:"workspace_id,omitempty"`
	CookieSet            bool                    `json:"cookie_set"`
	DashboardRefreshedAt string                  `json:"dashboard_refreshed_at,omitempty"`
	DashboardError       string                  `json:"dashboard_error,omitempty"`
}

type poolStatus struct {
	Version          string          `json:"version"`
	Accounts         []accountStatus `json:"accounts"`
	StickyBindings   int             `json:"sticky_bindings"`
	ThresholdPercent int             `json:"threshold_percent"`
	ConfigError      string          `json:"config_error,omitempty"`
	GeneratedAt      string          `json:"generated_at"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func buildStatus() poolStatus {
	p := currentPool()
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	out := poolStatus{
		Version:          pluginVersion,
		StickyBindings:   len(p.sticky),
		ThresholdPercent: p.cfg.ThresholdPercent,
		ConfigError:      p.configError,
		GeneratedAt:      now.Format(time.RFC3339),
		Accounts:         make([]accountStatus, 0, len(p.accounts)),
	}
	for _, acct := range p.accounts {
		st := p.stateFor(acct)
		entry := accountStatus{
			Name:           acct.Name,
			KeySuffix:      acct.KeySuffix,
			OpenAIAuthID:   acct.OpenAIID,
			ClaudeAuthID:   acct.ClaudeID,
			Disabled:       acct.Disabled,
			Blocked:        p.blockReason(acct, now),
			SuspendedUntil: formatTime(st.SuspendedUntil),
			SuspendReason:  st.SuspendReason,
			LastError:      st.LastError,
			LastUsedAt:     formatTime(st.LastUsedAt),
			Success:        st.Success,
			Failed:         st.Failed,
			Windows:        make(map[string]windowStatus, len(windowNames)),
		}
		workspaceID, cookie, cookieFile := p.dashboardCredentials(acct)
		entry.WorkspaceID = workspaceID
		entry.CookieSet = cookie != "" || cookieFile != ""
		entry.DashboardConfigured = workspaceID != "" && entry.CookieSet
		if !st.SuspendedUntil.After(now) {
			entry.SuspendedUntil = ""
			entry.SuspendReason = ""
		}
		entry.DashboardRefreshedAt = formatTime(st.DashboardRefreshedAt)
		entry.DashboardError = st.DashboardError
		for _, name := range windowNames {
			w := st.Windows[name]
			ws := windowStatus{UsagePercent: w.UsagePercent, UpdatedAt: formatTime(w.UpdatedAt)}
			if w.ResetAt.After(now) {
				ws.ResetAt = formatTime(w.ResetAt)
			}
			if w.BlockedUntil.After(now) {
				ws.BlockedUntil = formatTime(w.BlockedUntil)
			}
			entry.Windows[name] = ws
		}
		out.Accounts = append(out.Accounts, entry)
	}
	return out
}

func jsonResponse(status int, v any) ([]byte, error) {
	body, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	})
}

func htmlResponse(body string) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
	})
}

func normalizeManagementPath(path string) (string, bool) {
	isResource := false
	if idx := strings.Index(path, "/v0/resource/plugins/"+pluginID); idx >= 0 {
		path = path[idx+len("/v0/resource/plugins/"+pluginID):]
		isResource = true
	} else if idx := strings.Index(path, "/v0/management/plugins/"+pluginID); idx >= 0 {
		path = path[idx+len("/v0/management/plugins/"+pluginID):]
	} else if strings.HasPrefix(path, managementBasePath) {
		path = strings.TrimPrefix(path, managementBasePath)
	}
	if path == "" {
		path = "/"
	}
	return path, isResource
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	path, isResource := normalizeManagementPath(req.Path)

	if isResource {
		// Resource routes are not management-authenticated: serve only the
		// static page shell, never account data.
		if req.Method == http.MethodGet && (path == "/status" || path == "/") {
			return htmlResponse(statusPageHTML)
		}
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
	}

	switch {
	case req.Method == http.MethodGet && path == "/status":
		return jsonResponse(http.StatusOK, buildStatus())
	case req.Method == http.MethodPost && path == "/refresh":
		kickPoller()
		return jsonResponse(http.StatusAccepted, map[string]string{"status": "refresh scheduled"})
	case req.Method == http.MethodPost && path == "/account-config":
		var body struct {
			Account     string `json:"account"`
			WorkspaceID string `json:"workspace_id"`
			Cookie      string `json:"cookie"`
			Clear       bool   `json:"clear"`
		}
		if errUnmarshal := json.Unmarshal(req.Body, &body); errUnmarshal != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		}
		status, errSave := saveAccountConfig(body.Account, body.WorkspaceID, body.Cookie, body.Clear)
		if errSave != nil {
			return jsonResponse(status, map[string]string{"error": errSave.Error()})
		}
		kickPoller()
		return jsonResponse(http.StatusOK, map[string]string{"status": "saved"})
	case req.Method == http.MethodPost && path == "/unblock":
		var body struct {
			Account string `json:"account"`
		}
		_ = json.Unmarshal(req.Body, &body)
		if strings.TrimSpace(body.Account) == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account is required"})
		}
		if unblockAccount(body.Account) {
			return jsonResponse(http.StatusOK, map[string]string{"status": "unblocked"})
		}
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "unknown account"})
	default:
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// saveAccountConfig stores UI-entered dashboard credentials for one account.
// An empty cookie keeps the previously stored cookie; clear removes both.
func saveAccountConfig(name, workspaceID, cookie string, clear bool) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return http.StatusBadRequest, errAccountRequired
	}
	p := currentPool()
	p.mu.Lock()
	for _, acct := range p.accounts {
		if acct.Name != name {
			continue
		}
		id := identityKey(acct)
		current := p.uiSettings[id]
		if clear {
			delete(p.uiSettings, id)
		} else {
			if trimmed := strings.TrimSpace(workspaceID); trimmed != "" {
				current.WorkspaceID = trimmed
			}
			if trimmed := strings.TrimSpace(cookie); trimmed != "" {
				current.Cookie = strings.TrimPrefix(trimmed, "auth=")
			}
			p.uiSettings[id] = current
		}
		if errSave := saveUISettings(pluginStateDir(p.cfg.AuthDir), p.uiSettings); errSave != nil {
			p.mu.Unlock()
			return http.StatusInternalServerError, errSave
		}
		st := p.stateFor(acct)
		st.DashboardError = ""
		p.mu.Unlock()
		hostLog("info", "account dashboard credentials updated", map[string]any{"account": name, "cleared": clear})
		return http.StatusOK, nil
	}
	p.mu.Unlock()
	return http.StatusNotFound, errUnknownAccount
}

func unblockAccount(name string) bool {
	p := currentPool()
	p.mu.Lock()
	for _, acct := range p.accounts {
		if acct.Name != name {
			continue
		}
		st := p.stateFor(acct)
		st.SuspendedUntil = time.Time{}
		st.SuspendReason = ""
		st.LastError = ""
		st.CooldownSuppressedAt = time.Now()
		for _, w := range st.Windows {
			w.BlockedUntil = time.Time{}
			w.UsagePercent = -1
			w.ResetAt = time.Time{}
		}
		p.mu.Unlock()
		hostLog("info", "account manually unblocked", map[string]any{"account": name})
		return true
	}
	p.mu.Unlock()
	return false
}
