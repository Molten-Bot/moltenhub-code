package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareDefaultRootsRunsWithoutError(t *testing.T) {
	if err := PrepareDefaultRoots(); err != nil {
		t.Fatalf("PrepareDefaultRoots() error = %v", err)
	}
}

func TestPrepareRootsReturnsWrappedErrorWhenAllCandidatesFail(t *testing.T) {
	t.Parallel()

	m := Manager{
		PathExists: func(path string) bool { return path == defaultRAMBase },
		CanExec:    func(string) bool { return true },
		MkdirAll: func(string, os.FileMode) error {
			return errors.New("mkdir failed")
		},
	}

	if err := m.PrepareRoots(); err == nil {
		t.Fatal("PrepareRoots() error = nil, want non-nil")
	}
}

func TestPrepareRootsUsesDefaultMkdirAllWhenCallbackNil(t *testing.T) {
	t.Setenv(workspaceRAMBaseEnv, filepath.Join(t.TempDir(), "missing-ram"))
	diskBase := t.TempDir()
	t.Setenv(workspaceDiskBaseEnv, diskBase)
	t.Setenv(workspaceRootNameEnv, "tasks")

	if err := (Manager{}).PrepareRoots(); err != nil {
		t.Fatalf("PrepareRoots(default mkdir) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(diskBase, "tasks")); err != nil {
		t.Fatalf("Stat(workspace root) error = %v", err)
	}
}

func TestSelectBaseWithDefaultCallbacksHonorsConfiguredBases(t *testing.T) {
	ramBase := filepath.Join(t.TempDir(), "ram")
	diskBase := filepath.Join(t.TempDir(), "disk")
	if err := os.MkdirAll(ramBase, 0o755); err != nil {
		t.Fatalf("MkdirAll(ram) error = %v", err)
	}

	t.Setenv(workspaceRAMBaseEnv, ramBase)
	t.Setenv(workspaceDiskBaseEnv, diskBase)
	m := Manager{}
	if got := m.SelectBase(); got != ramBase {
		t.Fatalf("SelectBase() = %q, want %q", got, ramBase)
	}
}

func TestConfiguredDiskBaseUsesEnvironmentOverride(t *testing.T) {
	t.Setenv(workspaceDiskBaseEnv, "/custom-disk")
	if got := configuredDiskBase(); got != "/custom-disk" {
		t.Fatalf("configuredDiskBase() = %q, want /custom-disk", got)
	}
}

func TestConfiguredWorkspaceRootNameDefaultsForBlank(t *testing.T) {
	t.Setenv(workspaceRootNameEnv, " \t ")
	if got := configuredWorkspaceRootName(); got != defaultWorkspaceRoot {
		t.Fatalf("configuredWorkspaceRootName(blank) = %q, want %q", got, defaultWorkspaceRoot)
	}
}

func TestBaseAllowsExecUsesCachedValue(t *testing.T) {
	t.Parallel()

	const key = "/cached/noexec/path"
	baseAllowsExecCache.Store(key, false)
	t.Cleanup(func() {
		baseAllowsExecCache.Delete(key)
	})

	if got := baseAllowsExec(" " + key + " "); got {
		t.Fatalf("baseAllowsExec(cached false) = true, want false")
	}
}

func TestBaseAllowsExecAllowsBlankPath(t *testing.T) {
	t.Parallel()

	if !baseAllowsExec(" \n\t ") {
		t.Fatal("baseAllowsExec(blank) = false, want true")
	}
}
