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

func TestSyncProfileUsesOpenAPICompatiblePayloads(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	cfg := InitConfig{
		Handle: "moltenhub-code",
		Profile: ProfileConfig{
			DisplayName: "MoltenHub Code",
			Emoji:       "🎮",
			Bio:         "Automation worker",
		},
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}

	if calls[0].Method != http.MethodPost {
		t.Fatalf("first method = %q", calls[0].Method)
	}
	if calls[0].Path != "/v1/agents/me/metadata" {
		t.Fatalf("first path = %q", calls[0].Path)
	}
	if _, ok := calls[0].Body["handle"]; !ok {
		t.Fatalf("first body missing handle: %#v", calls[0].Body)
	}

	if calls[1].Method != http.MethodPost {
		t.Fatalf("second method = %q", calls[1].Method)
	}
	if calls[1].Path != "/v1/agents/me/metadata" {
		t.Fatalf("second path = %q", calls[1].Path)
	}
	metaRaw, ok := calls[1].Body["metadata"]
	if !ok {
		t.Fatalf("second body missing metadata wrapper: %#v", calls[1].Body)
	}
	meta, ok := metaRaw.(map[string]any)
	if !ok {
		t.Fatalf("metadata has wrong type: %#v", metaRaw)
	}

	if got := meta["display_name"]; got != "MoltenHub Code" {
		t.Fatalf("display_name = %#v", got)
	}
	if got := meta["emoji"]; got != "🎮" {
		t.Fatalf("emoji = %#v", got)
	}
	if got := meta["bio"]; got != "Automation worker" {
		t.Fatalf("bio = %#v", got)
	}
	if got := meta["public"]; got != true {
		t.Fatalf("public = %#v", got)
	}
	if got := meta["is_public"]; got != true {
		t.Fatalf("is_public = %#v", got)
	}
	if got := meta["visibility"]; got != "public" {
		t.Fatalf("visibility = %#v", got)
	}
	markdown, _ := meta["profile_markdown"].(string)
	if !strings.Contains(markdown, "🎮") {
		t.Fatalf("profile_markdown missing emoji: %q", markdown)
	}

	skillsRaw, ok := meta["skills"]
	if !ok {
		t.Fatalf("metadata missing skills: %#v", meta)
	}
	skills, ok := skillsRaw.([]any)
	if !ok || len(skills) == 0 {
		t.Fatalf("skills has wrong type/value: %#v", skillsRaw)
	}
	skill, ok := skills[0].(map[string]any)
	if !ok {
		t.Fatalf("skill has wrong type: %#v", skills[0])
	}
	if skill["name"] != "code_for_me" {
		t.Fatalf("skill name = %#v", skill["name"])
	}
	if _, ok := skill["description"]; !ok {
		t.Fatalf("skill missing description: %#v", skill)
	}
	if _, ok := skill["dispatch_type"]; ok {
		t.Fatalf("skill should not include dispatch_type: %#v", skill)
	}
	if _, ok := skill["result_type"]; ok {
		t.Fatalf("skill should not include result_type: %#v", skill)
	}
}

func TestSyncProfileIgnoresProfileMetadataOverridesFromInit(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	cfg := InitConfig{
		Profile: ProfileConfig{
			Metadata: map[string]any{
				"public":     false,
				"is_public":  false,
				"visibility": "private",
				"agent_type": "override",
			},
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
	if calls[0].Method != http.MethodPost {
		t.Fatalf("method = %q", calls[0].Method)
	}
	if calls[0].Path != "/v1/agents/me/metadata" {
		t.Fatalf("path = %q", calls[0].Path)
	}

	meta, ok := calls[0].Body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata wrapper missing: %#v", calls[0].Body)
	}
	if got := meta["public"]; got != true {
		t.Fatalf("public = %#v", got)
	}
	if got := meta["is_public"]; got != true {
		t.Fatalf("is_public = %#v", got)
	}
	if got := meta["visibility"]; got != "public" {
		t.Fatalf("visibility = %#v", got)
	}
	if got := meta["agent_type"]; got != runtimeIdentifier {
		t.Fatalf("agent_type = %#v", got)
	}

	skills, ok := meta["skills"].([]any)
	if !ok || len(skills) == 0 {
		t.Fatalf("skills missing: %#v", meta["skills"])
	}
	skill, ok := skills[0].(map[string]any)
	if !ok {
		t.Fatalf("skill wrong type: %#v", skills[0])
	}
	if skill["name"] != "code_for_me" {
		t.Fatalf("skill name = %#v", skill["name"])
	}
	if _, ok := skill["description"]; !ok {
		t.Fatalf("skill missing description: %#v", skill)
	}
}

func TestSyncProfileForcesPublicVisibilityMetadata(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	cfg := InitConfig{
		Profile: ProfileConfig{
			Metadata: map[string]any{
				"public":     false,
				"is_public":  false,
				"visibility": "private",
			},
		},
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	if err := client.SyncProfile(context.Background(), "token", cfg); err != nil {
		t.Fatalf("SyncProfile() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPost {
		t.Fatalf("method = %q", calls[0].Method)
	}

	meta, ok := calls[0].Body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata wrapper missing: %#v", calls[0].Body)
	}
	if got := meta["public"]; got != true {
		t.Fatalf("public = %#v", got)
	}
	if got := meta["is_public"]; got != true {
		t.Fatalf("is_public = %#v", got)
	}
	if got := meta["visibility"]; got != "public" {
		t.Fatalf("visibility = %#v", got)
	}
}

func TestNormalizeSkillName(t *testing.T) {
	t.Parallel()

	if got := normalizeSkillName("MoltenHub Code Run!!"); got != "moltenhub-code-run" {
		t.Fatalf("normalizeSkillName() = %q", got)
	}
	if got := normalizeSkillName("@"); got != "code_for_me" {
		t.Fatalf("normalizeSkillName() fallback = %q", got)
	}
}
