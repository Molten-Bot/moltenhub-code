package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathExistsChecksDirectoryOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if !pathExists(dir) {
		t.Fatalf("pathExists(%q) = false, want true", dir)
	}

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if pathExists(filePath) {
		t.Fatalf("pathExists(%q) = true, want false", filePath)
	}
	if pathExists(filepath.Join(dir, "missing")) {
		t.Fatal("pathExists(missing) = true, want false")
	}
}

func TestNewManagerInitializesDefaultFunctionPointers(t *testing.T) {
	t.Parallel()

	m := NewManager()
	if m.PathExists == nil || m.MkdirAll == nil || m.NewGUID == nil || m.ReadFile == nil || m.WriteFile == nil {
		t.Fatalf("NewManager() has nil function pointer(s): %+v", m)
	}
}

func TestNewGUIDReturnsHexIdentifier(t *testing.T) {
	t.Parallel()

	got := newGUID()
	if len(got) != 32 {
		t.Fatalf("len(newGUID()) = %d, want 32", len(got))
	}
	for _, r := range got {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("newGUID() contains non-hex rune %q in %q", r, got)
		}
	}
}

func TestResolveAgentsSeedPathReturnsEmptyWhenNoCandidatesExist(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tmp, err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	t.Setenv(agentsSeedEnv, filepath.Join(tmp, "missing-seed.md"))
	if got := resolveAgentsSeedPath(); got != "" {
		t.Fatalf("resolveAgentsSeedPath() = %q, want empty", got)
	}
}

func TestFindPathUpwardRejectsEmptyInputs(t *testing.T) {
	t.Parallel()

	if _, ok := findPathUpward("", agentsSeedPath); ok {
		t.Fatal("findPathUpward(empty startDir) ok = true, want false")
	}
	if _, ok := findPathUpward(t.TempDir(), ""); ok {
		t.Fatal("findPathUpward(empty relPath) ok = true, want false")
	}
}
