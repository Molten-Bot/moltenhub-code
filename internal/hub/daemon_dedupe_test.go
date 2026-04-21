package hub

import (
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
)

func TestDispatchDeduperBeginDone(t *testing.T) {
	t.Parallel()

	d := newDispatchDeduper(2 * time.Minute)
	ok, state, duplicateOf := d.Begin("key-1", "req-1")
	if !ok || state != "accepted" || duplicateOf != "" {
		t.Fatalf("first Begin() = (%v, %q, %q)", ok, state, duplicateOf)
	}

	ok, state, duplicateOf = d.Begin("key-1", "req-2")
	if ok || state != "in_flight" || duplicateOf != "req-1" {
		t.Fatalf("second Begin() = (%v, %q, %q)", ok, state, duplicateOf)
	}

	d.Done("key-1", "req-1", "completed")

	ok, state, duplicateOf = d.Begin("key-1", "req-3")
	if ok || state != "completed" || duplicateOf != "req-1" {
		t.Fatalf("third Begin() = (%v, %q, %q)", ok, state, duplicateOf)
	}
}

func TestDispatchDeduperDoneErrorAllowsRetry(t *testing.T) {
	t.Parallel()

	d := newDispatchDeduper(2 * time.Minute)
	if ok, state, duplicateOf := d.Begin("key-err", "req-1"); !ok || state != "accepted" || duplicateOf != "" {
		t.Fatalf("Begin() = (%v, %q, %q)", ok, state, duplicateOf)
	}

	d.Done("key-err", "req-1", "error")

	if ok, state, duplicateOf := d.Begin("key-err", "req-2"); !ok || state != "accepted" || duplicateOf != "" {
		t.Fatalf("retry Begin() = (%v, %q, %q)", ok, state, duplicateOf)
	}
}

func TestDedupeKeyForDispatch(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Repo:   "git@github.com:acme/repo.git",
		Prompt: "fix tests",
	}
	cfg.ApplyDefaults()
	if gotA, gotB := dedupeKeyForDispatch(SkillDispatch{RequestID: "req-a", Config: cfg}, "m-a", "d-a"), dedupeKeyForDispatch(SkillDispatch{RequestID: "req-b", Config: cfg}, "m-b", "d-b"); gotA == "" || gotA != gotB {
		t.Fatalf("config dedupe key mismatch: A=%q B=%q", gotA, gotB)
	}

	if got := dedupeKeyForDispatch(SkillDispatch{RequestID: "req-x"}, "m-1", "d-1"); got != "req-x" {
		t.Fatalf("dedupeKeyForDispatch() = %q", got)
	}
	if got := dedupeKeyForDispatch(SkillDispatch{}, "m-1", "d-1"); got != "m-1" {
		t.Fatalf("dedupeKeyForDispatch() = %q", got)
	}
	if got := dedupeKeyForDispatch(SkillDispatch{}, "", "d-1"); got != "d-1" {
		t.Fatalf("dedupeKeyForDispatch() = %q", got)
	}
}
