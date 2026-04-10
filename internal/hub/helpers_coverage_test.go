package hub

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/harness"
)

func TestFailureFollowUpHelperBranches(t *testing.T) {
	t.Parallel()

	if got := failureResponseMessage(""); got != "Failure: task failed. Error details: unknown error." {
		t.Fatalf("failureResponseMessage(empty) = %q", got)
	}

	dispatch := SkillDispatch{RequestID: "req-1"}
	if ok, reason := shouldQueueFailureFollowUp(dispatch, harness.Result{}); ok || !strings.Contains(reason, "did not include an error") {
		t.Fatalf("shouldQueueFailureFollowUp(no error) = (%v, %q)", ok, reason)
	}
	if ok, reason := shouldQueueFailureFollowUp(SkillDispatch{RequestID: "req-1-failure-review"}, harness.Result{Err: errors.New("boom")}); ok || !strings.Contains(reason, "nested follow-up") {
		t.Fatalf("shouldQueueFailureFollowUp(nested) = (%v, %q)", ok, reason)
	}

	res := harness.Result{
		ExitCode:     7,
		Err:          errors.New("boom"),
		WorkspaceDir: "/tmp/work",
		Branch:       "moltenhub-fix",
		PRURL:        "https://github.com/acme/repo/pull/1",
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/repo.git", RepoDir: "/tmp/work/repo", Changed: true, PRURL: "https://github.com/acme/repo/pull/1"},
			{RepoURL: "git@github.com:acme/repo.git", RepoDir: "/tmp/work/repo", Changed: false, PRURL: "https://github.com/acme/repo/pull/2"},
		},
	}
	runCfg := config.Config{RepoURL: "git@github.com:acme/repo.git", TargetSubdir: "internal/hub"}
	runCfg.ApplyDefaults()

	if got := failureFollowUpRepos(res, config.Config{}); len(got) != 1 || got[0] != "git@github.com:acme/repo.git" {
		t.Fatalf("failureFollowUpRepos() = %#v", got)
	}
	if got := singleRepoFromResults([]harness.RepoResult{{RepoURL: "a"}, {RepoURL: "b"}}); got != "" {
		t.Fatalf("singleRepoFromResults(multiple repos) = %q, want empty", got)
	}
	if got := failureFollowUpRequestID(""); got != "failure-review" {
		t.Fatalf("failureFollowUpRequestID(empty) = %q", got)
	}
	if got := joinRepoPRURLs(res.RepoResults); got != "https://github.com/acme/repo/pull/1" {
		t.Fatalf("joinRepoPRURLs() = %q", got)
	}
	if got := joinAllRepoPRURLs(res.RepoResults); got != "https://github.com/acme/repo/pull/1,https://github.com/acme/repo/pull/2" {
		t.Fatalf("joinAllRepoPRURLs() = %q", got)
	}

	context := failureFollowUpContext(dispatch, res)
	for _, want := range []string{"Observed failure context:", "request_id=req-1", "exit_code=7", `error="boom"`, "workspace_dir=/tmp/work", "branch=moltenhub-fix", "pr_url=https://github.com/acme/repo/pull/1"} {
		if !strings.Contains(context, want) {
			t.Fatalf("failureFollowUpContext() missing %q in %q", want, context)
		}
	}

	paths := failureLogPaths("/tmp/logs", "1775613327-000024", runCfg, res)
	if len(paths) == 0 {
		t.Fatal("failureLogPaths() returned no paths")
	}
	if !containsString(paths, filepath.Join("/tmp/work/repo", "internal/hub")) {
		t.Fatalf("failureLogPaths() = %#v, want target subdir path", paths)
	}
}

func TestRuntimeConfigHelperBranches(t *testing.T) {
	t.Setenv(runtimeConfigPathEnv, "")

	if got := docStringValue(" value "); got != "value" {
		t.Fatalf("docStringValue() = %q, want value", got)
	}
	if got := ReadRuntimeConfigString("", "github_token"); got != "" {
		t.Fatalf("ReadRuntimeConfigString(empty path) = %q, want empty", got)
	}
	if got := runtimeConfigCandidatePaths("/tmp/config.json"); len(got) != 2 || got[1] != "/tmp/config/config.json" {
		t.Fatalf("runtimeConfigCandidatePaths(custom) = %#v", got)
	}
	if got := ResolveRuntimeConfigPath("/tmp/init.json"); got != "/tmp/config.json" {
		t.Fatalf("ResolveRuntimeConfigPath() = %q, want /tmp/config.json", got)
	}
}

func TestDispatchAndProfileHelperBranches(t *testing.T) {
	t.Parallel()

	if _, ok := extractConfigValue(map[string]any{"note": "x"}); ok {
		t.Fatal("extractConfigValue(non-config payload) ok = true, want false")
	}
	if got := looksLikeRunConfigMap(map[string]any{"prompt": "x", "repo": "git@github.com:acme/repo.git"}); !got {
		t.Fatal("looksLikeRunConfigMap(prompt+repo) = false, want true")
	}
	if _, err := normalizeRunConfigMap(" ", ""); err == nil {
		t.Fatal("normalizeRunConfigMap(blank string) error = nil, want non-nil")
	}

	schema := requiredSkillPayloadSchema("", "", []string{"unit-test-coverage"})
	envelope, ok := schema["dispatch_envelope"].(map[string]any)
	if !ok || envelope["type"] != "skill_request" || envelope["skill"] != "code_for_me" {
		t.Fatalf("requiredSkillPayloadSchema() envelope = %#v", envelope)
	}

	skills := normalizeSkillsMetadata([]any{
		map[string]any{"name": " Code For Me ", "description": " fixes code "},
		"code for me",
		map[string]any{"skill": "reviewer", "summary": " reviews code "},
	}, "fallback skill", "fallback description")
	if len(skills) != 2 {
		t.Fatalf("normalizeSkillsMetadata() len = %d, want 2", len(skills))
	}
	if got := normalizeIdentifier(" ! ", "fallback skill"); got != "fallback-skill" {
		t.Fatalf("normalizeIdentifier() = %q", got)
	}
	if got := sanitizeIdentifier(" A@B@@C "); got != "a-b-c" {
		t.Fatalf("sanitizeIdentifier() = %q, want a-b-c", got)
	}
	if got := skillDescription(SkillConfig{DispatchType: "skill_request", ResultType: "skill_result"}); !strings.Contains(got, "Handles skill_request requests") {
		t.Fatalf("skillDescription() = %q", got)
	}
	if got := normalizeDescription(strings.Repeat("x", 260), "fallback"); len(got) > 240 {
		t.Fatalf("normalizeDescription() len = %d, want <= 240", len(got))
	}
	if got := buildSupportedSkillsMetadata(); len(got) != 3 {
		t.Fatalf("buildSupportedSkillsMetadata() len = %d, want 3", len(got))
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
