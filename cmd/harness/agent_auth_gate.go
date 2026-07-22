package main

import (
	"context"

	"github.com/Molten-Bot/agent_00/internal/agentruntime"
	"github.com/Molten-Bot/agent_00/internal/execx"
	"github.com/Molten-Bot/agent_00/internal/hub"
	"github.com/Molten-Bot/agent_00/internal/web"
)

type agentAuthGate interface {
	Status(context.Context) (web.AgentAuthState, error)
	StartDeviceAuth(context.Context) (web.AgentAuthState, error)
	Verify(context.Context) (web.AgentAuthState, error)
	Configure(context.Context, string) (web.AgentAuthState, error)
}

func newConcreteAgentAuthGate(
	ctx context.Context,
	runner execx.Runner,
	runtime agentruntime.Runtime,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) agentAuthGate {
	switch runtime.Harness {
	case agentruntime.HarnessCodex:
		return newCodexAuthGateWithConfig(
			ctx,
			runner,
			runtime.Command,
			initCfg.RuntimeConfigPath,
			initCfg,
			true,
			logf,
		)
	case agentruntime.HarnessClaude:
		return newClaudeAuthGateWithContextAndConfigAndRunner(
			ctx,
			runner,
			runtime.Command,
			initCfg.RuntimeConfigPath,
			initCfg,
			logf,
		)
	default:
		return nil
	}
}

func newAgentAuthGate(
	ctx context.Context,
	runner execx.Runner,
	_ agentruntime.Runtime,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) agentAuthGate {
	return newSelectableAgentAuthGate(ctx, runner, initCfg, logf)
}
