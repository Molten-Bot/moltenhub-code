package hub

import (
	"encoding/json"
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
	got = nonEmptyStringArray([]string{" ", "repo-a", "repo-b"})
	if len(got) != 2 || got[0] != "repo-a" || got[1] != "repo-b" {
		t.Fatalf("nonEmptyStringArray([]string) = %v, want [repo-a repo-b]", got)
	}
	if got := nonEmptyStringArray(123); got != nil {
		t.Fatalf("nonEmptyStringArray(non-array) = %#v, want nil", got)
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
	if _, err := normalizeRunConfigMap("", ""); err == nil {
		t.Fatal("normalizeRunConfigMap(empty string) error = nil, want non-nil")
	}
	if err := normalizeRunConfigAliases(nil); err == nil {
		t.Fatal("normalizeRunConfigAliases(nil) error = nil, want non-nil")
	}

	err = normalizeRunConfigAliases(map[string]any{
		"prompt":          "x",
		"libraryTaskName": "unit-test-coverage",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot include both prompt and librarytaskname") {
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
	if _, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git"}`, "code_review"); err == nil || !strings.Contains(err.Error(), "requires branch, prnumber, or review.prurl") {
		t.Fatalf("normalizeRunConfigMap(code_review missing selector) error = %v", err)
	}
	if _, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git"}`, "library_task"); err == nil || !strings.Contains(err.Error(), "requires librarytaskname") {
		t.Fatalf("normalizeRunConfigMap(library_task missing handle) error = %v", err)
	}
	if _, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git","prompt":"x"}`, "library_task"); err == nil || !strings.Contains(err.Error(), "does not accept prompt") {
		t.Fatalf("normalizeRunConfigMap(library_task prompt) error = %v", err)
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

	for _, msg := range []map[string]any{
		{"payload": map[string]any{"repo": "git@github.com:acme/repo.git", "prompt": "ship"}},
		{"data": map[string]any{"repo": "git@github.com:acme/repo.git", "prompt": "ship"}},
		{"repo": "git@github.com:acme/repo.git", "prompt": "ship"},
	} {
		if _, ok := extractConfigValue(msg); !ok {
			t.Fatalf("extractConfigValue(%#v) ok = false, want true", msg)
		}
	}
}

func TestDispatchHelperErrorAndEdgeBranches(t *testing.T) {
	t.Parallel()

	if _, matched, err := ParseSkillDispatch(nil, "skill_request", "code_for_me"); matched || err != nil {
		t.Fatalf("ParseSkillDispatch(empty) = matched %v err %v, want false nil", matched, err)
	}
	msgMissingType := map[string]any{
		"skill": "code_for_me",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship",
		},
	}
	if _, matched, err := ParseSkillDispatch(msgMissingType, "skill_request", "code_for_me"); !matched || err == nil || !strings.Contains(err.Error(), "missing dispatch type") {
		t.Fatalf("ParseSkillDispatch(missing type) matched=%v err=%v, want missing type", matched, err)
	}
	if _, err := ParseRunConfigJSON([]byte(" \n\t ")); err == nil {
		t.Fatal("ParseRunConfigJSON(empty) error = nil, want non-nil")
	}
	if _, err := ParseRunConfigJSON([]byte(`{"repo":"git@github.com:acme/repo.git"}`)); err == nil || !strings.Contains(err.Error(), "validate run config payload") {
		t.Fatalf("ParseRunConfigJSON(invalid config) error = %v, want validation error", err)
	}
	if _, err := parseRunConfigValue(map[string]any{
		"repo":   "git@github.com:acme/repo.git",
		"prompt": "ship",
		"bad":    func() {},
	}, ""); err == nil || !strings.Contains(err.Error(), "marshal run config payload") {
		t.Fatalf("parseRunConfigValue(unmarshalable value) error = %v, want marshal error", err)
	}
	if got := propertyStringEnum("", "  "); got["minLength"] != 1 {
		t.Fatalf("propertyStringEnum(empty values) = %#v, want non-empty string schema", got)
	}
	root := map[string]any{"nested": map[string]any{"value": "x"}}
	if got, ok := valueAtPath(root); !ok || got == nil {
		t.Fatalf("valueAtPath(root) = (%#v, %v), want root,true", got, ok)
	}
	if got := stringAt(map[string]any{"n": 1}, "n"); got != "" {
		t.Fatalf("stringAt(non-string) = %q, want empty", got)
	}
	if got := stringAtPath(root, "nested", "value"); got != "x" {
		t.Fatalf("stringAtPath(valid) = %q, want x", got)
	}
	if got := stringAtPath(root, "nested"); got != "" {
		t.Fatalf("stringAtPath(non-string leaf) = %q, want empty", got)
	}
}

