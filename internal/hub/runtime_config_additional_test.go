package hub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
)

func TestRuntimeConfigInitCarriesRuntimeConfigPath(t *testing.T) {
	t.Parallel()

	cfg := RuntimeConfig{
		InitConfig: InitConfig{
			BaseURL:           "https://na.hub.molten.bot/v1",
			AgentToken:        "agent-token",
			RuntimeConfigPath: "/workspace/.moltenhub/config.json",
		},
		TimeoutMs: runtimeTimeoutMs,
	}

	initCfg := cfg.Init()
	if got, want := initCfg.RuntimeConfigPath, cfg.RuntimeConfigPath; got != want {
		t.Fatalf("Init().RuntimeConfigPath = %q, want %q", got, want)
	}
}

func TestDefaultRuntimeConfigPathUsesEnvOverride(t *testing.T) {
	t.Setenv(runtimeConfigPathEnv, "/tmp/runtime-config.json")

	if got, want := defaultRuntimeConfigPath(), "/tmp/runtime-config.json"; got != want {
		t.Fatalf("defaultRuntimeConfigPath() = %q, want %q", got, want)
	}
}

func TestResolveRuntimeConfigPathEmptyInitUsesDefault(t *testing.T) {
	t.Setenv(runtimeConfigPathEnv, "")

	if got, want := ResolveRuntimeConfigPath(""), runtimeConfigPath; got != want {
		t.Fatalf("ResolveRuntimeConfigPath(\"\") = %q, want %q", got, want)
	}
}

func TestLegacyRuntimeConfigPathForVariants(t *testing.T) {
	t.Parallel()

	if got, want := legacyRuntimeConfigPathFor(""), legacyRuntimeConfigPath; got != want {
		t.Fatalf("legacyRuntimeConfigPathFor(empty) = %q, want %q", got, want)
	}
	if got, want := legacyRuntimeConfigPathFor(runtimeConfigPath), legacyRuntimeConfigPath; got != want {
		t.Fatalf("legacyRuntimeConfigPathFor(default) = %q, want %q", got, want)
	}
	if got, want := legacyRuntimeConfigPathFor("/workspace/config.json"), "/workspace/config/config.json"; got != want {
		t.Fatalf("legacyRuntimeConfigPathFor(custom) = %q, want %q", got, want)
	}
}

func TestRuntimeConfigValidateRejectsInvalidTimeout(t *testing.T) {
	t.Parallel()

	cfg := RuntimeConfig{
		InitConfig: InitConfig{
			BaseURL:    "https://na.hub.molten.bot/v1",
			AgentToken: "agent-token",
		},
		TimeoutMs: 0,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want non-nil for timeout <= 0")
	}
}

func TestRuntimeConfigUnmarshalJSONSupportsLegacyAliases(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	data := []byte(`{"baseUrl":"https://na.hub.molten.bot/v1","token":"agent-legacy","sessionKey":"main","timeoutMs":12000}`)
	if err := cfg.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got, want := cfg.AgentToken, "agent-legacy"; got != want {
		t.Fatalf("AgentToken = %q, want %q", got, want)
	}
	if got, want := cfg.SessionKey, "main"; got != want {
		t.Fatalf("SessionKey = %q, want %q", got, want)
	}
	if got, want := cfg.TimeoutMs, 12000; got != want {
		t.Fatalf("TimeoutMs = %d, want %d", got, want)
	}
}

func TestSaveAndLoadRuntimeConfigUseDefaultResolvedPath(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "runtime", "config.json")
	t.Setenv(runtimeConfigPathEnv, envPath)

	if err := SaveRuntimeConfig("", InitConfig{
		BaseURL: "https://na.hub.molten.bot/v1",
	}, "agent-token"); err != nil {
		t.Fatalf("SaveRuntimeConfig(default path) error = %v", err)
	}

	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("saved config stat error = %v", err)
	}

	got, err := LoadRuntimeConfig("")
	if err != nil {
		t.Fatalf("LoadRuntimeConfig(default path) error = %v", err)
	}
	if got.RuntimeConfigPath != envPath {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got.RuntimeConfigPath, envPath)
	}
}

func TestSaveRuntimeConfigAuggieAuthCreatesConfigFromInitWhenMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	sessionAuth := `{"accessToken":"token_saved","tenantURL":"https://tenant.example/","scopes":["email"]}`

	if err := SaveRuntimeConfigAuggieAuth(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "auggie",
	}, sessionAuth); err != nil {
		t.Fatalf("SaveRuntimeConfigAuggieAuth() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["augment_session_auth"] != sessionAuth {
		t.Fatalf("augment_session_auth = %#v, want %q", got["augment_session_auth"], sessionAuth)
	}
	if got["agent_harness"] != "auggie" {
		t.Fatalf("agent_harness = %#v, want %q", got["agent_harness"], "auggie")
	}
}

