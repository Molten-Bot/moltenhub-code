package hub

import (
	"fmt"
	"strings"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
)

const unboundAgentRuntimeErrorMessage = "agent harness is not configured; complete agent selection in the UI"

// BoundAgentRuntime resolves the configured one-agent runtime for this app instance.
func BoundAgentRuntime(initCfg InitConfig) (agentruntime.Runtime, error) {
	harness := strings.ToLower(strings.TrimSpace(initCfg.AgentHarness))
	if harness == "" {
		return agentruntime.Runtime{}, fmt.Errorf(unboundAgentRuntimeErrorMessage)
	}
	return agentruntime.Resolve(harness, strings.TrimSpace(initCfg.AgentCommand))
}

func hasBoundAgentRuntime(initCfg InitConfig) bool {
	return strings.TrimSpace(initCfg.AgentHarness) != ""
}

// ApplyBoundAgentRuntime enforces single-agent binding for one run request when
// this runtime already has a bound agent harness. If the runtime is still
// unbound, the request config is passed through unchanged.
func ApplyBoundAgentRuntime(runCfg config.Config, initCfg InitConfig) (config.Config, error) {
	if !hasBoundAgentRuntime(initCfg) {
		return runCfg, nil
	}

	runtime, err := BoundAgentRuntime(initCfg)
	if err != nil {
		return runCfg, err
	}

	if overrideHarness := strings.ToLower(strings.TrimSpace(runCfg.AgentHarness)); overrideHarness != "" && overrideHarness != runtime.Harness {
		return runCfg, fmt.Errorf(
			"run config agentHarness %q conflicts with bound agent %q",
			strings.TrimSpace(runCfg.AgentHarness),
			runtime.Harness,
		)
	}
	if overrideCommand := strings.TrimSpace(runCfg.AgentCommand); overrideCommand != "" && overrideCommand != runtime.Command {
		return runCfg, fmt.Errorf(
			"run config agentCommand %q conflicts with bound command %q for agent %q",
			overrideCommand,
			runtime.Command,
			runtime.Harness,
		)
	}

	runCfg.AgentHarness = runtime.Harness
	runCfg.AgentCommand = runtime.Command
	return runCfg, nil
}
