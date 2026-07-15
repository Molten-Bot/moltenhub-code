package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmarshalRejectsNonCanonicalGuardFieldCasing(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"librarytaskname", "LibraryTaskName", "requiresnondefaultbranch"} {
		payload := []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"work","` + field + `":"value"}`)
		var cfg Config
		err := json.Unmarshal(payload, &cfg)
		if err == nil || !strings.Contains(err.Error(), "unsupported field") {
			t.Fatalf("json.Unmarshal(%q) error = %v, want noncanonical guard field rejection", field, err)
		}
	}
}

func TestValidatePromptImageValidationPaths(t *testing.T) {
	t.Parallel()

	if err := validatePromptImage(PromptImage{}, 0); err == nil {
		t.Fatal("validatePromptImage(empty) error = nil, want non-nil")
	}
	if err := validatePromptImage(PromptImage{
		MediaType:  "text/plain",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("x")),
	}, 1); err == nil {
		t.Fatal("validatePromptImage(non-image mediaType) error = nil, want non-nil")
	}
	if err := validatePromptImage(PromptImage{
		MediaType:  "image/png",
		DataBase64: "%%%not-base64%%%",
	}, 2); err == nil {
		t.Fatal("validatePromptImage(invalid base64) error = nil, want non-nil")
	}
	if err := validatePromptImage(PromptImage{
		MediaType:  "image/png",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("hello")),
	}, 3); err != nil {
		t.Fatalf("validatePromptImage(valid) error = %v", err)
	}
}

func TestValidateRequiresBaseBranchForNonDefaultBranchRuns(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL:                  "git@github.com:acme/repo.git",
		Prompt:                   "merge main into current branch",
		RequiresNonDefaultBranch: true,
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "baseBranch is required when requiresNonDefaultBranch is true") {
		t.Fatalf("Validate() error = %v", err)
	}

	cfg.BaseBranch = "feature/conflicted"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate(feature branch) error = %v", err)
	}

	for _, branch := range []string{"main", "refs/heads/master", "origin/trunk"} {
		cfg.BaseBranch = branch
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "protected default branch name") {
			t.Fatalf("Validate(%q) error = %v, want protected branch rejection", branch, err)
		}
	}
}

func TestLoadAcceptsLegacyDataURIPromptImageStrings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "inspect screenshot",
  "images": [
    "data:image/png;base64,aGVsbG8="
  ]
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := len(cfg.Images), 1; got != want {
		t.Fatalf("len(Images) = %d, want %d", got, want)
	}
	if got, want := cfg.Images[0].MediaType, "image/png"; got != want {
		t.Fatalf("Images[0].MediaType = %q, want %q", got, want)
	}
	if got, want := cfg.Images[0].DataBase64, "aGVsbG8="; got != want {
		t.Fatalf("Images[0].DataBase64 = %q, want %q", got, want)
	}
}

func TestValidateSubdirAndRepoRefEdgeCases(t *testing.T) {
	t.Parallel()

	if err := validateSubdir("/abs/path"); err == nil {
		t.Fatal("validateSubdir(abs) error = nil, want non-nil")
	}
	if err := validateSubdir("../../escape"); err == nil {
		t.Fatal("validateSubdir(escape) error = nil, want non-nil")
	}
	if err := validateSubdir("nested/../safe"); err != nil {
		t.Fatalf("validateSubdir(clean relative) error = %v", err)
	}

	if err := validateRepoRef(" "); err == nil {
		t.Fatal("validateRepoRef(empty) error = nil, want non-nil")
	}
	if err := validateRepoRef("https://github.com"); err == nil {
		t.Fatal("validateRepoRef(missing path) error = nil, want non-nil")
	}
	if err := validateRepoRef("file://"); err == nil {
		t.Fatal("validateRepoRef(file missing path) error = nil, want non-nil")
	}
	if err := validateRepoRef("git@github.com:acme/repo.git"); err != nil {
		t.Fatalf("validateRepoRef(scp syntax) error = %v", err)
	}
}

func TestSummarizeAndFirstNonEmptyTrimmed(t *testing.T) {
	t.Parallel()

	if got := summarize("   ", 12); got != "" {
		t.Fatalf("summarize(empty) = %q, want empty", got)
	}
	if got := summarize("alpha beta gamma delta", 12); got != "alpha beta" {
		t.Fatalf("summarize(max=12) = %q, want %q", got, "alpha beta")
	}
	if got := summarize("value,,,", 32); got != "value" {
		t.Fatalf("summarize(trailing punctuation) = %q, want %q", got, "value")
	}

	if got := firstNonEmptyTrimmed(" ", "\n", " value "); got != "value" {
		t.Fatalf("firstNonEmptyTrimmed() = %q, want %q", got, "value")
	}
}

func TestTrimGeneratedPRTitleSuffixAndEnsureFooter(t *testing.T) {
	t.Parallel()

	if got := trimGeneratedPRTitleSuffix("cleanup-20260407-002959"); got != "cleanup" {
		t.Fatalf("trimGeneratedPRTitleSuffix() = %q, want %q", got, "cleanup")
	}
	if got := trimGeneratedPRTitleSuffix("release---"); got != "release" {
		t.Fatalf("trimGeneratedPRTitleSuffix(no generated suffix) = %q, want %q", got, "release")
	}
	if got := ensurePRBodyFooter("body\n\n" + prBodyFooter); strings.Count(got, "https://molten.bot/code?source=pr") != 1 {
		t.Fatalf("ensurePRBodyFooter(contains footer) duplicated link: %q", got)
	}
	if got := ensurePRBodyPromptAndFooter("body", "investigate failing tests"); !strings.Contains(got, "Original task prompt:\n```text\ninvestigate failing tests\n```") {
		t.Fatalf("ensurePRBodyPromptAndFooter() = %q, want original prompt block", got)
	}
	if got := ensurePRBodyPromptAndFooter("body\n\nOriginal task prompt:\n```text\ninvestigate failing tests\n```", "investigate failing tests"); strings.Count(got, "Original task prompt:") != 1 {
		t.Fatalf("ensurePRBodyPromptAndFooter(existing prompt) duplicated heading: %q", got)
	}
	if got := ensurePRBodyPromptAndFooter("body\n\n"+prBodyFooter, "investigate failing tests"); !strings.HasSuffix(got, prBodyFooter) {
		t.Fatalf("ensurePRBodyPromptAndFooter(existing footer) = %q, want footer at end", got)
	}
}

func TestLoadRejectsSnakeCaseAgentHarnessFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "run task",
  "agent_harness": "claude"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported field error")
	}
	if !strings.Contains(err.Error(), "agent_harness") || !strings.Contains(err.Error(), "agentHarness") {
		t.Fatalf("Load() error = %v, want agent_harness canonicalization hint", err)
	}
}

func TestApplyDefaultsReadsAgentRuntimeFromEnv(t *testing.T) {
	t.Setenv("HARNESS_AGENT_HARNESS", "CLAUDE")
	t.Setenv("HARNESS_AGENT_COMMAND", "claude-custom")

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "run task",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.AgentHarness, "claude"; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := cfg.AgentCommand, "claude-custom"; got != want {
		t.Fatalf("AgentCommand = %q, want %q", got, want)
	}
}

func TestApplyDefaultsNormalizesResponseModeAliases(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL:       "git@github.com:acme/repo.git",
		Prompt:        "run task",
		ResponseMode:  " caveman ",
		AgentHarness:  "codex",
		CommitMessage: "msg",
		PRTitle:       "title",
		PRBody:        "body",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.ResponseMode, "caveman-full"; got != want {
		t.Fatalf("ResponseMode = %q, want %q", got, want)
	}
}

func TestApplyDefaultsDefaultsResponseModeToCavemanFull(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL:       "git@github.com:acme/repo.git",
		Prompt:        "run task",
		AgentHarness:  "codex",
		CommitMessage: "msg",
		PRTitle:       "title",
		PRBody:        "body",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.ResponseMode, "caveman-full"; got != want {
		t.Fatalf("ResponseMode = %q, want %q", got, want)
	}
}

func TestApplyDefaultsPreservesExplicitResponseModeOptOut(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL:       "git@github.com:acme/repo.git",
		Prompt:        "run task",
		ResponseMode:  " off ",
		AgentHarness:  "codex",
		CommitMessage: "msg",
		PRTitle:       "title",
		PRBody:        "body",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.ResponseMode, DisabledResponseMode; got != want {
		t.Fatalf("ResponseMode = %q, want %q", got, want)
	}
}

func TestApplyDefaultsLowercasesUnsupportedResponseMode(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL:       "git@github.com:acme/repo.git",
		Prompt:        "run task",
		ResponseMode:  " Loud_Mode ",
		AgentHarness:  "codex",
		CommitMessage: "msg",
		PRTitle:       "title",
		PRBody:        "body",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.ResponseMode, "loud_mode"; got != want {
		t.Fatalf("ResponseMode = %q, want %q", got, want)
	}
}

func TestLoadRejectsSnakeCaseResponseModeField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "run task",
  "response_mode": "caveman-full"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported field error")
	}
	if !strings.Contains(err.Error(), "response_mode") || !strings.Contains(err.Error(), "responseMode") {
		t.Fatalf("Load() error = %v, want response_mode canonicalization hint", err)
	}
}
