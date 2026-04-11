package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

type stubAgentAuthGate struct {
	statusState hubui.AgentAuthState
	statusErr   error
	startState  hubui.AgentAuthState
	startErr    error
	startCalls  int
}

func (g *stubAgentAuthGate) Status(context.Context) (hubui.AgentAuthState, error) {
	return g.statusState, g.statusErr
}

func (g *stubAgentAuthGate) StartDeviceAuth(context.Context) (hubui.AgentAuthState, error) {
	g.startCalls++
	if g.startState.State == "" {
		g.startState = g.statusState
	}
	return g.startState, g.startErr
}

func (g *stubAgentAuthGate) Verify(context.Context) (hubui.AgentAuthState, error) {
	return g.statusState, nil
}

func (g *stubAgentAuthGate) Configure(context.Context, string) (hubui.AgentAuthState, error) {
	return g.statusState, nil
}

func TestStringListFlagSetAndString(t *testing.T) {
	t.Parallel()

	var values stringListFlag
	if err := values.Set("a.json"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := values.Set("b.json"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got, want := values.String(), "a.json,b.json"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestWriteStdoutAndStderrLineCaptureToSink(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	var out strings.Builder
	logger := newTerminalLogger(&out, false)
	logger.sink = sink

	writeStdoutLine(logger, "  status=ok  ")
	writeStderrLine(logger, "  error: boom  ")
	writeStdoutLine(logger, "   ")
	writeStderrLine(logger, "")

	if got, want := len(sink.lines), 2; got != want {
		t.Fatalf("len(sink.lines) = %d, want %d (%v)", got, want, sink.lines)
	}
	if got, want := sink.lines[0], "status=ok"; got != want {
		t.Fatalf("sink.lines[0] = %q, want %q", got, want)
	}
	if got, want := sink.lines[1], "error: boom"; got != want {
		t.Fatalf("sink.lines[1] = %q, want %q", got, want)
	}
}

func TestHubExitCodeMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want int
	}{
		{err: fmt.Errorf("init config: invalid"), want: harness.ExitConfig},
		{err: fmt.Errorf("hub auth: invalid token"), want: harness.ExitAuth},
		{err: fmt.Errorf("hub profile: invalid handle"), want: harness.ExitAuth},
		{err: fmt.Errorf("hub websocket url: malformed"), want: harness.ExitConfig},
		{err: fmt.Errorf("something else"), want: harness.ExitPreflight},
	}
	for _, tt := range tests {
		if got := hubExitCode(tt.err); got != tt.want {
			t.Fatalf("hubExitCode(%q) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

func TestShouldFallbackToLocalOnlyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		uiListen string
		err      error
		want     bool
	}{
		{
			name:     "auth failure with ui enabled falls back",
			uiListen: "127.0.0.1:7777",
			err:      fmt.Errorf("hub auth: pull status=401"),
			want:     true,
		},
		{
			name:     "auth failure with ui disabled does not fall back",
			uiListen: "",
			err:      fmt.Errorf("hub auth: pull status=401"),
			want:     false,
		},
		{
			name:     "non-auth error does not fall back",
			uiListen: "127.0.0.1:7777",
			err:      fmt.Errorf("something else"),
			want:     false,
		},
		{
			name:     "nil error does not fall back",
			uiListen: "127.0.0.1:7777",
			err:      nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldFallbackToLocalOnlyMode(tt.uiListen, tt.err); got != tt.want {
				t.Fatalf("shouldFallbackToLocalOnlyMode(%q, %v) = %v, want %v", tt.uiListen, tt.err, got, tt.want)
			}
		})
	}
}

func TestRunHubBootDiagnosticsPingFailureBehavior(t *testing.T) {
	t.Parallel()

	if got := shouldRunHubInLocalOnlyMode(true, false, "127.0.0.1:7777", false); !got {
		t.Fatal("shouldRunHubInLocalOnlyMode(ping_failed, ui_enabled, no_hub) = false, want true")
	}
	if got := shouldRunHubInLocalOnlyMode(true, false, "", false); !got {
		t.Fatal("shouldRunHubInLocalOnlyMode(ping_failed, headless, no_hub) = false, want true")
	}
	if got := shouldRunHubInLocalOnlyMode(true, false, "", true); got {
		t.Fatal("shouldRunHubInLocalOnlyMode(ping_failed, headless, hub_configured) = true, want false")
	}
	if got := shouldRunHubInLocalOnlyMode(true, true, "127.0.0.1:7777", false); got {
		t.Fatal("shouldRunHubInLocalOnlyMode(ping_ok, ui_enabled, no_hub) = true, want false")
	}
	if got := shouldRunHubInLocalOnlyMode(false, false, "127.0.0.1:7777", false); got {
		t.Fatal("shouldRunHubInLocalOnlyMode(ping_unchecked, ui_enabled, no_hub) = true, want false")
	}
}

func TestHubPingFailureDetail(t *testing.T) {
	t.Parallel()

	if got, want := hubPingFailureDetail("base message", nil), "base message"; got != want {
		t.Fatalf("hubPingFailureDetail(base,nil) = %q, want %q", got, want)
	}
	err := errors.New("GET https://na.hub.molten.bot/ping returned status=503")
	if got := hubPingFailureDetail("base message", err); !strings.Contains(got, "status=503") {
		t.Fatalf("hubPingFailureDetail(base,err) missing status detail: %q", got)
	}
}

func TestMaybeStartAgentAuthStartsClaudeLoginWhenBrowserAuthIsNeeded(t *testing.T) {
	t.Parallel()

	gate := &stubAgentAuthGate{
		statusState: hubui.AgentAuthState{
			Required: true,
			Ready:    false,
			State:    "needs_browser_login",
			Message:  "Claude login required",
		},
		startState: hubui.AgentAuthState{
			Required: true,
			Ready:    false,
			State:    "pending_browser_login",
		},
	}

	var logs []string
	maybeStartAgentAuth(
		context.Background(),
		agentruntime.Runtime{Harness: agentruntime.HarnessClaude, Command: "claude"},
		gate,
		func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	)

	if got, want := gate.startCalls, 1; got != want {
		t.Fatalf("startCalls = %d, want %d", got, want)
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "hub.auth status=start harness=claude action=start_device_auth") {
		t.Fatalf("logs missing start marker: %q", got)
	}
}

func TestMaybeStartAgentAuthSkipsWhenClaudeNeedsManualConfigure(t *testing.T) {
	t.Parallel()

	gate := &stubAgentAuthGate{
		statusState: hubui.AgentAuthState{
			Required: true,
			Ready:    false,
			State:    "needs_configure",
		},
	}

	maybeStartAgentAuth(
		context.Background(),
		agentruntime.Runtime{Harness: agentruntime.HarnessClaude, Command: "claude"},
		gate,
		func(string, ...any) {},
	)

	if got := gate.startCalls; got != 0 {
		t.Fatalf("startCalls = %d, want 0", got)
	}
}

func TestMaybeStartAgentAuthLogsStartErrors(t *testing.T) {
	t.Parallel()

	gate := &stubAgentAuthGate{
		statusState: hubui.AgentAuthState{
			Required: true,
			Ready:    false,
			State:    "needs_browser_login",
			Message:  "Claude login required",
		},
		startState: hubui.AgentAuthState{
			Required: true,
			Ready:    false,
			State:    "error",
			Message:  "start claude auth login: exit status 2",
		},
		startErr: errors.New("exit status 2"),
	}

	var logs []string
	maybeStartAgentAuth(
		context.Background(),
		agentruntime.Runtime{Harness: agentruntime.HarnessClaude, Command: "claude"},
		gate,
		func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	)

	if got, want := gate.startCalls, 1; got != want {
		t.Fatalf("startCalls = %d, want %d", got, want)
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, `status=warn harness=claude action=start_device_auth err="exit status 2" detail="start claude auth login: exit status 2"`) {
		t.Fatalf("logs missing detailed start failure: %q", got)
	}
}

func TestShouldEnableAgentAuthConfigure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		harness string
		want    bool
	}{
		{name: "codex", harness: agentruntime.HarnessCodex, want: true},
		{name: "claude", harness: agentruntime.HarnessClaude, want: true},
		{name: "auggie", harness: agentruntime.HarnessAuggie, want: true},
		{name: "pi", harness: agentruntime.HarnessPi, want: true},
		{name: "mixed-case-codex", harness: "  CoDeX  ", want: true},
		{name: "unknown", harness: "custom", want: false},
		{name: "empty", harness: "", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldEnableAgentAuthConfigure(tt.harness); got != tt.want {
				t.Fatalf("shouldEnableAgentAuthConfigure(%q) = %v, want %v", tt.harness, got, tt.want)
			}
		})
	}
}

