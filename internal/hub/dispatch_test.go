package hub

import (
	"strings"
	"testing"
)

func TestParseSkillDispatchFromPayloadConfig(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "moltenhub_code_run",
		"id":    "req-1",
		"from":  "agent-alpha",
		"payload": map[string]any{
			"config": map[string]any{
				"repo":         "git@github.com:acme/repo.git",
				"baseBranch":   "main",
				"targetSubdir": ".",
				"prompt":       "update tests",
			},
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
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

func TestParseSkillDispatchFromPayloadConfigWithReposArray(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "moltenhub_code_run",
		"id":    "req-multi",
		"payload": map[string]any{
			"config": map[string]any{
				"repos": []any{
					"git@github.com:acme/repo-one.git",
					"git@github.com:acme/repo-two.git",
				},
				"prompt": "update both repos",
			},
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := len(dispatch.Config.Repos), 2; got != want {
		t.Fatalf("len(Repos) = %d, want %d", got, want)
	}
	if dispatch.Config.RepoURL != "git@github.com:acme/repo-one.git" {
		t.Fatalf("RepoURL = %q", dispatch.Config.RepoURL)
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

	_, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
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
		"skill": "moltenhub_code_run",
		"id":    "req-2",
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
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
		"skill": "moltenhub_code_run",
		"id":    "req-3",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
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
		"skill":  "moltenhub_code_run",
		"config": "/tmp/run.json",
	}

	_, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
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
		"kind":           "skill_request",
		"skill_name":     "code_for_me",
		"request_id":     "req-json-input",
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

func TestParseSkillDispatchMatchesLegacyCurrentAndRenamedSkillAliases(t *testing.T) {
	t.Parallel()

	msgCurrent := map[string]any{
		"type":  "skill_request",
		"skill": "code_for_me",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	if _, matched, err := ParseSkillDispatch(msgCurrent, "skill_request", "moltenhub_code_run"); err != nil {
		t.Fatalf("ParseSkillDispatch() current->legacy error = %v", err)
	} else if !matched {
		t.Fatal("matched = false for current->legacy alias")
	}

	msgLegacy := map[string]any{
		"type":  "skill_request",
		"skill": "moltenhub_code_run",
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

	msgRenamed := map[string]any{
		"type":  "skill_request",
		"skill": "moltenhub_code_run",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "x",
		},
	}

	if _, matched, err := ParseSkillDispatch(msgCurrent, "skill_request", "moltenhub_code_run"); err != nil {
		t.Fatalf("ParseSkillDispatch() current->renamed error = %v", err)
	} else if !matched {
		t.Fatal("matched = false for current->renamed alias")
	}

	if _, matched, err := ParseSkillDispatch(msgRenamed, "skill_request", "code_for_me"); err != nil {
		t.Fatalf("ParseSkillDispatch() renamed->current error = %v", err)
	} else if !matched {
		t.Fatal("matched = false for renamed->current alias")
	}
}

func TestParseSkillDispatchIgnoresUnknownConfigFields(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "moltenhub_code_run",
		"config": map[string]any{
			"repo":        "git@github.com:acme/repo.git",
			"prompt":      "make change",
			"unknown_key": true,
		},
	}

	_, matched, err := ParseSkillDispatch(msg, "skill_request", "moltenhub_code_run")
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
}

func TestParseRunConfigJSON(t *testing.T) {
	t.Parallel()

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"baseBranch": "main",
		"targetSubdir": ".",
		"prompt": "make a change"
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if cfg.RepoURL != "git@github.com:acme/repo.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
	if cfg.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q", cfg.BaseBranch)
	}
	if cfg.TargetSubdir != "." {
		t.Fatalf("TargetSubdir = %q", cfg.TargetSubdir)
	}
	if cfg.Prompt != "make a change" {
		t.Fatalf("Prompt = %q", cfg.Prompt)
	}
}

func TestParseRunConfigJSONRejectsSnakeCaseAliases(t *testing.T) {
	t.Parallel()

	_, err := ParseRunConfigJSON([]byte(`{
		"repo_url": "git@github.com:acme/repo.git",
		"base_branch": "main",
		"target_subdir": ".",
		"prompt": "make a change",
		"github_handle": "@octocat",
		"images": [
			{"name":"shot.png","media_type":"image/png","data_base64":"aGVsbG8="}
		]
	}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRunConfigJSONRejectsSnakeCaseLibraryTaskName(t *testing.T) {
	t.Parallel()

	_, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"library_task_name": "unit-test-coverage"
	}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRunConfigJSONRejectsSnakeCaseImageFields(t *testing.T) {
	t.Parallel()

	_, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"prompt": "inspect screenshot",
		"images": [
			{"name":"shot.png","media_type":"image/png","dataBase64":"aGVsbG8="}
		]
	}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "images[0].media_type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRunConfigJSONWithImages(t *testing.T) {
	t.Parallel()

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"prompt": "inspect screenshot",
		"images": [
			{
				"name": "shot.png",
				"mediaType": "image/png",
				"dataBase64": "aGVsbG8="
			}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if got, want := len(cfg.Images), 1; got != want {
		t.Fatalf("len(Images) = %d, want %d", got, want)
	}
	if got, want := cfg.Images[0].Name, "shot.png"; got != want {
		t.Fatalf("Images[0].Name = %q, want %q", got, want)
	}
}

func TestParseRunConfigJSONExpandsLibraryTaskPayload(t *testing.T) {
	// This test resolves the on-disk library catalog and must not race tests that
	// temporarily change process working directory.

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"branch": "release",
		"libraryTaskName": "unit-test-coverage"
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if got, want := cfg.RepoURL, "git@github.com:acme/repo.git"; got != want {
		t.Fatalf("RepoURL = %q, want %q", got, want)
	}
	if got, want := cfg.BaseBranch, "release"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}
	if got := cfg.Prompt; !strings.Contains(got, "100% unit-test coverage") {
		t.Fatalf("Prompt = %q", got)
	}
}

func TestParseRunConfigJSONRejectsAmbiguousPromptAndLibraryTask(t *testing.T) {
	t.Parallel()

	_, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"prompt": "x",
		"libraryTaskName": "unit-test-coverage"
	}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot include both prompt and libraryTaskName") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRunConfigJSONWithReposArray(t *testing.T) {
	t.Parallel()

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repos": [
			"git@github.com:acme/repo-one.git",
			"git@github.com:acme/repo-two.git"
		],
		"prompt": "make a cross-repo change"
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if got, want := len(cfg.Repos), 2; got != want {
		t.Fatalf("len(Repos) = %d, want %d", got, want)
	}
	if cfg.RepoURL != "git@github.com:acme/repo-one.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
}

func TestParseRunConfigJSONIgnoresUnknownFields(t *testing.T) {
	t.Parallel()

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"baseBranch": "main",
		"targetSubdir": ".",
		"prompt": "make a change",
		"extra_field": {"ignored": true}
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if cfg.RepoURL != "git@github.com:acme/repo.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
	if cfg.Prompt != "make a change" {
		t.Fatalf("Prompt = %q", cfg.Prompt)
	}
}

func TestParseRunConfigJSONSupportsGitHubHandleReviewer(t *testing.T) {
	t.Parallel()

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"prompt": "make a change",
		"githubHandle": "@octocat"
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if got, want := cfg.GitHubHandle, "octocat"; got != want {
		t.Fatalf("GitHubHandle = %q, want %q", got, want)
	}
	if got, want := len(cfg.Reviewers), 1; got != want {
		t.Fatalf("len(Reviewers) = %d, want %d", got, want)
	}
	if got, want := cfg.Reviewers[0], "octocat"; got != want {
		t.Fatalf("Reviewers[0] = %q, want %q", got, want)
	}
}

func TestParseRunConfigJSONRejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	_, err := ParseRunConfigJSON([]byte(`{`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
