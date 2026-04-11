package main

import (
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

func TestClassifyHubLogLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line string
		want hub.LogLevel
	}{
		{line: `dispatch status=error request_id=req-1 err="boom"`, want: hub.LogLevelError},
		{line: `hub.auth status=warn action=start err="denied"`, want: hub.LogLevelWarn},
		{line: `cmd phase=git name=git stream=stdout b64=SGVsbG8=`, want: hub.LogLevelDebug},
		{line: `dispatch status=start request_id=req-1 skill=code_for_me`, want: hub.LogLevelDebug},
		{line: `dispatch request_id=req-1 stage=clone status=ok repo=git@github.com:acme/repo.git`, want: hub.LogLevelDebug},
		{line: `dispatch request_id=req-1 stage=codex status=running elapsed_s=9`, want: hub.LogLevelDebug},
		{line: `dispatch status=ok request_id=req-1 workspace=/tmp/run branch=moltenhub-fix`, want: hub.LogLevelInfo},
		{line: `hub.auth status=ok`, want: hub.LogLevelInfo},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			if got := classifyHubLogLine(tt.line); got != tt.want {
				t.Fatalf("classifyHubLogLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestHubLogRouterFiltersByConfiguredLevel(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	var rendered strings.Builder
	logger := newTerminalLogger(&rendered, false)
	logger.sink = sink
	broker := hubui.NewBroker()
	router := newHubLogRouter(logger, broker, hub.LogLevelInfo)

	router.Log(`cmd phase=git name=git stream=stdout b64=SGVsbG8=`)
	router.Log(`hub.auth status=ok`)
	router.Log(`dispatch status=error request_id=req-1 err="boom"`)

	out := rendered.String()
	if strings.Contains(out, "cmd phase=git") {
		t.Fatalf("debug cmd line rendered at info level: %q", out)
	}
	if !strings.Contains(out, "hub.auth status=ok") {
		t.Fatalf("info line missing from rendered output: %q", out)
	}
	if !strings.Contains(out, "dispatch status=error") {
		t.Fatalf("error line missing from rendered output: %q", out)
	}

	if got, want := len(sink.lines), 3; got != want {
		t.Fatalf("len(sink.lines) = %d, want %d (%v)", got, want, sink.lines)
	}
	if got := sink.lines[0]; got != `cmd phase=git name=git stream=stdout b64=SGVsbG8=` {
		t.Fatalf("sink.lines[0] = %q", got)
	}

	snapshot := broker.Snapshot()
	if got, want := len(snapshot.Events), 3; got != want {
		t.Fatalf("len(snapshot.Events) = %d, want %d", got, want)
	}
}

func TestHubLogRouterWarnLevelSuppressesInfoAndKeepsErrors(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	var rendered strings.Builder
	logger := newTerminalLogger(&rendered, false)
	logger.sink = sink
	broker := hubui.NewBroker()
	router := newHubLogRouter(logger, broker, hub.LogLevelWarn)

	router.Log(`hub.auth status=ok`)
	router.Log(`hub.auth status=warn action=start err="denied"`)
	router.Log(`dispatch status=error request_id=req-1 err="boom"`)

	out := rendered.String()
	if strings.Contains(out, "hub.auth status=ok") {
		t.Fatalf("info line rendered at warn level: %q", out)
	}
	if !strings.Contains(out, "hub.auth status=warn") {
		t.Fatalf("warn line missing from rendered output: %q", out)
	}
	if !strings.Contains(out, "dispatch status=error") {
		t.Fatalf("error line missing from rendered output: %q", out)
	}

	snapshot := broker.Snapshot()
	if got, want := len(snapshot.Events), 3; got != want {
		t.Fatalf("len(snapshot.Events) = %d, want %d", got, want)
	}
}

func TestHubLogRouterInfoLevelSuppressesHighVolumeDebugLifecycleLogs(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	var rendered strings.Builder
	logger := newTerminalLogger(&rendered, false)
	logger.sink = sink
	broker := hubui.NewBroker()
	router := newHubLogRouter(logger, broker, hub.LogLevelInfo)

	router.Log(`dispatch request_id=req-1 stage=clone status=start repo=git@github.com:acme/repo.git`)
	router.Log(`dispatch status=start request_id=req-1 skill=code_for_me`)
	router.Log(`hub.auth status=ok`)

	out := rendered.String()
	if strings.Contains(out, "stage=clone status=start") {
		t.Fatalf("stage debug line rendered at info level: %q", out)
	}
	if strings.Contains(out, "dispatch status=start request_id=req-1") {
		t.Fatalf("dispatch lifecycle debug line rendered at info level: %q", out)
	}
	if !strings.Contains(out, "hub.auth status=ok") {
		t.Fatalf("info line missing from rendered output: %q", out)
	}

	if got, want := len(sink.lines), 3; got != want {
		t.Fatalf("len(sink.lines) = %d, want %d", got, want)
	}

	snapshot := broker.Snapshot()
	if got, want := len(snapshot.Events), 3; got != want {
		t.Fatalf("len(snapshot.Events) = %d, want %d", got, want)
	}
	if got, want := len(snapshot.Tasks), 1; got != want {
		t.Fatalf("len(snapshot.Tasks) = %d, want %d", got, want)
	}
	if got, want := snapshot.Tasks[0].Status, "running"; got != want {
		t.Fatalf("snapshot.Tasks[0].Status = %q, want %q", got, want)
	}
}
