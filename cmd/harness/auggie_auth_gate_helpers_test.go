package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jef/moltenhub-code/internal/hub"
)

func TestLoadPersistedAuggieSessionAuthHandlesAliasesAndInvalidData(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, "")
	if got, source := firstConfiguredAuggieSessionAuth("", hub.InitConfig{}); got != "" || source != "" {
		t.Fatalf("firstConfiguredAuggieSessionAuth(blank) = (%q, %q), want empty", got, source)
	}

	invalidPath := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidPath, []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile(invalid) error = %v", err)
	}
	if got, source := firstConfiguredAuggieSessionAuth(invalidPath, hub.InitConfig{}); got != "" || source != "" {
		t.Fatalf("firstConfiguredAuggieSessionAuth(invalid JSON) = (%q, %q), want empty", got, source)
	}

	validPath := filepath.Join(t.TempDir(), "valid.json")
	if err := os.WriteFile(validPath, []byte(`{"augmentSessionAuth":"session-from-alias"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(valid) error = %v", err)
	}
	if got, source := firstConfiguredAuggieSessionAuth(validPath, hub.InitConfig{}); got != "session-from-alias" || source != "runtime config" {
		t.Fatalf("firstConfiguredAuggieSessionAuth(alias key) = (%q, %q), want runtime config value", got, source)
	}
}

func TestFirstConfiguredAuggieSessionAuthPriority(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, "session-from-env")
	if got, source := firstConfiguredAuggieSessionAuth("", hub.InitConfig{AugmentSessionAuth: "session-from-init"}); got != "session-from-env" || source != "environment" {
		t.Fatalf("firstConfiguredAuggieSessionAuth(env) = (%q, %q)", got, source)
	}

	t.Setenv(auggieSessionAuthEnv, "")
	if got, source := firstConfiguredAuggieSessionAuth("", hub.InitConfig{AugmentSessionAuth: "session-from-init"}); got != "session-from-init" || source != "init config" {
		t.Fatalf("firstConfiguredAuggieSessionAuth(init) = (%q, %q)", got, source)
	}

	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, []byte(`{"augment_session_auth":"session-from-runtime"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime) error = %v", err)
	}
	if got, source := firstConfiguredAuggieSessionAuth(path, hub.InitConfig{}); got != "session-from-runtime" || source != "runtime config" {
		t.Fatalf("firstConfiguredAuggieSessionAuth(runtime) = (%q, %q)", got, source)
	}
}

func TestNormalizeAuggieSessionAuthValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name:    "missing accessToken",
			raw:     `{"tenantURL":"https://tenant.example/","scopes":["email"]}`,
			wantErr: "accessToken is required",
		},
		{
			name:    "missing tenantURL",
			raw:     `{"accessToken":"token","scopes":["email"]}`,
			wantErr: "tenantURL is required",
		},
		{
			name:    "non absolute tenantURL",
			raw:     `{"accessToken":"token","tenantURL":"tenant.example","scopes":["email"]}`,
			wantErr: "tenantURL must be an absolute URL",
		},
		{
			name:    "non https tenantURL",
			raw:     `{"accessToken":"token","tenantURL":"http://tenant.example/","scopes":["email"]}`,
			wantErr: "tenantURL must use https",
		},
		{
			name:    "missing scopes",
			raw:     `{"accessToken":"token","tenantURL":"https://tenant.example/","scopes":[]}`,
			wantErr: "scopes must include at least one value",
		},
		{
			name:    "missing email scope",
			raw:     `{"accessToken":"token","tenantURL":"https://tenant.example/","scopes":["profile"]}`,
			wantErr: `scopes must include "email"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeAuggieSessionAuth(tt.raw); err == nil || err.Error() != tt.wantErr {
				t.Fatalf("normalizeAuggieSessionAuth() error = %v, want %q", err, tt.wantErr)
			}
		})
	}

	canonical, err := normalizeAuggieSessionAuth(`{"accessToken":"token","tenantURL":"https://tenant.example/","scopes":[" email ","profile"]}`)
	if err != nil {
		t.Fatalf("normalizeAuggieSessionAuth(valid) error = %v", err)
	}
	if canonical == "" {
		t.Fatal("normalizeAuggieSessionAuth(valid) returned empty string")
	}
}

func TestAuggieAuthGateConfigureRequiresGitHubTokenWhenNeeded(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, `{"accessToken":"token_env","tenantURL":"https://tenant.example/","scopes":["email"]}`)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	g := newAuggieAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	state, err := g.Configure(context.Background(), "  ")
	if err == nil {
		t.Fatal("Configure(empty github token) error = nil, want non-nil")
	}
	if state.Ready || state.State != "needs_configure" {
		t.Fatalf("Configure(empty github token) state = %+v", state)
	}
}
