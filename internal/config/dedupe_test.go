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
