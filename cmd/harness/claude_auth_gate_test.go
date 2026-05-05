package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

func TestClaudeAuthGateRequiresGitHubConfigureWhenTokenIsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	g := newClaudeAuthGate(context.Background(), "", nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Harness != agentruntime.HarnessClaude || status.Ready || !status.Required {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.State, "needs_configure"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := status.ConfigureCommand, claudeGitHubConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	if got, want := status.ConfigurePlaceholder, claudeGitHubConfigurePlaceholder; got != want {
		t.Fatalf("ConfigurePlaceholder = %q, want %q", got, want)
	}
	if !strings.Contains(status.Message, "GitHub token is required") {
		t.Fatalf("message = %q", status.Message)
	}
}

func TestClaudeAuthGateRequiresCredentialsConfigureWhenClaudeCredentialsAreMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	g := newClaudeAuthGate(context.Background(), "", nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.State, "needs_configure"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := status.ConfigureCommand, claudeCredentialsConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	if got, want := status.ConfigurePlaceholder, claudeCredentialsConfigurePlaceholder; got != want {
		t.Fatalf("ConfigurePlaceholder = %q, want %q", got, want)
	}
	if got := status.AuthURL; got != "" {
		t.Fatalf("AuthURL = %q, want empty until login command emits a browser URL", got)
	}
	if !strings.Contains(status.Message, "Paste `~/.claude/.credentials.json`") {
		t.Fatalf("message = %q", status.Message)
	}
}

func TestClaudeAuthGateRequiresClaudeCredentialsAfterValidatedGITHUBTOKEN(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github_token_ready")

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, "gh"; got != want {
				t.Fatalf("command = %q, want %q", got, want)
			}
			if got, want := strings.Join(cmd.Args, " "), "auth status"; got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_ready"; got != want {
				t.Fatalf("GITHUB_TOKEN = %q, want %q during validation", got, want)
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}

	g := newClaudeAuthGateWithContextAndConfigAndRunner(context.Background(), runner, "", "", hub.InitConfig{}, nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.State, "needs_configure"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := status.ConfigureCommand, claudeCredentialsConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	if got := status.ConfigureCommand; got == claudeGitHubConfigureCommand {
		t.Fatalf("ConfigureCommand = %q, want Claude credentials flow after GitHub validation", got)
	}
}

func TestClaudeAuthGateRecognizesEnvironmentCredentials(t *testing.T) {
	t.Run("api-key", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "github_token_ready")
		t.Setenv("ANTHROPIC_API_KEY", "test-key")
		g := newClaudeAuthGate(context.Background(), "", nil)
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if got, want := status.Message, "Claude Code and GitHub token are ready."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	t.Run("auth-token", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "github_token_ready")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "token")
		g := newClaudeAuthGate(context.Background(), "", nil)
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if got, want := status.Message, "Claude Code and GitHub token are ready."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	t.Run("cloud-provider", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "github_token_ready")
		t.Setenv("CLAUDE_CODE_USE_BEDROCK", "true")
		g := newClaudeAuthGate(context.Background(), "", nil)
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if got, want := status.Message, "Claude Code and GitHub token are ready."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})
}

