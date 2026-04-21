package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

const agentHarnessSelectionState = "needs_harness_selection"

var preferredHarnessSelectionOrder = []string{
	agentruntime.HarnessClaude,
	agentruntime.HarnessCodex,
	agentruntime.HarnessPi,
	agentruntime.HarnessAuggie,
}

type selectableAgentAuthGate struct {
	mu sync.Mutex

	baseCtx           context.Context
	runner            execx.Runner
	logf              func(string, ...any)
	runtimeConfigPath string
	initCfg           hub.InitConfig

	delegate       agentAuthGate
	delegateKey    string
	delegateConfig hub.InitConfig
}

func newSelectableAgentAuthGate(
	ctx context.Context,
	runner execx.Runner,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) *selectableAgentAuthGate {
	if ctx == nil {
		ctx = context.Background()
	}
	if runner == nil {
		runner = execx.OSRunner{}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	runtimeConfigPath := strings.TrimSpace(initCfg.RuntimeConfigPath)
	if runtimeConfigPath == "" {
		runtimeConfigPath = hub.ResolveRuntimeConfigPath("")
	}
	initCfg.RuntimeConfigPath = runtimeConfigPath

	return &selectableAgentAuthGate{
		baseCtx:           ctx,
		runner:            runner,
		logf:              logf,
		runtimeConfigPath: runtimeConfigPath,
		initCfg:           initCfg,
	}
}

func (g *selectableAgentAuthGate) Status(ctx context.Context) (hubui.AgentAuthState, error) {
	activeCfg, err := g.activeConfig()
	if err != nil {
		state := g.errorState(fmt.Sprintf("load runtime config: %v", err))
		return state, err
	}

	if runtime, ok, state := g.selectionState(activeCfg); !ok {
		return state, nil
	} else {
		delegate, err := g.resolveDelegate(runtime, activeCfg)
		if err != nil {
			state := g.errorState(fmt.Sprintf("configure agent auth: %v", err))
			return state, err
		}
		return delegate.Status(ctx)
	}
}

func (g *selectableAgentAuthGate) StartDeviceAuth(ctx context.Context) (hubui.AgentAuthState, error) {
	activeCfg, err := g.activeConfig()
	if err != nil {
		state := g.errorState(fmt.Sprintf("load runtime config: %v", err))
		return state, err
	}
	if runtime, ok, state := g.selectionState(activeCfg); !ok {
		return state, nil
	} else {
		delegate, err := g.resolveDelegate(runtime, activeCfg)
		if err != nil {
			state := g.errorState(fmt.Sprintf("configure agent auth: %v", err))
			return state, err
		}
		return delegate.StartDeviceAuth(ctx)
	}
}

func (g *selectableAgentAuthGate) Verify(ctx context.Context) (hubui.AgentAuthState, error) {
	activeCfg, err := g.activeConfig()
	if err != nil {
		state := g.errorState(fmt.Sprintf("load runtime config: %v", err))
		return state, err
	}
	if runtime, ok, state := g.selectionState(activeCfg); !ok {
		return state, nil
	} else {
		delegate, err := g.resolveDelegate(runtime, activeCfg)
		if err != nil {
			state := g.errorState(fmt.Sprintf("configure agent auth: %v", err))
			return state, err
		}
		return delegate.Verify(ctx)
	}
}

func (g *selectableAgentAuthGate) Configure(ctx context.Context, rawInput string) (hubui.AgentAuthState, error) {
	activeCfg, err := g.activeConfig()
	if err != nil {
		state := g.errorState(fmt.Sprintf("load runtime config: %v", err))
		return state, err
	}

	runtime, selectionLocked, selectionState := g.selectionState(activeCfg)
	if !selectionLocked {
		if selectionState.State == "needs_configure" {
			token, failureState, tokenErr := configureGitHubToken(
				"",
				g.runtimeConfigPath,
				activeCfg,
				rawInput,
				"GitHub token is required.",
			)
			if tokenErr != nil {
				return failureState, tokenErr
			}
			g.mu.Lock()
			g.initCfg.GitHubToken = token
			g.mu.Unlock()
			return g.Status(ctx)
		}

		selectedHarness := strings.ToLower(strings.TrimSpace(rawInput))
		if !isSupportedHarnessSelectionValue(selectedHarness) {
			state := g.harnessSelectionRequiredState("Select an agent to continue.")
			return state, fmt.Errorf("agent selection is required")
		}

		selectedRuntime, resolveErr := agentruntime.Resolve(selectedHarness, "")
		if resolveErr != nil {
			state := g.harnessSelectionRequiredState(resolveErr.Error())
			return state, resolveErr
		}

		if saveErr := hub.SaveRuntimeConfigAgentRuntime(
			g.runtimeConfigPath,
			activeCfg,
			selectedRuntime.Harness,
			selectedRuntime.Command,
		); saveErr != nil {
			state := g.harnessSelectionRequiredState(fmt.Sprintf("Save selected agent failed: %v", saveErr))
			return state, saveErr
		}

		g.mu.Lock()
		g.initCfg.AgentHarness = selectedRuntime.Harness
		g.initCfg.AgentCommand = selectedRuntime.Command
		g.resetDelegateLocked()
		g.mu.Unlock()
		return g.Status(ctx)
	}

	selectedHarness := strings.ToLower(strings.TrimSpace(rawInput))
	if isSupportedHarnessSelectionValue(selectedHarness) {
		if selectedHarness == runtime.Harness {
			return g.Status(ctx)
		}
		state, _ := g.Status(ctx)
		return state, fmt.Errorf(
			"agent harness is already configured as %q and cannot be changed in the UI",
			runtime.Harness,
		)
	}

	delegate, err := g.resolveDelegate(runtime, activeCfg)
	if err != nil {
		state := g.errorState(fmt.Sprintf("configure agent auth: %v", err))
		return state, err
	}
	return delegate.Configure(ctx, rawInput)
}

func (g *selectableAgentAuthGate) selectionState(activeCfg hub.InitConfig) (agentruntime.Runtime, bool, hubui.AgentAuthState) {
	runtime, err := hub.BoundAgentRuntime(activeCfg)
	if err == nil {
		return runtime, true, hubui.AgentAuthState{}
	}

	blocked, blockedState := githubTokenRequirementState("", g.runtimeConfigPath, activeCfg)
	if blocked {
		return agentruntime.Runtime{}, false, blockedState
	}
	return agentruntime.Runtime{}, false, g.harnessSelectionRequiredState("Select the agent logo to continue.")
}

func (g *selectableAgentAuthGate) resolveDelegate(runtime agentruntime.Runtime, activeCfg hub.InitConfig) (agentAuthGate, error) {
	key := runtime.Harness + "\x00" + runtime.Command

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.delegate != nil && g.delegateKey == key {
		return g.delegate, nil
	}

	cfg := activeCfg
	cfg.AgentHarness = runtime.Harness
	cfg.AgentCommand = runtime.Command
	if strings.TrimSpace(cfg.RuntimeConfigPath) == "" {
		cfg.RuntimeConfigPath = g.runtimeConfigPath
	}

	delegate := newConcreteAgentAuthGate(g.baseCtx, g.runner, runtime, cfg, g.logf)
	if delegate == nil {
		return nil, fmt.Errorf("unsupported agent harness %q", runtime.Harness)
	}

	g.delegate = delegate
	g.delegateKey = key
	g.delegateConfig = cfg
	return delegate, nil
}

func (g *selectableAgentAuthGate) activeConfig() (hub.InitConfig, error) {
	g.mu.Lock()
	baseCfg := g.initCfg
	runtimeConfigPath := g.runtimeConfigPath
	g.mu.Unlock()

	if strings.TrimSpace(baseCfg.RuntimeConfigPath) == "" {
		baseCfg.RuntimeConfigPath = runtimeConfigPath
	}
	activeCfg, err := effectiveHubSetupConfig(baseCfg)
	if err != nil {
		return hub.InitConfig{}, err
	}
	if strings.TrimSpace(activeCfg.RuntimeConfigPath) == "" {
		activeCfg.RuntimeConfigPath = runtimeConfigPath
	}
	return activeCfg, nil
}

func (g *selectableAgentAuthGate) harnessSelectionRequiredState(message string) hubui.AgentAuthState {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Select the agent logo to continue."
	}
	return hubui.AgentAuthState{
		Required:         true,
		Ready:            false,
		State:            agentHarnessSelectionState,
		Message:          message,
		ConfigureOptions: harnessSelectionOptions(),
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (g *selectableAgentAuthGate) errorState(message string) hubui.AgentAuthState {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "agent auth status failed"
	}
	return hubui.AgentAuthState{
		Required:  true,
		Ready:     false,
		State:     "error",
		Message:   message,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (g *selectableAgentAuthGate) resetDelegateLocked() {
	g.delegate = nil
	g.delegateKey = ""
	g.delegateConfig = hub.InitConfig{}
}

func harnessSelectionOptions() []hubui.AgentAuthOption {
	baseOrder := append([]string(nil), preferredHarnessSelectionOrder...)
	supported := agentruntime.SupportedHarnesses()
	seen := make(map[string]struct{}, len(baseOrder)+len(supported))
	ordered := make([]string, 0, len(baseOrder)+len(supported))
	for _, harness := range baseOrder {
		harness = strings.ToLower(strings.TrimSpace(harness))
		if harness == "" {
			continue
		}
		if _, ok := seen[harness]; ok {
			continue
		}
		seen[harness] = struct{}{}
		ordered = append(ordered, harness)
	}
	for _, harness := range supported {
		harness = strings.ToLower(strings.TrimSpace(harness))
		if harness == "" {
			continue
		}
		if _, ok := seen[harness]; ok {
			continue
		}
		seen[harness] = struct{}{}
		ordered = append(ordered, harness)
	}

	options := make([]hubui.AgentAuthOption, 0, len(ordered))
	for _, harness := range ordered {
		display := agentruntime.DisplayName(harness)
		options = append(options, hubui.AgentAuthOption{
			Value:       harness,
			Label:       display,
			Description: fmt.Sprintf("Bind this runtime to %s.", display),
		})
	}
	return options
}

func isSupportedHarnessSelectionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	harnesses := agentruntime.SupportedHarnesses()
	i := sort.SearchStrings(harnesses, value)
	return i < len(harnesses) && harnesses[i] == value
}
