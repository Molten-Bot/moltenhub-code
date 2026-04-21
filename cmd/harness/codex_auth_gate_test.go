package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/hub"
)

type authGateRunnerStub struct {
	run   func(context.Context, execx.Command) (execx.Result, error)
	calls []execx.Command
}

func (s *authGateRunnerStub) Run(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	s.calls = append(s.calls, cmd)
	if s.run == nil {
		return execx.Result{}, nil
	}
	return s.run(ctx, cmd)
}

func codexGitHubReadyInitCfg() hub.InitConfig {
	return hub.InitConfig{GitHubToken: "ghp_ready"}
}

func TestCodexAuthGateNilReceiversReturnReady(t *testing.T) {
	t.Parallel()

	var g *codexAuthGate
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.Required || status.State != "ready" {
		t.Fatalf("Status() = %+v", status)
	}

	status, err = g.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if !status.Ready || status.Required || status.State != "ready" {
		t.Fatalf("StartDeviceAuth() = %+v", status)
	}

	status, err = g.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !status.Ready || status.Required || status.State != "ready" {
		t.Fatalf("Verify() = %+v", status)
	}
}

func TestNewCodexAuthGateUsesProbeResultWhenReady(t *testing.T) {
	t.Parallel()

	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, agentruntime.HarnessCodex; got != want {
				t.Fatalf("probe command = %q, want %q", got, want)
			}
			if got, want := strings.Join(cmd.Args, " "), "login status"; got != want {
				t.Fatalf("probe args = %q, want %q", got, want)
			}
			return execx.Result{Stdout: "Logged in using an API key."}, nil
		},
	}

	g := newCodexAuthGateWithConfig(context.Background(), runner, "", "", codexGitHubReadyInitCfg(), true, nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.Message, "Logged in with OpenAI API key."; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestNewCodexAuthGateSetsErrorWhenCLIUnavailable(t *testing.T) {
	t.Parallel()

	runner := &authGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("executable file not found in $PATH")
		},
	}

	g := newCodexAuthGateWithConfig(context.Background(), runner, "codex", "", codexGitHubReadyInitCfg(), true, nil)
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || status.State != "error" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "codex CLI is not available on PATH") {
		t.Fatalf("message = %q", status.Message)
	}
}

func TestNewCodexAuthGateRequiresGitHubConfigureWhenTokenMissing(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &authGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{Stdout: "logged in"}, nil
		},
	}

	g := newCodexAuthGateWithConfig(
		context.Background(),
		runner,
		"codex",
		filepath.Join(t.TempDir(), ".moltenhub", "config.json"),
		hub.InitConfig{},
		true,
		nil,
	)
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

func TestCodexAuthGateConfigurePersistsGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &authGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{Stdout: "logged in"}, nil
		},
	}

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newCodexAuthGateWithConfig(
		context.Background(),
		runner,
		"codex",
		configPath,
		hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentHarness: "codex",
		},
		true,
		nil,
	)

	status, err := g.Configure(context.Background(), "ghp_saved_token")
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv("GH_TOKEN"), "ghp_saved_token"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "ghp_saved_token"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["github_token"], "ghp_saved_token"; got != want {
		t.Fatalf("github_token = %#v, want %q", got, want)
	}
}

func TestCodexAuthGateConfigureRejectsInvalidGitHubTokenBeforePersisting(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_existing_token")
	t.Setenv("GITHUB_TOKEN", "ghp_existing_token")

	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if cmd.Name == "gh" && strings.Join(cmd.Args, " ") == "auth status" {
				return execx.Result{Stderr: "X Failed to log in to github.com using token (GH_TOKEN)"}, errors.New("token invalid")
			}
			return execx.Result{}, nil
		},
	}

	configPath := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	g := newCodexAuthGateWithConfig(
		context.Background(),
		runner,
		"codex",
		configPath,
		hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentHarness: "codex",
		},
		true,
		nil,
	)

	status, err := g.Configure(context.Background(), "ghp_invalid_token")
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(err.Error(), "validate github token") {
		t.Fatalf("Configure() error = %q, want token validation failure", err)
	}
	if got, want := os.Getenv("GH_TOKEN"), "ghp_existing_token"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "ghp_existing_token"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}

	data, readErr := os.ReadFile(configPath)
	if readErr == nil {
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if _, exists := doc["github_token"]; exists {
			t.Fatalf("github_token unexpectedly persisted in config: %#v", doc["github_token"])
		}
	}
}

