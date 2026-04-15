package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigUnmarshalBranchAliasAndImageSnakeCaseFallback(t *testing.T) {
	t.Parallel()

	var cfg Config
	if err := json.Unmarshal([]byte(`{"repo":"git@github.com:acme/repo.git","branch":"release","prompt":"x"}`), &cfg); err != nil {
		t.Fatalf("UnmarshalJSON(branch alias) error = %v", err)
	}
	if got, want := cfg.BaseBranch, "release"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}

	if err := rejectSnakeCaseImageFields(json.RawMessage(`{"not":"an array"}`)); err != nil {
		t.Fatalf("rejectSnakeCaseImageFields(non-array) error = %v, want nil", err)
	}
	if err := rejectSnakeCaseImageFields(json.RawMessage(`[{"data_base64":"x"}]`)); err == nil {
		t.Fatal("rejectSnakeCaseImageFields(data_base64) error = nil, want non-nil")
	}
}

func TestLoadAndValidateErrorPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"repo":"git@github.com:acme/repo.git"`), 0o644); err != nil {
		t.Fatalf("WriteFile(bad) error = %v", err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "parse config json") {
		t.Fatalf("Load(bad) error = %v, want parse failure", err)
	}

	cfg := Config{Version: "v2"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `unsupported version "v2"`) {
		t.Fatalf("Validate(unsupported version) error = %v", err)
	}

	cfg = Config{
		Version:       "v1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  ".",
		Prompt:        "review",
		AgentHarness:  "codex",
		CommitMessage: "msg",
		PRTitle:       "title",
		PRBody:        "body",
		Review:        &ReviewConfig{PRNumber: -1},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "review.prNumber") {
		t.Fatalf("Validate(negative review prNumber) error = %v", err)
	}

	cfg.Review = &ReviewConfig{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "review.prNumber, review.prUrl, or review.headBranch is required") {
		t.Fatalf("Validate(empty review) error = %v", err)
	}

	cfg.Review = &ReviewConfig{PRURL: "://bad-url"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "invalid review.prUrl") {
		t.Fatalf("Validate(bad review URL) error = %v", err)
	}

	cfg.Review = nil
	cfg.Images = []PromptImage{{DataBase64: base64.StdEncoding.EncodeToString([]byte("img")), MediaType: "image/png"}}
	cfg.CommitMessage = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "commitMessage is required") {
		t.Fatalf("Validate(missing commit message) error = %v", err)
	}
}

func TestNormalizationAndValidationHelpers(t *testing.T) {
	t.Parallel()

	if got := normalizePromptImages([]PromptImage{{}, {Name: " shot.png ", MediaType: " image/png ", DataBase64: " ZGF0YQ== "}}); len(got) != 1 {
		t.Fatalf("normalizePromptImages() len = %d, want 1", len(got))
	}
	if got := normalizeReviewConfig(&ReviewConfig{}); got != nil {
		t.Fatalf("normalizeReviewConfig(empty) = %#v, want nil", got)
	}
	if err := validateReviewConfig(&ReviewConfig{PRNumber: 1}, []string{"a", "b"}); err == nil {
		t.Fatal("validateReviewConfig(multi-repo) error = nil, want non-nil")
	}
	if err := validateReviewConfig(&ReviewConfig{PRURL: "github.com/acme/repo/pull/1"}, []string{"a"}); err == nil {
		t.Fatal("validateReviewConfig(missing scheme/host) error = nil, want non-nil")
	}
	if err := validateReviewConfig(&ReviewConfig{PRURL: "https://github.com/acme/repo/pull/1"}, []string{"a"}); err != nil {
		t.Fatalf("validateReviewConfig(valid) error = %v", err)
	}
	if err := validateReviewConfig(&ReviewConfig{HeadBranch: "feature/review-me"}, []string{"a"}); err != nil {
		t.Fatalf("validateReviewConfig(headBranch) error = %v", err)
	}
	if err := validateSubdir("../escape"); err == nil {
		t.Fatal("validateSubdir(escape) error = nil, want non-nil")
	}
	if err := validateRepoRef("ssh://git@github.com:owner/repo.git"); err == nil {
		t.Fatal("validateRepoRef(mixed ssh styles) error = nil, want non-nil")
	}
	if err := validateRepoRef("https:///repo.git"); err == nil {
		t.Fatal("validateRepoRef(missing host) error = nil, want non-nil")
	}
	if err := validateRepoRef("file:///tmp/repo.git"); err != nil {
		t.Fatalf("validateRepoRef(file URL) error = %v", err)
	}
	if got := NormalizeResponseMode("wenyan"); got != "caveman-wenyan-full" {
		t.Fatalf("NormalizeResponseMode(wenyan) = %q, want caveman-wenyan-full", got)
	}
	if got := NormalizeResponseMode("default"); got != "caveman-full" {
		t.Fatalf("NormalizeResponseMode(default) = %q, want caveman-full", got)
	}
	if got := NormalizeResponseMode("off"); got != DisabledResponseMode {
		t.Fatalf("NormalizeResponseMode(off) = %q, want %q", got, DisabledResponseMode)
	}
	if modes := SupportedResponseModesWithDefault(); len(modes) != 8 || modes[0] != "default" || modes[1] != DisabledResponseMode {
		t.Fatalf("SupportedResponseModesWithDefault() = %#v", modes)
	}
}

func TestValidateRejectsUnsupportedResponseMode(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:       "v1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  ".",
		Prompt:        "review",
		ResponseMode:  "loud-mode",
		AgentHarness:  "codex",
		CommitMessage: "msg",
		PRTitle:       "title",
		PRBody:        "body",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported responseMode") {
		t.Fatalf("Validate(unsupported responseMode) error = %v", err)
	}
}

func TestDefaultMetadataAndStringHelpers(t *testing.T) {
	t.Parallel()

	if got := defaultCommitMessage(""); got != "chore: automated update" {
		t.Fatalf("defaultCommitMessage(empty) = %q", got)
	}
	if got := defaultPRTitle(""); got != "Automated update" {
		t.Fatalf("defaultPRTitle(empty) = %q", got)
	}
	if got := defaultPRBody(""); !strings.Contains(got, prBodyFooter) {
		t.Fatalf("defaultPRBody(empty) = %q, want footer", got)
	}
	if got := defaultPRBody("run the full regression suite"); !strings.Contains(got, "Original task prompt:\n```text\nrun the full regression suite\n```") {
		t.Fatalf("defaultPRBody(prompt) = %q, want original prompt block", got)
	}
	if got := normalizePRTitle("moltenhub-existing-title"); got != "existing-title" {
		t.Fatalf("normalizePRTitle(existing prefix) = %q", got)
	}
	if got := stripLineComments([]byte("{\"url\":\"https://example.com\"}//note\n")); strings.Contains(string(got), "//note") {
		t.Fatalf("stripLineComments() retained comment: %q", string(got))
	}
	if got := normalizeNonEmptyStrings([]string{" a ", "", "a", "b "}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("normalizeNonEmptyStrings() = %#v, want [a b]", got)
	}
	if got := prependIfMissing([]string{"a"}, "a"); len(got) != 1 {
		t.Fatalf("prependIfMissing(existing) = %#v, want unchanged", got)
	}
	if got := prependIfMissing([]string{"b"}, "a"); len(got) != 2 || got[0] != "a" {
		t.Fatalf("prependIfMissing(new) = %#v, want prefixed a", got)
	}
	if got := normalizeReviewerList([]string{" @OctoCat ", "octocat", "", "@hubbot"}); len(got) != 2 || got[0] != "OctoCat" || got[1] != "hubbot" {
		t.Fatalf("normalizeReviewerList() = %#v", got)
	}
	if got := normalizeReviewer(" none "); got != "" {
		t.Fatalf("normalizeReviewer(none) = %q, want empty", got)
	}
	if got := mergeReviewers([]string{"reviewer"}, "@octocat"); len(got) != 2 || got[0] != "octocat" {
		t.Fatalf("mergeReviewers() = %#v", got)
	}
}
