package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/harness"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

func useHubSetupLocationsLoaderForTest(t *testing.T, loader func(context.Context) ([]hubSetupLocation, error)) {
	t.Helper()
	previous := hubSetupLocationsLoader
	resetHubSetupLocationsCache()
	hubSetupLocationsLoader = loader
	t.Cleanup(func() {
		hubSetupLocationsLoader = previous
		resetHubSetupLocationsCache()
	})
}

func fakeHubSetupToken(prefix string) string {
	if len(prefix) >= 40 {
		return prefix
	}
	return prefix + strings.Repeat("x", 40-len(prefix))
}

func TestRunUsageMissingSubcommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunUsageMissingConfigFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "run"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunConfigLoadFailure(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	missing := filepath.Join(t.TempDir(), "missing.json")
	os.Args = []string{"harness", "run", "--config", missing}

	if code := run(); code != 10 {
		t.Fatalf("run() = %d, want 10", code)
	}
}

func TestRunUsageUnknownSubcommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "unknown"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunMultiplexUsageMissingConfigFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "multiplex"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestLoadHubBootConfigWithoutFlagsUsesDefaultsWhenRuntimeConfigMissing(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tempDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig("", "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got, want := cfg.RuntimeConfigPath, "./.moltenhub/config.json"; got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithMissingInitFlagFallsBackToRuntimeDefaults(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	tempDir := t.TempDir()
	initPath := filepath.Join(tempDir, "init.json")

	cfg, exitCode, err := loadHubBootConfig(initPath, "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got, want := cfg.RuntimeConfigPath, filepath.Join(tempDir, "config.json"); got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithMissingInitFlagUsesSiblingRuntimeConfigWhenAvailable(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	tempDir := t.TempDir()
	initPath := filepath.Join(tempDir, "init.json")
	runtimeConfigPath := filepath.Join(tempDir, "config.json")
	runtimeJSON := `{
  "version": "v1",
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_token": "agent_123",
  "agent_harness": "codex",
  "session_key": "main",
  "timeout_ms": 20000
}`
	if err := os.WriteFile(runtimeConfigPath, []byte(runtimeJSON), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig(initPath, "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.RuntimeConfigPath, runtimeConfigPath; got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
	if got, want := cfg.AgentToken, "agent_123"; got != want {
		t.Fatalf("AgentToken = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithInitUsesRuntimeConfigLogLevelWhenPresent(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	tempDir := t.TempDir()
	initPath := filepath.Join(tempDir, "init.json")
	runtimeConfigPath := filepath.Join(tempDir, "config.json")

	if err := os.WriteFile(initPath, []byte(`{"base_url":"https://na.hub.molten.bot/v1"}`), 0o600); err != nil {
		t.Fatalf("write init config: %v", err)
	}
	if err := os.WriteFile(runtimeConfigPath, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved","log_level":"debug"}`), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig(initPath, "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.LogLevel, hub.LogLevelDebug.String(); got != want {
		t.Fatalf("LogLevel = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithInitBackfillsMissingRuntimeConfigLogLevel(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	tempDir := t.TempDir()
	initPath := filepath.Join(tempDir, "init.json")
	runtimeConfigPath := filepath.Join(tempDir, "config.json")

	if err := os.WriteFile(initPath, []byte(`{"base_url":"https://na.hub.molten.bot/v1"}`), 0o600); err != nil {
		t.Fatalf("write init config: %v", err)
	}
	if err := os.WriteFile(runtimeConfigPath, []byte(`{"base_url":"https://na.hub.molten.bot/v1","agent_token":"agent_saved"}`), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig(initPath, "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.LogLevel, hub.DefaultLogLevel; got != want {
		t.Fatalf("LogLevel = %q, want %q", got, want)
	}

	data, err := os.ReadFile(runtimeConfigPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["log_level"], any(hub.DefaultLogLevel); got != want {
		t.Fatalf("config.json log_level = %#v, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithMissingConfigFlagFallsBackToRuntimeDefaults(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	tempDir := t.TempDir()
	runtimeConfigPath := filepath.Join(tempDir, "config.json")

	cfg, exitCode, err := loadHubBootConfig("", runtimeConfigPath)
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got, want := cfg.RuntimeConfigPath, runtimeConfigPath; got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithInvalidConfigFlagFallsBackToRuntimeDefaults(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")
	t.Setenv("HARNESS_ALLOW_NON_MOLTEN_HUB_BASE_URL", "")

	tempDir := t.TempDir()
	runtimeConfigPath := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(runtimeConfigPath, []byte(`{"base_url":"http://127.0.0.1:41099/v1","agent_token":"agent_saved"}`), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig("", runtimeConfigPath)
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got, want := cfg.RuntimeConfigPath, runtimeConfigPath; got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
	if cfg.AgentToken != "" {
		t.Fatalf("AgentToken = %q, want empty", cfg.AgentToken)
	}
}

func TestLoadHubBootConfigUsesDefaultRuntimeConfigWhenFlagsOmitted(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, ".moltenhub")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")
	configJSON := `{
  "version": "v1",
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_token": "agent_123",
  "agent_harness": "codex",
  "session_key": "main",
  "timeout_ms": 20000
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cfg, exitCode, err := loadHubBootConfig("", "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if cfg.RuntimeConfigPath != "./.moltenhub/config.json" {
		t.Fatalf("RuntimeConfigPath = %q, want %q", cfg.RuntimeConfigPath, "./.moltenhub/config.json")
	}
	if cfg.AgentToken != "agent_123" {
		t.Fatalf("AgentToken = %q, want %q", cfg.AgentToken, "agent_123")
	}
}

func TestLoadHubBootConfigWithoutFlagsAllowsDefaultRuntimeConfigWithoutCredentials(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, ".moltenhub")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"base_url":"https://na.hub.molten.bot/v1"}`), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cfg, exitCode, err := loadHubBootConfig("", "")
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if cfg.AgentToken != "" || cfg.BindToken != "" {
		t.Fatalf("expected no credentials, got AgentToken=%q BindToken=%q", cfg.AgentToken, cfg.BindToken)
	}
}

func TestLoadHubBootConfigUsesMoltenHubTokenEnvWhenRuntimeConfigOmitsCredentials(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")
	t.Setenv("MOLTEN_HUB_TOKEN", "t_env_agent_token")
	t.Setenv("MOLTEN_HUB_URL", "")
	t.Setenv("MOLTEN_HUB_REGION", "eu")

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"github_token":"github_token_saved","agent_harness":"codex"}`), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig("", configPath)
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.AgentToken, "t_env_agent_token"; got != want {
		t.Fatalf("AgentToken = %q, want %q", got, want)
	}
	if got, want := cfg.BaseURL, "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got, want := cfg.GitHubToken, "github_token_saved"; got != want {
		t.Fatalf("GitHubToken = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigAppliesEnvTokensToDefaultConfig(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")
	t.Setenv("MOLTEN_HUB_TOKEN", "t_env_agent_token")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github_token_env_token")
	t.Setenv("MOLTEN_HUB_URL", "")
	t.Setenv("MOLTEN_HUB_REGION", "eu")

	configPath := filepath.Join(t.TempDir(), "missing.json")
	cfg, exitCode, err := loadHubBootConfig("", configPath)
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.AgentToken, "t_env_agent_token"; got != want {
		t.Fatalf("AgentToken = %q, want %q", got, want)
	}
	if got, want := cfg.GitHubToken, "github_token_env_token"; got != want {
		t.Fatalf("GitHubToken = %q, want %q", got, want)
	}
	if got, want := cfg.AgentHarness, agentruntime.HarnessCodex; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := cfg.BaseURL, "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
}

func TestLoadHubBootConfigWithBadRuntimeConfigFallsBackToDefaults(t *testing.T) {
	t.Setenv("HARNESS_RUNTIME_CONFIG_PATH", "")
	t.Setenv("HARNESS_ALLOW_NON_MOLTEN_HUB_BASE_URL", "")

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"base_url":"http://127.0.0.1:41099/v1","agent_token":"agent_saved"}`), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	cfg, exitCode, err := loadHubBootConfig("", configPath)
	if err != nil {
		t.Fatalf("loadHubBootConfig() error = %v", err)
	}
	if exitCode != harness.ExitSuccess {
		t.Fatalf("loadHubBootConfig() exitCode = %d, want %d", exitCode, harness.ExitSuccess)
	}
	if got, want := cfg.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if got := cfg.AgentToken; got != "" {
		t.Fatalf("AgentToken = %q, want empty", got)
	}
	if got, want := cfg.RuntimeConfigPath, configPath; got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
}

func TestMonitorURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: ":7777", want: "http://127.0.0.1:7777"},
		{in: "127.0.0.1:7777", want: "http://127.0.0.1:7777"},
		{in: "http://localhost:8080", want: "http://localhost:8080"},
	}

	for _, tt := range tests {
		if got := monitorURL(tt.in); got != tt.want {
			t.Fatalf("monitorURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCollectConfigPathsFilesAndDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.json")
	fileB := filepath.Join(dir, "b.JSON")
	fileTxt := filepath.Join(dir, "notes.txt")
	nestedDir := filepath.Join(dir, "nested")
	fileC := filepath.Join(nestedDir, "c.json")

	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	for _, path := range []string{fileA, fileB, fileTxt, fileC} {
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	got, err := collectConfigPaths([]string{fileA, dir, fileA})
	if err != nil {
		t.Fatalf("collectConfigPaths() error = %v", err)
	}

	want := []string{fileA, fileB, fileC}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectConfigPaths() = %v, want %v", got, want)
	}
}

func TestLocalTaskLogDir(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), ".log")
	got, ok := localTaskLogDir(root, "local-1712345678-000321")
	if !ok {
		t.Fatal("localTaskLogDir() ok = false, want true")
	}
	want := filepath.Join(root, "local", "1712345678", "000321")
	if got != want {
		t.Fatalf("localTaskLogDir() = %q, want %q", got, want)
	}

	if _, ok := localTaskLogDir(root, "req-abc"); ok {
		t.Fatal("localTaskLogDir(non-local request) ok = true, want false")
	}
}

func TestFailureFollowUpRunConfigIncludesNonLocalRequestLogPaths(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	failedResult := harness.Result{Err: fmt.Errorf("checks failed")}
	failedRunCfg := config.Config{Repos: []string{"git@github.com:acme/repo.git"}}

	cfg := failureFollowUpRunConfig("req-123-abc", failedResult, failedRunCfg, logRoot)
	expectedLogDir := filepath.Join(logRoot, "req", "123", "abc")
	if !strings.Contains(cfg.Prompt, expectedLogDir) {
		t.Fatalf("Prompt missing non-local log dir path %q: %q", expectedLogDir, cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, filepath.Join(expectedLogDir, logFileName)) {
		t.Fatalf("Prompt missing non-local log file path: %q", cfg.Prompt)
	}
}

func TestFailureFollowUpRunConfigUsesRequiredPayloadShapeAndLogContext(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	logDir := filepath.Join(logRoot, "local", "1712345678", "000001")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	logPath := filepath.Join(logDir, logFileName)
	if err := os.WriteFile(logPath, []byte("stage=checks status=failed\ncmd phase=checks text=\"go test failed\"\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	failedResult := harness.Result{
		Err:          fmt.Errorf("clone: repository not found"),
		WorkspaceDir: "/tmp/run-123",
	}
	failedRunCfg := config.Config{
		Repos: []string{
			"git@github.com:acme/repo-a.git",
			"git@github.com:acme/repo-b.git",
			"git@github.com:acme/repo-a.git",
		},
	}

	cfg := failureFollowUpRunConfig("local-1712345678-000001", failedResult, failedRunCfg, logRoot)
	if got, want := cfg.Repos, []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos = %v, want %v", got, want)
	}
	if cfg.BaseBranch != "" {
		t.Fatalf("BaseBranch = %q, want empty for remote default", cfg.BaseBranch)
	}
	if cfg.TargetSubdir != "." {
		t.Fatalf("TargetSubdir = %q, want %q", cfg.TargetSubdir, ".")
	}

	expectedLogDir := filepath.Join(logRoot, followUpTaskLogArchiveSubdir, "local", "1712345678", "000001")
	if !strings.Contains(cfg.Prompt, expectedLogDir) {
		t.Fatalf("Prompt missing log dir path %q: %q", expectedLogDir, cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, filepath.Join(expectedLogDir, logFileName)) {
		t.Fatalf("Prompt missing log file path: %q", cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, failureFollowUpRequiredPrompt) {
		t.Fatalf("Prompt missing required instruction: %q", cfg.Prompt)
	}
}

func TestFailureFollowUpRunConfigForcesRemoteDefaultBranchAndRootTarget(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	failedResult := harness.Result{
		Err:    fmt.Errorf("git: nothing to commit, working tree clean"),
		Branch: "moltenhub-add-slight-padding-between-prompt-and-lo",
	}
	failedRunCfg := config.Config{
		Repos:        []string{"git@github.com:acme/repo.git"},
		BaseBranch:   "moltenhub-add-slight-padding-between-prompt-and-lo",
		TargetSubdir: "internal/hubui",
		Prompt:       "add slight padding between prompt and lower items",
	}

	cfg := failureFollowUpRunConfig("local-1712345678-000001", failedResult, failedRunCfg, logRoot)
	if cfg.BaseBranch != "" {
		t.Fatalf("BaseBranch = %q, want empty for remote default", cfg.BaseBranch)
	}
	if cfg.TargetSubdir != "." {
		t.Fatalf("TargetSubdir = %q, want %q", cfg.TargetSubdir, ".")
	}
	for _, want := range []string{
		"- target_subdir=internal/hubui",
		"Original task prompt:",
		"add slight padding between prompt and lower items",
	} {
		if !strings.Contains(cfg.Prompt, want) {
			t.Fatalf("Prompt missing %q: %q", want, cfg.Prompt)
		}
	}
}

func TestFailureFollowUpReposAlwaysUsesMoltenHubRepositoryWhenResultHasSingleRepo(t *testing.T) {
	t.Parallel()

	failedResult := harness.Result{
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/failed-result.git"},
			{RepoURL: "git@github.com:acme/failed-result.git"},
		},
	}
	failedRunCfg := config.Config{
		Repos: []string{
			"git@github.com:acme/from-config.git",
		},
	}
	if got, want := failureFollowUpRepos(failedResult, failedRunCfg), []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposAlwaysUsesMoltenHubRepositoryWhenResultAndConfigDiffer(t *testing.T) {
	t.Parallel()

	failedResult := harness.Result{
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/from-result.git"},
			{RepoURL: "git@github.com:acme/from-result.git"},
		},
	}
	failedRunCfg := config.Config{
		Repos: []string{
			"git@github.com:acme/from-config.git",
			"git@github.com:acme/other-config.git",
		},
	}
	if got, want := failureFollowUpRepos(failedResult, failedRunCfg), []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposAlwaysUsesMoltenHubRepositoryWhenOnlyResultRepoExists(t *testing.T) {
	t.Parallel()

	failedResult := harness.Result{
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/from-result.git"},
		},
	}
	if got, want := failureFollowUpRepos(failedResult, config.Config{}), []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposAlwaysUsesMoltenHubRepositoryWhenResultIsAmbiguous(t *testing.T) {
	t.Parallel()

	failedResult := harness.Result{
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/repo-a.git"},
			{RepoURL: "git@github.com:acme/repo-b.git"},
		},
	}
	failedRunCfg := config.Config{
		Repos: []string{
			"git@github.com:acme/from-config.git",
			"git@github.com:acme/other-config.git",
		},
	}

	if got, want := failureFollowUpRepos(failedResult, failedRunCfg), []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposUsesMoltenHubRepositoryWhenNoRepoFound(t *testing.T) {
	t.Parallel()

	if got, want := failureFollowUpRepos(harness.Result{}, config.Config{}), []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestRunHubBootDiagnosticsWithRuntimeLoader(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(pingServer.Close)

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}): {
				result: execx.Result{Stdout: "git version 2.47.1\n"},
			},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"--version"}}): {
				result: execx.Result{Stdout: "gh version 2.61.0 (2026-01-15)\n"},
			},
			stubCommandKey(execx.Command{Name: "codex", Args: []string{"--help"}}): {
				result: execx.Result{Stdout: "Codex CLI help\n"},
			},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}): {
				result: execx.Result{Stderr: "not logged into github.com\n"},
				err:    errors.New("gh auth status failed"),
			},
		},
	}

	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if ok := runHubBootDiagnosticsWithRuntimeLoader(
		context.Background(),
		runner,
		logf,
		hub.InitConfig{BaseURL: pingServer.URL + "/v1"},
		func() (hub.RuntimeConfig, error) {
			return hub.RuntimeConfig{}, errors.New("missing runtime config")
		},
	); !ok {
		t.Fatal("runHubBootDiagnosticsWithRuntimeLoader() = false, want true")
	}

	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=git_cli")
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=gh_cli")
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=codex_cli")
	assertLogContains(t, logs, "boot.diagnosis status=warn requirement=gh_auth")
	assertLogContains(t, logs, gitHubCLIAuthRecommendation)
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=moltenhub_ping")
	assertLogContains(t, logs, "boot.diagnosis status=recommendation requirement=moltenhub_hub")
	assertLogContains(t, logs, "boot.diagnosis status=complete required_checks=ok")
}

func TestRunHubBootDiagnosticsWithRuntimeLoaderFailsWhenPingUnavailable(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(pingServer.Close)

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}): {
				result: execx.Result{Stdout: "git version 2.47.1\n"},
			},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"--version"}}): {
				result: execx.Result{Stdout: "gh version 2.61.0 (2026-01-15)\n"},
			},
			stubCommandKey(execx.Command{Name: "codex", Args: []string{"--help"}}): {
				result: execx.Result{Stdout: "Codex CLI help\n"},
			},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}): {
				result: execx.Result{Stdout: "Logged in to github.com as test\n"},
			},
		},
	}

	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	ok := runHubBootDiagnosticsWithRuntimeLoader(
		context.Background(),
		runner,
		logf,
		hub.InitConfig{BaseURL: pingServer.URL + "/v1"},
		func() (hub.RuntimeConfig, error) {
			return hub.RuntimeConfig{}, nil
		},
	)
	if ok {
		t.Fatal("runHubBootDiagnosticsWithRuntimeLoader() = true, want false")
	}
	assertLogContains(t, logs, "boot.diagnosis status=error requirement=moltenhub_ping")
}