func TestJoinPRURLsAndCountChangedRepos(t *testing.T) {
	t.Parallel()

	results := []harness.RepoResult{
		{Changed: true, PRURL: " https://github.com/acme/repo-a/pull/1 "},
		{Changed: false, PRURL: "https://github.com/acme/repo-b/pull/2"},
		{Changed: true, PRURL: ""},
		{Changed: true, PRURL: "https://github.com/acme/repo-c/pull/3"},
	}
	if got, want := joinPRURLs(results), "https://github.com/acme/repo-a/pull/1,https://github.com/acme/repo-c/pull/3"; got != want {
		t.Fatalf("joinPRURLs() = %q, want %q", got, want)
	}
	if got, want := joinAllPRURLs(results), "https://github.com/acme/repo-a/pull/1,https://github.com/acme/repo-b/pull/2,https://github.com/acme/repo-c/pull/3"; got != want {
		t.Fatalf("joinAllPRURLs() = %q, want %q", got, want)
	}
	if got, want := countChangedRepos(results), 3; got != want {
		t.Fatalf("countChangedRepos() = %d, want %d", got, want)
	}
}

func TestMarshalRunConfigJSONReturnsJSONPayload(t *testing.T) {
	t.Parallel()

	payload, ok := marshalRunConfigJSON(config.Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
	})
	if !ok {
		t.Fatal("marshalRunConfigJSON() ok = false, want true")
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := decoded["repoUrl"], "git@github.com:acme/repo.git"; got != want {
		t.Fatalf("repoUrl = %v, want %q", got, want)
	}
}

