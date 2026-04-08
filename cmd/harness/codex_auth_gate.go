package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

const codexAuthProbeTimeout = 12 * time.Second

var (
	ansiEscapePattern     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	deviceAuthURLPattern  = regexp.MustCompile(`https://auth\.openai\.com/[^\s]+`)
	deviceAuthCodePattern = regexp.MustCompile(`\b[A-Z0-9]{4,}-[A-Z0-9]{4,}\b`)
)

type codexAuthGate struct {
	mu sync.Mutex

	baseCtx context.Context
	runner  execx.Runner
	logf    func(string, ...any)

	command string

	runtimeConfigPath  string
	initCfg            hub.InitConfig
	requireGitHubToken bool

	required bool
	ready    bool
	state    string
	message  string

	authURL    string
	deviceCode string
	updatedAt  time.Time

	procRunning bool
	procCancel  context.CancelFunc
}

func newCodexAuthGate(
	ctx context.Context,
	runner execx.Runner,
	command string,
	logf func(string, ...any),
) *codexAuthGate {
	return newCodexAuthGateWithConfig(ctx, runner, command, "", hub.InitConfig{}, false, logf)
}

func newCodexAuthGateWithConfig(
	ctx context.Context,
	runner execx.Runner,
	command string,
	runtimeConfigPath string,
	initCfg hub.InitConfig,
	requireGitHubToken bool,
	logf func(string, ...any),
) *codexAuthGate {
	if runner == nil {
		runner = execx.OSRunner{}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	command = strings.TrimSpace(command)
	if command == "" {
		command = agentruntime.HarnessCodex
	}

	g := &codexAuthGate{
		baseCtx:            ctx,
		runner:             runner,
		logf:               logf,
		command:            command,
		runtimeConfigPath:  strings.TrimSpace(runtimeConfigPath),
		initCfg:            initCfg,
		requireGitHubToken: requireGitHubToken,
		required:           true,
		ready:              false,
		state:              "needs_device_auth",
		message:            "Codex authorization is required before running tasks.",
		updatedAt:          time.Now().UTC(),
	}

	if blocked, state := g.githubTokenRequirementState(); blocked {
		g.mu.Lock()
		g.required = state.Required
		g.ready = state.Ready
		g.state = state.State
		g.message = state.Message
		g.updatedAt = time.Now().UTC()
		g.mu.Unlock()
		return g
	}

	ready, probeMessage, probeErr := g.probe(ctx)
	autoStartDeviceAuth := false
	g.mu.Lock()
	if probeErr != nil {
		g.state = "error"
		g.message = probeErr.Error()
		g.updatedAt = time.Now().UTC()
		g.mu.Unlock()
		return g
	}
	if ready {
		g.ready = true
		g.state = "ready"
		if strings.TrimSpace(probeMessage) == "" {
			g.message = "Codex authorization is ready."
		} else {
			g.message = probeMessage
		}
	} else if strings.TrimSpace(probeMessage) != "" {
		g.message = probeMessage
		autoStartDeviceAuth = true
	} else {
		autoStartDeviceAuth = true
	}
	g.updatedAt = time.Now().UTC()
	g.mu.Unlock()

	if autoStartDeviceAuth {
		if _, err := g.StartDeviceAuth(context.Background()); err != nil {
			g.logf("hub.auth status=warn action=start_codex_device_auth err=%q", err)
		}
	}

	return g
}

func (g *codexAuthGate) Status(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	if blocked, state := g.githubTokenRequirementState(); blocked {
		return state, nil
	}

	needsProbe := false
	g.mu.Lock()
	if !g.ready && !g.procRunning && strings.TrimSpace(g.state) == "needs_configure" {
		needsProbe = true
	}
	g.mu.Unlock()

	if needsProbe {
		ready, probeMessage, probeErr := g.probe(context.Background())
		g.mu.Lock()
		if probeErr != nil {
			g.ready = false
			g.state = "error"
			g.message = probeErr.Error()
			g.updatedAt = time.Now().UTC()
		} else if ready {
			g.ready = true
			g.state = "ready"
			g.message = normalizeCodexStatusMessage(probeMessage)
			g.updatedAt = time.Now().UTC()
		} else {
			g.ready = false
			g.state = "needs_device_auth"
			g.message = firstNonEmptyString(probeMessage, "Codex is not logged in. Device authorization is required.")
			g.updatedAt = time.Now().UTC()
		}
		g.mu.Unlock()
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked(), nil
}

func (g *codexAuthGate) StartDeviceAuth(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	if blocked, state := g.githubTokenRequirementState(); blocked {
		return state, fmt.Errorf("github token is required")
	}

	g.mu.Lock()
	if g.ready {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, nil
	}
	if g.state == "error" {
		snap := g.snapshotLocked()
		err := fmt.Errorf("cannot start codex device auth: %s", strings.TrimSpace(g.message))
		g.mu.Unlock()
		return snap, err
	}
	if g.procRunning {
		if g.state == "" {
			g.state = "pending_device_auth"
		}
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, nil
	}
	g.mu.Unlock()

	tmpDir, err := os.MkdirTemp("", "moltenhub-codex-auth-*")
	if err != nil {
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("create codex device auth temp dir: %w", err)
	}

	procCtx, cancel := context.WithCancel(g.baseCtx)
	cmd := exec.CommandContext(procCtx, g.command, "login", "--device-auth")
	cmd.Dir = tmpDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("open codex device auth stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		snap, _ := g.Status(context.Background())
		return snap, fmt.Errorf("open codex device auth stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmpDir)
		cancel()
		g.mu.Lock()
		g.state = "error"
		g.message = fmt.Sprintf("start codex device auth: %v", err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	g.mu.Lock()
	g.procRunning = true
	g.procCancel = cancel
	g.state = "pending_device_auth"
	g.message = "Waiting for device authorization. Use the link and code, then click Done."
	g.updatedAt = time.Now().UTC()
	snap := g.snapshotLocked()
	g.mu.Unlock()

	go g.readDeviceAuthStream(stdoutPipe)
	go g.readDeviceAuthStream(stderrPipe)
	go g.waitDeviceAuth(cmd, tmpDir)

	return snap, nil
}

func (g *codexAuthGate) Verify(ctx context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
	}

	if blocked, state := g.githubTokenRequirementState(); blocked {
		return state, nil
	}

	ready, probeMessage, probeErr := g.probe(ctx)

	g.mu.Lock()
	defer g.mu.Unlock()

	if probeErr != nil {
		g.state = "error"
		g.message = probeErr.Error()
		g.updatedAt = time.Now().UTC()
		return g.snapshotLocked(), probeErr
	}

	if ready {
		g.ready = true
		g.state = "ready"
		if strings.TrimSpace(probeMessage) == "" {
			g.message = "Codex authorization is ready."
		} else {
			g.message = probeMessage
		}
		if g.procRunning && g.procCancel != nil {
			g.procCancel()
		}
		g.updatedAt = time.Now().UTC()
		return g.snapshotLocked(), nil
	}

	if g.procRunning {
		g.state = "pending_device_auth"
		g.message = "Still waiting for authorization. Complete browser auth, then click Done."
	} else {
		g.state = "needs_device_auth"
		if strings.TrimSpace(probeMessage) == "" {
			g.message = "Codex is not logged in. Device authorization is required."
		} else {
			g.message = probeMessage
		}
	}
	g.ready = false
	g.updatedAt = time.Now().UTC()
	return g.snapshotLocked(), nil
}

func (g *codexAuthGate) Configure(_ context.Context, rawInput string) (hubui.AgentAuthState, error) {
	if g == nil {
		return readyAgentAuthState(), nil
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

	state, err := g.Verify(context.Background())
	if err != nil {
		return state, err
	}
	return state, nil
}

func (g *codexAuthGate) readDeviceAuthStream(r io.ReadCloser) {
	defer r.Close()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		g.ingestDeviceAuthLine(scanner.Text())
	}
}

func (g *codexAuthGate) ingestDeviceAuthLine(line string) {
	if g == nil {
		return
	}
	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return
	}

	urlMatch := deviceAuthURLPattern.FindString(line)
	codeMatch := deviceAuthCodePattern.FindString(strings.ToUpper(line))
	if urlMatch == "" && codeMatch == "" {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if urlMatch != "" {
		g.authURL = strings.TrimSpace(urlMatch)
	}
	if codeMatch != "" {
		g.deviceCode = strings.TrimSpace(codeMatch)
	}
	if !g.ready {
		g.state = "pending_device_auth"
		g.message = "Use the link and code above, then click Done."
	}
	g.updatedAt = time.Now().UTC()
}

func (g *codexAuthGate) waitDeviceAuth(cmd *exec.Cmd, tempDir string) {
	err := cmd.Wait()
	_ = os.RemoveAll(strings.TrimSpace(tempDir))

	g.mu.Lock()
	defer g.mu.Unlock()

	g.procRunning = false
	g.procCancel = nil

	if g.ready {
		return
	}
	if err == nil {
		g.ready = true
		g.state = "ready"
		g.message = "Codex authorization is ready."
		g.updatedAt = time.Now().UTC()
		return
	}
	if g.baseCtx != nil && g.baseCtx.Err() != nil {
		return
	}
	if strings.TrimSpace(g.authURL) == "" || strings.TrimSpace(g.deviceCode) == "" {
		g.state = "needs_device_auth"
		g.message = "Device authorization did not complete. Click Done to retry."
		g.updatedAt = time.Now().UTC()
	}
}

func (g *codexAuthGate) probe(ctx context.Context) (bool, string, error) {
	if g == nil {
		return true, "Codex authorization is ready.", nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	tmpDir, err := os.MkdirTemp("", "moltenhub-codex-auth-probe-*")
	if err != nil {
		return false, "", fmt.Errorf("create auth probe dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	probeCtx, cancel := context.WithTimeout(ctx, codexAuthProbeTimeout)
	defer cancel()

	res, runErr := g.runner.Run(probeCtx, execx.Command{
		Dir:  tmpDir,
		Name: g.command,
		Args: []string{"login", "status"},
	})

	combined := strings.TrimSpace(strings.Join([]string{res.Stdout, res.Stderr}, "\n"))
	combined = strings.TrimSpace(stripANSI(combined))
	combinedLower := strings.ToLower(combined)
	errLower := ""
	if runErr != nil {
		errLower = strings.ToLower(runErr.Error())
	}
	fullLower := strings.TrimSpace(combinedLower + " " + errLower)

	if runErr == nil {
		return true, normalizeCodexStatusMessage(combined), nil
	}
	if strings.Contains(fullLower, "not logged in") {
		return false, "Codex is not logged in. Device authorization is required.", nil
	}
	if strings.Contains(fullLower, "executable file not found") || strings.Contains(fullLower, "command not found") {
		return false, "", fmt.Errorf("codex CLI is not available on PATH")
	}
	if strings.Contains(fullLower, "logged in") {
		return true, normalizeCodexStatusMessage(combined), nil
	}
	if strings.Contains(fullLower, "api key") {
		return true, normalizeCodexStatusMessage(combined), nil
	}
	return false, firstNonEmptyString(combined, "Unable to verify Codex authorization status."), nil
}

func (g *codexAuthGate) snapshotLocked() hubui.AgentAuthState {
	state := strings.TrimSpace(g.state)
	if state == "" {
		if g.ready {
			state = "ready"
		} else {
			state = "needs_device_auth"
		}
	}
	return hubui.AgentAuthState{
		Harness:    agentruntime.HarnessCodex,
		Required:   g.required,
		Ready:      g.ready,
		State:      state,
		Message:    strings.TrimSpace(g.message),
		AuthURL:    strings.TrimSpace(g.authURL),
		DeviceCode: strings.TrimSpace(g.deviceCode),
		UpdatedAt:  g.updatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (g *codexAuthGate) needsGitHubTokenState(message string) hubui.AgentAuthState {
	return githubTokenNeedsConfigureState(agentruntime.HarnessCodex, message)
}

func (g *codexAuthGate) githubTokenRequirementState() (bool, hubui.AgentAuthState) {
	if g == nil || !g.requireGitHubToken {
		return false, hubui.AgentAuthState{}
	}

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

func stripANSI(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	return ansiEscapePattern.ReplaceAllString(text, "")
}

func normalizeCodexStatusMessage(raw string) string {
	raw = strings.TrimSpace(stripANSI(raw))
	if raw == "" {
		return "Codex authorization is ready."
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "logged in using an api key"):
		return "Logged in with OpenAI API key."
	case strings.Contains(lower, "logged in"):
		return "Codex login is ready."
	default:
		return "Codex authorization is ready."
	}
}
