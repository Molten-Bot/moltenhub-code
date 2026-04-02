package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInitDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  // comments are allowed
  "bind_token": "bind_abc123"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if cfg.Version != "v1" {
		t.Fatalf("Version = %q", cfg.Version)
	}
	if cfg.BaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.SessionKey != "main" {
		t.Fatalf("SessionKey = %q", cfg.SessionKey)
	}
	if cfg.Skill.Name != "codex_harness_run" {
		t.Fatalf("Skill.Name = %q", cfg.Skill.Name)
	}
	if cfg.Skill.DispatchType != "skill_request" {
		t.Fatalf("Skill.DispatchType = %q", cfg.Skill.DispatchType)
	}
	if cfg.Skill.ResultType != "skill_result" {
		t.Fatalf("Skill.ResultType = %q", cfg.Skill.ResultType)
	}
	if cfg.Dispatcher.MaxParallel != 2 {
		t.Fatalf("Dispatcher.MaxParallel = %d", cfg.Dispatcher.MaxParallel)
	}
}

func TestLoadInitRejectsMissingTokens(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "base_url": "https://na.hub.molten.bot/v1"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	_, err := LoadInit(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bind_token or agent_token is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsUnsupportedBaseURLScheme(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Version:    "v1",
		BaseURL:    "ftp://example.com/v1",
		BindToken:  "token",
		SessionKey: "main",
		Skill: SkillConfig{
			Name:         "codex_harness_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{MaxParallel: 1},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoadInitAllowsAgentTokenWithoutBindToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "agent_token": "agent_live_token",
  "skill": {
    "name": "codex_harness_run"
  },
  "dispatcher": {
    "max_parallel": 4
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if cfg.AgentToken != "agent_live_token" {
		t.Fatalf("AgentToken = %q", cfg.AgentToken)
	}
	if cfg.Dispatcher.MaxParallel != 4 {
		t.Fatalf("Dispatcher.MaxParallel = %d", cfg.Dispatcher.MaxParallel)
	}
}
