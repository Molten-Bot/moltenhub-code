package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
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