func TestHubCredentialsConfigured(t *testing.T) {
	t.Parallel()

	if !hubCredentialsConfigured(hub.InitConfig{AgentToken: "agent-token"}, nil) {
		t.Fatal("hubCredentialsConfigured() = false with agent token, want true")
	}
	if !hubCredentialsConfigured(hub.InitConfig{BindToken: "bind-token"}, nil) {
		t.Fatal("hubCredentialsConfigured() = false with bind token, want true")
	}
	if !hubCredentialsConfigured(hub.InitConfig{}, func() (hub.RuntimeConfig, error) {
		return hub.RuntimeConfig{InitConfig: hub.InitConfig{AgentToken: "saved-token"}}, nil
	}) {
		t.Fatal("hubCredentialsConfigured() = false with runtime config token, want true")
	}
	if hubCredentialsConfigured(hub.InitConfig{}, func() (hub.RuntimeConfig, error) {
		return hub.RuntimeConfig{}, nil
	}) {
		t.Fatal("hubCredentialsConfigured() = true with tokenless runtime config, want false")
	}
	if hubCredentialsConfigured(hub.InitConfig{}, func() (hub.RuntimeConfig, error) {
		return hub.RuntimeConfig{}, errors.New("not found")
	}) {
		t.Fatal("hubCredentialsConfigured() = true without token sources, want false")
	}
}

