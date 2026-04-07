package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
)

func TestApplyStoredRuntimeConfigSkipsWhenInitBindTokenProvided(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		BindToken:  "bind_token",
		SessionKey: "main",
	}
	stored := RuntimeConfig{
		InitConfig: InitConfig{
			BaseURL:    "https://na.hub.molten.bot/v1",
			AgentToken: "agent_saved",
			SessionKey: "saved-session",
		},
	}

	applied := applyStoredRuntimeConfig(&cfg, stored)
	if applied {
		t.Fatal("applied = true, want false")
	}
	if cfg.AgentToken != "" {
		t.Fatalf("AgentToken = %q, want empty", cfg.AgentToken)
	}
	if cfg.BindToken != "bind_token" {
		t.Fatalf("BindToken = %q, want %q", cfg.BindToken, "bind_token")
	}
	if cfg.SessionKey != "main" {
		t.Fatalf("SessionKey = %q, want %q", cfg.SessionKey, "main")
	}
}

func TestApplyStoredRuntimeConfigNoToken(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{BindToken: "bind_token"}
	applied := applyStoredRuntimeConfig(&cfg, RuntimeConfig{InitConfig: InitConfig{AgentToken: ""}})
	if applied {
		t.Fatal("applied = true, want false")
	}
	if cfg.BindToken != "bind_token" {
		t.Fatalf("BindToken = %q", cfg.BindToken)
	}
}

func TestApplyStoredRuntimeConfigKeepsExplicitAgentToken(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		AgentToken: "agent_explicit",
		SessionKey: "main",
	}
	stored := RuntimeConfig{
		InitConfig: InitConfig{
			BaseURL:    "https://na.hub.molten.bot/v1",
			AgentToken: "agent_saved",
			SessionKey: "saved-session",
		},
	}

	applied := applyStoredRuntimeConfig(&cfg, stored)
	if applied {
		t.Fatal("applied = true, want false")
	}
	if cfg.AgentToken != "agent_explicit" {
		t.Fatalf("AgentToken = %q, want %q", cfg.AgentToken, "agent_explicit")
	}
}

func TestApplyStoredRuntimeConfigKeepsInitBaseURL(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		BaseURL: "https://na.hub.molten.bot/v1",
	}
	stored := RuntimeConfig{
		InitConfig: InitConfig{
			BaseURL:    "http://127.0.0.1:37991/v1",
			AgentToken: "agent_saved",
			SessionKey: "saved-session",
		},
	}

	applied := applyStoredRuntimeConfig(&cfg, stored)
	if !applied {
		t.Fatal("applied = false, want true")
	}
	if cfg.BaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, "https://na.hub.molten.bot/v1")
	}
	if cfg.AgentToken != "agent_saved" {
		t.Fatalf("AgentToken = %q, want %q", cfg.AgentToken, "agent_saved")
	}
	if cfg.SessionKey != "saved-session" {
		t.Fatalf("SessionKey = %q, want %q", cfg.SessionKey, "saved-session")
	}
}

func TestLoadStoredRuntimeConfigReadsPrimaryPath(t *testing.T) {
	root := t.TempDir()
	primaryPath := filepath.Join(root, ".moltenhub", "config.json")

	if err := SaveRuntimeConfig(primaryPath, InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		SessionKey: "main",
	}, "agent_primary"); err != nil {
		t.Fatalf("SaveRuntimeConfig(primary) error = %v", err)
	}

	cfg, loadedPath, err := loadStoredRuntimeConfig(primaryPath)
	if err != nil {
		t.Fatalf("loadStoredRuntimeConfig() error = %v", err)
	}
	if loadedPath != primaryPath {
		t.Fatalf("loadedPath = %q, want %q", loadedPath, primaryPath)
	}
	if cfg.AgentToken != "agent_primary" {
		t.Fatalf("AgentToken = %q, want %q", cfg.AgentToken, "agent_primary")
	}
}

