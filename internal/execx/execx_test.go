package execx

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestOSRunnerRunSuccess(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-lc", "printf hello"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Stdout != "hello" {
		t.Fatalf("stdout = %q, want hello", res.Stdout)
	}
}

func TestOSRunnerRunPipesStdin(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Name:  "sh",
		Args:  []string{"-lc", "cat"},
		Stdin: "hello-from-stdin",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := res.Stdout, "hello-from-stdin"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestOSRunnerRunFailure(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-lc", "echo boom 1>&2; exit 7"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Fatalf("stderr = %q, want to contain boom", res.Stderr)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %q, want to contain stderr summary", err)
	}
}

func TestOSRunnerRunStreamEmitsLines(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	var mu sync.Mutex
	var got []string

	res, err := r.RunStream(context.Background(), Command{
		Name: "sh",
		Args: []string{"-lc", "echo out-one; echo err-one 1>&2; printf out-two"},
	}, func(stream, line string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, stream+":"+line)
	})
	if err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	if res.Stdout != "out-one\nout-two" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.Stderr != "err-one\n" {
		t.Fatalf("stderr = %q", res.Stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	slices.Sort(got)
	want := []string{"stderr:err-one", "stdout:out-one", "stdout:out-two"}
	if !slices.Equal(got, want) {
		t.Fatalf("streamed lines = %v, want %v", got, want)
	}
}

func TestSummarizeOutputTailUsesLastNonEmptyLines(t *testing.T) {
	t.Parallel()

	got := summarizeOutputTail("\nfirst\n\nsecond\nthird\n")
	want := "first | second | third"
	if got != want {
		t.Fatalf("summarizeOutputTail() = %q, want %q", got, want)
	}

	got = summarizeOutputTail("line-1\nline-2\nline-3\nline-4")
	want = "line-2 | line-3 | line-4"
	if got != want {
		t.Fatalf("summarizeOutputTail(last 3) = %q, want %q", got, want)
	}
}
