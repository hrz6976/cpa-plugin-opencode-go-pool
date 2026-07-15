package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

// hostCall invokes a host callback and unwraps the response envelope.
func hostCall(method string, payload any) (json.RawMessage, error) {
	var raw []byte
	if payload != nil {
		var errMarshal error
		raw, errMarshal = json.Marshal(payload)
		if errMarshal != nil {
			return nil, errMarshal
		}
	}
	respRaw, ok := callHost(method, raw)
	if len(respRaw) == 0 {
		if !ok {
			return nil, fmt.Errorf("host call %s failed", method)
		}
		return nil, nil
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(respRaw, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("host call %s: invalid envelope: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("host call %s: %s: %s", method, env.Error.Code, env.Error.Message)
		}
		return nil, errors.New("host call " + method + " failed")
	}
	return env.Result, nil
}

func hostLog(level, message string, fields map[string]any) {
	payload := map[string]any{
		"level":   level,
		"message": "opencode-go-pool: " + message,
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	_, _ = hostCall(pluginabi.MethodHostLog, payload)
}

func hostHTTPDo(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	raw, errCall := hostCall(pluginabi.MethodHostHTTPDo, req)
	if errCall != nil {
		return pluginapi.HTTPResponse{}, errCall
	}
	var resp pluginapi.HTTPResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HTTPResponse{}, errUnmarshal
	}
	return resp, nil
}