func TestFailureFollowUpPromptDefaultWhenNoPaths(t *testing.T) {
	t.Parallel()

	got := failureFollowUpPrompt(nil, harness.Result{}, config.Config{})
	if !strings.Contains(got, failureFollowUpRequiredPrompt) {
		t.Fatalf("prompt missing required instructions: %q", got)
	}
	if !strings.Contains(got, ".log/local/<request timestamp>/<request sequence>") {
		t.Fatalf("prompt missing default log dir hint: %q", got)
	}
	if !strings.Contains(got, ".log/local/<request timestamp>/<request sequence>/term") {
		t.Fatalf("prompt missing default legacy log file hint: %q", got)
	}
	if !strings.Contains(got, ".log/local/<request timestamp>/<request sequence>/terminal.log") {
		t.Fatalf("prompt missing default log path hint: %q", got)
	}
	if !strings.Contains(got, "Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.") {
		t.Fatalf("prompt missing molten hub integration instruction: %q", got)
	}
	if !strings.Contains(got, `When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.`) {
		t.Fatalf("prompt missing failure response instruction: %q", got)
	}
	if !strings.Contains(got, `"repos":["git@github.com:Molten-Bot/moltenhub-code.git"],"baseBranch":"main","targetSubdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."`) {
		t.Fatalf("prompt missing follow-up payload shape: %q", got)
	}
	if !strings.Contains(got, "If no file changes are required, return a clear no-op result with concrete evidence instead of forcing an empty PR.") {
		t.Fatalf("prompt missing no-op completion carve-out: %q", got)
	}
}

func TestFailureFollowUpPromptIncludesFailureContext(t *testing.T) {
	t.Parallel()

	got := failureFollowUpPrompt(
		[]string{"/workspace/.log/local/1775600653/000013/terminal.log"},
		harness.Result{
			ExitCode:     harness.ExitClone,
			Err:          errors.New("clone: repository not found"),
			WorkspaceDir: "/tmp/run-123",
			Branch:       "moltenhub-fix-clone",
			PRURL:        "https://github.com/acme/repo/pull/17",
		},
		config.Config{
			Repos: []string{
				"git@github.com:acme/repo-a.git",
				"git@github.com:acme/repo-b.git",
			},
		},
	)

	for _, want := range []string{
		"Observed failure context:",
		"- exit_code=40",
		`- error="clone: repository not found"`,
		"- workspace_dir=/tmp/run-123",
		"- branch=moltenhub-fix-clone",
		"- pr_url=https://github.com/acme/repo/pull/17",
		"- repos=git@github.com:acme/repo-a.git,git@github.com:acme/repo-b.git",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q: %q", want, got)
		}
	}
}

