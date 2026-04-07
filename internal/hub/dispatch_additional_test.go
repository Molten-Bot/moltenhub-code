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

	normalized, err := normalizeRunConfigMap(`{"repo":"git@github.com:acme/repo.git","branch":"release","prompt":"do work"}`)
	if err != nil {
		t.Fatalf("normalizeRunConfigMap(string) error = %v", err)
	}
	if got, want := stringAt(normalized, "baseBranch"), "release"; got != want {
		t.Fatalf("baseBranch alias = %q, want %q", got, want)
	}

	if _, err := normalizeRunConfigMap(`["not","an","object"]`); err == nil {
		t.Fatal("normalizeRunConfigMap(array JSON) error = nil, want non-nil")
	}
	if _, err := normalizeRunConfigMap(42); err == nil {
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
