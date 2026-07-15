package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultCompatName    = "opencode-go"
	defaultClaudeBaseURL = "https://opencode.ai/zen/go"
	defaultCPAConfigPath = "config.yaml"
	defaultThresholdPct  = 97
	defaultStickyTTL     = 24 * time.Hour
	defaultSuspend       = 30 * time.Minute
	defaultFallbackCool  = 10 * time.Minute
	defaultRefreshEvery  = 3 * time.Minute
	defaultStaleAfter    = 20 * time.Minute
	defaultCooldownDir   = "/root/.cli-proxy-api"
	defaultStateDir      = "/root/.cli-proxy-api/opencode-go-pool"
	cooldownScanInterval = 10 * time.Second
	maxWindowBlock       = 35 * 24 * time.Hour
	claudeProviderKey    = "claude"
	openaiCompatKindBase = "openai-compatibility"
)

// accountOverride carries optional per-account settings from the plugin
// config, matched to a discovered credential pair by API-key suffix.
type accountOverride struct {
	KeySuffix   string `yaml:"key-suffix"`
	Name        string `yaml:"name"`
	WorkspaceID string `yaml:"workspace-id"`
	CookieFile  string `yaml:"cookie-file"`
	Disabled    bool   `yaml:"disabled"`
}

type pluginConfig struct {
	CompatName       string            `yaml:"compat-name"`
	ClaudeBaseURL    string            `yaml:"claude-base-url"`
	CPAConfigPath    string            `yaml:"cpa-config-path"`
	ThresholdPercent int               `yaml:"threshold-percent"`
	StickyTTL        string            `yaml:"sticky-ttl"`
	SuspendDuration  string            `yaml:"suspend-duration"`
	FallbackCooldown string            `yaml:"fallback-cooldown"`
	RefreshInterval  string            `yaml:"dashboard-refresh-interval"`
	StaleAfter       string            `yaml:"dashboard-stale-after"`
	CooldownDir      string            `yaml:"cooldown-dir"`
	StateDir         string            `yaml:"state-dir"`
	Accounts         []accountOverride `yaml:"accounts"`
}

type settings struct {
	CompatName       string
	ClaudeBaseURL    string
	CPAConfigPath    string
	ThresholdPercent int
	StickyTTL        time.Duration
	SuspendDuration  time.Duration
	FallbackCooldown time.Duration
	RefreshInterval  time.Duration
	StaleAfter       time.Duration
	CooldownDir      string
	StateDir         string
	Overrides        []accountOverride
}