func TestConfigureHubSetupTracksCompletedOnboardingSteps(t *testing.T) {
	t.Parallel()

	const bindToken = "f9mju6sL6Qns5WX1H09ghY5X4HJHHRTlcc6nzfiOdxs"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bind-tokens":
			_, _ = w.Write([]byte(`{"agent_token":"agent_bound"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			_, _ = w.Write([]byte(`{"handle":"saved-agent","profile":{"display_name":"Saved Agent","emoji":"🔥","profile":"Ships onboarding"}}`))
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) &&
			(r.URL.Path == "/v1/agents/me" || r.URL.Path == "/v1/agents/me/metadata"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		RuntimeConfigPath: filepath.Join(t.TempDir(), ".moltenhub", "config.json"),
	}, hubui.HubSetupRequest{
		AgentMode: "new",
		Token:     bindToken,
		Handle:    "saved-agent",
		Profile: struct {
			ProfileText string `json:"profile"`
			DisplayName string `json:"display_name"`
			Emoji       string `json:"emoji"`
		}{
			ProfileText: "Ships onboarding",
			DisplayName: "Saved Agent",
			Emoji:       "🔥",
		},
	}, func(context.Context, hub.InitConfig) error {
		return nil
	})
	if err != nil {
		t.Fatalf("configureHubSetup() error = %v", err)
	}
	if !state.ActivationReady {
		t.Fatal("ActivationReady = false, want true")
	}
	if state.OnboardingActive {
		t.Fatal("OnboardingActive = true, want false")
	}
	if got, want := state.OnboardingStage, "work_activate"; got != want {
		t.Fatalf("OnboardingStage = %q, want %q", got, want)
	}
	wantStatuses := map[string]string{
		"bind":          "completed",
		"work_bind":     "completed",
		"profile_set":   "completed",
		"work_activate": "completed",
	}
	for _, step := range state.Onboarding {
		if want, ok := wantStatuses[step.ID]; ok && step.Status != want {
			t.Fatalf("step %q status = %q, want %q", step.ID, step.Status, want)
		}
	}
}

func TestConfigureHubSetupMarksFailingOnboardingStep(t *testing.T) {
	t.Parallel()

	const bindToken = "f9mju6sL6Qns5WX1H09ghY5X4HJHHRTlcc6nzfiOdxs"
	var agentProfileReads int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bind-tokens":
			_, _ = w.Write([]byte(`{"agent_token":"agent_bound"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me":
			agentProfileReads++
			if agentProfileReads <= 1 {
				_, _ = w.Write([]byte(`{"metadata":{"visibility":"public"}}`))
				return
			}
			http.Error(w, `{"error":"profile unavailable"}`, http.StatusBadGateway)
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && r.URL.Path == "/v1/agents/me/status":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) &&
			(r.URL.Path == "/v1/agents/me" || r.URL.Path == "/v1/agents/me/metadata"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	state, err := configureHubSetup(context.Background(), hub.InitConfig{
		BaseURL:           server.URL + "/v1",
		RuntimeConfigPath: filepath.Join(t.TempDir(), ".moltenhub", "config.json"),
	}, hubui.HubSetupRequest{
		AgentMode: "new",
		Token:     bindToken,
	}, func(context.Context, hub.InitConfig) error {
		return nil
	})
	if err == nil {
		t.Fatal("configureHubSetup() error = nil, want non-nil")
	}
	if got, want := state.OnboardingStage, "work_activate"; got != want {
		t.Fatalf("OnboardingStage = %q, want %q", got, want)
	}
	for _, step := range state.Onboarding {
		if step.ID == "work_activate" {
			if step.Status != "error" {
				t.Fatalf("work_activate status = %q, want error", step.Status)
			}
			if !strings.Contains(step.Detail, "status=502") {
				t.Fatalf("work_activate detail = %q, want status details", step.Detail)
			}
			return
		}
	}
	t.Fatal("work_activate step missing")
}

func TestShouldQueueUnexpectedNoChangesFollowUpRequiresMissingPR(t *testing.T) {
	t.Parallel()

	ok, reason := shouldQueueUnexpectedNoChangesFollowUp(harness.Result{NoChanges: true})
	if !ok || reason != "" {
		t.Fatalf("shouldQueueUnexpectedNoChangesFollowUp(no PR) = (%v, %q), want (true, \"\")", ok, reason)
	}

	ok, reason = shouldQueueUnexpectedNoChangesFollowUp(harness.Result{
		NoChanges: true,
		PRURL:     "https://github.com/acme/repo/pull/1",
	})
	if ok {
		t.Fatal("shouldQueueUnexpectedNoChangesFollowUp(existing PR) = true, want false")
	}
	if reason != "task already has a pull request" {
		t.Fatalf("reason = %q, want %q", reason, "task already has a pull request")
	}
}

func TestShouldEscalateNoChangesFollowUpRequiresFollowUpSourceAndMissingPR(t *testing.T) {
	t.Parallel()

	ok, reason := shouldEscalateNoChangesFollowUp("local_submit", harness.Result{NoChanges: true})
	if ok {
		t.Fatal("shouldEscalateNoChangesFollowUp(local_submit) = true, want false")
	}
	if reason != "run is not a no-changes follow-up" {
		t.Fatalf("reason = %q, want %q", reason, "run is not a no-changes follow-up")
	}

	ok, reason = shouldEscalateNoChangesFollowUp("no_changes_followup", harness.Result{NoChanges: true})
	if !ok || reason != "" {
		t.Fatalf("shouldEscalateNoChangesFollowUp(no_changes_followup,no PR) = (%v, %q), want (true, \"\")", ok, reason)
	}

	ok, reason = shouldEscalateNoChangesFollowUp("no_changes_followup", harness.Result{
		NoChanges: true,
		PRURL:     "https://github.com/acme/repo/pull/1",
	})
	if ok {
		t.Fatal("shouldEscalateNoChangesFollowUp(existing PR) = true, want false")
	}
	if reason != "task already has a pull request" {
		t.Fatalf("reason = %q, want %q", reason, "task already has a pull request")
	}
}

func TestUnexpectedNoChangesFollowUpRunConfigPreservesTaskTargetingAndAddsContext(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	logDir := filepath.Join(logRoot, "local", "1712345678", "000001")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	logPath := filepath.Join(logDir, logFileName)
	if err := os.WriteFile(logPath, []byte("dispatch status=no_changes\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	runCfg := config.Config{
		Repos:        []string{"git@github.com:acme/repo.git"},
		BaseBranch:   "release/2026.04-hotfix",
		TargetSubdir: "cmd/harness",
		Prompt:       "fix the broken local no changes task handling",
	}
	result := harness.Result{
		NoChanges:    true,
		WorkspaceDir: "/tmp/run-123",
		Branch:       "release/2026.04-hotfix",
	}

	cfg := unexpectedNoChangesFollowUpRunConfig("local-1712345678-000001", result, runCfg, logRoot)
	if got, want := cfg.BaseBranch, "release/2026.04-hotfix"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}
	if got, want := cfg.TargetSubdir, "cmd/harness"; got != want {
		t.Fatalf("TargetSubdir = %q, want %q", got, want)
	}
	if got, want := cfg.Repos, []string{"git@github.com:acme/repo.git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos = %v, want %v", got, want)
	}

	for _, want := range []string{
		"Review the previous local task logs first.",
		filepath.Join(logRoot, "local", "1712345678", "000001"),
		"Observed no-change context:",
		"- request_id=local-1712345678-000001",
		"- workspace_dir=/tmp/run-123",
		"- branch=release/2026.04-hotfix",
		"- target_subdir=cmd/harness",
		"Original task prompt:",
		"fix the broken local no changes task handling",
		"Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.",
	} {
		if !strings.Contains(cfg.Prompt, want) {
			t.Fatalf("Prompt missing %q: %q", want, cfg.Prompt)
		}
	}
	if strings.Contains(cfg.Prompt, filepath.Join(logRoot, logFileName)) {
		t.Fatalf("Prompt should exclude aggregate terminal log path to avoid recursive follow-up reads: %q", cfg.Prompt)
	}
}

func TestUnexpectedNoChangesFollowUpRunConfigKeepsConfiguredMainBranch(t *testing.T) {
	t.Parallel()

	cfg := unexpectedNoChangesFollowUpRunConfig(
		"local-1712345678-000001",
		harness.Result{
			NoChanges: true,
			Branch:    "moltenhub-add-the-emoji-picker-to-the-agent-profil",
		},
		config.Config{
			Repos:      []string{"git@github.com:acme/repo.git"},
			BaseBranch: "main",
		},
		t.TempDir(),
	)

	if got, want := cfg.BaseBranch, "main"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}
}

func TestUnexpectedNoChangesFollowUpRunConfigRetainsOriginalRepoList(t *testing.T) {
	t.Parallel()

	runCfg := config.Config{
		Repo: "git@github.com:acme/repo-a.git",
		Repos: []string{
			"git@github.com:acme/repo-b.git",
			"git@github.com:acme/repo-a.git",
		},
	}
	cfg := unexpectedNoChangesFollowUpRunConfig(
		"local-1712345678-000001",
		harness.Result{NoChanges: true},
		runCfg,
		t.TempDir(),
	)

	if got, want := cfg.Repos, runCfg.RepoList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos = %v, want %v", got, want)
	}
}

