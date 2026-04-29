package config

import "testing"

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