func TestDiagnosticDetailForResult(t *testing.T) {
	t.Parallel()

	if got := diagnosticDetailForResult(execx.Result{Stdout: "\n\n git version 2.47.1 \n"}); got != "git version 2.47.1" {
		t.Fatalf("diagnosticDetailForResult(stdout) = %q", got)
	}
	if got := diagnosticDetailForResult(execx.Result{Stderr: "not logged in\n"}); got != "not logged in" {
		t.Fatalf("diagnosticDetailForResult(stderr) = %q", got)
	}
	if got := diagnosticDetailForResult(execx.Result{}); got != "check completed" {
		t.Fatalf("diagnosticDetailForResult(empty) = %q", got)
	}
}

func TestHubPingURLUsesHostRootPingEndpoint(t *testing.T) {
	t.Parallel()

	got, err := hubPingURL("https://na.hub.molten.bot/v1")
	if err != nil {
		t.Fatalf("hubPingURL() error = %v", err)
	}
	if got != "https://na.hub.molten.bot/ping" {
		t.Fatalf("hubPingURL() = %q, want %q", got, "https://na.hub.molten.bot/ping")
	}
}

type stubExecResult struct {
	result execx.Result
	err    error
}

type stubExecRunner struct {
	results map[string]stubExecResult
}

func (r *stubExecRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	if r == nil {
		return execx.Result{}, errors.New("nil runner")
	}
	entry, ok := r.results[stubCommandKey(cmd)]
	if !ok {
		return execx.Result{}, fmt.Errorf("unexpected command: %s", stubCommandKey(cmd))
	}
	return entry.result, entry.err
}

