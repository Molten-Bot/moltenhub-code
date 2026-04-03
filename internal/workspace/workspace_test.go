package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectBasePrefersDevShm(t *testing.T) {
	t.Parallel()

	m := Manager{PathExists: func(path string) bool { return path == "/dev/shm" }}
	if got := m.SelectBase(); got != "/dev/shm" {
		t.Fatalf("SelectBase() = %q", got)
	}
}

func TestSelectBaseFallsBackToTmp(t *testing.T) {
	t.Parallel()

	m := Manager{PathExists: func(string) bool { return false }}
	if got := m.SelectBase(); got != "/tmp" {
		t.Fatalf("SelectBase() = %q", got)
	}
}

func TestCreateRunDirUsesGUIDSubfolder(t *testing.T) {
	t.Parallel()

	var createdPath string
	m := Manager{
		PathExists: func(path string) bool { return path == "/dev/shm" },
		NewGUID:    func() string { return "abc123" },
		MkdirAll: func(path string, _ os.FileMode) error {
			createdPath = path
			return nil
		},
	}

	runDir, guid, err := m.CreateRunDir()
	if err != nil {
		t.Fatalf("CreateRunDir() error = %v", err)
	}
	if guid != "abc123" {
		t.Fatalf("guid = %q", guid)
	}
	want := filepath.Join("/dev/shm", "temp", "abc123")
	if runDir != want {
		t.Fatalf("runDir = %q, want %q", runDir, want)
	}
	if createdPath != want {
		t.Fatalf("created path = %q, want %q", createdPath, want)
	}
}

func TestCreateRunDirFallsBackToTmpTempRoot(t *testing.T) {
	t.Parallel()

	var createdPath string
	m := Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return "abc123" },
		MkdirAll: func(path string, _ os.FileMode) error {
			createdPath = path
			return nil
		},
	}

	runDir, guid, err := m.CreateRunDir()
	if err != nil {
		t.Fatalf("CreateRunDir() error = %v", err)
	}
	if guid != "abc123" {
		t.Fatalf("guid = %q", guid)
	}
	want := filepath.Join("/tmp", "temp", "abc123")
	if runDir != want {
		t.Fatalf("runDir = %q, want %q", runDir, want)
	}
	if createdPath != want {
		t.Fatalf("created path = %q, want %q", createdPath, want)
	}
}

func TestCreateRunDirMkdirFailure(t *testing.T) {
	t.Parallel()

	m := Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return "abc123" },
		MkdirAll: func(string, os.FileMode) error {
			return errors.New("boom")
		},
	}

	_, _, err := m.CreateRunDir()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateRunDirFallsBackWhenDevShmCreateFails(t *testing.T) {
	t.Parallel()

	var created []string
	m := Manager{
		PathExists: func(path string) bool { return path == "/dev/shm" },
		NewGUID:    func() string { return "abc123" },
		MkdirAll: func(path string, _ os.FileMode) error {
			created = append(created, path)
			if path == filepath.Join("/dev/shm", "temp", "abc123") {
				return errors.New("permission denied")
			}
			return nil
		},
	}

	runDir, guid, err := m.CreateRunDir()
	if err != nil {
		t.Fatalf("CreateRunDir() error = %v", err)
	}
	if guid != "abc123" {
		t.Fatalf("guid = %q", guid)
	}
	want := filepath.Join("/tmp", "temp", "abc123")
	if runDir != want {
		t.Fatalf("runDir = %q, want %q", runDir, want)
	}
	if len(created) != 2 {
		t.Fatalf("mkdir attempts = %d, want 2 (%v)", len(created), created)
	}
}
