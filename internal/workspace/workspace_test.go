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

func TestSeedAgentsFileCopiesSeedIntoRunDir(t *testing.T) {
	t.Parallel()

	var (
		readPath   string
		writePath  string
		writeBytes []byte
		writeMode  os.FileMode
	)

	m := Manager{
		ReadFile: func(path string) ([]byte, error) {
			readPath = path
			return []byte("seeded instructions"), nil
		},
		WriteFile: func(path string, data []byte, mode os.FileMode) error {
			writePath = path
			writeBytes = append([]byte(nil), data...)
			writeMode = mode
			return nil
		},
	}

	runDir := filepath.Join("/tmp", "temp", "abc123")
	seedPath, err := m.SeedAgentsFile(runDir)
	if err != nil {
		t.Fatalf("SeedAgentsFile() error = %v", err)
	}

	if readPath != agentsSeedPath {
		t.Fatalf("read path = %q, want %q", readPath, agentsSeedPath)
	}
	wantSeedPath := filepath.Join(runDir, agentsFileName)
	if seedPath != wantSeedPath {
		t.Fatalf("seed path = %q, want %q", seedPath, wantSeedPath)
	}
	if writePath != wantSeedPath {
		t.Fatalf("write path = %q, want %q", writePath, wantSeedPath)
	}
	if string(writeBytes) != "seeded instructions" {
		t.Fatalf("write bytes = %q", string(writeBytes))
	}
	if writeMode != 0o644 {
		t.Fatalf("write mode = %o, want 644", writeMode)
	}
}

func TestSeedAgentsFileReadError(t *testing.T) {
	t.Parallel()

	m := Manager{
		ReadFile: func(string) ([]byte, error) {
			return nil, errors.New("missing seed")
		},
	}

	if _, err := m.SeedAgentsFile(filepath.Join("/tmp", "temp", "abc123")); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSeedAgentsFileWriteError(t *testing.T) {
	t.Parallel()

	m := Manager{
		ReadFile: func(string) ([]byte, error) {
			return []byte("seed"), nil
		},
		WriteFile: func(string, []byte, os.FileMode) error {
			return errors.New("write failed")
		},
	}

	if _, err := m.SeedAgentsFile(filepath.Join("/tmp", "temp", "abc123")); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFindPathUpwardFindsSeedFromNestedDirectory(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "repo")
	startDir := filepath.Join(root, "internal", "harness")
	seedPath := filepath.Join(root, "library", "AGENTS.md")

	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("mkdir start dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(seedPath), 0o755); err != nil {
		t.Fatalf("mkdir seed dir: %v", err)
	}
	if err := os.WriteFile(seedPath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	got, ok := findPathUpward(startDir, agentsSeedPath)
	if !ok {
		t.Fatal("findPathUpward() found = false, want true")
	}
	if got != seedPath {
		t.Fatalf("findPathUpward() = %q, want %q", got, seedPath)
	}
}

func TestResolveAgentsSeedPathUsesEnvOverride(t *testing.T) {
	seedPath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(seedPath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	t.Setenv(agentsSeedEnv, seedPath)
	if got := resolveAgentsSeedPath(); got != seedPath {
		t.Fatalf("resolveAgentsSeedPath() = %q, want %q", got, seedPath)
	}
}
