package multiplex

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/app"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
)

func TestResultExitCodeReturnsFirstPositiveOrSuccess(t *testing.T) {
	t.Parallel()

	if got := (Result{Sessions: []Session{{ExitCode: app.ExitSuccess}, {ExitCode: 7}, {ExitCode: 9}}}).ExitCode(); got != 7 {
		t.Fatalf("ExitCode() = %d, want 7", got)
	}
	if got := (Result{Sessions: []Session{{ExitCode: app.ExitSuccess}}}).ExitCode(); got != app.ExitSuccess {
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
		RunSession: func(context.Context, config.Config, logFn) app.Result {
			return app.Result{ExitCode: app.ExitCodex, Err: errors.New("boom")}
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

func TestNewDefaultLogfAndRunNilLogf(t *testing.T) {
	t.Parallel()

	New(nil).Logf("ignored %s", "line")

	dir := t.TempDir()
	path := writeMuxConfig(t, dir, "task.json", "prompt")
	m := Multiplexer{
		MaxParallel: 1,
		RunSession: func(context.Context, config.Config, logFn) app.Result {
			return app.Result{ExitCode: app.ExitSuccess}
		},
	}
	res := m.Run(context.Background(), []string{path})
	if len(res.Sessions) != 1 || res.Sessions[0].State != SessionOK {
		t.Fatalf("Run(nil Logf) sessions = %+v, want one ok session", res.Sessions)
	}
}

func TestRunInitializesDefaultSessionRunnerWithoutCallingItOnConfigError(t *testing.T) {
	t.Parallel()

	m := Multiplexer{Logf: func(string, ...any) {}}
	res := m.Run(context.Background(), []string{"/missing/config.json"})
	if len(res.Sessions) != 1 || res.Sessions[0].State != SessionError || res.Sessions[0].ExitCode != app.ExitConfig {
		t.Fatalf("Run(default session runner with bad config) = %+v, want config error", res.Sessions)
	}
}

func TestRunOneIgnoresLogLinesWithoutStageStatus(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeMuxConfig(t, dir, "task.json", "prompt")
	m := Multiplexer{
		Logf: func(string, ...any) {},
		RunSession: func(_ context.Context, _ config.Config, logf logFn) app.Result {
			logf("message without stage or status")
			return app.Result{ExitCode: app.ExitSuccess}
		},
	}
	res := m.Run(context.Background(), []string{path})
	if got := res.Sessions[0].State; got != SessionOK {
		t.Fatalf("session state = %q, want %q", got, SessionOK)
	}
}

func TestRunOnePreservesInitialStageStatusWhenCalledDirectly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeMuxConfig(t, dir, "task.json", "prompt")
	var mu sync.Mutex

	errorSessions := []Session{{ID: "task-error"}}
	errorMux := Multiplexer{
		Logf: func(string, ...any) {},
		RunSession: func(context.Context, config.Config, logFn) app.Result {
			return app.Result{ExitCode: app.ExitCodex, Err: errors.New("boom")}
		},
	}
	errorMux.runOne(context.Background(), &mu, errorSessions, 0, path)
	if got := errorSessions[0]; got.Stage != "config" || got.StageStatus != "start" || got.State != SessionError {
		t.Fatalf("error session = %+v, want config/start preserved", got)
	}

	okSessions := []Session{{ID: "task-ok"}}
	okMux := Multiplexer{
		Logf: func(string, ...any) {},
		RunSession: func(context.Context, config.Config, logFn) app.Result {
			return app.Result{ExitCode: app.ExitSuccess}
		},
	}
	okMux.runOne(context.Background(), &mu, okSessions, 0, path)
	if got := okSessions[0]; got.Stage != "config" || got.StageStatus != "start" || got.State != SessionOK {
		t.Fatalf("ok session = %+v, want config/start preserved", got)
	}
}
