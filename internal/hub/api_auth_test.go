package hub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveAgentToken_UsesConfiguredAgentTokenWhenVerified(t *testing.T) {
	t.Parallel()

	var bindCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			if r.Header.Get("Authorization") == "Bearer agent_saved" {
				w.WriteHeader(http.StatusOK)
				return
			}
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

func TestResolveAgentToken_AgentTokenUnverifiedFallsBackToConfiguredBindToken(t *testing.T) {
	t.Parallel()

	var bindBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens":
			data, _ := io.ReadAll(r.Body)
			bindBodies = append(bindBodies, string(data))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"agent_token":"agent_bound"}`))
		case "/v1/agents/me":
			if r.Header.Get("Authorization") == "Bearer agent_bound" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewAPIClient(server.URL + "/v1")
	token, err := client.ResolveAgentToken(context.Background(), InitConfig{
		AgentToken: "agent_saved",
		BindToken:  "bind_valid",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToken() error = %v", err)
	}
	if token != "agent_bound" {
		t.Fatalf("ResolveAgentToken() token = %q, want %q", token, "agent_bound")
	}
	if len(bindBodies) == 0 {
		t.Fatal("expected bind fallback attempt")
	}
	if !strings.Contains(bindBodies[0], "bind_valid") {
		t.Fatalf("first bind attempt body = %q, want bind token", bindBodies[0])
	}
}

func TestResolveAgentToken_AgentTokenUnverifiedAttemptsAgentTokenAsBindCandidate(t *testing.T) {
	t.Parallel()

	var bindCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens":
			bindCalls++
			data, _ := io.ReadAll(r.Body)
			if strings.Contains(string(data), "agent_saved") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"agent_token":"agent_bound"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/v1/agents/me":
			if r.Header.Get("Authorization") == "Bearer agent_bound" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewAPIClient(server.URL + "/v1")
	token, err := client.ResolveAgentToken(context.Background(), InitConfig{
		AgentToken: "agent_saved",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToken() error = %v", err)
	}
	if token != "agent_bound" {
		t.Fatalf("ResolveAgentToken() token = %q, want %q", token, "agent_bound")
	}
	if bindCalls == 0 {
		t.Fatal("expected bind candidate fallback using agent token")
	}
}

func TestResolveAgentToken_AgentTokenUnverifiedReturnsOriginalTokenWhenFallbackFails(t *testing.T) {
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
		BindToken:  "bind_invalid",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToken() error = %v", err)
	}
	if token != "agent_saved" {
		t.Fatalf("ResolveAgentToken() token = %q, want %q", token, "agent_saved")
	}
	if bindCalls == 0 {
		t.Fatal("expected bind fallback attempts before using original token")
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

func TestBindTokenFlowIgnoresNonMoltenAPIBaseWhenOverrideDisabled(t *testing.T) {
	t.Setenv(allowNonMoltenHubBaseURLEnvName, "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"agent_token":"agent_bound","api_base":"http://127.0.0.1:37581/v1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewAPIClient(server.URL + "/v1")
	token, err := client.bindTokenFlow(context.Background(), "bind_valid")
	if err != nil {
		t.Fatalf("bindTokenFlow() error = %v", err)
	}
	if got, want := token, "agent_bound"; got != want {
		t.Fatalf("bindTokenFlow() token = %q, want %q", got, want)
	}
	if got, want := client.BaseURL, server.URL+"/v1"; got != want {
		t.Fatalf("client.BaseURL = %q, want %q", got, want)
	}
}

func TestBindTokenFlowCanonicalizesMoltenAPIBaseWhenOverrideDisabled(t *testing.T) {
	t.Setenv(allowNonMoltenHubBaseURLEnvName, "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"agent_token":"agent_bound","api_base":"https://eu.hub.molten.bot/v1/"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewAPIClient(server.URL + "/v1")
	token, err := client.bindTokenFlow(context.Background(), "bind_valid")
	if err != nil {
		t.Fatalf("bindTokenFlow() error = %v", err)
	}
	if got, want := token, "agent_bound"; got != want {
		t.Fatalf("bindTokenFlow() token = %q, want %q", got, want)
	}
	if got, want := client.BaseURL, "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("client.BaseURL = %q, want %q", got, want)
	}
}
