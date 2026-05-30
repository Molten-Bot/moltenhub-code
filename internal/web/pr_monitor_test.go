package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	if got, want := len(snap.Releases), 1; got != want {
		t.Fatalf("len(releases) after merged PR observation = %d, want %d", got, want)
	}
	if got, want := snap.Releases[0].Prompt, "ship it"; got != want {
		t.Fatalf("release.Prompt = %q, want %q", got, want)
	}
	if got, want := snap.Releases[0].PRURL, "https://github.com/acme/repo/pull/42"; got != want {
		t.Fatalf("release.PRURL = %q, want %q", got, want)
	}
	if got, want := snap.Releases[0].MergedAt, "2026-04-09T12:00:00Z"; got != want {
		t.Fatalf("release.MergedAt = %q, want %q", got, want)
	}

	commands := runner.Commands()
	if len(commands) == 0 {
		t.Fatal("gh command was not executed")
	}
	if got, want := commands[0].Name, "gh"; got != want {
		t.Fatalf("command name = %q, want %q", got, want)
	}
	if got, want := commands[0].Args, []string{"pr", "view", "42", "--json", "state,mergedAt,url,number,title,headRefName,baseRefName,reviewDecision,latestReviews,comments", "--repo", "acme/repo"}; !slices.Equal(got, want) {
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

func TestPRMergeMonitorQueuesActionableReviewFeedbackOncePerPR(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	broker.RecordTaskRunConfig("req-feedback", []byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"main","prompt":"ship it"}`))
	broker.IngestLog("dispatch status=start request_id=req-feedback repo=git@github.com:acme/repo.git")
	broker.IngestLog("dispatch status=completed request_id=req-feedback workspace=/tmp/run branch=moltenhub-ship pr_url=https://github.com/acme/repo/pull/117")

	reviewBody := "**Positive**\n- Good scope.\n\n**Negative**\n- [Medium] src/main.jsx:1139 - Custom theme radiogroup lacks arrow-key navigation."
	runner := &stubPRMonitorRunner{
		result: execx.Result{Stdout: fmt.Sprintf(`{
			"state":"OPEN",
			"mergedAt":"",
			"url":"https://github.com/acme/repo/pull/117",
			"number":117,
			"title":"Ship it",
			"headRefName":"moltenhub-ship",
			"baseRefName":"main",
			"reviewDecision":"REVIEW_REQUIRED",
			"latestReviews":[{"id":"review-1","author":{"login":"reviewer"},"state":"COMMENTED","submittedAt":"2026-05-30T12:00:00Z","body":%q}],
			"comments":[]
		}`, reviewBody)},
	}

	calls := 0
	var gotTask Task
	var gotFeedback PRReviewFeedback
	monitor := PRMergeMonitor{
		Runner: runner,
		Broker: broker,
		Logf:   func(string, ...any) {},
		OnReviewFeedback: func(_ context.Context, task Task, feedback PRReviewFeedback) (string, error) {
			calls++
			gotTask = task
			gotFeedback = feedback
			return "local-follow-up", nil
		},
	}

	task := broker.Snapshot().Tasks[0]
	monitor.checkTaskPR(context.Background(), task)
	monitor.checkTaskPR(context.Background(), task)

	if got, want := calls, 1; got != want {
		t.Fatalf("OnReviewFeedback calls = %d, want %d", got, want)
	}
	if got, want := gotTask.RequestID, "req-feedback"; got != want {
		t.Fatalf("feedback task request_id = %q, want %q", got, want)
	}
	if got, want := gotFeedback.PRURL, "https://github.com/acme/repo/pull/117"; got != want {
		t.Fatalf("feedback.PRURL = %q, want %q", got, want)
	}
	if got, want := gotFeedback.HeadBranch, "moltenhub-ship"; got != want {
		t.Fatalf("feedback.HeadBranch = %q, want %q", got, want)
	}
	if got, want := gotFeedback.BaseBranch, "main"; got != want {
		t.Fatalf("feedback.BaseBranch = %q, want %q", got, want)
	}
	if got := len(gotFeedback.Items); got != 1 {
		t.Fatalf("len(feedback.Items) = %d, want 1", got)
	}
	if !strings.Contains(gotFeedback.Items[0].Body, "Custom theme radiogroup lacks arrow-key navigation") {
		t.Fatalf("feedback item body = %q", gotFeedback.Items[0].Body)
	}
	if gotFeedback.Digest == "" {
		t.Fatal("feedback.Digest is empty")
	}
}

func TestPRMergeMonitorIgnoresCleanReviewFeedback(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	broker.IngestLog("dispatch status=start request_id=req-clean repo=git@github.com:acme/repo.git")
	broker.IngestLog("dispatch status=completed request_id=req-clean workspace=/tmp/run branch=moltenhub-clean pr_url=https://github.com/acme/repo/pull/118")

	reviewBody := "**Positive**\n- Looks safe.\n\n**Negative**\n- No material issues found."
	runner := &stubPRMonitorRunner{
		result: execx.Result{Stdout: fmt.Sprintf(`{
			"state":"OPEN",
			"mergedAt":"",
			"url":"https://github.com/acme/repo/pull/118",
			"headRefName":"moltenhub-clean",
			"baseRefName":"main",
			"latestReviews":[{"id":"review-1","author":{"login":"reviewer"},"state":"COMMENTED","submittedAt":"2026-05-30T12:00:00Z","body":%q}],
			"comments":[]
		}`, reviewBody)},
	}

	calls := 0
	monitor := PRMergeMonitor{
		Runner: runner,
		Broker: broker,
		Logf:   func(string, ...any) {},
		OnReviewFeedback: func(_ context.Context, _ Task, _ PRReviewFeedback) (string, error) {
			calls++
			return "local-follow-up", nil
		},
	}

	monitor.checkTaskPR(context.Background(), broker.Snapshot().Tasks[0])

	if calls != 0 {
		t.Fatalf("OnReviewFeedback calls = %d, want 0", calls)
	}
}

func TestActionablePRReviewFeedbackIncludesInlineReviewComments(t *testing.T) {
	t.Parallel()

	task := Task{PRURL: "https://github.com/acme/repo/pull/120", Branch: "feature"}
	feedback := actionablePRReviewFeedback(task, prViewState{
		State:       "OPEN",
		URL:         "https://github.com/acme/repo/pull/120",
		HeadRefName: "feature",
		BaseRefName: "main",
		ReviewComments: []prReviewCommentEntry{{
			ID:        123,
			User:      prActor{Login: "reviewer"},
			Path:      "src/main.jsx",
			Line:      1139,
			Body:      "Please add arrow-key navigation for the custom theme radiogroup.",
			UpdatedAt: "2026-05-30T12:00:00Z",
		}},
	})

	if got := len(feedback.Items); got != 1 {
		t.Fatalf("len(feedback.Items) = %d, want 1", got)
	}
	item := feedback.Items[0]
	if got, want := item.Kind, "review_comment"; got != want {
		t.Fatalf("item.Kind = %q, want %q", got, want)
	}
	if !strings.Contains(item.Body, "src/main.jsx:1139") || !strings.Contains(item.Body, "arrow-key navigation") {
		t.Fatalf("item.Body = %q, want location and comment text", item.Body)
	}
	if feedback.Digest == "" {
		t.Fatal("feedback.Digest is empty")
	}
}

func TestPRMergeMonitorDoesNotQueueFeedbackForReviewTasks(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	broker.RecordTaskRunConfigWithSource("req-review", []byte(`{"repo":"git@github.com:acme/repo.git","libraryTaskName":"code-review","review":{"prNumber":117}}`), "review")
	broker.IngestLog("dispatch status=start request_id=req-review repo=git@github.com:acme/repo.git")
	broker.IngestLog("dispatch status=no_changes request_id=req-review workspace=/tmp/run branch=feature pr_url=https://github.com/acme/repo/pull/117")

	reviewBody := "**Negative**\n- [Medium] src/main.jsx:1139 - Custom theme radiogroup lacks arrow-key navigation."
	runner := &stubPRMonitorRunner{
		result: execx.Result{Stdout: fmt.Sprintf(`{
			"state":"OPEN",
			"mergedAt":"",
			"url":"https://github.com/acme/repo/pull/117",
			"headRefName":"feature",
			"baseRefName":"main",
			"latestReviews":[{"id":"review-1","author":{"login":"reviewer"},"state":"COMMENTED","submittedAt":"2026-05-30T12:00:00Z","body":%q}],
			"comments":[]
		}`, reviewBody)},
	}

	calls := 0
	monitor := PRMergeMonitor{
		Runner: runner,
		Broker: broker,
		Logf:   func(string, ...any) {},
		OnReviewFeedback: func(_ context.Context, _ Task, _ PRReviewFeedback) (string, error) {
			calls++
			return "local-follow-up", nil
		},
	}

	monitor.checkTaskPR(context.Background(), broker.Snapshot().Tasks[0])

	if calls != 0 {
		t.Fatalf("OnReviewFeedback calls = %d, want 0", calls)
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
