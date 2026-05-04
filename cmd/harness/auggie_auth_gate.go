package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

const (
	auggieSessionAuthEnv            = "AUGMENT_SESSION_AUTH"
	auggieConfigureCommand          = "auggie token print"
	auggieConfigurePlaceholderValue = `{"accessToken":"XXX","tenantURL":"https://YYY/","scopes":["email"]}`
)

type auggieAuthGate struct {
	configurableAgentAuthGateBase
}

type auggieSessionAuth struct {
	AccessToken string   `json:"accessToken"`
	TenantURL   string   `json:"tenantURL"`
	Scopes      []string `json:"scopes"`
}

func newAuggieAuthGate(runtimeConfigPath string, initCfg hub.InitConfig) *auggieAuthGate {
	return newAuggieAuthGateWithRunner(nil, runtimeConfigPath, initCfg)
}

func newAuggieAuthGateWithRunner(runner execx.Runner, runtimeConfigPath string, initCfg hub.InitConfig) *auggieAuthGate {
	g := &auggieAuthGate{
		configurableAgentAuthGateBase: configurableAgentAuthGateBase{
			runner:            runner,
			runtimeConfigPath: strings.TrimSpace(runtimeConfigPath),
			initCfg:           initCfg,
		},
	}
	applyAuggieConfigureUIState(&g.authState, "")

	g.mu.Lock()
	g.refreshLocked()
	g.mu.Unlock()
	return g
}

func (g *auggieAuthGate) Status(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot()
}

func (g *auggieAuthGate) StartDeviceAuth(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot()
}

func (g *auggieAuthGate) Verify(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot()
}

func (g *auggieAuthGate) Configure(ctx context.Context, rawInput string) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	g.mu.Lock()
	g.refreshLocked()
	if g.authState.ready {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, nil
	}
	configureCommand := strings.TrimSpace(g.authState.configureCommand)
	initCfg := g.initCfg
	runtimeConfigPath := g.runtimeConfigPath
	runner := g.runner
	g.mu.Unlock()

	if configureCommand == claudeGitHubConfigureCommand {
		return configureGitHubTokenAndApply(
			ctx,
			agentruntime.HarnessAuggie,
			runtimeConfigPath,
			initCfg,
			runner,
			rawInput,
			func(state hubui.AgentAuthState, err error) (hubui.AgentAuthState, error) {
				g.mu.Lock()
				g.authState.applySnapshot(state)
				snap := g.snapshotLocked()
				g.mu.Unlock()
				return snap, err
			},
			func(token string) (hubui.AgentAuthState, error) {
				g.mu.Lock()
				g.initCfg.GitHubToken = token
				g.refreshLocked()
				snap := g.snapshotLocked()
				g.mu.Unlock()
				return snap, nil
			},
		)
	}

	canonical, err := normalizeAuggieSessionAuth(rawInput)
	if err != nil {
		g.mu.Lock()
		applyAuggieConfigureUIState(&g.authState, fmt.Sprintf("Auggie session auth is invalid: %v.", err))
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}
	if err := hub.SaveRuntimeConfigAuggieAuth(runtimeConfigPath, initCfg, canonical); err != nil {
		g.mu.Lock()
		applyAuggieConfigureUIState(&g.authState, fmt.Sprintf("save auggie config.json: %v", err))
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}
	if err := os.Setenv(auggieSessionAuthEnv, canonical); err != nil {
		g.mu.Lock()
		applyAuggieConfigureUIState(&g.authState, fmt.Sprintf("set %s: %v", auggieSessionAuthEnv, err))
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	g.mu.Lock()
	g.initCfg.AugmentSessionAuth = canonical
	g.refreshLocked()
	snap := g.snapshotLocked()
	g.mu.Unlock()
	return snap, nil
}