func TestSaveRuntimeConfigAuggieAuthMergesIntoExistingConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved","custom":"preserved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	sessionAuth := `{"accessToken":"token_saved","tenantURL":"https://tenant.example/","scopes":["email"]}`
	if err := SaveRuntimeConfigAuggieAuth(path, InitConfig{}, sessionAuth); err != nil {
		t.Fatalf("SaveRuntimeConfigAuggieAuth() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["augment_session_auth"] != sessionAuth {
		t.Fatalf("augment_session_auth = %#v, want %q", got["augment_session_auth"], sessionAuth)
	}
	if got["custom"] != "preserved" {
		t.Fatalf("custom = %#v, want %q", got["custom"], "preserved")
	}
}

func TestSaveRuntimeConfigAuggieAuthRejectsMalformedConfigJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := SaveRuntimeConfigAuggieAuth(path, InitConfig{}, `{"accessToken":"token_saved","tenantURL":"https://tenant.example/","scopes":["email"]}`)
	if err == nil {
		t.Fatal("SaveRuntimeConfigAuggieAuth() error = nil, want non-nil")
	}
	if got := err.Error(); got == "" || got == "parse runtime config" {
		t.Fatalf("error = %q, want parse detail", got)
	}
}

func TestSaveRuntimeConfigPiProviderAuthMergesIntoExistingConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved","custom":"preserved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	providerAuth := `{"env_var":"OPENAI_API_KEY","value":"sk-saved"}`
	if err := SaveRuntimeConfigPiProviderAuth(path, InitConfig{}, providerAuth); err != nil {
		t.Fatalf("SaveRuntimeConfigPiProviderAuth() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["pi_provider_auth"] != providerAuth {
		t.Fatalf("pi_provider_auth = %#v, want %q", got["pi_provider_auth"], providerAuth)
	}
	if got["custom"] != "preserved" {
		t.Fatalf("custom = %#v, want %q", got["custom"], "preserved")
	}
}

func TestSaveRuntimeConfigPiAuthJSONMergesIntoExistingConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved","custom":"preserved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	piAuthJSON := `{"provider":"pi","token":"saved"}`
	if err := SaveRuntimeConfigPiAuthJSON(path, InitConfig{}, piAuthJSON); err != nil {
		t.Fatalf("SaveRuntimeConfigPiAuthJSON() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["pi_auth_json"] != piAuthJSON {
		t.Fatalf("pi_auth_json = %#v, want %q", got["pi_auth_json"], piAuthJSON)
	}
	if got["custom"] != "preserved" {
		t.Fatalf("custom = %#v, want %q", got["custom"], "preserved")
	}
}

func TestSaveRuntimeConfigGitHubTokenCreatesConfigFromInitWhenMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	token := "ghp_saved_token"

	if err := SaveRuntimeConfigGitHubToken(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "claude",
	}, token); err != nil {
		t.Fatalf("SaveRuntimeConfigGitHubToken() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["github_token"] != token {
		t.Fatalf("github_token = %#v, want %q", got["github_token"], token)
	}
	if got["agent_harness"] != "claude" {
		t.Fatalf("agent_harness = %#v, want %q", got["agent_harness"], "claude")
	}
}

func TestSaveRuntimeConfigGitHubTokenMergesIntoExistingConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved","custom":"preserved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := SaveRuntimeConfigGitHubToken(path, InitConfig{}, "ghp_saved_token"); err != nil {
		t.Fatalf("SaveRuntimeConfigGitHubToken() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["github_token"] != "ghp_saved_token" {
		t.Fatalf("github_token = %#v, want %q", got["github_token"], "ghp_saved_token")
	}
	if got["custom"] != "preserved" {
		t.Fatalf("custom = %#v, want %q", got["custom"], "preserved")
	}
}