func TestStartDeviceAuthRespectsExistingState(t *testing.T) {
	t.Parallel()

	t.Run("ready", func(t *testing.T) {
		t.Parallel()
		g := &codexAuthGate{required: true, ready: true, state: "ready", message: "ok", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		status, err := g.StartDeviceAuth(context.Background())
		if err != nil {
			t.Fatalf("StartDeviceAuth() error = %v", err)
		}
		if !status.Ready || status.State != "ready" {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		g := &codexAuthGate{required: true, state: "error", message: "boom", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		status, err := g.StartDeviceAuth(context.Background())
		if err == nil {
			t.Fatal("StartDeviceAuth() error = nil, want non-nil")
		}
		if status.State != "error" {
			t.Fatalf("status = %+v", status)
		}
		if !strings.Contains(err.Error(), "cannot start codex device auth") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("already-running", func(t *testing.T) {
		t.Parallel()
		g := &codexAuthGate{required: true, procRunning: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		status, err := g.StartDeviceAuth(context.Background())
		if err != nil {
			t.Fatalf("StartDeviceAuth() error = %v", err)
		}
		if status.State != "pending_device_auth" {
			t.Fatalf("status = %+v", status)
		}
	})
}

func TestStartDeviceAuthFailsWhenTempDirCannotBeCreated(t *testing.T) {
	badTempRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badTempRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("TMPDIR", badTempRoot)

	g := &codexAuthGate{baseCtx: context.Background(), command: "sh", required: true, state: "needs_device_auth", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
	_, err := g.StartDeviceAuth(context.Background())
	if err == nil {
		t.Fatal("StartDeviceAuth() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "create codex device auth temp dir") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartDeviceAuthFailsWhenCommandCannotStart(t *testing.T) {
	t.Parallel()

	g := &codexAuthGate{baseCtx: context.Background(), command: filepath.Join(t.TempDir(), "missing-codex"), required: true, state: "needs_device_auth", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
	status, err := g.StartDeviceAuth(context.Background())
	if err == nil {
		t.Fatal("StartDeviceAuth() error = nil, want non-nil")
	}
	if status.State != "error" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "start codex device auth") {
		t.Fatalf("message = %q", status.Message)
	}
}

func TestStartDeviceAuthStreamsAndCompletes(t *testing.T) {
	t.Parallel()

	g := &codexAuthGate{baseCtx: context.Background(), command: "true", required: true, state: "needs_device_auth", message: "auth required", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}

	status, err := g.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if status.State != "pending_device_auth" {
		t.Fatalf("initial status = %+v", status)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return s.State == "ready" && s.Ready
	})
}

func TestStartDeviceAuthFailureRequiresRetryWhenHintsMissing(t *testing.T) {
	t.Parallel()

	g := &codexAuthGate{baseCtx: context.Background(), command: "false", required: true, state: "needs_device_auth", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}

	if _, err := g.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return s.State == "needs_device_auth" && !s.Ready
	})
}

func TestVerifyTransitions(t *testing.T) {
	t.Parallel()

	t.Run("probe-error", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("command not found")
		}}
		g := &codexAuthGate{baseCtx: context.Background(), runner: runner, command: "codex", required: true, state: "needs_device_auth", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		status, err := g.Verify(context.Background())
		if err == nil {
			t.Fatal("Verify() error = nil, want non-nil")
		}
		if status.State != "error" {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("ready-cancels-pending-process", func(t *testing.T) {
		t.Parallel()
		canceled := false
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{Stdout: "logged in"}, nil
		}}
		g := &codexAuthGate{
			baseCtx:     context.Background(),
			runner:      runner,
			command:     "codex",
			required:    true,
			state:       "pending_device_auth",
			procRunning: true,
			procCancel:  func() { canceled = true },
			updatedAt:   time.Now().UTC(),
			initCfg:     codexGitHubReadyInitCfg(),
		}
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if !status.Ready || status.State != "ready" {
			t.Fatalf("status = %+v", status)
		}
		if !canceled {
			t.Fatal("expected running process cancel callback")
		}
	})

	t.Run("still-pending", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("not logged in")
		}}
		g := &codexAuthGate{baseCtx: context.Background(), runner: runner, command: "codex", required: true, procRunning: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if status.State != "pending_device_auth" || status.Ready {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("needs-device-auth", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("not logged in")
		}}
		g := &codexAuthGate{baseCtx: context.Background(), runner: runner, command: "codex", required: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		status, err := g.Verify(context.Background())
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if status.State != "needs_device_auth" || status.Ready {
			t.Fatalf("status = %+v", status)
		}
		if !strings.Contains(strings.ToLower(status.Message), "not logged in") {
			t.Fatalf("message = %q", status.Message)
		}
	})
}

func TestProbeClassificationAndHelpers(t *testing.T) {
	t.Parallel()

	var nilGate *codexAuthGate
	if ready, msg, err := nilGate.probe(context.Background()); !ready || err != nil || msg == "" {
		t.Fatalf("nil probe = (%v, %q, %v)", ready, msg, err)
	}

	t.Run("probe-fallback-message", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{Stderr: "auth check failed"}, errors.New("exit status 1")
		}}
		g := &codexAuthGate{runner: runner, command: "codex"}
		ready, msg, err := g.probe(nil)
		if err != nil || ready {
			t.Fatalf("probe() = (%v, %q, %v)", ready, msg, err)
		}
		if got, want := msg, "auth check failed"; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	t.Run("probe-api-key-classification", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("logged in using api key")
		}}
		g := &codexAuthGate{runner: runner, command: "codex"}
		ready, msg, err := g.probe(context.Background())
		if err != nil || !ready {
			t.Fatalf("probe() = (%v, %q, %v)", ready, msg, err)
		}
		if got, want := msg, "Codex authorization is ready."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	t.Run("probe-not-logged-in-classification", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("NOT LOGGED IN")
		}}
		g := &codexAuthGate{runner: runner, command: "codex"}
		ready, msg, err := g.probe(context.Background())
		if err != nil || ready {
			t.Fatalf("probe() = (%v, %q, %v)", ready, msg, err)
		}
		if !strings.Contains(strings.ToLower(msg), "not logged in") {
			t.Fatalf("message = %q", msg)
		}
	})

	t.Run("probe-command-not-found", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("command not found")
		}}
		g := &codexAuthGate{runner: runner, command: "codex"}
		ready, msg, err := g.probe(context.Background())
		if err == nil || ready || msg != "" {
			t.Fatalf("probe() = (%v, %q, %v), want command-not-found error", ready, msg, err)
		}
	})

	t.Run("probe-logged-in-from-error-text", func(t *testing.T) {
		t.Parallel()
		runner := &authGateRunnerStub{run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("already logged in")
		}}
		g := &codexAuthGate{runner: runner, command: "codex"}
		ready, msg, err := g.probe(context.Background())
		if err != nil || !ready {
			t.Fatalf("probe() = (%v, %q, %v)", ready, msg, err)
		}
		if got, want := msg, "Codex authorization is ready."; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})

	if got, want := stripANSI("\x1b[31merror\x1b[0m"), "error"; got != want {
		t.Fatalf("stripANSI() = %q, want %q", got, want)
	}
	if got, want := firstNonEmptyString("  ", "\n", "value"), "value"; got != want {
		t.Fatalf("firstNonEmptyString() = %q, want %q", got, want)
	}
	if got := firstNonEmptyString(" ", "\t"); got != "" {
		t.Fatalf("firstNonEmptyString(all empty) = %q, want empty", got)
	}
	if got, want := normalizeCodexStatusMessage("logged in using an API key"), "Logged in with OpenAI API key."; got != want {
		t.Fatalf("normalizeCodexStatusMessage(api key) = %q, want %q", got, want)
	}
	if got, want := normalizeCodexStatusMessage("logged in"), "Codex login is ready."; got != want {
		t.Fatalf("normalizeCodexStatusMessage(logged in) = %q, want %q", got, want)
	}
	if got, want := normalizeCodexStatusMessage("  \x1b[32munknown\x1b[0m  "), "Codex authorization is ready."; got != want {
		t.Fatalf("normalizeCodexStatusMessage(default) = %q, want %q", got, want)
	}
}

