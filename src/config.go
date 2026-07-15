package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	opencodeCompatName   = "opencode-go"
	defaultCPAConfigPath = "config.yaml"
	defaultThresholdPct  = 97
	defaultStickyTTL     = 24 * time.Hour
	defaultSuspend       = 30 * time.Minute
	defaultFallbackCool  = 10 * time.Minute
	defaultRefreshEvery  = 3 * time.Minute
	defaultStaleAfter    = 20 * time.Minute
	defaultAuthDir       = "~/.cli-proxy-api"
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
	CPAConfigPath    string            `yaml:"cpa-config-path"`
	ThresholdPercent int               `yaml:"threshold-percent"`
	StickyTTL        string            `yaml:"sticky-ttl"`
	SuspendDuration  string            `yaml:"suspend-duration"`
	FallbackCooldown string            `yaml:"fallback-cooldown"`
	RefreshInterval  string            `yaml:"dashboard-refresh-interval"`
	StaleAfter       string            `yaml:"dashboard-stale-after"`
	Accounts         []accountOverride `yaml:"accounts"`
}

type settings struct {
	CPAConfigPath    string
	ThresholdPercent int
	StickyTTL        time.Duration
	SuspendDuration  time.Duration
	FallbackCooldown time.Duration
	RefreshInterval  time.Duration
	StaleAfter       time.Duration
	AuthDir          string
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
		CPAConfigPath:    strings.TrimSpace(cfg.CPAConfigPath),
		ThresholdPercent: cfg.ThresholdPercent,
		StickyTTL:        parseDurationOr(cfg.StickyTTL, defaultStickyTTL),
		SuspendDuration:  parseDurationOr(cfg.SuspendDuration, defaultSuspend),
		FallbackCooldown: parseDurationOr(cfg.FallbackCooldown, defaultFallbackCool),
		RefreshInterval:  parseDurationOr(cfg.RefreshInterval, defaultRefreshEvery),
		StaleAfter:       parseDurationOr(cfg.StaleAfter, defaultStaleAfter),
		Overrides:        cfg.Accounts,
	}
	if out.CPAConfigPath == "" {
		out.CPAConfigPath = defaultCPAConfigPath
	}
	if out.ThresholdPercent <= 0 || out.ThresholdPercent > 100 {
		out.ThresholdPercent = defaultThresholdPct
	}
	return out
}

// cpaConfig mirrors just the parts of the CPA config file needed to discover
// OpenCode Go credentials.
type cpaConfig struct {
	AuthDir             string `yaml:"auth-dir"`
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

// resolveAuthDir mirrors CPA's auth-dir resolution so the plugin reads host
// cooldown files and stores its own state alongside the host credentials.
func resolveAuthDir(raw string) (string, error) {
	if raw == "" {
		raw = defaultAuthDir
	}
	if !strings.HasPrefix(raw, "~") {
		return filepath.Clean(raw), nil
	}

	home, errHome := os.UserHomeDir()
	if errHome != nil {
		return "", fmt.Errorf("resolve auth dir: %w", errHome)
	}
	remainder := strings.TrimLeft(strings.TrimPrefix(raw, "~"), `/\`)
	if remainder == "" {
		return filepath.Clean(home), nil
	}
	remainder = strings.ReplaceAll(remainder, `\`, "/")
	return filepath.Clean(filepath.Join(home, filepath.FromSlash(remainder))), nil
}

func pluginStateDir(authDir string) string {
	if authDir == "" {
		return ""
	}
	return filepath.Join(authDir, pluginID)
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
func discoverAccounts(cfg settings) ([]*account, string, error) {
	raw, errRead := os.ReadFile(cfg.CPAConfigPath)
	if errRead != nil {
		return nil, "", fmt.Errorf("read CPA config %s: %w", cfg.CPAConfigPath, errRead)
	}
	var cpa cpaConfig
	if errUnmarshal := yaml.Unmarshal(raw, &cpa); errUnmarshal != nil {
		return nil, "", fmt.Errorf("parse CPA config: %w", errUnmarshal)
	}
	authDir, errResolve := resolveAuthDir(cpa.AuthDir)
	if errResolve != nil {
		return nil, "", fmt.Errorf("resolve CPA auth-dir: %w", errResolve)
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
			if providerName != opencodeCompatName || key == "" {
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
		acct, ok := byKey[key]
		if !ok {
			continue
		}
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
	return ordered, authDir, nil
}

func keySuffix(key string) string {
	if len(key) <= 6 {
		return key
	}
	return key[len(key)-6:]
}
