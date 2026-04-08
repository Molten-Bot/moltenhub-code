package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/hub"
)

func TestClaudeAuthGateRequiresBrowserLoginWhenNoCredentialsExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "")

	g := newClaudeAuthGate("")
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Harness != agentruntime.HarnessClaude || status.Ready || !status.Required {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.State, "needs_browser_login"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := status.AuthURL, claudeAuthDocsURL; got != want {
		t.Fatalf("AuthURL = %q, want %q", got, want)
	}
	if !strings.Contains(status.Message, "Run `claude`") {
		t.Fatalf("message = %q", status.Message)
	}
}

func TestClaudeAuthGateRecognizesEnvironmentCredentials(t *testing.T) {
	t.Run("api-key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "test-key")
		g := newClaudeAuthGate("")
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if got, want := status.Message, "Claude Code is configured via ANTHROPIC_API_KEY."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	t.Run("auth-token", func(t *testing.T) {
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "token")
		g := newClaudeAuthGate("")
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if got, want := status.Message, "Claude Code is configured via ANTHROPIC_AUTH_TOKEN."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	t.Run("cloud-provider", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_USE_BEDROCK", "true")
		g := newClaudeAuthGate("")
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if got, want := status.Message, "Claude Code is configured for Amazon Bedrock credentials."; got != want {
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

	g := newClaudeAuthGate("")
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, credentialsPath) {
		t.Fatalf("message = %q", status.Message)
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
