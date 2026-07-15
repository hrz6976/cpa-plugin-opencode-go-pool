package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	Scheduler     bool `json:"scheduler"`
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		startPoller()
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	case pluginabi.MethodManagementRegister:
		return handleManagementRegister()
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := decodeSettings(req.ConfigYAML)
	accounts, authDir, errDiscover := discoverAccounts(cfg)
	cfg.AuthDir = authDir
	currentPool().reconfigure(cfg, accounts, errDiscover)
	fields := map[string]any{"accounts": len(accounts), "threshold": cfg.ThresholdPercent}
	if errDiscover != nil {
		fields["config_error"] = errDiscover.Error()
	}
	hostLog("info", "configured", fields)
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "opencode-go-pool",
			Version:          pluginVersion,
			Author:           "hrz6976",
			GitHubRepository: "https://github.com/hrz6976/cpa-plugin-opencode-go-pool",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "cpa-config-path",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Path of the CPA config file used to auto-discover accounts (default config.yaml).",
				},
				{
					Name:        "threshold-percent",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Dashboard quota percentage at which an account stops receiving new requests (default 97).",
				},
				{
					Name:        "sticky-ttl",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "TTL of plugin-side session-to-account bindings during degraded routing (default 24h).",
				},
				{
					Name:        "suspend-duration",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "How long an account is suspended on 401/403 (default 30m).",
				},
				{
					Name:        "fallback-cooldown",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Account block duration for a 429 that carries no retry-after (default 10m).",
				},
				{
					Name:        "dashboard-refresh-interval",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Interval between dashboard quota refreshes for accounts with workspace-id and cookie-file (default 3m).",
				},
				{
					Name:        "dashboard-stale-after",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Dashboard data older than this is ignored for proactive blocking (default 20m).",
				},
				{
					Name:        "accounts",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Per-account overrides matched by key-suffix: name, workspace-id, cookie-file, disabled.",
				},
			},
		},
		Capabilities: registrationCapability{
			Scheduler:     true,
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}
}
