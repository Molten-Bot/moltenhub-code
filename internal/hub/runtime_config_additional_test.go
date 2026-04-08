package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	t.Parallel()

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
