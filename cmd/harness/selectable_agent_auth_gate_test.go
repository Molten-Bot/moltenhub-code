package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func selectableGitHubReadyRunner() *sharedAuthGateRunnerStub {
	return &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, "gh"; got != want {
				return execx.Result{}, nil
			}
			if got, want := strings.Join(cmd.Args, " "), "auth status"; got != want {
				return execx.Result{}, nil
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}
}

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

func TestSelectableAgentAuthGateStartVerifyAndErrorState(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	gate := newSelectableAgentAuthGate(context.Background(), nil, hub.InitConfig{RuntimeConfigPath: path}, nil)

	startState, err := gate.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if got, want := startState.State, "needs_configure"; got != want {
		t.Fatalf("StartDeviceAuth() state = %q, want %q", got, want)
	}
	verifyState, err := gate.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got, want := verifyState.State, "needs_configure"; got != want {
		t.Fatalf("Verify() state = %q, want %q", got, want)
	}
	errorState := gate.errorState(" ")
	if got, want := errorState.Message, "agent auth status failed"; got != want {
		t.Fatalf("errorState(empty).Message = %q, want %q", got, want)
	}
}

func TestSelectableAgentAuthGateStatusRequiresHarnessSelectionAfterGitHubReady(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_ready_token")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	gate := newSelectableAgentAuthGate(context.Background(), selectableGitHubReadyRunner(), hub.InitConfig{RuntimeConfigPath: path}, nil)

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

func TestSelectableAgentAuthGateStatusRequiresHarnessSelectionAfterValidatedGITHUBTOKEN(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github_token_ready_token")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, "gh"; got != want {
				t.Fatalf("command = %q, want %q", got, want)
			}
			if got, want := strings.Join(cmd.Args, " "), "auth status"; got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_ready_token"; got != want {
				t.Fatalf("GITHUB_TOKEN = %q, want %q during validation", got, want)
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}
	gate := newSelectableAgentAuthGate(context.Background(), runner, hub.InitConfig{RuntimeConfigPath: path}, nil)

	state, err := gate.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := state.State, agentHarnessSelectionState; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got := state.ConfigureCommand; got != "" {
		t.Fatalf("ConfigureCommand = %q, want empty once GitHub token is ready", got)
	}
}

func TestSelectableAgentAuthGateStatusAcceptsMalformedDockerComposeGitHubEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN:github_token_ready_token", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_ready_token"; got != want {
				t.Fatalf("GITHUB_TOKEN = %q, want %q during validation", got, want)
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}
	gate := newSelectableAgentAuthGate(context.Background(), runner, hub.InitConfig{RuntimeConfigPath: path}, nil)

	state, err := gate.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := state.State, agentHarnessSelectionState; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got := state.ConfigureCommand; got != "" {
		t.Fatalf("ConfigureCommand = %q, want empty once GitHub token is ready", got)
	}
}

func TestSelectableAgentAuthGateConfigurePersistsSelectionAndRejectsSwitch(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_ready_token")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	gate := newSelectableAgentAuthGate(context.Background(), selectableGitHubReadyRunner(), hub.InitConfig{RuntimeConfigPath: path}, nil)

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
