package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveRuntimeConfigAgentRuntimeCreatesConfigFromInitWhenMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := SaveRuntimeConfigAgentRuntime(path, InitConfig{BaseURL: "https://na.hub.molten.bot/v1"}, "claude", ""); err != nil {
		t.Fatalf("SaveRuntimeConfigAgentRuntime() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["agent_harness"] != "claude" {
		t.Fatalf("agent_harness = %#v, want %q", got["agent_harness"], "claude")
	}
	if got["agent_command"] != "claude" {
		t.Fatalf("agent_command = %#v, want %q", got["agent_command"], "claude")
	}
}

func TestSaveRuntimeConfigAgentRuntimeRejectsHarnessSwitch(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"agent_harness":"claude","agent_command":"claude"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := SaveRuntimeConfigAgentRuntime(path, InitConfig{}, "codex", "")
	if err == nil {
		t.Fatal("SaveRuntimeConfigAgentRuntime() error = nil, want non-nil")
	}
	if got := strings.ToLower(err.Error()); !strings.Contains(got, "already configured") || !strings.Contains(got, "agent harness") {
		t.Fatalf("SaveRuntimeConfigAgentRuntime() error = %q, want harness lock guidance", err)
	}
}

func TestSaveRuntimeConfigAgentRuntimeRejectsCommandSwitchForBoundHarness(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"agent_harness":"claude","agent_command":"claude-custom"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := SaveRuntimeConfigAgentRuntime(path, InitConfig{}, "claude", "claude")
	if err == nil {
		t.Fatal("SaveRuntimeConfigAgentRuntime() error = nil, want non-nil")
	}
	if got := strings.ToLower(err.Error()); !strings.Contains(got, "already configured") || !strings.Contains(got, "agent command") {
		t.Fatalf("SaveRuntimeConfigAgentRuntime() error = %q, want command lock guidance", err)
	}
}

func TestSaveRuntimeConfigAgentRuntimeAllowsIdempotentResave(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"agent_harness":"pi","agent_command":"pi"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := SaveRuntimeConfigAgentRuntime(path, InitConfig{}, "pi", ""); err != nil {
		t.Fatalf("SaveRuntimeConfigAgentRuntime() error = %v", err)
	}
}
