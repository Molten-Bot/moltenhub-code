package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/agent_00/internal/agentruntime"
)

func TestReviewSkillPromptAndRuntimePaths(t *testing.T) {
	t.Parallel()

	prompt, err := withReviewSkillPrompt("Review the pull request.")
	if err != nil {
		t.Fatalf("withReviewSkillPrompt() error = %v", err)
	}
	for _, want := range []string{
		"The bundled review skill is mandatory",
		"Do not edit files, commit, push",
		`{"status":"clean|findings|blocked"`,
		"Review the pull request.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("review skill prompt missing %q", want)
		}
	}

	candidates := reviewSkillPathCandidates()
	for _, want := range []string{
		filepath.Join(workspaceSkillsDir, "review", "SKILL.md"),
		filepath.Join(runtimeSkillsDir, "review", "SKILL.md"),
	} {
		if !containsString(candidates, want) {
			t.Fatalf("review skill candidates = %#v, want %q", candidates, want)
		}
	}

	for _, harness := range []string{agentruntime.HarnessCodex, agentruntime.HarnessClaude} {
		runtime, err := agentruntime.Resolve(harness, "")
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", harness, err)
		}
		cmd, err := agentCommandWithOptions(runtime, "/tmp/repo", prompt, codexRunOptions{})
		if err != nil {
			t.Fatalf("agentCommandWithOptions(%q) error = %v", harness, err)
		}
		commandPrompt := cmd.Stdin
		if commandPrompt == "" && len(cmd.Args) > 0 {
			commandPrompt = cmd.Args[len(cmd.Args)-1]
		}
		if !strings.Contains(commandPrompt, "The bundled review skill is mandatory") {
			t.Fatalf("%s command did not retain the review skill prompt", harness)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
