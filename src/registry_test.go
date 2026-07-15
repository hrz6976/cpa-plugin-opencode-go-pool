package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
)

func TestCustomRegistry(t *testing.T) {
	raw, errRead := os.ReadFile("../registry.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	client := pluginstore.NewClient(server.Client(), server.URL)
	registry, errFetch := client.FetchRegistry(context.Background())
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if len(registry.Plugins) != 1 {
		t.Fatalf("plugins = %d, want 1", len(registry.Plugins))
	}
	plugin := registry.Plugins[0]
	if plugin.ID != pluginID {
		t.Fatalf("plugin id = %q, want %q", plugin.ID, pluginID)
	}
}
