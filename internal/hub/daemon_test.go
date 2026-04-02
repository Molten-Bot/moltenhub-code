package hub

import "testing"

func TestApplyStoredRuntimeConfigOverridesTokens(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		BindToken:  "bind_token",
		SessionKey: "main",
	}
	stored := RuntimeConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		Token:      "agent_saved",
		SessionKey: "saved-session",
	}

	applied := applyStoredRuntimeConfig(&cfg, stored)
	if !applied {
		t.Fatal("applied = false, want true")
	}
	if cfg.AgentToken != "agent_saved" {
		t.Fatalf("AgentToken = %q", cfg.AgentToken)
	}
	if cfg.BindToken != "" {
		t.Fatalf("BindToken = %q, want empty", cfg.BindToken)
	}
	if cfg.SessionKey != "saved-session" {
		t.Fatalf("SessionKey = %q", cfg.SessionKey)
	}
}

func TestApplyStoredRuntimeConfigNoToken(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{BindToken: "bind_token"}
	applied := applyStoredRuntimeConfig(&cfg, RuntimeConfig{Token: ""})
	if applied {
		t.Fatal("applied = true, want false")
	}
	if cfg.BindToken != "bind_token" {
		t.Fatalf("BindToken = %q", cfg.BindToken)
	}
}
