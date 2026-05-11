package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func clearOpencodeProviderEnv(t *testing.T) {
	t.Helper()
	for _, envVar := range opencodeProviderEnvVars {
		t.Setenv(envVar, "")
	}
}

func opencodeReadyInitCfg() hub.InitConfig {
	return hub.InitConfig{GitHubToken: fakeGitHubPAT("opencode_ready")}
}

func TestOpencodeAuthGateNilReceiversReturnReady(t *testing.T) {
	t.Parallel()

	var g *opencodeAuthGate
	for name, call := range map[string]func(context.Context) error{
		"Status": func(ctx context.Context) error {
			state, err := g.Status(ctx)
			if err == nil && (!state.Ready || state.Required || state.State != "ready") {
				err = errors.New("state not ready")
			}
			return err
		},
		"StartDeviceAuth": func(ctx context.Context) error {
			state, err := g.StartDeviceAuth(ctx)
			if err == nil && (!state.Ready || state.Required || state.State != "ready") {
				err = errors.New("state not ready")
			}
			return err
		},
		"Verify": func(ctx context.Context) error {
			state, err := g.Verify(ctx)
			if err == nil && (!state.Ready || state.Required || state.State != "ready") {
				err = errors.New("state not ready")
			}
			return err
		},
		"Configure": func(ctx context.Context) error {
			state, err := g.Configure(ctx, "")
			if err == nil && (!state.Ready || state.Required || state.State != "ready") {
				err = errors.New("state not ready")
			}
			return err
		},
	} {
		if err := call(context.Background()); err != nil {
			t.Fatalf("%s() error = %v", name, err)
		}
	}
}

func TestNewOpencodeAuthGateDefaultsCommandAndRequiresConfigure(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	g := newOpencodeAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})
	g.logf("covered")
	if got, want := g.command, agentruntime.HarnessOpencode; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.ConfigureCommand, claudeGitHubConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
}

func TestOpencodeAuthGateReadyFromProviderEnvironment(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "openai_key")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	g := newOpencodeAuthGateWithRuntime(nil, " opencode-custom ", "", opencodeReadyInitCfg(), nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.Message, "OpenCode provider auth is ready via OPENAI_API_KEY."; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
}

func TestOpencodeAuthGateReadyFromAuthFile(t *testing.T) {
	clearOpencodeProviderEnv(t)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	authPath := filepath.Join(dataHome, "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(authPath, []byte(`{"provider":"ready"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	g := newOpencodeAuthGateWithRuntime(nil, "opencode", "", opencodeReadyInitCfg(), nil)
	status, err := g.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !status.Ready || status.Message != opencodeAuthFileReadyMessage {
		t.Fatalf("status = %+v", status)
	}
}

func TestOpencodeAuthGateProbePaths(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var calls []execx.Command
	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			calls = append(calls, cmd)
			if isGitHubTokenValidationCommandForTest(cmd) {
				return execx.Result{Stdout: "github.com logged in"}, nil
			}
			if got, want := strings.Join(cmd.Args, " "), "auth list"; got != want {
				t.Fatalf("probe args = %q, want %q", got, want)
			}
			return execx.Result{Stdout: "anthropic  logged in"}, nil
		},
	}

	g := newOpencodeAuthGateWithRuntime(runner, "opencode", "", opencodeReadyInitCfg(), nil)
	status, err := g.StartDeviceAuth(nil)
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if !status.Ready || status.Message != opencodeProviderAuthReady {
		t.Fatalf("status = %+v", status)
	}
	if len(calls) == 0 {
		t.Fatal("runner was not called")
	}
}

func TestOpencodeAuthGateProbeUnavailableMessage(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if isGitHubTokenValidationCommandForTest(cmd) {
				return execx.Result{Stdout: "github.com logged in"}, nil
			}
			return execx.Result{}, errors.New("executable file not found")
		},
	}

	g := newOpencodeAuthGateWithRuntime(runner, "opencode", "", opencodeReadyInitCfg(), nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || !strings.Contains(status.Message, "OpenCode CLI was not found") {
		t.Fatalf("status = %+v", status)
	}
}