func TestDaemonRunUsesStoredRuntimeConfigBaseURLWhenInitBaseURLOmitted(t *testing.T) {
	root := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir temp root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	var (
		reqMu        sync.Mutex
		paths        []string
		pullTimeouts []string
		logMu        sync.Mutex
		logs         []string
		base         string
		token        = "agent_saved"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqMu.Lock()
		paths = append(paths, r.URL.Path)
		reqMu.Unlock()

		switch r.URL.Path {
		case "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/openclaw/messages/register-plugin":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/agents/me/metadata", "/v1/agents/me/status":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/openclaw/messages/pull":
			reqMu.Lock()
			pullTimeouts = append(pullTimeouts, r.URL.Query().Get("timeout_ms"))
			reqMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	base = server.URL + "/v1"

	if err := SaveRuntimeConfig("", InitConfig{
		BaseURL:    base,
		SessionKey: "main",
	}, token); err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}
	runtimeCfgPath := defaultRuntimeConfigPath()
	cfgData, err := os.ReadFile(runtimeCfgPath)
	if err != nil {
		t.Fatalf("read runtime config: %v", err)
	}
	var runtimeCfg RuntimeConfig
	if err := json.Unmarshal(cfgData, &runtimeCfg); err != nil {
		t.Fatalf("parse runtime config: %v", err)
	}
	const storedTimeoutMs = 12345
	runtimeCfg.TimeoutMs = storedTimeoutMs
	encodedRuntimeCfg, err := json.Marshal(runtimeCfg)
	if err != nil {
		t.Fatalf("marshal runtime config: %v", err)
	}
	if err := os.WriteFile(runtimeCfgPath, append(encodedRuntimeCfg, '\n'), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = 10 * time.Millisecond
	d.Logf = func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if err := d.Run(ctx, InitConfig{}); err != nil {
		t.Fatalf("Daemon.Run() error = %v", err)
	}

	reqMu.Lock()
	gotPaths := append([]string(nil), paths...)
	gotPullTimeouts := append([]string(nil), pullTimeouts...)
	reqMu.Unlock()

	foundAgentsMe := false
	for _, path := range gotPaths {
		if path == "/v1/agents/me" {
			foundAgentsMe = true
			break
		}
	}
	if !foundAgentsMe {
		t.Fatalf("expected auth request against stored runtime base URL, got paths=%v", gotPaths)
	}
	wantTimeout := strconv.Itoa(storedTimeoutMs)
	if len(gotPullTimeouts) == 0 {
		t.Fatalf("expected pull requests, got none (paths=%v)", gotPaths)
	}
	foundStoredTimeout := false
	for _, got := range gotPullTimeouts {
		if got == wantTimeout {
			foundStoredTimeout = true
			break
		}
	}
	if !foundStoredTimeout {
		t.Fatalf("expected pull timeout_ms %q from stored runtime config, got %v", wantTimeout, gotPullTimeouts)
	}

	wantLog := fmt.Sprintf("hub.connection status=configured base_url=%s", base)
	logMu.Lock()
	defer logMu.Unlock()
	for _, line := range logs {
		if strings.Contains(line, wantLog) {
			return
		}
	}
	t.Fatalf("missing configured base URL log %q in logs=%v", wantLog, logs)
}

func TestDaemonRunUsesStoredRuntimeConfigPullTimeout(t *testing.T) {
	root := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir temp root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	const pullTimeoutMs = 4321

	var (
		reqMu       sync.Mutex
		pullQueries []string
		pullSeen    = make(chan struct{})
		pullOnce    sync.Once
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/openclaw/messages/register-plugin":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/agents/me/metadata", "/v1/agents/me/status":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/openclaw/messages/ws":
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		case "/v1/openclaw/messages/pull":
			reqMu.Lock()
			pullQueries = append(pullQueries, r.URL.RawQuery)
			reqMu.Unlock()
			pullOnce.Do(func() {
				close(pullSeen)
			})
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	runtimeCfgPath := filepath.Join(root, ".moltenhub", "config.json")
	if err := os.MkdirAll(filepath.Dir(runtimeCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir runtime config dir: %v", err)
	}
	runtimeCfgJSON := fmt.Sprintf(
		`{"baseUrl":%q,"token":"agent_saved","sessionKey":"main","timeoutMs":%d}`,
		server.URL+"/v1",
		pullTimeoutMs,
	)
	if err := os.WriteFile(runtimeCfgPath, []byte(runtimeCfgJSON), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, InitConfig{})
	}()

	select {
	case <-pullSeen:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for pull request")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Daemon.Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for daemon shutdown")
	}

	reqMu.Lock()
	defer reqMu.Unlock()
	if len(pullQueries) == 0 {
		t.Fatal("expected at least one pull query")
	}
	if got, want := pullQueries[0], fmt.Sprintf("timeout_ms=%d", pullTimeoutMs); got != want {
		t.Fatalf("pull query = %q, want %q", got, want)
	}
}

func TestIncomingSkillName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  map[string]any
		want string
	}{
		{
			name: "top-level skill",
			msg:  map[string]any{"skill": "code_for_me"},
			want: "code_for_me",
		},
		{
			name: "payload skill name",
			msg: map[string]any{
				"payload": map[string]any{"skill_name": "other_skill"},
			},
			want: "other_skill",
		},
		{
			name: "missing skill",
			msg:  map[string]any{"type": "skill_request"},
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := incomingSkillName(tt.msg); got != tt.want {
				t.Fatalf("incomingSkillName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDispatchParseErrorPayloadIncludesRequiredSchema(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-1",
		Skill:     "code_for_me",
	}
	payload := dispatchParseErrorPayload(cfg, dispatch, errors.New("bad payload"))
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing or wrong type: %#v", payload["result"])
	}
	if _, ok := result["requiredSchema"]; !ok {
		t.Fatalf("requiredSchema missing: %#v", result)
	}
}

func TestDispatchResultPayloadIncludesRepoResults(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-22",
		Skill:     "code_for_me",
	}
	res := harness.Result{
		ExitCode:     harness.ExitSuccess,
		WorkspaceDir: "/tmp/run",
		Branch:       "moltenhub-feature",
		PRURL:        "https://github.com/acme/repo-a/pull/10",
		RepoResults: []harness.RepoResult{
			{
				RepoURL: "git@github.com:acme/repo-a.git",
				RepoDir: "/tmp/run/repo-01-repo-a",
				Branch:  "moltenhub-feature",
				PRURL:   "https://github.com/acme/repo-a/pull/10",
				Changed: true,
			},
			{
				RepoURL: "git@github.com:acme/repo-b.git",
				RepoDir: "/tmp/run/repo-02-repo-b",
				Branch:  "moltenhub-feature",
				PRURL:   "https://github.com/acme/repo-b/pull/20",
				Changed: true,
			},
		},
	}

	payload := dispatchResultPayload(cfg, dispatch, res)
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing or wrong type: %#v", payload["result"])
	}
	prURLs, ok := result["prUrls"].([]string)
	if !ok {
		t.Fatalf("prUrls missing or wrong type: %#v", result["prUrls"])
	}
	if len(prURLs) != 2 {
		t.Fatalf("len(prUrls) = %d, want 2", len(prURLs))
	}
	repoResults, ok := result["repoResults"].([]map[string]any)
	if !ok {
		t.Fatalf("repoResults missing or wrong type: %#v", result["repoResults"])
	}
	if len(repoResults) != 2 {
		t.Fatalf("len(repoResults) = %d, want 2", len(repoResults))
	}
}

