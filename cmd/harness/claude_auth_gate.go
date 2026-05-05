package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

const claudeAuthDocsURL = "https://code.claude.com/docs/en/authentication"
const claudeGitHubConfigureCommand = "gh auth token"
const claudeGitHubConfigurePlaceholder = "ghp_xxx"
const claudeCredentialsConfigureCommand = "cat ~/.claude/.credentials.json"
const claudeCredentialsConfigurePlaceholder = "Paste ~/.claude/.credentials.json contents or CLAUDE_CODE_OAUTH_TOKEN..."
const claudeLoginCommand = "claude auth login"
const claudeOAuthTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

var claudeAuthURLPattern = regexp.MustCompile(`https?://[^\s"'<>()]+`)
var claudeOAuthTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{24,}$`)

type claudeAuthGate struct {
	mu sync.Mutex

	baseCtx context.Context
	runner  execx.Runner
	logf    func(string, ...any)

	command           string
	runtimeConfigPath string
	initCfg           hub.InitConfig

	required bool
	ready    bool
	state    string
	message  string

	authURL   string
	updatedAt time.Time

	procRunning           bool
	procCancel            context.CancelFunc
	procInput             io.WriteCloser
	lastPromptAdvance     time.Time
	pendingBrowserCode    string
	lastBrowserSubmit     time.Time
	browserSubmitAttempts int
	awaitingOAuthToken    bool
}

func newClaudeAuthGate(ctx context.Context, command string, logf func(string, ...any)) *claudeAuthGate {
	return newClaudeAuthGateWithContextAndConfigAndRunner(ctx, nil, command, "", hub.InitConfig{}, logf)
}

func newClaudeAuthGateWithConfig(command, runtimeConfigPath string, initCfg hub.InitConfig) *claudeAuthGate {
	return newClaudeAuthGateWithContextAndConfigAndRunner(context.Background(), nil, command, runtimeConfigPath, initCfg, nil)
}

func newClaudeAuthGateWithContextAndConfig(
	ctx context.Context,
	command, runtimeConfigPath string,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) *claudeAuthGate {
	return newClaudeAuthGateWithContextAndConfigAndRunner(ctx, nil, command, runtimeConfigPath, initCfg, logf)
}

func newClaudeAuthGateWithContextAndConfigAndRunner(
	ctx context.Context,
	runner execx.Runner,
	command, runtimeConfigPath string,
	initCfg hub.InitConfig,
	logf func(string, ...any),
) *claudeAuthGate {
	if ctx == nil {
		ctx = context.Background()
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	command = strings.TrimSpace(command)
	if command == "" {
		command = agentruntime.HarnessClaude
	}

	g := &claudeAuthGate{
		baseCtx:           ctx,
		runner:            runner,
		logf:              logf,
		command:           command,
		runtimeConfigPath: strings.TrimSpace(runtimeConfigPath),
		initCfg:           initCfg,
		required:          true,
		state:             "needs_browser_login",
		message:           claudeBrowserLoginRequiredMessage(),
		updatedAt:         time.Now().UTC(),
	}

	status, _ := g.Status(context.Background())
	g.mu.Lock()
	g.required = status.Required
	g.ready = status.Ready
	g.state = strings.TrimSpace(status.State)
	g.message = strings.TrimSpace(status.Message)
	g.authURL = strings.TrimSpace(status.AuthURL)
	g.updatedAt = time.Now().UTC()
	g.mu.Unlock()

	return g
}

func (g *claudeAuthGate) Status(_ context.Context) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	if blocked, state := g.githubTokenRequirementState(); blocked {
		return state, nil
	}

	g.mu.Lock()
	if !g.procRunning {
		ready, probeMessage := g.probeClaude()
		if ready {
			g.ready = true
			g.state = "ready"
			g.message = "Claude Code and GitHub token are ready."
			g.authURL = ""
		} else {
			g.ready = false
			if g.state != "pending_browser_login" && g.state != "error" {
				g.state = "needs_configure"
			}
			if g.state == "needs_configure" || strings.TrimSpace(g.message) == "" {
				g.message = firstNonEmptyString(
					probeMessage,
					claudeCredentialsConfigureMessage(),
				)
			}
		}
		g.updatedAt = time.Now().UTC()
	}
	state := g.snapshotLocked()
	g.mu.Unlock()

	return state, nil
}

func (g *claudeAuthGate) StartDeviceAuth(_ context.Context) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	status, _ := g.Status(context.Background())
	if status.Ready {
		return status, nil
	}
	if status.State == "needs_configure" && strings.TrimSpace(status.ConfigureCommand) == claudeGitHubConfigureCommand {
		return status, fmt.Errorf("github token is required")
	}

	g.mu.Lock()
	if g.procRunning {
		g.state = "pending_browser_login"
		g.message = "Waiting for Claude browser sign-in. Complete auth, then click Done."
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, nil
	}
	baseCtx := g.baseCtx
	command := g.command
	g.mu.Unlock()

	if baseCtx == nil {
		baseCtx = context.Background()
	}

	tmpDir, err := os.MkdirTemp("", "moltenhub-claude-auth-*")
	if err != nil {
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("create %s temp dir: %w", claudeLoginCommand, err)
	}

	procCtx, cancel := context.WithCancel(baseCtx)
	cmd := buildClaudeLoginCommand(procCtx, command)
	cmd.Dir = tmpDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("open %s stdout: %w", claudeLoginCommand, err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("open %s stderr: %w", claudeLoginCommand, err)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("open %s stdin: %w", claudeLoginCommand, err)
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		_ = stdinPipe.Close()
		g.mu.Lock()
		g.ready = false
		g.state = "error"
		g.message = fmt.Sprintf("start %s: %v", claudeLoginCommand, err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	g.mu.Lock()
	g.required = true
	g.ready = false
	g.procRunning = true
	g.procCancel = cancel
	g.procInput = stdinPipe
	g.lastPromptAdvance = time.Now().UTC()
	g.state = "pending_browser_login"
	g.message = "Starting Claude login. Follow prompts, complete browser sign-in, then click Done."
	g.updatedAt = time.Now().UTC()
	snap := g.snapshotLocked()
	g.mu.Unlock()

	var readerWG sync.WaitGroup
	readerWG.Add(2)
	go func() {
		defer readerWG.Done()
		g.readLoginStream(stdoutPipe)
	}()
	go func() {
		defer readerWG.Done()
		g.readLoginStream(stderrPipe)
	}()
	go g.waitLogin(cmd, tmpDir, &readerWG)

	// Prompt-driven login flows can emit non-newline terminal controls; proactively
	// send Enter once so default selections continue and the browser URL is printed.
	if _, err := io.WriteString(stdinPipe, "\n"); err != nil {
		g.logf("hub.auth status=warn harness=claude action=advance_login_prompt err=%q", err)
	}

	return snap, nil
}

func (g *claudeAuthGate) Verify(ctx context.Context) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	status, _ := g.Status(context.Background())
	if status.Ready {
		return status, nil
	}
	if status.State == "needs_configure" {
		return status, nil
	}
	if status.State == "pending_browser_login" {
		if snap, ok := g.completePendingBrowserLoginIfReady(); ok {
			return snap, nil
		}
		g.resubmitPendingBrowserCode()
		g.advanceLoginPrompt()
		return status, nil
	}

	return g.StartDeviceAuth(ctx)
}

func (g *claudeAuthGate) Configure(ctx context.Context, rawInput string) (web.AgentAuthState, error) {
	status, _ := g.Status(context.Background())
	if status.State == "pending_browser_login" || isClaudeCredentialsConfigureState(status) {
		if credentialsJSON, looksLikeJSON, err := normalizeClaudeCredentialsJSON(rawInput); looksLikeJSON {
			if err != nil {
				return g.pendingBrowserLoginFailure(
					fmt.Sprintf("Claude credentials JSON is invalid: %v", err),
					fmt.Errorf("invalid claude credentials json: %w", err),
				)
			}
			if err := g.completeWithClaudeCredentialsJSON(credentialsJSON); err != nil {
				return g.pendingBrowserLoginFailure(
					fmt.Sprintf("Claude credentials JSON failed to save: %v", err),
					fmt.Errorf("save claude credentials json: %w", err),
				)
			}
			state, _ := g.Status(context.Background())
			return state, nil
		}
		if token := extractClaudeOAuthTokenFromInput(rawInput); token != "" {
			g.completeWithClaudeOAuthToken(token)
			state, _ := g.Status(context.Background())
			return state, nil
		}
		if isClaudeCredentialsConfigureState(status) {
			state := status
			state.Message = "Claude credentials are required. Paste ~/.claude/.credentials.json contents or CLAUDE_CODE_OAUTH_TOKEN."
			return state, fmt.Errorf("claude credentials are required")
		}
		return g.submitBrowserCode(rawInput)
	}

	g.mu.Lock()
	runtimeConfigPath := g.runtimeConfigPath
	initCfg := g.initCfg
	runner := g.runner
	g.mu.Unlock()

	return configureGitHubTokenAndApply(
		ctx,
		agentruntime.HarnessClaude,
		runtimeConfigPath,
		initCfg,
		runner,
		rawInput,
		nil,
		func(token string) (web.AgentAuthState, error) {
			g.mu.Lock()
			g.initCfg.GitHubToken = token
			g.mu.Unlock()
			state, _ := g.Status(context.Background())
			return state, nil
		},
	)
}

func (g *claudeAuthGate) submitBrowserCode(rawInput string) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	var (
		input   io.WriteCloser
		authURL string
	)
	g.mu.Lock()
	if g.procRunning && g.procInput != nil {
		input = g.procInput
	}
	authURL = strings.TrimSpace(g.authURL)
	g.mu.Unlock()

	code := normalizeClaudeBrowserCodeForSubmission(rawInput, authURL)
	if strings.TrimSpace(code) == "" {
		state, _ := g.Status(context.Background())
		if strings.TrimSpace(state.State) == "" {
			state.State = "pending_browser_login"
		}
		state.Message = "Claude authentication input is required. Paste the browser code, token, or `~/.claude/.credentials.json` contents, then click Done."
		return state, fmt.Errorf("claude authentication code is required")
	}

	g.mu.Lock()
	g.pendingBrowserCode = code
	g.browserSubmitAttempts = 0
	g.mu.Unlock()

	if input == nil {
		g.mu.Lock()
		if !g.ready {
			g.state = "needs_browser_login"
			g.message = "Claude login is not waiting for a code. Click Done to restart browser login."
			g.updatedAt = time.Now().UTC()
		}
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, fmt.Errorf("claude login is not waiting for a code")
	}

	if _, err := io.WriteString(input, code+"\n"); err != nil {
		g.mu.Lock()
		if !g.ready {
			g.state = "pending_browser_login"
			g.message = fmt.Sprintf("submit claude authentication code: %v", err)
			g.updatedAt = time.Now().UTC()
		}
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}
	g.logf(
		"hub.auth status=progress harness=claude action=submit_browser_code chars=%d has_state=%t",
		len(code),
		strings.Contains(code, "#"),
	)
	g.mu.Lock()
	g.lastBrowserSubmit = time.Now().UTC()
	g.mu.Unlock()

	g.mu.Lock()
	if !g.ready {
		g.state = "pending_browser_login"
		g.message = "Claude authentication code received. If auth does not complete, paste `~/.claude/.credentials.json` (or token) and click Done."
		g.updatedAt = time.Now().UTC()
	}
	snap := g.snapshotLocked()
	g.mu.Unlock()

	return snap, nil
}

func normalizeClaudeBrowserCode(raw string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(raw)), "")
}

func normalizeClaudeCredentialsJSON(raw string) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return "", false, nil
	}

	var parsed map[string]any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return "", true, err
	}
	if parsed == nil {
		return "", true, fmt.Errorf("credentials payload must be one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", true, fmt.Errorf("credentials payload must contain only one JSON object")
	}
	return trimmed, true, nil
}

func extractClaudeOAuthTokenFromInput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	fields := strings.Fields(raw)
	if len(fields) == 1 && isLikelyClaudeOAuthToken(fields[0]) {
		return fields[0]
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	return extractClaudeOAuthTokenFromJSONValue(parsed)
}

func extractClaudeOAuthTokenFromJSONValue(value any) string {
	switch typed := value.(type) {
	case string:
		token := strings.TrimSpace(typed)
		if isLikelyClaudeOAuthToken(token) {
			return token
		}
		return ""
	case map[string]any:
		for _, key := range []string{
			"accessToken",
			"access_token",
			"claude_code_oauth_token",
			"oauth_token",
		} {
			if token := extractClaudeOAuthTokenFromJSONValue(typed[key]); token != "" {
				return token
			}
		}
		for _, key := range []string{
			"claudeAiOauth",
			"claude_ai_oauth",
			"oauth",
			"credentials",
		} {
			if token := extractClaudeOAuthTokenFromJSONValue(typed[key]); token != "" {
				return token
			}
		}
		for _, nested := range typed {
			if token := extractClaudeOAuthTokenFromJSONValue(nested); token != "" {
				return token
			}
		}
	case []any:
		for _, nested := range typed {
			if token := extractClaudeOAuthTokenFromJSONValue(nested); token != "" {
				return token
			}
		}
	}
	return ""
}

func isLikelyClaudeOAuthToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\r\n#") {
		return false
	}
	if strings.HasPrefix(value, "sk-ant-") || strings.HasPrefix(value, "sk-") {
		return true
	}
	return false
}

func normalizeClaudeBrowserCodeForSubmission(rawInput, authURL string) string {
	normalized := normalizeClaudeBrowserCode(rawInput)
	if normalized == "" {
		return ""
	}
	if strings.Contains(normalized, "#") {
		return normalized
	}

	if code, state := extractClaudeAuthCodeAndState(normalized); code != "" {
		if state == "" {
			state = extractClaudeAuthStateFromURL(authURL)
		}
		if state != "" {
			return code + "#" + state
		}
		return code
	}

	state := extractClaudeAuthStateFromURL(authURL)
	if state == "" {
		return normalized
	}
	return normalized + "#" + state
}

func extractClaudeAuthCodeAndState(raw string) (code string, state string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed != nil {
		parsedCode := strings.TrimSpace(parsed.Query().Get("code"))
		parsedState := strings.TrimSpace(parsed.Query().Get("state"))
		if parsedCode != "" {
			return parsedCode, parsedState
		}
	}
	if !strings.Contains(raw, "=") || !strings.Contains(raw, "&") {
		return "", ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(values.Get("code")), strings.TrimSpace(values.Get("state"))
}

func extractClaudeAuthStateFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("state"))
}

func (g *claudeAuthGate) completePendingBrowserLoginIfReady() (web.AgentAuthState, bool) {
	if g == nil {
		return web.AgentAuthState{}, false
	}

	ready, _ := g.probeClaude()
	if !ready {
		return web.AgentAuthState{}, false
	}

	var (
		cancel context.CancelFunc
		input  io.WriteCloser
	)

	g.mu.Lock()
	cancel = g.procCancel
	input = g.procInput
	g.ready = true
	g.state = "ready"
	g.message = "Claude Code and GitHub token are ready."
	g.authURL = ""
	g.pendingBrowserCode = ""
	g.browserSubmitAttempts = 0
	g.procRunning = false
	g.procCancel = nil
	g.procInput = nil
	g.updatedAt = time.Now().UTC()
	snap := g.snapshotLocked()
	g.mu.Unlock()

	if input != nil {
		_ = input.Close()
	}
	if cancel != nil {
		cancel()
	}

	return snap, true
}

func (g *claudeAuthGate) advanceLoginPrompt() {
	if g == nil {
		return
	}

	var input io.WriteCloser
	g.mu.Lock()
	if g.procRunning && g.procInput != nil {
		now := time.Now().UTC()
		if now.Sub(g.lastPromptAdvance) >= 250*time.Millisecond {
			g.lastPromptAdvance = now
			input = g.procInput
		}
	}
	g.mu.Unlock()

	if input == nil {
		return
	}
	if _, err := io.WriteString(input, "\n"); err != nil {
		g.logf("hub.auth status=warn harness=claude action=advance_login_prompt err=%q", err)
	}
}

func (g *claudeAuthGate) resubmitPendingBrowserCode() {
	if g == nil {
		return
	}

	var (
		input      io.WriteCloser
		code       string
		attempt    int
		altVariant bool
	)
	g.mu.Lock()
	if g.procRunning && g.procInput != nil {
		now := time.Now().UTC()
		if now.Sub(g.lastBrowserSubmit) >= 1200*time.Millisecond {
			input = g.procInput
			code = strings.TrimSpace(g.pendingBrowserCode)
			g.lastBrowserSubmit = now
			attempt = g.browserSubmitAttempts
			g.browserSubmitAttempts++
			if strings.Contains(code, "#") && attempt%2 == 1 {
				if rawCode, _, ok := strings.Cut(code, "#"); ok {
					code = strings.TrimSpace(rawCode)
					altVariant = true
				}
			}
		}
	}
	g.mu.Unlock()

	if input == nil || code == "" {
		return
	}
	if _, err := io.WriteString(input, code+"\n"); err != nil {
		g.logf("hub.auth status=warn harness=claude action=resubmit_browser_code err=%q", err)
		return
	}
	g.logf(
		"hub.auth status=progress harness=claude action=resubmit_browser_code chars=%d attempt=%d variant=%s",
		len(code),
		attempt+1,
		map[bool]string{true: "code_only", false: "full"}[altVariant],
	)
}

func (g *claudeAuthGate) snapshotLocked() web.AgentAuthState {
	state := strings.TrimSpace(g.state)
	if state == "" {
		if g.ready {
			state = "ready"
		} else {
			state = "needs_configure"
		}
	}
	authURL := strings.TrimSpace(g.authURL)
	required := g.required
	if !required {
		required = true
	}
	return web.AgentAuthState{
		Harness:              agentruntime.HarnessClaude,
		Required:             required,
		Ready:                g.ready,
		State:                state,
		Message:              strings.TrimSpace(g.message),
		ConfigureCommand:     claudeConfigureCommandForState(state),
		ConfigurePlaceholder: claudeConfigurePlaceholderForState(state),
		AuthURL:              authURL,
		AcceptsBrowserCode:   claudeAuthURLAcceptsBrowserCode(authURL),
		UpdatedAt:            g.updatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func isClaudeCredentialsConfigureState(state web.AgentAuthState) bool {
	if strings.TrimSpace(strings.ToLower(state.Harness)) != agentruntime.HarnessClaude {
		return false
	}
	if strings.TrimSpace(strings.ToLower(state.State)) != "needs_configure" {
		return false
	}
	command := strings.TrimSpace(strings.ToLower(state.ConfigureCommand))
	return command != "" && command != claudeGitHubConfigureCommand
}

func claudeConfigureCommandForState(state string) string {
	if strings.TrimSpace(strings.ToLower(state)) == "needs_configure" {
		return claudeCredentialsConfigureCommand
	}
	return ""
}

func claudeConfigurePlaceholderForState(state string) string {
	if strings.TrimSpace(strings.ToLower(state)) == "needs_configure" {
		return claudeCredentialsConfigurePlaceholder
	}
	return ""
}

func claudeAuthURLAcceptsBrowserCode(authURL string) bool {
	authURL = strings.TrimSpace(authURL)
	if authURL == "" {
		return false
	}
	parsed, err := url.Parse(authURL)
	if err != nil || parsed == nil {
		return false
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	path := strings.ToLower(strings.TrimSpace(parsed.Path))

	if parsed.Query().Get("code") == "true" {
		return true
	}
	if host == "claude.com" || strings.HasSuffix(host, ".claude.com") ||
		host == "claude.ai" || strings.HasSuffix(host, ".claude.ai") {
		if strings.Contains(path, "/oauth/authorize") || strings.Contains(path, "/login/device") {
			return true
		}
	}

	return false
}

func (g *claudeAuthGate) githubTokenRequirementState() (bool, web.AgentAuthState) {
	g.mu.Lock()
	baseCtx := g.baseCtx
	runner := g.runner
	runtimeConfigPath := g.runtimeConfigPath
	initCfg := g.initCfg
	g.mu.Unlock()

	return githubTokenRequirementState(baseCtx, runner, agentruntime.HarnessClaude, runtimeConfigPath, initCfg)
}

func (g *claudeAuthGate) probeClaude() (bool, string) {
	runtimeConfigPath := strings.TrimSpace(g.runtimeConfigPath)

	if oauthToken, source := firstConfiguredClaudeOAuthToken(runtimeConfigPath); strings.TrimSpace(oauthToken) != "" {
		if err := setClaudeOAuthTokenEnvironment(oauthToken); err == nil {
			if strings.TrimSpace(source) == "" {
				source = "token"
			}
			return true, fmt.Sprintf("Claude Code and GitHub token are ready (%s).", source)
		}
	}
	if enabled := firstClaudeEnabledProvider(); enabled != "" {
		return true, fmt.Sprintf("Claude Code is configured for %s credentials.", enabled)
	}
	if strings.TrimSpace(os.Getenv(claudeOAuthTokenEnv)) != "" {
		return true, "Claude Code is configured via CLAUDE_CODE_OAUTH_TOKEN."
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")) != "" {
		return true, "Claude Code is configured via ANTHROPIC_AUTH_TOKEN."
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return true, "Claude Code is configured via ANTHROPIC_API_KEY."
	}
	if loggedIn, statusMessage := g.probeClaudeAuthStatus(); loggedIn {
		return true, statusMessage
	}
	if path := claudeCredentialsPath(); path != "" {
		return true, fmt.Sprintf("Claude Code credentials were found at %s.", path)
	}

	return false, claudeBrowserLoginRequiredMessage()
}

func (g *claudeAuthGate) probeClaudeAuthStatus() (bool, string) {
	command := strings.TrimSpace(g.command)
	if command == "" {
		command = agentruntime.HarnessClaude
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := exec.CommandContext(ctx, command, "auth", "status").CombinedOutput()
	if err != nil {
		return false, ""
	}

	trimmed := strings.TrimSpace(string(res))
	if trimmed == "" {
		return false, ""
	}

	var payload struct {
		LoggedIn    bool   `json:"loggedIn"`
		AuthMethod  string `json:"authMethod"`
		APIProvider string `json:"apiProvider"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		if !payload.LoggedIn {
			return false, ""
		}
		method := strings.TrimSpace(payload.AuthMethod)
		provider := strings.TrimSpace(payload.APIProvider)
		if method == "" && provider == "" {
			return true, "Claude Code and GitHub token are ready."
		}
		return true, fmt.Sprintf(
			"Claude Code and GitHub token are ready (%s/%s).",
			firstNonEmptyString(provider, "firstParty"),
			firstNonEmptyString(method, "session"),
		)
	}

	if strings.Contains(strings.ToLower(trimmed), `"loggedin": true`) {
		return true, "Claude Code and GitHub token are ready."
	}
	return false, ""
}

