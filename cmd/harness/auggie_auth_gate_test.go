package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/hub"
)

func TestNewAuggieAuthGateRequiresConfigureWhenMissingSession(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, "")
	g := newAuggieAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Required || status.Ready {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.Harness, agentruntime.HarnessAuggie; got != want {
		t.Fatalf("Harness = %q, want %q", got, want)
	}
	if got, want := status.State, "needs_configure"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := status.ConfigureCommand, auggieConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	if got, want := status.ConfigurePlaceholder, auggieConfigurePlaceholderValue; got != want {
		t.Fatalf("ConfigurePlaceholder = %q, want %q", got, want)
	}
}

func TestNewAuggieAuthGateReadyWhenEnvironmentAlreadyConfigured(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, `{"accessToken":"token_env","tenantURL":"https://tenant.example/","scopes":["email"]}`)
	g := newAuggieAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
}

func TestAuggieAuthGateConfigurePersistsRuntimeConfigAndEnvironment(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newAuggieAuthGate(path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_token",
		AgentHarness: agentruntime.HarnessAuggie,
	})

	input := `{"accessToken":"token_saved","tenantURL":"https://tenant.example/","scopes":["email"]}`
	expected, err := normalizeAuggieSessionAuth(input)
	if err != nil {
		t.Fatalf("normalizeAuggieSessionAuth() error = %v", err)
	}

	status, err := g.Configure(context.Background(), input)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv(auggieSessionAuthEnv), expected; got != want {
		t.Fatalf("%s = %q, want %q", auggieSessionAuthEnv, got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["augment_session_auth"], expected; got != want {
		t.Fatalf("augment_session_auth = %#v, want %q", got, want)
	}
}

func TestAuggieAuthGateConfigureRejectsInvalidSchema(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, "")

	g := newAuggieAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})
	if _, err := g.Configure(context.Background(), `{"accessToken":"token_saved","tenantURL":"https://tenant.example/","scopes":["profile"]}`); err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
}
