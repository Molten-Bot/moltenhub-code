package main

import (
	"os"
	"path/filepath"
	"testing"
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

	path, err := mirror.taskLogFilePathLocked("   ")
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

	if got, ok := identifierSubdir(""); ok || got != "" {
		t.Fatalf("identifierSubdir(empty) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := identifierSubdir("###"); !ok || got != fallbackLogSubdir {
		t.Fatalf("identifierSubdir(###) = (%q, %v), want (%q, true)", got, ok, fallbackLogSubdir)
	}
	if got := sanitizeLogPathPart("a__b--c/***d"); got != "a_b_c_d" {
		t.Fatalf("sanitizeLogPathPart() = %q, want %q", got, "a_b_c_d")
	}
}