func TestUnexpectedNoChangesFollowUpRunConfigFallsBackToMoltenHubRepoWhenNoOriginalRepo(t *testing.T) {
	t.Parallel()

	cfg := unexpectedNoChangesFollowUpRunConfig(
		"local-1712345678-000001",
		harness.Result{NoChanges: true},
		config.Config{},
		t.TempDir(),
	)

	if got, want := cfg.Repos, []string{config.DefaultRepositoryURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos = %v, want %v", got, want)
	}
}

func TestUnexpectedNoChangesFollowUpRunConfigUsesNoPathGuidanceWhenTaskLogsMissing(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join(t.TempDir(), ".log")
	runCfg := config.Config{
		Repos:        []string{"git@github.com:acme/repo.git"},
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       "investigate missing changes",
	}
	result := harness.Result{
		NoChanges: true,
		Branch:    "main",
	}

	cfg := unexpectedNoChangesFollowUpRunConfig("local-1712345678-000001", result, runCfg, logRoot)
	missingLogPath := filepath.Join(logRoot, "local", "1712345678", "000001")
	if strings.Contains(cfg.Prompt, missingLogPath) {
		t.Fatalf("Prompt should not include missing log path %q: %q", missingLogPath, cfg.Prompt)
	}
	if !strings.Contains(cfg.Prompt, "No local task log path was captured before the task completed without changes.") {
		t.Fatalf("Prompt missing no-path guidance: %q", cfg.Prompt)
	}
}

