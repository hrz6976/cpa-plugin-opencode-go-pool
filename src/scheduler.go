package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// sessionKeyFromOptions extracts a session identifier from the header sources
// the built-in affinity selector also understands. The request body is not
// visible to scheduler plugins, so body-derived session IDs (Claude Code
// metadata.user_id, message hashes) cannot be recovered here.
func sessionKeyFromOptions(opts pluginapi.SchedulerOptions) string {
	if len(opts.Headers) == 0 {
		return ""
	}
	headers := http.Header(opts.Headers)
	if sid := headers.Get("X-Session-ID"); sid != "" {
		return "header:" + sid
	}
	if sid := headers.Get("Session-Id"); sid != "" {
		return "codex:" + sid
	}
	if sid := headers.Get("Session_id"); sid != "" {
		return "codex:" + sid
	}
	if rid := headers.Get("X-Client-Request-Id"); rid != "" {
		return "clientreq:" + rid
	}
	return ""
}

type candidateRef struct {
	authID  string
	account *account
}

// handleSchedulerPick applies pool health to auth selection.
//
// Steady state (no account impaired): returns Handled=false so the host's
// built-in session-affinity round-robin keeps full control, including
// body-hash affinity the plugin cannot replicate.
//
// Degraded state (any candidate's account blocked): picks among healthy
// candidates with plugin-side stickiness, excluding both credentials of
// impaired accounts.
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	p := currentPool()
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.byAuthID) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	now := time.Now()
	var healthy []candidateRef
	managed := 0
	impaired := false
	for _, candidate := range req.Candidates {
		acct, ok := p.byAuthID[candidate.ID]
		if !ok {
			continue
		}
		managed++
		ref := candidateRef{authID: candidate.ID, account: acct}
		if reason := p.blockReason(acct, now); reason != "" {
			impaired = true
		} else {
			healthy = append(healthy, ref)
		}
	}

	if managed == 0 || !impaired || len(healthy) == 0 {
		// Nothing to manage, or nothing healthy left to offer: let the host's
		// built-in selection behave exactly as without this plugin.
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	sessionKey := sessionKeyFromOptions(req.Options)
	if sessionKey != "" {
		if bound, ok := p.stickyGet(sessionKey, now); ok {
			for _, ref := range healthy {
				if ref.account.Name == bound {
					return okEnvelope(pluginapi.SchedulerPickResponse{AuthID: ref.authID, Handled: true})
				}
			}
		}
	}

	cursorKey := req.Provider + "::" + req.Model
	index := p.cursor[cursorKey] % len(healthy)
	p.cursor[cursorKey] = (p.cursor[cursorKey] + 1) % (1 << 20)
	chosen := healthy[index]
	p.stickySet(sessionKey, chosen.account.Name, now)
	return okEnvelope(pluginapi.SchedulerPickResponse{AuthID: chosen.authID, Handled: true})
}
