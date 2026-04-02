package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSkillDispatchFromPayloadConfig(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "codex_harness_run",
		"id":    "req-1",
		"from":  "agent-alpha",
		"payload": map[string]any{
			"config": map[string]any{
				"repo":          "git@github.com:acme/repo.git",
				"base_branch":   "main",
				"target_subdir": ".",
				"prompt":        "update tests",
			},
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.RequestID != "req-1" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
	if dispatch.ReplyTo != "agent-alpha" {
		t.Fatalf("ReplyTo = %q", dispatch.ReplyTo)
	}
	if dispatch.Config.RepoURL != "git@github.com:acme/repo.git" {
		t.Fatalf("RepoURL = %q", dispatch.Config.RepoURL)
	}
	if dispatch.Config.Prompt != "update tests" {
		t.Fatalf("Prompt = %q", dispatch.Config.Prompt)
	}
}

func TestParseSkillDispatchIgnoresDifferentSkill(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "other_skill",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	_, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if matched {
		t.Fatal("matched = true, want false")
	}
}

func TestParseSkillDispatchLoadsConfigFromPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "run.json")
	content := `{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "make change"
}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write run config: %v", err)
	}

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "codex_harness_run",
		"config": map[string]any{
			"config_path": configPath,
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.Config.Prompt != "make change" {
		t.Fatalf("Prompt = %q", dispatch.Config.Prompt)
	}
}