func TestShouldQueueFailureFollowUpQueuesNoDeltaFailures(t *testing.T) {
	t.Parallel()

	result := harness.Result{
		Err: errors.New("task failed to meet completion requirements because this branch has no delta from `main`; No commits between main and moltenhub-fix"),
	}

	ok, reason := shouldQueueFailureFollowUp(result)
	if !ok {
		t.Fatalf("shouldQueueFailureFollowUp() = false, want true for no-delta failures (reason=%q)", reason)
	}
}

func TestTaskLogDirAndTaskLogPathsValidateInputs(t *testing.T) {
	t.Parallel()

	if got, ok := taskLogDir("", "req-1"); ok || got != "" {
		t.Fatalf("taskLogDir(empty root) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := taskLogDir("/tmp/.log", ""); ok || got != "" {
		t.Fatalf("taskLogDir(empty request) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got := taskLogPaths("", "req-1"); got != nil {
		t.Fatalf("taskLogPaths(empty root) = %v, want nil", got)
	}

	paths := taskLogPaths("/tmp/.log", "local-1712345678-000001")
	want := []string{
		filepath.Join("/tmp/.log", "local", "1712345678", "000001"),
		filepath.Join("/tmp/.log", "local", "1712345678", "000001", legacyTaskLogFileName),
		filepath.Join("/tmp/.log", "local", "1712345678", "000001", logFileName),
	}
	if got, wantJoined := strings.Join(paths, "\n"), strings.Join(want, "\n"); got != wantJoined {
		t.Fatalf("taskLogPaths(...) = %q, want %q", got, wantJoined)
	}
}

func TestShouldQueueFailureFollowUpQueuesFailuresWithErrorDetails(t *testing.T) {
	t.Parallel()

	ok, reason := shouldQueueFailureFollowUp(harness.Result{
		Err: errors.New("codex: ERROR: Quota exceeded. Check your plan and billing details."),
	})
	if ok || !strings.Contains(reason, "non-remediable failure: quota exceeded") {
		t.Fatalf("shouldQueueFailureFollowUp(quota exceeded) = (%v, %q), want non-remediable skip", ok, reason)
	}

	ok, reason = shouldQueueFailureFollowUp(harness.Result{
		Err: errors.New("codex: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"),
	})
	if ok || !strings.Contains(reason, "non-remediable failure:") {
		t.Fatalf("shouldQueueFailureFollowUp(auth failure) = (%v, %q), want non-remediable skip", ok, reason)
	}

	ok, reason = shouldQueueFailureFollowUp(harness.Result{
		Err: errors.New("clone: run git [clone ...]: exit status 128"),
	})
	if !ok || reason != "" {
		t.Fatalf("shouldQueueFailureFollowUp(clone failure) = (%v, %q), want (true, \"\")", ok, reason)
	}

	ok, reason = shouldQueueFailureFollowUp(harness.Result{
		Err: errors.New("git: verify remote write access for repo https://github.com/acme/repo.git branch \"moltenhub-fix\": exit status 128: remote: Write access to repository not granted. fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403"),
	})
	if ok || !strings.Contains(reason, "non-remediable failure: write access to repository not granted") {
		t.Fatalf("shouldQueueFailureFollowUp(repo write access failure) = (%v, %q), want non-remediable skip", ok, reason)
	}

	ok, reason = shouldQueueFailureFollowUp(harness.Result{
		Err: errors.New("git: run git [push -u origin moltenhub-branch]: exit status 1: remote: refusing to allow an OAuth App to create or update workflow `.github/workflows/docker-release.yml` without `workflow` scope"),
	})
	if ok || !strings.Contains(reason, "non-remediable failure: refusing to allow an oauth app to create or update workflow") {
		t.Fatalf("shouldQueueFailureFollowUp(workflow scope failure) = (%v, %q), want non-remediable skip", ok, reason)
	}
}

func TestHubPingURLValidationAndCheckHubPingFailures(t *testing.T) {
	t.Parallel()

	if _, err := hubPingURL("ftp://example.com/v1"); err == nil {
		t.Fatal("hubPingURL(ftp) error = nil, want non-nil")
	}
	if _, err := hubPingURL("https:///v1"); err == nil {
		t.Fatal("hubPingURL(missing host) error = nil, want non-nil")
	}

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 200), http.StatusServiceUnavailable)
	}))
	defer pingServer.Close()

	if _, err := checkHubPing(context.Background(), pingServer.URL); err == nil {
		t.Fatal("checkHubPing(non-2xx) error = nil, want non-nil")
	}
}

