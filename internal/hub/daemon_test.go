package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
)

func TestApplyStoredRuntimeConfigOverridesTokens(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		BindToken:  "bind_token",
		SessionKey: "main",
	}
	stored := RuntimeConfig{
		BaseURL:    "https://na.hub.molten.bot/v1",
		Token:      "agent_saved",
		SessionKey: "saved-session",
	}

	applied := applyStoredRuntimeConfig(&cfg, stored)
	if !applied {
		t.Fatal("applied = false, want true")
	}
	if cfg.AgentToken != "agent_saved" {
		t.Fatalf("AgentToken = %q", cfg.AgentToken)
	}
	if cfg.BindToken != "" {
		t.Fatalf("BindToken = %q, want empty", cfg.BindToken)
	}
	if cfg.SessionKey != "saved-session" {
		t.Fatalf("SessionKey = %q", cfg.SessionKey)
	}
}

func TestApplyStoredRuntimeConfigNoToken(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{BindToken: "bind_token"}
	applied := applyStoredRuntimeConfig(&cfg, RuntimeConfig{Token: ""})
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
		BaseURL:    "https://na.hub.molten.bot/v1",
		Token:      "agent_saved",
		SessionKey: "saved-session",
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
		BaseURL:   "https://na.hub.molten.bot/v1",
		BindToken: "bind_token",
	}
	stored := RuntimeConfig{
		BaseURL:    "http://127.0.0.1:37991/v1",
		Token:      "agent_saved",
		SessionKey: "saved-session",
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

func TestLoadStoredRuntimeConfigFallsBackToLegacyPath(t *testing.T) {
	root := t.TempDir()
	primaryPath := filepath.Join(root, "home", ".moltenhub", "config.json")
	legacyPath := filepath.Join(root, "legacy", ".moltenhub", "config.json")

	if err := SaveRuntimeConfig(legacyPath, "https://na.hub.molten.bot/v1", "agent_legacy", "main"); err != nil {
		t.Fatalf("SaveRuntimeConfig(legacy) error = %v", err)
	}

	cfg, loadedPath, err := loadStoredRuntimeConfigWithLegacyPath(primaryPath, legacyPath)
	if err != nil {
		t.Fatalf("loadStoredRuntimeConfigWithLegacyPath() error = %v", err)
	}
	if loadedPath != legacyPath {
		t.Fatalf("loadedPath = %q, want %q", loadedPath, legacyPath)
	}
	if cfg.Token != "agent_legacy" {
		t.Fatalf("Token = %q, want %q", cfg.Token, "agent_legacy")
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
	if _, ok := result["required_schema"]; !ok {
		t.Fatalf("required_schema missing: %#v", result)
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
	prURLs, ok := result["pr_urls"].([]string)
	if !ok {
		t.Fatalf("pr_urls missing or wrong type: %#v", result["pr_urls"])
	}
	if len(prURLs) != 2 {
		t.Fatalf("len(pr_urls) = %d, want 2", len(prURLs))
	}
	repoResults, ok := result["repo_results"].([]map[string]any)
	if !ok {
		t.Fatalf("repo_results missing or wrong type: %#v", result["repo_results"])
	}
	if len(repoResults) != 2 {
		t.Fatalf("len(repo_results) = %d, want 2", len(repoResults))
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
		NewAPIClient(server.URL+"/v1"),
		"test-token",
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
		NewAPIClient(server.URL+"/v1"),
		"agent-token",
		cfg,
		msg,
		"",
		"",
		dispatchController,
		&workers,
		nil,
	)
	workers.Wait()

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
		APIClient{},
		"",
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
		APIClient{},
		"",
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
	d.processInboundMessage(ctx, APIClient{}, "", cfg, msg, "", "", dispatchController, &workers, nil)

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
