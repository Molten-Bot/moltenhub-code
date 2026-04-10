package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

const piProviderAuthField = "pi_provider_auth"
const piAuthProbeTimeout = 20 * time.Second
const piAuthProbePrompt = "Reply with OK."
const openRouterProviderSetupDocURL = "https://raw.githubusercontent.com/Dicklesworthstone/pi_agent_rust/refs/heads/main/docs/provider-openrouter-setup.json"

type piProviderOption struct {
	EnvVar      string
	Label       string
	Description string
}

var piProviderOptions = []piProviderOption{
	{EnvVar: "ANTHROPIC_API_KEY", Label: "Anthropic Claude API key", Description: "Use Anthropic Claude with an API key."},
	{EnvVar: "ANTHROPIC_OAUTH_TOKEN", Label: "Anthropic OAuth token", Description: "Use Anthropic Claude with an OAuth token instead of an API key."},
	{EnvVar: "OPENAI_API_KEY", Label: "OpenAI GPT API key", Description: "Use OpenAI GPT models via API key."},
	{EnvVar: "AZURE_OPENAI_API_KEY", Label: "Azure OpenAI API key", Description: "Azure OpenAI API key."},
	{EnvVar: "AZURE_OPENAI_BASE_URL", Label: "Azure OpenAI base URL", Description: "Azure OpenAI base URL such as https://{resource}.openai.azure.com/openai/v1."},
	{EnvVar: "AZURE_OPENAI_RESOURCE_NAME", Label: "Azure OpenAI resource name", Description: "Azure OpenAI resource name instead of a base URL."},
	{EnvVar: "AZURE_OPENAI_API_VERSION", Label: "Azure OpenAI API version", Description: "Azure OpenAI API version, for example v1."},
	{EnvVar: "AZURE_OPENAI_DEPLOYMENT_NAME_MAP", Label: "Azure OpenAI deployment map", Description: "Comma-separated model=deployment mapping for Azure OpenAI."},
	{EnvVar: "GEMINI_API_KEY", Label: "Google Gemini API key", Description: "Use Google Gemini with an API key."},
	{EnvVar: "GROQ_API_KEY", Label: "Groq API key", Description: "Use Groq with an API key."},
	{EnvVar: "CEREBRAS_API_KEY", Label: "Cerebras API key", Description: "Use Cerebras with an API key."},
	{EnvVar: "XAI_API_KEY", Label: "xAI Grok API key", Description: "Use xAI Grok with an API key."},
	{EnvVar: "OPENROUTER_API_KEY", Label: "OpenRouter API key", Description: "Use an OpenRouter API key (sk-or-vN-...), not a Personal Access Token."},
	{EnvVar: "AI_GATEWAY_API_KEY", Label: "Vercel AI Gateway API key", Description: ""},
	{EnvVar: "ZAI_API_KEY", Label: "ZAI API key", Description: "Use ZAI with an API key."},
	{EnvVar: "MISTRAL_API_KEY", Label: "Mistral API key", Description: "Use Mistral with an API key."},
	{EnvVar: "MINIMAX_API_KEY", Label: "MiniMax API key", Description: "Use MiniMax with an API key."},
	{EnvVar: "OPENCODE_API_KEY", Label: "OpenCode Zen/OpenCode Go API key", Description: "Use OpenCode Zen/OpenCode Go with an API key."},
	{EnvVar: "KIMI_API_KEY", Label: "Kimi For Coding API key", Description: "Use Kimi For Coding with an API key."},
	{EnvVar: "AWS_PROFILE", Label: "AWS profile", Description: "AWS profile name for Amazon Bedrock."},
	{EnvVar: "AWS_ACCESS_KEY_ID", Label: "AWS access key ID", Description: "AWS access key for Amazon Bedrock."},
	{EnvVar: "AWS_SECRET_ACCESS_KEY", Label: "AWS secret access key", Description: "AWS secret key for Amazon Bedrock."},
	{EnvVar: "AWS_BEARER_TOKEN_BEDROCK", Label: "AWS Bedrock bearer token", Description: "Bedrock API key bearer token."},
	{EnvVar: "AWS_REGION", Label: "AWS region", Description: "AWS region for Amazon Bedrock, for example us-east-1."},
	{EnvVar: "PI_CODING_AGENT_DIR", Label: "PI session storage directory", Description: "Override the PI session storage directory."},
	{EnvVar: "PI_PACKAGE_DIR", Label: "PI package directory", Description: "Override the PI package directory for Nix or Guix store paths."},
	{EnvVar: "PI_OFFLINE", Label: "PI offline mode", Description: "Disable PI startup network operations with 1, true, or yes."},
	{EnvVar: "PI_SHARE_VIEWER_URL", Label: "PI share viewer URL", Description: "Override the PI /share viewer base URL."},
	{EnvVar: "PI_AI_ANTIGRAVITY_VERSION", Label: "PI Antigravity version", Description: "Override the Antigravity user-agent version."},
}

