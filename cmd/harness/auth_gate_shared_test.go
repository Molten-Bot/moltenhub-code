package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

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
