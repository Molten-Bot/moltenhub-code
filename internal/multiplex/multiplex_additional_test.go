package multiplex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/harness"
)

func TestResultExitCodeHandlesNegativeExitCodes(t *testing.T) {
	t.Parallel()

	result := Result{
		Sessions: []Session{
			{ExitCode: harness.ExitSuccess},
			{ExitCode: -1},
		},
	}
	if got := result.ExitCode(); got != 1 {
		t.Fatalf("ExitCode() = %d, want 1", got)
	}
}

func TestRunReturnsEmptyResultWhenNoConfigsProvided(t *testing.T) {
	t.Parallel()

	if got := (New(nil)).Run(context.Background(), nil); len(got.Sessions) != 0 {
		t.Fatalf("len(Run(nil).Sessions) = %d, want 0", len(got.Sessions))
	}
}

func TestRunMarksSessionAsContextErrorWhenBlockedBySemaphore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pathA := writeMuxConfig(t, dir, "a.json", "a")
	pathB := writeMuxConfig(t, dir, "b.json", "b")

	m := New(nil)
	m.MaxParallel = 1
	m.Logf = func(string, ...any) {}
	m.RunSession = func(ctx context.Context, _ config.Config, _ logFn) harness.Result {
		<-ctx.Done()
		return harness.Result{ExitCode: harness.ExitSuccess}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		done <- m.Run(ctx, []string{pathA, pathB})
	}()

	time.Sleep(25 * time.Millisecond)
	cancel()

	res := <-done
	if len(res.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2", len(res.Sessions))
	}

	var sawContextError bool
	for _, session := range res.Sessions {
		if session.State == SessionError && session.Stage == "context" && session.StageStatus == "error" {
			sawContextError = true
		}
	}
	if !sawContextError {
		t.Fatalf("did not observe context-blocked session state in %+v", res.Sessions)
	}
}

func TestParseStageStatusAndSessionIDHelpers(t *testing.T) {
	t.Parallel()

	stage, status, ok := parseStageStatus("stage=codex status=running")
	if !ok || stage != "codex" || status != "running" {
		t.Fatalf("parseStageStatus() = (%q, %q, %v), want (codex, running, true)", stage, status, ok)
	}

	stage, status, ok = parseStageStatus("message=only")
	if ok || stage != "" || status != "" {
		t.Fatalf("parseStageStatus(no fields) = (%q, %q, %v), want empty,false", stage, status, ok)
	}

	if got, want := sessionID(4), "task-005"; got != want {
		t.Fatalf("sessionID(4) = %q, want %q", got, want)
	}
}

func writeMuxConfig(t *testing.T, dir, name, prompt string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data := fmt.Sprintf(`{"repo":"git@github.com:acme/repo.git","prompt":%q}`, prompt)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}
