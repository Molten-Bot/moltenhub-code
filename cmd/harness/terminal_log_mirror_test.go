package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskLogSubdirForLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "request_id_local",
			line: "dispatch request_id=local-1712345678-000007 stage=codex status=start",
			want: filepath.Join("local", "1712345678", "000007"),
		},
		{
			name: "session_id",
			line: "session=task-003 stage=clone status=ok",
			want: filepath.Join("task", "003"),
		},
		{
			name: "request_id_preferred_over_session",
			line: "dispatch request_id=local-1700000000-000001 session=task-020 status=start",
			want: filepath.Join("local", "1700000000", "000001"),
		},
		{
			name: "fallback_main",
			line: "stage=preflight status=ok",
			want: fallbackLogSubdir,
		},
		{
			name: "sanitize_identifier_parts",
			line: "dispatch request_id=local-17/abc-00*01 status=start",
			want: filepath.Join("local", "17_abc", "00_01"),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := taskLogSubdirForLine(tt.line); got != tt.want {
				t.Fatalf("taskLogSubdirForLine(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestTaskLogMirrorWritesAggregateAndTaskLogs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), ".log")
	mirror, err := newTaskLogMirror(root)
	if err != nil {
		t.Fatalf("newTaskLogMirror() error = %v", err)
	}

	lines := []string{
		"dispatch request_id=local-1712345678-000111 stage=codex status=start",
		"session=task-005 stage=clone status=ok",
		"status=ok workspace=/tmp/workspace branch=codex/fix",
	}
	for _, line := range lines {
		mirror.WriteLine(line)
	}

	if err := mirror.Close(); err != nil {
		t.Fatalf("mirror.Close() error = %v", err)
	}

	assertLogFileContent(t, filepath.Join(root, logFileName), lines)
	assertLogFileContent(t, filepath.Join(root, "local", "1712345678", "000111", logFileName), []string{lines[0]})
	assertLogFileContent(t, filepath.Join(root, "task", "005", logFileName), []string{lines[1]})
	assertLogFileContent(t, filepath.Join(root, fallbackLogSubdir, logFileName), []string{lines[2]})
}

func TestTaskLogMirrorEvictsOldTaskFilesAndContinuesWriting(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), ".log")
	mirror, err := newTaskLogMirror(root)
	if err != nil {
		t.Fatalf("newTaskLogMirror() error = %v", err)
	}

	total := maxOpenTaskLogFiles + 16
	for i := 0; i < total; i++ {
		mirror.WriteLine(fmt.Sprintf("dispatch request_id=req-%03d status=start", i))
	}

	if err := mirror.Close(); err != nil {
		t.Fatalf("mirror.Close() error = %v", err)
	}

	assertLogFileContent(
		t,
		filepath.Join(root, "req", "000", logFileName),
		[]string{"dispatch request_id=req-000 status=start"},
	)
	assertLogFileContent(
		t,
		filepath.Join(root, "req", fmt.Sprintf("%03d", total-1), logFileName),
		[]string{fmt.Sprintf("dispatch request_id=req-%03d status=start", total-1)},
	)
}

func assertLogFileContent(t *testing.T, path string, want []string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	expected := ""
	for _, line := range want {
		expected += line + "\n"
	}
	if string(content) != expected {
		t.Fatalf("content of %s = %q, want %q", path, string(content), expected)
	}
}