func TestRunHubBootDiagnosticsWithRuntimeLoaderRejectsNilDeps(t *testing.T) {
	t.Parallel()

	ok := runHubBootDiagnosticsWithRuntimeLoader(context.Background(), nil, nil, hub.InitConfig{}, nil)
	if ok {
		t.Fatal("runHubBootDiagnosticsWithRuntimeLoader(nil,nil,...) = true, want false")
	}
}

func TestRunHubBootDiagnosticsWithRuntimeLoaderRejectsInvalidAgentHarness(t *testing.T) {
	t.Parallel()

	var logs []string
	ok := runHubBootDiagnosticsWithRuntimeLoader(
		context.Background(),
		&stubExecRunner{},
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		hub.InitConfig{
			BaseURL:      "https://na.hub.molten.bot/v1",
			AgentHarness: "unsupported",
		},
		nil,
	)
	if ok {
		t.Fatal("runHubBootDiagnosticsWithRuntimeLoader() = true, want false")
	}
	assertLogContains(t, logs, "boot.diagnosis status=error requirement=agent_runtime")
}

func TestRunHubBootDiagnosticsWrapper(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer pingServer.Close()

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}): {result: execx.Result{Stdout: "git version"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"--version"}}):  {result: execx.Result{Stdout: "gh version"}},
			stubCommandKey(execx.Command{Name: "codex", Args: []string{"--help"}}):  {result: execx.Result{Stdout: "codex help"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}): {
				err: errors.New("not authenticated"),
			},
		},
	}

	var logs []string
	ok := runHubBootDiagnostics(
		context.Background(),
		runner,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		hub.InitConfig{BaseURL: pingServer.URL + "/v1"},
	)
	if !ok {
		t.Fatal("runHubBootDiagnostics() = false, want true")
	}
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=git_cli")
}

