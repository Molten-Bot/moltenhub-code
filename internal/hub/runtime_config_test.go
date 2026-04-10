package hub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveRuntimeConfigWritesExpectedShape(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	err := SaveRuntimeConfig(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		BindToken:    "bind_123",
		AgentHarness: "codex",
		SessionKey:   "main",
		Profile: ProfileConfig{
			DisplayName: "Molten Bot",
			Emoji:       "🤙🏻",
			ProfileText: "Lightspeed is trailing behind my commit velocity",
		},
		GitHubToken:  "ghp_secret",
		OpenAIAPIKey: "sk-secret",
		Dispatcher: DispatcherConfig{
			MaxParallel:            3,
			MinParallel:            1,
			SampleWindow:           5,
			SampleIntervalMS:       1500,
			CPUHighWatermark:       85,
			MemoryHighWatermark:    90,
			DiskIOHighWatermarkMBs: 120,
		},
	}, "agent_123")
	if err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var got RuntimeConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got.BaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.BindToken != "bind_123" {
		t.Fatalf("BindToken = %q", got.BindToken)
	}
	if got.AgentToken != "agent_123" {
		t.Fatalf("AgentToken = %q", got.AgentToken)
	}
	if got.SessionKey != "main" {
		t.Fatalf("SessionKey = %q", got.SessionKey)
	}
	if got.TimeoutMs != 20000 {
		t.Fatalf("TimeoutMs = %d", got.TimeoutMs)
	}
	if got.Profile.DisplayName != "Molten Bot" {
		t.Fatalf("Profile.DisplayName = %q", got.Profile.DisplayName)
	}
	if got.Profile.LLM != "codex" {
		t.Fatalf("Profile.LLM = %q, want codex", got.Profile.LLM)
	}
	if got.Profile.Harness != runtimeIdentifier {
		t.Fatalf("Profile.Harness = %q, want %q", got.Profile.Harness, runtimeIdentifier)
	}
	if len(got.Profile.Skills) != 3 || got.Profile.Skills[0] != "code_for_me" || got.Profile.Skills[1] != "code_review" || got.Profile.Skills[2] != "library_task" {
		t.Fatalf("Profile.Skills = %#v, want [code_for_me code_review library_task]", got.Profile.Skills)
	}
	if got.GitHubToken != "ghp_secret" {
		t.Fatalf("GitHubToken = %q", got.GitHubToken)
	}
	if got.OpenAIAPIKey != "sk-secret" {
		t.Fatalf("OpenAIAPIKey = %q", got.OpenAIAPIKey)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %#o, want 0600", st.Mode().Perm())
	}
}

func TestSaveRuntimeConfigDefaultsSessionKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	err := SaveRuntimeConfig(path, InitConfig{
		BaseURL: "https://na.hub.molten.bot/v1",
	}, "agent_123")
	if err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var got RuntimeConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got.SessionKey != "main" {
		t.Fatalf("SessionKey = %q, want main", got.SessionKey)
	}
}

func TestSaveRuntimeConfigDefaultsBaseURLAndRequiresToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	if err := SaveRuntimeConfig(path, InitConfig{}, "agent_123"); err != nil {
		t.Fatalf("SaveRuntimeConfig() with default base URL error = %v", err)
	}
	if err := SaveRuntimeConfig(path, InitConfig{BaseURL: "https://na.hub.molten.bot/v1"}, ""); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestLoadRuntimeConfigRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	if err := SaveRuntimeConfig(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "codex",
	}, "agent_abc"); err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.BaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.AgentToken != "agent_abc" {
		t.Fatalf("AgentToken = %q", got.AgentToken)
	}
	if got.SessionKey != "main" {
		t.Fatalf("SessionKey = %q", got.SessionKey)
	}
	if got.TimeoutMs != 20000 {
		t.Fatalf("TimeoutMs = %d", got.TimeoutMs)
	}
}

func TestLoadRuntimeConfigDefaultsOptionalFieldsWhenMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")
	data := `{"baseUrl":"https://na.hub.molten.bot/v1","token":"agent_optional"}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.SessionKey != "main" {
		t.Fatalf("SessionKey = %q, want main", got.SessionKey)
	}
	if got.TimeoutMs != runtimeTimeoutMs {
		t.Fatalf("TimeoutMs = %d, want %d", got.TimeoutMs, runtimeTimeoutMs)
	}
}

func TestLoadRuntimeConfigMissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadRuntimeConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

func TestLoadRuntimeConfigDefaultsOptionalSessionKeyAndTimeout(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")
	data := `{"baseUrl":"https://na.hub.molten.bot/v1","token":"agent_123"}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.SessionKey != runtimeSessionKey {
		t.Fatalf("SessionKey = %q, want %q", got.SessionKey, runtimeSessionKey)
	}
	if got.TimeoutMs != runtimeTimeoutMs {
		t.Fatalf("TimeoutMs = %d, want %d", got.TimeoutMs, runtimeTimeoutMs)
	}
}

