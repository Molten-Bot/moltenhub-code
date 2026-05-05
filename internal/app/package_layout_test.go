package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInternalPackageLayoutMatchesAppHubWebSplit(t *testing.T) {
	t.Parallel()

	repoRoot := currentRepoRoot(t)
	for _, rel := range []string{
		"internal/app",
		"internal/hub",
		"internal/web",
		"internal/web/static",
	} {
		if info, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		} else if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", rel)
		}
	}

	for _, rel := range []string{
		"internal/harness",
		"internal/hubui",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err == nil {
			t.Fatalf("legacy package directory %s should not exist", rel)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", rel, err)
		}
	}
}

func currentRepoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
