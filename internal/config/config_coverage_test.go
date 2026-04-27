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

	if err := (&Config{}).UnmarshalJSON([]byte(`not-json`)); err == nil {
		t.Fatal("UnmarshalJSON(invalid json) error = nil, want non-nil")
	}
	if err := json.Unmarshal([]byte(`{"repos":{}}`), &Config{}); err == nil {
		t.Fatal("UnmarshalJSON(invalid repos type) error = nil, want non-nil")
	}
	if err := json.Unmarshal([]byte(`{"branch":{}}`), &Config{}); err == nil {
		t.Fatal("UnmarshalJSON(invalid branch alias) error = nil, want non-nil")
	}

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
	if err := rejectSnakeCaseImageFields(json.RawMessage(`[{"mediaType":"image/png"}]`)); err != nil {
		t.Fatalf("rejectSnakeCaseImageFields(canonical image fields) error = %v", err)
	}
}

func TestLoadAndValidateErrorPaths(t *testing.T) {
	t.Parallel()

	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Load(missing) error = %v, want read failure", err)
	}

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

	cfg = Config{Version: " "}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("Validate(missing version) error = %v, want version required", err)
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
	cfg.CommitMessage = "msg"
	cfg.PRTitle = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "prTitle is required") {
		t.Fatalf("Validate(missing pr title) error = %v", err)
	}
	cfg.PRTitle = "title"
	cfg.PRBody = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "prBody is required") {
		t.Fatalf("Validate(missing pr body) error = %v", err)
	}
	cfg.PRBody = "body"
	cfg.Images = []PromptImage{{MediaType: "image/png"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "images[0].dataBase64 is required") {
		t.Fatalf("Validate(invalid image) error = %v", err)
	}
	cfg.Images = nil
	cfg.BaseBranch = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "baseBranch is required") {
		t.Fatalf("Validate(missing baseBranch) error = %v", err)
	}
	cfg.BaseBranch = "main"
	cfg.TargetSubdir = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "targetSubdir is required") {
		t.Fatalf("Validate(missing targetSubdir) error = %v", err)
	}
	cfg.TargetSubdir = "."
	cfg.Prompt = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("Validate(missing prompt) error = %v", err)
	}
}

func TestNormalizationAndValidationHelpers(t *testing.T) {
	t.Parallel()

	if got := normalizePromptImages([]PromptImage{{}, {Name: " shot.png ", MediaType: " image/png ", DataBase64: " ZGF0YQ== "}}); len(got) != 1 {
		t.Fatalf("normalizePromptImages() len = %d, want 1", len(got))
	}
	if got := normalizePromptImages([]PromptImage{{Name: " "}}); got != nil {
		t.Fatalf("normalizePromptImages(blank values) = %#v, want nil", got)
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
	if err := validateRepoRef("https://[::1"); err == nil {
		t.Fatal("validateRepoRef(parse error) error = nil, want non-nil")
	}
	if err := validateRepoRef("git@github.com:acme/repo with spaces.git"); err == nil {
		t.Fatal("validateRepoRef(whitespace) error = nil, want non-nil")
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
	if got := NormalizeResponseMode("unknown"); got != "" {
		t.Fatalf("NormalizeResponseMode(unknown) = %q, want empty", got)
	}
	if got := NormalizeResponseMode("wenyan_lite"); got != "caveman-wenyan-lite" {
		t.Fatalf("NormalizeResponseMode(wenyan_lite) = %q, want caveman-wenyan-lite", got)
	}
	if got := NormalizeResponseMode("wenyan_ultra"); got != "caveman-wenyan-ultra" {
		t.Fatalf("NormalizeResponseMode(wenyan_ultra) = %q, want caveman-wenyan-ultra", got)
	}
	if modes := SupportedResponseModes(); len(modes) != 7 || modes[0] != DisabledResponseMode {
		t.Fatalf("SupportedResponseModes() = %#v", modes)
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
	if got := normalizePRTitle(" "); got != "Automated update" {
		t.Fatalf("normalizePRTitle(empty) = %q, want Automated update", got)
	}
	if got := trimGeneratedPRTitleSuffix(" -20260407-002959"); got != "-20260407-002959" {
		t.Fatalf("trimGeneratedPRTitleSuffix(generated-only) = %q, want original title", got)
	}
	if got := trimGeneratedPRTitleSuffix(" "); got != "" {
		t.Fatalf("trimGeneratedPRTitleSuffix(empty) = %q, want empty", got)
	}
	if got := ensurePRBodyFooter(" "); got != prBodyFooter {
		t.Fatalf("ensurePRBodyFooter(empty) = %q, want footer", got)
	}
	if got := stripLineComments([]byte("{\"url\":\"https://example.com\"}//note\n")); strings.Contains(string(got), "//note") {
		t.Fatalf("stripLineComments() retained comment: %q", string(got))
	}
	if got := stripLineComments([]byte(`{"quote":"escaped \\\" value"}`)); string(got) != `{"quote":"escaped \\\" value"}` {
		t.Fatalf("stripLineComments(escaped string) = %q", string(got))
	}
	if got := normalizeNonEmptyStrings([]string{" a ", "", "a", "b "}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("normalizeNonEmptyStrings() = %#v, want [a b]", got)
	}
	if got := normalizeNonEmptyStrings([]string{" ", "\t"}); got != nil {
		t.Fatalf("normalizeNonEmptyStrings(empty values) = %#v, want nil", got)
	}
	if got := prependIfMissing([]string{"b"}, " "); len(got) != 1 || got[0] != "b" {
		t.Fatalf("prependIfMissing(empty value) = %#v, want unchanged", got)
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
