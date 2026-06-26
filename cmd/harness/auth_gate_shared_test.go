package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

type sharedAuthGateRunnerStub struct {
	run func(context.Context, execx.Command) (execx.Result, error)
}

func (s *sharedAuthGateRunnerStub) Run(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	if s.run == nil {
		return execx.Result{}, nil
	}
	return s.run(ctx, cmd)
}

func fakeGitHubPAT(suffix string) string {
	return "ghp_" + suffix
}

func gitHubTokenValidationArgsStringForTest() string {
	return strings.Join(gitHubTokenValidationCommand().Args, " ")
}

func isGitHubTokenValidationCommandForTest(cmd execx.Command) bool {
	return cmd.Name == "gh" && strings.Join(cmd.Args, " ") == gitHubTokenValidationArgsStringForTest()
}

func useGitHubStarTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	previousBaseURL := githubStarAPIBaseURL
	previousClient := githubStarHTTPClient
	githubStarAPIBaseURL = server.URL
	githubStarHTTPClient = server.Client()
	t.Cleanup(func() {
		githubStarAPIBaseURL = previousBaseURL
		githubStarHTTPClient = previousClient
	})
}

func useSuccessfulGitHubStarTestServer(t *testing.T) {
	t.Helper()
	useGitHubStarTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPut; got != want {
			t.Fatalf("star method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, moltenHubCodeStarPath; got != want {
			t.Fatalf("star path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Fatalf("Authorization header missing bearer prefix")
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestFirstConfiguredGitHubTokenPrefersEnvironmentOverRuntimeConfig(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_env_token")
	t.Setenv("GITHUB_TOKEN", "github_token_env_token_alt")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"github_token":"github_token_runtime_token"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	got, source := firstConfiguredGitHubToken(path, hub.InitConfig{GitHubToken: "github_token_init_token"})
	if want := "github_token_env_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "environment"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestFirstConfiguredGitHubTokenPrefersRuntimeConfigOverInitConfig(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"github_token":"github_token_runtime_token"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	got, source := firstConfiguredGitHubToken(path, hub.InitConfig{GitHubToken: "github_token_init_token"})
	if want := "github_token_runtime_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "runtime config"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestFirstConfiguredGitHubTokenFallsBackToGHAndGITHUBEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_env_token")
	t.Setenv("GITHUB_TOKEN", "github_token_env_token_alt")

	got, source := firstConfiguredGitHubToken(filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{})
	if want := "github_token_env_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "environment"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}

	t.Setenv("GH_TOKEN", "")
	got, source = firstConfiguredGitHubToken(filepath.Join(t.TempDir(), "missing-2.json"), hub.InitConfig{})
	if want := "github_token_env_token_alt"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "environment"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestFirstConfiguredGitHubTokenAcceptsMalformedDockerComposeEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN:github_token_env_token", "")

	got, source := firstConfiguredGitHubToken(filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{})
	if want := "github_token_env_token"; got != want {
		t.Fatalf("firstConfiguredGitHubToken() value = %q, want %q", got, want)
	}
	if want := "environment"; source != want {
		t.Fatalf("firstConfiguredGitHubToken() source = %q, want %q", source, want)
	}
}

func TestSetGitHubTokenEnvironmentTrimsAndRejectsEmpty(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	if err := setGitHubTokenEnvironment("  github_token_configured  "); err != nil {
		t.Fatalf("setGitHubTokenEnvironment() error = %v", err)
	}
	if got, want := os.Getenv("GH_TOKEN"), "github_token_configured"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_configured"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}

	if err := setGitHubTokenEnvironment(" \t "); err == nil || err.Error() != "github token is required" {
		t.Fatalf("setGitHubTokenEnvironment(empty) error = %v, want required", err)
	}
}

func TestIsLikelyGitHubTokenRecognizesSupportedPrefixes(t *testing.T) {
	t.Parallel()

	for _, token := range []string{
		"ghp_example",
		"gho_example",
		"ghu_example",
		"ghs_example",
		"ghr_example",
		"github_pat_example",
		"  ghp_trimmed  ",
	} {
		token := token
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			if !isLikelyGitHubToken(token) {
				t.Fatalf("isLikelyGitHubToken(%q) = false, want true", token)
			}
		})
	}

	for _, token := range []string{"", "github_token_plain", "pat_github", "xghp_prefix"} {
		token := token
		t.Run("reject_"+token, func(t *testing.T) {
			t.Parallel()
			if isLikelyGitHubToken(token) {
				t.Fatalf("isLikelyGitHubToken(%q) = true, want false", token)
			}
		})
	}
}

func TestConfigurableAgentAuthStateSharedTransitions(t *testing.T) {
	var state configurableAgentAuthState
	options := []web.AgentAuthOption{{Value: "OPENAI_API_KEY", Label: "OpenAI"}}

	state.setError(" failed ")
	if !state.required || state.ready || state.state != "error" || state.message != "failed" {
		t.Fatalf("setError() state = %+v", state)
	}

	state.setConfigureUI(" paste token ", " command ", " placeholder ", options)
	options[0].Label = "mutated"
	if !state.required || state.ready || state.state != "needs_configure" {
		t.Fatalf("setConfigureUI() state = %+v", state)
	}
	if state.message != "paste token" || state.configureCommand != "command" || state.configurePlaceholder != "placeholder" {
		t.Fatalf("setConfigureUI() config fields = %+v", state)
	}
	if got := state.configureOptions[0].Label; got != "OpenAI" {
		t.Fatalf("setConfigureUI() option label = %q, want copied OpenAI", got)
	}

	state.setReady(" ready ")
	if !state.required || !state.ready || state.state != "ready" || state.message != "ready" {
		t.Fatalf("setReady() state = %+v", state)
	}
	if state.configureCommand != "" || state.configurePlaceholder != "" || state.configureOptions != nil {
		t.Fatalf("setReady() configure fields = %+v", state)
	}
}

func TestConfigurableAgentAuthGateBaseSnapshotAndNilConfigure(t *testing.T) {
	t.Parallel()

	var nilGate *configurableAgentAuthGateBase
	state, err := nilGate.configureGitHubTokenAndRefresh(context.Background(), "codex", "ignored", nil)
	if err != nil {
		t.Fatalf("configureGitHubTokenAndRefresh(nil) error = %v", err)
	}
	if !state.Ready || state.Required || state.State != "ready" {
		t.Fatalf("configureGitHubTokenAndRefresh(nil) state = %+v, want ready", state)
	}

	gate := &configurableAgentAuthGateBase{}
	gate.authState.setConfigureUI(
		" paste ",
		" configure ",
		" token ",
		[]web.AgentAuthOption{{Value: "one", Label: "One"}},
	)
	snapshot := gate.snapshotLocked("claude")
	if got, want := snapshot.Harness, "claude"; got != want {
		t.Fatalf("Harness = %q, want %q", got, want)
	}
	if got, want := snapshot.Message, "paste"; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got, want := snapshot.ConfigureCommand, "configure"; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
	snapshot.ConfigureOptions[0].Label = "mutated"
	if got, want := gate.authState.configureOptions[0].Label, "One"; got != want {
		t.Fatalf("snapshot options alias state: got %q, want %q", got, want)
	}
}

func TestApplyGitHubTokenRequirementStateConfiguresPromptWhenMissing(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	var state configurableAgentAuthState
	token, blocked := applyGitHubTokenRequirementState(context.Background(), nil, &state, "test", filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{})
	if token != "" || !blocked {
		t.Fatalf("applyGitHubTokenRequirementState() = token %q blocked %v, want empty true", token, blocked)
	}
	snapshot := state.snapshot("test")
	if snapshot.Ready || snapshot.State != "needs_configure" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got, want := snapshot.ConfigureCommand, claudeGitHubConfigureCommand; got != want {
		t.Fatalf("ConfigureCommand = %q, want %q", got, want)
	}
}

func TestGitHubTokenRequirementStateAcceptsValidatedStartupToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := cmd.Name, "gh"; got != want {
				t.Fatalf("command = %q, want %q", got, want)
			}
			if got, want := strings.Join(cmd.Args, " "), gitHubTokenValidationArgsStringForTest(); got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}

	blocked, state := githubTokenRequirementState(context.Background(), runner, "test", filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{GitHubToken: "github_token_valid"})
	if blocked {
		t.Fatalf("githubTokenRequirementState() blocked = true, state = %+v", state)
	}
	if got, want := os.Getenv("GH_TOKEN"), "github_token_valid"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_valid"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}
}

