package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	b.IngestLog("dispatch status=completed request_id=req-42 workspace=/tmp/run branch=moltenhub-feature pr_url=https://github.com/acme/repo/pull/99")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.RequestID != requestID {
		t.Fatalf("task.RequestID = %q", task.RequestID)
	}
	if task.Status != "completed" {
		t.Fatalf("task.Status = %q, want completed", task.Status)
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

func TestBrokerNormalizesLegacyOKTerminalStatusToCompleted(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-legacy")
	b.IngestLog("dispatch status=ok request_id=req-legacy workspace=/tmp/run branch=moltenhub-legacy")

	snap := b.Snapshot()
	if got, want := len(snap.Tasks), 1; got != want {
		t.Fatalf("len(tasks) = %d, want %d", got, want)
	}
	if got, want := snap.Tasks[0].Status, "completed"; got != want {
		t.Fatalf("task.Status = %q, want %q", got, want)
	}
}

func TestBrokerDropsEmptyCommandPayloadMarkers(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-empty-b64"

	b.IngestLog("dispatch status=start request_id=" + requestID)
	b.IngestLog("dispatch request_id=req-empty-b64 cmd phase=codex name=codex stream=stderr b64=")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got := len(snap.Tasks[0].Logs); got != 1 {
		t.Fatalf("len(task logs) = %d, want 1 start meta line only", got)
	}
	if got, want := len(snap.Events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d (drop empty command marker event)", got, want)
	}
}

func TestBrokerDropsInvalidCommandPayloadMarkers(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-invalid-b64"

	b.IngestLog("dispatch status=start request_id=" + requestID)
	b.IngestLog("dispatch request_id=req-invalid-b64 cmd phase=codex name=codex stream=stderr b64=%%%")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got := len(snap.Tasks[0].Logs); got != 1 {
		t.Fatalf("len(task logs) = %d, want 1 start meta line only", got)
	}
	if got, want := len(snap.Events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d (drop invalid command marker event)", got, want)
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
	if snap.Connection.HubTransport != "" {
		t.Fatalf("connection.hub_transport = %q, want empty until transport mode is known", snap.Connection.HubTransport)
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
	snap := b.Snapshot()
	if !snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = false after websocket connect")
	}
	if snap.Connection.HubTransport != hubTransportWS {
		t.Fatalf("connection.hub_transport = %q after websocket connect, want %q", snap.Connection.HubTransport, hubTransportWS)
	}

	b.IngestLog(`hub.ws status=disconnected err="network reset"`)
	snap = b.Snapshot()
	if snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = true after websocket disconnect")
	}
	if snap.Connection.HubTransport != hubTransportDisconnected {
		t.Fatalf("connection.hub_transport = %q after websocket disconnect, want %q", snap.Connection.HubTransport, hubTransportDisconnected)
	}

	b.IngestLog("hub.transport mode=runtime_pull")
	snap = b.Snapshot()
	if !snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = false after pull transport fallback")
	}
	if snap.Connection.HubTransport != hubTransportHTTPLongPoll {
		t.Fatalf("connection.hub_transport = %q after pull fallback, want %q", snap.Connection.HubTransport, hubTransportHTTPLongPoll)
	}

	b.IngestLog(`hub.pull status=error err="poll timeout"`)
	snap = b.Snapshot()
	if snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = true after pull transport error")
	}
	if snap.Connection.HubTransport != hubTransportDisconnected {
		t.Fatalf("connection.hub_transport = %q after pull error, want %q", snap.Connection.HubTransport, hubTransportDisconnected)
	}
	if snap.Connection.HubDomain != "eu.hub.molten.bot" {
		t.Fatalf("connection.hub_domain = %q", snap.Connection.HubDomain)
	}
}

