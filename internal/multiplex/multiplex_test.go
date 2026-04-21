package multiplex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/harness"
)

func TestRunTracksStatusesFromHarnessLogs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	okPath := writeConfig(t, dir, "ok.json", "build feature")
	noChangesPath := writeConfig(t, dir, "no-changes.json", "nochange")
	failPath := writeConfig(t, dir, "fail.json", "fail run")

	m := New(nil)
	m.MaxParallel = 3
	m.Logf = func(string, ...any) {}
	m.RunSession = func(_ context.Context, cfg config.Config, logf logFn) harness.Result {
		switch cfg.Prompt {
		case "build feature":
			logf("stage=preflight status=start")
			logf("stage=preflight status=ok")
			logf("stage=pr status=ok pr_url=https://github.com/acme/repo/pull/12")
			return harness.Result{
				ExitCode:     harness.ExitSuccess,
				WorkspaceDir: "/tmp/run-ok",
				Branch:       "moltenhub-build-feature",
				PRURL:        "https://github.com/acme/repo/pull/12",
			}
		case "nochange":
			logf("stage=preflight status=start")
			logf("stage=preflight status=ok")
			logf("stage=git status=no_changes")
			return harness.Result{
				ExitCode:     harness.ExitSuccess,
				WorkspaceDir: "/tmp/run-nochanges",
				Branch:       "moltenhub-nochange",
				NoChanges:    true,
			}
		default:
			logf("stage=codex status=start")
			logf("stage=codex status=error err=%q", "codex exploded")
			return harness.Result{
				ExitCode:     harness.ExitCodex,
				Err:          errors.New("codex: codex exploded"),
				WorkspaceDir: "/tmp/run-fail",
			}
		}
	}

	res := m.Run(context.Background(), []string{okPath, noChangesPath, failPath})
	if len(res.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(res.Sessions))
	}

	byPath := map[string]Session{}
	for _, s := range res.Sessions {
		byPath[s.ConfigPath] = s
	}

	ok := byPath[okPath]
	if ok.State != SessionOK {
		t.Fatalf("ok state = %q, want %q", ok.State, SessionOK)
	}
	if ok.Stage != "pr" || ok.StageStatus != "ok" {
		t.Fatalf("ok stage/status = %q/%q, want pr/ok", ok.Stage, ok.StageStatus)
	}
	if ok.PRURL == "" {
		t.Fatal("ok PRURL is empty")
	}

	noChanges := byPath[noChangesPath]
	if noChanges.State != SessionNoChanges {
		t.Fatalf("nochanges state = %q, want %q", noChanges.State, SessionNoChanges)
	}
	if noChanges.Stage != "git" || noChanges.StageStatus != "no_changes" {
		t.Fatalf("nochanges stage/status = %q/%q, want git/no_changes", noChanges.Stage, noChanges.StageStatus)
	}
	if !noChanges.NoChanges {
		t.Fatal("nochanges NoChanges = false, want true")
	}

	fail := byPath[failPath]
	if fail.State != SessionError {
		t.Fatalf("fail state = %q, want %q", fail.State, SessionError)
	}
	if fail.ExitCode != harness.ExitCodex {
		t.Fatalf("fail exit code = %d, want %d", fail.ExitCode, harness.ExitCodex)
	}
	if fail.Stage != "codex" || fail.StageStatus != "error" {
		t.Fatalf("fail stage/status = %q/%q, want codex/error", fail.Stage, fail.StageStatus)
	}
	if fail.Error == "" {
		t.Fatal("fail error is empty")
	}

	if code := res.ExitCode(); code != harness.ExitCodex {
		t.Fatalf("ExitCode() = %d, want %d", code, harness.ExitCodex)
	}
}

func TestRunRespectsMaxParallel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	paths := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		paths = append(paths, writeConfig(t, dir, fmt.Sprintf("task-%d.json", i), fmt.Sprintf("prompt-%d", i)))
	}

	var current int32
	var maxSeen int32

	m := New(nil)
	m.MaxParallel = 2
	m.Logf = func(string, ...any) {}
	m.RunSession = func(_ context.Context, _ config.Config, _ logFn) harness.Result {
		n := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if n <= old || atomic.CompareAndSwapInt32(&maxSeen, old, n) {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return harness.Result{ExitCode: harness.ExitSuccess}
	}

	res := m.Run(context.Background(), paths)
	if len(res.Sessions) != len(paths) {
		t.Fatalf("sessions = %d, want %d", len(res.Sessions), len(paths))
	}
	if got := atomic.LoadInt32(&maxSeen); got > 2 {
		t.Fatalf("max in-flight sessions = %d, want <= 2", got)
	}
	if got := atomic.LoadInt32(&maxSeen); got < 2 {
		t.Fatalf("max in-flight sessions = %d, want 2", got)
	}
}

func TestRunConfigLoadFailureDoesNotCallSessionRunner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := writeConfig(t, dir, "valid.json", "ok")
	missing := filepath.Join(dir, "missing.json")

	var called int32

	m := New(nil)
	m.MaxParallel = 1
	m.Logf = func(string, ...any) {}
	m.RunSession = func(_ context.Context, _ config.Config, _ logFn) harness.Result {
		atomic.AddInt32(&called, 1)
		return harness.Result{ExitCode: harness.ExitSuccess}
	}

	res := m.Run(context.Background(), []string{missing, valid})
	if got := atomic.LoadInt32(&called); got != 1 {
		t.Fatalf("RunSession calls = %d, want 1", got)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(res.Sessions))
	}
	first := res.Sessions[0]
	if first.ConfigPath != missing {
		t.Fatalf("first config path = %q, want %q", first.ConfigPath, missing)
	}
	if first.State != SessionError {
		t.Fatalf("first state = %q, want %q", first.State, SessionError)
	}
	if first.ExitCode != harness.ExitConfig {
		t.Fatalf("first exit code = %d, want %d", first.ExitCode, harness.ExitConfig)
	}
	if first.Stage != "config" || first.StageStatus != "error" {
		t.Fatalf("first stage/status = %q/%q, want config/error", first.Stage, first.StageStatus)
	}
	if first.Error == "" {
		t.Fatal("first error is empty")
	}
	if code := res.ExitCode(); code != harness.ExitConfig {
		t.Fatalf("ExitCode() = %d, want %d", code, harness.ExitConfig)
	}
}

func writeConfig(t *testing.T, dir, name, prompt string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data := fmt.Sprintf(`{
  "repo": "git@github.com:acme/repo.git",
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": %q
}
`, prompt)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
	return path
}
