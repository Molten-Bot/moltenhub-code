package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
)

func TestDedupeKeyForRunConfigDefaultsBranchAndNormalizesRepos(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Prompt: "  update docs  ",
		Repos: []string{
			" git@github.com:acme/repo.git ",
			"",
			"git@github.com:acme/repo-two.git",
		},
	}

	got := dedupeKeyForRunConfig(cfg)
	want := `{"repos":["git@github.com:acme/repo.git","git@github.com:acme/repo-two.git"],"baseBranch":"main","promptHash":"` + promptHashForTest("update docs") + `"}`
	if got != want {
		t.Fatalf("dedupeKeyForRunConfig() = %q, want %q", got, want)
	}
}

func TestDedupeKeyForRunConfigIgnoresPromptImages(t *testing.T) {
	t.Parallel()

	cfgA := config.Config{
		Prompt: "update docs",
		Repos:  []string{"git@github.com:acme/repo.git"},
		Images: []config.PromptImage{{Name: "a.png", MediaType: "image/png", DataBase64: "YQ=="}},
	}
	cfgB := config.Config{
		Prompt: "update docs",
		Repos:  []string{"git@github.com:acme/repo.git"},
		Images: []config.PromptImage{{Name: "b.png", MediaType: "image/png", DataBase64: "Yg=="}},
	}

	keyA := dedupeKeyForRunConfig(cfgA)
	keyB := dedupeKeyForRunConfig(cfgB)
	if keyA != keyB {
		t.Fatalf("dedupe keys should match when only image attachments differ\nA: %q\nB: %q", keyA, keyB)
	}
}

func TestDedupeKeyForRunConfigNonMainIncludesPromptHash(t *testing.T) {
	t.Parallel()

	cfgA := config.Config{
		Prompt:     "fix flaky test",
		Repos:      []string{"git@github.com:acme/repo.git"},
		BaseBranch: "release/2026.04-hotfix",
	}
	cfgB := config.Config{
		Prompt:     "fix prod panic",
		Repos:      []string{"git@github.com:acme/repo.git"},
		BaseBranch: "release/2026.04-hotfix",
	}

	keyA := dedupeKeyForRunConfig(cfgA)
	keyB := dedupeKeyForRunConfig(cfgB)
	if keyA == keyB {
		t.Fatalf("dedupe keys should differ when prompts differ\nA: %q\nB: %q", keyA, keyB)
	}

	want := `{"repos":["git@github.com:acme/repo.git"],"baseBranch":"release/2026.04-hotfix","promptHash":"` + promptHashForTest("fix flaky test") + `"}`
	if keyA != want {
		t.Fatalf("dedupeKeyForRunConfig() = %q, want %q", keyA, want)
	}
}

func TestDedupeKeyForRunConfigNonMainNormalizesBranchRefs(t *testing.T) {
	t.Parallel()

	cfgRef := config.Config{
		Prompt:     "fix release issue",
		Repos:      []string{"git@github.com:acme/repo.git"},
		BaseBranch: "refs/heads/release/2026.04-hotfix",
	}
	cfgOrigin := config.Config{
		Prompt:     "another prompt",
		Repos:      []string{"git@github.com:acme/repo.git"},
		BaseBranch: "origin/release/2026.04-hotfix",
	}

	cfgOrigin.Prompt = cfgRef.Prompt
	keyRef := dedupeKeyForRunConfig(cfgRef)
	keyOrigin := dedupeKeyForRunConfig(cfgOrigin)
	if keyRef != keyOrigin {
		t.Fatalf("normalized non-main branch keys differ\nrefs: %q\norigin: %q", keyRef, keyOrigin)
	}
}

func TestDedupeKeyForRunConfigIncludesAgentRuntimeWhenConfigured(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Prompt:       "fix release issue",
		Repos:        []string{"git@github.com:acme/repo.git"},
		BaseBranch:   "main",
		AgentHarness: "claude",
		AgentCommand: "claude-custom",
	}

	got := dedupeKeyForRunConfig(cfg)
	want := `{"repos":["git@github.com:acme/repo.git"],"baseBranch":"main","agentHarness":"claude","agentCommand":"claude-custom","promptHash":"` + promptHashForTest("fix release issue") + `"}`
	if got != want {
		t.Fatalf("dedupeKeyForRunConfig() = %q, want %q", got, want)
	}
}

func TestDedupeKeyForRunConfigDiffersByHarness(t *testing.T) {
	t.Parallel()

	base := config.Config{
		Prompt:     "fix release issue",
		Repos:      []string{"git@github.com:acme/repo.git"},
		BaseBranch: "main",
	}
	claude := base
	claude.AgentHarness = "claude"

	keyBase := dedupeKeyForRunConfig(base)
	keyClaude := dedupeKeyForRunConfig(claude)
	if keyBase == keyClaude {
		t.Fatalf("dedupe keys should differ when agent harness differs\nbase: %q\nclaude: %q", keyBase, keyClaude)
	}
}

func promptHashForTest(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:])
}

