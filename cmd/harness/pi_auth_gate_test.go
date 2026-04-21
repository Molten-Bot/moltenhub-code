package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func clearPiAuthTestEnv(t *testing.T) {
	t.Helper()
	for _, option := range piProviderOptions {
		t.Setenv(option.EnvVar, "")
	}
	t.Setenv("PI_AUTH_JSON", "")
	t.Setenv("PI_PROVIDER_AUTH", "")
}

func piOKAuthGateRunner() *authGateRunnerStub {
	return &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{Stdout: "OK\n"}, nil
		},
	}
}

func TestNewPiAuthGateRequiresConfigureWhenExistingPiProbeFails(t *testing.T) {
	clearPiAuthTestEnv(t)
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("pi login failed")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{}, nil)

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got, want := status.Harness, agentruntime.HarnessPi; got != want {
		t.Fatalf("Harness = %q, want %q", got, want)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if len(status.ConfigureOptions) != 0 {
		t.Fatalf("ConfigureOptions = %v, want empty", status.ConfigureOptions)
	}
	if got, want := status.Message, piExistingLocalAuthValidationStatusMessage(); got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got, want := status.ConfigureCommand, piAuthConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	if got, want := status.ConfigurePlaceholder, piAuthConfigurePlaceholder; got != want {
		t.Fatalf("ConfigurePlaceholder = %q, want %q", got, want)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestNewPiAuthGateReadyWhenEnvironmentAlreadyConfigured(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{}, nil)

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestNewPiAuthGateRequiresGitHubConfigureWhenExistingPiAuthAlreadyWorks(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{}, nil)

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
	if got, want := status.Message, "GitHub token is required."; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestNewPiAuthGateReadyWhenExistingPiAuthAndGitHubTokenAlreadyWork(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{}, nil)

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := status.Message, piExistingLocalAuthReadyMessage; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestPiAuthGateConfigurePersistsRuntimeConfigAndEnvironment(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_token",
		AgentHarness: agentruntime.HarnessPi,
	}, nil)

	input := `{"env_var":"OPENAI_API_KEY","value":"sk-saved"}`
	expected, err := normalizePiProviderAuth(input)
	if err != nil {
		t.Fatalf("normalizePiProviderAuth() error = %v", err)
	}

	status, err := g.Configure(context.Background(), input)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got, want := os.Getenv("OPENAI_API_KEY"), "sk-saved"; got != want {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["pi_provider_auth"], expected; got != want {
		t.Fatalf("pi_provider_auth = %#v, want %q", got, want)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestPiAuthGateConfigurePersistsPiAuthJSONAndWritesAuthFile(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	input := `{"provider":"demo","token":"saved"}`
	expected, err := normalizePiAuthJSON(input)
	if err != nil {
		t.Fatalf("normalizePiAuthJSON() error = %v", err)
	}

	status, err := g.Configure(context.Background(), input)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["pi_auth_json"], expected; got != want {
		t.Fatalf("pi_auth_json = %#v, want %q", got, want)
	}

	authPath := filepath.Join(homeDir, ".pi", "agent", "auth.json")
	written, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(auth.json) error = %v", err)
	}
	if got, want := string(written), expected; got != want {
		t.Fatalf("auth.json = %q, want %q", got, want)
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestPiAuthGateConfigurePiAuthJSONAcceptsOKOutputDespiteProbeExitError(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{
				Stdout: "OK\n",
				Stderr: "No API key for provider: openai-codex\n",
			}, errors.New("run pi [--print --mode text --no-session Reply with OK.]: exit status 1")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	status, err := g.Configure(context.Background(), `{"provider":"demo","token":"saved"}`)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
}

func TestPiAuthGateConfigureProviderAcceptsPiOKOutputDespiteProbeExitError(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{
				Stdout: "OK\n",
				Stderr: "No API key for provider: openai-codex\n",
			}, errors.New("run pi [--print --mode text --no-session Reply with OK.]: exit status 1")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	status, err := g.Configure(context.Background(), `{"env_var":"OPENAI_API_KEY","value":"sk-saved"}`)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
}

func TestPiProbeResultHasOKAcceptsNoisyAgentOutput(t *testing.T) {
	res := execx.Result{
		Stdout: "assistant: OK\n",
		Stderr: "No API key for provider: openai-codex\n",
	}
	if !piProbeResultHasOK(res) {
		t.Fatal("piProbeResultHasOK() = false, want true")
	}
}

func TestPiProbeResultHasOKRejectsSubstringOnly(t *testing.T) {
	res := execx.Result{Stderr: "No API key for provider: openai-codex\nstatus=ok\n"}
	if piProbeResultHasOK(res) {
		t.Fatal("piProbeResultHasOK() = true, want false")
	}
}

func TestPiAuthGateConfigureProviderProbeFailureIncludesExplicitFailureFields(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{Stderr: "No API key for provider: openai-codex\n"}, errors.New("exit status 1")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	_, err := g.Configure(context.Background(), `{"env_var":"OPENAI_API_KEY","value":"sk-fail"}`)
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "Failure: PI probe failed.") {
		t.Fatalf("Configure() error = %q, want explicit failure field", err)
	}
	if !strings.Contains(err.Error(), "Error details:") {
		t.Fatalf("Configure() error = %q, want error details field", err)
	}
	if !strings.Contains(err.Error(), "No API key for provider: openai-codex") {
		t.Fatalf("Configure() error = %q, want command output detail", err)
	}
}

func TestPiAuthGateConfigureRejectsUnsupportedEnvVar(t *testing.T) {
	clearPiAuthTestEnv(t)
	g := newPiAuthGate(filepath.Join(t.TempDir(), ".moltenhub", "config.json"), hub.InitConfig{})

	if _, err := g.Configure(context.Background(), `{"env_var":"UNSUPPORTED_ENV","value":"x"}`); err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
}

func TestPiAuthGateConfigureOfflineModeAllowsEmptyTokenWithoutProbe(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "ghp_ready")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			t.Fatal("probe should not run for PI_OFFLINE provider configuration")
			return execx.Result{}, nil
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	input := `{"env_var":"PI_OFFLINE","value":""}`
	expected, err := normalizePiProviderAuth(input)
	if err != nil {
		t.Fatalf("normalizePiProviderAuth() error = %v", err)
	}

	status, err := g.Configure(context.Background(), input)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !status.Ready || status.State != "ready" {
		t.Fatalf("status = %+v", status)
	}
	if got := strings.TrimSpace(os.Getenv("PI_OFFLINE")); got != "1" {
		t.Fatalf("PI_OFFLINE = %q, want %q", got, "1")
	}
	if got := len(runner.calls); got != 0 {
		t.Fatalf("probe calls = %d, want 0", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := doc["pi_provider_auth"], expected; got != want {
		t.Fatalf("pi_provider_auth = %#v, want %q", got, want)
	}
}

func TestPiAuthGateConfigureTransitionsToGitHubConfigureWhenTokenMissing(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_token",
		AgentHarness: agentruntime.HarnessPi,
	}, nil)

	status, err := g.Configure(context.Background(), `{"provider":"demo","token":"saved"}`)
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
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
}

func TestPiAuthGateConfigureAcceptsGitHubTokenWhenRequired(t *testing.T) {
	clearPiAuthTestEnv(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := piOKAuthGateRunner()
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentToken:   "agent_token",
		AgentHarness: agentruntime.HarnessPi,
		PiAuthJSON:   `{"provider":"demo","token":"saved"}`,
	}, nil)

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}

	status, err = g.Configure(context.Background(), "ghp_saved_token")
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

func TestPiAuthGateStatusMasksProbeLaunchDetails(t *testing.T) {
	clearPiAuthTestEnv(t)

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New(`run pi [--print --mode text --no-session Reply with OK.]: exit status 1 (401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}})`)
		},
	}
	canonical, err := normalizePiProviderAuth(`{"env_var":"OPENAI_API_KEY","value":"sk-invalid"}`)
	if err != nil {
		t.Fatalf("normalizePiProviderAuth() error = %v", err)
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{PiProviderAuth: canonical}, nil)

	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if got := status.Message; strings.Contains(got, "run pi [--print --mode text --no-session") {
		t.Fatalf("status.Message = %q, want sanitized provider validation failure message", got)
	}
	if got := status.Message; !strings.Contains(got, "PI provider validation failed for OPENAI_API_KEY.") {
		t.Fatalf("status.Message = %q, want provider-specific validation guidance", got)
	}
}

