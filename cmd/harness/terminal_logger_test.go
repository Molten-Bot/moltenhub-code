package main

import (
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

type recordingTerminalLogSink struct {
	lines []string
}

func (s *recordingTerminalLogSink) WriteLine(line string) {
	s.lines = append(s.lines, line)
}

func (s *recordingTerminalLogSink) Close() error {
	return nil
}

func TestFormatTerminalLogLineDecodesBase64CommandOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=clone name=git stream=stdout b64=" + base64.StdEncoding.EncodeToString([]byte("Cloning into 'repo'..."))

	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeNormal {
		t.Fatalf("mode = %v, want normal", mode)
	}
	if strings.Contains(got, "b64=") {
		t.Fatalf("expected decoded line without b64 field: %q", got)
	}
	if !strings.Contains(got, `text="Cloning into 'repo'..."`) {
		t.Fatalf("expected decoded text in output: %q", got)
	}
}

func TestFormatTerminalLogLineSuppressesLowSignalCodexOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`  it("times out waiting for websocket response deadline after non-response payloads", async () => {`))

	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty output", got)
	}
}

func TestFormatTerminalLogLineDropsEmptyCommandPayloadMarker(t *testing.T) {
	t.Parallel()

	line := "dispatch request_id=local-1712345678-000007 cmd phase=codex name=codex stream=stderr b64="
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty output", got)
	}
}

func TestFormatTerminalLogLineKeepsImportantCodexOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte("ERROR: failed to apply patch"))

	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeNormal {
		t.Fatalf("mode = %v, want normal", mode)
	}
	if !strings.Contains(got, `text="ERROR: failed to apply patch"`) {
		t.Fatalf("expected important codex output to be kept: %q", got)
	}
}

func TestFormatTerminalLogLineDropsCodexSearchResultNoise(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`internal/hub/daemon_test.go:621: if got := result["message"]; got != "Failure: task failed. Error details: codex: process exited with status 1" {`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineDropsNestedDispatchLogEchoNoise(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`dispatch request_id=local-1775872065-000001 cmd phase=codex name=codex stream=stderr text="fatal: unable to access 'https://github.com/acme/repo.git/': Could not resolve host: github.com"`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineDropsNestedDispatchLogEchoFromSearchResults(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`/home/jef/git/moltenbot/local/moltenhub-code/.log/terminal.log:431:dispatch request_id=local-1775872228-000004 cmd phase=codex name=codex stream=stderr text=\"fatal: unable to access 'https://github.com/acme/repo.git/': Could not resolve host: github.com\"`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineDropsCodexNoiseContainingExceptionally(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte("- Functions that do one thing exceptionally well"))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineKeepsCodexUnhandledExceptionOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`Exception in thread "main" java.lang.NullPointerException`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeNormal {
		t.Fatalf("mode = %v, want normal", mode)
	}
	if !strings.Contains(got, "NullPointerException") {
		t.Fatalf("expected unhandled exception output to be preserved: %q", got)
	}
}

func TestFormatTerminalLogLineKeepsCodexCompilerStyleFailure(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`cmd/harness/main.go:10:2: undefined: notARealSymbol`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeNormal {
		t.Fatalf("mode = %v, want normal", mode)
	}
	if !strings.Contains(got, `undefined: notARealSymbol`) {
		t.Fatalf("expected compiler-style failure to be kept: %q", got)
	}
}

func TestFormatTerminalLogLineDropsNestedHarnessLogLinesFromCodexOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`dispatch request_id=local-1775872076-000002 stage=codex status=running elapsed_s=75`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineDropsNestedHarnessErrorLinesFromCodexOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`dispatch request_id=local-1775872076-000002 cmd phase=clone name=git stream=stderr text="fatal: Remote branch moltenhub-review missing"`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineDropsNumberedNestedHarnessErrorLinesFromCodexOutput(t *testing.T) {
	t.Parallel()

	line := "cmd phase=codex name=codex stream=stderr b64=" + base64.StdEncoding.EncodeToString([]byte(`25:dispatch request_id=local-1775873174-000001 cmd phase=clone name=git stream=stderr text="fatal: Remote branch moltenhub-review-the-previous-local-task-logs-firs not found in upstream origin"`))
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeDrop {
		t.Fatalf("mode = %v, want drop", mode)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFormatTerminalLogLineCompactsSingleRunAgentHeartbeat(t *testing.T) {
	t.Parallel()

	got, mode := formatTerminalLogLine("stage=claude status=running elapsed_s=45")
	if mode != terminalLogModeProgress {
		t.Fatalf("mode = %v, want progress", mode)
	}
	if got != "claude running... 45s" {
		t.Fatalf("got %q, want compact progress line", got)
	}
}

func TestFormatTerminalLogLineCompactsGenericAgentHeartbeat(t *testing.T) {
	t.Parallel()

	got, mode := formatTerminalLogLine("stage=agent status=running elapsed_s=8")
	if mode != terminalLogModeProgress {
		t.Fatalf("mode = %v, want progress", mode)
	}
	if got != "agent running... 8s" {
		t.Fatalf("got %q, want compact progress line", got)
	}
}

func TestFormatTerminalLogLineLeavesHubHeartbeatUntouched(t *testing.T) {
	t.Parallel()

	line := "dispatch request_id=req-9 stage=codex status=running elapsed_s=30"
	got, mode := formatTerminalLogLine(line)
	if mode != terminalLogModeNormal {
		t.Fatalf("mode = %v, want normal", mode)
	}
	if got != line {
		t.Fatalf("got %q, want original line", got)
	}
}

func TestTerminalLoggerCaptureWritesTrimmedLineToSink(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	logger := newTerminalLogger(io.Discard, false)
	logger.sink = sink

	logger.Capture("  session=task-004 status=ok  ")
	logger.Capture("   ")

	if len(sink.lines) != 1 {
		t.Fatalf("sink lines = %d, want 1", len(sink.lines))
	}
	if sink.lines[0] != "session=task-004 status=ok" {
		t.Fatalf("sink line = %q", sink.lines[0])
	}
}

func TestTerminalLoggerHidesDebugLinesFromRegularOutputButKeepsSink(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	var out strings.Builder
	logger := newTerminalLogger(&out, false)
	logger.sink = sink

	logger.Print("debug dispatcher status=window state=steady cpu=11.6 memory=35.2 disk_io_mb_s=0.4 allowed=24 max=24 running=0 queue_depth=0")

	if got := out.String(); got != "" {
		t.Fatalf("terminal output = %q, want empty", got)
	}
	if len(sink.lines) != 1 {
		t.Fatalf("sink lines = %d, want 1", len(sink.lines))
	}
	if sink.lines[0] != "debug dispatcher status=window state=steady cpu=11.6 memory=35.2 disk_io_mb_s=0.4 allowed=24 max=24 running=0 queue_depth=0" {
		t.Fatalf("sink line = %q", sink.lines[0])
	}
}
