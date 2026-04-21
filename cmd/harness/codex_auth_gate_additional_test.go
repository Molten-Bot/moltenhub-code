package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
)

func TestNewCodexAuthGateWrapperUsesDefaultRuntimeAndProbe(t *testing.T) {
	t.Parallel()

	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, agentruntime.HarnessCodex; got != want {
				t.Fatalf("probe command = %q, want %q", got, want)
			}
			if got, want := strings.Join(cmd.Args, " "), "login status"; got != want {
				t.Fatalf("probe args = %q, want %q", got, want)
			}
			return execx.Result{Stdout: "logged in using ChatGPT credentials"}, nil
		},
	}

	g := newCodexAuthGate(context.Background(), runner, "", nil)
	state, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !state.Ready || state.State != "ready" {
		t.Fatalf("Status() = %+v", state)
	}
}
