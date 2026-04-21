package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/failurefollowup"
)

func TestResetLogRootRejectsUnsafeDirectories(t *testing.T) {
	t.Parallel()

	if err := resetLogRoot(""); err == nil {
		t.Fatal("resetLogRoot(empty) error = nil, want non-nil")
	}
	if err := resetLogRoot(t.TempDir()); err == nil {
		t.Fatal("resetLogRoot(non-.log) error = nil, want non-nil")
	}
}

func TestNewTaskLogMirrorRejectsEmptyRoot(t *testing.T) {
	t.Parallel()

	if _, err := newTaskLogMirror("   "); err == nil {
		t.Fatal("newTaskLogMirror(empty) error = nil, want non-nil")
	}
}

func TestTaskLogMirrorNilAndCloseOldestNoopPaths(t *testing.T) {
	t.Parallel()

	var mirror *taskLogMirror
	mirror.WriteLine("line")
	if err := mirror.Close(); err != nil {
		t.Fatalf("(*taskLogMirror)(nil).Close() error = %v", err)
	}

	m := &taskLogMirror{}
	m.closeOldestTaskFileLocked()
}

func TestTaskLogFilePathUsesFallbackSubdirForBlankInput(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), ".log")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	mirror := &taskLogMirror{rootDir: root}

	path, err := mirror.taskLogFilePathLocked("   ", "")
	if err != nil {
		t.Fatalf("taskLogFilePathLocked() error = %v", err)
	}
	want := filepath.Join(root, fallbackLogSubdir, logFileName)
	if path != want {
		t.Fatalf("taskLogFilePathLocked(blank) = %q, want %q", path, want)
	}
}

func TestIdentifierSubdirAndSanitizeFallbacks(t *testing.T) {
	t.Parallel()

	if got, ok := failurefollowup.IdentifierSubdir(""); ok || got != "" {
		t.Fatalf("IdentifierSubdir(empty) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := failurefollowup.IdentifierSubdir("###"); !ok || got != fallbackLogSubdir {
		t.Fatalf("IdentifierSubdir(###) = (%q, %v), want (%q, true)", got, ok, fallbackLogSubdir)
	}
	if got := failurefollowup.SanitizeLogPathPart("a__b--c/***d"); got != "a__b--c-d" {
		t.Fatalf("SanitizeLogPathPart() = %q, want %q", got, "a__b--c-d")
	}
}

func TestDefaultLogRootFallsBackToTempWhenWorkingDirectoryIsUnavailable(t *testing.T) {
	t.Parallel()

	failingPrimary := true
	root, err := defaultLogRootForWorkingDir("/workspace/project", func(path string) error {
		if failingPrimary {
			failingPrimary = false
			return fmt.Errorf("permission denied")
		}
		if filepath.Base(path) != logDirectoryName {
			t.Fatalf("fallback base path = %q, want %q", filepath.Base(path), logDirectoryName)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("defaultLogRootForWorkingDir() error = %v", err)
	}
	if filepath.Base(root) != logDirectoryName {
		t.Fatalf("base(defaultLogRootForWorkingDir()) = %q, want %q", filepath.Base(root), logDirectoryName)
	}
	wantPrefix := filepath.Join(os.TempDir(), "moltenhub-code", "logs")
	if got := root; len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("defaultLogRootForWorkingDir() = %q, want prefix %q", got, wantPrefix)
	}
}

func TestLogRootHashIsStable(t *testing.T) {
	t.Parallel()

	const input = "/workspace/project"
	got := logRootHash(input)
	if got == "" {
		t.Fatal("logRootHash() = empty")
	}
	if want := logRootHash(input); got != want {
		t.Fatalf("logRootHash() = %q, want stable %q", got, want)
	}
	if len(got) != 16 {
		t.Fatalf("len(logRootHash()) = %d, want 16", len(got))
	}
	if _, err := fmt.Sscanf(got, "%x", new(uint64)); err != nil {
		t.Fatalf("logRootHash() = %q, want hex: %v", got, err)
	}
}
