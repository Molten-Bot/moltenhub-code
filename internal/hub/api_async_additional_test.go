package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAsyncAPIClientRecordGitHubTaskCompleteActivity(t *testing.T) {
	t.Parallel()

	var patchSeen int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer agent-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"metadata":{"activities":["existing"]}}`))
		case r.Method == http.MethodPatch && (r.URL.Path == "/v1/agents/me/metadata" || r.URL.Path == "/v1/agents/me"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			meta, _ := body["metadata"].(map[string]any)
			activities, _ := meta["activities"].([]any)
			found := false
			for _, activity := range activities {
				if strings.TrimSpace(strings.ToLower(activity.(string))) == "github task complete" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("patch activities = %#v, expected github task complete", activities)
			}
			atomic.StoreInt32(&patchSeen, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAsyncAPIClient(server.URL+"/v1", "agent-token")
	if err := client.RecordGitHubTaskCompleteActivity(context.Background()); err != nil {
		t.Fatalf("RecordGitHubTaskCompleteActivity() error = %v", err)
	}
	if atomic.LoadInt32(&patchSeen) != 1 {
		t.Fatal("RecordGitHubTaskCompleteActivity() did not issue metadata patch")
	}
}

func TestAsyncAPIClientRecordGitHubTaskCompleteActivityRequiresToken(t *testing.T) {
	t.Parallel()

	client := NewAsyncAPIClient("https://example.test/v1", "")
	if err := client.RecordGitHubTaskCompleteActivity(context.Background()); err == nil {
		t.Fatal("RecordGitHubTaskCompleteActivity() error = nil, want non-nil")
	}
}

func TestAsyncAPIClientNilReceiverHelpers(t *testing.T) {
	t.Parallel()

	var client *AsyncAPIClient
	if got := client.BaseURL(); got != "" {
		t.Fatalf("BaseURL() = %q, want empty", got)
	}
	if got := client.Token(); got != "" {
		t.Fatalf("Token() = %q, want empty", got)
	}
	if _, err := client.ResolveAgentToken(context.Background(), InitConfig{}); err == nil {
		t.Fatal("ResolveAgentToken() error = nil, want non-nil")
	}
}

func TestAsyncAPIClientTokenBoundMethods(t *testing.T) {
	t.Parallel()

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer agent-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		seen = append(seen, r.Method+" "+r.URL.Path)

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{}}}}`))
			return
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/openclaw/messages/pull"):
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()

	client := NewAsyncAPIClient(server.URL+"/v1/", "agent-token")
	if got, want := client.BaseURL(), strings.TrimSuffix(server.URL+"/v1", "/"); got != want {
		t.Fatalf("BaseURL() = %q, want %q", got, want)
	}
	if err := client.SyncProfile(context.Background(), InitConfig{}); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}
	if err := client.UpdateAgentStatus(context.Background(), "online"); err != nil {
		t.Fatalf("UpdateAgentStatus() error = %v", err)
	}
	if err := client.MarkOpenClawOffline(context.Background(), "main", "harness_shutdown"); err != nil {
		t.Fatalf("MarkOpenClawOffline() error = %v", err)
	}
	if err := client.RegisterRuntime(context.Background(), InitConfig{SessionKey: "main"}, nil); err != nil {
		t.Fatalf("RegisterRuntime() error = %v", err)
	}
	if _, ok, err := client.PullOpenClawMessage(context.Background(), 1000); err != nil || ok {
		t.Fatalf("PullOpenClawMessage() = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if err := client.AckOpenClawDelivery(context.Background(), "delivery-1"); err != nil {
		t.Fatalf("AckOpenClawDelivery() error = %v", err)
	}
	if err := client.NackOpenClawDelivery(context.Background(), "delivery-2"); err != nil {
		t.Fatalf("NackOpenClawDelivery() error = %v", err)
	}

	sort.Strings(seen)
	wantContains := []string{
		"GET /v1/agents/me",
		"GET /v1/openclaw/messages/pull",
		"PATCH /v1/agents/me",
		"PATCH /v1/agents/me/status",
		"POST /v1/openclaw/messages/ack",
		"POST /v1/openclaw/messages/nack",
		"POST /v1/openclaw/messages/offline",
		"PATCH /v1/agents/me/metadata",
	}
	for _, want := range wantContains {
		found := false
		for _, got := range seen {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("request log missing %q; got=%v", want, seen)
		}
	}
}