func (g *auggieAuthGate) refreshLocked() {
	if g == nil {
		return
	}

	applyAuggieConfigureUIState(&g.authState, "")

	configuredSessionAuth, source := firstConfiguredAuggieSessionAuth(g.runtimeConfigPath, g.initCfg)
	if configuredSessionAuth == "" {
		g.authState.message = ""
		return
	}

	canonicalSessionAuth, err := normalizeAuggieSessionAuth(configuredSessionAuth)
	if err != nil {
		g.authState.message = fmt.Sprintf("Auggie session auth from %s is invalid: %v.", source, err)
		return
	}
	if source != "environment" {
		if err := os.Setenv(auggieSessionAuthEnv, canonicalSessionAuth); err != nil {
			g.authState.message = fmt.Sprintf("set %s: %v", auggieSessionAuthEnv, err)
			return
		}
	}
	g.initCfg.AugmentSessionAuth = canonicalSessionAuth

	githubToken, blocked := applyGitHubTokenRequirementState(context.Background(), g.runner, &g.authState, agentruntime.HarnessAuggie, g.runtimeConfigPath, g.initCfg)
	if blocked {
		return
	}

	g.initCfg.GitHubToken = strings.TrimSpace(githubToken)
	g.authState.setReady("Auggie session auth and GitHub token are ready.")
}

func (g *auggieAuthGate) snapshotLocked() hubui.AgentAuthState {
	return g.configurableAgentAuthGateBase.snapshotLocked(agentruntime.HarnessAuggie)
}

func (g *auggieAuthGate) refreshAndSnapshot() (hubui.AgentAuthState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked()
	return g.snapshotLocked(), nil
}

func applyAuggieConfigureUIState(state *configurableAgentAuthState, message string) {
	state.setConfigureUI(message, auggieConfigureCommand, auggieConfigurePlaceholderValue, nil)
}

func firstConfiguredAuggieSessionAuth(runtimeConfigPath string, initCfg hub.InitConfig) (value string, source string) {
	if env := strings.TrimSpace(os.Getenv(auggieSessionAuthEnv)); env != "" {
		return env, "environment"
	}
	if init := strings.TrimSpace(initCfg.AugmentSessionAuth); init != "" {
		return init, "init config"
	}
	if persisted := hub.ReadRuntimeConfigString(runtimeConfigPath, "augment_session_auth", "augmentSessionAuth", "AUGMENT_SESSION_AUTH"); persisted != "" {
		return persisted, "runtime config"
	}
	return "", ""
}

func normalizeAuggieSessionAuth(rawInput string) (string, error) {
	parsed, err := decodeAuggieSessionAuth(rawInput)
	if err != nil {
		return "", err
	}

	parsed.AccessToken = strings.TrimSpace(parsed.AccessToken)
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("accessToken is required")
	}

	parsed.TenantURL = strings.TrimSpace(parsed.TenantURL)
	if parsed.TenantURL == "" {
		return "", fmt.Errorf("tenantURL is required")
	}
	parsedURL, err := url.Parse(parsed.TenantURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", fmt.Errorf("tenantURL must be an absolute URL")
	}
	if !strings.EqualFold(parsedURL.Scheme, "https") {
		return "", fmt.Errorf("tenantURL must use https")
	}

	scopes := make([]string, 0, len(parsed.Scopes))
	hasEmailScope := false
	for _, scope := range parsed.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		scopes = append(scopes, scope)
		if strings.EqualFold(scope, "email") {
			hasEmailScope = true
		}
	}
	if len(scopes) == 0 {
		return "", fmt.Errorf("scopes must include at least one value")
	}
	if !hasEmailScope {
		return "", fmt.Errorf("scopes must include \"email\"")
	}
	parsed.Scopes = scopes

	encoded, err := json.Marshal(parsed)
	if err != nil {
		return "", fmt.Errorf("encode augment session auth: %w", err)
	}
	return string(encoded), nil
}

func decodeAuggieSessionAuth(rawInput string) (auggieSessionAuth, error) {
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		return auggieSessionAuth{}, fmt.Errorf("session auth JSON is required")
	}

	var parsed auggieSessionAuth
	if err := decodeJSONStrictOrWrappedString(rawInput, &parsed); err == nil {
		return parsed, nil
	}

	return auggieSessionAuth{}, fmt.Errorf("expected JSON object with accessToken, tenantURL, and scopes")
}
