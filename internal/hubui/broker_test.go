package hubui

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestBrokerTracksTaskLifecycleAndCommandOutput(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-42"

	b.IngestLog("dispatch status=start request_id=req-42 skill=codex_harness_run repo=git@github.com:acme/repo.git repos=git@github.com:acme/repo.git,git@github.com:acme/repo-two.git")
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

func TestBrokerCapturesPRURLFromStageLine(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-pr skill=codex_harness_run repo=git@github.com:acme/repo.git")
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
	b.IngestLog("dispatch status=start request_id=req-single skill=codex_harness_run repo=git@github.com:acme/repo.git")

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
	b.IngestLog("dispatch status=start request_id=req-rerun skill=codex_harness_run repo=git@github.com:acme/repo.git")

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

func TestBrokerAppliesPromptWhenConfigRecordedAfterTaskStart(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-after-start"

	b.IngestLog("dispatch status=start request_id=req-after-start skill=codex_harness_run repo=git@github.com:acme/repo.git")
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
