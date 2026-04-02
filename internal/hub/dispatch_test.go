package hub

import (
	"strings"
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

func TestParseSkillDispatchMissingPayloadIsValidationError(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "codex_harness_run",
		"id":    "req-2",
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if dispatch.RequestID != "req-2" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
}

func TestParseSkillDispatchWrongTypeIsValidationError(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "not_skill_request",
		"skill": "codex_harness_run",
		"id":    "req-3",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected dispatch type") {
		t.Fatalf("unexpected error: %v", err)
	}
	if dispatch.RequestID != "req-3" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
}

func TestParseSkillDispatchRequiresInlineConfigObject(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":   "skill_request",
		"skill":  "codex_harness_run",
		"config": "/tmp/run.json",
	}

	_, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "decode run config payload string") &&
		!strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseSkillDispatchAcceptsJSONStringInputAndSourceRouting(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"kind":         "skill_request",
		"skill_name":   "code_for_me",
		"request_id":   "req-json-input",
		"from_agent_uri": "https://na.hub.molten.bot/acme/sender",
		"input": `{
			"repo":"git@github.com:acme/repo.git",
			"prompt":"do the thing"
		}`,
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.RequestID != "req-json-input" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
	if dispatch.ReplyTo != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("ReplyTo = %q", dispatch.ReplyTo)
	}
	if dispatch.Config.RepoURL != "git@github.com:acme/repo.git" {
		t.Fatalf("RepoURL = %q", dispatch.Config.RepoURL)
	}
	if dispatch.Config.Prompt != "do the thing" {
		t.Fatalf("Prompt = %q", dispatch.Config.Prompt)
	}
}

func TestParseSkillDispatchMatchesLegacyAndCurrentSkillAliases(t *testing.T) {
	t.Parallel()

	msgCurrent := map[string]any{
		"type":  "skill_request",
		"skill": "code_for_me",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	if _, matched, err := ParseSkillDispatch(msgCurrent, "skill_request", "codex_harness_run"); err != nil {
		t.Fatalf("ParseSkillDispatch() current->legacy error = %v", err)
	} else if !matched {
		t.Fatal("matched = false for current->legacy alias")
	}

	msgLegacy := map[string]any{
		"type":  "skill_request",
		"skill": "codex_harness_run",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	if _, matched, err := ParseSkillDispatch(msgLegacy, "skill_request", "code_for_me"); err != nil {
		t.Fatalf("ParseSkillDispatch() legacy->current error = %v", err)
	} else if !matched {
		t.Fatal("matched = false for legacy->current alias")
	}
}

func TestParseSkillDispatchRejectsUnknownConfigFields(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "codex_harness_run",
		"config": map[string]any{
			"repo":        "git@github.com:acme/repo.git",
			"prompt":      "make change",
			"unknown_key": true,
		},
	}

	_, matched, err := ParseSkillDispatch(msg, "skill_request", "codex_harness_run")
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}