func TestRunHubBootDiagnosticsUsesConfiguredAgentRuntime(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer pingServer.Close()

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}):        {result: execx.Result{Stdout: "git version"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"--version"}}):         {result: execx.Result{Stdout: "gh version"}},
			stubCommandKey(execx.Command{Name: "claude-custom", Args: []string{"--help"}}): {result: execx.Result{Stdout: "claude help"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}):    {result: execx.Result{Stdout: "Logged in to github.com as test\n"}},
		},
	}

	var logs []string
	ok := runHubBootDiagnostics(
		context.Background(),
		runner,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		hub.InitConfig{
			BaseURL:      pingServer.URL + "/v1",
			AgentHarness: "claude",
			AgentCommand: "claude-custom",
		},
	)
	if !ok {
		t.Fatal("runHubBootDiagnostics() = false, want true")
	}
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=claude_cli")
}

func TestRunHubBootDiagnosticsSupportsPiRuntime(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer pingServer.Close()

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}):     {result: execx.Result{Stdout: "git version"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"--version"}}):      {result: execx.Result{Stdout: "gh version"}},
			stubCommandKey(execx.Command{Name: "pi-custom", Args: []string{"--help"}}):  {result: execx.Result{Stdout: "pi help"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}): {result: execx.Result{Stdout: "Logged in to github.com as test\n"}},
		},
	}

	var logs []string
	ok := runHubBootDiagnostics(
		context.Background(),
		runner,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		hub.InitConfig{
			BaseURL:      pingServer.URL + "/v1",
			AgentHarness: "pi",
			AgentCommand: "pi-custom",
		},
	)
	if !ok {
		t.Fatal("runHubBootDiagnostics() = false, want true")
	}
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=pi_cli")
}

func TestRunHubBootDiagnosticsMentionsGitHubCLIPackageWhenPreflightFails(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer pingServer.Close()

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}):     {result: execx.Result{Stdout: "git version"}},
			stubCommandKey(execx.Command{Name: "codex", Args: []string{"--help"}}):      {result: execx.Result{Stdout: "codex help"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}): {result: execx.Result{Stdout: "Logged in to github.com as test\n"}},
		},
	}

	var logs []string
	ok := runHubBootDiagnostics(
		context.Background(),
		runner,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		hub.InitConfig{BaseURL: pingServer.URL + "/v1"},
	)
	if !ok {
		t.Fatal("runHubBootDiagnostics() = false, want true")
	}
	assertLogContains(t, logs, "boot.diagnosis status=warn required_checks=failed")
	assertLogContains(t, logs, gitHubCLIPackageLabel)
}

func TestApplyDefaultAgentRuntimeConfig(t *testing.T) {
	t.Parallel()

	initCfg := hub.InitConfig{
		AgentHarness: "claude",
		AgentCommand: "claude-custom",
	}
	got := applyDefaultAgentRuntimeConfig(config.Config{}, initCfg)
	if got.AgentHarness != "claude" || got.AgentCommand != "claude-custom" {
		t.Fatalf("applyDefaultAgentRuntimeConfig() = %+v", got)
	}

	explicit := config.Config{AgentHarness: "auggie", AgentCommand: "auggie-custom"}
	got = applyDefaultAgentRuntimeConfig(explicit, initCfg)
	if got.AgentHarness != "auggie" || got.AgentCommand != "auggie-custom" {
		t.Fatalf("explicit run config should win; got %+v", got)
	}
}

func TestRunLocalDispatchReportsErrorState(t *testing.T) {
	t.Parallel()

	var logs []string
	outcome := runLocalDispatch(
		context.Background(),
		nil,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		"code_for_me",
		"req-1",
		config.Config{
			RepoURL: "git@github.com:acme/repo.git",
			Prompt:  "fix tests",
		},
		nil,
	)

	if outcome.State != "error" {
		t.Fatalf("runLocalDispatch() state = %q, want error", outcome.State)
	}
	if outcome.Result.Err == nil {
		t.Fatal("runLocalDispatch() result error = nil, want non-nil")
	}
	assertLogContains(t, logs, "dispatch status=error request_id=req-1")
}
