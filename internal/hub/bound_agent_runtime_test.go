package hub

import (
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
)

func TestBoundAgentRuntimeRequiresConfiguredHarness(t *testing.T) {
	t.Parallel()

	_, err := BoundAgentRuntime(InitConfig{})
	if err == nil {
		t.Fatal("BoundAgentRuntime() error = nil, want non-nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "agent harness") {
		t.Fatalf("BoundAgentRuntime() error = %v, want agent harness guidance", err)
	}
}

func TestApplyBoundAgentRuntimeAppliesConfiguredDefaults(t *testing.T) {
	t.Parallel()

	runCfg, err := ApplyBoundAgentRuntime(config.Config{}, InitConfig{AgentHarness: "claude"})
	if err != nil {
		t.Fatalf("ApplyBoundAgentRuntime() error = %v", err)
	}
	if got, want := runCfg.AgentHarness, "claude"; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := runCfg.AgentCommand, "claude"; got != want {
		t.Fatalf("AgentCommand = %q, want %q", got, want)
	}
}

func TestApplyBoundAgentRuntimeAllowsUnboundRuntime(t *testing.T) {
	t.Parallel()

	input := config.Config{
		AgentHarness: "codex",
		AgentCommand: "codex",
	}
	runCfg, err := ApplyBoundAgentRuntime(input, InitConfig{})
	if err != nil {
		t.Fatalf("ApplyBoundAgentRuntime() error = %v", err)
	}
	if got, want := runCfg.AgentHarness, input.AgentHarness; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := runCfg.AgentCommand, input.AgentCommand; got != want {
		t.Fatalf("AgentCommand = %q, want %q", got, want)
	}
}

func TestApplyBoundAgentRuntimeRejectsConflictingHarnessOverride(t *testing.T) {
	t.Parallel()

	_, err := ApplyBoundAgentRuntime(
		config.Config{AgentHarness: "codex"},
		InitConfig{AgentHarness: "claude"},
	)
	if err == nil {
		t.Fatal("ApplyBoundAgentRuntime() error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "agentHarness") || !strings.Contains(got, "bound agent") {
		t.Fatalf("ApplyBoundAgentRuntime() error = %q, want harness conflict guidance", got)
	}
}

func TestApplyBoundAgentRuntimeRejectsConflictingCommandOverride(t *testing.T) {
	t.Parallel()

	_, err := ApplyBoundAgentRuntime(
		config.Config{AgentHarness: "claude", AgentCommand: "claude-alt"},
		InitConfig{AgentHarness: "claude", AgentCommand: "claude"},
	)
	if err == nil {
		t.Fatal("ApplyBoundAgentRuntime() error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "agentCommand") || !strings.Contains(got, "bound command") {
		t.Fatalf("ApplyBoundAgentRuntime() error = %q, want command conflict guidance", got)
	}
}

func TestApplyBoundAgentRuntimeAllowsMatchingOverrides(t *testing.T) {
	t.Parallel()

	runCfg, err := ApplyBoundAgentRuntime(
		config.Config{AgentHarness: "CLAUDE", AgentCommand: "claude"},
		InitConfig{AgentHarness: "claude"},
	)
	if err != nil {
		t.Fatalf("ApplyBoundAgentRuntime() error = %v", err)
	}
	if got, want := runCfg.AgentHarness, "claude"; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := runCfg.AgentCommand, "claude"; got != want {
		t.Fatalf("AgentCommand = %q, want %q", got, want)
	}
}

func TestApplyBoundAgentRuntimeReturnsResolveErrorForUnknownBoundHarness(t *testing.T) {
	t.Parallel()

	_, err := ApplyBoundAgentRuntime(
		config.Config{},
		InitConfig{AgentHarness: "unknown-harness"},
	)
	if err == nil {
		t.Fatal("ApplyBoundAgentRuntime() error = nil, want non-nil")
	}
	if got := strings.ToLower(err.Error()); !strings.Contains(got, "unsupported") {
		t.Fatalf("ApplyBoundAgentRuntime() error = %q, want unsupported harness detail", err.Error())
	}
}
