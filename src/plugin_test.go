package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const fixtureConfig = `
openai-compatibility:
  - name: opencode-go
    base-url: https://opencode.ai/zen/go/v1
    api-key-entries:
      - api-key: sk-key-one
      - api-key: sk-key-two
      - api-key: sk-key-three
claude-api-key:
  - api-key: sk-key-one
    base-url: https://opencode.ai/zen/go
  - api-key: sk-key-two
    base-url: https://example.invalid/base-url-is-not-a-filter
  - api-key: sk-key-three
    base-url: https://opencode.ai/zen/go
  - api-key: sk-other
    base-url: https://api.anthropic.com
`

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixtureConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverAccounts(t *testing.T) {
	cfg := decodeSettings(nil)
	cfg.CPAConfigPath = writeFixture(t)
	cfg.Overrides = []accountOverride{{KeySuffix: "key-two", Name: "friend-a", WorkspaceID: "wrk_1", CookieFile: "/x"}}

	accounts, _, err := discoverAccounts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(accounts))
	}
	for _, acct := range accounts {
		if acct.OpenAIID == "" || acct.ClaudeID == "" {
			t.Errorf("account %s missing credential IDs: openai=%q claude=%q", acct.Name, acct.OpenAIID, acct.ClaudeID)
		}
		if !hasPrefix(acct.OpenAIID, "openai-compatibility:opencode-go:") {
			t.Errorf("unexpected openai ID %q", acct.OpenAIID)
		}
		if !hasPrefix(acct.ClaudeID, "claude:apikey:") {
			t.Errorf("unexpected claude ID %q", acct.ClaudeID)
		}
	}
	if accounts[1].Name != "friend-a" || accounts[1].WorkspaceID != "wrk_1" {
		t.Errorf("override not applied: %+v", accounts[1])
	}
	if accounts[0].Name != "go-1" || accounts[2].Name != "go-3" {
		t.Errorf("default names wrong: %q %q", accounts[0].Name, accounts[2].Name)
	}
}

