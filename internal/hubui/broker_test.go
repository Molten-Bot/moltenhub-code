package hubui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBrokerTracksTaskLifecycleAndCommandOutput(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-42"

	b.IngestLog("dispatch status=start request_id=req-42 skill=moltenhub_code_run repo=git@github.com:acme/repo.git repos=git@github.com:acme/repo.git,git@github.com:acme/repo-two.git")
	b.IngestLog("dispatch request_id=req-42 stage=codex status=start")
	b.IngestLog("dispatch request_id=req-42 cmd phase=codex name=codex stream=stdout b64=" + base64.StdEncoding.EncodeToString([]byte("thinking...")))
	b.IngestLog("dispatch status=ok request_id=req-42 workspace=/tmp/run branch=moltenhub-feature pr_url=https://github.com/acme/repo/pull/99")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.RequestID != requestID {
		t.Fatalf("task.RequestID = %q", task.RequestID)
	}
	if task.Status != "ok" {
		t.Fatalf("task.Status = %q, want ok", task.Status)
	}
	if task.Stage != "codex" {
		t.Fatalf("task.Stage = %q, want codex", task.Stage)
	}
	if got, want := len(task.Repos), 2; got != want {
		t.Fatalf("len(task.Repos) = %d, want %d", got, want)
	}
	if task.Repos[0] != "git@github.com:acme/repo.git" || task.Repos[1] != "git@github.com:acme/repo-two.git" {
		t.Fatalf("task.Repos = %#v", task.Repos)
	}
	if task.PRURL != "https://github.com/acme/repo/pull/99" {
		t.Fatalf("task.PRURL = %q", task.PRURL)
	}

	foundOutput := false
	for _, line := range task.Logs {
		if line.Stream == "stdout" && line.Text == "thinking..." {
			foundOutput = true
			break
		}
	}
	if !foundOutput {
		t.Fatalf("task logs missing decoded stdout line: %#v", task.Logs)
	}
}

func TestBrokerTracksMoltenHubConnectionAndDomain(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("hub.connection status=configured base_url=https://na.hub.molten.bot/v1")
	b.IngestLog("hub.auth status=ok")

	snap := b.Snapshot()
	if !snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = false, want true")
	}
	if snap.Connection.HubBaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("connection.hub_base_url = %q", snap.Connection.HubBaseURL)
	}
	if snap.Connection.HubDomain != "na.hub.molten.bot" {
		t.Fatalf("connection.hub_domain = %q", snap.Connection.HubDomain)
	}
}

func TestBrokerTracksMoltenHubConnectionTransitions(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("hub.connection status=configured base_url=https://eu.hub.molten.bot/v1")
	b.IngestLog("hub.ws status=connected")
	if !b.Snapshot().Connection.HubConnected {
		t.Fatal("connection.hub_connected = false after websocket connect")
	}

	b.IngestLog(`hub.ws status=disconnected err="network reset"`)
	if b.Snapshot().Connection.HubConnected {
		t.Fatal("connection.hub_connected = true after websocket disconnect")
	}

	b.IngestLog("hub.transport mode=openclaw_pull")
	if !b.Snapshot().Connection.HubConnected {
		t.Fatal("connection.hub_connected = false after pull transport fallback")
	}

	b.IngestLog(`hub.pull status=error err="poll timeout"`)
	snap := b.Snapshot()
	if snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = true after pull transport error")
	}
	if snap.Connection.HubDomain != "eu.hub.molten.bot" {
		t.Fatalf("connection.hub_domain = %q", snap.Connection.HubDomain)
	}
}

