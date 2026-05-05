package hub

import (
	"strings"
	"testing"
)

func TestParseSkillDispatchFromPayloadConfig(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "moltenhub_code_run",
		"request_id": "req-1",
		"message_id": "hub-task-1",
		"from":       "agent-alpha",
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
	if dispatch.HubTaskID != "hub-task-1" {
		t.Fatalf("HubTaskID = %q", dispatch.HubTaskID)
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
		"type":       "skill_request",
		"skill":      "moltenhub_code_run",
		"request_id": "req-multi",
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
		"type":       "skill_request",
		"skill":      "moltenhub_code_run",
		"request_id": "req-2",
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
		"type":       "not_skill_request",
		"skill":      "moltenhub_code_run",
		"request_id": "req-3",
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

func TestParseSkillDispatchAcceptsA2AEnvelopeDataPart(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"protocol":       "a2a.v1",
		"from_agent_uri": "https://na.hub.molten.bot/acme/sender",
		"message": map[string]any{
			"messageId": "a2a-msg-1",
			"contextId": "a2a-context-1",
			"taskId":    "a2a-task-1",
			"role":      "ROLE_USER",
			"parts": []any{
				map[string]any{
					"data": map[string]any{
						"repo":   "git@github.com:acme/repo.git",
						"prompt": "ship a2a data",
					},
				},
			},
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.RequestID != "a2a-msg-1" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
	if dispatch.HubTaskID != "a2a-task-1" {
		t.Fatalf("HubTaskID = %q", dispatch.HubTaskID)
	}
	if dispatch.ContextID != "a2a-context-1" {
		t.Fatalf("ContextID = %q", dispatch.ContextID)
	}
	if dispatch.ReplyTo != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("ReplyTo = %q", dispatch.ReplyTo)
	}
	if dispatch.Config.RepoURL != "git@github.com:acme/repo.git" {
		t.Fatalf("RepoURL = %q", dispatch.Config.RepoURL)
	}
	if dispatch.Config.Prompt != "ship a2a data" {
		t.Fatalf("Prompt = %q", dispatch.Config.Prompt)
	}
}

func TestParseSkillDispatchAcceptsA2ATextPartRunConfig(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"message": map[string]any{
			"messageId": "a2a-msg-text",
			"role":      "ROLE_USER",
			"metadata": map[string]any{
				"skill_name": "code_for_me",
			},
			"parts": []any{
				map[string]any{
					"text": `{"repo":"git@github.com:acme/repo.git","prompt":"ship a2a text"}`,
				},
			},
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.RequestID != "a2a-msg-text" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
	if dispatch.Config.Prompt != "ship a2a text" {
		t.Fatalf("Prompt = %q", dispatch.Config.Prompt)
	}
}

func TestParseSkillDispatchAcceptsRuntimeTextMessageRunConfig(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"kind":           "text_message",
		"text":           `{"repo":"git@github.com:acme/repo.git","prompt":"ship text message"}`,
		"from_agent_uri": "https://na.hub.molten.bot/acme/sender",
		"message_id":     "msg-text",
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.RequestID != "msg-text" {
		t.Fatalf("RequestID = %q", dispatch.RequestID)
	}
	if dispatch.ReplyTo != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("ReplyTo = %q", dispatch.ReplyTo)
	}
	if dispatch.Config.Prompt != "ship text message" {
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

	msgCodeReview := map[string]any{
		"type":  "skill_request",
		"skill": "code_review",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"branch": "review-branch",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msgCodeReview, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() code_review alias error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false for code_review alias")
	}
	if got, want := dispatch.Config.LibraryTaskName, codeReviewLibraryTaskName; got != want {
		t.Fatalf("LibraryTaskName = %q, want %q", got, want)
	}
	if dispatch.Config.Review == nil || dispatch.Config.Review.HeadBranch != "review-branch" {
		t.Fatalf("Review = %#v, want headBranch review-branch", dispatch.Config.Review)
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
	if got, want := cfg.LibraryTaskDisplayName, "100% Unit Test Coverage"; got != want {
		t.Fatalf("LibraryTaskDisplayName = %q, want %q", got, want)
	}
	if got := cfg.Prompt; !strings.Contains(got, "100% unit-test coverage") {
		t.Fatalf("Prompt = %q", got)
	}
}

func TestParseRunConfigJSONExpandsLibraryTaskPayloadWithReposArray(t *testing.T) {
	// This test resolves the on-disk library catalog and must not race tests that
	// temporarily change process working directory.

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repos": [
			"git@github.com:acme/repo-one.git",
			"git@github.com:acme/repo-two.git"
		],
		"branch": "release",
		"libraryTaskName": "unit-test-coverage"
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if got, want := len(cfg.Repos), 2; got != want {
		t.Fatalf("len(Repos) = %d, want %d", got, want)
	}
	if got, want := cfg.RepoURL, "git@github.com:acme/repo-one.git"; got != want {
		t.Fatalf("RepoURL = %q, want %q", got, want)
	}
	if got, want := cfg.BaseBranch, "release"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}
	if got := cfg.Prompt; !strings.Contains(got, "100% unit-test coverage") {
		t.Fatalf("Prompt = %q", got)
	}
}

func TestParseRunConfigJSONExpandsLibraryTaskPayloadAndPreservesReviewConfig(t *testing.T) {
	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"libraryTaskName": "code-review",
		"review": {
			"prNumber": 42,
			"headBranch": "feature/improve-tests"
		}
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if cfg.Review == nil {
		t.Fatal("Review = nil, want non-nil")
	}
	if got, want := cfg.Review.PRNumber, 42; got != want {
		t.Fatalf("Review.PRNumber = %d, want %d", got, want)
	}
	if got, want := cfg.Review.HeadBranch, "feature/improve-tests"; got != want {
		t.Fatalf("Review.HeadBranch = %q, want %q", got, want)
	}
	if got := cfg.Prompt; !strings.Contains(got, "pull-request code review") {
		t.Fatalf("Prompt = %q", got)
	}
}

func TestParseRunConfigJSONExpandsLibraryTaskPayloadAndPreservesImages(t *testing.T) {
	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"libraryTaskName": "unit-test-coverage",
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
	if !strings.Contains(err.Error(), "cannot include both prompt and librarytaskname") {
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

func TestParseRunConfigJSONTreatsNoneReviewerAsUnset(t *testing.T) {
	t.Parallel()

	cfg, err := ParseRunConfigJSON([]byte(`{
		"repo": "git@github.com:acme/repo.git",
		"prompt": "make a change",
		"reviewers": ["none"]
	}`))
	if err != nil {
		t.Fatalf("ParseRunConfigJSON() error = %v", err)
	}
	if cfg.Reviewers != nil {
		t.Fatalf("Reviewers = %#v, want nil", cfg.Reviewers)
	}
}

func TestParseRunConfigJSONRejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	_, err := ParseRunConfigJSON([]byte(`{`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
