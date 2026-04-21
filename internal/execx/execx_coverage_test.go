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
		Name: "sh",
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
		Name: "sh",
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

func TestLineEmitterFlushTrimsTrailingCarriageReturn(t *testing.T) {
	t.Parallel()

	var gotStream, gotLine string
	w := &lineEmitter{
		stream: "stdout",
		handler: func(stream, line string) {
			gotStream = stream
			gotLine = line
		},
	}
	w.pending.WriteString("line-with-cr\r")
	w.Flush()

	if got, want := gotStream, "stdout"; got != want {
		t.Fatalf("stream = %q, want %q", got, want)
	}
	if got, want := gotLine, "line-with-cr"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	if got := w.pending.Len(); got != 0 {
		t.Fatalf("pending len = %d, want 0", got)
	}
}
