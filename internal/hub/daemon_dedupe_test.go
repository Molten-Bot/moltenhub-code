package hub

import (
	"testing"
	"time"
)

func TestDispatchDeduperBeginDone(t *testing.T) {
	t.Parallel()

	d := newDispatchDeduper(2 * time.Minute)
	ok, state := d.Begin("req-1")
	if !ok || state != "accepted" {
		t.Fatalf("first Begin() = (%v, %q)", ok, state)
	}

	ok, state = d.Begin("req-1")
	if ok || state != "in_flight" {
		t.Fatalf("second Begin() = (%v, %q)", ok, state)
	}

	d.Done("req-1")

	ok, state = d.Begin("req-1")
	if ok || state != "completed" {
		t.Fatalf("third Begin() = (%v, %q)", ok, state)
	}
}

func TestDedupeKeyForDispatch(t *testing.T) {
	t.Parallel()

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

