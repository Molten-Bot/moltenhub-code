package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAsyncAPIClientImplementsMoltenHubAPI(t *testing.T) {
	var _ MoltenHubAPI = (*AsyncAPIClient)(nil)
}

func TestAsyncAPIClientStoresBaseURLAndToken(t *testing.T) {
	client := NewAsyncAPIClient(" https://example.test/v1/ ", "  agent-token  ")
	if got, want := client.BaseURL(), "https://example.test/v1"; got != want {
		t.Fatalf("BaseURL() = %q, want %q", got, want)
	}
	if got, want := client.Token(), "agent-token"; got != want {
		t.Fatalf("Token() = %q, want %q", got, want)
	}
}

func TestAsyncAPIClientResolveAgentTokenStoresToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/a2a" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      body["id"],
			"result": map[string]any{
				"name":                "MoltenHub A2A Gateway",
				"description":         "test",
				"version":             "1.0.0",
				"defaultInputModes":   []string{"application/json"},
				"defaultOutputModes":  []string{"application/json"},
				"supportedInterfaces": []any{},
				"capabilities":        map[string]any{},
				"skills":              []any{},
			},
		})
	}))
	defer server.Close()

	client := NewAsyncAPIClient(server.URL+"/v1", "")
	token, err := client.ResolveAgentToken(context.Background(), InitConfig{
		AgentToken: "agent-resolved",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToken() error = %v", err)
	}
	if got, want := token, "agent-resolved"; got != want {
		t.Fatalf("ResolveAgentToken() token = %q, want %q", got, want)
	}
	if got, want := client.Token(), "agent-resolved"; got != want {
		t.Fatalf("Token() = %q, want %q", got, want)
	}
}

func TestAsyncAPIClientAsyncMethodsUseStoredToken(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
		authz []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		authz = append(authz, r.Header.Get("Authorization"))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewAsyncAPIClient(server.URL+"/v1", "agent-token")
	payload := map[string]any{
		"type":       "skill_result",
		"request_id": "req-1",
		"reply_to":   "agent-2",
		"status":     "ok",
	}

	if err := <-client.PublishResultAsync(context.Background(), payload); err != nil {
		t.Fatalf("PublishResultAsync() error = %v", err)
	}
	if err := <-client.AckOpenClawDeliveryAsync(context.Background(), "delivery-1"); err != nil {
		t.Fatalf("AckOpenClawDeliveryAsync() error = %v", err)
	}
	if err := <-client.NackOpenClawDeliveryAsync(context.Background(), "delivery-2"); err != nil {
		t.Fatalf("NackOpenClawDeliveryAsync() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got, want := strings.Join(paths, ","), "/v1/a2a,/v1/openclaw/messages/publish,/v1/openclaw/messages/ack,/v1/openclaw/messages/nack"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	for i, header := range authz {
		if got, want := header, "Bearer agent-token"; got != want {
			t.Fatalf("auth[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestAsyncAPIClientReturnsClearErrorWhenTokenMissing(t *testing.T) {
	client := NewAsyncAPIClient("https://example.test/v1", "")
	err := <-client.PublishResultAsync(context.Background(), map[string]any{"status": "ok"})
	if err == nil {
		t.Fatal("PublishResultAsync() err = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "moltenhub api token is required") {
		t.Fatalf("PublishResultAsync() err = %q", got)
	}
}
