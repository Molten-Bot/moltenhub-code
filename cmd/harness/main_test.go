package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/hub"
)

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

func TestLoadHubBootConfigUsesDefaultRuntimeConfigWhenFlagsOmitted(t *testing.T) {
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
	if got, want := cfg.Repos, []string{"git@github.com:acme/repo-a.git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos = %v, want %v", got, want)
	}
	if cfg.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want %q", cfg.BaseBranch, "main")
	}
	if cfg.TargetSubdir != "." {
		t.Fatalf("TargetSubdir = %q, want %q", cfg.TargetSubdir, ".")
	}

	expectedLogDir := filepath.Join(logRoot, "local", "1712345678", "000001")
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

func TestFailureFollowUpReposUsesFailedResultRepoWhenItIdentifiesASingleRepo(t *testing.T) {
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
	if got, want := failureFollowUpRepos(failedResult, failedRunCfg), []string{"git@github.com:acme/failed-result.git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposPrefersSingleRepoFromResult(t *testing.T) {
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
	if got, want := failureFollowUpRepos(failedResult, failedRunCfg), []string{"git@github.com:acme/from-result.git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposFallsBackToFailedResultRepo(t *testing.T) {
	t.Parallel()

	failedResult := harness.Result{
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/from-result.git"},
		},
	}
	if got, want := failureFollowUpRepos(failedResult, config.Config{}), []string{"git@github.com:acme/from-result.git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposFallsBackToConfigRepoWhenResultIsAmbiguous(t *testing.T) {
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

	if got, want := failureFollowUpRepos(failedResult, failedRunCfg), []string{"git@github.com:acme/from-config.git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failureFollowUpRepos() = %v, want %v", got, want)
	}
}

func TestFailureFollowUpReposReturnsNilWhenNoRepoFound(t *testing.T) {
	t.Parallel()

	if got := failureFollowUpRepos(harness.Result{}, config.Config{}); got != nil {
		t.Fatalf("failureFollowUpRepos() = %v, want nil", got)
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