func TestBrokerTracksDispatcherResourceWindow(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatcher status=window state=steady cpu=12.5 memory=48.2 disk_io_mb_s=3.7 allowed=2 max=4 running=1 queue_depth=0")

	snap := b.Snapshot()
	if got := snap.Resources.CPUPercent; got != 12.5 {
		t.Fatalf("resources.cpu_percent = %v, want 12.5", got)
	}
	if got := snap.Resources.MemoryPercent; got != 48.2 {
		t.Fatalf("resources.memory_percent = %v, want 48.2", got)
	}
	if got := snap.Resources.DiskIOMBs; got != 3.7 {
		t.Fatalf("resources.disk_io_mb_s = %v, want 3.7", got)
	}
	if snap.Resources.UpdatedAt == "" {
		t.Fatal("resources.updated_at = empty, want timestamp")
	}
}

func TestBrokerCapturesPRURLFromStageLine(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-pr skill=moltenhub_code_run repo=git@github.com:acme/repo.git")
	b.IngestLog("dispatch request_id=req-pr stage=pr status=ok pr_url=https://github.com/acme/repo/pull/101")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].PRURL != "https://github.com/acme/repo/pull/101" {
		t.Fatalf("task.PRURL = %q", snap.Tasks[0].PRURL)
	}
}

func TestBrokerFallsBackToSingleRepoWhenReposFieldMissing(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-single skill=moltenhub_code_run repo=git@github.com:acme/repo.git")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got, want := len(snap.Tasks[0].Repos), 1; got != want {
		t.Fatalf("len(task.Repos) = %d, want %d", got, want)
	}
	if snap.Tasks[0].Repos[0] != "git@github.com:acme/repo.git" {
		t.Fatalf("task.Repos = %#v", snap.Tasks[0].Repos)
	}
}

func TestBrokerParsesQuotedErrorText(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog(`dispatch status=error request_id=req-err exit_code=50 err="codex exploded at step two"`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].Status != "error" {
		t.Fatalf("status = %q, want error", snap.Tasks[0].Status)
	}
	if snap.Tasks[0].ExitCode != 50 {
		t.Fatalf("exit code = %d, want 50", snap.Tasks[0].ExitCode)
	}
	if snap.Tasks[0].Error != "codex exploded at step two" {
		t.Fatalf("error = %q", snap.Tasks[0].Error)
	}
}

func TestBrokerParsesQuotedErrorTextWithEscapedQuotes(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog(`dispatch status=error request_id=req-err-escaped exit_code=10 err="decode run config payload: json: unknown field \"branch\""`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].Error != `decode run config payload: json: unknown field "branch"` {
		t.Fatalf("error = %q", snap.Tasks[0].Error)
	}
}

