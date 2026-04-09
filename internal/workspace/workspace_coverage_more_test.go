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