func TestBrokerTracksMoltenHubPingRetryState(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog(`hub.connection status=retrying base_url=https://na.hub.molten.bot/v1 detail="Hub endpoint ping failed; retrying every 12s until live. Error: GET https://na.hub.molten.bot/ping returned status=503"`)

	snap := b.Snapshot()
	if snap.Connection.HubConnected {
		t.Fatal("connection.hub_connected = true during ping retry, want false")
	}
	if snap.Connection.HubTransport != hubTransportRetrying {
		t.Fatalf("connection.hub_transport = %q, want %q", snap.Connection.HubTransport, hubTransportRetrying)
	}
	if !strings.Contains(snap.Connection.HubDetail, "retrying every 12s") {
		t.Fatalf("connection.hub_detail = %q", snap.Connection.HubDetail)
	}

	b.IngestLog(`hub.connection status=reachable base_url=https://na.hub.molten.bot/v1 detail="https://na.hub.molten.bot/ping status=204"`)
	snap = b.Snapshot()
	if snap.Connection.HubTransport != hubTransportReachable {
		t.Fatalf("connection.hub_transport = %q, want %q", snap.Connection.HubTransport, hubTransportReachable)
	}
	if snap.Connection.HubDetail != "https://na.hub.molten.bot/ping status=204" {
		t.Fatalf("connection.hub_detail = %q", snap.Connection.HubDetail)
	}

	b.IngestLog("hub.ws status=connected")
	snap = b.Snapshot()
	if snap.Connection.HubTransport != hubTransportWS {
		t.Fatalf("connection.hub_transport = %q after websocket connect, want %q", snap.Connection.HubTransport, hubTransportWS)
	}
	if snap.Connection.HubDetail != "" {
		t.Fatalf("connection.hub_detail = %q after websocket connect, want empty", snap.Connection.HubDetail)
	}
}

func TestBrokerTracksDispatcherResourceWindow(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("debug dispatcher status=window state=steady cpu=12.5 memory=48.2 disk_io_mb_s=3.7 allowed=2 max=4 running=1 queue_depth=0")

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

func TestBrokerCapturesFirstPRURLFromCompletedPRURLsField(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-prs skill=moltenhub_code_run repo=git@github.com:acme/repo.git")
	b.IngestLog("dispatch status=completed request_id=req-prs workspace=/tmp/run branch=moltenhub-feature pr_url= pr_urls=https://github.com/acme/repo-a/pull/101,https://github.com/acme/repo-b/pull/202")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got, want := snap.Tasks[0].PRURL, "https://github.com/acme/repo-a/pull/101"; got != want {
		t.Fatalf("task.PRURL = %q, want %q", got, want)
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

func TestBrokerIgnoresNestedRequestMetadataInsideQuotedCommandText(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "local-1712345678-000001"
	b.IngestLog("dispatch status=start request_id=" + requestID)
	b.IngestLog(`dispatch request_id=local-1712345678-000001 cmd phase=codex name=codex stream=stderr text="dispatch status=error request_id=req-err-ws err=\"clone failed\""`)

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.RequestID != requestID {
		t.Fatalf("task.RequestID = %q, want %q", task.RequestID, requestID)
	}
	if task.Status != "running" {
		t.Fatalf("task.Status = %q, want running", task.Status)
	}
	if task.Error != "" {
		t.Fatalf("task.Error = %q, want empty", task.Error)
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

func TestBrokerDoesNotMaterializeDuplicateStatusAsTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=duplicate request_id=req-dup state=in_flight duplicate_of=req-100")

	snap := b.Snapshot()
	if len(snap.Tasks) != 0 {
		t.Fatalf("tasks = %d, want 0", len(snap.Tasks))
	}
	if len(snap.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(snap.Events))
	}
}

func TestBrokerDuplicateStatusDoesNotReplaceOriginalTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-100 skill=moltenhub_code_run repo=git@github.com:acme/repo.git")
	b.IngestLog(
		"dispatch status=completed request_id=req-100 workspace=/tmp/run branch=moltenhub-fix pr_url=https://github.com/acme/repo/pull/55",
	)
	b.IngestLog("dispatch status=duplicate request_id=req-dup state=completed duplicate_of=req-100")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.RequestID != "req-100" {
		t.Fatalf("request_id = %q, want req-100", task.RequestID)
	}
	if task.Status != "completed" {
		t.Fatalf("status = %q, want completed", task.Status)
	}
	if task.Error != "" {
		t.Fatalf("error = %q, want empty", task.Error)
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
	payload := []byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"rerun me"}`)

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
	if snap.Tasks[0].BaseBranch != "main" {
		t.Fatalf("task.BaseBranch = %q, want %q", snap.Tasks[0].BaseBranch, "main")
	}
	if snap.Tasks[0].Branch != "main" {
		t.Fatalf("task.Branch = %q, want %q", snap.Tasks[0].Branch, "main")
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

func TestBrokerRecordTaskRunConfigCreatesPendingTaskAndNotifies(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	updates, cancel := b.Subscribe()
	defer cancel()

	requestID := "req-pending-config"
	payload := []byte(`{"repos":["git@github.com:acme/repo.git","git@github.com:acme/repo-two.git"],"baseBranch":"main","targetSubdir":".","prompt":"show immediately"}`)

	b.RecordTaskRunConfig(requestID, payload)

	select {
	case <-updates:
		// Expected.
	default:
		t.Fatal("expected update signal")
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.RequestID != requestID {
		t.Fatalf("task.RequestID = %q, want %q", task.RequestID, requestID)
	}
	if task.Status != "pending" {
		t.Fatalf("task.Status = %q, want pending", task.Status)
	}
	if task.Stage != "dispatch" || task.StageStatus != "queued" {
		t.Fatalf("task stage = (%q, %q), want (dispatch, queued)", task.Stage, task.StageStatus)
	}
	if task.Prompt != "show immediately" {
		t.Fatalf("task.Prompt = %q, want %q", task.Prompt, "show immediately")
	}
	if task.Repo != "git@github.com:acme/repo.git" {
		t.Fatalf("task.Repo = %q", task.Repo)
	}
	if got, want := strings.Join(task.Repos, ","), "git@github.com:acme/repo.git,git@github.com:acme/repo-two.git"; got != want {
		t.Fatalf("task.Repos = %q, want %q", got, want)
	}
	if task.BaseBranch != "main" || task.Branch != "main" {
		t.Fatalf("task branches = (%q, %q), want main", task.BaseBranch, task.Branch)
	}
	if !task.CanRerun {
		t.Fatal("task.CanRerun = false, want true")
	}
}

func TestBrokerTaskRunConfigSupportsBranchAlias(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-rerun-branch-alias"
	payload := []byte(`{"repo":"git@github.com:acme/repo.git","branch":"release/2026.04","targetSubdir":".","prompt":"rerun me"}`)

	b.RecordTaskRunConfig(requestID, payload)
	b.IngestLog("dispatch status=start request_id=req-rerun-branch-alias skill=moltenhub_code_run repo=git@github.com:acme/repo.git")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got, want := snap.Tasks[0].Branch, "release/2026.04"; got != want {
		t.Fatalf("task.Branch = %q, want %q", got, want)
	}
	if got, want := snap.Tasks[0].BaseBranch, "release/2026.04"; got != want {
		t.Fatalf("task.BaseBranch = %q, want %q", got, want)
	}
}

func TestBrokerUpdatesTaskBranchFromStageLogsWhileRetainingBaseBranch(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-branch-transition"
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"main","prompt":"rerun me"}`))
	b.IngestLog("dispatch status=start request_id=req-branch-transition skill=moltenhub_code_run repo=git@github.com:acme/repo.git")
	b.IngestLog("dispatch request_id=req-branch-transition stage=git status=ok action=branch branch=moltenhub-branch-transition")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got, want := snap.Tasks[0].BaseBranch, "main"; got != want {
		t.Fatalf("task.BaseBranch = %q, want %q", got, want)
	}
	if got, want := snap.Tasks[0].Branch, "moltenhub-branch-transition"; got != want {
		t.Fatalf("task.Branch = %q, want %q", got, want)
	}
}