func TestExpandLibraryTaskRunConfigBranches(t *testing.T) {
	t.Parallel()

	expanded, err := expandLibraryTaskRunConfig(map[string]any{
		"libraryTaskName": "unit-test-coverage",
		"repos":           []any{"git@github.com:acme/repo.git"},
		"extra":           "kept",
	}, "unit-test-coverage")
	if err != nil {
		t.Fatalf("expandLibraryTaskRunConfig() error = %v", err)
	}
	if got := stringAt(expanded, "extra"); got != "kept" {
		t.Fatalf("expanded extra = %q, want kept", got)
	}
	if got := nonEmptyStringArray(expanded["repos"]); len(got) != 1 || got[0] != "git@github.com:acme/repo.git" {
		t.Fatalf("expanded repos = %#v, want original repo list", expanded["repos"])
	}

	if _, err := expandLibraryTaskRunConfig(map[string]any{
		"repo": "git@github.com:acme/repo.git",
	}, "missing-task"); err == nil {
		t.Fatal("expandLibraryTaskRunConfig(missing task) error = nil, want non-nil")
	}
}

func TestReviewSelectorAndPositiveIntBranches(t *testing.T) {
	t.Parallel()

	if ensureReviewSelector(nil) {
		t.Fatal("ensureReviewSelector(nil) = true, want false")
	}
	for _, review := range []map[string]any{
		{"prUrl": "https://github.com/acme/repo/pull/1"},
		{"headBranch": "feature"},
	} {
		m := map[string]any{"review": review}
		if !ensureReviewSelector(m) {
			t.Fatalf("ensureReviewSelector(%#v) = false, want true", m)
		}
	}
	cases := []any{int32(7), int64(8), float64(9), json.Number("10")}
	for _, value := range cases {
		got, ok := positiveIntValue(value)
		if !ok || got <= 0 {
			t.Fatalf("positiveIntValue(%T(%v)) = (%d, %v), want positive", value, value, got, ok)
		}
	}
	for _, value := range []any{float64(1.5), json.Number("bad"), "12"} {
		if _, ok := positiveIntValue(value); ok {
			t.Fatalf("positiveIntValue(%T(%v)) ok = true, want false", value, value)
		}
	}
}

