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