func TestLoadRuntimeConfigAllowsMissingToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")
	data := `{"baseUrl":"https://na.hub.molten.bot/v1","sessionKey":"main","timeoutMs":20000}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.AgentToken != "" {
		t.Fatalf("AgentToken = %q, want empty", got.AgentToken)
	}
	if got.BindToken != "" {
		t.Fatalf("BindToken = %q, want empty", got.BindToken)
	}
}

func TestIncrementRuntimeConfigLibraryTaskUsagePersistsAndMergesCounts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := IncrementRuntimeConfigLibraryTaskUsage(path, InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		SessionKey: "main",
	}, "unit-test-coverage"); err != nil {
		t.Fatalf("IncrementRuntimeConfigLibraryTaskUsage(first) error = %v", err)
	}
	if err := IncrementRuntimeConfigLibraryTaskUsage(path, InitConfig{}, "unit-test-coverage"); err != nil {
		t.Fatalf("IncrementRuntimeConfigLibraryTaskUsage(second) error = %v", err)
	}
	if err := IncrementRuntimeConfigLibraryTaskUsage(path, InitConfig{}, "security-review"); err != nil {
		t.Fatalf("IncrementRuntimeConfigLibraryTaskUsage(third) error = %v", err)
	}

	got := ReadRuntimeConfigLibraryTaskUsage(path)
	want := map[string]int{
		"unit-test-coverage": 2,
		"security-review":    1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadRuntimeConfigLibraryTaskUsage() = %#v, want %#v", got, want)
	}
}

func TestSaveRuntimeConfigPreservesLibraryTaskUsage(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{
  "base_url": "https://na.hub.molten.bot/v1",
  "session_key": "main",
  "library_task_usage": {
    "unit-test-coverage": 3
  }
}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := SaveRuntimeConfig(path, InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		SessionKey: "main",
	}, "agent_123"); err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}

	got := ReadRuntimeConfigLibraryTaskUsage(path)
	want := map[string]int{"unit-test-coverage": 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadRuntimeConfigLibraryTaskUsage() after SaveRuntimeConfig = %#v, want %#v", got, want)
	}
}

func TestLoadRuntimeConfigSupportsInitStyleWholeConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")
	data := `{
  "base_url": "https://na.hub.molten.bot/v1",
  "bind_token": "bind_saved",
  "agent_token": "agent_saved",
  "agent_harness": "codex",
  "session_key": "main",
  "github_token": "ghp_saved",
  "openai_api_key": "sk_saved",
  "augment_session_auth": "{\"accessToken\":\"token_saved\",\"tenantURL\":\"https://tenant.example\"}",
  "profile": {
    "display_name": "moltenbot000 hub coder",
    "emoji": "🤙🏻",
    "profile": "Lightspeed is trailing behind my commit velocity"
  },
  "dispatcher": {
    "max_parallel": 4
  }
}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.BindToken != "bind_saved" {
		t.Fatalf("BindToken = %q", got.BindToken)
	}
	if got.AgentToken != "agent_saved" {
		t.Fatalf("AgentToken = %q", got.AgentToken)
	}
	if got.Profile.DisplayName != "moltenbot000 hub coder" {
		t.Fatalf("Profile.DisplayName = %q", got.Profile.DisplayName)
	}
	if got.GitHubToken != "ghp_saved" {
		t.Fatalf("GitHubToken = %q", got.GitHubToken)
	}
	if got.OpenAIAPIKey != "sk_saved" {
		t.Fatalf("OpenAIAPIKey = %q", got.OpenAIAPIKey)
	}
	if got.AugmentSessionAuth != "{\"accessToken\":\"token_saved\",\"tenantURL\":\"https://tenant.example\"}" {
		t.Fatalf("AugmentSessionAuth = %q", got.AugmentSessionAuth)
	}
	if got.Dispatcher.MaxParallel != 4 {
		t.Fatalf("Dispatcher.MaxParallel = %d", got.Dispatcher.MaxParallel)
	}
}

func TestSaveRuntimeConfigClaudeOAuthTokenPersistsValue(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := SaveRuntimeConfigClaudeOAuthToken(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "claude",
	}, "oauth_token_saved"); err != nil {
		t.Fatalf("SaveRuntimeConfigClaudeOAuthToken() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got, want := doc["claude_code_oauth_token"], "oauth_token_saved"; got != want {
		t.Fatalf("claude_code_oauth_token = %#v, want %q", got, want)
	}
}

func TestDefaultRuntimeConfigPath(t *testing.T) {
	t.Setenv(runtimeConfigPathEnv, "")

	if got := defaultRuntimeConfigPath(); got != runtimeConfigPath {
		t.Fatalf("defaultRuntimeConfigPath() = %q, want %q", got, runtimeConfigPath)
	}
}

func TestRuntimeConfigCandidatePathsDefaultIncludesLegacyLocation(t *testing.T) {
	t.Parallel()

	got := runtimeConfigCandidatePaths("")
	want := []string{runtimeConfigPath, legacyRuntimeConfigPath}
	if len(got) != len(want) {
		t.Fatalf("len(runtimeConfigCandidatePaths()) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runtimeConfigCandidatePaths()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveRuntimeConfigPathUsesInitSiblingDirectory(t *testing.T) {
	t.Setenv(runtimeConfigPathEnv, "")

	got := ResolveRuntimeConfigPath("/workspace/init.json")
	want := "/workspace/config.json"
	if got != want {
		t.Fatalf("ResolveRuntimeConfigPath() = %q, want %q", got, want)
	}
}

func TestResolveRuntimeConfigPathPrefersEnvOverride(t *testing.T) {
	t.Setenv(runtimeConfigPathEnv, "/custom/runtime.json")

	got := ResolveRuntimeConfigPath("/workspace/init.json")
	if got != "/custom/runtime.json" {
		t.Fatalf("ResolveRuntimeConfigPath() = %q, want %q", got, "/custom/runtime.json")
	}
}

func TestRuntimeConfigCandidatePathsCustomPathIncludesLegacySibling(t *testing.T) {
	t.Parallel()

	got := runtimeConfigCandidatePaths("/workspace/config.json")
	want := []string{"/workspace/config.json", "/workspace/config/config.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtimeConfigCandidatePaths() = %v, want %v", got, want)
	}
}
