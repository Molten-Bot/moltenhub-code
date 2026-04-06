package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSupportsJSONCAndSimplifiedFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	jsonWithComments := `{
  // "version": "v1",
  "repo": "git@github.com:acme/repo.git",
  // "repo_url": "git@github.com:acme/repo.git",
  "prompt": "Implement API change and update tests"
}

this can contain extra notes after the object`
	if err := os.WriteFile(path, []byte(jsonWithComments), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Version != "v1" {
		t.Fatalf("Version = %q, want v1", cfg.Version)
	}
	if cfg.RepoURL != "git@github.com:acme/repo.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != "git@github.com:acme/repo.git" {
		t.Fatalf("Repos = %#v", cfg.Repos)
	}
	if cfg.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", cfg.BaseBranch)
	}
	if cfg.TargetSubdir != "." {
		t.Fatalf("TargetSubdir = %q, want .", cfg.TargetSubdir)
	}
	if cfg.CommitMessage == "" || cfg.PRTitle == "" || cfg.PRBody == "" {
		t.Fatalf("expected defaults for commit/pr metadata, got: %+v", cfg)
	}
}

func TestLoadDefaultsBaseBranch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repo_url": "git@github.com:acme/repo.git",
  "prompt": "fix tests"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", cfg.BaseBranch)
	}
}

func TestLoadPreservesDoubleSlashInsideStrings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repo_url": "https://github.com/acme/repo.git",
  "prompt": "Update docs that reference https://example.com/docs"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RepoURL != "https://github.com/acme/repo.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
}

func TestLoadRejectsMissingRepo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "prompt": "fix tests"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "repo, repo_url, or repos[] is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSupportsReposArray(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repos": [
    "git@github.com:acme/repo-one.git",
    "git@github.com:acme/repo-two.git"
  ],
  "prompt": "update both repos"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := len(cfg.Repos), 2; got != want {
		t.Fatalf("len(Repos) = %d, want %d", got, want)
	}
	if cfg.RepoURL != "git@github.com:acme/repo-one.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
}