func TestBrokerCloseTaskRemovesCompletedTaskAndPreservesRunConfig(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-close"
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"close me"}`))
	b.IngestLog("dispatch status=start request_id=req-close")
	b.IngestLog("dispatch status=completed request_id=req-close workspace=/tmp/run branch=moltenhub-close")

	if err := b.CloseTask(requestID); err != nil {
		t.Fatalf("CloseTask() error = %v", err)
	}

	if _, ok := b.TaskRunConfig(requestID); !ok {
		t.Fatal("TaskRunConfig() found = false after CloseTask(), want true")
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 0 {
		t.Fatalf("len(tasks) = %d, want 0", len(snap.Tasks))
	}
}

func TestBrokerCloseTaskIgnoresLateCleanupLogs(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-close-cleanup"
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"close me"}`))
	b.IngestLog("dispatch status=start request_id=req-close-cleanup")
	b.IngestLog("dispatch status=completed request_id=req-close-cleanup workspace=/tmp/run branch=moltenhub-close-cleanup")

	if err := b.CloseTask(requestID); err != nil {
		t.Fatalf("CloseTask() error = %v", err)
	}

	b.IngestLog("dispatch status=ok action=task_close_cleanup request_id=req-close-cleanup log_dir=/tmp/.log/req/close/cleanup")

	snap := b.Snapshot()
	if len(snap.Tasks) != 0 {
		t.Fatalf("len(tasks) after cleanup log = %d, want 0", len(snap.Tasks))
	}
}

