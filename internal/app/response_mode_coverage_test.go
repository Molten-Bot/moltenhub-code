package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWithResponseModePromptReturnsInstructionBlockForBlankPrompt(t *testing.T) {
	t.Parallel()

	got, err := withResponseModePrompt(" \n\t ", "caveman-ultra")
	if err != nil {
		t.Fatalf("withResponseModePrompt() error = %v", err)
	}
	if !strings.Contains(got, "Selected intensity: ultra.") {
		t.Fatalf("prompt = %q, want ultra intensity", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("prompt contains unexpected empty trailing prompt block: %q", got)
	}
}

func TestResponseModeSkillPathCandidatesIncludesSeedRelativePathOnce(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	t.Setenv("HARNESS_AGENTS_SEED_PATH", filepath.Join(repoRoot, "library", "AGENTS.md"))

	candidates := responseModeSkillPathCandidates()
	want := filepath.Join(repoRoot, "skills", "caveman", "SKILL.md")
	count := 0
	for _, candidate := range candidates {
		if candidate == "" {
			t.Fatal("candidate path is blank")
		}
		if candidate == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("candidate %q count = %d, want 1", want, count)
	}
}

func TestStripSkillFrontMatterEdges(t *testing.T) {
	t.Parallel()

	if got := stripSkillFrontMatter("body only"); got != "body only" {
		t.Fatalf("stripSkillFrontMatter(no frontmatter) = %q", got)
	}
	if got := stripSkillFrontMatter("---not-frontmatter\nbody"); got != "---not-frontmatter\nbody" {
		t.Fatalf("stripSkillFrontMatter(non-delimited) = %q", got)
	}
	if got := stripSkillFrontMatter("---\nname: caveman\nbody"); got != "---\nname: caveman\nbody" {
		t.Fatalf("stripSkillFrontMatter(unclosed) = %q", got)
	}
}

func TestCavemanIntensityLabels(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"caveman-lite":         "lite",
		"caveman-full":         "full",
		"caveman-ultra":        "ultra",
		"caveman-wenyan-lite":  "wenyan-lite",
		"caveman-wenyan-full":  "wenyan-full",
		"caveman-wenyan-ultra": "wenyan-ultra",
	}
	for mode, want := range tests {
		if got := cavemanIntensityLabel(mode); got != want {
			t.Fatalf("cavemanIntensityLabel(%q) = %q, want %q", mode, got, want)
		}
	}
}
