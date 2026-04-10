package hub

import (
	"strings"
	"testing"
)

func TestRunConfigArrayAndAliasHelpers(t *testing.T) {
	t.Parallel()

	if !hasNonEmptyStringArray([]string{"", " repo "}) {
		t.Fatal("hasNonEmptyStringArray([]string) = false, want true")
	}
	if !hasNonEmptyStringArray([]any{" ", "repo"}) {
		t.Fatal("hasNonEmptyStringArray([]any) = false, want true")
	}
	if hasNonEmptyStringArray([]any{1, true}) {
		t.Fatal("hasNonEmptyStringArray(non-string entries) = true, want false")
	}
	if !hasSingleNonEmptyStringArray([]any{"repo"}) {
		t.Fatal("hasSingleNonEmptyStringArray(single) = false, want true")
	}
	if hasSingleNonEmptyStringArray([]any{"repo-a", "repo-b"}) {
		t.Fatal("hasSingleNonEmptyStringArray(multi) = true, want false")
	}

	got := nonEmptyStringArray([]any{" ", "repo-a", 12, "repo-b"})
	if len(got) != 2 || got[0] != "repo-a" || got[1] != "repo-b" {
		t.Fatalf("nonEmptyStringArray() = %v, want [repo-a repo-b]", got)
	}
}

func TestNormalizeRunConfigMapAndAliasesValidation(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git","branch":"release","prompt":"do work"}`, "")
	if err != nil {
		t.Fatalf("normalizeRunConfigMap(string) error = %v", err)
	}
	if got, want := stringAt(normalized, "baseBranch"), "release"; got != want {
		t.Fatalf("baseBranch alias = %q, want %q", got, want)
	}

	if _, err := normalizeRunConfigMap(`["not","an","object"]`, ""); err == nil {
		t.Fatal("normalizeRunConfigMap(array JSON) error = nil, want non-nil")
	}
	if _, err := normalizeRunConfigMap(42, ""); err == nil {
		t.Fatal("normalizeRunConfigMap(non-map) error = nil, want non-nil")
	}

	err = normalizeRunConfigAliases(map[string]any{
		"prompt":          "x",
		"libraryTaskName": "unit-test-coverage",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot include both prompt and libraryTaskName") {
		t.Fatalf("normalizeRunConfigAliases(conflict) error = %v", err)
	}
}

func TestNormalizeRunConfigMapAppliesCodeReviewSkillDefaults(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git","branch":"review-branch"}`, "code_review")
	if err != nil {
		t.Fatalf("normalizeRunConfigMap(code_review) error = %v", err)
	}
	if got, want := stringAt(normalized, "libraryTaskName"), codeReviewLibraryTaskName; got != want {
		t.Fatalf("libraryTaskName = %q, want %q", got, want)
	}
	review, _ := normalized["review"].(map[string]any)
	if got, want := stringAt(review, "headBranch"), "review-branch"; got != want {
		t.Fatalf("review.headBranch = %q, want %q", got, want)
	}

	normalized, err = normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git","prNumber":123}`, "code_review")
	if err != nil {
		t.Fatalf("normalizeRunConfigMap(code_review prNumber) error = %v", err)
	}
	review, _ = normalized["review"].(map[string]any)
	if got, ok := positiveIntValue(review["prNumber"]); !ok || got != 123 {
		t.Fatalf("review.prNumber = %#v, want 123", review["prNumber"])
	}

	if _, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git","prompt":"x"}`, "code_review"); err == nil || !strings.Contains(err.Error(), "does not accept prompt") {
		t.Fatalf("normalizeRunConfigMap(code_review prompt) error = %v", err)
	}
	if _, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git"}`, "code_review"); err == nil || !strings.Contains(err.Error(), "requires branch, prNumber, or review.prUrl") {
		t.Fatalf("normalizeRunConfigMap(code_review missing selector) error = %v", err)
	}
	if _, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git"}`, "library_task"); err == nil || !strings.Contains(err.Error(), "requires libraryTaskName") {
		t.Fatalf("normalizeRunConfigMap(library_task missing handle) error = %v", err)
	}
}

func TestExtractConfigValueAndLooksLikeRunConfigMap(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"payload": map[string]any{
			"config": map[string]any{
				"repo":   "git@github.com:acme/repo.git",
				"prompt": "do work",
			},
		},
	}
	value, ok := extractConfigValue(msg)
	if !ok {
		t.Fatal("extractConfigValue(payload.config) ok = false, want true")
	}
	cfgMap, mapOK := value.(map[string]any)
	if !mapOK || stringAt(cfgMap, "repo") == "" {
		t.Fatalf("extractConfigValue(payload.config) value = %#v", value)
	}

	if !looksLikeRunConfigMap(map[string]any{"libraryTaskName": "unit-test-coverage", "repos": []any{"repo"}}) {
		t.Fatal("looksLikeRunConfigMap(library task + one repo) = false, want true")
	}
	if looksLikeRunConfigMap(map[string]any{"prompt": "x", "repos": []any{}}) {
		t.Fatal("looksLikeRunConfigMap(empty repos) = true, want false")
	}
}

func TestParseSkillDispatchPrefersSenderRoutingOverRecipientTarget(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":           "skill_request",
		"skill":          "code_for_me",
		"request_id":     "req-routing",
		"to_agent_uuid":  "receiver-agent",
		"from_agent_uri": "https://na.hub.molten.bot/acme/caller",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix the issue",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.ReplyTo, "https://na.hub.molten.bot/acme/caller"; got != want {
		t.Fatalf("ReplyTo = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchFallsBackToRecipientTargetWhenSenderMissing(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":          "skill_request",
		"skill":         "code_for_me",
		"request_id":    "req-routing-fallback",
		"to_agent_uuid": "caller-agent-uuid",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix the issue",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.ReplyTo, "caller-agent-uuid"; got != want {
		t.Fatalf("ReplyTo = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchAcceptsJSONStringPayload(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-payload-json",
		"payload": `{
			"repo":"git@github.com:acme/repo.git",
			"prompt":"ship the fix"
		}`,
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.Config.RepoURL, "git@github.com:acme/repo.git"; got != want {
		t.Fatalf("RepoURL = %q, want %q", got, want)
	}
	if got, want := dispatch.Config.Prompt, "ship the fix"; got != want {
		t.Fatalf("Prompt = %q, want %q", got, want)
	}
}