func TestBrokerIgnoresActionOnlyDispatchStatusForTerminalState(t *testing.T) {
	t.Parallel()

	b := NewBroker()

	b.IngestLog("dispatch status=start request_id=req-follow-up")
	b.IngestLog(`dispatch status=error request_id=req-follow-up exit_code=50 err="codex failed"`)
	b.IngestLog("dispatch status=ok action=queue_failure_followup request_id=req-follow-up follow_up_request_id=req-fix")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(snap.Tasks))
	}
	if got := snap.Tasks[0].Status; got != "error" {
		t.Fatalf("task.Status = %q, want error", got)
	}
}

func TestBrokerClosedTaskTombstoneExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	b := NewBroker()
	b.now = func() time.Time { return now }

	requestID := "req-reuse-after-close"
	b.IngestLog("dispatch status=stopped request_id=req-reuse-after-close err=\"task stopped by operator\"")
	if err := b.CloseTask(requestID); err != nil {
		t.Fatalf("CloseTask() error = %v", err)
	}

	b.IngestLog("dispatch status=start request_id=req-reuse-after-close")
	if got := len(b.Snapshot().Tasks); got != 0 {
		t.Fatalf("len(tasks) before tombstone expiry = %d, want 0", got)
	}

	now = now.Add(defaultClosedTaskRetention + time.Second)
	b.IngestLog("dispatch status=start request_id=req-reuse-after-close")

	snap := b.Snapshot()
	if got := len(snap.Tasks); got != 1 {
		t.Fatalf("len(tasks) after tombstone expiry = %d, want 1", got)
	}
	if got := snap.Tasks[0].RequestID; got != requestID {
		t.Fatalf("task.RequestID = %q, want %q", got, requestID)
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

func TestBrokerTracksPausedResumedAndStoppedStatuses(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=paused request_id=req-control")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("len(tasks) after pause = %d, want 1", len(snap.Tasks))
	}
	if got := snap.Tasks[0].Status; got != "paused" {
		t.Fatalf("status after pause = %q, want %q", got, "paused")
	}

	b.IngestLog("dispatch status=resumed request_id=req-control")
	snap = b.Snapshot()
	if got := snap.Tasks[0].Status; got != "pending" {
		t.Fatalf("status after resume = %q, want %q", got, "pending")
	}

	b.IngestLog(`dispatch status=stopped request_id=req-control err="task was stopped by operator"`)
	snap = b.Snapshot()
	if got := snap.Tasks[0].Status; got != "stopped" {
		t.Fatalf("status after stop = %q, want %q", got, "stopped")
	}
	if got := snap.Tasks[0].Error; got != "task was stopped by operator" {
		t.Fatalf("error after stop = %q, want %q", got, "task was stopped by operator")
	}
}

