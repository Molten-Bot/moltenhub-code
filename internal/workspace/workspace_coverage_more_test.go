package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMountParsingAndPathHelpers(t *testing.T) {
	t.Parallel()

	if _, _, ok := parseMountInfoLine("bad"); ok {
		t.Fatal("parseMountInfoLine(bad) ok = true, want false")
	}
	if _, _, ok := parseMountInfoLine("24 23 0:21 / /tmp - tmpfs"); ok {
		t.Fatal("parseMountInfoLine(short fields) ok = true, want false")
	}
	mountPoint, options, ok := parseMountInfoLine("24 23 0:21 / /tmp rw,nosuid - tmpfs tmpfs rw,noexec")
	if !ok || mountPoint != "/tmp" || options != "rw,nosuid,rw,noexec" {
		t.Fatalf("parseMountInfoLine() = (%q, %q, %v)", mountPoint, options, ok)
	}

	if _, _, ok := parseProcMountsLine("bad"); ok {
		t.Fatal("parseProcMountsLine(bad) ok = true, want false")
	}
	mountPoint, options, ok = parseProcMountsLine("tmpfs /tmp tmpfs rw,nosuid,nodev,noexec 0 0")
	if !ok || mountPoint != "/tmp" || options != "rw,nosuid,nodev,noexec" {
		t.Fatalf("parseProcMountsLine() = (%q, %q, %v)", mountPoint, options, ok)
	}

	if got := parseMountOptions("rw, noexec ,,nodev"); len(got) != 3 {
		t.Fatalf("parseMountOptions() len = %d, want 3", len(got))
	}
	if !pathWithinMount("/tmp/work", "/tmp") || pathWithinMount("/var/tmp", "/tmp") {
		t.Fatal("pathWithinMount() returned unexpected result")
	}
	if got := unescapeMountField(`with\040space`); got != "with space" {
		t.Fatalf("unescapeMountField() = %q, want %q", got, "with space")
	}
	if got := unescapeMountField(`bad\999escape`); got != `bad\999escape` {
		t.Fatalf("unescapeMountField(invalid octal) = %q", got)
	}
	if pathWithinMount(" ", "/tmp") {
		t.Fatal("pathWithinMount(blank path) = true, want false")
	}
	if _, ok := mountOptionsFromProcMounts(filepath.Join(t.TempDir(), "missing"), "/tmp"); ok {
		t.Fatal("mountOptionsFromProcMounts(missing) ok = true, want false")
	}
}

func TestSeedAgentsFileFallbackAndConfigRootHelpers(t *testing.T) {
	runDir := t.TempDir()
	fallback := filepath.Join(t.TempDir(), "seed.md")
	if err := os.WriteFile(fallback, []byte("seed"), 0o644); err != nil {
		t.Fatalf("WriteFile(fallback) error = %v", err)
	}
	t.Setenv(agentsSeedEnv, fallback)

	readCalls := 0
	m := Manager{
		ReadFile: func(path string) ([]byte, error) {
			readCalls++
			if path == agentsSeedPath {
				return nil, os.ErrNotExist
			}
			return os.ReadFile(path)
		},
		WriteFile: os.WriteFile,
	}
	seedPath, err := m.SeedAgentsFile(runDir)
	if err != nil {
		t.Fatalf("SeedAgentsFile() error = %v", err)
	}
	if readCalls < 2 {
		t.Fatalf("ReadFile calls = %d, want fallback attempt", readCalls)
	}
	if _, err := os.Stat(seedPath); err != nil {
		t.Fatalf("Stat(seedPath) error = %v", err)
	}

	t.Setenv(workspaceRootNameEnv, "/abs/path")
	if got := configuredWorkspaceRootName(); got != defaultWorkspaceRoot {
		t.Fatalf("configuredWorkspaceRootName(abs) = %q, want default", got)
	}
	t.Setenv(workspaceRootNameEnv, "../escape")
	if got := configuredWorkspaceRootName(); got != defaultWorkspaceRoot {
		t.Fatalf("configuredWorkspaceRootName(parent) = %q, want default", got)
	}
}

func TestCreateRunDirDefaultGUIDAndRunDirFailure(t *testing.T) {
	diskBase := t.TempDir()
	t.Setenv(workspaceRAMBaseEnv, filepath.Join(t.TempDir(), "missing-ram"))
	t.Setenv(workspaceDiskBaseEnv, diskBase)
	t.Setenv(workspaceRootNameEnv, "tasks")

	runDir, guid, err := (Manager{}).CreateRunDir()
	if err != nil {
		t.Fatalf("CreateRunDir(default callbacks) error = %v", err)
	}
	if guid == "" || filepath.Base(runDir) != guid {
		t.Fatalf("CreateRunDir() = (%q, %q), want run dir ending with guid", runDir, guid)
	}

	mkdirCalls := 0
	m := Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return "0123456789abcdef0123456789abcdef" },
		MkdirAll: func(path string, _ os.FileMode) error {
			mkdirCalls++
			if mkdirCalls == 2 {
				return os.ErrPermission
			}
			return nil
		},
	}
	if _, _, err := m.CreateRunDir(); err == nil {
		t.Fatal("CreateRunDir(run dir mkdir failure) error = nil, want non-nil")
	}
}

func TestSeedAgentsFileUsesDefaultCallbacks(t *testing.T) {
	runDir := t.TempDir()
	seedDir := t.TempDir()
	seed := filepath.Join(seedDir, "AGENTS.md")
	if err := os.WriteFile(seed, []byte("seed"), 0o644); err != nil {
		t.Fatalf("WriteFile(seed) error = %v", err)
	}
	t.Setenv(agentsSeedEnv, seed)

	got, err := (Manager{}).SeedAgentsFile(runDir)
	if err != nil {
		t.Fatalf("SeedAgentsFile(default callbacks) error = %v", err)
	}
	if data, err := os.ReadFile(got); err != nil || string(data) != "seed" {
		t.Fatalf("ReadFile(seed copy) = (%q, %v), want seed content", string(data), err)
	}
}
