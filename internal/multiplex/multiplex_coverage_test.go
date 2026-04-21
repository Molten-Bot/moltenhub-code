package multiplex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/harness"
)

func TestResultExitCodeReturnsFirstPositiveOrSuccess(t *testing.T) {
	t.Parallel()

	if got := (Result{Sessions: []Session{{ExitCode: harness.ExitSuccess}, {ExitCode: 7}, {ExitCode: 9}}}).ExitCode(); got != 7 {
		t.Fatalf("ExitCode() = %d, want 7", got)
	}
	if got := (Result{Sessions: []Session{{ExitCode: harness.ExitSuccess}}}).ExitCode(); got != harness.ExitSuccess {
		t.Fatalf("ExitCode() = %d, want success", got)
	}
}

func TestRunDefaultsAndErrorStateFallbacks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeMuxConfig(t, dir, "task.json", "prompt")

	var logs []string
	m := Multiplexer{
		MaxParallel: 0,
		Logf: func(format string, args ...any) {
			logs = append(logs, format)
		},
		RunSession: func(context.Context, config.Config, logFn) harness.Result {
			return harness.Result{ExitCode: harness.ExitCodex, Err: errors.New("boom")}
		},
	}

	res := m.Run(context.Background(), []string{path})
	if len(res.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(res.Sessions))
	}
	session := res.Sessions[0]
	if session.State != SessionError || session.Stage != "config" || session.StageStatus != "start" {
		t.Fatalf("session = %+v, want error with config/start defaults preserved", session)
	}
	if session.Error == "" {
		t.Fatal("session.Error = empty, want failure message")
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "session=%s state=running config=%s") {
		t.Fatalf("Logf capture = %#v, want session running line", logs)
	}
}
