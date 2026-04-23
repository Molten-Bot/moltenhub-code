package execx

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestCommandFailureDetailFallsBackToStdout(t *testing.T) {
	t.Parallel()

	got := commandFailureDetail(Result{
		Stdout: "first\nsecond\n",
		Stderr: "\n\n",
	})
	if got != "first | second" {
		t.Fatalf("commandFailureDetail() = %q, want %q", got, "first | second")
	}
}

func TestSummarizeOutputTailTruncatesLongSummary(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 500)
	got := summarizeOutputTail(long)
	if len(got) != 320 {
		t.Fatalf("len(summarizeOutputTail()) = %d, want 320", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("summarizeOutputTail() = %q, want suffix ...", got)
	}
}

func TestLineEmitterWriteAndFlushEdgeCases(t *testing.T) {
	t.Parallel()

	var nilEmitter *lineEmitter
	if n, err := nilEmitter.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("(*lineEmitter)(nil).Write() = (%d, %v), want (3, nil)", n, err)
	}
	nilEmitter.Flush()

	got := make([]string, 0, 3)
	emitter := &lineEmitter{
		stream: "stderr",
		handler: func(stream, line string) {
			got = append(got, stream+":"+line)
		},
	}
	if n, err := emitter.Write([]byte("one\r\ntwo")); err != nil || n != len("one\r\ntwo") {
		t.Fatalf("Write() = (%d, %v)", n, err)
	}
	emitter.Flush()

	want := []string{"stderr:one", "stderr:two"}
	if !slices.Equal(got, want) {
		t.Fatalf("emitted lines = %v, want %v", got, want)
	}
}

func TestOSRunnerRunFailureUsesStdoutSummaryWhenStderrEmpty(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	_, err := r.Run(context.Background(), Command{
		Name: "bash",
		Args: []string{"-lc", "echo from-stdout; exit 9"},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "from-stdout") {
		t.Fatalf("Run() error = %q, want stdout summary", err.Error())
	}
}

func TestOSRunnerRunAcceptsNilContext(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	res, err := r.Run(nil, Command{
		Name: "sh",
		Args: []string{"-lc", "printf ok"},
	})
	if err != nil {
		t.Fatalf("Run(nil context) error = %v", err)
	}
	if got, want := res.Stdout, "ok"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
