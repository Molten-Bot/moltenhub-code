package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/hubui"
)

const claudeAuthDocsURL = "https://code.claude.com/docs/en/authentication"

type claudeAuthGate struct {
	command string
}

func newClaudeAuthGate(command string) *claudeAuthGate {
	command = strings.TrimSpace(command)
	if command == "" {
		command = agentruntime.HarnessClaude
	}
	return &claudeAuthGate{command: command}
}

func (g *claudeAuthGate) Status(_ context.Context) (hubui.AgentAuthState, error) {
	return g.currentState(), nil
}

func (g *claudeAuthGate) StartDeviceAuth(_ context.Context) (hubui.AgentAuthState, error) {
	return g.currentState(), nil
}

func (g *claudeAuthGate) Verify(_ context.Context) (hubui.AgentAuthState, error) {
	return g.currentState(), nil
}

func (g *claudeAuthGate) Configure(_ context.Context, _ string) (hubui.AgentAuthState, error) {
	return g.currentState(), fmt.Errorf("claude auth does not support manual config submission")
}

func (g *claudeAuthGate) currentState() hubui.AgentAuthState {
	ready, message := g.probe()
	state := "ready"
	if !ready {
		state = "needs_browser_login"
	}

	authURL := ""
	if !ready {
		authURL = claudeAuthDocsURL
	}

	return hubui.AgentAuthState{
		Harness:   agentruntime.HarnessClaude,
		Required:  true,
		Ready:     ready,
		State:     state,
		Message:   message,
		AuthURL:   authURL,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (g *claudeAuthGate) probe() (bool, string) {
	if enabled := firstClaudeEnabledProvider(); enabled != "" {
		return true, fmt.Sprintf("Claude Code is configured for %s credentials.", enabled)
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")) != "" {
		return true, "Claude Code is configured via ANTHROPIC_AUTH_TOKEN."
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return true, "Claude Code is configured via ANTHROPIC_API_KEY."
	}
	if path := claudeCredentialsPath(); path != "" {
		return true, fmt.Sprintf("Claude Code credentials were found at %s.", path)
	}

	return false, "Claude Code login is required. Run `claude`, complete the browser sign-in, then click Done."
}

func firstClaudeEnabledProvider() string {
	providers := []struct {
		flag string
		name string
	}{
		{flag: "CLAUDE_CODE_USE_BEDROCK", name: "Amazon Bedrock"},
		{flag: "CLAUDE_CODE_USE_VERTEX", name: "Google Vertex AI"},
		{flag: "CLAUDE_CODE_USE_FOUNDRY", name: "Microsoft Foundry"},
	}
	for _, provider := range providers {
		if envEnabled(provider.flag) {
			return provider.name
		}
	}
	return ""
}

func envEnabled(key string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch value {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func claudeCredentialsPath() string {
	base := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".claude")
	}
	path := filepath.Join(base, ".credentials.json")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return ""
	}
	return path
}