func TestPiAuthGateConfigureReturnsLaunchFailureDetails(t *testing.T) {
	clearPiAuthTestEnv(t)

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("pi login failed")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	status, err := g.Configure(context.Background(), `{"env_var":"OPENAI_API_KEY","value":"sk-fail"}`)
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "PI provider validation failed. Error details:") {
		t.Fatalf("Configure() error = %q, want explicit failure prefix", err)
	}
	if !strings.Contains(err.Error(), "pi login failed") {
		t.Fatalf("Configure() error = %q, want launch failure detail", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "launch pi with OPENAI_API_KEY") {
		t.Fatalf("status.Message = %q, want launch detail", status.Message)
	}
	if got, want := os.Getenv("OPENAI_API_KEY"), "sk-fail"; got != want {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", got, want)
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := doc["pi_provider_auth"]; got == nil || got == "" {
		t.Fatalf("pi_provider_auth = %#v, want persisted value", got)
	}
}

func TestPiAgentAuthOptionsAreSortedAlphabeticallyByLabel(t *testing.T) {
	options := piAgentAuthOptions()
	if len(options) == 0 {
		t.Fatal("piAgentAuthOptions() returned no options")
	}
	for i := 1; i < len(options); i++ {
		prev := strings.ToLower(strings.TrimSpace(options[i-1].Label))
		curr := strings.ToLower(strings.TrimSpace(options[i].Label))
		if prev > curr {
			t.Fatalf("piAgentAuthOptions() not sorted at index %d: %q > %q", i, options[i-1].Label, options[i].Label)
		}
	}
}

func TestPiAuthGateConfigureOpenRouterPATFailureIncludesActionableGuidance(t *testing.T) {
	clearPiAuthTestEnv(t)

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			return execx.Result{}, errors.New("run pi [--print --mode text --no-session Reply with OK.]: exit status 1 (400 checking third-party user token: bad request: Personal Access Tokens are not supported for this endpoint)")
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	status, err := g.Configure(context.Background(), `{"env_var":"OPENROUTER_API_KEY","value":"sk-or-v1-demo"}`)
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "PI provider validation failed. Error details:") {
		t.Fatalf("Configure() error = %q, want explicit failure prefix", err)
	}
	if !strings.Contains(err.Error(), "Personal Access Tokens are not supported for this endpoint") {
		t.Fatalf("Configure() error = %q, want OpenRouter PAT detail", err)
	}
	if !strings.Contains(err.Error(), "Use an OpenRouter API key for OPENROUTER_API_KEY") {
		t.Fatalf("Configure() error = %q, want actionable OpenRouter guidance", err)
	}
	if !strings.Contains(err.Error(), openRouterProviderSetupDocURL) {
		t.Fatalf("Configure() error = %q, want setup documentation link", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "launch pi with OPENROUTER_API_KEY") {
		t.Fatalf("status.Message = %q, want OpenRouter launch detail", status.Message)
	}
	if !strings.Contains(status.Message, "Use an OpenRouter API key for OPENROUTER_API_KEY") {
		t.Fatalf("status.Message = %q, want OpenRouter remediation detail", status.Message)
	}
}

func TestPiAuthGateConfigureRejectsOpenRouterPersonalAccessTokenBeforeProbe(t *testing.T) {
	clearPiAuthTestEnv(t)

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	runner := &authGateRunnerStub{
		run: func(context.Context, execx.Command) (execx.Result, error) {
			t.Fatal("probe should not run when OPENROUTER_API_KEY format is invalid")
			return execx.Result{}, nil
		},
	}
	g := newPiAuthGateWithRuntime(runner, "pi", path, hub.InitConfig{}, nil)

	status, err := g.Configure(context.Background(), `{"env_var":"OPENROUTER_API_KEY","value":"pat_demo_token"}`)
	if err == nil {
		t.Fatal("Configure() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY must be an OpenRouter API key") {
		t.Fatalf("Configure() error = %q, want OpenRouter API key guidance", err)
	}
	if !strings.Contains(err.Error(), "Personal Access Token") {
		t.Fatalf("Configure() error = %q, want PAT guidance", err)
	}
	if !strings.Contains(err.Error(), openRouterProviderSetupDocURL) {
		t.Fatalf("Configure() error = %q, want setup documentation link", err)
	}
	if status.Ready || status.State != "needs_configure" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "PI provider auth is invalid") {
		t.Fatalf("status.Message = %q, want invalid auth detail", status.Message)
	}
	if got := len(runner.calls); got != 0 {
		t.Fatalf("probe calls = %d, want 0", got)
	}
	if got := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); got != "" {
		t.Fatalf("OPENROUTER_API_KEY = %q, want empty", got)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("runtime config path should not be written for invalid OpenRouter token, stat err = %v", statErr)
	}
}

func TestIsLikelyOpenRouterAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "v1 key", value: "sk-or-v1-demo", want: true},
		{name: "v10 key", value: "sk-or-v10-demo", want: true},
		{name: "trimmed whitespace", value: "  sk-or-v1-demo  ", want: true},
		{name: "missing prefix", value: "sk-demo", want: false},
		{name: "missing version digits", value: "sk-or-v-demo", want: false},
		{name: "missing separator after version", value: "sk-or-v1demo", want: false},
		{name: "missing key body", value: "sk-or-v1-", want: false},
		{name: "personal token marker", value: "pat_demo_token", want: false},
	}

	for _, tt := range tests {
		if got := isLikelyOpenRouterAPIKey(tt.value); got != tt.want {
			t.Fatalf("%s: isLikelyOpenRouterAPIKey(%q) = %v, want %v", tt.name, tt.value, got, tt.want)
		}
	}
}
