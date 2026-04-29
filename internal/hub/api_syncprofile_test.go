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

func TestSyncProfileUsesAgentMetadataPayload(t *testing.T) {
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
			ProfileText: "Automation worker",
		},
	}
	cfg.ApplyDefaults()

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Method != http.MethodGet {
		t.Fatalf("first method = %q, want GET", calls[0].Method)
	}
	if calls[0].Path != "/v1/agents/me" {
		t.Fatalf("first path = %q, want /v1/agents/me", calls[0].Path)
	}
	if calls[1].Method != http.MethodPatch {
		t.Fatalf("second method = %q, want PATCH", calls[1].Method)
	}
	if calls[1].Path != "/v1/agents/me/metadata" {
		t.Fatalf("second path = %q, want /v1/agents/me/metadata", calls[1].Path)
	}
	if got := calls[1].Body["handle"]; got != "moltenhub-code" {
		t.Fatalf("handle = %#v", got)
	}

	metadataRaw, ok := calls[1].Body["metadata"]
	if !ok {
		t.Fatalf("metadata missing from payload: %#v", calls[1].Body)
	}
	metadata, ok := metadataRaw.(map[string]any)
	if !ok {
		t.Fatalf("metadata has wrong type: %#v", metadataRaw)
	}
	if got := metadata["display_name"]; got != "MoltenHub Code" {
		t.Fatalf("metadata.display_name = %#v", got)
	}
	if got := metadata["emoji"]; got != "🎮" {
		t.Fatalf("metadata.emoji = %#v", got)
	}
	if got := metadata["profile"]; got != "Automation worker" {
		t.Fatalf("metadata.profile = %#v", got)
	}
	if got := metadata["profile_markdown"]; got != "Automation worker" {
		t.Fatalf("metadata.profile_markdown = %#v", got)
	}
	if got := metadata["agent_harness"]; got != "codex" {
		t.Fatalf("metadata.agent_harness = %#v", got)
	}
	if got := metadata["llm"]; got != "Codex" {
		t.Fatalf("metadata.llm = %#v", got)
	}
	if got := metadata["harness"]; got != runtimeIdentifier+"@v1" {
		t.Fatalf("metadata.harness = %#v", got)
	}
	skills, ok := metadata["skills"].([]any)
	if !ok || len(skills) != 2 {
		t.Fatalf("metadata.skills = %#v", metadata["skills"])
	}
	firstSkill, ok := skills[0].(map[string]any)
	if !ok {
		t.Fatalf("metadata.skills[0] = %#v, want map[string]any", skills[0])
	}
	if got := strings.TrimSpace(firstSkill["name"].(string)); got != "code_for_me" {
		t.Fatalf("metadata.skills[0].name = %q, want code_for_me", got)
	}
	secondSkill, ok := skills[1].(map[string]any)
	if !ok {
		t.Fatalf("metadata.skills[1] = %#v, want map[string]any", skills[1])
	}
	if got := strings.TrimSpace(secondSkill["name"].(string)); got != "code_review" {
		t.Fatalf("metadata.skills[1].name = %q, want code_review", got)
	}
}

func TestSyncProfileClearsMetadataEmojiWhenProfileEmojiEmpty(t *testing.T) {
	t.Parallel()

	var metadataPatch map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(data, &body)

		switch {
		case r.Method == http.MethodPatch && (r.URL.Path == "/v1/agents/me" || r.URL.Path == "/v1/agents/me/metadata"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"existing":"keep","emoji":"🔥","display_name":"Old","profile":"Old bio","profile_markdown":"# Old"}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false}`))
		}

		if r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/metadata" {
			metadataPatch = body
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	cfg := InitConfig{
		AgentHarness: "codex",
		Profile: ProfileConfig{
			Emoji: "",
		},
	}
	cfg.ApplyDefaults()

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}
	if metadataPatch == nil {
		t.Fatal("metadata patch was not captured")
	}

	metadataRaw, ok := metadataPatch["metadata"]
	if !ok {
		t.Fatalf("metadata missing from patch body: %#v", metadataPatch)
	}
	metadata, ok := metadataRaw.(map[string]any)
	if !ok {
		t.Fatalf("metadata type = %T, want map[string]any", metadataRaw)
	}
	if _, hasEmoji := metadata["emoji"]; hasEmoji {
		t.Fatalf("metadata.emoji should be unset when profile emoji is empty: %#v", metadata["emoji"])
	}
	if got := metadata["existing"]; got != "keep" {
		t.Fatalf("metadata.existing = %#v, want preserved \"keep\"", got)
	}
}

func TestSyncProfileRetriesWithoutHandleWhenHandleUpdateFails(t *testing.T) {
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
		body := map[string]any{}
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
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
			ProfileText: "Owns automation",
		},
	}

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	if calls[0].Method != http.MethodGet || calls[0].Path != "/v1/agents/me" {
		t.Fatalf("first call = %s %s, want GET /v1/agents/me", calls[0].Method, calls[0].Path)
	}
	if _, hasHandle := calls[1].Body["handle"]; !hasHandle {
		t.Fatalf("second request should include handle: %#v", calls[1].Body)
	}
	if calls[1].Path != "/v1/agents/me/metadata" {
		t.Fatalf("second path = %q, want /v1/agents/me/metadata", calls[1].Path)
	}
	if _, hasHandle := calls[2].Body["handle"]; !hasHandle {
		t.Fatalf("third request should include handle for /agents/me alias retry: %#v", calls[2].Body)
	}
	if calls[2].Path != "/v1/agents/me" {
		t.Fatalf("third path = %q, want /v1/agents/me", calls[2].Path)
	}
	if _, hasHandle := calls[3].Body["handle"]; hasHandle {
		t.Fatalf("final retry request should omit handle: %#v", calls[3].Body)
	}
	if calls[3].Path != "/v1/agents/me/metadata" {
		t.Fatalf("final retry path = %q, want /v1/agents/me/metadata", calls[3].Path)
	}
	metadataRaw, ok := calls[3].Body["metadata"]
	if !ok {
		t.Fatalf("final retry request missing metadata wrapper: %#v", calls[3].Body)
	}
	metadata, ok := metadataRaw.(map[string]any)
	if !ok {
		t.Fatalf("final retry metadata type = %T, want map[string]any", metadataRaw)
	}
	if got := metadata["llm"]; got != "Claude" {
		t.Fatalf("metadata.llm = %#v, want Claude", got)
	}
}
