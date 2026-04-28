package library

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentsSeedSharesRuntimeToolingAndSafety(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	agentsPath := filepath.Join(repoRoot, "library", "AGENTS.md")

	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", agentsPath, err)
	}

	content := string(data)
	for _, want := range []string{
		"Playwright is installed",
		"`npm` is available",
		"Python, `pip`, and `virtualenv`",
		"Go is available",
		"`Failure:` and `Error details:`",
		"`gh repo view OWNER/REPO --json isPrivate,nameWithOwner`",
		"concrete repository evidence",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing AGENTS seed instruction %q", agentsPath, want)
		}
	}
}