func TestLoadSupportsGitHubHandleAsReviewer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	json := `{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "ship change",
  "github_handle": "@octocat"
}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.GitHubHandle, "octocat"; got != want {
		t.Fatalf("GitHubHandle = %q, want %q", got, want)
	}
	if len(cfg.Reviewers) != 1 || cfg.Reviewers[0] != "octocat" {
		t.Fatalf("Reviewers = %#v, want [octocat]", cfg.Reviewers)
	}
}

func TestApplyDefaultsCombinesRepoURLAndRepos(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/primary.git",
		Repos: []string{
			"git@github.com:acme/secondary.git",
		},
		Prompt: "x",
	}
	cfg.ApplyDefaults()
	if got, want := len(cfg.Repos), 2; got != want {
		t.Fatalf("len(Repos) = %d, want %d", got, want)
	}
	if cfg.Repos[0] != "git@github.com:acme/primary.git" {
		t.Fatalf("Repos[0] = %q", cfg.Repos[0])
	}
	if cfg.RepoURL != "git@github.com:acme/primary.git" {
		t.Fatalf("RepoURL = %q", cfg.RepoURL)
	}
}

func TestApplyDefaultsNormalizesPromptImages(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "x",
		Images: []PromptImage{
			{Name: " shot.png ", MediaType: " image/png ", DataBase64: " Zm9v "},
			{},
		},
	}
	cfg.ApplyDefaults()

	if got, want := len(cfg.Images), 1; got != want {
		t.Fatalf("len(Images) = %d, want %d", got, want)
	}
	if got, want := cfg.Images[0].Name, "shot.png"; got != want {
		t.Fatalf("Images[0].Name = %q, want %q", got, want)
	}
	if got, want := cfg.Images[0].MediaType, "image/png"; got != want {
		t.Fatalf("Images[0].MediaType = %q, want %q", got, want)
	}
	if got, want := cfg.Images[0].DataBase64, "Zm9v"; got != want {
		t.Fatalf("Images[0].DataBase64 = %q, want %q", got, want)
	}
}

func TestApplyDefaultsMergesGitHubHandleIntoReviewers(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL:       "git@github.com:acme/repo.git",
		Prompt:        "x",
		GitHubHandle:  " @octocat ",
		Reviewers:     []string{"", "@octocat", "Acme/Platform", "acme/platform"},
		CommitMessage: "commit",
		PRTitle:       "title",
		PRBody:        "body",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.GitHubHandle, "octocat"; got != want {
		t.Fatalf("GitHubHandle = %q, want %q", got, want)
	}
	if got, want := len(cfg.Reviewers), 2; got != want {
		t.Fatalf("len(Reviewers) = %d, want %d (%#v)", got, want, cfg.Reviewers)
	}
	if got, want := cfg.Reviewers[0], "octocat"; got != want {
		t.Fatalf("Reviewers[0] = %q, want %q", got, want)
	}
	if got, want := cfg.Reviewers[1], "Acme/Platform"; got != want {
		t.Fatalf("Reviewers[1] = %q, want %q", got, want)
	}
}

func TestValidateRejectsUnsafeSubdir(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:       "v1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  "../escape",
		Prompt:        "fix tests",
		CommitMessage: "commit",
		PRTitle:       "title",
		PRBody:        "body",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error, got nil")
	}
}

func TestValidateAllowsRootSubdir(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:      "v1",
		RepoURL:      "git@github.com:acme/repo.git",
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       "fix tests",
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateAllowsSSHURLForm(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:      "v1",
		RepoURL:      "ssh://git@github.com/acme/repo.git",
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       "fix tests",
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsMixedSSHURLStyles(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:      "v1",
		RepoURL:      "ssh://git@github.com:acme/repo.git",
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       "fix tests",
	}
	cfg.ApplyDefaults()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "mixed SSH URL styles") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyDefaultsPrefixesDefaultPRTitle(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.PRTitle, "moltenhub-fix tests"; got != want {
		t.Fatalf("PRTitle = %q, want %q", got, want)
	}
}

func TestApplyDefaultsPrefixesCustomPRTitle(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
		PRTitle: "release cleanup",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.PRTitle, "moltenhub-release cleanup"; got != want {
		t.Fatalf("PRTitle = %q, want %q", got, want)
	}
}

func TestApplyDefaultsKeepsExistingPRTitlePrefix(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
		PRTitle: "moltenhub-release cleanup",
	}
	cfg.ApplyDefaults()

	if got, want := cfg.PRTitle, "moltenhub-release cleanup"; got != want {
		t.Fatalf("PRTitle = %q, want %q", got, want)
	}
}

func TestApplyDefaultsAppendsMoltenBotHubFooterToDefaultPRBody(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
	}
	cfg.ApplyDefaults()

	if !strings.HasSuffix(cfg.PRBody, prBodyFooter) {
		t.Fatalf("PRBody = %q, want footer %q at the end", cfg.PRBody, prBodyFooter)
	}
}

func TestApplyDefaultsAppendsMoltenBotHubFooterToCustomPRBody(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
		PRBody:  "Custom PR body.",
	}
	cfg.ApplyDefaults()

	if !strings.Contains(cfg.PRBody, "Custom PR body.") {
		t.Fatalf("PRBody = %q, want to preserve the custom body", cfg.PRBody)
	}
	if !strings.HasSuffix(cfg.PRBody, prBodyFooter) {
		t.Fatalf("PRBody = %q, want footer %q at the end", cfg.PRBody, prBodyFooter)
	}
}

func TestApplyDefaultsDoesNotDuplicateMoltenBotHubFooter(t *testing.T) {
	t.Parallel()

	cfg := Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
		PRBody:  "Custom PR body.\n\n" + prBodyFooter,
	}
	cfg.ApplyDefaults()

	if got := strings.Count(cfg.PRBody, "https://molten.bot/hub"); got != 1 {
		t.Fatalf("PRBody contains %d hub links, want exactly 1: %q", got, cfg.PRBody)
	}
}