var piProviderOptionsByEnv = func() map[string]piProviderOption {
	out := make(map[string]piProviderOption, len(piProviderOptions))
	for _, option := range piProviderOptions {
		out[option.EnvVar] = option
	}
	return out
}()

type piProviderAuth struct {
	EnvVar string `json:"env_var"`
	Value  string `json:"value"`
}

type piAuthGate struct {
	mu sync.Mutex

	runner  execx.Runner
	command string
	logf    func(string, ...any)

	runtimeConfigPath string
	initCfg           hub.InitConfig

	required bool
	ready    bool
	state    string
	message  string

	configureOptions []hubui.AgentAuthOption
	updatedAt        time.Time
	validatedAuth    string
}

func newPiAuthGate(runtimeConfigPath string, initCfg hub.InitConfig) *piAuthGate {
	return newPiAuthGateWithRuntime(nil, "", runtimeConfigPath, initCfg, nil)
}

func newPiAuthGateWithRuntime(
	runner execx.Runner,
	command string,
	runtimeConfigPath string,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) *piAuthGate {
	if runner == nil {
		runner = execx.OSRunner{}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = agentruntime.HarnessPi
	}

	g := &piAuthGate{
		runner:            runner,
		command:           command,
		logf:              logf,
		runtimeConfigPath: strings.TrimSpace(runtimeConfigPath),
		initCfg:           initCfg,
		required:          true,
		state:             "needs_configure",
		message:           "Select a PI provider, and supply the token.",
		configureOptions:  piAgentAuthOptions(),
		updatedAt:         time.Now().UTC(),
	}
	g.mu.Lock()
	g.refreshLocked()
	g.mu.Unlock()
	return g
}

func (g *piAuthGate) Status(ctx context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}
	return g.refreshAndSnapshot(ctx)
}

func (g *piAuthGate) StartDeviceAuth(ctx context.Context) (hubui.AgentAuthState, error) {
	return g.refreshAndSnapshot(ctx)
}

func (g *piAuthGate) Verify(ctx context.Context) (hubui.AgentAuthState, error) {
	return g.refreshAndSnapshot(ctx)
}