func TestBrokerParsesStageErrorText(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-stage-err")
	b.IngestLog(`dispatch request_id=req-stage-err stage=codex status=error err="codex command failed"`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.Status != "error" {
		t.Fatalf("status = %q, want error", task.Status)
	}
	if task.Stage != "codex" {
		t.Fatalf("stage = %q, want codex", task.Stage)
	}
	if task.StageStatus != "error" {
		t.Fatalf("stage status = %q, want error", task.StageStatus)
	}
	if task.Error != "codex command failed" {
		t.Fatalf("error = %q", task.Error)
	}
}

func TestBrokerParsesStageErrorTextWithEscapedQuotes(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-stage-err-quoted")
	b.IngestLog(`dispatch request_id=req-stage-err-quoted stage=codex status=error err="decode run config payload: json: unknown field \"branch\""`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].Error != `decode run config payload: json: unknown field "branch"` {
		t.Fatalf("error = %q", snap.Tasks[0].Error)
	}
}

func TestBrokerCapturesWorkspaceAndBranchFromErrorStatus(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog(`dispatch status=error request_id=req-err-ws exit_code=40 workspace=/tmp/run-x branch=moltenhub-fix pr_url=https://github.com/acme/repo/pull/55 err="clone failed"`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.WorkspaceDir != "/tmp/run-x" {
		t.Fatalf("workspace = %q, want /tmp/run-x", task.WorkspaceDir)
	}
	if task.Branch != "moltenhub-fix" {
		t.Fatalf("branch = %q, want moltenhub-fix", task.Branch)
	}
	if task.PRURL != "https://github.com/acme/repo/pull/55" {
		t.Fatalf("pr_url = %q", task.PRURL)
	}
}

func TestBrokerCapturesDuplicateStatus(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=duplicate request_id=req-dup state=in_flight duplicate_of=req-100")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.Status != "duplicate" {
		t.Fatalf("status = %q, want duplicate", task.Status)
	}
	if task.StageStatus != "in_flight" {
		t.Fatalf("stage status = %q, want in_flight", task.StageStatus)
	}
	if task.Error != "duplicate submission ignored (state=in_flight, duplicate_of=req-100)" {
		t.Fatalf("error = %q", task.Error)
	}
}

func TestBrokerSubscribeSignalsChanges(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	updates, cancel := b.Subscribe()
	defer cancel()

	b.IngestLog("dispatch status=start request_id=req-1")

	select {
	case <-updates:
		// Expected.
	default:
		t.Fatal("expected update signal")
	}
}

func TestBrokerTaskRunConfigSupportsRerunMetadata(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-rerun"
	payload := []byte(`{"repo":"git@github.com:acme/repo.git","base_branch":"main","target_subdir":".","prompt":"rerun me"}`)

	b.RecordTaskRunConfig(requestID, payload)
	b.IngestLog("dispatch status=start request_id=req-rerun skill=moltenhub_code_run repo=git@github.com:acme/repo.git")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if !snap.Tasks[0].CanRerun {
		t.Fatal("task.CanRerun = false, want true")
	}
	if snap.Tasks[0].Prompt != "rerun me" {
		t.Fatalf("task.Prompt = %q, want %q", snap.Tasks[0].Prompt, "rerun me")
	}

	got, ok := b.TaskRunConfig(requestID)
	if !ok {
		t.Fatal("TaskRunConfig() found = false, want true")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("TaskRunConfig() = %q, want %q", string(got), string(payload))
	}

	got[0] = 'x'
	gotAgain, ok := b.TaskRunConfig(requestID)
	if !ok {
		t.Fatal("TaskRunConfig() second fetch found = false, want true")
	}
	if bytes.Equal(gotAgain, got) {
		t.Fatalf("TaskRunConfig() returned shared slice %q", string(gotAgain))
	}
}

func TestBrokerCloseTaskRemovesCompletedTaskAndRunConfig(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-close"
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"close me"}`))
	b.IngestLog("dispatch status=start request_id=req-close")
	b.IngestLog("dispatch status=ok request_id=req-close workspace=/tmp/run branch=moltenhub-close")

	if err := b.CloseTask(requestID); err != nil {
		t.Fatalf("CloseTask() error = %v", err)
	}

	if _, ok := b.TaskRunConfig(requestID); ok {
		t.Fatal("TaskRunConfig() found = true after CloseTask(), want false")
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 0 {
		t.Fatalf("len(tasks) = %d, want 0", len(snap.Tasks))
	}
}

func TestBrokerCloseTaskRejectsIncompleteTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-running"
	b.IngestLog("dispatch status=start request_id=req-running")

	err := b.CloseTask(requestID)
	if !errors.Is(err, ErrTaskNotCompleted) {
		t.Fatalf("CloseTask() error = %v, want %v", err, ErrTaskNotCompleted)
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(snap.Tasks))
	}
}

func TestBrokerCloseTaskMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	err := b.CloseTask("req-missing")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("CloseTask() error = %v, want %v", err, ErrTaskNotFound)
	}
}

func TestBrokerHidesCompletedOKTasksAfterFiveMinutes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	b := NewBroker()
	b.now = func() time.Time { return now }

	b.IngestLog("dispatch status=start request_id=req-ok")
	b.IngestLog("dispatch status=ok request_id=req-ok workspace=/tmp/run branch=moltenhub-cleanup")

	if got := len(b.Snapshot().Tasks); got != 1 {
		t.Fatalf("len(tasks) before retention = %d, want 1", got)
	}

	now = now.Add(4*time.Minute + 59*time.Second)
	if got := len(b.Snapshot().Tasks); got != 1 {
		t.Fatalf("len(tasks) before ttl expiry = %d, want 1", got)
	}

	now = now.Add(1 * time.Second)
	snap := b.Snapshot()
	if got := len(snap.Tasks); got != 0 {
		t.Fatalf("len(tasks) after ttl expiry = %d, want 0", got)
	}
	if _, ok := b.TaskRunConfig("req-ok"); ok {
		t.Fatal("TaskRunConfig() found = true for pruned ok task, want false")
	}
}

func TestBrokerKeepsFailedTasksAfterFiveMinutes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	b := NewBroker()
	b.now = func() time.Time { return now }

	b.IngestLog("dispatch status=start request_id=req-error")
	b.IngestLog(`dispatch status=error request_id=req-error exit_code=50 err="codex exploded"`)

	now = now.Add(6 * time.Minute)
	snap := b.Snapshot()
	if got := len(snap.Tasks); got != 1 {
		t.Fatalf("len(tasks) after ttl for failed task = %d, want 1", got)
	}
	if snap.Tasks[0].Status != "error" {
		t.Fatalf("status = %q, want error", snap.Tasks[0].Status)
	}
}

func TestBrokerAppliesPromptWhenConfigRecordedAfterTaskStart(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-after-start"

	b.IngestLog("dispatch status=start request_id=req-after-start skill=moltenhub_code_run repo=git@github.com:acme/repo.git")
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"late prompt value"}`))

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].Prompt != "late prompt value" {
		t.Fatalf("task.Prompt = %q, want %q", snap.Tasks[0].Prompt, "late prompt value")
	}
}

func TestPromptFromRunConfigJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "valid prompt",
			raw:  []byte(`{"prompt":"run integration tests"}`),
			want: "run integration tests",
		},
		{
			name: "missing prompt",
			raw:  []byte(`{"repo":"git@github.com:acme/repo.git"}`),
			want: "",
		},
		{
			name: "invalid json",
			raw:  []byte(`{"prompt":"oops"`),
			want: "",
		},
		{
			name: "trimmed prompt",
			raw:  []byte(`{"prompt":"  refactor api  "}`),
			want: "refactor api",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := promptFromRunConfigJSON(tt.raw); got != tt.want {
				t.Fatalf("promptFromRunConfigJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBrokerCapsEventsWithoutGrowingSlice(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.maxEvents = 3

	for i := 1; i <= 5; i++ {
		b.IngestLog(fmt.Sprintf("dispatch status=start request_id=req-%d", i))
	}

	snap := b.Snapshot()
	if got, want := len(snap.Events), 3; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if snap.Events[0].RequestID != "req-3" || snap.Events[1].RequestID != "req-4" || snap.Events[2].RequestID != "req-5" {
		t.Fatalf("events request ids = %#v", []string{
			snap.Events[0].RequestID,
			snap.Events[1].RequestID,
			snap.Events[2].RequestID,
		})
	}
}

func TestBrokerCapsTaskLogsWithoutGrowingSlice(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.maxTaskLog = 2

	b.IngestLog("dispatch status=start request_id=req-cap")
	b.IngestLog("dispatch request_id=req-cap stage=clone status=start")
	b.IngestLog("dispatch request_id=req-cap stage=pr status=ok")

	snap := b.Snapshot()
	if got, want := len(snap.Tasks), 1; got != want {
		t.Fatalf("len(tasks) = %d, want %d", got, want)
	}
	logs := snap.Tasks[0].Logs
	if got, want := len(logs), 2; got != want {
		t.Fatalf("len(task logs) = %d, want %d", got, want)
	}
	if logs[0].Text != "dispatch request_id=req-cap stage=clone status=start" {
		t.Fatalf("logs[0].Text = %q", logs[0].Text)
	}
	if logs[1].Text != "dispatch request_id=req-cap stage=pr status=ok" {
		t.Fatalf("logs[1].Text = %q", logs[1].Text)
	}
}