func TestExtractConfigValueAcceptsNestedConfigWrappers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  map[string]any
	}{
		{
			name: "input.config",
			msg: map[string]any{
				"input": map[string]any{
					"config": map[string]any{
						"repo":   "git@github.com:acme/repo.git",
						"prompt": "ship fix",
					},
				},
			},
		},
		{
			name: "input.input",
			msg: map[string]any{
				"input": map[string]any{
					"input": map[string]any{
						"repo":   "git@github.com:acme/repo.git",
						"prompt": "ship fix",
					},
				},
			},
		},
		{
			name: "payload.input.config",
			msg: map[string]any{
				"payload": map[string]any{
					"input": map[string]any{
						"config": map[string]any{
							"repo":   "git@github.com:acme/repo.git",
							"prompt": "ship fix",
						},
					},
				},
			},
		},
		{
			name: "data.input.config",
			msg: map[string]any{
				"data": map[string]any{
					"input": map[string]any{
						"config": map[string]any{
							"repo":   "git@github.com:acme/repo.git",
							"prompt": "ship fix",
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			value, ok := extractConfigValue(tc.msg)
			if !ok {
				t.Fatalf("extractConfigValue(%s) ok = false, want true", tc.name)
			}
			cfgMap, mapOK := value.(map[string]any)
			if !mapOK {
				t.Fatalf("extractConfigValue(%s) value type = %T, want map[string]any", tc.name, value)
			}
			if got := stringAt(cfgMap, "repo"); got != "git@github.com:acme/repo.git" {
				t.Fatalf("extractConfigValue(%s) repo = %q", tc.name, got)
			}
			if got := stringAt(cfgMap, "prompt"); got != "ship fix" {
				t.Fatalf("extractConfigValue(%s) prompt = %q", tc.name, got)
			}
		})
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
	if got, want := dispatch.RouteTo, "receiver-agent"; got != want {
		t.Fatalf("RouteTo = %q, want %q", got, want)
	}
	if got, want := dispatch.Originator, "https://na.hub.molten.bot/acme/caller"; got != want {
		t.Fatalf("Originator = %q, want %q", got, want)
	}
	if got, want := dispatch.OriginatorAgentURI, "https://na.hub.molten.bot/acme/caller"; got != want {
		t.Fatalf("OriginatorAgentURI = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchUsesA2AParamsMetadataForReplyRouting(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "message/send",
		"params": map[string]any{
			"metadata": map[string]any{
				"from_agent_uri": "https://na.hub.molten.bot/acme/caller",
				"to_agent_uuid":  "receiver-agent-uuid",
			},
			"message": map[string]any{
				"role":      "user",
				"messageId": "req-a2a-routing",
				"parts": []any{
					map[string]any{
						"kind": "data",
						"data": map[string]any{
							"type":  "skill_request",
							"skill": "code_for_me",
							"config": map[string]any{
								"repo":   "git@github.com:acme/repo.git",
								"prompt": "fix issue",
							},
						},
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
	if got, want := dispatch.ReplyTo, "https://na.hub.molten.bot/acme/caller"; got != want {
		t.Fatalf("ReplyTo = %q, want %q", got, want)
	}
	if got, want := dispatch.RouteTo, "receiver-agent-uuid"; got != want {
		t.Fatalf("RouteTo = %q, want %q", got, want)
	}
	if got, want := dispatch.OriginatorAgentURI, "https://na.hub.molten.bot/acme/caller"; got != want {
		t.Fatalf("OriginatorAgentURI = %q, want %q", got, want)
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
	if got, want := dispatch.RouteTo, "caller-agent-uuid"; got != want {
		t.Fatalf("RouteTo = %q, want %q", got, want)
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

func TestParseSkillDispatchAcceptsSkillActivationKind(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"kind":           "skill_activation",
		"skill_name":     "code_for_me",
		"payload_format": "json",
		"payload": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship openclaw activation",
		},
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
	if got, want := dispatch.Config.Prompt, "ship openclaw activation"; got != want {
		t.Fatalf("Prompt = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchAcceptsHubNormalizedParameterNames(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":       "skill_request",
		"skill_name": "code_for_me",
		"request_id": "req-normalized-params",
		"payload": map[string]any{
			"repo":         "git@github.com:acme/repo.git",
			"prompt":       "ship normalized parameter aliases",
			"basebranch":   "release",
			"targetsubdir": "cmd/harness",
			"responsemode": "off",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.Config.BaseBranch, "release"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}
	if got, want := dispatch.Config.TargetSubdir, "cmd/harness"; got != want {
		t.Fatalf("TargetSubdir = %q, want %q", got, want)
	}
	if got, want := dispatch.Config.ResponseMode, "off"; got != want {
		t.Fatalf("ResponseMode = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchAcceptsHubNormalizedReviewParameters(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":       "skill_request",
		"skill_name": "code_review",
		"request_id": "req-normalized-review",
		"payload": map[string]any{
			"repo":         "git@github.com:acme/repo.git",
			"prnumber":     42,
			"responsemode": "off",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_review")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.Config.LibraryTaskName, codeReviewLibraryTaskName; got != want {
		t.Fatalf("LibraryTaskName = %q, want %q", got, want)
	}
	if dispatch.Config.Review == nil {
		t.Fatal("Review = nil, want populated review config")
	}
	if got, want := dispatch.Config.Review.PRNumber, 42; got != want {
		t.Fatalf("Review.PRNumber = %d, want %d", got, want)
	}
	if got, want := dispatch.Config.ResponseMode, "off"; got != want {
		t.Fatalf("ResponseMode = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchAcceptsNestedConfigWrappers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  map[string]any
	}{
		{
			name: "input.config",
			msg: map[string]any{
				"type":       "skill_request",
				"skill":      "code_for_me",
				"request_id": "req-input-config",
				"input": map[string]any{
					"config": map[string]any{
						"repo":   "git@github.com:acme/repo.git",
						"prompt": "ship input-config fix",
					},
				},
			},
		},
		{
			name: "input.input",
			msg: map[string]any{
				"type":       "skill_request",
				"skill":      "code_for_me",
				"request_id": "req-input-input",
				"input": map[string]any{
					"input": map[string]any{
						"repo":   "git@github.com:acme/repo.git",
						"prompt": "ship input-input fix",
					},
				},
			},
		},
		{
			name: "payload.input.config",
			msg: map[string]any{
				"type":       "skill_request",
				"skill":      "code_for_me",
				"request_id": "req-payload-input-config",
				"payload": map[string]any{
					"input": map[string]any{
						"config": map[string]any{
							"repo":   "git@github.com:acme/repo.git",
							"prompt": "ship payload-input-config fix",
						},
					},
				},
			},
		},
		{
			name: "payload.input.input",
			msg: map[string]any{
				"type":       "skill_request",
				"skill":      "code_for_me",
				"request_id": "req-payload-input-input",
				"payload": map[string]any{
					"input": map[string]any{
						"input": map[string]any{
							"repo":   "git@github.com:acme/repo.git",
							"prompt": "ship payload-input-input fix",
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dispatch, matched, err := ParseSkillDispatch(tc.msg, "skill_request", "code_for_me")
			if err != nil {
				t.Fatalf("ParseSkillDispatch(%s) error = %v", tc.name, err)
			}
			if !matched {
				t.Fatalf("ParseSkillDispatch(%s) matched = false, want true", tc.name)
			}
			if got := dispatch.Config.RepoURL; got != "git@github.com:acme/repo.git" {
				t.Fatalf("ParseSkillDispatch(%s) RepoURL = %q", tc.name, got)
			}
			if got := strings.TrimSpace(dispatch.Config.Prompt); got == "" {
				t.Fatalf("ParseSkillDispatch(%s) Prompt is empty", tc.name)
			}
		})
	}
}

func TestParseSkillDispatchUsesClientMsgIDForRequestCorrelation(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":          "skill_request",
		"skill":         "code_for_me",
		"id":            "sender-agent-static-id",
		"client_msg_id": "msg-123",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship the fix",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.RequestID, "msg-123"; got != want {
		t.Fatalf("RequestID = %q, want %q", got, want)
	}
}

func TestParseSkillDispatchIgnoresGenericIDWithoutMessageCorrelationFields(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "code_for_me",
		"id":    "sender-agent-static-id",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship the fix",
		},
	}

	dispatch, matched, err := ParseSkillDispatch(msg, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if dispatch.RequestID != "" {
		t.Fatalf("RequestID = %q, want empty", dispatch.RequestID)
	}
}
