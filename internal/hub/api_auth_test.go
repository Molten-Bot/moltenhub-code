package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveAgentToken_UsesConfiguredAgentTokenAndSkipsBind(t *testing.T) {
	t.Parallel()

	var bindCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.WriteHeader(http.StatusUnauthorized)
		case "/v1/agents/bind", "/v1/agents/bind-tokens":
			bindCalls++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewAPIClient(server.URL + "/v1")
	token, err := client.ResolveAgentToken(context.Background(), InitConfig{
		AgentToken: "agent_saved",
		BindToken:  "bind_unused",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToken() error = %v", err)
	}
	if token != "agent_saved" {
		t.Fatalf("ResolveAgentToken() token = %q, want %q", token, "agent_saved")
	}
	if bindCalls != 0 {
		t.Fatalf("expected bind flow not to run, bindCalls=%d", bindCalls)
	}
}

func TestResolveAgentToken_BindFallbackAllowsUnverifiedToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"agent_token":"agent_bound"}`))
		case "/v1/agents/me":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewAPIClient(server.URL + "/v1")
	token, err := client.ResolveAgentToken(context.Background(), InitConfig{
		BindToken: "bind_valid",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToken() error = %v", err)
	}
	if token != "agent_bound" {
		t.Fatalf("ResolveAgentToken() token = %q, want %q", token, "agent_bound")
	}
}

func TestResolveAgentToken_RequiresSomeCredential(t *testing.T) {
	t.Parallel()

	client := NewAPIClient("https://na.hub.molten.bot/v1")
	_, err := client.ResolveAgentToken(context.Background(), InitConfig{})
	if err == nil {
		t.Fatalf("ResolveAgentToken() expected error")
	}
	if !strings.Contains(err.Error(), "missing bind_token and agent_token") {
		t.Fatalf("ResolveAgentToken() err = %q", err)
	}
}