func TestLocalSubmissionDeduperCheckBeginDone(t *testing.T) {
	t.Parallel()

	d := newLocalSubmissionDeduper(2 * time.Minute)
	key := "k-1"

	if duplicate, _, _ := d.Check(key, false); duplicate {
		t.Fatal("Check() duplicate = true before Begin")
	}

	if accepted, state, duplicateOf := d.Begin(key, "local-1", false); !accepted || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Begin() = (%v, %q, %q), want (true, accepted, empty)", accepted, state, duplicateOf)
	}

	if duplicate, state, duplicateOf := d.Check(key, false); !duplicate || state != "in_flight" || duplicateOf != "local-1" {
		t.Fatalf("Check() after Begin = (%v, %q, %q)", duplicate, state, duplicateOf)
	}

	if accepted, state, duplicateOf := d.Begin(key, "local-2", false); accepted || state != "in_flight" || duplicateOf != "local-1" {
		t.Fatalf("second Begin() = (%v, %q, %q)", accepted, state, duplicateOf)
	}

	d.Done(key, "local-1", "ok")

	if duplicate, state, duplicateOf := d.Check(key, false); !duplicate || state != "completed" || duplicateOf != "local-1" {
		t.Fatalf("Check() after Done = (%v, %q, %q)", duplicate, state, duplicateOf)
	}
}

func TestLocalSubmissionDeduperAllowsRetryAfterError(t *testing.T) {
	t.Parallel()

	d := newLocalSubmissionDeduper(2 * time.Minute)
	key := "k-error"

	if accepted, state, duplicateOf := d.Begin(key, "local-1", false); !accepted || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Begin() = (%v, %q, %q), want (true, accepted, empty)", accepted, state, duplicateOf)
	}

	d.Done(key, "local-1", "error")

	if duplicate, state, duplicateOf := d.Check(key, false); duplicate || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Check() after error Done = (%v, %q, %q), want (false, accepted, empty)", duplicate, state, duplicateOf)
	}

	if accepted, state, duplicateOf := d.Begin(key, "local-2", false); !accepted || state != "accepted" || duplicateOf != "" {
		t.Fatalf("retry Begin() = (%v, %q, %q), want (true, accepted, empty)", accepted, state, duplicateOf)
	}
}

func TestLocalSubmissionDeduperAllowsExplicitRerunAfterCompletion(t *testing.T) {
	t.Parallel()

	d := newLocalSubmissionDeduper(2 * time.Minute)
	key := "k-rerun"

	if accepted, state, duplicateOf := d.Begin(key, "local-1", false); !accepted || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Begin() = (%v, %q, %q), want (true, accepted, empty)", accepted, state, duplicateOf)
	}
	d.Done(key, "local-1", "ok")

	if duplicate, state, duplicateOf := d.Check(key, true); duplicate || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Check(allowCompleted) = (%v, %q, %q), want (false, accepted, empty)", duplicate, state, duplicateOf)
	}

	if accepted, state, duplicateOf := d.Begin(key, "local-2", true); !accepted || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Begin(allowCompleted) = (%v, %q, %q), want (true, accepted, empty)", accepted, state, duplicateOf)
	}
}

func TestDuplicateSubmissionErrorDetails(t *testing.T) {
	t.Parallel()

	err := newDuplicateSubmissionError("local-9", "in_flight")
	dup, ok := err.(*duplicateSubmissionError)
	if !ok {
		t.Fatalf("error type = %T, want *duplicateSubmissionError", err)
	}
	if dup.DuplicateRequestID() != "local-9" {
		t.Fatalf("DuplicateRequestID() = %q, want local-9", dup.DuplicateRequestID())
	}
	if dup.DuplicateState() != "in_flight" {
		t.Fatalf("DuplicateState() = %q, want in_flight", dup.DuplicateState())
	}
	if dup.Error() != "duplicate submission ignored (request_id=local-9 state=in_flight)" {
		t.Fatalf("Error() = %q", dup.Error())
	}
}

func TestDuplicateSubmissionErrorMessageVariants(t *testing.T) {
	t.Parallel()

	var nilErr *duplicateSubmissionError
	if got, want := nilErr.Error(), "duplicate submission ignored"; got != want {
		t.Fatalf("nil receiver Error() = %q, want %q", got, want)
	}

	tests := []struct {
		name      string
		requestID string
		state     string
		want      string
	}{
		{
			name:      "empty request and state",
			requestID: "   ",
			state:     "\t",
			want:      "duplicate submission ignored",
		},
		{
			name:      "state only",
			requestID: "",
			state:     " completed ",
			want:      "duplicate submission ignored (state=completed)",
		},
		{
			name:      "request only",
			requestID: " local-21 ",
			state:     "",
			want:      "duplicate submission ignored (request_id=local-21)",
		},
		{
			name:      "request and state",
			requestID: " local-22 ",
			state:     " in_flight ",
			want:      "duplicate submission ignored (request_id=local-22 state=in_flight)",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := newDuplicateSubmissionError(tt.requestID, tt.state)
			dup, ok := err.(*duplicateSubmissionError)
			if !ok {
				t.Fatalf("error type = %T, want *duplicateSubmissionError", err)
			}
			if got := dup.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