func TestGitHubTokenRequirementStateRejectsInvalidStartupToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			if got, want := strings.Join(cmd.Args, " "), gitHubTokenValidationArgsStringForTest(); got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			return execx.Result{Stderr: "bad credentials"}, errors.New("token invalid")
		},
	}

	var state configurableAgentAuthState
	token, blocked := applyGitHubTokenRequirementState(context.Background(), runner, &state, "test", filepath.Join(t.TempDir(), "missing.json"), hub.InitConfig{GitHubToken: "github_token_invalid"})
	if token != "" || !blocked {
		t.Fatalf("applyGitHubTokenRequirementState() = token %q blocked %v, want empty true", token, blocked)
	}
	snapshot := state.snapshot("test")
	if snapshot.Ready || snapshot.State != "needs_configure" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !strings.Contains(snapshot.Message, "validate github token") {
		t.Fatalf("message = %q, want validation failure", snapshot.Message)
	}
	if got := os.Getenv("GH_TOKEN"); got != "" {
		t.Fatalf("GH_TOKEN = %q, want empty", got)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "" {
		t.Fatalf("GITHUB_TOKEN = %q, want empty", got)
	}
}

func TestValidateGitHubTokenUsesLegacyCompatibleStatusCommand(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	var calls []string
	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, cmd execx.Command) (execx.Result, error) {
			calls = append(calls, strings.Join(cmd.Args, " "))
			if got, want := strings.Join(cmd.Args, " "), "auth status --hostname github.com"; got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			if got, want := os.Getenv("GH_TOKEN"), "github_token_valid"; got != want {
				t.Fatalf("GH_TOKEN during validation = %q, want %q", got, want)
			}
			if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_valid"; got != want {
				t.Fatalf("GITHUB_TOKEN during validation = %q, want %q", got, want)
			}
			return execx.Result{Stdout: "github.com logged in"}, nil
		},
	}

	if err := validateGitHubToken(context.Background(), runner, "github_token_valid"); err != nil {
		t.Fatalf("validateGitHubToken() error = %v", err)
	}
	wantCalls := []string{
		gitHubTokenValidationArgsStringForTest(),
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	if got := os.Getenv("GH_TOKEN"); got != "" {
		t.Fatalf("GH_TOKEN after validation = %q, want restored empty", got)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "" {
		t.Fatalf("GITHUB_TOKEN after validation = %q, want restored empty", got)
	}
}

func TestConfigureGitHubTokenStarsMoltenHubCodeBeforePersisting(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	githubToken := fakeGitHubPAT("star_token")
	starred := false
	useGitHubStarTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		starred = true
		if got, want := r.Method, http.MethodPut; got != want {
			t.Fatalf("star method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, moltenHubCodeStarPath; got != want {
			t.Fatalf("star path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer "+githubToken; got != want {
			t.Fatalf("Authorization header mismatch: got length %d, want length %d", len(got), len(want))
		}
		w.WriteHeader(http.StatusNoContent)
	})

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	token, state, err := configureGitHubToken(context.Background(), "test", path, hub.InitConfig{}, nil, githubToken, "")
	if err != nil {
		t.Fatalf("configureGitHubToken() error = %v", err)
	}
	if token != githubToken {
		t.Fatalf("token = %q, want configured token", token)
	}
	if state.State != "" {
		t.Fatalf("failure state = %+v, want empty", state)
	}
	if !starred {
		t.Fatal("star endpoint not called")
	}
}

func TestConfigureGitHubTokenRejectsFailedStarWithoutPersisting(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_existing")
	t.Setenv("GITHUB_TOKEN", "github_token_existing")

	githubToken := fakeGitHubPAT("star_denied")
	useGitHubStarTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
	})

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	token, state, err := configureGitHubToken(context.Background(), "test", path, hub.InitConfig{}, nil, githubToken, "")
	if err == nil {
		t.Fatal("configureGitHubToken() error = nil, want star failure")
	}
	if token != "" {
		t.Fatalf("token = %q, want empty", token)
	}
	if state.State != "needs_configure" {
		t.Fatalf("state = %+v, want needs_configure", state)
	}
	if strings.Contains(err.Error(), githubToken) {
		t.Fatalf("error leaked token: %q", err)
	}
	if _, readErr := os.ReadFile(path); readErr == nil {
		t.Fatal("runtime config exists, want no persisted token")
	}
	if got, want := os.Getenv("GH_TOKEN"), "github_token_existing"; got != want {
		t.Fatalf("GH_TOKEN = %q, want existing value", got)
	}
}