func TestDiscoverAccountsReadsAuthDir(t *testing.T) {
	authDir := t.TempDir()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	raw := "auth-dir: " + strconv.Quote(authDir) + "\n" + fixtureConfig
	if errWrite := os.WriteFile(configPath, []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	cfg := decodeSettings(nil)
	cfg.CPAConfigPath = configPath
	_, discoveredAuthDir, errDiscover := discoverAccounts(cfg)
	if errDiscover != nil {
		t.Fatal(errDiscover)
	}
	if discoveredAuthDir != filepath.Clean(authDir) {
		t.Fatalf("auth-dir = %q, want %q", discoveredAuthDir, filepath.Clean(authDir))
	}
}

func TestResolveAuthDirDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	resolved, errResolve := resolveAuthDir("")
	if errResolve != nil {
		t.Fatal(errResolve)
	}
	want := filepath.Join(home, ".cli-proxy-api")
	if resolved != want {
		t.Fatalf("default auth-dir = %q, want %q", resolved, want)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestStableIDMatchesCPA pins the hash algorithm to a value computed by CPA's
// own StableIDGenerator (sha256 over kind + NUL-joined trimmed parts).
func TestStableIDMatchesCPA(t *testing.T) {
	gen := newStableIDGenerator()
	got := gen.next("claude:apikey", "sk-key-one", "https://opencode.ai/zen/go")
	// sha256("claude:apikey\x00sk-key-one\x00https://opencode.ai/zen/go")[:12]
	want := "claude:apikey:4c07a43b6ebf"
	if got != want {
		t.Fatalf("stable ID mismatch: got %q want %q", got, want)
	}
	// Duplicate input gains a -1 suffix like CPA's generator.
	dup := gen.next("claude:apikey", "sk-key-one", "https://opencode.ai/zen/go")
	if dup != want+"-1" {
		t.Fatalf("duplicate suffix mismatch: got %q", dup)
	}
}

func TestParse429(t *testing.T) {
	rec := pluginapi.UsageRecord{
		Failure: pluginapi.UsageFailure{
			StatusCode: 429,
			Body:       `{"type":"error","error":{"type":"GoUsageLimitError","message":"5 hour limit exceeded"},"metadata":{"workspace":"wrk_x","limitName":"5 hour"}}`,
		},
		ResponseHeaders: http.Header{"Retry-After": []string{"1800"}},
	}
	window, blockFor := parse429(rec, 10*time.Minute)
	if window != windowFiveHour {
		t.Errorf("window = %q, want %q", window, windowFiveHour)
	}
	if blockFor != 30*time.Minute {
		t.Errorf("blockFor = %v, want 30m", blockFor)
	}

	weekly := pluginapi.UsageRecord{Failure: pluginapi.UsageFailure{StatusCode: 429, Body: `..."limitName":"weekly"...`}}
	window, blockFor = parse429(weekly, 10*time.Minute)
	if window != windowWeekly || blockFor != 10*time.Minute {
		t.Errorf("weekly fallback: window=%q blockFor=%v", window, blockFor)
	}

	unknown := pluginapi.UsageRecord{Failure: pluginapi.UsageFailure{StatusCode: 429, Body: `upstream exploded`}}
	window, _ = parse429(unknown, 10*time.Minute)
	if window != "" {
		t.Errorf("unknown body should yield empty window, got %q", window)
	}
}

func TestParseDashboardHTMLSSR(t *testing.T) {
	html := `<script>$R[0]={rollingUsage:$R[1]={usagePercent:42,resetInSec:3600},weeklyUsage:$R[2]={resetInSec:86400,usagePercent:77},monthlyUsage:$R[3]={usagePercent:12,resetInSec:2592000}}</script>`
	got := parseDashboardHTML(html)
	if got[windowFiveHour].UsagePercent != 42 || got[windowFiveHour].ResetInSec != 3600 {
		t.Errorf("5h = %+v", got[windowFiveHour])
	}
	if got[windowWeekly].UsagePercent != 77 || got[windowWeekly].ResetInSec != 86400 {
		t.Errorf("weekly = %+v", got[windowWeekly])
	}
	if got[windowMonthly].UsagePercent != 12 {
		t.Errorf("monthly = %+v", got[windowMonthly])
	}
}

func TestParseDashboardHTMLDataSlot(t *testing.T) {
	html := `
<div data-slot="usage-item"><span data-slot="usage-label">Rolling Usage</span><span data-slot="usage-value">Used 42%</span><span data-slot="reset-time">Resets in 1 hour 30 minutes</span></div>
<div data-slot="usage-item"><span data-slot="usage-label">Weekly Usage</span><span data-slot="usage-value">88%</span><span data-slot="reset-now">Resets now</span></div>`
	got := parseDashboardHTML(html)
	if got[windowFiveHour].UsagePercent != 42 || got[windowFiveHour].ResetInSec != 5400 {
		t.Errorf("5h = %+v", got[windowFiveHour])
	}
	if got[windowWeekly].UsagePercent != 88 || got[windowWeekly].ResetInSec != 0 {
		t.Errorf("weekly = %+v", got[windowWeekly])
	}
}

func TestSchedulerHealthyPassthrough(t *testing.T) {
	cfg := decodeSettings(nil)
	cfg.CPAConfigPath = writeFixture(t)
	accounts, authDir, err := discoverAccounts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = authDir
	currentPool().reconfigure(cfg, accounts, nil)

	pick := func(candidates []pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickResponse {
		t.Helper()
		raw, errPick := handleSchedulerPick(mustJSON(t, pluginapi.SchedulerPickRequest{
			Provider:   "claude",
			Model:      "minimax-m3",
			Candidates: candidates,
		}))
		if errPick != nil {
			t.Fatal(errPick)
		}
		return decodePickResponse(t, raw)
	}

	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: accounts[0].ClaudeID, Provider: "claude"},
		{ID: accounts[1].ClaudeID, Provider: "claude"},
		{ID: accounts[2].ClaudeID, Provider: "claude"},
	}

	// All healthy: plugin must not interfere.
	if resp := pick(candidates); resp.Handled {
		t.Fatalf("expected Handled=false when healthy, got %+v", resp)
	}

	// Block account 0 via a synthetic 429 on its OpenAI credential; the
	// claude credential of the same account must then be excluded.
	_, errUsage := handleUsage(mustJSON(t, pluginapi.UsageRecord{
		AuthID: accounts[0].OpenAIID,
		Failed: true,
		Failure: pluginapi.UsageFailure{
			StatusCode: 429,
			Body:       `{"metadata":{"limitName":"5 hour"}}`,
		},
		ResponseHeaders: http.Header{"Retry-After": []string{"600"}},
	}))
	if errUsage != nil {
		t.Fatal(errUsage)
	}
	resp := pick(candidates)
	if !resp.Handled {
		t.Fatal("expected takeover when an account is impaired")
	}
	if resp.AuthID == accounts[0].ClaudeID {
		t.Fatal("picked the impaired account's sibling credential")
	}

	// Only the impaired account remains: hand control back to the host.
	if resp := pick(candidates[:1]); resp.Handled {
		t.Fatalf("expected Handled=false when no healthy accounts, got %+v", resp)
	}
}

func TestHostCooldownPromotion(t *testing.T) {
	cfg := decodeSettings(nil)
	cfg.CPAConfigPath = writeFixture(t)
	cdsDir := t.TempDir()
	accounts, _, err := discoverAccounts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = cdsDir
	p := currentPool()
	p.reconfigure(cfg, accounts, nil)
	p.mu.Lock()
	p.states = make(map[string]*accountState)
	p.mu.Unlock()

	retry := time.Now().Add(20 * time.Minute).UTC()
	cds := `{"version":1,"records":[{"auth_id":"` + accounts[2].OpenAIID + `","model":"glm-5.2","status":"cooling","next_retry_after":"` + retry.Format(time.RFC3339Nano) + `","reason":"quota","quota":{"exceeded":true,"reason":"quota"},"last_error":{"message":"weekly limit exceeded, retry later","http_status":429},"updated_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}]}`
	if errWrite := os.WriteFile(filepath.Join(cdsDir, "test.cds"), []byte(cds), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	refreshHostCooldowns(p)

	p.mu.Lock()
	reason := p.blockReason(accounts[2], time.Now())
	weekly := p.stateFor(accounts[2]).Windows[windowWeekly].BlockedUntil
	p.mu.Unlock()
	if reason == "" {
		t.Fatal("expected account 3 to be blocked from host cooldown")
	}
	if weekly.IsZero() {
		t.Fatalf("expected weekly window block, got reason %q", reason)
	}

	// The claude-side sibling credential must now be excluded by the picker.
	raw, errPick := handleSchedulerPick(mustJSON(t, pluginapi.SchedulerPickRequest{
		Provider: "claude",
		Model:    "minimax-m3",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: accounts[0].ClaudeID, Provider: "claude"},
			{ID: accounts[2].ClaudeID, Provider: "claude"},
		},
	}))
	if errPick != nil {
		t.Fatal(errPick)
	}
	resp := decodePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != accounts[0].ClaudeID {
		t.Fatalf("expected takeover picking account 1, got %+v", resp)
	}

	// Manual unblock suppresses the same cooldown record.
	if !unblockAccount(accounts[2].Name) {
		t.Fatal("unblock failed")
	}
	refreshHostCooldowns(p)
	p.mu.Lock()
	reason = p.blockReason(accounts[2], time.Now())
	p.mu.Unlock()
	if reason != "" {
		t.Fatalf("expected unblock to stick, got %q", reason)
	}
}

