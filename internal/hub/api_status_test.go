package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestUpdateAgentStatusUsesPrimaryEndpoint(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var (
		mu    sync.Mutex
		calls []captured
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)

		mu.Lock()
		calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		if r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.UpdateAgentStatus(context.Background(), "token", "online"); err != nil {
		t.Fatalf("UpdateAgentStatus() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPatch {
		t.Fatalf("method = %q", calls[0].Method)
	}
	if calls[0].Path != "/v1/agents/me/status" {
		t.Fatalf("path = %q", calls[0].Path)
	}
	if calls[0].Body["status"] != "online" {
		t.Fatalf("status = %#v", calls[0].Body["status"])
	}
}

func TestUpdateAgentStatusFallsBackToMetadataWrapper(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var (
		mu    sync.Mutex
		calls []captured
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)

		mu.Lock()
		calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		switch r.URL.Path {
		case "/v1/agents/me/status":
			w.WriteHeader(http.StatusNotFound)
		case "/v1/agents/me":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case "/v1/agents/me/metadata":
			if _, ok := body["status"]; ok {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			meta, _ := body["metadata"].(map[string]any)
			if meta["status"] == "offline" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.UpdateAgentStatus(context.Background(), "token", "offline"); err != nil {
		t.Fatalf("UpdateAgentStatus() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 5 {
		t.Fatalf("calls = %d, want 5", len(calls))
	}
	last := calls[len(calls)-1]
	if last.Method != http.MethodPatch {
		t.Fatalf("last method = %q", last.Method)
	}
	if last.Path != "/v1/agents/me/metadata" {
		t.Fatalf("last path = %q", last.Path)
	}
	meta, _ := last.Body["metadata"].(map[string]any)
	if meta["status"] != "offline" {
		t.Fatalf("metadata.status = %#v", meta["status"])
	}
}

func TestUpdateAgentStatusValidation(t *testing.T) {
	t.Parallel()

	client := NewAPIClient("https://na.hub.molten.bot/v1")
	if err := client.UpdateAgentStatus(context.Background(), "", "online"); err == nil {
		t.Fatal("expected token validation error")
	}
	if err := client.UpdateAgentStatus(context.Background(), "token", "busy"); err == nil {
		t.Fatal("expected status validation error")
	}
}

func TestMarkRuntimeOfflineUsesOfflineEndpoint(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var (
		mu    sync.Mutex
		calls []captured
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)

		mu.Lock()
		calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		if r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/messages/offline" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.MarkRuntimeOffline(context.Background(), "token", "main", "harness_shutdown"); err != nil {
		t.Fatalf("MarkRuntimeOffline() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", calls[0].Method)
	}
	if calls[0].Path != "/v1/runtime/messages/offline" {
		t.Fatalf("path = %q", calls[0].Path)
	}
	if calls[0].Body["session_key"] != "main" {
		t.Fatalf("session_key = %#v", calls[0].Body["session_key"])
	}
	if _, ok := calls[0].Body["sessionKey"]; ok {
		t.Fatalf("sessionKey = %#v, want absent", calls[0].Body["sessionKey"])
	}
	if calls[0].Body["reason"] != "harness_shutdown" {
		t.Fatalf("reason = %#v", calls[0].Body["reason"])
	}
}

func TestPingAndHealthUseRootEndpoints(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		paths []string
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/ping", "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got, want := len(paths), 2; got != want {
		t.Fatalf("paths len = %d, want %d", got, want)
	}
	if paths[0] != "/ping" {
		t.Fatalf("first path = %q, want /ping", paths[0])
	}
	if paths[1] != "/health" {
		t.Fatalf("second path = %q, want /health", paths[1])
	}
}

func TestPingIncludesStatusAndBodyOnFailure(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"moltenhub is starting","status":"starting"}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping() error = nil, want non-nil")
	}
	if got := err.Error(); got == "" || got == "<nil>" {
		t.Fatalf("Ping() error = %q, want populated error", got)
	}
	if got := err.Error(); !containsAll(got, "/ping status=503", `{"message":"moltenhub is starting","status":"starting"}`) {
		t.Fatalf("Ping() error = %q, want status and body detail", got)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if part == "" {
			continue
		}
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
