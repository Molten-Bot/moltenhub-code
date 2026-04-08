package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewDefaultTaskLogMirrorAndDefaultTerminalLogger(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tmp, err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	mirror, err := newDefaultTaskLogMirror()
	if err != nil {
		t.Fatalf("newDefaultTaskLogMirror() error = %v", err)
	}
	mirror.WriteLine("stage=preflight status=ok")
	if err := mirror.Close(); err != nil {
		t.Fatalf("mirror.Close() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, logDirectoryName, logFileName)); err != nil {
		t.Fatalf("aggregate log stat error = %v", err)
	}

	logger := newDefaultTerminalLogger()
	logger.Capture("session=task-001 status=ok")
	logger.Close()
}

func TestTerminalLoggerPrintfCloseAndTerminalChecks(t *testing.T) {
	t.Parallel()

	if isTerminalFile(nil) {
		t.Fatal("isTerminalFile(nil) = true, want false")
	}
	_ = isTerminalFile(os.Stdout)

	var out strings.Builder
	logger := newTerminalLogger(&out, false)
	logger.Printf("hello %s", "world")
	logger.Close()
	logger.Close()

	if got := out.String(); !strings.Contains(got, "hello world") {
		t.Fatalf("terminal output = %q, want hello world", got)
	}
}

func TestDecodeCommandLogLineHandlesInvalidAndEmptyPayloads(t *testing.T) {
	t.Parallel()

	if _, handled, drop := decodeCommandLogLine("cmd phase=clone b64=%%%"); handled || drop {
		t.Fatal("decodeCommandLogLine(invalid b64) should not be handled")
	}
	if _, handled, drop := decodeCommandLogLine("cmd phase=clone b64=IA=="); !handled || !drop {
		t.Fatal("decodeCommandLogLine(blank decoded payload) should be dropped")
	}
}

func TestTerminalLoggerProgressRenderingInTTYMode(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	logger := newTerminalLogger(&out, true)
	logger.Print("stage=claude status=running elapsed_s=3")
	logger.Print("stage=claude status=running elapsed_s=5")
	logger.Print("stage=git status=ok")
	logger.Close()

	text := out.String()
	if !strings.Contains(text, "claude running... 3s") || !strings.Contains(text, "claude running... 5s") {
		t.Fatalf("tty progress output missing compact heartbeat lines: %q", text)
	}
	if !strings.Contains(text, "stage=git status=ok") {
		t.Fatalf("tty output missing non-progress line: %q", text)
	}
}

func TestWriteSinkLockedWithNilSinkAndNilLogger(t *testing.T) {
	t.Parallel()

	var logger *terminalLogger
	logger.Printf("ignored")
	logger.Print("ignored")
	logger.Capture("ignored")
	logger.Close()

	l := newTerminalLogger(io.Discard, false)
	l.writeSinkLocked("line")
}
