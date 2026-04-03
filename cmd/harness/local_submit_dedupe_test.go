package main

import (
	"testing"
	"time"

	"github.com/jef/how-i-want-to-code/internal/config"
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
	want := `{"prompt":"update docs","repos":["git@github.com:acme/repo.git","git@github.com:acme/repo-two.git"],"base_branch":"main"}`
	if got != want {
		t.Fatalf("dedupeKeyForRunConfig() = %q, want %q", got, want)
	}
}

func TestLocalSubmissionDeduperCheckBeginDone(t *testing.T) {
	t.Parallel()

	d := newLocalSubmissionDeduper(2 * time.Minute)
	key := "k-1"

	if duplicate, _, _ := d.Check(key); duplicate {
		t.Fatal("Check() duplicate = true before Begin")
	}

	if accepted, state, duplicateOf := d.Begin(key, "local-1"); !accepted || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Begin() = (%v, %q, %q), want (true, accepted, empty)", accepted, state, duplicateOf)
	}

	if duplicate, state, duplicateOf := d.Check(key); !duplicate || state != "in_flight" || duplicateOf != "local-1" {
		t.Fatalf("Check() after Begin = (%v, %q, %q)", duplicate, state, duplicateOf)
	}

	if accepted, state, duplicateOf := d.Begin(key, "local-2"); accepted || state != "in_flight" || duplicateOf != "local-1" {
		t.Fatalf("second Begin() = (%v, %q, %q)", accepted, state, duplicateOf)
	}

	d.Done(key, "local-1")

	if duplicate, state, duplicateOf := d.Check(key); !duplicate || state != "completed" || duplicateOf != "local-1" {
		t.Fatalf("Check() after Done = (%v, %q, %q)", duplicate, state, duplicateOf)
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