func (g *piAuthGate) Configure(ctx context.Context, rawInput string) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	canonical, err := normalizePiProviderAuth(rawInput)
	if err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("PI provider auth is invalid: %v.", err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	if err := hub.SaveRuntimeConfigPiProviderAuth(g.runtimeConfigPath, g.initCfg, canonical); err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("save pi config.json: %v", err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	auth, err := decodePiProviderAuth(canonical)
	if err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("PI provider auth is invalid: %v.", err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}
	if err := os.Setenv(auth.EnvVar, auth.Value); err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("set %s: %v", auth.EnvVar, err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}
	if err := annotatePiProviderProbeError(g.probe(ctx), auth.EnvVar); err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("launch pi with %s: %v", auth.EnvVar, err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	g.mu.Lock()
	g.initCfg.PiProviderAuth = canonical
	g.validatedAuth = canonical
	g.refreshLocked()
	snap := g.snapshotLocked()
	g.mu.Unlock()
	return snap, nil
}

func (g *piAuthGate) refreshAndSnapshot(ctx context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	g.mu.Lock()
	g.refreshLocked()
	if g.ready || g.state == "error" || strings.TrimSpace(g.initCfg.PiProviderAuth) == "" {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, nil
	}
	canonical := strings.TrimSpace(g.initCfg.PiProviderAuth)
	g.mu.Unlock()

	if err := annotatePiProviderProbeError(g.probe(ctx), canonicalPiProviderEnvVar(canonical)); err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("launch pi: %v", err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, nil
	}

	g.mu.Lock()
	g.validatedAuth = canonical
	g.refreshLocked()
	snap := g.snapshotLocked()
	g.mu.Unlock()
	return snap, nil
}

func (g *piAuthGate) refreshLocked() {
	g.required = true
	g.ready = false
	g.state = "needs_configure"
	g.message = "Select a PI provider, and supply the token."
	g.configureOptions = piAgentAuthOptions()
	g.updatedAt = time.Now().UTC()

	auth, source, err := firstConfiguredPiProviderAuth(g.runtimeConfigPath, g.initCfg)
	if err != nil {
		g.state = "error"
		g.message = fmt.Sprintf("PI provider auth from %s is invalid: %v.", source, err)
		return
	}
	if auth.EnvVar != "" {
		if err := os.Setenv(auth.EnvVar, auth.Value); err != nil {
			g.state = "error"
			g.message = fmt.Sprintf("set %s: %v", auth.EnvVar, err)
			return
		}
		canonical, err := encodePiProviderAuth(auth)
		if err != nil {
			g.state = "error"
			g.message = fmt.Sprintf("PI provider auth from %s is invalid: %v.", source, err)
			return
		}
		g.initCfg.PiProviderAuth = canonical
		if g.validatedAuth == canonical {
			g.ready = true
			g.state = "ready"
			g.message = fmt.Sprintf("PI provider auth is ready via %s.", auth.EnvVar)
			return
		}
		g.message = fmt.Sprintf("PI provider auth is configured via %s. Validating Pi launch.", auth.EnvVar)
		return
	}
}

func (g *piAuthGate) snapshotLocked() hubui.AgentAuthState {
	return hubui.AgentAuthState{
		Harness:              agentruntime.HarnessPi,
		Required:             g.required,
		Ready:                g.ready,
		State:                strings.TrimSpace(g.state),
		Message:              strings.TrimSpace(g.message),
		ConfigurePlaceholder: "Paste the selected providers token in here...",
		ConfigureOptions:     append([]hubui.AgentAuthOption(nil), g.configureOptions...),
		UpdatedAt:            g.updatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func piAgentAuthOptions() []hubui.AgentAuthOption {
	options := make([]hubui.AgentAuthOption, 0, len(piProviderOptions))
	for _, option := range piProviderOptions {
		options = append(options, hubui.AgentAuthOption{
			Value:       option.EnvVar,
			Label:       option.Label,
			Description: option.Description,
		})
	}
	return options
}

func firstConfiguredPiProviderAuth(runtimeConfigPath string, initCfg hub.InitConfig) (piProviderAuth, string, error) {
	for _, option := range piProviderOptions {
		if value := strings.TrimSpace(os.Getenv(option.EnvVar)); value != "" {
			return piProviderAuth{EnvVar: option.EnvVar, Value: value}, "environment", nil
		}
	}
	if persistedEnv := strings.TrimSpace(os.Getenv("PI_PROVIDER_AUTH")); persistedEnv != "" {
		auth, err := decodePiProviderAuth(persistedEnv)
		if err != nil {
			return piProviderAuth{}, "environment", err
		}
		return auth, "environment", nil
	}

	for _, candidate := range []struct {
		value  string
		source string
	}{
		{value: strings.TrimSpace(initCfg.PiProviderAuth), source: "init config"},
		{value: hub.ReadRuntimeConfigString(runtimeConfigPath, piProviderAuthField, "piProviderAuth", "PI_PROVIDER_AUTH"), source: "runtime config"},
	} {
		if candidate.value == "" {
			continue
		}
		auth, err := decodePiProviderAuth(candidate.value)
		if err != nil {
			return piProviderAuth{}, candidate.source, err
		}
		return auth, candidate.source, nil
	}

	return piProviderAuth{}, "", nil
}

func normalizePiProviderAuth(rawInput string) (string, error) {
	auth, err := decodePiProviderAuth(rawInput)
	if err != nil {
		return "", err
	}
	return encodePiProviderAuth(auth)
}

func decodePiProviderAuth(rawInput string) (piProviderAuth, error) {
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		return piProviderAuth{}, fmt.Errorf("provider auth JSON is required")
	}

	var auth piProviderAuth
	if err := decodeJSONStrict(rawInput, &auth); err != nil {
		var wrapped string
		if err := decodeJSONStrict(rawInput, &wrapped); err == nil {
			return decodePiProviderAuth(wrapped)
		}
		return piProviderAuth{}, fmt.Errorf("expected JSON object with env_var and value")
	}

	auth.EnvVar = strings.TrimSpace(auth.EnvVar)
	if auth.EnvVar == "" {
		return piProviderAuth{}, fmt.Errorf("env_var is required")
	}
	option, ok := piProviderOptionsByEnv[auth.EnvVar]
	if !ok || option.EnvVar == "" {
		return piProviderAuth{}, fmt.Errorf("env_var %q is not supported", auth.EnvVar)
	}
	auth.Value = strings.TrimSpace(auth.Value)
	if auth.Value == "" {
		return piProviderAuth{}, fmt.Errorf("value is required")
	}
	if err := validatePiProviderAuthValue(auth.EnvVar, auth.Value); err != nil {
		return piProviderAuth{}, err
	}
	return auth, nil
}

func validatePiProviderAuthValue(envVar, value string) error {
	switch strings.TrimSpace(envVar) {
	case "OPENROUTER_API_KEY":
		if isLikelyOpenRouterAPIKey(value) {
			return nil
		}
		return fmt.Errorf("OPENROUTER_API_KEY must be an OpenRouter API key (sk-or-vN-...), not a Personal Access Token. See %s", openRouterProviderSetupDocURL)
	default:
		return nil
	}
}

func isLikelyOpenRouterAPIKey(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sk-or-v") {
		return false
	}
	rest := strings.TrimPrefix(value, "sk-or-v")
	if rest == "" {
		return false
	}

	versionDigits := 0
	for versionDigits < len(rest) && rest[versionDigits] >= '0' && rest[versionDigits] <= '9' {
		versionDigits++
	}
	if versionDigits == 0 || versionDigits >= len(rest) || rest[versionDigits] != '-' {
		return false
	}
	return versionDigits+1 < len(rest)
}

func encodePiProviderAuth(auth piProviderAuth) (string, error) {
	encoded, err := json.Marshal(auth)
	if err != nil {
		return "", fmt.Errorf("encode pi provider auth: %w", err)
	}
	return string(encoded), nil
}

func (g *piAuthGate) probe(ctx context.Context) error {
	if g == nil {
		return nil
	}
	runner := g.runner
	if runner == nil {
		runner = execx.OSRunner{}
	}
	command := strings.TrimSpace(g.command)
	if command == "" {
		command = agentruntime.HarnessPi
	}
	if ctx == nil {
		ctx = context.Background()
	}

	probeCtx, cancel := context.WithTimeout(ctx, piAuthProbeTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "moltenhub-pi-auth-*")
	if err != nil {
		return fmt.Errorf("create pi probe dir: %w", err)
	}
	defer os.RemoveAll(dir)

	runtime := agentruntime.Runtime{Harness: agentruntime.HarnessPi, Command: command}
	cmd, err := runtime.BuildCommand(dir, piAuthProbePrompt, agentruntime.RunOptions{})
	if err != nil {
		return fmt.Errorf("build pi probe command: %w", err)
	}
	if _, err := runner.Run(probeCtx, cmd); err != nil {
		g.logf("hub.auth status=warn harness=pi action=probe err=%q", err)
		return err
	}
	return nil
}

func canonicalPiProviderEnvVar(canonical string) string {
	auth, err := decodePiProviderAuth(canonical)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(auth.EnvVar)
}

func annotatePiProviderProbeError(err error, envVar string) error {
	if err == nil {
		return nil
	}

	errText := strings.TrimSpace(err.Error())
	if errText == "" {
		errText = "unknown error"
	}

	detail := errText
	if strings.EqualFold(strings.TrimSpace(envVar), "OPENROUTER_API_KEY") &&
		strings.Contains(strings.ToLower(errText), "personal access tokens are not supported for this endpoint") {
		detail += ". OpenRouter Personal Access Tokens are not supported for this PI flow. Use an OpenRouter API key for OPENROUTER_API_KEY. See " + openRouterProviderSetupDocURL
	}
	return fmt.Errorf("PI provider validation failed. Error details: %s", detail)
}