func TestOpencodeAuthGateConfigurePersistsGitHubTokenWhenRequired(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if isGitHubTokenValidationCommandForTest(cmd) {
				return execx.Result{Stdout: "github.com logged in"}, nil
			}
			return execx.Result{}, nil
		},
	}
	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newOpencodeAuthGateWithRuntime(runner, "opencode", configPath, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: agentruntime.HarnessOpencode,
	}, nil)

	githubToken := fakeGitHubPAT("opencode_saved")
	status, err := g.Configure(context.Background(), githubToken)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if status.Ready || status.ConfigureCommand != opencodeConfigureCommand {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv("GH_TOKEN"), githubToken; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
}

func TestOpencodeAuthGateConfigureReadyAndPendingPaths(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "openai_key")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	readyGate := newOpencodeAuthGateWithRuntime(nil, "opencode", "", opencodeReadyInitCfg(), nil)
	readyStatus, err := readyGate.Configure(context.Background(), "")
	if err != nil {
		t.Fatalf("Configure(ready) error = %v", err)
	}
	if !readyStatus.Ready {
		t.Fatalf("ready status = %+v", readyStatus)
	}

	clearOpencodeProviderEnv(t)
	pendingGate := newOpencodeAuthGateWithRuntime(nil, "opencode", "", opencodeReadyInitCfg(), nil)
	pendingStatus, err := pendingGate.Configure(context.Background(), "")
	if err != nil {
		t.Fatalf("Configure(pending) error = %v", err)
	}
	if pendingStatus.Ready || pendingStatus.ConfigureCommand != opencodeConfigureCommand {
		t.Fatalf("pending status = %+v", pendingStatus)
	}

	var nilGate *opencodeAuthGate
	nilGate.refreshLocked(context.Background())
}

func TestOpencodeHelpers(t *testing.T) {
	clearOpencodeProviderEnv(t)
	t.Setenv("XDG_DATA_HOME", " /tmp/opencode-data ")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_PROFILE", "dev")
	if got, want := firstConfiguredOpencodeProviderEnv(), "AWS_PROFILE"; got != want {
		t.Fatalf("firstConfiguredOpencodeProviderEnv() = %q, want %q", got, want)
	}

	candidates := opencodeAuthFileCandidates()
	if len(candidates) != 2 {
		t.Fatalf("len(opencodeAuthFileCandidates()) = %d, want 2", len(candidates))
	}
	if !strings.HasSuffix(candidates[0], filepath.Join("opencode", "auth.json")) {
		t.Fatalf("first candidate = %q", candidates[0])
	}

	for _, output := range []string{
		"",
		"no auth configured",
		"no provider found",
		"not authenticated",
		"not logged in",
		"login required",
		"empty",
	} {
		if hasOpencodeAuthListCredentials(output) {
			t.Fatalf("hasOpencodeAuthListCredentials(%q) = true, want false", output)
		}
	}
	if !hasOpencodeAuthListCredentials("anthropic logged in") {
		t.Fatal("hasOpencodeAuthListCredentials(valid) = false, want true")
	}
	if got := opencodeProbeConfigureMessage(nil); got != "" {
		t.Fatalf("opencodeProbeConfigureMessage(nil) = %q, want empty", got)
	}
	if got := opencodeProbeConfigureMessage(errors.New("no such file or directory")); !strings.Contains(got, "OpenCode CLI was not found") {
		t.Fatalf("opencodeProbeConfigureMessage(missing) = %q", got)
	}
	if got := opencodeProbeConfigureMessage(errors.New("exit status 1")); got != "" {
		t.Fatalf("opencodeProbeConfigureMessage(other) = %q, want empty", got)
	}
}

func TestFirstConfiguredOpencodeProviderEnvSkipsBlankConfiguredNames(t *testing.T) {
	clearOpencodeProviderEnv(t)
	oldVars := append([]string(nil), opencodeProviderEnvVars...)
	opencodeProviderEnvVars = append([]string{" "}, oldVars...)
	t.Cleanup(func() {
		opencodeProviderEnvVars = oldVars
	})
	t.Setenv(oldVars[0], "provider_key")

	if got, want := firstConfiguredOpencodeProviderEnv(), oldVars[0]; got != want {
		t.Fatalf("firstConfiguredOpencodeProviderEnv() = %q, want %q", got, want)
	}
}