func TestBrokerCloseTaskAllowsStoppedTasks(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-stopped-close"
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"stop me"}`))
	b.IngestLog(`dispatch status=stopped request_id=req-stopped-close err="task stopped by operator"`)

	if err := b.CloseTask(requestID); err != nil {
		t.Fatalf("CloseTask() for stopped task error = %v", err)
	}
	if _, ok := b.TaskRunConfig(requestID); !ok {
		t.Fatal("TaskRunConfig() found = false after CloseTask(stopped), want true")
	}
}

func TestBrokerTracksTaskAttemptsAcrossRerunsAndFailures(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-attempt")
	b.IngestLog(`dispatch status=error request_id=req-attempt exit_code=1 err="first failure"`)
	b.RecordTaskRerunAttempt("req-attempt", "req-attempt-rerun")
	b.IngestLog("dispatch status=start request_id=req-attempt-rerun")
	b.IngestLog("dispatch status=completed request_id=req-attempt-rerun workspace=/tmp/run branch=moltenhub-attempt")

	attempts := b.TaskAttempts("req-attempt")
	if len(attempts) != 2 {
		t.Fatalf("len(attempts) = %d, want 2: %#v", len(attempts), attempts)
	}
	if got, want := attempts[0].RequestID, "req-attempt"; got != want {
		t.Fatalf("attempt[0].RequestID = %q, want %q", got, want)
	}
	if got, want := attempts[0].Status, "error"; got != want {
		t.Fatalf("attempt[0].Status = %q, want %q", got, want)
	}
	if got, want := attempts[0].Error, "first failure"; got != want {
		t.Fatalf("attempt[0].Error = %q, want %q", got, want)
	}
	if got, want := attempts[1].RequestID, "req-attempt-rerun"; got != want {
		t.Fatalf("attempt[1].RequestID = %q, want %q", got, want)
	}
	if got, want := attempts[1].RerunOf, "req-attempt"; got != want {
		t.Fatalf("attempt[1].RerunOf = %q, want %q", got, want)
	}
	if got, want := attempts[1].Status, "completed"; got != want {
		t.Fatalf("attempt[1].Status = %q, want %q", got, want)
	}

	rerunAttempts := b.TaskAttempts("req-attempt-rerun")
	if len(rerunAttempts) != len(attempts) {
		t.Fatalf("len(rerun attempts) = %d, want %d", len(rerunAttempts), len(attempts))
	}
}

func TestBrokerKeepsCompletedTasksVisibleUntilClosed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	b := NewBroker()
	b.now = func() time.Time { return now }

	b.RecordTaskRunConfig("req-ok", []byte(`{"repo":"git@github.com:acme/repo.git","prompt":"keep me visible"}`))
	b.IngestLog("dispatch status=start request_id=req-ok")
	b.IngestLog("dispatch status=completed request_id=req-ok workspace=/tmp/run branch=moltenhub-cleanup")

	if got := len(b.Snapshot().Tasks); got != 1 {
		t.Fatalf("len(tasks) before retention = %d, want 1", got)
	}

	now = now.Add(10 * time.Minute)
	snap := b.Snapshot()
	if got := len(snap.Tasks); got != 1 {
		t.Fatalf("len(tasks) after waiting = %d, want 1", got)
	}
	if got, want := snap.Tasks[0].Status, "completed"; got != want {
		t.Fatalf("task.Status = %q, want %q", got, want)
	}
	if _, ok := b.TaskRunConfig("req-ok"); !ok {
		t.Fatal("TaskRunConfig() found = false for visible completed task, want true")
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
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"release/2026.04","prompt":"late prompt value"}`))

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].Prompt != "late prompt value" {
		t.Fatalf("task.Prompt = %q, want %q", snap.Tasks[0].Prompt, "late prompt value")
	}
	if snap.Tasks[0].BaseBranch != "release/2026.04" {
		t.Fatalf("task.BaseBranch = %q, want %q", snap.Tasks[0].BaseBranch, "release/2026.04")
	}
	if snap.Tasks[0].Branch != "release/2026.04" {
		t.Fatalf("task.Branch = %q, want %q", snap.Tasks[0].Branch, "release/2026.04")
	}
	if !snap.Tasks[0].PromptIsUserInput {
		t.Fatal("task.PromptIsUserInput = false, want true for user-entered prompt")
	}
}

func TestBrokerMarksFailureFollowUpPromptAsSystemGenerated(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-after-start-failure-review"

	b.IngestLog("dispatch status=start request_id=req-after-start-failure-review skill=moltenhub_code_run repo=git@github.com:acme/repo.git")
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"main","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying MoltenHub Code application issues in this repository, validate locally where possible, and summarize the verified results. Treat the original task prompt as failure context only; do not implement that requested product change here unless it is required to fix MoltenHub Code failure handling.\n\nObserved failure context:\n- error=boom"}`))

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].PromptIsUserInput {
		t.Fatal("task.PromptIsUserInput = true, want false for failure follow-up prompt")
	}
}

func TestBrokerResolvesLibraryTaskDisplayTextFromRunConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HARNESS_LIBRARY_DIR", tmp)
	if err := os.WriteFile(filepath.Join(tmp, "coverage.json"), []byte(`{
  "name": "unit-test-coverage",
  "displayName": "100% Unit Test Coverage",
  "description": "Raise coverage.",
  "prompt": "Raise coverage."
}`), 0o644); err != nil {
		t.Fatalf("write library task: %v", err)
	}

	b := NewBroker()
	requestID := "req-library-task"
	b.RecordTaskRunConfig(requestID, []byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"main","libraryTaskName":"unit-test-coverage"}`))
	b.IngestLog("dispatch status=start request_id=req-library-task skill=library_task repo=git@github.com:acme/repo.git")

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if got, want := snap.Tasks[0].Prompt, "100% Unit Test Coverage\n\nRaise coverage."; got != want {
		t.Fatalf("task.Prompt = %q, want %q", got, want)
	}
	if !snap.Tasks[0].PromptIsUserInput {
		t.Fatal("task.PromptIsUserInput = false, want true for library prompt")
	}
}

