package main

import (
	"context"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/hubui"
)

type agentAuthGate interface {
	Status(context.Context) (hubui.AgentAuthState, error)
	StartDeviceAuth(context.Context) (hubui.AgentAuthState, error)
	Verify(context.Context) (hubui.AgentAuthState, error)
}

func newAgentAuthGate(
	ctx context.Context,
	runner execx.Runner,
	runtime agentruntime.Runtime,
	logf func(string, ...any),
) agentAuthGate {
	switch runtime.Harness {
	case agentruntime.HarnessCodex:
		return newCodexAuthGate(ctx, runner, runtime.Command, logf)
	case agentruntime.HarnessClaude:
		return newClaudeAuthGate(runtime.Command)
	default:
		return nil
	}
}