func (g *claudeAuthGate) readLoginStream(r io.ReadCloser) {
	defer r.Close()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		g.ingestLoginLine(scanner.Text())
	}
}

func (g *claudeAuthGate) ingestLoginLine(line string) {
	if g == nil {
		return
	}

	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return
	}
	if shouldLogClaudeLoginLine(line) {
		g.logf("hub.auth status=stream harness=claude line=%q", truncateClaudeAuthLine(line))
	}

	authURL := extractClaudeAuthURL(line)
	promptAdvance := shouldAdvanceClaudeLoginPrompt(line)
	codePrompt := shouldPromptForClaudeBrowserCode(line)
	tokenPrompt := shouldCaptureClaudeOAuthToken(line)

	var input io.WriteCloser
	var update bool
	var token string

	g.mu.Lock()
	if authURL != "" {
		g.authURL = authURL
		if !g.ready {
			g.state = "pending_browser_login"
			g.message = ""
		}
		update = true
	}
	if promptAdvance && g.procRunning && g.procInput != nil {
		now := time.Now().UTC()
		if now.Sub(g.lastPromptAdvance) >= 250*time.Millisecond {
			g.lastPromptAdvance = now
			input = g.procInput
			update = true
		}
	}
	if tokenPrompt {
		g.awaitingOAuthToken = true
	}
	if g.awaitingOAuthToken {
		if candidate := extractClaudeOAuthTokenCandidate(line); candidate != "" {
			token = candidate
			g.awaitingOAuthToken = false
		} else if shouldStopClaudeOAuthTokenCapture(line) {
			g.awaitingOAuthToken = false
		}
	}
	if update {
		g.updatedAt = time.Now().UTC()
	}
	g.mu.Unlock()

	if token != "" {
		g.completeWithClaudeOAuthToken(token)
	}
	if codePrompt {
		g.resubmitPendingBrowserCode()
	}
	if input != nil {
		if _, err := io.WriteString(input, "\n"); err != nil {
			g.logf("hub.auth status=warn harness=claude action=advance_login_prompt err=%q", err)
		}
	}
}