func TestBrokerRecordsRejectedPromptSubmission(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := b.RecordRejectedPromptSubmission(
		[]byte(`{"repo":"git@github.com:acme/repo.git","baseBranch":"main","prompt":"fix broken prompt"}`),
		"invalid",
		errors.New("invalid run config: prompt failed checks"),
	)

	if requestID == "" {
		t.Fatal("RecordRejectedPromptSubmission() requestID = empty, want value")
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(snap.Tasks))
	}

	task := snap.Tasks[0]
	if task.RequestID != requestID {
		t.Fatalf("task.RequestID = %q, want %q", task.RequestID, requestID)
	}
	if task.Status != "invalid" {
		t.Fatalf("task.Status = %q, want invalid", task.Status)
	}
	if task.Prompt != "fix broken prompt" {
		t.Fatalf("task.Prompt = %q, want %q", task.Prompt, "fix broken prompt")
	}
	if task.Repo != "git@github.com:acme/repo.git" {
		t.Fatalf("task.Repo = %q, want %q", task.Repo, "git@github.com:acme/repo.git")
	}
	if task.BaseBranch != "main" {
		t.Fatalf("task.BaseBranch = %q, want main", task.BaseBranch)
	}
	if task.Branch != "main" {
		t.Fatalf("task.Branch = %q, want main", task.Branch)
	}
	if task.CanRerun {
		t.Fatal("task.CanRerun = true, want false")
	}
	if task.Error != "invalid run config: prompt failed checks" {
		t.Fatalf("task.Error = %q, want detailed failure", task.Error)
	}
	if len(task.Logs) != 1 || !strings.Contains(task.Logs[0].Text, "prompt submission failed: invalid run config: prompt failed checks") {
		t.Fatalf("task.Logs = %#v, want failure log entry", task.Logs)
	}
}

func TestPromptFromRunConfigJSON(t *testing.T) {
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
		{
			name: "library task display text",
			raw:  []byte(`{"libraryTaskName":"unit-test-coverage"}`),
			want: "100% Unit Test Coverage\n\nRaise coverage.",
		},
		{
			name: "canonical library task display text",
			raw:  []byte(`{"librarytaskname":"unit-test-coverage"}`),
			want: "100% Unit Test Coverage\n\nRaise coverage.",
		},
		{
			name: "library task display text wins over expanded prompt",
			raw:  []byte(`{"libraryTaskName":"unit-test-coverage","prompt":"Full execution prompt."}`),
			want: "100% Unit Test Coverage\n\nRaise coverage.",
		},
		{
			name: "library task falls back to prompt when metadata missing",
			raw:  []byte(`{"libraryTaskName":"missing-task","prompt":"Full execution prompt."}`),
			want: "Full execution prompt.",
		},
	}

	tmp := t.TempDir()
	t.Setenv("HARNESS_LIBRARY_DIR", tmp)
	if err := os.WriteFile(filepath.Join(tmp, "coverage.json"), []byte(`{
  "name": "unit-test-coverage",
  "displayName": "100% Unit Test Coverage",
  "description": "Raise coverage.",
  "prompt": "Raise coverage."
}`), 0o644); err != nil {
		t.Fatalf("write library task: %v", err)
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := promptFromRunConfigJSON(tt.raw); got != tt.want {
				t.Fatalf("promptFromRunConfigJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBranchFromRunConfigJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "base branch camelCase",
			raw:  []byte(`{"baseBranch":"main"}`),
			want: "main",
		},
		{
			name: "branch alias",
			raw:  []byte(`{"branch":"release/2026.04"}`),
			want: "release/2026.04",
		},
		{
			name: "prefer base branch",
			raw:  []byte(`{"baseBranch":"main","branch":"feature-x"}`),
			want: "main",
		},
		{
			name: "missing branch",
			raw:  []byte(`{"prompt":"run tests"}`),
			want: "",
		},
		{
			name: "invalid json",
			raw:  []byte(`{"baseBranch":"main"`),
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := branchFromRunConfigJSON(tt.raw); got != tt.want {
				t.Fatalf("branchFromRunConfigJSON() = %q, want %q", got, tt.want)
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
