package config

import (
	"strings"
	"testing"
)

func TestDedupeKeyNormalizesTaskIdentity(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Prompt:       "  fix tests  ",
		Repos:        []string{" git@github.com:acme/repo-b.git ", "git@github.com:acme/repo-a.git"},
		BaseBranch:   "refs/heads/main",
		TargetSubdir: "internal/hub/./",
		AgentHarness: "CODEX",
		AgentCommand: " codex ",
	}

	got := DedupeKey(cfg)
	want := `{"repos":["git@github.com:acme/repo-a.git","git@github.com:acme/repo-b.git"],"baseBranch":"main","targetSubdir":"internal/hub","agentHarness":"codex","agentCommand":"codex","promptHash":"ee9cbc728602f8e7e3e355c3916d8a4866c51955db5badefe9ef0d25f127d639"}`
	if got != want {
		t.Fatalf("DedupeKey() = %q, want %q", got, want)
	}
}

func TestDedupeKeyDefaultsEmptyBranchAndTarget(t *testing.T) {
	t.Parallel()

	got := DedupeKey(Config{
		RepoURL:      "git@github.com:acme/repo.git",
		TargetSubdir: " ",
		Prompt:       "fix tests",
	})
	want := `{"repos":["git@github.com:acme/repo.git"],"baseBranch":"default","targetSubdir":".","promptHash":"ee9cbc728602f8e7e3e355c3916d8a4866c51955db5badefe9ef0d25f127d639"}`
	if got != want {
		t.Fatalf("DedupeKey(empty defaults) = %q, want %q", got, want)
	}
}

func TestDedupeKeyIncludesReviewSelector(t *testing.T) {
	t.Parallel()

	got := DedupeKey(Config{
		RepoURL:    "git@github.com:acme/repo.git",
		BaseBranch: "main",
		Prompt:     "review pull request",
		Review: &ReviewConfig{
			PRNumber:   42,
			PRURL:      " https://github.com/acme/repo/pull/42 ",
			HeadBranch: "refs/heads/feature/review-me",
		},
	})
	want := `"review":{"prNumber":42,"prUrl":"https://github.com/acme/repo/pull/42","headBranch":"feature/review-me"}`
	if !strings.Contains(got, want) {
		t.Fatalf("DedupeKey(review) = %q, want it to contain %q", got, want)
	}
}

func TestDedupeKeyDiffersByReviewPullRequest(t *testing.T) {
	t.Parallel()

	base := Config{
		RepoURL:    "git@github.com:acme/repo.git",
		BaseBranch: "main",
		Prompt:     "review pull request",
	}
	pr112 := base
	pr112.Review = &ReviewConfig{PRNumber: 112}
	pr114 := base
	pr114.Review = &ReviewConfig{PRNumber: 114}

	key112 := DedupeKey(pr112)
	key114 := DedupeKey(pr114)
	if key112 == "" || key112 == key114 {
		t.Fatalf("dedupe keys should differ by review PR\nPR 112: %q\nPR 114: %q", key112, key114)
	}
}

func TestDedupeKeyDiffersByReviewer(t *testing.T) {
	t.Parallel()

	base := Config{
		RepoURL:    "git@github.com:acme/repo.git",
		BaseBranch: "main",
		Prompt:     "fix tests",
	}
	noReviewer := base
	noneReviewer := base
	noneReviewer.Reviewers = []string{"none", ""}
	octocatReviewer := base
	octocatReviewer.Reviewers = []string{"@OctoCat"}
	hubotReviewer := base
	hubotReviewer.Reviewers = []string{"hubot"}

	keyNoReviewer := DedupeKey(noReviewer)
	keyNoneReviewer := DedupeKey(noneReviewer)
	keyOctocatReviewer := DedupeKey(octocatReviewer)
	keyHubotReviewer := DedupeKey(hubotReviewer)

	if keyNoReviewer != keyNoneReviewer {
		t.Fatalf("none reviewer should not change dedupe key\nnone: %q\nempty: %q", keyNoneReviewer, keyNoReviewer)
	}
	if keyNoReviewer == keyOctocatReviewer {
		t.Fatalf("dedupe keys should differ when reviewer is added\nno reviewer: %q\nwith reviewer: %q", keyNoReviewer, keyOctocatReviewer)
	}
	if keyOctocatReviewer == keyHubotReviewer {
		t.Fatalf("dedupe keys should differ when reviewer differs\noctocat: %q\nhubot: %q", keyOctocatReviewer, keyHubotReviewer)
	}
}

func TestDedupeKeyNormalizesReviewerIdentity(t *testing.T) {
	t.Parallel()

	base := Config{
		RepoURL:    "git@github.com:acme/repo.git",
		BaseBranch: "main",
		Prompt:     "fix tests",
	}
	withHandle := base
	withHandle.GitHubHandle = "@OctoCat"
	withReviewer := base
	withReviewer.Reviewers = []string{"octocat"}
	withDuplicateReviewers := base
	withDuplicateReviewers.Reviewers = []string{"hubot", "@OctoCat", "octocat"}

	if got, want := DedupeKey(withHandle), DedupeKey(withReviewer); got != want {
		t.Fatalf("githubHandle and reviewers should produce same dedupe key\ngot: %q\nwant: %q", got, want)
	}
	if got := DedupeKey(withDuplicateReviewers); !strings.Contains(got, `"reviewers":["hubot","octocat"]`) {
		t.Fatalf("DedupeKey(reviewers) = %q, want sorted de-duped lowercase reviewers", got)
	}
}

func TestDedupeKeyDiffersByRequestedReviewer(t *testing.T) {
	t.Parallel()

	base := Config{
		RepoURL:    "git@github.com:acme/repo.git",
		BaseBranch: "main",
		Prompt:     "review pull request",
		Review: &ReviewConfig{
			PRNumber: 42,
		},
	}
	octocat := base
	octocat.Review = &ReviewConfig{
		PRNumber:          42,
		RequestedReviewer: "@OctoCat",
	}
	hubot := base
	hubot.Review = &ReviewConfig{
		PRNumber:          42,
		RequestedReviewer: "hubot",
	}

	keyBase := DedupeKey(base)
	keyOctocat := DedupeKey(octocat)
	keyHubot := DedupeKey(hubot)
	if keyBase == keyOctocat {
		t.Fatalf("dedupe keys should differ when requested reviewer is added\nbase: %q\noctocat: %q", keyBase, keyOctocat)
	}
	if keyOctocat == keyHubot {
		t.Fatalf("dedupe keys should differ when requested reviewer differs\noctocat: %q\nhubot: %q", keyOctocat, keyHubot)
	}
	if !strings.Contains(keyOctocat, `"requestedReviewer":"octocat"`) {
		t.Fatalf("DedupeKey(requested reviewer) = %q, want requested reviewer in review payload", keyOctocat)
	}
}

func TestDedupeHelpersHandleEmptyValues(t *testing.T) {
	t.Parallel()

	if got := normalizeRepoListForDeduper(nil); got != nil {
		t.Fatalf("normalizeRepoListForDeduper(nil) = %#v, want nil", got)
	}
	got := normalizeRepoListForDeduper([]string{" repo-b ", " ", "repo-a"})
	want := []string{"repo-a", "repo-b"}
	if len(got) != len(want) {
		t.Fatalf("normalizeRepoListForDeduper() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeRepoListForDeduper()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if got := normalizeTargetSubdirForDeduper(" "); got != "." {
		t.Fatalf("normalizeTargetSubdirForDeduper(empty) = %q, want .", got)
	}
}