func (g *claudeAuthGate) waitLogin(cmd *exec.Cmd, tempDir string, readerWG *sync.WaitGroup) {
	if readerWG != nil {
		readerWG.Wait()
	}
	err := cmd.Wait()
	if err != nil {
		g.logf("hub.auth status=warn harness=claude action=login_process_exit err=%q", err)
	} else {
		g.logf("hub.auth status=ok harness=claude action=login_process_exit")
	}
	_ = os.RemoveAll(strings.TrimSpace(tempDir))

	g.mu.Lock()
	procInput := g.procInput
	g.procRunning = false
	g.procCancel = nil
	g.procInput = nil
	g.pendingBrowserCode = ""
	g.browserSubmitAttempts = 0
	g.awaitingOAuthToken = false
	if procInput != nil {
		_ = procInput.Close()
	}

	if g.ready {
		g.mu.Unlock()
		return
	}
	if g.baseCtx != nil && g.baseCtx.Err() != nil {
		g.mu.Unlock()
		return
	}

	if err == nil {
		ready, probeMessage := g.probeClaude()
		if ready {
			g.ready = true
			g.state = "ready"
			g.message = "Claude Code and GitHub token are ready."
			g.authURL = ""
		} else {
			g.ready = false
			g.state = "needs_browser_login"
			g.message = firstNonEmptyString(
				probeMessage,
				"Claude login process ended before authorization was detected. Click Done to retry.",
			)
		}
		g.updatedAt = time.Now().UTC()
		g.mu.Unlock()
		return
	}

	g.ready = false
	if strings.TrimSpace(g.authURL) == "" {
		g.state = "needs_browser_login"
		g.message = "Claude login did not provide a browser URL. Click Done to retry."
	} else {
		g.state = "pending_browser_login"
		g.message = "Complete Claude browser sign-in, then click Done."
	}
	g.updatedAt = time.Now().UTC()
	g.mu.Unlock()
}

