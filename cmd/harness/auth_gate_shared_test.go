package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

type sharedAuthGateRunnerStub struct {
	run func(context.Context, execx.Command) (execx.Result, error)
}

func (s *sharedAuthGateRunnerStub) Run(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	if s.run == nil {
		return execx.Result{}, nil
	}
	return s.run(ctx, cmd)
}

func TestFirstConfiguredGitHubTokenPrefersRuntimeConfig(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_env_token")
	t.Setenv("GITHUB_TOKEN", "ghp_env_token_alt")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"github_token":"ghp_runtime_token"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	got, source := firstConfiguredGitHubToken(path, hub.InitConfig{GitHubToken: "ghp_init_token"})
	if want := "ghp_runtime_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "runtime config"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestFirstConfiguredGitHubTokenPrefersInitConfigOverEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_env_token")
	t.Setenv("GITHUB_TOKEN", "ghp_env_token_alt")

	got, source := firstConfiguredGitHubToken(filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{GitHubToken: "ghp_init_token"})
	if want := "ghp_init_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "init config"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestFirstConfiguredGitHubTokenFallsBackToGHAndGITHUBEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_env_token")
	t.Setenv("GITHUB_TOKEN", "ghp_env_token_alt")

	got, source := firstConfiguredGitHubToken(filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{})
	if want := "ghp_env_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "environment"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}

	t.Setenv("GH_TOKEN", "")
	got, source = firstConfiguredGitHubToken(filepath.Join(t.TempDir(), "missing-2.json"), hub.InitConfig{})
	if want := "ghp_env_token_alt"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "environment"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestConfigurableAgentAuthStateSharedTransitions(t *testing.T) {
	var state configurableAgentAuthState
	options := []hubui.AgentAuthOption{{Value: "OPENAI_API_KEY", Label: "OpenAI"}}

	state.setConfigureUI(" paste token ", " command ", " placeholder ", options)
	options[0].Label = "mutated"
	if !state.required || state.ready || state.state != "needs_configure" {
		t.Fatalf("setConfigureUI() state = %+v", state)
	}
	if state.message != "paste token" || state.configureCommand != "command" || state.configurePlaceholder != "placeholder" {
		t.Fatalf("setConfigureUI() config fields = %+v", state)
	}
	if got := state.configureOptions[0].Label; got != "OpenAI" {
		t.Fatalf("setConfigureUI() option label = %q, want copied OpenAI", got)
	}

	state.setReady(" ready ")
	if !state.required || !state.ready || state.state != "ready" || state.message != "ready" {
		t.Fatalf("setReady() state = %+v", state)
	}
	if state.configureCommand != "" || state.configurePlaceholder != "" || state.configureOptions != nil {
		t.Fatalf("setReady() configure fields = %+v", state)
	}
}

func TestApplyGitHubTokenRequirementStateConfiguresPromptWhenMissing(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	var state configurableAgentAuthState
	token, blocked := applyGitHubTokenRequirementState(context.Background(), nil, &state, "test", filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{})
	if token != "" || !blocked {
		t.Fatalf("applyGitHubTokenRequirementState() = token %q blocked %v, want empty true", token, blocked)
	}
	snapshot := state.snapshot("test")
	if snapshot.Ready || snapshot.State != "needs_configure" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got, want := snapshot.ConfigureCommand, claudeGitHubConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
}

func TestGitHubTokenRequirementStateAcceptsValidatedStartupToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, "gh"; got != want {
				t.Fatalf("command = %q, want %q", got, want)
			}
			if got, want := strings.Join(cmd.Args, " "), "auth status"; got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}

	blocked, state := githubTokenRequirementState(context.Background(), runner, "test", filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{GitHubToken: "ghp_valid"})
	if blocked {
		t.Fatalf("githubTokenRequirementState() blocked = true, state = %+v", state)
	}
	if got, want := os.Getenv("GH_TOKEN"), "ghp_valid"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "ghp_valid"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}
}

func TestGitHubTokenRequirementStateRejectsInvalidStartupToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := strings.Join(cmd.Args, " "), "auth status"; got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			return execx.Result{Stderr: "bad credentials"}, errors.New("token invalid")
		},
	}

	var state configurableAgentAuthState
	token, blocked := applyGitHubTokenRequirementState(context.Background(), runner, &state, "test", filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{GitHubToken: "ghp_invalid"})
	if token != "" || !blocked {
		t.Fatalf("applyGitHubTokenRequirementState() = token %q blocked %v, want empty true", token, blocked)
	}
	snapshot := state.snapshot("test")
	if snapshot.Ready || snapshot.State != "needs_configure" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !strings.Contains(snapshot.Message, "validate github token") {
		t.Fatalf("message = %q, want validation failure", snapshot.Message)
	}
	if got := os.Getenv("GH_TOKEN"); got != "" {
		t.Fatalf("GH_TOKEN = %q, want empty", got)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "" {
		t.Fatalf("GITHUB_TOKEN = %q, want empty", got)
	}
}

func TestDecodeJSONStrictOrWrappedString(t *testing.T) {
	var payload struct {
		Value string `json:"value"`
	}
	if err := decodeJSONStrictOrWrappedString(`"{\"value\":\"ok\"}"`, &payload); err != nil {
		t.Fatalf("decodeJSONStrictOrWrappedString(wrapped) error = %v", err)
	}
	if payload.Value != "ok" {
		t.Fatalf("decodeJSONStrictOrWrappedString(wrapped) value = %q, want ok", payload.Value)
	}

	if err := decodeJSONStrictOrWrappedString(`{"value":"ok","extra":true}`, &payload); err == nil {
		t.Fatal("decodeJSONStrictOrWrappedString(unknown field) error = nil, want non-nil")
	}
}

func TestDecodeJSONOrWrappedString(t *testing.T) {
	var decoded struct {
		Value string `json:"value"`
	}
	if err := decodeJSONOrWrappedString(`"{\"value\":\"ok\"}"`, &decoded); err != nil {
		t.Fatalf("decodeJSONOrWrappedString() error = %v", err)
	}
	if got, want := decoded.Value, "ok"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}
