package hubui

import (
	"encoding/base64"
	"testing"
)

func TestBrokerTracksTaskLifecycleAndCommandOutput(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-42"

	b.IngestLog("dispatch status=start request_id=req-42 skill=codex_harness_run repo=git@github.com:acme/repo.git")
	b.IngestLog("dispatch request_id=req-42 stage=codex status=start")
	b.IngestLog("dispatch request_id=req-42 cmd phase=codex name=codex stream=stdout b64=" + base64.StdEncoding.EncodeToString([]byte("thinking...")))
	b.IngestLog("dispatch status=ok request_id=req-42 workspace=/tmp/run branch=codex/feature pr_url=https://github.com/acme/repo/pull/99")

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

func TestBrokerCapturesWorkspaceAndBranchFromErrorStatus(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog(`dispatch status=error request_id=req-err-ws exit_code=40 workspace=/tmp/run-x branch=codex/fix pr_url=https://github.com/acme/repo/pull/55 err="clone failed"`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.WorkspaceDir != "/tmp/run-x" {
		t.Fatalf("workspace = %q, want /tmp/run-x", task.WorkspaceDir)
	}
	if task.Branch != "codex/fix" {
		t.Fatalf("branch = %q, want codex/fix", task.Branch)
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