func shouldLogClaudeLoginLine(line string) bool {
	normalized := strings.ToLower(strings.TrimSpace(stripANSI(line)))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"http://",
		"https://",
		"auth",
		"login",
		"browser",
		"code",
		"verify",
		"success",
		"complete",
		"press enter",
		"select",
		"choose",
		"option",
		"error",
		"failed",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func truncateClaudeAuthLine(line string) string {
	line = strings.TrimSpace(line)
	const max = 240
	if len(line) <= max {
		return line
	}
	return line[:max-3] + "..."
}

func extractClaudeAuthURL(line string) string {
	matches := claudeAuthURLPattern.FindAllString(strings.TrimSpace(stripANSI(line)), -1)
	for _, match := range matches {
		candidate := sanitizeClaudeAuthURL(match)
		if candidate == "" || isClaudeDocsURL(candidate) {
			continue
		}
		parsed, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
			continue
		}
		if strings.TrimSpace(parsed.Host) == "" {
			continue
		}
		return candidate
	}
	return ""
}

func sanitizeClaudeAuthURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Terminal hyperlink escapes can wrap URLs with control bytes and a trailing backslash.
	raw = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, raw)
	if idx := strings.Index(raw, `\`); idx >= 0 {
		raw = raw[:idx]
	}
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,);]}>\\")
	return raw
}

func shouldAdvanceClaudeLoginPrompt(line string) bool {
	text := strings.ToLower(strings.TrimSpace(stripANSI(line)))
	if text == "" {
		return false
	}

	for _, marker := range []string{
		"select",
		"choose",
		"press enter",
		"enter to continue",
		"which account",
		"which option",
		"pick an option",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func shouldPromptForClaudeBrowserCode(line string) bool {
	text := strings.ToLower(strings.TrimSpace(stripANSI(line)))
	if text == "" {
		return false
	}
	return strings.Contains(text, "paste code here") ||
		strings.Contains(text, "authentication code") ||
		strings.Contains(text, "if prompted >")
}

func shouldCaptureClaudeOAuthToken(line string) bool {
	text := strings.ToLower(strings.TrimSpace(stripANSI(line)))
	if text == "" {
		return false
	}
	return strings.Contains(text, "your oauth token")
}

func shouldStopClaudeOAuthTokenCapture(line string) bool {
	text := strings.ToLower(strings.TrimSpace(stripANSI(line)))
	if text == "" {
		return false
	}
	return strings.Contains(text, "store this token securely") ||
		strings.Contains(text, "use this token by setting") ||
		strings.Contains(text, "login successful")
}

func extractClaudeOAuthTokenCandidate(line string) string {
	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return ""
	}
	if strings.Contains(line, " ") {
		return ""
	}
	if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
		return ""
	}
	if !claudeOAuthTokenPattern.MatchString(line) {
		return ""
	}
	return line
}

func (g *claudeAuthGate) completeWithClaudeOAuthToken(token string) {
	if g == nil {
		return
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	if err := setClaudeOAuthTokenEnvironment(token); err != nil {
		g.logf("hub.auth status=warn harness=claude action=save_oauth_token err=%q", err)
		return
	}

	var (
		cancel            context.CancelFunc
		input             io.WriteCloser
		runtimeConfigPath string
		initCfg           hub.InitConfig
	)

	g.mu.Lock()
	runtimeConfigPath = g.runtimeConfigPath
	initCfg = g.initCfg
	cancel = g.procCancel
	input = g.procInput
	g.ready = true
	g.state = "ready"
	g.message = "Claude Code and GitHub token are ready."
	g.authURL = ""
	g.pendingBrowserCode = ""
	g.browserSubmitAttempts = 0
	g.awaitingOAuthToken = false
	g.procRunning = false
	g.procCancel = nil
	g.procInput = nil
	g.updatedAt = time.Now().UTC()
	g.mu.Unlock()

	if runtimeConfigPath != "" {
		if err := hub.SaveRuntimeConfigClaudeOAuthToken(runtimeConfigPath, initCfg, token); err != nil {
			g.logf("hub.auth status=warn harness=claude action=persist_oauth_token err=%q", err)
		}
	}

	if input != nil {
		_ = input.Close()
	}
	if cancel != nil {
		cancel()
	}
	g.logf("hub.auth status=ok harness=claude action=oauth_token_captured")
}

func (g *claudeAuthGate) completeWithClaudeCredentialsJSON(raw string) error {
	if g == nil {
		return nil
	}

	credentialsPath, err := saveClaudeCredentialsJSON(raw)
	if err != nil {
		return err
	}
	if token := extractClaudeOAuthTokenFromInput(raw); token != "" {
		if err := setClaudeOAuthTokenEnvironment(token); err != nil {
			g.logf("hub.auth status=warn harness=claude action=save_oauth_token err=%q", err)
		} else {
			g.mu.Lock()
			runtimeConfigPath := g.runtimeConfigPath
			initCfg := g.initCfg
			g.mu.Unlock()
			if runtimeConfigPath != "" {
				if err := hub.SaveRuntimeConfigClaudeOAuthToken(runtimeConfigPath, initCfg, token); err != nil {
					g.logf("hub.auth status=warn harness=claude action=persist_oauth_token err=%q", err)
				}
			}
		}
	}

	var (
		cancel context.CancelFunc
		input  io.WriteCloser
	)
	g.mu.Lock()
	cancel = g.procCancel
	input = g.procInput
	g.ready = true
	g.state = "ready"
	g.message = "Claude Code and GitHub token are ready."
	g.authURL = ""
	g.pendingBrowserCode = ""
	g.browserSubmitAttempts = 0
	g.awaitingOAuthToken = false
	g.procRunning = false
	g.procCancel = nil
	g.procInput = nil
	g.updatedAt = time.Now().UTC()
	g.mu.Unlock()

	if input != nil {
		_ = input.Close()
	}
	if cancel != nil {
		cancel()
	}
	g.logf("hub.auth status=ok harness=claude action=credentials_json_captured path=%q", credentialsPath)
	return nil
}

func buildClaudeLoginCommand(ctx context.Context, command string) *exec.Cmd {
	command = strings.TrimSpace(command)
	if command == "" {
		command = agentruntime.HarnessClaude
	}
	args := claudeLoginArgs(command)
	if _, err := exec.LookPath("script"); err == nil {
		loginCmd := shellQuoteJoin(append([]string{command}, args...)...)
		ptyCmd := "stty cols 1000 rows 50; " + loginCmd
		return exec.CommandContext(
			ctx,
			"script",
			"-qefc",
			ptyCmd,
			"/dev/null",
		)
	}
	return exec.CommandContext(ctx, command, args...)
}

func claudeLoginArgs(command string) []string {
	trimmed := strings.TrimSpace(command)
	base := strings.ToLower(strings.TrimSpace(filepath.Base(trimmed)))
	if trimmed == agentruntime.HarnessClaude || base == "claude" {
		return []string{"auth", "login"}
	}
	return []string{"auth", "login"}
}

func shellQuoteJoin(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func firstConfiguredClaudeOAuthToken(runtimeConfigPath string) (value string, source string) {
	if env := strings.TrimSpace(os.Getenv(claudeOAuthTokenEnv)); env != "" {
		return env, "environment"
	}
	if persisted := hub.ReadRuntimeConfigString(
		runtimeConfigPath,
		"claude_code_oauth_token",
		"claudeCodeOauthToken",
		"CLAUDE_CODE_OAUTH_TOKEN",
	); persisted != "" {
		return persisted, "runtime config"
	}
	return "", ""
}

func setClaudeOAuthTokenEnvironment(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("claude oauth token is required")
	}
	return os.Setenv(claudeOAuthTokenEnv, token)
}

func saveClaudeCredentialsJSON(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("claude credentials json is required")
	}
	candidates := claudeCredentialCandidates()
	if len(candidates) == 0 {
		return "", fmt.Errorf("claude credentials path is unavailable")
	}
	path := candidates[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create claude credentials dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(trimmed+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write claude credentials file: %w", err)
	}
	return path, nil
}

func (g *claudeAuthGate) pendingBrowserLoginFailure(message string, err error) (web.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), err
	}
	g.mu.Lock()
	if !g.ready {
		if strings.TrimSpace(g.state) == "" {
			g.state = "needs_configure"
		}
		g.message = strings.TrimSpace(message)
		g.updatedAt = time.Now().UTC()
	}
	snap := g.snapshotLocked()
	g.mu.Unlock()
	return snap, err
}

func firstClaudeEnabledProvider() string {
	providers := []struct {
		flag string
		name string
	}{
		{flag: "CLAUDE_CODE_USE_BEDROCK", name: "Amazon Bedrock"},
		{flag: "CLAUDE_CODE_USE_VERTEX", name: "Google Vertex AI"},
		{flag: "CLAUDE_CODE_USE_FOUNDRY", name: "Microsoft Foundry"},
	}
	for _, provider := range providers {
		if envEnabled(provider.flag) {
			return provider.name
		}
	}
	return ""
}

func envEnabled(key string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch value {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func claudeCredentialsPath() string {
	for _, path := range claudeCredentialCandidates() {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		return path
	}
	return ""
}

func claudeCredentialCandidates() []string {
	candidates := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	addCandidate := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}

	configDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configDir != "" {
		if strings.HasSuffix(strings.ToLower(configDir), ".json") {
			addCandidate(configDir)
		} else {
			addCandidate(filepath.Join(configDir, ".credentials.json"))
		}
	}

	homeValues := make([]string, 0, 2)
	if homeEnv := strings.TrimSpace(os.Getenv("HOME")); homeEnv != "" {
		homeValues = append(homeValues, homeEnv)
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		homeDir = strings.TrimSpace(homeDir)
		if homeDir != "" {
			homeValues = append(homeValues, homeDir)
		}
	}

	for _, home := range homeValues {
		addCandidate(filepath.Join(home, ".claude", ".credentials.json"))
		addCandidate(filepath.Join(home, ".config", "claude", ".credentials.json"))
	}

	return candidates
}

func claudeBrowserLoginRequiredMessage() string {
	return claudeCredentialsConfigureMessage()
}

func claudeCredentialsConfigureMessage() string {
	return "Claude Code credentials are required. Paste `~/.claude/.credentials.json` contents or `CLAUDE_CODE_OAUTH_TOKEN`."
}

func isClaudeDocsURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	path := strings.ToLower(strings.TrimSpace(parsed.Path))
	if host != "code.claude.com" {
		return false
	}
	return strings.HasPrefix(path, "/docs/")
}