func parseDurationOr(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func decodeSettings(configYAML []byte) settings {
	cfg := pluginConfig{}
	if len(configYAML) > 0 {
		_ = yaml.Unmarshal(configYAML, &cfg)
	}
	out := settings{
		CompatName:       strings.ToLower(strings.TrimSpace(cfg.CompatName)),
		ClaudeBaseURL:    strings.TrimSpace(cfg.ClaudeBaseURL),
		CPAConfigPath:    strings.TrimSpace(cfg.CPAConfigPath),
		ThresholdPercent: cfg.ThresholdPercent,
		StickyTTL:        parseDurationOr(cfg.StickyTTL, defaultStickyTTL),
		SuspendDuration:  parseDurationOr(cfg.SuspendDuration, defaultSuspend),
		FallbackCooldown: parseDurationOr(cfg.FallbackCooldown, defaultFallbackCool),
		RefreshInterval:  parseDurationOr(cfg.RefreshInterval, defaultRefreshEvery),
		StaleAfter:       parseDurationOr(cfg.StaleAfter, defaultStaleAfter),
		CooldownDir:      strings.TrimSpace(cfg.CooldownDir),
		StateDir:         strings.TrimSpace(cfg.StateDir),
		Overrides:        cfg.Accounts,
	}
	if out.CompatName == "" {
		out.CompatName = defaultCompatName
	}
	if out.ClaudeBaseURL == "" {
		out.ClaudeBaseURL = defaultClaudeBaseURL
	}
	if out.CPAConfigPath == "" {
		out.CPAConfigPath = defaultCPAConfigPath
	}
	if out.CooldownDir == "" {
		out.CooldownDir = defaultCooldownDir
	}
	if out.StateDir == "" {
		out.StateDir = defaultStateDir
	}
	if out.ThresholdPercent <= 0 || out.ThresholdPercent > 100 {
		out.ThresholdPercent = defaultThresholdPct
	}
	return out
}

// cpaConfig mirrors just the parts of the CPA config file needed to discover
// OpenCode Go credentials.
type cpaConfig struct {
	OpenAICompatibility []struct {
		Name          string `yaml:"name"`
		BaseURL       string `yaml:"base-url"`
		Disabled      bool   `yaml:"disabled"`
		APIKeyEntries []struct {
			APIKey   string `yaml:"api-key"`
			ProxyURL string `yaml:"proxy-url"`
		} `yaml:"api-key-entries"`
	} `yaml:"openai-compatibility"`
	ClaudeKey []struct {
		APIKey  string `yaml:"api-key"`
		BaseURL string `yaml:"base-url"`
	} `yaml:"claude-api-key"`
}

// stableIDGenerator replicates CPA's internal/watcher/synthesizer
// StableIDGenerator so the plugin can compute the exact runtime auth IDs of
// config-synthesized credentials.
type stableIDGenerator struct {
	counters map[string]int
}

func newStableIDGenerator() *stableIDGenerator {
	return &stableIDGenerator{counters: make(map[string]int)}
}

func (g *stableIDGenerator) next(kind string, parts ...string) string {
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		hasher.Write([]byte{0})
		hasher.Write([]byte(strings.TrimSpace(part)))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return kind + ":" + short
}

// account is one logical OpenCode Go subscription: two CPA credentials that
// share the same quota windows.
type account struct {
	Name        string
	KeySuffix   string
	OpenAIID    string
	ClaudeID    string
	WorkspaceID string
	CookieFile  string
	Disabled    bool

	// apiKey is kept in memory only for override matching; it must never be
	// serialized into status output or logs.
	apiKey string
}

// discoverAccounts reads the CPA config file and pairs openai-compatibility
// and claude-api-key entries that use the same API key into logical accounts.
func discoverAccounts(cfg settings) ([]*account, error) {
	raw, errRead := os.ReadFile(cfg.CPAConfigPath)
	if errRead != nil {
		return nil, fmt.Errorf("read CPA config %s: %w", cfg.CPAConfigPath, errRead)
	}
	var cpa cpaConfig
	if errUnmarshal := yaml.Unmarshal(raw, &cpa); errUnmarshal != nil {
		return nil, fmt.Errorf("parse CPA config: %w", errUnmarshal)
	}

	idGen := newStableIDGenerator()
	byKey := make(map[string]*account)
	var ordered []*account

	ensure := func(key string) *account {
		if acct, ok := byKey[key]; ok {
			return acct
		}
		acct := &account{KeySuffix: keySuffix(key), apiKey: key}
		byKey[key] = acct
		ordered = append(ordered, acct)
		return acct
	}

	// openai-compatibility entries; iterate all entries to keep the ID
	// generator's duplicate counters aligned with CPA's synthesizer.
	for i := range cpa.OpenAICompatibility {
		compat := cpa.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = openaiCompatKindBase
		}
		kind := fmt.Sprintf("%s:%s", openaiCompatKindBase, providerName)
		base := strings.TrimSpace(compat.BaseURL)
		for j := range compat.APIKeyEntries {
			entry := compat.APIKeyEntries[j]
			key := strings.TrimSpace(entry.APIKey)
			id := idGen.next(kind, key, base, strings.TrimSpace(entry.ProxyURL))
			if providerName != cfg.CompatName || key == "" {
				continue
			}
			acct := ensure(key)
			acct.OpenAIID = id
		}
	}

	// claude-api-key entries.
	for i := range cpa.ClaudeKey {
		ck := cpa.ClaudeKey[i]
		key := strings.TrimSpace(ck.APIKey)
		if key == "" {
			continue
		}
		base := strings.TrimSpace(ck.BaseURL)
		id := idGen.next("claude:apikey", key, base)
		if !strings.EqualFold(strings.TrimRight(base, "/"), strings.TrimRight(cfg.ClaudeBaseURL, "/")) {
			continue
		}
		acct := ensure(key)
		acct.ClaudeID = id
	}

	// Apply overrides and default names.
	for i, acct := range ordered {
		acct.Name = fmt.Sprintf("go-%d", i+1)
		for _, ov := range cfg.Overrides {
			suffix := strings.TrimSpace(ov.KeySuffix)
			if suffix == "" || !strings.HasSuffix(acct.apiKey, suffix) {
				continue
			}
			if ov.Name != "" {
				acct.Name = ov.Name
			}
			acct.WorkspaceID = strings.TrimSpace(ov.WorkspaceID)
			acct.CookieFile = strings.TrimSpace(ov.CookieFile)
			acct.Disabled = ov.Disabled
		}
	}
	return ordered, nil
}

func keySuffix(key string) string {
	if len(key) <= 6 {
		return key
	}
	return key[len(key)-6:]
}
