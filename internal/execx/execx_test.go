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
		Name: "bash",
		Args: []string{"-lc", "printf hello"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Stdout != "hello" {
		t.Fatalf("stdout = %q, want hello", res.Stdout)
	}
}

func TestOSRunnerRunFailure(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Name: "bash",
		Args: []string{"-lc", "echo boom 1>&2; exit 7"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Fatalf("stderr = %q, want to contain boom", res.Stderr)
	}
}

func TestOSRunnerRunStreamEmitsLines(t *testing.T) {
	t.Parallel()

	r := OSRunner{}
	var mu sync.Mutex
	var got []string

	res, err := r.RunStream(context.Background(), Command{
		Name: "bash",
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
