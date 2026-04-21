package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func TestSelectableAgentAuthGateStatusRequiresGitHubTokenWhenUnbound(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	gate := newSelectableAgentAuthGate(context.Background(), nil, hub.InitConfig{RuntimeConfigPath: path}, nil)

	state, err := gate.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := state.State, "needs_configure"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := state.ConfigureCommand, claudeGitHubConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
}

func TestSelectableAgentAuthGateStatusRequiresHarnessSelectionAfterGitHubReady(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_ready_token")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	gate := newSelectableAgentAuthGate(context.Background(), nil, hub.InitConfig{RuntimeConfigPath: path}, nil)

	state, err := gate.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := state.State, agentHarnessSelectionState; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if len(state.ConfigureOptions) == 0 {
		t.Fatal("ConfigureOptions = empty, want harness options")
	}
	gotOrder := make([]string, 0, len(state.ConfigureOptions))
	for _, option := range state.ConfigureOptions {
		gotOrder = append(gotOrder, option.Value)
	}
	wantOrder := []string{"claude", "codex", "pi", "auggie"}
	if len(gotOrder) < len(wantOrder) {
		t.Fatalf("ConfigureOptions count = %d, want at least %d", len(gotOrder), len(wantOrder))
	}
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Fatalf("harness option[%d] = %q, want %q (full=%v)", i, gotOrder[i], want, gotOrder)
		}
	}
}

func TestSelectableAgentAuthGateConfigurePersistsSelectionAndRejectsSwitch(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_ready_token")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	gate := newSelectableAgentAuthGate(context.Background(), nil, hub.InitConfig{RuntimeConfigPath: path}, nil)

	state, err := gate.Configure(context.Background(), "auggie")
	if err != nil {
		t.Fatalf("Configure(auggie) error = %v", err)
	}
	if got, want := state.Harness, "auggie"; got != want {
		t.Fatalf("Harness = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal(config.json) error = %v", err)
	}
	if got, want := doc["agent_harness"], "auggie"; got != want {
		t.Fatalf("agent_harness = %#v, want %q", got, want)
	}
	if got, want := doc["agent_command"], "auggie"; got != want {
		t.Fatalf("agent_command = %#v, want %q", got, want)
	}

	_, err = gate.Configure(context.Background(), "codex")
	if err == nil {
		t.Fatal("Configure(codex) error = nil, want non-nil")
	}
	if got := strings.ToLower(err.Error()); !strings.Contains(got, "already configured") || !strings.Contains(got, "cannot be changed") {
		t.Fatalf("Configure(codex) error = %q, want lock-once guidance", err)
	}
}
