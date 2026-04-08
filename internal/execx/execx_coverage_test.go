package execx

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOSRunnerRunUsesDirAndErrorWithoutOutputDetail(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	r := OSRunner{}

	res, err := r.Run(context.Background(), Command{
		Dir:  workingDir,
		Name: "bash",
		Args: []string{"-lc", "pwd"},
	})
	if err != nil {
		t.Fatalf("Run(pwd) error = %v", err)
	}
	gotPwd := strings.TrimSpace(res.Stdout)
	if gotPwd != filepath.Clean(workingDir) {
		t.Fatalf("pwd stdout = %q, want %q", gotPwd, filepath.Clean(workingDir))
	}

	_, err = r.Run(context.Background(), Command{
		Name: "bash",
		Args: []string{"-lc", "exit 5"},
	})
	if err == nil {
		t.Fatal("Run(exit 5) error = nil, want non-nil")
	}
	if strings.Contains(err.Error(), "(") {
		t.Fatalf("Run(exit 5) error = %q, want no failure-detail suffix", err.Error())
	}
}

func TestSummarizeOutputTailHandlesBlankAndCRInput(t *testing.T) {
	t.Parallel()

	if got := summarizeOutputTail(" \n\r\t "); got != "" {
		t.Fatalf("summarizeOutputTail(blank) = %q, want empty", got)
	}

	if got, want := summarizeOutputTail("one\r\ntwo\rthree"), "one | two | three"; got != want {
		t.Fatalf("summarizeOutputTail(CR input) = %q, want %q", got, want)
	}
}

func TestLineEmitterFlushWithNoHandlerAndNoPending(t *testing.T) {
	t.Parallel()

	w := &lineEmitter{}
	w.pending.WriteString("dangling")
	w.Flush()
	if got := w.pending.String(); got != "dangling" {
		t.Fatalf("Flush() with nil handler mutated pending = %q", got)
	}

	w.handler = func(string, string) {}
	w.pending.Reset()
	w.Flush()
}