func stubCommandKey(cmd execx.Command) string {
	return fmt.Sprintf("%s %s", cmd.Name, strings.Join(cmd.Args, " "))
}

func assertLogContains(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(line, want) {
			return
		}
	}
	t.Fatalf("logs missing %q\nlogs:\n%s", want, strings.Join(lines, "\n"))
}

func TestCurrentHubSetupStateUsesStoredBindTokenAsNewAgentMode(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": "v1",
  "base_url": "https://na.hub.molten.bot/v1",
  "bind_token": "bind_saved",
  "agent_token": "agent_saved",
  "handle": "builder",
  "profile": {
    "display_name": "Builder",
    "emoji": "🔥",
    "profile": "Ships UI work"
  },
  "timeout_ms": 20000
}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupState(hub.InitConfig{RuntimeConfigPath: configPath})
	if got, want := state.AgentMode, "new"; got != want {
		t.Fatalf("AgentMode = %q, want %q", got, want)
	}
	if got, want := state.TokenType, "bind"; got != want {
		t.Fatalf("TokenType = %q, want %q", got, want)
	}
	if got, want := state.Handle, "builder"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got := state.Message; got != "" {
		t.Fatalf("Message = %q, want empty", got)
	}
}

func TestCurrentHubSetupStateWithRemoteProfileHydratesMissingProfileFromHub(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agents/me" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("GET /agents/me token = %q, want %q", got, savedToken)
		}
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": {
				"agent": {
					"handle": "remote-agent",
					"metadata": {
						"profile_markdown": "# 🌊 Remote Agent\n\nOwns remote profile hydration."
					}
				}
			}
		}`))
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{
		"version": "v1",
		"base_url": %q,
		"agent_token": %q
	}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupStateWithRemoteProfile(context.Background(), hub.InitConfig{
		RuntimeConfigPath: configPath,
	})
	if got, want := state.Handle, "remote-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := state.Profile.DisplayName, "Remote Agent"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "🌊"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Owns remote profile hydration."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestCurrentHubSetupStateWithRemoteProfileMergesSplitProfileFields(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agents/me" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("GET /agents/me token = %q, want %q", got, savedToken)
		}
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": {
				"agent": {
					"handle": "codex-beast",
					"profile": {
						"display_name": "Jef's Codex"
					},
					"metadata": {
						"profile_markdown": "# 🦍 Jef's Codex\n\nRunning code updates quickly."
					}
				}
			}
		}`))
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{
		"version": "v1",
		"base_url": %q,
		"agent_token": %q
	}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupStateWithRemoteProfile(context.Background(), hub.InitConfig{
		RuntimeConfigPath: configPath,
	})
	if got, want := state.Handle, "codex-beast"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := state.Profile.DisplayName, "Jef's Codex"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "🦍"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Running code updates quickly."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestCurrentHubSetupStateWithRemoteProfileMergesDirectProfileAndMetadata(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agents/me" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("GET /agents/me token = %q, want %q", got, savedToken)
		}
		_, _ = w.Write([]byte(`{
			"handle": "codex-beast",
			"profile": {
				"display_name": "Jef's Codex",
				"emoji": "🦍"
			},
			"metadata": {
				"profile": "Running code updates quickly."
			}
		}`))
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{
		"version": "v1",
		"base_url": %q,
		"agent_token": %q
	}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupStateWithRemoteProfile(context.Background(), hub.InitConfig{
		RuntimeConfigPath: configPath,
	})
	if got, want := state.Handle, "codex-beast"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := state.Profile.DisplayName, "Jef's Codex"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "🦍"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Running code updates quickly."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestConfigureHubSetupNewAgentUsesBindTokenFlow(t *testing.T) {
	t.Parallel()

	bindToken := fakeHubSetupToken("b_")

	var bindCalled bool
	var profilePatchCalls int
	var liveCfg hub.InitConfig

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bind-tokens":
			bodyBytes, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(bodyBytes), fmt.Sprintf(`"bind_token":%q`, bindToken)) && !strings.Contains(string(bodyBytes), fmt.Sprintf(`"bind_token":"%s"`, bindToken)) {
				t.Fatalf("bind body = %s", string(bodyBytes))
			}
			bindCalled = true
			_, _ = w.Write([]byte(`{"token":"agent-resolved"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != "agent-resolved" {
				t.Fatalf("GET /agents/me token = %q, want %q", got, "agent-resolved")
			}
			_, _ = w.Write([]byte(`{"handle":"new-builder","profile":{"display_name":"Molten Builder","emoji":"🔥","profile":"Builds things"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case (r.Method == http.MethodPost || r.Method == http.MethodPatch) &&
			(r.URL.Path == "/v1/agents/me" || r.URL.Path == "/v1/agents/me/metadata"):
			if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != "agent-resolved" {
				t.Fatalf("sync token = %q, want %q", got, "agent-resolved")
			}
			profilePatchCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		Token:  bindToken,
		Handle: "new-builder",
		Profile: struct {
			ProfileText string `json:"profile"`
			DisplayName string `json:"display_name"`
			Emoji       string `json:"emoji"`
		}{
			ProfileText: "Builds things",
			DisplayName: "Molten Builder",
			Emoji:       "🔥",
		},
	}, func(_ context.Context, cfg hub.InitConfig) error {
		liveCfg = cfg
		return nil
	})
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if !bindCalled {
		t.Fatal("expected bind token flow to be used for new agent setup")
	}
	if profilePatchCalls != 0 {
		t.Fatalf("profile patch calls = %d, want 0 when submitted profile matches Hub profile", profilePatchCalls)
	}
	if got, want := state.AgentMode, "new"; got != want {
		t.Fatalf("AgentMode = %q, want %q", got, want)
	}
	if got, want := state.TokenType, "bind"; got != want {
		t.Fatalf("TokenType = %q, want %q", got, want)
	}
	if state.NeedsRestart {
		t.Fatal("NeedsRestart = true, want false")
	}
	if got, want := strings.TrimSpace(state.Message), "Molten Hub setup saved and applied live."; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got, want := strings.TrimSpace(liveCfg.AgentToken), "agent-resolved"; got != want {
		t.Fatalf("live config agent token = %q, want %q", got, want)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	contents := string(data)
	if !strings.Contains(contents, fmt.Sprintf(`"bind_token": %q`, bindToken)) {
		t.Fatalf("saved config missing bind_token: %s", contents)
	}
	if !strings.Contains(contents, `"agent_token": "agent-resolved"`) {
		t.Fatalf("saved config missing resolved agent token: %s", contents)
	}
}

func TestConfigureHubSetupExistingAgentUsesAgentTokenFlow(t *testing.T) {
	t.Parallel()

	agentToken := fakeHubSetupToken("t_")

	var getCalls int
	var onlineCalls int
	var liveCfg hub.InitConfig
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me" {
			getCalls++
			if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != agentToken {
				t.Fatalf("GET /agents/me token = %q, want %q", got, agentToken)
			}
			_, _ = w.Write([]byte(`{"handle":"existing-agent","profile":{"display_name":"Existing Agent","emoji":"🤖","profile":"Owns automation"}}`))
			return
		}
		if r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status" {
			onlineCalls++
			bodyBytes, _ := io.ReadAll(r.Body)
			body := string(bodyBytes)
			if !strings.Contains(body, `"status":"online"`) {
				t.Fatalf("status body = %s, want online status", body)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"base_url":%q,"bind_token":"stale_bind"}`, server.URL+"/v1")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		Token: agentToken,
	}, func(_ context.Context, cfg hub.InitConfig) error {
		liveCfg = cfg
		return nil
	})
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if getCalls < 2 {
		t.Fatalf("GET /agents/me calls = %d, want at least 2", getCalls)
	}
	if onlineCalls != 1 {
		t.Fatalf("PATCH /agents/me/status calls = %d, want 1", onlineCalls)
	}
	if got, want := state.AgentMode, "existing"; got != want {
		t.Fatalf("AgentMode = %q, want %q", got, want)
	}
	if got, want := state.Region, "na"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got, want := state.TokenType, "agent"; got != want {
		t.Fatalf("TokenType = %q, want %q", got, want)
	}
	if state.NeedsRestart {
		t.Fatal("NeedsRestart = true, want false")
	}
	if got, want := strings.TrimSpace(liveCfg.AgentToken), agentToken; got != want {
		t.Fatalf("live config agent token = %q, want %q", got, want)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	contents := string(data)
	if strings.Contains(contents, `"bind_token"`) {
		t.Fatalf("saved config should remove stale bind_token: %s", contents)
	}
	if !strings.Contains(contents, fmt.Sprintf(`"agent_token": %q`, agentToken)) {
		t.Fatalf("saved config missing agent token: %s", contents)
	}
}

func TestCurrentHubSetupStateDerivesRegionFromRuntimeConfigBaseURL(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"base_url":"https://eu.hub.molten.bot/v1","agent_token":"agent_saved"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupState(hub.InitConfig{RuntimeConfigPath: configPath})
	if got, want := state.Region, "eu"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
}

func TestHubSetupBaseURLUsesSelectedRegionForHubEndpoints(t *testing.T) {
	t.Parallel()

	if got, want := hubSetupBaseURL("", "eu"), "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(empty, eu) = %q, want %q", got, want)
	}
	if got, want := hubSetupBaseURL("https://na.hub.molten.bot/v1", "eu"), "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(na, eu) = %q, want %q", got, want)
	}
	if got, want := hubSetupBaseURL("https://eu.hub.molten.bot/v1", "na"), "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(eu, na) = %q, want %q", got, want)
	}
	if got, want := hubSetupBaseURL("http://127.0.0.1:7777/v1", "eu"), "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(custom, eu) = %q, want %q", got, want)
	}
}

func TestHubSetupBaseURLUsesHubRegistryLocations(t *testing.T) {
	useHubSetupLocationsLoaderForTest(t, func(context.Context) ([]hubSetupLocation, error) {
		return []hubSetupLocation{
			{Display: "North America", Key: "na", Domain: "north-america.hub.molten.bot"},
			{Display: "Europe", Key: "eu", Domain: "europe.hub.molten.bot"},
		}, nil
	})

	if got, want := hubSetupBaseURL("", "eu"), "https://europe.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(empty, eu) = %q, want %q", got, want)
	}
	if got, want := hubSetupBaseURL("https://north-america.hub.molten.bot/v1", "eu"), "https://europe.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(registry default, eu) = %q, want %q", got, want)
	}
}

func TestHubSetupPersistedBaseURLUsesHubRegistryDomain(t *testing.T) {
	useHubSetupLocationsLoaderForTest(t, func(context.Context) ([]hubSetupLocation, error) {
		return []hubSetupLocation{
			{Display: "North America", Key: "na", Domain: "north-america.hub.molten.bot"},
			{Display: "Europe", Key: "eu", Domain: "europe.hub.molten.bot"},
		}, nil
	})

	if got, want := hubSetupPersistedBaseURL("http://127.0.0.1:41099/v1", "eu"), "https://europe.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupPersistedBaseURL(loopback, eu) = %q, want %q", got, want)
	}
}

func TestHubSetupRegionForBaseURLUsesHubRegistryLocations(t *testing.T) {
	useHubSetupLocationsLoaderForTest(t, func(context.Context) ([]hubSetupLocation, error) {
		return []hubSetupLocation{
			{Display: "North America", Key: "na", Domain: "north-america.hub.molten.bot"},
			{Display: "Europe", Key: "eu", Domain: "europe.hub.molten.bot"},
		}, nil
	})

	if got, want := hubSetupRegionForBaseURL("https://europe.hub.molten.bot/v1"), "eu"; got != want {
		t.Fatalf("hubSetupRegionForBaseURL(registry eu) = %q, want %q", got, want)
	}
	if got, want := hubSetupRegionForBaseURL("https://north-america.hub.molten.bot/v1"), "na"; got != want {
		t.Fatalf("hubSetupRegionForBaseURL(registry na) = %q, want %q", got, want)
	}
}

func TestHubSetupLocationsFallbackWhenRegistryFails(t *testing.T) {
	useHubSetupLocationsLoaderForTest(t, func(context.Context) ([]hubSetupLocation, error) {
		return nil, errors.New("registry unavailable")
	})

	if got, want := hubSetupBaseURL("", "eu"), "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("hubSetupBaseURL(fallback, eu) = %q, want %q", got, want)
	}
	if got, want := hubSetupRegionForBaseURL("https://na.hub.molten.bot/v1"), "na"; got != want {
		t.Fatalf("hubSetupRegionForBaseURL(fallback na) = %q, want %q", got, want)
	}
}

func TestFetchHubSetupLocationsNormalizesRegistryResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/hubs.json" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"display":"North America","key":"NA","domain":"https://north-america.hub.molten.bot/"},
			{"display":"Europe","key":"eu","domain":"europe.hub.molten.bot/"}
		]`))
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/hubs.json", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	var raw []hubSetupLocation
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	got := cloneHubSetupLocations(raw)
	want := []hubSetupLocation{
		{Display: "North America", Key: "na", Domain: "north-america.hub.molten.bot"},
		{Display: "Europe", Key: "eu", Domain: "europe.hub.molten.bot"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cloneHubSetupLocations() = %#v, want %#v", got, want)
	}
}