func TestSaveRuntimeConfigHubSettingsMergesHubFieldsWithoutDroppingExtras(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","custom":"preserved","github_token":"ghp_saved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := SaveRuntimeConfigHubSettings(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		BindToken:    "bind_saved",
		AgentHarness: "codex",
		Handle:       "molten-builder",
		Profile: ProfileConfig{
			ProfileText: "Builds things",
			DisplayName: "Molten Builder",
			Emoji:       "🔥",
		},
	}, "agent_saved")
	if err != nil {
		t.Fatalf("SaveRuntimeConfigHubSettings() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["bind_token"] != "bind_saved" {
		t.Fatalf("bind_token = %#v, want %q", got["bind_token"], "bind_saved")
	}
	if got["agent_token"] != "agent_saved" {
		t.Fatalf("agent_token = %#v, want %q", got["agent_token"], "agent_saved")
	}
	if got["handle"] != "molten-builder" {
		t.Fatalf("handle = %#v, want %q", got["handle"], "molten-builder")
	}
	profile, _ := got["profile"].(map[string]any)
	if profile["display_name"] != "Molten Builder" {
		t.Fatalf("profile.display_name = %#v, want %q", profile["display_name"], "Molten Builder")
	}
	if got, want := profile["llm"], agentruntime.Default().Harness; got != want {
		t.Fatalf("profile.llm = %#v, want %q", got, want)
	}
	if profile["harness"] != runtimeIdentifier {
		t.Fatalf("profile.harness = %#v, want %q", profile["harness"], runtimeIdentifier)
	}
	skills, _ := profile["skills"].([]any)
	if len(skills) != 3 || skills[0] != "code_for_me" || skills[1] != "code_review" || skills[2] != "library_task" {
		t.Fatalf("profile.skills = %#v, want [code_for_me code_review library_task]", profile["skills"])
	}
	if got["custom"] != "preserved" {
		t.Fatalf("custom = %#v, want %q", got["custom"], "preserved")
	}
	if got["github_token"] != "ghp_saved" {
		t.Fatalf("github_token = %#v, want %q", got["github_token"], "ghp_saved")
	}
}

func TestSaveRuntimeConfigHubSettingsClearsStaleBindTokenForAgentTokenFlow(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","bind_token":"old_bind"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := SaveRuntimeConfigHubSettings(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_direct",
		AgentHarness: "codex",
	}, "agent_direct")
	if err != nil {
		t.Fatalf("SaveRuntimeConfigHubSettings() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := got["bind_token"]; ok {
		t.Fatalf("bind_token present = %#v, want removed", got["bind_token"])
	}
	if got["agent_token"] != "agent_direct" {
		t.Fatalf("agent_token = %#v, want %q", got["agent_token"], "agent_direct")
	}
}

func TestSaveRuntimeConfigHubSettingsPreservesConfiguredLogLevel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","log_level":"debug"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := SaveRuntimeConfigHubSettings(path, InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_direct",
		AgentHarness: "codex",
	}, "agent_direct"); err != nil {
		t.Fatalf("SaveRuntimeConfigHubSettings() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["log_level"] != "debug" {
		t.Fatalf("log_level = %#v, want %q", got["log_level"], "debug")
	}
}

func TestSaveRuntimeConfigHubSettingsRejectsUnboundAgentWithoutWriting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")

	err := SaveRuntimeConfigHubSettings(path, InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		AgentToken: "agent_direct",
	}, "agent_direct")
	if err == nil {
		t.Fatal("SaveRuntimeConfigHubSettings() error = nil, want non-nil")
	}
	if got := err.Error(); got != unboundAgentRuntimeErrorMessage {
		t.Fatalf("SaveRuntimeConfigHubSettings() error = %q, want %q", got, unboundAgentRuntimeErrorMessage)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("config file stat error = %v, want not exist", statErr)
	}
}

func TestReadRuntimeConfigString(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"github_token":"ghp_saved","augment_session_auth":"{\"accessToken\":\"a\"}"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if got, want := ReadRuntimeConfigString(path, "github_token", "githubToken"), "ghp_saved"; got != want {
		t.Fatalf("ReadRuntimeConfigString(github_token) = %q, want %q", got, want)
	}
	if got, want := ReadRuntimeConfigString(path, "missing", "augment_session_auth"), `{"accessToken":"a"}`; got != want {
		t.Fatalf("ReadRuntimeConfigString(augment_session_auth) = %q, want %q", got, want)
	}
}

func TestReadRuntimeConfigStringInvalidInputs(t *testing.T) {
	t.Parallel()

	if got := ReadRuntimeConfigString("", "github_token"); got != "" {
		t.Fatalf("ReadRuntimeConfigString(empty path) = %q, want empty", got)
	}
	if got := ReadRuntimeConfigString(filepath.Join(t.TempDir(), "missing.json"), "github_token"); got != "" {
		t.Fatalf("ReadRuntimeConfigString(missing path) = %q, want empty", got)
	}

	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got := ReadRuntimeConfigString(path, "github_token"); got != "" {
		t.Fatalf("ReadRuntimeConfigString(malformed) = %q, want empty", got)
	}
}

func TestLoadRuntimeConfigBackfillsMissingLogLevel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.LogLevel != DefaultLogLevel {
		t.Fatalf("LogLevel = %q, want %q", got.LogLevel, DefaultLogLevel)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if doc["log_level"] != DefaultLogLevel {
		t.Fatalf("log_level = %#v, want %q", doc["log_level"], DefaultLogLevel)
	}
}

func TestLoadRuntimeConfigHonorsConfiguredLogLevel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved","log_level":"debug"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want %q", got.LogLevel, "debug")
	}
}