func TestReadAndIngestDeviceAuthStream(t *testing.T) {
	t.Parallel()

	g := &codexAuthGate{required: true, state: "needs_device_auth", updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
	g.readDeviceAuthStream(io.NopCloser(strings.NewReader("noise\nVisit https://auth.openai.com/device\nCode: abcd-efgh\n")))

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.AuthURL, "https://auth.openai.com/device"; got != want {
		t.Fatalf("authURL = %q, want %q", got, want)
	}
	if got, want := status.DeviceCode, "ABCD-EFGH"; got != want {
		t.Fatalf("deviceCode = %q, want %q", got, want)
	}
	if status.State != "pending_device_auth" {
		t.Fatalf("status = %+v", status)
	}

	before := status.UpdatedAt
	g.ingestDeviceAuthLine("no auth data here")
	after, _ := g.Status(context.Background())
	if after.UpdatedAt != before {
		t.Fatalf("UpdatedAt changed on irrelevant line: before=%q after=%q", before, after.UpdatedAt)
	}
}

func TestWaitDeviceAuthHandlesExitScenarios(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("sh", "-c", "exit 0")
		if err := cmd.Start(); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		g := &codexAuthGate{required: true, state: "pending_device_auth", procRunning: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		g.waitDeviceAuth(cmd, t.TempDir())
		status, _ := g.Status(context.Background())
		if !status.Ready || status.State != "ready" {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("failure-with-canceled-base-context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		cmd := exec.Command("sh", "-c", "exit 7")
		if err := cmd.Start(); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		g := &codexAuthGate{baseCtx: ctx, required: true, state: "pending_device_auth", procRunning: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		g.waitDeviceAuth(cmd, t.TempDir())
		status, _ := g.Status(context.Background())
		if status.State != "pending_device_auth" {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("failure-without-auth-hints", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("sh", "-c", "exit 8")
		if err := cmd.Start(); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		g := &codexAuthGate{required: true, state: "pending_device_auth", procRunning: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
		g.waitDeviceAuth(cmd, t.TempDir())
		status, _ := g.Status(context.Background())
		if status.State != "needs_device_auth" {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("failure-with-auth-hints-keeps-pending", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("sh", "-c", "exit 9")
		if err := cmd.Start(); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		g := &codexAuthGate{
			required:    true,
			state:       "pending_device_auth",
			procRunning: true,
			authURL:     "https://auth.openai.com/device",
			deviceCode:  "ABCD-EFGH",
			updatedAt:   time.Now().UTC(),
			initCfg:     codexGitHubReadyInitCfg(),
		}
		g.waitDeviceAuth(cmd, t.TempDir())
		status, _ := g.Status(context.Background())
		if status.State != "pending_device_auth" {
			t.Fatalf("status = %+v", status)
		}
	})
}

func TestCodexAuthGateSnapshotAndIngestEdgeCases(t *testing.T) {
	t.Parallel()

	var nilGate *codexAuthGate
	nilGate.ingestDeviceAuthLine("https://auth.openai.com/device CODE-1234")

	g := &codexAuthGate{required: true, ready: true, updatedAt: time.Now().UTC(), initCfg: codexGitHubReadyInitCfg()}
	if got := g.snapshotLocked().State; got != "ready" {
		t.Fatalf("snapshotLocked().State = %q, want ready", got)
	}
	g.ready = false
	if got := g.snapshotLocked().State; got != "needs_device_auth" {
		t.Fatalf("snapshotLocked().State = %q, want needs_device_auth", got)
	}

	before := g.updatedAt
	g.ingestDeviceAuthLine("   ")
	g.ingestDeviceAuthLine("no matching auth fields")
	if !g.updatedAt.Equal(before) {
		t.Fatalf("updatedAt changed for non-matching lines: before=%s after=%s", before, g.updatedAt)
	}
}

func TestCodexAuthGateStatusAutoStartsDeviceAuthWhenChallengeMissing(t *testing.T) {
	t.Parallel()

	g := &codexAuthGate{
		baseCtx:   context.Background(),
		command:   "true",
		required:  true,
		state:     "needs_device_auth",
		message:   "Codex is not logged in. Device authorization is required.",
		updatedAt: time.Now().UTC(),
		initCfg:   codexGitHubReadyInitCfg(),
	}

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := status.State; got == "needs_device_auth" {
		t.Fatalf("Status() did not auto-start device auth: %+v", status)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return s.Ready && s.State == "ready"
	})
}

func TestCodexAuthGateStatusDebouncesAutoStartAttempts(t *testing.T) {
	badTempRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badTempRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("TMPDIR", badTempRoot)

	g := &codexAuthGate{
		baseCtx:   context.Background(),
		command:   "true",
		required:  true,
		state:     "needs_device_auth",
		message:   "Codex is not logged in. Device authorization is required.",
		updatedAt: time.Now().UTC(),
		initCfg:   codexGitHubReadyInitCfg(),
	}

	if _, err := g.Status(context.Background()); err != nil {
		t.Fatalf("first Status() error = %v", err)
	}
	firstAttempt := g.lastAutoStartAttempt
	if firstAttempt.IsZero() {
		t.Fatal("first Status() did not record auto-start attempt")
	}

	if _, err := g.Status(context.Background()); err != nil {
		t.Fatalf("second Status() error = %v", err)
	}
	if !g.lastAutoStartAttempt.Equal(firstAttempt) {
		t.Fatalf("second Status() should not re-attempt immediately: first=%s second=%s", firstAttempt, g.lastAutoStartAttempt)
	}
}

func TestNewCodexAuthGateAutoStartsWhenNotLoggedIn(t *testing.T) {
	t.Parallel()

	runner := &authGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("not logged in")
		},
	}

	g := newCodexAuthGateWithConfig(context.Background(), runner, "true", "", codexGitHubReadyInitCfg(), true, nil)
	waitForCondition(t, 2*time.Second, func() bool {
		s, _ := g.Status(context.Background())
		return s.Ready && s.State == "ready"
	})
}

func TestNewCodexAuthGateWithConfigRequiresGitHubTokenWhenMissing(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &authGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("probe should not run when github token is missing")
		},
	}

	g := newCodexAuthGateWithConfig(
		context.Background(),
		runner,
		"codex",
		filepath.Join(t.TempDir(), ".moltenhub", "config.json"),
		hub.InitConfig{},
		true,
		nil,
	)
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
	if got, want := status.ConfigurePlaceholder, claudeGitHubConfigurePlaceholder; got != want {
		t.Fatalf("ConfigurePlaceholder = %q, want %q", got, want)
	}
	if got := len(runner.calls); got != 0 {
		t.Fatalf("probe calls = %d, want 0", got)
	}
}

func TestCodexAuthGateWithConfigConfigurePersistsGitHubTokenAndTransitionsState(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if cmd.Name == "gh" && strings.Join(cmd.Args, " ") == "auth status" {
				return execx.Result{Stdout: "Logged in to github.com as octocat"}, nil
			}
			return execx.Result{}, errors.New("not logged in")
		},
	}

	g := newCodexAuthGateWithConfig(
		context.Background(),
		runner,
		"codex",
		path,
		hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentToken:   "agent_token",
			AgentHarness: agentruntime.HarnessCodex,
		},
		true,
		nil,
	)

	status, err := g.Configure(context.Background(), "ghp_saved_token")
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if status.Ready || status.State != "needs_device_auth" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv("GH_TOKEN"), "ghp_saved_token"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "ghp_saved_token"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["github_token"], "ghp_saved_token"; got != want {
		t.Fatalf("github_token = %#v, want %q", got, want)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
