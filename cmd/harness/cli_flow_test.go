package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jef/moltenhub-code/internal/harness"
)

func TestRunSingleReturnsPreflightWhenToolingUnavailable(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	dir := t.TempDir()
	configPath := filepath.Join(dir, "run.json")
	data := `{"repo":"git@github.com:acme/repo.git","prompt":"ship fix"}`
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	t.Setenv("PATH", "")
	os.Args = []string{"harness", "run", "--config", configPath}

	if code := run(); code != harness.ExitPreflight {
		t.Fatalf("run() = %d, want %d", code, harness.ExitPreflight)
	}
}

func TestRunMultiplexReturnsFirstSessionFailureExitCode(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	dir := t.TempDir()
	configPath := filepath.Join(dir, "task.json")
	data := `{"repo":"git@github.com:acme/repo.git","prompt":"ship fix"}`
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	t.Setenv("PATH", "")
	os.Args = []string{"harness", "multiplex", "--config", configPath}

	if code := run(); code != harness.ExitPreflight {
		t.Fatalf("run() = %d, want %d", code, harness.ExitPreflight)
	}
}

func TestRunHubReturnsPreflightWhenBootDiagnosticsFail(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	dir := t.TempDir()
	initPath := filepath.Join(dir, "init.json")
	data := `{"base_url":"http://127.0.0.1:1/v1"}`
	if err := os.WriteFile(initPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile(init) error = %v", err)
	}

	t.Setenv("PATH", "")
	os.Args = []string{"harness", "hub", "--init", initPath, "--ui-listen", ""}

	if code := run(); code != harness.ExitPreflight {
		t.Fatalf("run() = %d, want %d", code, harness.ExitPreflight)
	}
}