func TestAccountConfigUISettings(t *testing.T) {
	cfg := decodeSettings(nil)
	cfg.CPAConfigPath = writeFixture(t)
	accounts, _, err := discoverAccounts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = t.TempDir()
	p := currentPool()
	p.reconfigure(cfg, accounts, nil)

	if status, errSave := saveAccountConfig("go-1", "wrk_ABC", "auth=secret-cookie", false); errSave != nil {
		t.Fatalf("save failed: %d %v", status, errSave)
	}
	if _, errSave := saveAccountConfig("nope", "wrk_X", "", false); errSave == nil {
		t.Fatal("expected unknown account error")
	}

	p.mu.Lock()
	ws, cookie, cookieFile := p.dashboardCredentials(accounts[0])
	p.mu.Unlock()
	if ws != "wrk_ABC" || cookie != "secret-cookie" || cookieFile != "" {
		t.Fatalf("credentials = %q %q %q", ws, cookie, cookieFile)
	}

	// Settings survive reconfigure via the persisted file.
	accounts2, _, _ := discoverAccounts(cfg)
	p.reconfigure(cfg, accounts2, nil)
	p.mu.Lock()
	ws, cookie, _ = p.dashboardCredentials(accounts2[0])
	p.mu.Unlock()
	if ws != "wrk_ABC" || cookie != "secret-cookie" {
		t.Fatalf("credentials lost after reconfigure: %q %q", ws, cookie)
	}

	// Clear removes them.
	if _, errSave := saveAccountConfig("go-1", "", "", true); errSave != nil {
		t.Fatal(errSave)
	}
	p.mu.Lock()
	ws, cookie, _ = p.dashboardCredentials(accounts2[0])
	p.mu.Unlock()
	if ws != "" || cookie != "" {
		t.Fatalf("clear did not remove credentials: %q %q", ws, cookie)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodePickResponse(t *testing.T, raw []byte) pluginapi.SchedulerPickResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env.Error)
	}
	var resp pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}
