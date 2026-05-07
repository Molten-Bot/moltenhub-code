package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

const (
	opencodeAuthProbeTimeout       = 12 * time.Second
	opencodeConfigureCommand       = "opencode auth login"
	opencodeConfigurePlaceholder   = "Run opencode auth login or configure provider credentials, then click Done."
	opencodeConfigureMessage       = "Opencode provider auth is required. Run `opencode auth login` or configure provider credentials, then click Done."
	opencodeProviderAuthReady      = "Opencode provider auth is ready."
	opencodeAuthFileRelativePath   = ".local/share/opencode/auth.json"
	opencodeAuthFileReadyMessage   = "Opencode auth is ready via ~/.local/share/opencode/auth.json."
	opencodeProviderEnvReadyFormat = "Opencode provider auth is ready via %s."
)

var opencodeProviderEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"AZURE_OPENAI_API_KEY",
	"OPENROUTER_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_GENERATIVE_AI_API_KEY",
	"GOOGLE_API_KEY",
	"GROQ_API_KEY",
	"CEREBRAS_API_KEY",
	"XAI_API_KEY",
	"MISTRAL_API_KEY",
	"ZAI_API_KEY",
	"AWS_PROFILE",
	"AWS_ACCESS_KEY_ID",
}

type opencodeAuthGate struct {
	configurableAgentAuthGateBase

	command string
	logf    func(string, ...any)
}

func newOpencodeAuthGate(runtimeConfigPath string, initCfg hub.InitConfig) *opencodeAuthGate {
	return newOpencodeAuthGateWithRuntime(nil, "", runtimeConfigPath, initCfg, nil)
}

func newOpencodeAuthGateWithRuntime(
	runner execx.Runner,
	command string,
	runtimeConfigPath string,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) *opencodeAuthGate {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = agentruntime.HarnessOpencode
	}

	g := &opencodeAuthGate{
		configurableAgentAuthGateBase: configurableAgentAuthGateBase{
			runner:            runner,
			runtimeConfigPath: strings.TrimSpace(runtimeConfigPath),
			initCfg:           initCfg,
		},
		command: command,
		logf:    logf,
	}
	applyOpencodeConfigureUIState(&g.authState, "")
	g.mu.Lock()
	g.refreshLocked(context.Background())
	g.mu.Unlock()
	return g
}

func (g *opencodeAuthGate) Status(ctx context.Context) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot(ctx)
}

func (g *opencodeAuthGate) StartDeviceAuth(ctx context.Context) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot(ctx)
}

func (g *opencodeAuthGate) Verify(ctx context.Context) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot(ctx)
}

func (g *opencodeAuthGate) Configure(ctx context.Context, rawInput string) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	g.mu.Lock()
	g.refreshLocked(ctx)
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
			agentruntime.HarnessOpencode,
			runtimeConfigPath,
			initCfg,
			runner,
			rawInput,
			func(state web.AgentAuthState, err error) (web.AgentAuthState, error) {
				g.mu.Lock()
				g.authState.applySnapshot(state)
				snap := g.snapshotLocked()
				g.mu.Unlock()
				return snap, err
			},
			func(token string) (web.AgentAuthState, error) {
				g.mu.Lock()
				g.initCfg.GitHubToken = token
				g.refreshLocked(ctx)
				snap := g.snapshotLocked()
				g.mu.Unlock()
				return snap, nil
			},
		)
	}

	return g.refreshAndSnapshot(ctx)
}

func (g *opencodeAuthGate) refreshLocked(ctx context.Context) {
	if g == nil {
		return
	}

	applyOpencodeConfigureUIState(&g.authState, "")

	githubToken, blocked := applyGitHubTokenRequirementState(ctx, g.runner, &g.authState, agentruntime.HarnessOpencode, g.runtimeConfigPath, g.initCfg)
	if blocked {
		return
	}
	g.initCfg.GitHubToken = strings.TrimSpace(githubToken)

	if envVar := firstConfiguredOpencodeProviderEnv(); envVar != "" {
		g.authState.setReady(fmt.Sprintf(opencodeProviderEnvReadyFormat, envVar))
		return
	}
	if opencodeAuthFileReady() {
		g.authState.setReady(opencodeAuthFileReadyMessage)
		return
	}

	ready, probeMessage := g.probeAuthList(ctx)
	if ready {
		g.authState.setReady(firstNonEmptyString(probeMessage, opencodeProviderAuthReady))
		return
	}
	if probeMessage != "" {
		g.authState.message = probeMessage
	}
}

func (g *opencodeAuthGate) snapshotLocked() web.AgentAuthState {
	return g.configurableAgentAuthGateBase.snapshotLocked(agentruntime.HarnessOpencode)
}

func (g *opencodeAuthGate) refreshAndSnapshot(ctx context.Context) (web.AgentAuthState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked(ctx)
	return g.snapshotLocked(), nil
}

func (g *opencodeAuthGate) probeAuthList(ctx context.Context) (bool, string) {
	if g == nil || g.runner == nil || strings.TrimSpace(g.command) == "" {
		return false, ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, opencodeAuthProbeTimeout)
	defer cancel()

	res, err := g.runner.Run(probeCtx, execx.Command{
		Name: strings.TrimSpace(g.command),
		Args: []string{"auth", "list"},
	})
	combined := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if err != nil {
		return false, opencodeProbeConfigureMessage(err)
	}
	if hasOpencodeAuthListCredentials(combined) {
		return true, opencodeProviderAuthReady
	}
	return false, ""
}

func applyOpencodeConfigureUIState(state *configurableAgentAuthState, message string) {
	state.setConfigureUI(firstNonEmptyString(message, opencodeConfigureMessage), opencodeConfigureCommand, opencodeConfigurePlaceholder, nil)
}

func firstConfiguredOpencodeProviderEnv() string {
	for _, envVar := range opencodeProviderEnvVars {
		envVar = strings.TrimSpace(envVar)
		if envVar == "" {
			continue
		}
		if strings.TrimSpace(os.Getenv(envVar)) != "" {
			return envVar
		}
	}
	return ""
}

func opencodeAuthFileReady() bool {
	for _, path := range opencodeAuthFileCandidates() {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() <= 0 {
			continue
		}
		return true
	}
	return false
}

func opencodeAuthFileCandidates() []string {
	var out []string
	if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
		out = append(out, filepath.Join(dataHome, "opencode", "auth.json"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		out = append(out, filepath.Join(home, opencodeAuthFileRelativePath))
	}
	return out
}

func hasOpencodeAuthListCredentials(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{
		"no auth",
		"no provider",
		"not authenticated",
		"not logged in",
		"login required",
		"empty",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func opencodeProbeConfigureMessage(err error) string {
	if err == nil {
		return ""
	}
	errText := strings.ToLower(err.Error())
	if strings.Contains(errText, "executable file not found") || strings.Contains(errText, "no such file or directory") {
		return "Opencode CLI was not found. Install it with `npm install -g opencode-ai`, then click Done."
	}
	return ""
}
