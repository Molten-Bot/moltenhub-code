package hub

import (
	"fmt"
	"strings"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/config"
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

// ApplyBoundAgentRuntime enforces single-agent binding for one run request.
func ApplyBoundAgentRuntime(runCfg config.Config, initCfg InitConfig) (config.Config, error) {
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