func TestConfigureHubSetupRejectsShortToken(t *testing.T) {
	t.Parallel()

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		RuntimeConfigPath: filepath.Join(t.TempDir(), ".moltenhub", "config.json"),
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Token:     "too-short",
	}, nil)
	if err == nil {
		t.Fatal("configureHubSetup() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("configureHubSetup() err = %q, want token length failure", err)
	}
	if state.Configured {
		t.Fatal("Configured = true, want false")
	}
}

func TestConfigureHubSetupRejectsUnboundAgentWithoutWritingConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Token:     fakeHubSetupToken("t_"),
	}, nil)
	if err == nil {
		t.Fatal("configureHubSetup() error = nil, want non-nil")
	}
	if got := err.Error(); got != "agent harness is not configured; complete agent selection in the UI" {
		t.Fatalf("configureHubSetup() error = %q, want unbound agent error", got)
	}
	if state.Configured {
		t.Fatal("Configured = true, want false")
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("config file stat error = %v, want not exist", statErr)
	}
}

func TestConfigureHubSetupExistingAgentIgnoresStatusUpdateFailuresDuringVerification(t *testing.T) {
	t.Parallel()

	agentToken := fakeHubSetupToken("t_")

	var (
		getCalls    int
		statusCalls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			getCalls++
			_, _ = w.Write([]byte(`{"handle":"existing-agent","profile":{"display_name":"Existing Agent","emoji":"🤖","profile":"Owns automation"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			statusCalls++
			http.Error(w, `{"error":"invalid status payload"}`, http.StatusBadRequest)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/me/status":
			statusCalls++
			http.Error(w, `{"error":"invalid status payload"}`, http.StatusBadRequest)
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) &&
			(r.URL.Path == "/v1/agents/me" || r.URL.Path == "/v1/agents/me/metadata"):
			statusCalls++
			http.Error(w, `{"error":"status update not allowed"}`, http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Token:     agentToken,
	}, nil)
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if getCalls < 2 {
		t.Fatalf("GET /agents/me calls = %d, want at least 2", getCalls)
	}
	if statusCalls == 0 {
		t.Fatal("expected status update attempts during existing-agent setup")
	}
	if !state.Configured {
		t.Fatal("Configured = false, want true")
	}
	if got, want := strings.TrimSpace(state.Handle), "existing-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
}

func TestConfigureHubSetupExistingAgentReturnsLoginVerificationFailure(t *testing.T) {
	t.Parallel()

	agentToken := fakeHubSetupToken("t_")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && (r.URL.Path == "/v1/agents/bind-tokens" || r.URL.Path == "/v1/agents/bind"):
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) &&
			(r.URL.Path == "/v1/agents/me/status" || r.URL.Path == "/v1/agents/me" || r.URL.Path == "/v1/agents/me/metadata"):
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: filepath.Join(t.TempDir(), ".moltenhub", "config.json"),
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Token:     agentToken,
	}, nil)
	if err == nil {
		t.Fatal("configureHubSetup() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "load hub profile") {
		t.Fatalf("configureHubSetup() err = %q, want profile load context", err)
	}
	if !strings.Contains(err.Error(), "status=401") {
		t.Fatalf("configureHubSetup() err = %q, want HTTP status details", err)
	}
	if state.Configured {
		t.Fatal("Configured = true, want false")
	}
}

func TestConfigureHubSetupExistingAgentProfileEditSkipsSyncWhenRemoteAlreadyMatches(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	var (
		syncCalls   int
		profileGets int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("%s %s token = %q, want %q", r.Method, r.URL.Path, got, savedToken)
		}
		switch {
		case (r.Method == http.MethodPost || r.Method == http.MethodPatch) &&
			(r.URL.Path == "/v1/agents/me/metadata" || r.URL.Path == "/v1/agents/me"):
			syncCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			profileGets++
			_, _ = w.Write([]byte(`{"handle":"existing-agent","profile":{"display_name":"Molten Bot","emoji":"⚙️","profile":"Owns hub edits"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"base_url":%q,"agent_token":%q,"profile":{"display_name":"Old Name","emoji":"🤖","profile":"Old bio"}}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Profile: struct {
			ProfileText string `json:"profile"`
			DisplayName string `json:"display_name"`
			Emoji       string `json:"emoji"`
		}{
			ProfileText: "Owns hub edits",
			DisplayName: "Molten Bot",
			Emoji:       "⚙️",
		},
	}, nil)
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if syncCalls != 0 {
		t.Fatalf("profile sync requests = %d, want 0 when submitted profile already matches Hub profile", syncCalls)
	}
	if profileGets == 0 {
		t.Fatal("expected profile lookup before deciding whether to sync")
	}
	if got, want := state.Profile.DisplayName, "Molten Bot"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "⚙️"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Owns hub edits"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestConfigureHubSetupSavedCredentialsWithoutProfileChangesSkipsProfileSync(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	var (
		getCalls    int
		syncCalls   int
		statusCalls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("%s %s token = %q, want %q", r.Method, r.URL.Path, got, savedToken)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			getCalls++
			_, _ = w.Write([]byte(`{"handle":"existing-agent","profile":{"display_name":"Remote Agent","emoji":"🚀","profile":"Loaded from hub"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			statusCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		case (r.Method == http.MethodPost || r.Method == http.MethodPatch) &&
			(r.URL.Path == "/v1/agents/me/metadata" || r.URL.Path == "/v1/agents/me"):
			syncCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"base_url":%q,"agent_token":%q}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
	}, nil)
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if syncCalls != 0 {
		t.Fatalf("profile sync calls = %d, want 0 when no profile changes were requested", syncCalls)
	}
	if statusCalls == 0 {
		t.Fatal("expected online status verification call")
	}
	if getCalls < 2 {
		t.Fatalf("GET /agents/me calls = %d, want at least 2", getCalls)
	}
	if got, want := state.Handle, "existing-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := state.Profile.DisplayName, "Remote Agent"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "🚀"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Loaded from hub"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestConfigureHubSetupExistingAgentProfileEditKeepsRequestedValuesWhenReadbackStale(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	var syncCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("%s %s token = %q, want %q", r.Method, r.URL.Path, got, savedToken)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			// Simulate eventual consistency: readback remains stale immediately after PATCH.
			_, _ = w.Write([]byte(`{"handle":"existing-agent","profile":{"display_name":"Old Name","emoji":"🤖","profile":"Old bio"}}`))
		case (r.Method == http.MethodPost || r.Method == http.MethodPatch) &&
			(r.URL.Path == "/v1/agents/me/metadata" || r.URL.Path == "/v1/agents/me"):
			bodyBytes, _ := io.ReadAll(r.Body)
			body := string(bodyBytes)
			if !strings.Contains(body, `"display_name":"Molten Bot"`) {
				t.Fatalf("profile sync missing updated display_name: %s", body)
			}
			if !strings.Contains(body, `"emoji":"⚙️"`) {
				t.Fatalf("profile sync missing updated emoji: %s", body)
			}
			if !strings.Contains(body, `"profile":"Owns hub edits"`) {
				t.Fatalf("profile sync missing updated profile text: %s", body)
			}
			syncCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"base_url":%q,"agent_token":%q,"handle":"existing-agent","profile":{"display_name":"Old Name","emoji":"🤖","profile":"Old bio"}}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Handle:    "existing-agent",
		Profile: struct {
			ProfileText string `json:"profile"`
			DisplayName string `json:"display_name"`
			Emoji       string `json:"emoji"`
		}{
			ProfileText: "Owns hub edits",
			DisplayName: "Molten Bot",
			Emoji:       "⚙️",
		},
	}, nil)
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if syncCalls == 0 {
		t.Fatal("expected profile sync request")
	}
	if got, want := state.Profile.DisplayName, "Molten Bot"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "⚙️"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Owns hub edits"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestConfigureHubSetupExistingAgentLoadsProfileTextFromProfileMarkdown(t *testing.T) {
	t.Parallel()

	savedToken := fakeHubSetupToken("saved-")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != savedToken {
			t.Fatalf("%s %s token = %q, want %q", r.Method, r.URL.Path, got, savedToken)
		}
		switch {
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) &&
			(r.URL.Path == "/v1/agents/me/metadata" || r.URL.Path == "/v1/agents/me"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			_, _ = w.Write([]byte(`{"result":{"agent":{"handle":"existing-agent","metadata":{"display_name":"Molten Bot","emoji":"💯","profile_markdown":"# 💯 Molten Bot\n\nOwns markdown profile text"}}}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"base_url":%q,"agent_token":%q}`, server.URL+"/v1", savedToken)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
	}, nil)
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if got, want := state.Profile.DisplayName, "Molten Bot"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "💯"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Owns markdown profile text"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestConfigureHubSetupReturnsSavedStateWhenLiveApplyFails(t *testing.T) {
	t.Parallel()

	agentToken := fakeHubSetupToken("t_")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me" {
			_, _ = w.Write([]byte(`{"handle":"existing-agent","profile":{"display_name":"Existing Agent","emoji":"🤖","profile":"Owns automation"}}`))
			return
		}
		if r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Token:     agentToken,
	}, func(context.Context, hub.InitConfig) error {
		return errors.New("hub daemon start failed")
	})
	if err == nil {
		t.Fatal("configureHubSetup() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "apply live hub setup") {
		t.Fatalf("configureHubSetup() err = %q, want live apply failure", err)
	}
	if !state.Configured {
		t.Fatal("Configured = false, want true because save completed")
	}
	if !strings.Contains(state.Message, "live apply failed") {
		t.Fatalf("Message = %q, want live apply failure detail", state.Message)
	}
}

func TestConnectHubSetupUsesSavedRuntimeConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": "v1",
  "base_url": "https://eu.hub.molten.bot/v1",
  "agent_token": "agent_saved",
  "handle": "builder",
  "profile": {
    "display_name": "Builder",
    "emoji": "🔥",
    "bio": "Ships UI work"
  },
  "timeout_ms": 20000
}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var liveCfg hub.InitConfig
	state, err := connectHubSetup(context.Background(), hub.InitConfig{
		RuntimeConfigPath: configPath,
	}, func(_ context.Context, cfg hub.InitConfig) error {
		liveCfg = cfg
		return nil
	})
	if err != nil {
		t.Fatalf("connectHubSetup() error = %v", err)
	}
	if got, want := liveCfg.AgentToken, "agent_saved"; got != want {
		t.Fatalf("liveCfg.AgentToken = %q, want %q", got, want)
	}
	if got, want := liveCfg.BaseURL, "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("liveCfg.BaseURL = %q, want %q", got, want)
	}
	if got, want := state.Message, "Molten Hub connected."; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
}

func TestCurrentHubSetupStateUsesLiveCredentialsForConfiguredMode(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": "v1",
  "timeout_ms": 20000
}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupState(hub.InitConfig{
		BaseURL:           "https://eu.hub.molten.bot/v1",
		AgentToken:        "live_agent_token",
		Handle:            "live-agent",
		RuntimeConfigPath: configPath,
		Profile: hub.ProfileConfig{
			DisplayName: "Live Agent",
			Emoji:       "⚙️",
			ProfileText: "Owns connected hub sessions",
		},
	})

	if !state.Configured {
		t.Fatal("Configured = false, want true")
	}
	if got, want := state.Region, "eu"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got, want := state.Handle, "live-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := state.Profile.DisplayName, "Live Agent"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := state.Profile.Emoji, "⚙️"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := state.Profile.ProfileText, "Owns connected hub sessions"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
	if got, want := state.AgentMode, "existing"; got != want {
		t.Fatalf("AgentMode = %q, want %q", got, want)
	}
	if got, want := state.TokenType, "agent"; got != want {
		t.Fatalf("TokenType = %q, want %q", got, want)
	}
	if !state.ActivationReady {
		t.Fatal("ActivationReady = false, want true")
	}
}

func TestCurrentHubSetupStateUsesSavedBaseURLForRegionWhenCredentialsAreMissing(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": "v1",
  "base_url": "https://eu.hub.molten.bot/v1",
  "timeout_ms": 20000
}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := currentHubSetupState(hub.InitConfig{
		BaseURL:           "https://na.hub.molten.bot/v1",
		RuntimeConfigPath: configPath,
	})

	if state.Configured {
		t.Fatal("Configured = true, want false")
	}
	if got, want := state.Region, "eu"; got != want {
		t.Fatalf("Region = %q, want %q", got, want)
	}
	if got := state.Message; got != "" {
		t.Fatalf("Message = %q, want empty", got)
	}
}

func TestConfigureHubSetupFallsBackToLiveInitCredentialsWhenRuntimeConfigOmitsTokens(t *testing.T) {
	t.Parallel()

	liveToken := fakeHubSetupToken("live-")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); got != liveToken {
			t.Fatalf("%s %s token = %q, want %q", r.Method, r.URL.Path, got, liveToken)
		}
		switch {
		case (r.Method == http.MethodPost || r.Method == http.MethodPatch) &&
			(r.URL.Path == "/v1/agents/me/metadata" || r.URL.Path == "/v1/agents/me"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			_, _ = w.Write([]byte(`{"handle":"live-agent","profile":{"display_name":"Live Agent","emoji":"⚙️","profile":"Owns connected hub sessions"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me/status":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": "v1",
  "timeout_ms": 20000
}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		AgentToken:        liveToken,
		AgentHarness:      "codex",
		RuntimeConfigPath: configPath,
	}, hubui.HubSetupRequest{
		AgentMode: "existing",
		Profile: struct {
			ProfileText string `json:"profile"`
			DisplayName string `json:"display_name"`
			Emoji       string `json:"emoji"`
		}{
			ProfileText: "Owns connected hub sessions",
			DisplayName: "Live Agent",
			Emoji:       "⚙️",
		},
	}, nil)
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if !state.Configured {
		t.Fatal("Configured = false, want true")
	}
	if got, want := state.Handle, "live-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
}

func TestDisconnectHubSetupStopsLiveRuntime(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": "v1",
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_token": "agent_saved",
  "bind_token": "bind_saved",
  "handle": "live-agent",
  "agent_harness": "codex",
  "github_token": "github_token_saved",
  "profile": {
    "display_name": "Live Agent",
    "emoji": "⚙️",
    "profile": "Owns connected hub sessions"
  },
  "timeout_ms": 20000
}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stopCalls := 0
	state, err := disconnectHubSetup(context.Background(), hub.InitConfig{
		RuntimeConfigPath: configPath,
	}, func(context.Context) error {
		stopCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("disconnectHubSetup() error = %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("stopCalls = %d, want 1", stopCalls)
	}
	if state.Configured {
		t.Fatal("Configured = true, want false after disconnect")
	}
	if got, want := state.Message, "Molten Hub disconnected."; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, key := range []string{"agent_token", "bind_token", "handle", "profile"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("doc[%q] still present after disconnect: %#v", key, doc[key])
		}
	}
	if got, want := doc["github_token"], "github_token_saved"; got != want {
		t.Fatalf("github_token = %#v, want %q", got, want)
	}
	if got, want := doc["agent_harness"], "codex"; got != want {
		t.Fatalf("agent_harness = %#v, want %q", got, want)
	}
}