func TestDispatchResultPayloadIncludesTopLevelFailureMessage(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-err",
		Skill:     "code_for_me",
		ReplyTo:   "agent-123",
	}
	res := harness.Result{
		ExitCode: harness.ExitCodex,
		Err:      fmt.Errorf("codex: process exited with status 1"),
	}

	payload := dispatchResultPayload(cfg, dispatch, res)
	if got := payload["status"]; got != "error" {
		t.Fatalf("status = %#v, want %q", got, "error")
	}
	if got := payload["error"]; got != "codex: process exited with status 1" {
		t.Fatalf("error = %#v", got)
	}
	if got := payload["message"]; got != "Task failed. Error details: codex: process exited with status 1" {
		t.Fatalf("message = %#v", got)
	}
	failure, _ := payload["failure"].(map[string]any)
	if failure == nil {
		t.Fatal("failure payload missing")
	}
	if got := failure["status"]; got != "failed" {
		t.Fatalf("failure.status = %#v", got)
	}
	if got := failure["message"]; got != "Task failed. Error details: codex: process exited with status 1" {
		t.Fatalf("failure.message = %#v", got)
	}
	if got := failure["error"]; got != "codex: process exited with status 1" {
		t.Fatalf("failure.error = %#v", got)
	}
}

func TestHandleDispatchInvokesOnDispatchFailed(t *testing.T) {
	t.Parallel()

	publishRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/openclaw/messages/publish" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		publishRequests++
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
	}))
	defer server.Close()

	runCfg := config.Config{
		Repo:   "git@github.com:acme/repo.git",
		Prompt: "fix failing checks",
	}
	runCfg.ApplyDefaults()

	d := NewDaemon(failingRunner{err: errors.New("runner exploded")})
	failed := make(chan harness.Result, 1)
	d.OnDispatchFailed = func(requestID string, failedRunCfg config.Config, result harness.Result) {
		if requestID != "req-fail" {
			t.Fatalf("requestID = %q, want %q", requestID, "req-fail")
		}
		if got, want := strings.Join(failedRunCfg.RepoList(), ","), strings.Join(runCfg.RepoList(), ","); got != want {
			t.Fatalf("failed run repos = %q, want %q", got, want)
		}
		failed <- result
	}

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}

	d.handleDispatch(
		context.Background(),
		NewAsyncAPIClientFrom(NewAPIClient(server.URL+"/v1"), "test-token"),
		cfg,
		SkillDispatch{
			RequestID: "req-fail",
			Skill:     "code_for_me",
			ReplyTo:   "agent-123",
			Config:    runCfg,
		},
		"",
		false,
	)

	select {
	case result := <-failed:
		if result.Err == nil {
			t.Fatal("result.Err = nil, want non-nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnDispatchFailed callback")
	}

	if publishRequests != 1 {
		t.Fatalf("publish requests = %d, want 1", publishRequests)
	}
}