func TestClaudeAuthGateRecognizesCredentialFile(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	credentialsPath := filepath.Join(configDir, ".credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("GH_TOKEN", "github_token_ready")

	g := newClaudeAuthGate(context.Background(), "", nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.Message, "Claude Code and GitHub token are ready."; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestClaudeAuthGateRecognizesCredentialFileFromHomeConfigDirectory(t *testing.T) {
	homeDir := t.TempDir()
	credentialsPath := filepath.Join(homeDir, ".config", "claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(credentialsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(credentialsPath, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("HOME", homeDir)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	g := newClaudeAuthGate(context.Background(), "", nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.Message, "Claude Code and GitHub token are ready."; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestClaudeAuthGateRecognizesCLIAuthStatusWhenLoggedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	cmdPath := filepath.Join(t.TempDir(), "claude-auth-status-ready.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  echo '{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}'
  exit 0
fi
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "ready") {
		t.Fatalf("message = %q, want readiness message", status.Message)
	}
}

func clearClaudeCredentialSignals(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv(claudeOAuthTokenEnv, "")
}

func TestClaudeAuthGateConfigurePersistsGitHubTokenAndEnvironment(t *testing.T) {
	clearClaudeCredentialSignals(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newClaudeAuthGateWithConfig("", path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: agentruntime.HarnessClaude,
	})

	githubToken := fakeGitHubPAT("saved_token")
	status, err := g.Configure(context.Background(), githubToken)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if got, want := status.State, "needs_configure"; got != want {
		t.Fatalf("State = %q, want %q (Claude credentials are configured separately)", got, want)
	}
	if got, want := status.ConfigureCommand, claudeCredentialsConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GH_TOKEN"), githubToken; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), githubToken; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["github_token"], githubToken; got != want {
		t.Fatalf("github_token = %#v, want %q", got, want)
	}
}

func TestClaudeAuthGateConfigureAcceptsCredentialsJSONInConfigureState(t *testing.T) {
	clearClaudeCredentialSignals(t)
	t.Setenv("GH_TOKEN", "github_token_ready")
	t.Setenv("GITHUB_TOKEN", "github_token_ready")
	t.Setenv(claudeOAuthTokenEnv, "")
	claudeConfigDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDir)

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := &claudeAuthGate{
		baseCtx:           context.Background(),
		required:          true,
		state:             "needs_configure",
		message:           "auth pending",
		runtimeConfigPath: path,
		initCfg: hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentHarness: agentruntime.HarnessClaude,
			GitHubToken:  "github_token_ready",
		},
		updatedAt: time.Now().UTC(),
		logf:      func(string, ...any) {},
	}

	claudeOAuthToken := "sk-" + "ant-oat01-manual-token-123456"
	raw := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q}}`, claudeOAuthToken)
	status, err := g.Configure(context.Background(), raw)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %+v, want ready", status)
	}
	if got, want := os.Getenv(claudeOAuthTokenEnv), claudeOAuthToken; got != want {
		t.Fatalf("%s = %q, want %q", claudeOAuthTokenEnv, got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["claude_code_oauth_token"], claudeOAuthToken; got != want {
		t.Fatalf("claude_code_oauth_token = %#v, want %q", got, want)
	}

	credentialsPath := filepath.Join(claudeConfigDir, ".credentials.json")
	data, err = os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", credentialsPath, err)
	}
	if got, want := strings.TrimSpace(string(data)), raw; got != want {
		t.Fatalf("credentials json = %q, want %q", got, want)
	}
}

func TestClaudeAuthGateConfigureAcceptsUnknownCredentialsJSONSchemaInConfigureState(t *testing.T) {
	clearClaudeCredentialSignals(t)
	t.Setenv("GH_TOKEN", "github_token_ready")
	t.Setenv("GITHUB_TOKEN", "github_token_ready")
	t.Setenv(claudeOAuthTokenEnv, "")
	claudeConfigDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDir)

	g := &claudeAuthGate{
		baseCtx:  context.Background(),
		required: true,
		state:    "needs_configure",
		message:  "auth pending",
		initCfg: hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentHarness: agentruntime.HarnessClaude,
			GitHubToken:  "github_token_ready",
		},
		updatedAt: time.Now().UTC(),
		logf:      func(string, ...any) {},
	}

	raw := `{"futureSchema":{"oauth":{"tokenEnvelope":{"value":"opaque"}}},"meta":{"version":2}}`
	status, err := g.Configure(context.Background(), raw)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %+v, want ready", status)
	}
	if got := os.Getenv(claudeOAuthTokenEnv); got != "" {
		t.Fatalf("%s = %q, want empty", claudeOAuthTokenEnv, got)
	}

	credentialsPath := filepath.Join(claudeConfigDir, ".credentials.json")
	data, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", credentialsPath, err)
	}
	if got, want := strings.TrimSpace(string(data)), raw; got != want {
		t.Fatalf("credentials json = %q, want %q", got, want)
	}
}

func TestClaudeAuthGateConfigureRejectsInvalidCredentialsJSONInConfigureState(t *testing.T) {
	clearClaudeCredentialSignals(t)
	t.Setenv("GH_TOKEN", "github_token_ready")
	t.Setenv("GITHUB_TOKEN", "github_token_ready")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	g := &claudeAuthGate{
		baseCtx:  context.Background(),
		required: true,
		state:    "needs_configure",
		message:  "auth pending",
		initCfg: hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentHarness: agentruntime.HarnessClaude,
			GitHubToken:  "github_token_ready",
		},
		updatedAt: time.Now().UTC(),
		logf:      func(string, ...any) {},
	}

	status, err := g.Configure(context.Background(), `{"claudeAiOauth":`)
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if got := status.State; got != "needs_configure" {
		t.Fatalf("status.State = %q, want %q", got, "needs_configure")
	}
	if !strings.Contains(status.Message, "Claude credentials JSON is invalid:") {
		t.Fatalf("status.Message = %q, want invalid JSON details", status.Message)
	}
	if !strings.Contains(err.Error(), "invalid claude credentials json") {
		t.Fatalf("Configure() error = %q, want invalid claude credentials json", err)
	}
}

func TestClaudeAuthGatePendingBrowserLoginFailureNilGate(t *testing.T) {
	var g *claudeAuthGate

	wantErr := context.DeadlineExceeded
	status, err := g.pendingBrowserLoginFailure("ignored", wantErr)
	if err != wantErr {
		t.Fatalf("pendingBrowserLoginFailure() error = %v, want %v", err, wantErr)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("pendingBrowserLoginFailure() status = %+v", status)
	}
}

func TestClaudeAuthGateStartDeviceAuthRunsLoginAndCapturesURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	cmdPath := filepath.Join(t.TempDir(), "claude-login-stub.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  echo "unexpected args: $*" >&2
  exit 64
fi
echo "Select login method:"
if ! read choice; then
  echo "read failed" >&2
  exit 2
fi
echo "Open browser:"
echo "https://claude.ai/login/device?flow=test"
sleep 0.1
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := &claudeAuthGate{
		baseCtx:   context.Background(),
		command:   cmdPath,
		required:  true,
		state:     "needs_browser_login",
		message:   "auth required",
		updatedAt: time.Now().UTC(),
		logf:      func(string, ...any) {},
	}

	status, err := g.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if status.State != "pending_browser_login" {
		t.Fatalf("initial status = %+v", status)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "https://claude.ai/login/device?flow=test")
	})

	status, err = g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.AuthURL, "https://claude.ai/login/device?flow=test"; got != want {
		t.Fatalf("AuthURL = %q, want %q", got, want)
	}
	if status.Ready {
		t.Fatalf("status = %+v, want not ready", status)
	}
	if got, want := status.State, "pending_browser_login"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
}

func TestClaudeAuthGateVerifyDoesNotStartLoginWhenCredentialsNeedConfigure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	cmdPath := filepath.Join(t.TempDir(), "claude-login-verify-stub.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 1
fi
echo "unexpected args: $*" >&2
exit 64
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	status, err := g.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if status.State != "needs_configure" {
		t.Fatalf("Verify() status = %+v, want needs_configure", status)
	}
	if status.ConfigureCommand != claudeCredentialsConfigureCommand {
		t.Fatalf("ConfigureCommand = %q, want %q", status.ConfigureCommand, claudeCredentialsConfigureCommand)
	}
}

func TestClaudeAuthGateStartDeviceAuthSendsInitialPromptAdvance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	cmdPath := filepath.Join(t.TempDir(), "claude-login-initial-enter.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
if ! read choice; then
  echo "read failed" >&2
  exit 3
fi
echo "https://claude.ai/login/device?flow=initial-enter"
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := &claudeAuthGate{
		baseCtx:   context.Background(),
		command:   cmdPath,
		required:  true,
		state:     "needs_browser_login",
		message:   "auth required",
		updatedAt: time.Now().UTC(),
		logf:      func(string, ...any) {},
	}

	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "https://claude.ai/login/device?flow=initial-enter")
	})
}

func TestClaudeAuthGateConfigureSubmitsBrowserCodeToRunningLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	capturedPath := filepath.Join(t.TempDir(), "captured-code.txt")
	cmdPath := filepath.Join(t.TempDir(), "claude-login-submit-code.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
echo "Select login method:"
if ! read choice; then
  exit 2
fi
echo "Open browser:"
echo "https://claude.ai/login/device?flow=submit-code"
if ! read authcode; then
  exit 3
fi
printf "%s" "$authcode" > "`+capturedPath+`"
sleep 1
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "https://claude.ai/login/device?flow=submit-code")
	})

	const submittedCode = "7zhfhHRjHhqZKEwd0T3hN4npff2bJOBx0NJYCWSggMrzXlTi#O_nyYiogHRbCqVf0kOk0oSWejds77rLgKVbzVCcenKQ"
	status, err := g.Configure(context.Background(), submittedCode)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if got, want := status.State, "pending_browser_login"; got != want {
		t.Fatalf("Configure() state = %q, want %q", got, want)
	}
	if !strings.Contains(strings.ToLower(status.Message), "credentials.json") {
		t.Fatalf("Configure() message = %q, want fallback credentials hint", status.Message)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(capturedPath)
		if err != nil {
			return false
		}
		return string(data) == submittedCode
	})
}

func TestClaudeAuthGateConfigureNormalizesBrowserCodeWhitespace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	capturedPath := filepath.Join(t.TempDir(), "captured-code-normalized.txt")
	cmdPath := filepath.Join(t.TempDir(), "claude-login-submit-code-normalized.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
echo "Select login method:"
if ! read choice; then
  exit 2
fi
echo "Open browser:"
echo "https://claude.ai/login/device?flow=submit-code-normalized"
if ! read authcode; then
  exit 3
fi
printf "%s" "$authcode" > "`+capturedPath+`"
sleep 1
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "https://claude.ai/login/device?flow=submit-code-normalized")
	})

	rawPastedCode := "  code-from-\n  claude \t browser  "
	status, err := g.Configure(context.Background(), rawPastedCode)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if got, want := status.State, "pending_browser_login"; got != want {
		t.Fatalf("Configure() state = %q, want %q", got, want)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(capturedPath)
		if err != nil {
			return false
		}
		return string(data) == "code-from-claudebrowser"
	})
}

func TestClaudeAuthGateConfigureAppendsOAuthStateWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	capturedPath := filepath.Join(t.TempDir(), "captured-code-with-state.txt")
	cmdPath := filepath.Join(t.TempDir(), "claude-login-submit-code-with-state.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
echo "Select login method:"
if ! read choice; then
  exit 2
fi
echo "Open browser:"
echo "https://claude.ai/oauth/authorize?code=true&state=test-state-123"
if ! read authcode; then
  exit 3
fi
printf "%s" "$authcode" > "`+capturedPath+`"
sleep 1
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "state=test-state-123")
	})

	const submittedCode = "code-from-browser-only"
	if _, err := g.Configure(context.Background(), submittedCode); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(capturedPath)
		if err != nil {
			return false
		}
		return string(data) == submittedCode+"#test-state-123"
	})
}

func TestClaudeAuthGateVerifyResubmitsPendingBrowserCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	capturedPath := filepath.Join(t.TempDir(), "captured-resubmitted-code.txt")
	cmdPath := filepath.Join(t.TempDir(), "claude-login-resubmit-code.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
echo "Select login method:"
if ! read choice; then
  exit 2
fi
echo "Open browser:"
echo "https://claude.ai/oauth/authorize?code=true&state=resubmit-state"
if ! read discard; then
  exit 3
fi
if ! read authcode; then
  exit 4
fi
printf "%s" "$authcode" > "`+capturedPath+`"
sleep 1
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "state=resubmit-state")
	})

	if _, err := g.Configure(context.Background(), "resubmit-code"); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	// Let resubmit throttle elapse, then verify to trigger resend while pending.
	time.Sleep(1300 * time.Millisecond)
	if _, err := g.Verify(context.Background()); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(capturedPath)
		if err != nil {
			return false
		}
		return string(data) == "resubmit-code#resubmit-state"
	})
}

func TestClaudeAuthGateVerifyCompletesWhenCredentialsAreReadyBeforeLoginExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claudeConfigDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDir)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "github_token_ready")

	capturedPath := filepath.Join(t.TempDir(), "captured-code.txt")
	cmdPath := filepath.Join(t.TempDir(), "claude-login-verify-ready.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
echo "Choose account:"
if ! read choice; then
  exit 2
fi
echo "Continue at https://claude.ai/login/verify-ready"
if ! read authcode; then
  exit 3
fi
printf "%s" "$authcode" > "`+capturedPath+`"
mkdir -p "$CLAUDE_CONFIG_DIR"
printf '{"accessToken":"ok"}' > "$CLAUDE_CONFIG_DIR/.credentials.json"
sleep 30
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g := newClaudeAuthGate(baseCtx, cmdPath, nil)
	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "https://claude.ai/login/verify-ready")
	})

	const submittedCode = "auth-code-from-browser"
	if _, err := g.Configure(context.Background(), submittedCode); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(capturedPath)
		if err != nil {
			return false
		}
		return string(data) == submittedCode
	})

	var (
		status web.AgentAuthState
		err    error
	)
	waitForCondition(t, 5*time.Second, func() bool {
		status, err = g.Verify(context.Background())
		return err == nil && status.Ready
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("Verify() ready = false, want true; status=%+v", status)
	}
	if got, want := status.State, "ready"; got != want {
		t.Fatalf("Verify() state = %q, want %q", got, want)
	}
	if status.AuthURL != "" {
		t.Fatalf("Verify() authURL = %q, want empty", status.AuthURL)
	}
}

func TestClaudeAuthHelpers(t *testing.T) {
	t.Parallel()

	if got, want := normalizeClaudeBrowserCode("  code-from-\n claude \t browser  "), "code-from-claudebrowser"; got != want {
		t.Fatalf("normalizeClaudeBrowserCode() = %q, want %q", got, want)
	}
	jsonToken := "sk-" + "ant-oat01-json-token-123456"
	if got, want := extractClaudeOAuthTokenFromInput(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q}}`, jsonToken)), jsonToken; got != want {
		t.Fatalf("extractClaudeOAuthTokenFromInput(json) = %q, want %q", got, want)
	}
	directToken := "sk-" + "ant-oat01-direct-token-123456"
	if got, want := extractClaudeOAuthTokenFromInput(directToken), directToken; got != want {
		t.Fatalf("extractClaudeOAuthTokenFromInput(token) = %q, want %q", got, want)
	}
	if got := extractClaudeOAuthTokenFromInput("browserCodeWithoutPrefix#state-xyz"); got != "" {
		t.Fatalf("extractClaudeOAuthTokenFromInput(browser-code) = %q, want empty", got)
	}
	if got, want := extractClaudeAuthStateFromURL("https://claude.ai/oauth/authorize?code=true&state=abc123"), "abc123"; got != want {
		t.Fatalf("extractClaudeAuthStateFromURL() = %q, want %q", got, want)
	}
	if got, want := normalizeClaudeBrowserCodeForSubmission("code-only", "https://claude.ai/oauth/authorize?state=state-xyz"), "code-only#state-xyz"; got != want {
		t.Fatalf("normalizeClaudeBrowserCodeForSubmission(code-only) = %q, want %q", got, want)
	}
	if got, want := normalizeClaudeBrowserCodeForSubmission("https://platform.claude.com/oauth/code/callback?code=abc&state=xyz", ""), "abc#xyz"; got != want {
		t.Fatalf("normalizeClaudeBrowserCodeForSubmission(callback-url) = %q, want %q", got, want)
	}
	if got, want := normalizeClaudeBrowserCodeForSubmission("abc#xyz", "https://claude.ai/oauth/authorize?state=other"), "abc#xyz"; got != want {
		t.Fatalf("normalizeClaudeBrowserCodeForSubmission(existing-state) = %q, want %q", got, want)
	}
	if got, want := extractClaudeAuthURL("Use https://claude.ai/login/device?x=y); now"), "https://claude.ai/login/device?x=y"; got != want {
		t.Fatalf("extractClaudeAuthURL() = %q, want %q", got, want)
	}
	if got, want := extractClaudeAuthURL("If the browser didn't open, visit: https://claude.ai/oauth/authorize?code=true&client_id=abc123&scope=user%3Aprofile"), "https://claude.ai/oauth/authorize?code=true&client_id=abc123&scope=user%3Aprofile"; got != want {
		t.Fatalf("extractClaudeAuthURL() should parse oauth authorize URL: got %q, want %q", got, want)
	}
	if got, want := extractClaudeAuthURL("Read docs at https://code.claude.com/docs/en/authentication and sign in at https://claude.ai/login/device?flow=x"), "https://claude.ai/login/device?flow=x"; got != want {
		t.Fatalf("extractClaudeAuthURL() should ignore docs URL and keep login URL: got %q, want %q", got, want)
	}
	if got, want := extractClaudeAuthURL("\x1b]8;;https://claude.ai/login/device?flow=osc-test\x1b\\Authorize Claude\x1b]8;;\x1b\\"), "https://claude.ai/login/device?flow=osc-test"; got != want {
		t.Fatalf("extractClaudeAuthURL(osc-hyperlink) = %q, want %q", got, want)
	}
	if got := extractClaudeAuthURL("Read docs at https://code.claude.com/docs/en/authentication"); got != "" {
		t.Fatalf("extractClaudeAuthURL(docs-url-only) = %q, want empty", got)
	}
	if got := extractClaudeAuthURL("no url here"); got != "" {
		t.Fatalf("extractClaudeAuthURL(no-url) = %q, want empty", got)
	}

	for _, line := range []string{
		"Select login method:",
		"Choose account",
		"Press Enter to continue",
		"Which option would you like?",
	} {
		if !shouldAdvanceClaudeLoginPrompt(line) {
			t.Fatalf("shouldAdvanceClaudeLoginPrompt(%q) = false, want true", line)
		}
	}
	if shouldAdvanceClaudeLoginPrompt("Login URL: https://claude.ai/login") {
		t.Fatalf("shouldAdvanceClaudeLoginPrompt(url line) = true, want false")
	}
	for _, line := range []string{
		"Paste code here if prompted >",
		"Claude authentication code required",
		"if prompted >",
	} {
		if !shouldPromptForClaudeBrowserCode(line) {
			t.Fatalf("shouldPromptForClaudeBrowserCode(%q) = false, want true", line)
		}
	}
	if shouldPromptForClaudeBrowserCode("Open browser and continue") {
		t.Fatalf("shouldPromptForClaudeBrowserCode(non-prompt) = true, want false")
	}
	if !shouldCaptureClaudeOAuthToken("Your OAuth token (valid for 1 year):") {
		t.Fatalf("shouldCaptureClaudeOAuthToken() = false, want true")
	}
	if got, want := extractClaudeOAuthTokenCandidate("abc.DEF_123-xyz-token-value"), "abc.DEF_123-xyz-token-value"; got != want {
		t.Fatalf("extractClaudeOAuthTokenCandidate() = %q, want %q", got, want)
	}
	if got := extractClaudeOAuthTokenCandidate("https://claude.com/cai/oauth/authorize"); got != "" {
		t.Fatalf("extractClaudeOAuthTokenCandidate(url) = %q, want empty", got)
	}
	if got := claudeLoginArgs("claude"); len(got) != 2 || got[0] != "auth" || got[1] != "login" {
		t.Fatalf("claudeLoginArgs(claude) = %v, want [auth login]", got)
	}
	if got := claudeLoginArgs("/tmp/custom-claude-wrapper"); len(got) != 2 || got[0] != "auth" || got[1] != "login" {
		t.Fatalf("claudeLoginArgs(custom) = %v, want [auth login]", got)
	}
}

func TestAgentAuthGateFactorySelectsSupportedHarnesses(t *testing.T) {
	t.Parallel()

	if gate := newConcreteAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessCodex, Command: "codex"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newConcreteAgentAuthGate(codex) = nil")
	}
	if gate := newConcreteAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessClaude, Command: "claude"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newConcreteAgentAuthGate(claude) = nil")
	}
	if gate := newConcreteAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessAuggie, Command: "auggie"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newConcreteAgentAuthGate(auggie) = nil")
	}
	if gate := newConcreteAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessPi, Command: "pi"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newConcreteAgentAuthGate(pi) = nil")
	}
	if gate := newConcreteAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: "unknown"}, hub.InitConfig{}, nil); gate != nil {
		t.Fatalf("newConcreteAgentAuthGate(unknown) = %#v, want nil", gate)
	}
	if gate := newAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: "unknown"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newAgentAuthGate(unknown) = nil, want selectable wrapper")
	}
}
