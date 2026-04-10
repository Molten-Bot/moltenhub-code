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

func TestNewPiAuthGateRequiresConfigureWhenMissingProviderAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	g := newPiAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.Harness, agentruntime.HarnessPi; got != want {
		t.Fatalf("Harness = %q, want %q", got, want)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if len(status.ConfigureOptions) == 0 {
		t.Fatalf("ConfigureOptions = %v, want non-empty", status.ConfigureOptions)
	}
}

func TestNewPiAuthGateReadyWhenEnvironmentAlreadyConfigured(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	g := newPiAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
}

func TestPiAuthGateConfigurePersistsRuntimeConfigAndEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newPiAuthGate(path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_token",
		AgentHarness: agentruntime.HarnessPi,
	})

	input := `{"env_var":"OPENAI_API_KEY","value":"sk-saved"}`
	expected, err := normalizePiProviderAuth(input)
	if err != nil {
		t.Fatalf("normalizePiProviderAuth() error = %v", err)
	}

	status, err := g.Configure(context.Background(), input)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv("OPENAI_API_KEY"), "sk-saved"; got != want {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["pi_provider_auth"], expected; got != want {
		t.Fatalf("pi_provider_auth = %#v, want %q", got, want)
	}
}

func TestPiAuthGateConfigureRejectsUnsupportedEnvVar(t *testing.T) {
	g := newPiAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	if _, err := g.Configure(context.Background(), `{"env_var":"UNSUPPORTED_ENV","value":"x"}`); err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
}