func TestProcessInboundMessagePublishesAcquireFailurePayload(t *testing.T) {
	t.Parallel()

	var (
		mu             sync.Mutex
		publishedMsg   map[string]any
		publishRequest int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/openclaw/messages/publish" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		defer r.Body.Close()

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode publish body: %v", err)
		}
		message, _ := body["message"].(map[string]any)

		mu.Lock()
		publishRequest++
		publishedMsg = message
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
	}))
	defer server.Close()

	d := NewDaemon(nil)
	failed := make(chan harness.Result, 1)
	d.OnDispatchFailed = func(requestID string, failedRunCfg config.Config, result harness.Result) {
		if requestID != "req-closed-controller" {
			t.Fatalf("requestID = %q, want %q", requestID, "req-closed-controller")
		}
		if got, want := strings.Join(failedRunCfg.RepoList(), ","), "git@github.com:acme/repo.git"; got != want {
			t.Fatalf("failed run repos = %q, want %q", got, want)
		}
		failed <- result
	}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{MaxParallel: 1},
	}

	dispatchController := NewAdaptiveDispatchController(cfg.Dispatcher, nil)
	dispatchController.close()

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-closed-controller",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship it",
		},
	}

	var workers sync.WaitGroup
	d.processInboundMessage(
		context.Background(),
		NewAsyncAPIClientFrom(NewAPIClient(server.URL+"/v1"), "agent-token"),
		cfg,
		msg,
		"",
		"",
		dispatchController,
		&workers,
		nil,
	)
	workers.Wait()

	select {
	case result := <-failed:
		if result.Err == nil {
			t.Fatal("result.Err = nil, want non-nil")
		}
		if got := result.Err.Error(); !strings.Contains(got, "dispatch controller is closed") {
			t.Fatalf("result.Err = %q, want dispatch controller closed detail", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnDispatchFailed callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if publishRequest != 1 {
		t.Fatalf("publish requests = %d, want 1", publishRequest)
	}
	if got := fmt.Sprint(publishedMsg["status"]); got != "error" {
		t.Fatalf("message.status = %v, want error", publishedMsg["status"])
	}
	if got := fmt.Sprint(publishedMsg["message"]); !strings.Contains(got, "Task failed. Error details: dispatch acquire: dispatch controller is closed") {
		t.Fatalf("message.message = %q", got)
	}
	if got := fmt.Sprint(publishedMsg["error"]); !strings.Contains(got, "dispatch acquire: dispatch controller is closed") {
		t.Fatalf("message.error = %q", got)
	}
}

func TestProcessInboundMessageInvokesOnDispatchFailedForAcquireFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/openclaw/messages/publish" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
	}))
	defer server.Close()

	d := NewDaemon(nil)
	failed := make(chan struct {
		requestID string
		runCfg    config.Config
		result    harness.Result
	}, 1)
	d.OnDispatchFailed = func(requestID string, failedRunCfg config.Config, result harness.Result) {
		failed <- struct {
			requestID string
			runCfg    config.Config
			result    harness.Result
		}{
			requestID: requestID,
			runCfg:    failedRunCfg,
			result:    result,
		}
	}

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{MaxParallel: 1},
	}

	dispatchController := NewAdaptiveDispatchController(cfg.Dispatcher, nil)
	dispatchController.close()

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-acquire-fail",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship it",
		},
	}

	var workers sync.WaitGroup
	d.processInboundMessage(
		context.Background(),
		NewAsyncAPIClientFrom(NewAPIClient(server.URL+"/v1"), "agent-token"),
		cfg,
		msg,
		"",
		"",
		dispatchController,
		&workers,
		nil,
	)
	workers.Wait()

	select {
	case got := <-failed:
		if got.requestID != "req-acquire-fail" {
			t.Fatalf("requestID = %q, want %q", got.requestID, "req-acquire-fail")
		}
		if gotRepos, wantRepos := strings.Join(got.runCfg.RepoList(), ","), "git@github.com:acme/repo.git"; gotRepos != wantRepos {
			t.Fatalf("failed run repos = %q, want %q", gotRepos, wantRepos)
		}
		if got.result.Err == nil {
			t.Fatal("result.Err = nil, want non-nil")
		}
		if !strings.Contains(got.result.Err.Error(), "dispatch acquire: dispatch controller is closed") {
			t.Fatalf("result.Err = %q", got.result.Err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnDispatchFailed callback")
	}
}

func TestProcessInboundMessageSkipsIgnoredLogForUnknownSkill(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	logs := make([]string, 0, 1)
	d.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel: 1,
		},
	}

	var workers sync.WaitGroup
	d.processInboundMessage(
		context.Background(),
		NewAsyncAPIClientFrom(APIClient{}, ""),
		cfg,
		map[string]any{"type": "status_update"},
		"",
		"",
		nil,
		&workers,
		nil,
	)

	if len(logs) != 0 {
		t.Fatalf("logs = %v, want none", logs)
	}
}

