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

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

const claudeAuthDocsURL = "https://code.claude.com/docs/en/authentication"
const claudeGitHubConfigureCommand = "gh auth token"
const claudeGitHubConfigurePlaceholder = "ghp_xxx"
const claudeLoginCommand = "claude auth login"

var claudeAuthURLPattern = regexp.MustCompile(`https?://[^\s"'<>()]+`)

type claudeAuthGate struct {
	mu sync.Mutex

	baseCtx context.Context
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

	procRunning       bool
	procCancel        context.CancelFunc
	procInput         io.WriteCloser
	lastPromptAdvance time.Time
}

func newClaudeAuthGate(ctx context.Context, command string, logf func(string, ...any)) *claudeAuthGate {
	return newClaudeAuthGateWithContextAndConfig(ctx, command, "", hub.InitConfig{}, logf)
}

func newClaudeAuthGateWithConfig(command, runtimeConfigPath string, initCfg hub.InitConfig) *claudeAuthGate {
	return newClaudeAuthGateWithContextAndConfig(context.Background(), command, runtimeConfigPath, initCfg, nil)
}

func newClaudeAuthGateWithContextAndConfig(
	ctx context.Context,
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

func (g *claudeAuthGate) Status(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
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
				g.state = "needs_browser_login"
			}
			if g.state == "needs_browser_login" || strings.TrimSpace(g.message) == "" {
				g.message = firstNonEmptyString(
					probeMessage,
					claudeBrowserLoginRequiredMessage(),
				)
			}
		}
		g.updatedAt = time.Now().UTC()
	}
	state := g.snapshotLocked()
	g.mu.Unlock()

	return state, nil
}

func (g *claudeAuthGate) StartDeviceAuth(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
	}

	status, _ := g.Status(context.Background())
	if status.Ready {
		return status, nil
	}
	if status.State == "needs_configure" {
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
	cmd := exec.CommandContext(procCtx, command, "auth", "login")
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

	go g.readLoginStream(stdoutPipe)
	go g.readLoginStream(stderrPipe)
	go g.waitLogin(cmd, tmpDir)

	// Prompt-driven login flows can emit non-newline terminal controls; proactively
	// send Enter once so default selections continue and the browser URL is printed.
	if _, err := io.WriteString(stdinPipe, "\n"); err != nil {
		g.logf("hub.auth status=warn harness=claude action=advance_login_prompt err=%q", err)
	}

	return snap, nil
}

func (g *claudeAuthGate) Verify(ctx context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
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
		g.advanceLoginPrompt()
		return status, nil
	}

	return g.StartDeviceAuth(ctx)
}

func (g *claudeAuthGate) Configure(_ context.Context, rawInput string) (hubui.AgentAuthState, error) {
	status, _ := g.Status(context.Background())
	if status.State == "pending_browser_login" {
		return g.submitBrowserCode(rawInput)
	}

	token := strings.TrimSpace(rawInput)
	if token == "" {
		state := g.needsGitHubTokenState("GitHub token is required. Paste a GitHub token below, then click Done.")
		return state, fmt.Errorf("github token is required")
	}

	g.mu.Lock()
	runtimeConfigPath := g.runtimeConfigPath
	initCfg := g.initCfg
	g.mu.Unlock()

	if err := hub.SaveRuntimeConfigGitHubToken(runtimeConfigPath, initCfg, token); err != nil {
		state := g.needsGitHubTokenState(fmt.Sprintf("save github token: %v", err))
		return state, err
	}
	if err := setGitHubTokenEnvironment(token); err != nil {
		state := g.needsGitHubTokenState(fmt.Sprintf("set github token env: %v", err))
		return state, err
	}

	g.mu.Lock()
	g.initCfg.GitHubToken = token
	g.mu.Unlock()

	state, _ := g.Status(context.Background())
	return state, nil
}

func (g *claudeAuthGate) submitBrowserCode(rawInput string) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
	}

	code := strings.TrimSpace(rawInput)
	if code == "" {
		state, _ := g.Status(context.Background())
		if strings.TrimSpace(state.State) == "" {
			state.State = "pending_browser_login"
		}
		state.Message = "Claude authentication code is required. Paste the code from the browser, then click Done."
		return state, fmt.Errorf("claude authentication code is required")
	}

	var input io.WriteCloser
	g.mu.Lock()
	if g.procRunning && g.procInput != nil {
		input = g.procInput
	}
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

	g.mu.Lock()
	if !g.ready {
		g.state = "pending_browser_login"
		g.message = "Claude authentication code received. Finish browser login, then click Done."
		g.updatedAt = time.Now().UTC()
	}
	snap := g.snapshotLocked()
	g.mu.Unlock()

	return snap, nil
}

