package slug

import (
	"testing"
	"time"
)

func TestFromPromptSanitizes(t *testing.T) {
	t.Parallel()

	got := FromPrompt("  Build API: Add OAuth + tests!!!  ")
	want := "build-api-add-oauth-tests"
	if got != want {
		t.Fatalf("FromPrompt() = %q, want %q", got, want)
	}
}

func TestFromPromptFallback(t *testing.T) {
	t.Parallel()

	got := FromPrompt("###")
	if got != "task" {
		t.Fatalf("FromPrompt() = %q, want task", got)
	}
}

func TestFromPromptTrimsGeneratedSuffix(t *testing.T) {
	t.Parallel()

	got := FromPrompt("fix flaky ci-20260407-002959-2fc3c864")
	want := "fix-flaky-ci"
	if got != want {
		t.Fatalf("FromPrompt() = %q, want %q", got, want)
	}
}

func TestFromPromptTrimsGeneratedSuffixWithoutHash(t *testing.T) {
	t.Parallel()

	got := FromPrompt("release cleanup-20260407-002959")
	want := "release-cleanup"
	if got != want {
		t.Fatalf("FromPrompt() = %q, want %q", got, want)
	}
}

func TestBranchNameUsesStableSlug(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	got := BranchName("Build API", now, "abcdef123456")
	want := "moltenhub-build-api"
	if got != want {
		t.Fatalf("BranchName() = %q, want %q", got, want)
	}
}
