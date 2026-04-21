package main

import (
	"context"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

type agentAuthGate interface {
	Status(context.Context) (hubui.AgentAuthState, error)
	StartDeviceAuth(context.Context) (hubui.AgentAuthState, error)
	Verify(context.Context) (hubui.AgentAuthState, error)
	Configure(context.Context, string) (hubui.AgentAuthState, error)
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
	case agentruntime.HarnessAuggie:
		return newAuggieAuthGateWithRunner(runner, initCfg.RuntimeConfigPath, initCfg)
	case agentruntime.HarnessPi:
		return newPiAuthGateWithRuntime(runner, runtime.Command, initCfg.RuntimeConfigPath, initCfg, logf)
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
