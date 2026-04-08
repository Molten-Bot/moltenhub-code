package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/hub"
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

func TestClaudeAuthGateRequiresBrowserLoginWhenClaudeCredentialsAreMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "ghp_ready")

	g := newClaudeAuthGate(context.Background(), "", nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.State, "needs_browser_login"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := status.AuthURL, claudeAuthDocsURL; got != want {
		t.Fatalf("AuthURL = %q, want %q", got, want)
	}
	if !strings.Contains(status.Message, "Run `claude auth login`") {
		t.Fatalf("message = %q", status.Message)
	}
}

func TestClaudeAuthGateRecognizesEnvironmentCredentials(t *testing.T) {
	t.Run("api-key", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "ghp_ready")
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
		t.Setenv("GH_TOKEN", "ghp_ready")
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
		t.Setenv("GH_TOKEN", "ghp_ready")
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
	t.Setenv("GH_TOKEN", "ghp_ready")

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

func TestClaudeAuthGateConfigurePersistsGitHubTokenAndEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newClaudeAuthGateWithConfig("", path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: agentruntime.HarnessClaude,
	})

	status, err := g.Configure(context.Background(), "ghp_saved_token")
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if status.State == "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv("GH_TOKEN"), "ghp_saved_token"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "ghp_saved_token"; got != want {
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
	if got, want := doc["github_token"], "ghp_saved_token"; got != want {
		t.Fatalf("github_token = %#v, want %q", got, want)
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
	t.Setenv("GH_TOKEN", "ghp_ready")

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

	waitForCondition(t, 2*time.Second, func() bool {
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

func TestClaudeAuthGateVerifyStartsLoginWhenNotReady(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")
	t.Setenv("GH_TOKEN", "ghp_ready")

	cmdPath := filepath.Join(t.TempDir(), "claude-login-verify-stub.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/bin/sh
if [ "$1" != "auth" ] || [ "$2" != "login" ]; then
  exit 64
fi
echo "Choose account:"
if ! read choice; then
  exit 3
fi
echo "Continue at https://claude.ai/login/verify-flow"
sleep 0.1
exit 1
`), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newClaudeAuthGate(context.Background(), cmdPath, nil)
	status, err := g.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if status.State != "pending_browser_login" {
		t.Fatalf("Verify() status = %+v, want pending_browser_login", status)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return strings.Contains(s.AuthURL, "https://claude.ai/login/verify-flow")
	})
}

func TestClaudeAuthHelpers(t *testing.T) {
	t.Parallel()

	if got, want := extractClaudeAuthURL("Use https://claude.ai/login/device?x=y); now"), "https://claude.ai/login/device?x=y"; got != want {
		t.Fatalf("extractClaudeAuthURL() = %q, want %q", got, want)
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
}

func TestAgentAuthGateFactorySelectsSupportedHarnesses(t *testing.T) {
	t.Parallel()

	if gate := newAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessCodex, Command: "codex"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newAgentAuthGate(codex) = nil")
	}
	if gate := newAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessClaude, Command: "claude"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newAgentAuthGate(claude) = nil")
	}
	if gate := newAgentAuthGate(context.Background(), nil, agentruntime.Runtime{Harness: agentruntime.HarnessAuggie, Command: "auggie"}, hub.InitConfig{}, nil); gate == nil {
		t.Fatal("newAgentAuthGate(auggie) = nil")
	}
}