func TestProcessInboundMessageLogsIgnoredKnownSkill(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	logs := make([]string, 0, 1)
	d.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel: 1,
		},
	}

	var workers sync.WaitGroup
	d.processInboundMessage(
		context.Background(),
		NewAsyncAPIClientFrom(APIClient{}, ""),
		cfg,
		map[string]any{
			"type":  "skill_request",
			"skill": "other_skill",
		},
		"",
		"",
		nil,
		&workers,
		nil,
	)

	if len(logs) != 1 {
		t.Fatalf("len(logs) = %d, want 1 (%v)", len(logs), logs)
	}
	if !strings.Contains(logs[0], "dispatch status=ignored skill=other_skill") {
		t.Fatalf("ignored log = %q", logs[0])
	}
}

func TestProcessInboundMessageInvokesOnDispatchQueued(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	var (
		mu           sync.Mutex
		gotRequestID string
		gotRepo      string
		gotPrompt    string
	)
	d.OnDispatchQueued = func(requestID string, runCfg config.Config) {
		mu.Lock()
		defer mu.Unlock()
		gotRequestID = requestID
		gotRepo = runCfg.RepoURL
		gotPrompt = runCfg.Prompt
	}

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel: 1,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var workers sync.WaitGroup
	dispatchController := NewAdaptiveDispatchController(cfg.Dispatcher, nil)
	dispatchController.Start(ctx)

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "moltenhub_code_run",
		"request_id": "req-queued",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "ship rerun button",
		},
	}
	d.processInboundMessage(ctx, NewAsyncAPIClientFrom(APIClient{}, ""), cfg, msg, "", "", dispatchController, &workers, nil)

	mu.Lock()
	defer mu.Unlock()
	if gotRequestID != "req-queued" {
		t.Fatalf("request id = %q, want %q", gotRequestID, "req-queued")
	}
	if gotRepo != "git@github.com:acme/repo.git" {
		t.Fatalf("repo = %q, want %q", gotRepo, "git@github.com:acme/repo.git")
	}
	if gotPrompt != "ship rerun button" {
		t.Fatalf("prompt = %q, want %q", gotPrompt, "ship rerun button")
	}
}

type failingRunner struct {
	err error
}

func (r failingRunner) Run(_ context.Context, _ execx.Command) (execx.Result, error) {
	if r.err == nil {
		r.err = errors.New("runner failed")
	}
	return execx.Result{}, r.err
}
