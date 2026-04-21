package hub

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/harness"
)

func TestFailureFollowUpHelperBranches(t *testing.T) {
	t.Parallel()

	if got := failureResponseMessage(""); got != "Failure: task failed. Error details: unknown error." {
		t.Fatalf("failureResponseMessage(empty) = %q", got)
	}
	if got := duplicateDispatchErrorText("", ""); got != "duplicate submission ignored" {
		t.Fatalf("duplicateDispatchErrorText(empty) = %q", got)
	}
	if got := duplicateDispatchErrorText("", "completed"); got != "duplicate submission ignored (state=completed)" {
		t.Fatalf("duplicateDispatchErrorText(state) = %q", got)
	}
	if got := duplicateDispatchErrorText("req-2", ""); got != "duplicate submission ignored (request_id=req-2)" {
		t.Fatalf("duplicateDispatchErrorText(request) = %q", got)
	}
	if got := duplicateDispatchErrorText("req-2", "in_flight"); got != "duplicate submission ignored (request_id=req-2 state=in_flight)" {
		t.Fatalf("duplicateDispatchErrorText(full) = %q", got)
	}

	dispatch := SkillDispatch{RequestID: "req-1"}
	if ok, reason := shouldQueueFailureFollowUp(dispatch, harness.Result{}); ok || !strings.Contains(reason, "did not include an error") {
		t.Fatalf("shouldQueueFailureFollowUp(no error) = (%v, %q)", ok, reason)
	}
	if ok, reason := shouldQueueFailureRerun(dispatch, harness.Result{}); ok || !strings.Contains(reason, "did not include an error") {
		t.Fatalf("shouldQueueFailureRerun(no error) = (%v, %q)", ok, reason)
	}
	if ok, reason := shouldQueueFailureFollowUp(SkillDispatch{RequestID: "req-1-failure-review"}, harness.Result{Err: errors.New("boom")}); ok || reason != "run is already a failure follow-up" {
		t.Fatalf("shouldQueueFailureFollowUp(nested) = (%v, %q), want (false, %q)", ok, reason, "run is already a failure follow-up")
	}
	if ok, reason := shouldQueueFailureRerun(SkillDispatch{RequestID: "req-1-rerun"}, harness.Result{Err: errors.New("boom")}); ok || reason != "run is already a failure rerun" {
		t.Fatalf("shouldQueueFailureRerun(nested) = (%v, %q), want (false, %q)", ok, reason, "run is already a failure rerun")
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

	if got := failureFollowUpRepos(res, config.Config{}); len(got) != 1 || got[0] != config.DefaultRepositoryURL {
		t.Fatalf("failureFollowUpRepos() = %#v", got)
	}
	if got := failureFollowUpRequestID(""); got != "failure-review" {
		t.Fatalf("failureFollowUpRequestID(empty) = %q", got)
	}
	if got := failureFollowUpRequestID("req-1-failure-review"); got != "req-1-failure-review" {
		t.Fatalf("failureFollowUpRequestID(existing) = %q", got)
	}
	if got := failureRerunRequestID(""); got != "rerun" {
		t.Fatalf("failureRerunRequestID(empty) = %q", got)
	}
	if got := failureRerunRequestID("req-1-rerun"); got != "req-1-rerun" {
		t.Fatalf("failureRerunRequestID(existing) = %q", got)
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
	runConfigPayload := requiredSkillPayloadSchema("", "", []string{"unit-test-coverage"})
	runConfigSchema, _ := runConfigPayload["run_config_schema"].(map[string]any)
	properties, _ := runConfigSchema["properties"].(map[string]any)
	responseMode, _ := properties["responseMode"].(map[string]any)
	enumValues, ok := responseMode["enum"].([]string)
	if !ok {
		rawEnum, rawOK := responseMode["enum"].([]any)
		if rawOK {
			enumValues = make([]string, 0, len(rawEnum))
			for _, item := range rawEnum {
				value, valueOK := item.(string)
				if !valueOK {
					continue
				}
				enumValues = append(enumValues, value)
			}
			ok = true
		}
	}
	if !ok || len(enumValues) == 0 || enumValues[0] != "default" {
		t.Fatalf("requiredSkillPayloadSchema().responseMode = %#v, want enum with default", responseMode)
	}
}

func TestAPIExtractionHelperBranches(t *testing.T) {
	t.Parallel()

	if !looksLikeAgentURI(" https://na.hub.molten.bot/agent ") {
		t.Fatal("looksLikeAgentURI(https) = false, want true")
	}
	if looksLikeAgentURI("mailto:test@example.com") {
		t.Fatal("looksLikeAgentURI(mailto) = true, want false")
	}

	token := extractTokenFromAny([]any{
		map[string]any{"value": "ignored"},
		map[string]any{"data": map[string]any{"access_token": " token-123 "}},
	})
	if token != "token-123" {
		t.Fatalf("extractTokenFromAny(nested array) = %q, want token-123", token)
	}
	if got := extractTokenFromAny("not-a-map"); got != "" {
		t.Fatalf("extractTokenFromAny(non map) = %q, want empty", got)
	}

	if got := extractAPIBaseFromAny([]any{map[string]any{"payload": map[string]any{"baseUrl": " https://na.hub.molten.bot/v1 "}}}); got != "https://na.hub.molten.bot/v1" {
		t.Fatalf("extractAPIBaseFromAny(nested) = %q", got)
	}
	if got := extractAPIBaseFromAny(map[string]any{"base_url": " "}); got != "" {
		t.Fatalf("extractAPIBaseFromAny(blank) = %q, want empty", got)
	}

	if got := extractMetadataFromJSON(nil); len(got) != 0 {
		t.Fatalf("extractMetadataFromJSON(nil) = %#v, want empty map", got)
	}
	if got := extractMetadataFromJSON([]byte("{")); len(got) != 0 {
		t.Fatalf("extractMetadataFromJSON(invalid) = %#v, want empty map", got)
	}

	if got := extractAgentProfileFromJSON([]byte("{")); !agentProfileEmpty(got) {
		t.Fatalf("extractAgentProfileFromJSON(invalid) = %#v, want empty profile", got)
	}
	if got := extractAgentProfileFromAny([]any{
		map[string]any{
			"handle": "array-agent",
			"profile": map[string]any{
				"display_name": "Array Agent",
			},
		},
	}); got.Handle != "array-agent" || got.Profile.DisplayName != "Array Agent" {
		t.Fatalf("extractAgentProfileFromAny(array) = %#v", got)
	}

	skills := parseProfileSkills([]string{" code_for_me ", "", "code_review"})
	if got, want := strings.Join(skills, ","), "code_for_me,code_review"; got != want {
		t.Fatalf("parseProfileSkills([]string) = %q, want %q", got, want)
	}
	skills = parseProfileSkills([]any{
		"unit-test-coverage",
		map[string]any{"name": "security-review"},
		map[string]any{"id": "code-review"},
		17,
	})
	if got, want := strings.Join(skills, ","), "unit-test-coverage,security-review,code-review"; got != want {
		t.Fatalf("parseProfileSkills([]any) = %q, want %q", got, want)
	}
	if got := parseProfileSkills(map[string]any{"name": "unsupported"}); got != nil {
		t.Fatalf("parseProfileSkills(unsupported) = %#v, want nil", got)
	}

	if !looksLikeEmojiToken("🔥") {
		t.Fatal("looksLikeEmojiToken(emoji) = false, want true")
	}
	if looksLikeEmojiToken("A🔥") {
		t.Fatal("looksLikeEmojiToken(alphanumeric) = true, want false")
	}

	activities := appendActivityEntries([]any{"first", " ", "second"}, "second")
	if got, want := strings.Join(activities, ","), "first,second"; got != want {
		t.Fatalf("appendActivityEntries(duplicate tail) = %q, want %q", got, want)
	}
	activities = appendActivityEntries("first", "third")
	if got, want := strings.Join(activities, ","), "first,third"; got != want {
		t.Fatalf("appendActivityEntries(string source) = %q, want %q", got, want)
	}
	if got := appendActivityEntries(nil, " "); got != nil {
		t.Fatalf("appendActivityEntries(blank entry) = %#v, want nil", got)
	}

	if got := skillDescription(SkillConfig{DispatchType: "dispatch_only"}); !strings.Contains(got, "dispatch_only") {
		t.Fatalf("skillDescription(dispatch only) = %q", got)
	}
	if got := skillDescription(SkillConfig{ResultType: "result_only"}); !strings.Contains(got, "result_only") {
		t.Fatalf("skillDescription(result only) = %q", got)
	}
	if got := skillDescription(SkillConfig{}); got != runtimeSkillFallback {
		t.Fatalf("skillDescription(default) = %q, want %q", got, runtimeSkillFallback)
	}
	if got := normalizeDescription(" ", " "); got != runtimeSkillFallback {
		t.Fatalf("normalizeDescription(empty fallback) = %q, want runtime fallback", got)
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
