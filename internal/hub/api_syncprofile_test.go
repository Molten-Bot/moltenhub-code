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

func TestSyncProfileUsesAgentProfilePayload(t *testing.T) {
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

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	cfg := InitConfig{
		AgentHarness: "codex",
		Handle:       "moltenhub-code",
		Profile: ProfileConfig{
			DisplayName: "MoltenHub Code",
			Emoji:       "🎮",
			Bio:         "Automation worker",
		},
	}
	cfg.ApplyDefaults()

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", calls[0].Method)
	}
	if calls[0].Path != "/v1/agents/me" {
		t.Fatalf("path = %q, want /v1/agents/me", calls[0].Path)
	}
	if got := calls[0].Body["handle"]; got != "moltenhub-code" {
		t.Fatalf("handle = %#v", got)
	}

	profileRaw, ok := calls[0].Body["profile"]
	if !ok {
		t.Fatalf("profile missing from payload: %#v", calls[0].Body)
	}
	profile, ok := profileRaw.(map[string]any)
	if !ok {
		t.Fatalf("profile has wrong type: %#v", profileRaw)
	}
	if got := profile["display_name"]; got != "MoltenHub Code" {
		t.Fatalf("profile.display_name = %#v", got)
	}
	if got := profile["emoji"]; got != "🎮" {
		t.Fatalf("profile.emoji = %#v", got)
	}
	if got := profile["bio"]; got != "Automation worker" {
		t.Fatalf("profile.bio = %#v", got)
	}
	if got := profile["llm"]; got != "codex" {
		t.Fatalf("profile.llm = %#v", got)
	}
	if got := profile["harness"]; got != runtimeIdentifier {
		t.Fatalf("profile.harness = %#v", got)
	}
	skills, ok := profile["skills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("profile.skills = %#v", profile["skills"])
	}
	if got := strings.TrimSpace(skills[0].(string)); got != "code_for_me" {
		t.Fatalf("profile.skills[0] = %q, want code_for_me", got)
	}
	if _, ok := calls[0].Body["metadata"]; ok {
		t.Fatalf("metadata should not be sent in profile sync payload: %#v", calls[0].Body["metadata"])
	}
}

func TestSyncProfileRetriesWithoutHandleWhenHandleUpdateFails(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		calls []map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		calls = append(calls, body)
		mu.Unlock()

		if _, hasHandle := body["handle"]; hasHandle {
			http.Error(w, `{"error":"handle is immutable"}`, http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	cfg := InitConfig{
		AgentHarness: "claude",
		Handle:       "immutable-handle",
		Profile: ProfileConfig{
			DisplayName: "Existing Agent",
			Emoji:       "🤖",
			Bio:         "Owns automation",
		},
	}

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	if _, hasHandle := calls[0]["handle"]; !hasHandle {
		t.Fatalf("first request should include handle: %#v", calls[0])
	}
	if _, hasHandle := calls[1]["handle"]; !hasHandle {
		t.Fatalf("second request should include handle: %#v", calls[1])
	}
	if _, hasHandle := calls[2]["handle"]; hasHandle {
		t.Fatalf("retry request should omit handle: %#v", calls[2])
	}
}
