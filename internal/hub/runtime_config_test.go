package hub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveRuntimeConfigWritesExpectedShape(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	err := SaveRuntimeConfig(path, "https://na.hub.molten.bot/v1", "agent_123", "main")
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
	if got.Token != "agent_123" {
		t.Fatalf("Token = %q", got.Token)
	}
	if got.SessionKey != "main" {
		t.Fatalf("SessionKey = %q", got.SessionKey)
	}
	if got.TimeoutMs != 20000 {
		t.Fatalf("TimeoutMs = %d", got.TimeoutMs)
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

	err := SaveRuntimeConfig(path, "https://na.hub.molten.bot/v1", "agent_123", "")
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

func TestSaveRuntimeConfigRequiresBaseURLAndToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	if err := SaveRuntimeConfig(path, "", "agent_123", "main"); err == nil {
		t.Fatal("expected error for empty base URL")
	}
	if err := SaveRuntimeConfig(path, "https://na.hub.molten.bot/v1", "", "main"); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestLoadRuntimeConfigRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "moltenhub", "config.json")

	if err := SaveRuntimeConfig(path, "https://na.hub.molten.bot/v1", "agent_abc", "main"); err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}

	got, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if got.BaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.Token != "agent_abc" {
		t.Fatalf("Token = %q", got.Token)
	}
	if got.SessionKey != "main" {
		t.Fatalf("SessionKey = %q", got.SessionKey)
	}
	if got.TimeoutMs != 20000 {
		t.Fatalf("TimeoutMs = %d", got.TimeoutMs)
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

func TestLoadRuntimeConfigRejectsMissingToken(t *testing.T) {
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

	_, err := LoadRuntimeConfig(path)
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestDefaultRuntimeConfigPath(t *testing.T) {
	t.Parallel()

	if got := defaultRuntimeConfigPath(); got != runtimeConfigPath {
		t.Fatalf("defaultRuntimeConfigPath() = %q, want %q", got, runtimeConfigPath)
	}
}
