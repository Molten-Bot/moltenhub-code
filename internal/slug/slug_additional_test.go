package slug

import "testing"

func TestTrimGeneratedPromptSuffixKeepsOriginalWhenTrimWouldBeEmpty(t *testing.T) {
	t.Parallel()

	in := "-20260407-002959"
	if got := trimGeneratedPromptSuffix(in); got != in {
		t.Fatalf("trimGeneratedPromptSuffix(%q) = %q, want %q", in, got, in)
	}
}

func TestFromPromptTruncatesAndTrimsBoundarySeparators(t *testing.T) {
	t.Parallel()

	got := FromPrompt("This branch name should stop exactly before trailing separators ######## and keep readable")
	if len(got) > 40 {
		t.Fatalf("len(FromPrompt()) = %d, want <= 40", len(got))
	}
	if got[len(got)-1] == '-' {
		t.Fatalf("FromPrompt() = %q ends with '-'", got)
	}
}

func TestFromPromptEmptyFallsBackToTask(t *testing.T) {
	t.Parallel()

	if got := FromPrompt(" \n\t "); got != "task" {
		t.Fatalf("FromPrompt(empty) = %q, want task", got)
	}
}

func TestTrimGeneratedPromptSuffixEmptyInput(t *testing.T) {
	t.Parallel()

	if got := trimGeneratedPromptSuffix(" "); got != "" {
		t.Fatalf("trimGeneratedPromptSuffix(empty) = %q, want empty", got)
	}
}
