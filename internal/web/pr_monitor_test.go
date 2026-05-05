package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
)

type stubPRMonitorRunner struct {
	mu       sync.Mutex
	result   execx.Result
	err      error
	commands []execx.Command
}

func (s *stubPRMonitorRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, cmd)
	return s.result, s.err
}

func (s *stubPRMonitorRunner) Commands() []execx.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

func TestPRMergeMonitorRemovesMergedTaskFromQueueAndRunsCleanup(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	broker.RecordTaskRunConfig("req-merged", []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"ship it"}`))
	broker.IngestLog("dispatch status=start request_id=req-merged repo=git@github.com:acme/repo.git")
	broker.IngestLog("dispatch status=completed request_id=req-merged workspace=/tmp/run branch=moltenhub-ship pr_url=https://github.com/acme/repo/pull/42")

	runner := &stubPRMonitorRunner{
		result: execx.Result{Stdout: `{"state":"MERGED","mergedAt":"2026-04-09T12:00:00Z"}`},
	}

	cleanupCalls := make(chan string, 1)
	monitor := PRMergeMonitor{
		Runner:       runner,
		Broker:       broker,
		PollInterval: 10 * time.Millisecond,
		CleanupTask: func(_ context.Context, requestID string) error {
			cleanupCalls <- requestID
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- monitor.Run(ctx)
	}()

	waitForHubUITest(t, 300*time.Millisecond, func() bool {
		return len(runner.Commands()) > 0
	})

	select {
	case requestID := <-cleanupCalls:
		if got, want := requestID, "req-merged"; got != want {
			t.Fatalf("cleanup requestID = %q, want %q", got, want)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected automatic cleanup after merged PR observation")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("monitor.Run() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for monitor shutdown")
	}

	if _, ok := broker.TaskRunConfig("req-merged"); ok {
		t.Fatal("TaskRunConfig() found = true after merged PR observation, want false")
	}

	snap := broker.Snapshot()
	if got, want := len(snap.Tasks), 0; got != want {
		t.Fatalf("len(tasks) after merged PR observation = %d, want %d", got, want)
	}

	commands := runner.Commands()
	if len(commands) == 0 {
		t.Fatal("gh command was not executed")
	}
	if got, want := commands[0].Name, "gh"; got != want {
		t.Fatalf("command name = %q, want %q", got, want)
	}
	if got, want := commands[0].Args, []string{"pr", "view", "42", "--json", "state,mergedAt"}; !slices.Equal(got, want) {
		t.Fatalf("command args = %v, want %v", got, want)
	}
}

func TestPRMergeMonitorKeepsTaskVisibleUntilPRIsMerged(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	broker.IngestLog("dispatch status=start request_id=req-open repo=git@github.com:acme/repo.git")
	broker.IngestLog("dispatch status=completed request_id=req-open workspace=/tmp/run branch=moltenhub-ship pr_url=https://github.com/acme/repo/pull/77")

	runner := &stubPRMonitorRunner{
		result: execx.Result{Stdout: `{"state":"OPEN","mergedAt":""}`},
	}

	monitor := PRMergeMonitor{
		Runner:       runner,
		Broker:       broker,
		PollInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- monitor.Run(ctx)
	}()

	waitForHubUITest(t, 80*time.Millisecond, func() bool {
		return len(runner.Commands()) > 0
	})

	snap := broker.Snapshot()
	if got, want := len(snap.Tasks), 1; got != want {
		t.Fatalf("len(tasks) = %d, want %d", got, want)
	}
	if got, want := snap.Tasks[0].RequestID, "req-open"; got != want {
		t.Fatalf("task request_id = %q, want %q", got, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("monitor.Run() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for monitor shutdown")
	}
}

func TestPRMergeMonitorLogsCheckFailuresAndKeepsTask(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	broker.IngestLog("dispatch status=start request_id=req-fail repo=git@github.com:acme/repo.git")
	broker.IngestLog("dispatch status=completed request_id=req-fail workspace=/tmp/run branch=moltenhub-ship pr_url=https://github.com/acme/repo/pull/99")

	runner := &stubPRMonitorRunner{err: errors.New("gh failed")}
	logs := make(chan string, 8)
	monitor := PRMergeMonitor{
		Runner:       runner,
		Broker:       broker,
		PollInterval: 10 * time.Millisecond,
		Logf: func(format string, args ...any) {
			logs <- fmt.Sprintf(format, args...)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- monitor.Run(ctx)
	}()

	waitForHubUITest(t, 80*time.Millisecond, func() bool {
		return len(runner.Commands()) > 0
	})

	snap := broker.Snapshot()
	if got, want := len(snap.Tasks), 1; got != want {
		t.Fatalf("len(tasks) = %d, want %d", got, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("monitor.Run() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for monitor shutdown")
	}

	select {
	case line := <-logs:
		if line == "" {
			t.Fatal("expected non-empty log line")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected warning log for failed PR status check")
	}
}

func waitForHubUITest(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