func (g *claudeAuthGate) completePendingBrowserLoginIfReady() (hubui.AgentAuthState, bool) {
	if g == nil {
		return hubui.AgentAuthState{}, false
	}

	ready, _ := g.probeClaude()
	if !ready {
		return hubui.AgentAuthState{}, false
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

func (g *claudeAuthGate) snapshotLocked() hubui.AgentAuthState {
	state := strings.TrimSpace(g.state)
	if state == "" {
		if g.ready {
			state = "ready"
		} else {
			state = "needs_browser_login"
		}
	}
	authURL := strings.TrimSpace(g.authURL)
	required := g.required
	if !required {
		required = true
	}
	return hubui.AgentAuthState{
		Harness:   agentruntime.HarnessClaude,
		Required:  required,
		Ready:     g.ready,
		State:     state,
		Message:   strings.TrimSpace(g.message),
		AuthURL:   authURL,
		UpdatedAt: g.updatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (g *claudeAuthGate) needsGitHubTokenState(message string) hubui.AgentAuthState {
	message = firstNonEmptyString(
		message,
		"GitHub token is required. Set GITHUB_TOKEN/GH_TOKEN or paste a token below, then click Done.",
	)
	return hubui.AgentAuthState{
		Harness:              agentruntime.HarnessClaude,
		Required:             true,
		Ready:                false,
		State:                "needs_configure",
		Message:              message,
		ConfigureCommand:     claudeGitHubConfigureCommand,
		ConfigurePlaceholder: claudeGitHubConfigurePlaceholder,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (g *claudeAuthGate) githubTokenRequirementState() (bool, hubui.AgentAuthState) {
	g.mu.Lock()
	runtimeConfigPath := g.runtimeConfigPath
	initCfg := g.initCfg
	g.mu.Unlock()

	githubToken, _ := firstConfiguredGitHubToken(runtimeConfigPath, initCfg)
	if strings.TrimSpace(githubToken) == "" {
		return true, g.needsGitHubTokenState("")
	}
	if err := setGitHubTokenEnvironment(githubToken); err != nil {
		return true, g.needsGitHubTokenState(fmt.Sprintf("set github token env: %v", err))
	}

	return false, hubui.AgentAuthState{}
}

func (g *claudeAuthGate) probeClaude() (bool, string) {
	if enabled := firstClaudeEnabledProvider(); enabled != "" {
		return true, fmt.Sprintf("Claude Code is configured for %s credentials.", enabled)
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")) != "" {
		return true, "Claude Code is configured via ANTHROPIC_AUTH_TOKEN."
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return true, "Claude Code is configured via ANTHROPIC_API_KEY."
	}
	if path := claudeCredentialsPath(); path != "" {
		return true, fmt.Sprintf("Claude Code credentials were found at %s.", path)
	}

	return false, claudeBrowserLoginRequiredMessage()
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

	authURL := extractClaudeAuthURL(line)
	promptAdvance := shouldAdvanceClaudeLoginPrompt(line)

	var input io.WriteCloser
	var update bool

	g.mu.Lock()
	if authURL != "" {
		g.authURL = authURL
		if !g.ready {
			g.state = "pending_browser_login"
			g.message = "Open the Claude login URL, complete sign-in, then click Done."
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
	if update {
		g.updatedAt = time.Now().UTC()
	}
	g.mu.Unlock()

	if input != nil {
		if _, err := io.WriteString(input, "\n"); err != nil {
			g.logf("hub.auth status=warn harness=claude action=advance_login_prompt err=%q", err)
		}
	}
}

func (g *claudeAuthGate) waitLogin(cmd *exec.Cmd, tempDir string) {
	err := cmd.Wait()
	_ = os.RemoveAll(strings.TrimSpace(tempDir))

	g.mu.Lock()
	procInput := g.procInput
	g.procRunning = false
	g.procCancel = nil
	g.procInput = nil
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

func firstConfiguredGitHubToken(runtimeConfigPath string, initCfg hub.InitConfig) (value string, source string) {
	if env := strings.TrimSpace(os.Getenv("GH_TOKEN")); env != "" {
		return env, "environment"
	}
	if env := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); env != "" {
		return env, "environment"
	}
	if init := strings.TrimSpace(initCfg.GitHubToken); init != "" {
		return init, "init config"
	}
	if persisted := loadPersistedGitHubToken(runtimeConfigPath); persisted != "" {
		return persisted, "runtime config"
	}
	return "", ""
}

func loadPersistedGitHubToken(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	return firstNonEmptyString(
		stringValue(doc["github_token"]),
		stringValue(doc["githubToken"]),
		stringValue(doc["GITHUB_TOKEN"]),
	)
}

func setGitHubTokenEnvironment(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("github token is required")
	}
	if err := os.Setenv("GITHUB_TOKEN", token); err != nil {
		return err
	}
	if err := os.Setenv("GH_TOKEN", token); err != nil {
		return err
	}
	return nil
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
	base := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".claude")
	}
	path := filepath.Join(base, ".credentials.json")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return ""
	}
	return path
}

func claudeBrowserLoginRequiredMessage() string {
	return "Claude Code login is required. Run `claude auth login`, complete browser sign-in, then click Done.\nReference docs (not an authorization link): " + claudeAuthDocsURL
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
