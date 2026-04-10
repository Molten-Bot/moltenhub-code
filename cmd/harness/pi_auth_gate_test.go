package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
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
	if got, want := status.Message, "Select a PI provider, and supply the token."; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got, want := status.ConfigurePlaceholder, "Paste provider token..."; got != want {
		t.Fatalf("ConfigurePlaceholder = %q, want %q", got, want)
	}
}

func TestNewPiAuthGateReadyWhenEnvironmentAlreadyConfigured(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runner := &authGateRunnerStub{}
	g := newPiAuthGateWithRuntime(runner, "pi", filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{}, nil)

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestPiAuthGateConfigurePersistsRuntimeConfigAndEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_token",
		AgentHarness: agentruntime.HarnessPi,
	}, nil)

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
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestPiAuthGateConfigureRejectsUnsupportedEnvVar(t *testing.T) {
	g := newPiAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	if _, err := g.Configure(context.Background(), `{"env_var":"UNSUPPORTED_ENV","value":"x"}`); err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
}

func TestPiAuthGateConfigureReturnsLaunchFailureDetails(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("pi login failed")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	status, err := g.Configure(context.Background(), `{"env_var":"OPENAI_API_KEY","value":"sk-fail"}`)
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "pi login failed") {
		t.Fatalf("Configure() error = %q, want launch failure detail", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "launch pi with OPENAI_API_KEY") {
		t.Fatalf("status.Message = %q, want launch detail", status.Message)
	}
	if got, want := os.Getenv("OPENAI_API_KEY"), "sk-fail"; got != want {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", got, want)
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := doc["pi_provider_auth"]; got == nil || got == "" {
		t.Fatalf("pi_provider_auth = %#v, want persisted value", got)
	}
}