func TestGitHubTokenRequirementStatePrefersValidatedEnvironmentTokenOverInvalidPersistedToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github_token_env_valid")

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"github_token":"github_token_runtime_invalid"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			switch got := os.Getenv("GITHUB_TOKEN"); got {
			case "github_token_env_valid":
				return execx.Result{Stdout: "github.com logged in"}, nil
			case "github_token_runtime_invalid":
				return execx.Result{Stderr: "bad credentials"}, errors.New("token invalid")
			default:
				t.Fatalf("validator saw unexpected token %q", got)
				return execx.Result{}, nil
			}
		},
	}

	blocked, state := githubTokenRequirementState(context.Background(), runner, "test", path, hub.InitConfig{})
	if blocked {
		t.Fatalf("githubTokenRequirementState() blocked = true, state = %+v", state)
	}
	if got, want := os.Getenv("GH_TOKEN"), "github_token_env_valid"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
}

func TestApplyGitHubTokenRequirementStateFallsBackToLaterValidCandidate(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_env_invalid")
	t.Setenv("GITHUB_TOKEN", "")

	runner := &sharedAuthGateRunnerStub{
		run: func(_ context.Context, _ execx.Command) (execx.Result, error) {
			switch got := os.Getenv("GITHUB_TOKEN"); got {
			case "github_token_env_invalid":
				return execx.Result{Stderr: "bad credentials"}, errors.New("token invalid")
			case "github_token_init_valid":
				return execx.Result{Stdout: "github.com logged in"}, nil
			default:
				t.Fatalf("validator saw unexpected token %q", got)
				return execx.Result{}, nil
			}
		},
	}

	var state configurableAgentAuthState
	token, blocked := applyGitHubTokenRequirementState(
		context.Background(),
		runner,
		&state,
		"test",
		filepath.Join(t.TempDir(), "missing.json"),
		hub.InitConfig{GitHubToken: "github_token_init_valid"},
	)
	if blocked {
		t.Fatalf("applyGitHubTokenRequirementState() blocked = true, state = %+v", state.snapshot("test"))
	}
	if got, want := token, "github_token_init_valid"; got != want {
		t.Fatalf("token = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GH_TOKEN"), "github_token_init_valid"; got != want {
		t.Fatalf("GH_TOKEN = %q, want %q", got, want)
	}
	if got, want := os.Getenv("GITHUB_TOKEN"), "github_token_init_valid"; got != want {
		t.Fatalf("GITHUB_TOKEN = %q, want %q", got, want)
	}
}

func TestDecodeJSONStrictOrWrappedString(t *testing.T) {
	var payload struct {
		Value string `json:"value"`
	}
	if err := decodeJSONStrictOrWrappedString(`"{\"value\":\"ok\"}"`, &payload); err != nil {
		t.Fatalf("decodeJSONStrictOrWrappedString(wrapped) error = %v", err)
	}
	if payload.Value != "ok" {
		t.Fatalf("decodeJSONStrictOrWrappedString(wrapped) value = %q, want ok", payload.Value)
	}

	if err := decodeJSONStrictOrWrappedString(`{"value":"ok","extra":true}`, &payload); err == nil {
		t.Fatal("decodeJSONStrictOrWrappedString(unknown field) error = nil, want non-nil")
	}
}

func TestDecodeJSONOrWrappedString(t *testing.T) {
	var decoded struct {
		Value string `json:"value"`
	}
	if err := decodeJSONOrWrappedString(`"{\"value\":\"ok\"}"`, &decoded); err != nil {
		t.Fatalf("decodeJSONOrWrappedString() error = %v", err)
	}
	if got, want := decoded.Value, "ok"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}
