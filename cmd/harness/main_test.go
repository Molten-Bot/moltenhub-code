package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jef/how-i-want-to-code/internal/harness"
)

func TestRunUsageMissingSubcommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunUsageMissingConfigFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "run"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunConfigLoadFailure(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	missing := filepath.Join(t.TempDir(), "missing.json")
	os.Args = []string{"harness", "run", "--config", missing}

	if code := run(); code != 10 {
		t.Fatalf("run() = %d, want 10", code)
	}
}

func TestRunUsageUnknownSubcommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "unknown"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunMultiplexUsageMissingConfigFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "multiplex"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestRunHubUsageMissingInitFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"harness", "hub"}

	if code := run(); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func TestMonitorURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: ":7777", want: "http://127.0.0.1:7777"},
		{in: "127.0.0.1:7777", want: "http://127.0.0.1:7777"},
		{in: "http://localhost:8080", want: "http://localhost:8080"},
	}

	for _, tt := range tests {
		if got := monitorURL(tt.in); got != tt.want {
			t.Fatalf("monitorURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCollectConfigPathsFilesAndDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.json")
	fileB := filepath.Join(dir, "b.JSON")
	fileTxt := filepath.Join(dir, "notes.txt")
	nestedDir := filepath.Join(dir, "nested")
	fileC := filepath.Join(nestedDir, "c.json")

	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	for _, path := range []string{fileA, fileB, fileTxt, fileC} {
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	got, err := collectConfigPaths([]string{fileA, dir, fileA})
	if err != nil {
		t.Fatalf("collectConfigPaths() error = %v", err)
	}

	want := []string{fileA, fileB, fileC}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectConfigPaths() = %v, want %v", got, want)
	}
}

func TestLocalTaskLogDir(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), ".log")
	got, ok := localTaskLogDir(root, "local-1712345678-000321")
	if !ok {
		t.Fatal("localTaskLogDir() ok = false, want true")
	}
	want := filepath.Join(root, "local", "1712345678", "000321")
	if got != want {
		t.Fatalf("localTaskLogDir() = %q, want %q", got, want)
	}

	if _, ok := localTaskLogDir(root, "req-abc"); ok {
		t.Fatal("localTaskLogDir(non-local request) ok = true, want false")
	}
}

func TestFailureFollowUpRunConfigIncludesNonLocalRequestLogPaths(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	failedResult := harness.Result{Err: fmt.Errorf("checks failed")}

	cfg := failureFollowUpRunConfig("req-123-abc", failedResult, logRoot)
	expectedLogDir := filepath.Join(logRoot, "req", "123", "abc")
	if !strings.Contains(cfg.Prompt, expectedLogDir) {
		t.Fatalf("Prompt missing non-local log dir path %q: %q", expectedLogDir, cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, filepath.Join(expectedLogDir, logFileName)) {
		t.Fatalf("Prompt missing non-local log file path: %q", cfg.Prompt)
	}
}

func TestFailureFollowUpRunConfigUsesRequiredPayloadShapeAndLogContext(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	failedResult := harness.Result{
		Err:          fmt.Errorf("clone: repository not found"),
		WorkspaceDir: "/tmp/run-123",
	}

	cfg := failureFollowUpRunConfig("local-1712345678-000001", failedResult, logRoot)
	if got, want := cfg.Repos, []string{failureFollowUpRepoURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos = %v, want %v", got, want)
	}
	if cfg.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want %q", cfg.BaseBranch, "main")
	}
	if cfg.TargetSubdir != "." {
		t.Fatalf("TargetSubdir = %q, want %q", cfg.TargetSubdir, ".")
	}

	expectedLogDir := filepath.Join(logRoot, "local", "1712345678", "000001")
	if !strings.Contains(cfg.Prompt, expectedLogDir) {
		t.Fatalf("Prompt missing log dir path %q: %q", expectedLogDir, cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, filepath.Join(expectedLogDir, logFileName)) {
		t.Fatalf("Prompt missing log file path: %q", cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, "clone: repository not found") {
		t.Fatalf("Prompt missing failure summary: %q", cfg.Prompt)
	}
}
