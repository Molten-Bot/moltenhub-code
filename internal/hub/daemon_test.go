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

	"github.com/Molten-Bot/moltenhub-code/internal/app"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/a2aproject/a2a-go/v2/a2a"
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
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "codex",
		SessionKey:   "main",
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
	t.Setenv(runtimeConfigPathEnv, "")

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
		case "/v1/agents/me/metadata", "/v1/agents/me/status", "/v1/agents/me/activities":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/runtime/messages/pull":
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

	runtimeCfgPath := defaultRuntimeConfigPath()
	if err := os.MkdirAll(filepath.Dir(runtimeCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir runtime config dir: %v", err)
	}
	runtimeCfgJSON := fmt.Sprintf(
		`{"baseUrl":%q,"token":%q,"agent_harness":"codex","sessionKey":"main"}`,
		base,
		token,
	)
	if err := os.WriteFile(runtimeCfgPath, []byte(runtimeCfgJSON), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
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
	t.Setenv(runtimeConfigPathEnv, "")

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
		case "/v1/agents/me/metadata", "/v1/agents/me/status", "/v1/agents/me/activities":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/runtime/messages/ws":
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		case "/v1/runtime/messages/pull":
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
		`{"baseUrl":%q,"token":"agent_saved","agent_harness":"codex","sessionKey":"main","timeoutMs":%d}`,
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
	requiredSchema, ok := result["requiredSchema"].(map[string]any)
	if !ok {
		t.Fatalf("requiredSchema missing: %#v", result)
	}
	runConfigSchema, _ := requiredSchema["run_config_schema"].(map[string]any)
	branchConditions, _ := runConfigSchema["allOf"].([]map[string]any)
	encodedConditions, err := json.Marshal(branchConditions)
	if err != nil {
		t.Fatalf("json.Marshal(requiredSchema branch conditions) error = %v", err)
	}
	if !strings.Contains(string(encodedConditions), `"const":"fix-merge-main"`) {
		t.Fatalf("requiredSchema branch conditions = %s, want fix-merge-main condition", encodedConditions)
	}
	if got := payload["status"]; got != "error" {
		t.Fatalf("status = %#v, want %q", got, "error")
	}
	if got := payload["message"]; got != "Failure: task failed.\nError details: dispatch parse: bad payload" {
		t.Fatalf("message = %#v", got)
	}
	if got := payload["error"]; got != "dispatch parse: bad payload" {
		t.Fatalf("error = %#v", got)
	}
	if got := result["status"]; got != "failed" {
		t.Fatalf("result.status = %#v", got)
	}
	if got := result["message"]; got != "Failure: task failed.\nError details: dispatch parse: bad payload" {
		t.Fatalf("result.message = %#v", got)
	}
	if got := result["error"]; got != "dispatch parse: bad payload" {
		t.Fatalf("result.error = %#v", got)
	}
	failure, ok := payload["failure"].(map[string]any)
	if !ok {
		t.Fatalf("failure missing or wrong type: %#v", payload["failure"])
	}
	if got := failure["status"]; got != "failed" {
		t.Fatalf("failure.status = %#v", got)
	}
	if got := failure["message"]; got != "Failure: task failed.\nError details: dispatch parse: bad payload" {
		t.Fatalf("failure.message = %#v", got)
	}
	if got := failure["error"]; got != "dispatch parse: bad payload" {
		t.Fatalf("failure.error = %#v", got)
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
		HubTaskID: "hub-task-22",
		Skill:     "code_for_me",
	}
	res := app.Result{
		ExitCode:     app.ExitSuccess,
		WorkspaceDir: "/tmp/run",
		Branch:       "moltenhub-feature",
		PRURL:        "https://github.com/acme/repo-a/pull/10",
		RepoResults: []app.RepoResult{
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
	if got := payload["hub_task_id"]; got != "hub-task-22" {
		t.Fatalf("hub_task_id = %#v", got)
	}
	if got := payload["a2a_task_id"]; got != "hub-task-22" {
		t.Fatalf("a2a_task_id = %#v", got)
	}
	if got := result["hubTaskId"]; got != "hub-task-22" {
		t.Fatalf("result.hubTaskId = %#v", got)
	}
	if got := result["a2aTaskId"]; got != "hub-task-22" {
		t.Fatalf("result.a2aTaskId = %#v", got)
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

func TestDispatchStatusPayloadUsesA2AStatusUpdateAndOriginator(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID:           "req-status",
		HubTaskID:           "hub-task-status",
		ContextID:           "ctx-status",
		Skill:               "code_for_me",
		ReplyTo:             "https://na.hub.molten.bot/acme/caller",
		Originator:          "https://na.hub.molten.bot/acme/caller",
		OriginatorAgentURI:  "https://na.hub.molten.bot/acme/caller",
		OriginatorAgentUUID: "caller-uuid",
	}

	payload := dispatchStatusPayload(cfg, dispatch, "working", a2a.TaskStateWorking, "Task running.", nil)
	if got := payload["type"]; got != dispatchTaskStatusType {
		t.Fatalf("type = %#v, want %q", got, dispatchTaskStatusType)
	}
	if got := payload["reply_to"]; got != "https://na.hub.molten.bot/acme/caller" {
		t.Fatalf("reply_to = %#v", got)
	}
	if got := payload["to"]; got != "https://na.hub.molten.bot/acme/caller" {
		t.Fatalf("to = %#v", got)
	}
	if got := payload["a2a_task_id"]; got != "hub-task-status" {
		t.Fatalf("a2a_task_id = %#v", got)
	}
	if got := payload["client_msg_id"]; got != "req-status-status-working" {
		t.Fatalf("client_msg_id = %#v", got)
	}
	originator, _ := payload["originator"].(map[string]any)
	if originator == nil {
		t.Fatalf("originator missing: %#v", payload)
	}
	if got := originator["type"]; got != "agent" {
		t.Fatalf("originator.type = %#v, want agent", got)
	}
	if got := originator["agent_uri"]; got != "https://na.hub.molten.bot/acme/caller" {
		t.Fatalf("originator.agent_uri = %#v", got)
	}

	statusUpdate, _ := payload["statusUpdate"].(map[string]any)
	if statusUpdate == nil {
		t.Fatalf("statusUpdate missing: %#v", payload)
	}
	if got := statusUpdate["taskId"]; got != "hub-task-status" {
		t.Fatalf("statusUpdate.taskId = %#v", got)
	}
	if got := statusUpdate["contextId"]; got != "ctx-status" {
		t.Fatalf("statusUpdate.contextId = %#v", got)
	}
	status, _ := statusUpdate["status"].(map[string]any)
	if got := status["state"]; got != "TASK_STATE_WORKING" {
		t.Fatalf("statusUpdate.status.state = %#v", got)
	}
	message, _ := status["message"].(map[string]any)
	parts, _ := message["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("status message parts = %#v", message["parts"])
	}
	part, _ := parts[0].(map[string]any)
	if got := part["text"]; got != "Task running." {
		t.Fatalf("status message text = %#v", got)
	}
}

func TestDispatchStatusPayloadFailureIncludesExplicitFields(t *testing.T) {
	t.Parallel()

	payload := dispatchStatusPayload(
		InitConfig{Skill: SkillConfig{Name: "code_for_me"}},
		SkillDispatch{RequestID: "req-status-failed", Skill: "code_for_me"},
		"error",
		a2a.TaskStateFailed,
		"Failure: task failed.\nError details: checks: required PR checks failed",
		map[string]any{
			"error":          "checks: required PR checks failed",
			"Failure:":       "task failed",
			"Error details:": "checks: required PR checks failed",
		},
	)

	if got := payload["Failure:"]; got != "task failed" {
		t.Fatalf("Failure: = %#v", got)
	}
	if got := payload["Error details:"]; got != "checks: required PR checks failed" {
		t.Fatalf("Error details: = %#v", got)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details = %#v, want map", payload["details"])
	}
	if got := details["Failure:"]; got != "task failed" {
		t.Fatalf("details.Failure: = %#v", got)
	}
	if got := details["Error details:"]; got != "checks: required PR checks failed" {
		t.Fatalf("details.Error details: = %#v", got)
	}
}

func TestDispatchStatusPayloadIncludesStageMetadata(t *testing.T) {
	t.Parallel()

	status, state, message, details, ok := dispatchStatusFromHarnessLogLine(
		`stage=clone status=warn action=fallback_repo_owner err="repository \"missing\" not found"`,
	)
	if !ok {
		t.Fatal("dispatchStatusFromHarnessLogLine() ok = false, want true")
	}
	if status != "working" {
		t.Fatalf("status = %q, want working", status)
	}
	if state != a2a.TaskStateWorking {
		t.Fatalf("state = %s, want TASK_STATE_WORKING", state)
	}
	if message != "Task status updated." {
		t.Fatalf("message = %q", message)
	}
	if got := details["status_action"]; got != "Task stage updated: clone warn." {
		t.Fatalf("details.status_action = %#v", got)
	}
	if got := details["stage"]; got != "clone" {
		t.Fatalf("details.stage = %#v", got)
	}
	if got := details["stage_status"]; got != "warn" {
		t.Fatalf("details.stage_status = %#v", got)
	}
	if got := details["err"]; got != `repository "missing" not found` {
		t.Fatalf("details.err = %#v", got)
	}

	payload := dispatchStatusPayload(
		InitConfig{Skill: SkillConfig{Name: "code_for_me"}},
		SkillDispatch{RequestID: "req-stage", HubTaskID: "task-stage", ContextID: "ctx-stage", Skill: "code_for_me"},
		status,
		state,
		message,
		details,
	)
	statusUpdate, _ := payload["statusUpdate"].(map[string]any)
	metadata, _ := statusUpdate["metadata"].(map[string]any)
	if got := metadata["stage"]; got != "clone" {
		t.Fatalf("statusUpdate.metadata.stage = %#v", got)
	}
	if got := metadata["stage_status"]; got != "warn" {
		t.Fatalf("statusUpdate.metadata.stage_status = %#v", got)
	}
	if got := payload["action"]; got != "Task stage updated: clone warn." {
		t.Fatalf("action = %#v", got)
	}
	clientMsgID := fmt.Sprint(payload["client_msg_id"])
	if !strings.Contains(clientMsgID, "clone-warn") {
		t.Fatalf("client_msg_id = %q, want stage-specific id", clientMsgID)
	}
}

func TestDispatchStatusFromHarnessLogLineFailureDetailsIncludeExplicitFields(t *testing.T) {
	t.Parallel()

	status, state, message, details, ok := dispatchStatusFromHarnessLogLine(
		`stage=checks status=error err="required PR checks failed after 3 remediation attempt(s)"`,
	)
	if !ok {
		t.Fatal("dispatchStatusFromHarnessLogLine() ok = false, want true")
	}
	if status != "error" {
		t.Fatalf("status = %q, want error", status)
	}
	if state != a2a.TaskStateFailed {
		t.Fatalf("state = %s, want TASK_STATE_FAILED", state)
	}
	if message != "Failure: task failed.\nError details: required PR checks failed after 3 remediation attempt(s)" {
		t.Fatalf("message = %q", message)
	}
	if got := details["Failure:"]; got != "task failed" {
		t.Fatalf("details.Failure: = %#v", got)
	}
	if got := details["Error details:"]; got != "required PR checks failed after 3 remediation attempt(s)" {
		t.Fatalf("details.Error details: = %#v", got)
	}
}

func TestDispatchChildWorkflowStatusFromHarnessLogLineBuildsChildPayload(t *testing.T) {
	t.Parallel()

	parent := SkillDispatch{
		RequestID: "req-parent",
		HubTaskID: "task-parent",
		ContextID: "ctx-parent",
		Skill:     "code_for_me",
		Config: config.Config{
			LibraryTaskName: "unit-test-coverage",
		},
	}
	line := `stage=codex status=start target=services/api agent_run_id=agent-implementation-1 agent_harness=codex mode=implementation attempt=1 repo=git@github.com:acme/private.git repo_dir=repo`

	parentStatus, parentState, _, parentDetails, ok := dispatchStatusFromHarnessLogLine(line)
	if !ok {
		t.Fatal("dispatchStatusFromHarnessLogLine() ok = false, want true")
	}
	if parentStatus != "working" || parentState != a2a.TaskStateWorking {
		t.Fatalf("parent status = (%q, %s), want working/TASK_STATE_WORKING", parentStatus, parentState)
	}
	if got := parentDetails["agent_run_id"]; got != "agent-implementation-1" {
		t.Fatalf("parent details agent_run_id = %#v", got)
	}

	child, status, state, message, details, ok := dispatchChildWorkflowStatusFromHarnessLogLine(parent, line)
	if !ok {
		t.Fatal("dispatchChildWorkflowStatusFromHarnessLogLine() ok = false, want true")
	}
	childTaskID := "task-parent-child-agent-implementation-1"
	if child.RequestID != childTaskID || child.HubTaskID != childTaskID {
		t.Fatalf("child ids = request:%q hub:%q, want %q", child.RequestID, child.HubTaskID, childTaskID)
	}
	if child.ContextID != "ctx-parent" {
		t.Fatalf("child context = %q, want ctx-parent", child.ContextID)
	}
	if status != "start" || state != a2a.TaskStateSubmitted {
		t.Fatalf("child status = (%q, %s), want start/TASK_STATE_SUBMITTED", status, state)
	}
	if message != "Agent invocation queued." {
		t.Fatalf("child message = %q", message)
	}
	for key, want := range map[string]any{
		"parent_task_id":     "task-parent",
		"parent_request_id":  "req-parent",
		"workflow_node_type": workflowNodeTypeAgentInvocation,
		"agent_harness":      "codex",
		"agent_run_id":       "agent-implementation-1",
		"mode":               "implementation",
		"attempt":            "1",
		"repo":               "repo",
		"repo_dir":           "repo",
		"target":             "services/api",
		"stage":              "codex",
		"stage_status":       "start",
	} {
		if got := details[key]; got != want {
			t.Fatalf("details[%s] = %#v, want %#v", key, got, want)
		}
	}

	payload := dispatchStatusPayload(InitConfig{Skill: runtimeSkillConfig()}, child, status, state, message, details)
	if got := payload["a2a_task_id"]; got != childTaskID {
		t.Fatalf("a2a_task_id = %#v, want %q", got, childTaskID)
	}
	if got := payload["context_id"]; got != "ctx-parent" {
		t.Fatalf("context_id = %#v, want ctx-parent", got)
	}
	if got := payload["a2a_state"]; got != a2a.TaskStateSubmitted.String() {
		t.Fatalf("a2a_state = %#v, want %s", got, a2a.TaskStateSubmitted)
	}
	payloadDetails, _ := payload["details"].(map[string]any)
	if _, hasInternal := payloadDetails["_workflow_child_status"]; hasInternal {
		t.Fatalf("payload details leaked internal workflow marker: %#v", payloadDetails)
	}

	statusUpdate, _ := payload["statusUpdate"].(map[string]any)
	metadata, _ := statusUpdate["metadata"].(map[string]any)
	for key, want := range map[string]any{
		"parent_task_id":     "task-parent",
		"workflow_node_type": workflowNodeTypeAgentInvocation,
		"agent_harness":      "codex",
		"agent_run_id":       "agent-implementation-1",
		"mode":               "implementation",
		"repo":               "repo",
		"repo_dir":           "repo",
		"target":             "services/api",
		"stage":              "codex",
		"stage_status":       "start",
	} {
		if got := metadata[key]; got != want {
			t.Fatalf("statusUpdate.metadata[%s] = %#v, want %#v", key, got, want)
		}
	}
}

func TestChildWorkflowStatusStateMapping(t *testing.T) {
	t.Parallel()

	parent := SkillDispatch{RequestID: "req-parent", HubTaskID: "task-parent", ContextID: "ctx-parent"}
	tests := []struct {
		status string
		state  a2a.TaskState
		extra  string
	}{
		{status: "start", state: a2a.TaskStateSubmitted},
		{status: "running", state: a2a.TaskStateWorking},
		{status: "ok", state: a2a.TaskStateCompleted},
		{status: "error", state: a2a.TaskStateFailed, extra: ` err="boom"`},
		{status: "stopped", state: a2a.TaskStateCanceled},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()

			line := fmt.Sprintf(`stage=claude status=%s agent_run_id=agent-run agent_harness=claude mode=implementation attempt=1%s`, tt.status, tt.extra)
			_, status, state, _, details, ok := dispatchChildWorkflowStatusFromHarnessLogLine(parent, line)
			if !ok {
				t.Fatal("dispatchChildWorkflowStatusFromHarnessLogLine() ok = false, want true")
			}
			if status != tt.status || state != tt.state {
				t.Fatalf("status/state = (%q, %s), want (%q, %s)", status, state, tt.status, tt.state)
			}
			if tt.status == "error" && details["error"] != "boom" {
				t.Fatalf("error details = %#v, want boom", details["error"])
			}
		})
	}
}

func TestAgentInvocationErrorLogKeepsParentStatusWorking(t *testing.T) {
	t.Parallel()

	line := `stage=codex status=error agent_run_id=agent-implementation-1 agent_harness=codex mode=implementation attempt=1 repo=repo repo_dir=repo err="sandbox blocked local commands"`
	status, state, message, details, ok := dispatchStatusFromHarnessLogLine(line)
	if !ok {
		t.Fatal("dispatchStatusFromHarnessLogLine() ok = false, want true")
	}
	if status != "working" {
		t.Fatalf("parent status = %q, want working", status)
	}
	if state != a2a.TaskStateWorking {
		t.Fatalf("parent state = %s, want TASK_STATE_WORKING", state)
	}
	if message != "Task status updated." {
		t.Fatalf("parent message = %q, want non-terminal update", message)
	}
	if got := details["stage_status"]; got != "error" {
		t.Fatalf("details.stage_status = %#v, want error", got)
	}

	parent := SkillDispatch{RequestID: "req-parent", HubTaskID: "task-parent", ContextID: "ctx-parent"}
	child, childStatus, childState, _, _, ok := dispatchChildWorkflowStatusFromHarnessLogLine(parent, line)
	if !ok {
		t.Fatal("dispatchChildWorkflowStatusFromHarnessLogLine() ok = false, want true")
	}
	if child.HubTaskID != "task-parent-child-agent-implementation-1" {
		t.Fatalf("child task id = %q", child.HubTaskID)
	}
	if childStatus != "error" || childState != a2a.TaskStateFailed {
		t.Fatalf("child status/state = (%q, %s), want error/TASK_STATE_FAILED", childStatus, childState)
	}
}

type workflowVisibilityRunner struct {
	targetSubdir string
}

func (r *workflowVisibilityRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	if cmd.Name == "git" && len(cmd.Args) > 0 {
		switch cmd.Args[0] {
		case "clone":
			if len(cmd.Args) > 0 {
				repoDir := cmd.Args[len(cmd.Args)-1]
				if err := os.MkdirAll(filepath.Join(repoDir, r.targetSubdir), 0o755); err != nil {
					return execx.Result{}, err
				}
			}
		case "status":
			return execx.Result{Stdout: ""}, nil
		}
	}
	if cmd.Name == "gh" && len(cmd.Args) >= 2 && cmd.Args[0] == "pr" && cmd.Args[1] == "list" {
		return execx.Result{Stdout: "[]"}, nil
	}
	return execx.Result{}, nil
}

func TestHandleDispatchPublishesAgentLogsOnParentTaskOnly(t *testing.T) {
	t.Parallel()

	runCfg := config.Config{
		RepoURL:      "git@github.com:acme/repo.git",
		Repo:         "git@github.com:acme/repo.git",
		BaseBranch:   "main",
		TargetSubdir: "services/api",
		Prompt:       "review API workflow logs",
	}
	runCfg.ApplyDefaults()

	api := &stubMoltenHubAPI{token: "t"}
	d := NewDaemon(&workflowVisibilityRunner{targetSubdir: runCfg.TargetSubdir})
	finalState := d.handleDispatch(
		context.Background(),
		api,
		InitConfig{Skill: runtimeSkillConfig()},
		SkillDispatch{
			RequestID: "req-workflow",
			HubTaskID: "task-workflow",
			ContextID: "ctx-workflow",
			Skill:     "code_for_me",
			ReplyTo:   "caller-agent",
			Config:    runCfg,
		},
		"",
		false,
	)
	if finalState != "no_changes" {
		t.Fatalf("handleDispatch() final state = %q, want no_changes", finalState)
	}

	api.mu.Lock()
	statusUpdates := statusPayloads(api.published)
	api.mu.Unlock()

	var parentStart map[string]any
	for _, payload := range statusUpdates {
		details, _ := payload["details"].(map[string]any)
		if details["stage"] != "codex" || details["stage_status"] != "start" {
			continue
		}
		switch payload["a2a_task_id"] {
		case "task-workflow":
			parentStart = payload
		case "task-workflow-child-agent-implementation-1":
			t.Fatalf("unexpected child workflow task status: %#v", payload)
		}
	}
	if parentStart == nil {
		t.Fatalf("parent codex start status missing: %#v", statusUpdates)
	}
	parentStatusUpdate, _ := parentStart["statusUpdate"].(map[string]any)
	parentMetadata, _ := parentStatusUpdate["metadata"].(map[string]any)
	if _, hasWorkflowNode := parentMetadata["workflow_node_type"]; hasWorkflowNode {
		t.Fatalf("parent metadata unexpectedly marked as workflow child: %#v", parentMetadata)
	}
}

func TestDispatchResultPayloadNoChangesIncludesExistingPRURLs(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-no-change",
		Skill:     "code_for_me",
	}
	res := app.Result{
		ExitCode:  app.ExitSuccess,
		NoChanges: true,
		PRURL:     "https://github.com/acme/repo-a/pull/10",
		RepoResults: []app.RepoResult{
			{
				RepoURL: "git@github.com:acme/repo-a.git",
				RepoDir: "/tmp/run/repo",
				Branch:  "release/2026.04-hotfix",
				PRURL:   "https://github.com/acme/repo-a/pull/10",
				Changed: false,
			},
		},
	}

	payload := dispatchResultPayload(cfg, dispatch, res)
	if got := payload["status"]; got != "completed" {
		t.Fatalf("status = %#v, want %q", got, "completed")
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing or wrong type: %#v", payload["result"])
	}
	if got := result["prUrl"]; got != "https://github.com/acme/repo-a/pull/10" {
		t.Fatalf("prUrl = %#v", got)
	}
	prURLs, ok := result["prUrls"].([]string)
	if !ok {
		t.Fatalf("prUrls missing or wrong type: %#v", result["prUrls"])
	}
	if len(prURLs) != 1 || prURLs[0] != "https://github.com/acme/repo-a/pull/10" {
		t.Fatalf("prUrls = %#v, want [https://github.com/acme/repo-a/pull/10]", prURLs)
	}
	wantMessage := "[PR is ready](https://github.com/acme/repo-a/pull/10) for your review."
	if got := payload["message"]; got != wantMessage {
		t.Fatalf("message = %#v, want %q", got, wantMessage)
	}
	if got := payload["response"]; got != wantMessage {
		t.Fatalf("response = %#v, want %q", got, wantMessage)
	}
	if got := result["status"]; got != "completed" {
		t.Fatalf("result.status = %#v, want %q", got, "completed")
	}
	if got := result["message"]; got != wantMessage {
		t.Fatalf("result.message = %#v, want %q", got, wantMessage)
	}
	if got := result["response"]; got != wantMessage {
		t.Fatalf("result.response = %#v, want %q", got, wantMessage)
	}
}

func TestDispatchResultPayloadNoChangesWithoutPRIncludesExplicitMessage(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-no-change-no-pr",
		Skill:     "code_for_me",
		ReplyTo:   "agent-123",
	}
	res := app.Result{
		ExitCode:  app.ExitSuccess,
		NoChanges: true,
	}

	payload := dispatchResultPayload(cfg, dispatch, res)
	if got := payload["status"]; got != "no_changes" {
		t.Fatalf("status = %#v, want %q", got, "no_changes")
	}
	if got := payload["message"]; got != "No changes: task completed without repository changes or pull requests." {
		t.Fatalf("message = %#v", got)
	}
	if got := payload["reply_to"]; got != "agent-123" {
		t.Fatalf("reply_to = %#v, want %q", got, "agent-123")
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		t.Fatal("result payload missing")
	}
	if got := result["status"]; got != "no_changes" {
		t.Fatalf("result.status = %#v, want %q", got, "no_changes")
	}
	if got := result["message"]; got != "No changes: task completed without repository changes or pull requests." {
		t.Fatalf("result.message = %#v", got)
	}
}

func TestDispatchResultPayloadCompletedIncludesExplicitMessage(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-success",
		Skill:     "code_for_me",
		ReplyTo:   "agent-123",
	}
	res := app.Result{
		ExitCode: app.ExitSuccess,
		Branch:   "moltenhub-fix-success-response",
	}

	payload := dispatchResultPayload(cfg, dispatch, res)
	if got := payload["status"]; got != "completed" {
		t.Fatalf("status = %#v, want %q", got, "completed")
	}
	if got := payload["message"]; got != "Success: task completed." {
		t.Fatalf("message = %#v, want %q", got, "Success: task completed.")
	}
	if got := payload["reply_to"]; got != "agent-123" {
		t.Fatalf("reply_to = %#v, want %q", got, "agent-123")
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		t.Fatal("result payload missing")
	}
	if got := result["status"]; got != "completed" {
		t.Fatalf("result.status = %#v, want %q", got, "completed")
	}
	if got := result["message"]; got != "Success: task completed." {
		t.Fatalf("result.message = %#v, want %q", got, "Success: task completed.")
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
	res := app.Result{
		ExitCode: app.ExitCodex,
		Err:      fmt.Errorf("codex: process exited with status 1"),
	}

	payload := dispatchResultPayload(cfg, dispatch, res)
	if got := payload["status"]; got != "error" {
		t.Fatalf("status = %#v, want %q", got, "error")
	}
	if got := payload["failed"]; got != true {
		t.Fatalf("failed = %#v, want true", got)
	}
	if got := payload["error"]; got != "codex: process exited with status 1" {
		t.Fatalf("error = %#v", got)
	}
	if got := payload["Failure:"]; got != "task failed" {
		t.Fatalf("Failure: = %#v", got)
	}
	if got := payload["Error details:"]; got != "codex: process exited with status 1" {
		t.Fatalf("Error details: = %#v", got)
	}
	if got := payload["message"]; got != "Failure: task failed.\nError details: codex: process exited with status 1" {
		t.Fatalf("message = %#v", got)
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		t.Fatal("result payload missing")
	}
	if got := result["status"]; got != "failed" {
		t.Fatalf("result.status = %#v", got)
	}
	if got := result["message"]; got != "Failure: task failed.\nError details: codex: process exited with status 1" {
		t.Fatalf("result.message = %#v", got)
	}
	if got := result["error"]; got != "codex: process exited with status 1" {
		t.Fatalf("result.error = %#v", got)
	}
	if got := result["Failure:"]; got != "task failed" {
		t.Fatalf("result.Failure: = %#v", got)
	}
	if got := result["Error details:"]; got != "codex: process exited with status 1" {
		t.Fatalf("result.Error details: = %#v", got)
	}
	failure, _ := payload["failure"].(map[string]any)
	if failure == nil {
		t.Fatal("failure payload missing")
	}
	if got := failure["status"]; got != "failed" {
		t.Fatalf("failure.status = %#v", got)
	}
	if got := failure["message"]; got != "Failure: task failed.\nError details: codex: process exited with status 1" {
		t.Fatalf("failure.message = %#v", got)
	}
	if got := failure["error"]; got != "codex: process exited with status 1" {
		t.Fatalf("failure.error = %#v", got)
	}
	if got := failure["Failure:"]; got != "task failed" {
		t.Fatalf("failure.Failure: = %#v", got)
	}
	if got := failure["Error details:"]; got != "codex: process exited with status 1" {
		t.Fatalf("failure.Error details: = %#v", got)
	}
	details, _ := failure["details"].(map[string]any)
	if details == nil {
		t.Fatal("failure.details missing")
	}
	if got := details["status"]; got != "failed" {
		t.Fatalf("failure.details.status = %#v", got)
	}
	if got := details["message"]; got != "Failure: task failed.\nError details: codex: process exited with status 1" {
		t.Fatalf("failure.details.message = %#v", got)
	}
	if got := details["error"]; got != "codex: process exited with status 1" {
		t.Fatalf("failure.details.error = %#v", got)
	}
	if got := details["Failure:"]; got != "task failed" {
		t.Fatalf("failure.details.Failure: = %#v", got)
	}
	if got := details["Error details:"]; got != "codex: process exited with status 1" {
		t.Fatalf("failure.details.Error details: = %#v", got)
	}
}

func TestDispatchResultPayloadStoppedOmitsFailureFields(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-stopped",
		Skill:     "code_for_me",
		ReplyTo:   "agent-123",
	}
	res := app.Result{
		ExitCode: app.ExitPreflight,
		Err:      fmt.Errorf("task was stopped by operator"),
	}

	payload := dispatchResultPayloadWithStatus(cfg, dispatch, res, "stopped")
	if got := payload["status"]; got != "stopped" {
		t.Fatalf("status = %#v, want stopped", got)
	}
	if got := payload["failed"]; got != false {
		t.Fatalf("failed = %#v, want false", got)
	}
	if got := payload["ok"]; got != false {
		t.Fatalf("ok = %#v, want false", got)
	}
	if got := payload["message"]; got != "Task stopped by operator." {
		t.Fatalf("message = %#v", got)
	}
	for _, key := range []string{"error", "Failure", "Failure:", "Error details", "Error details:", "failure"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload %s present: %#v", key, payload[key])
		}
	}
	if got := payload["reason"]; got != "task was stopped by operator" {
		t.Fatalf("reason = %#v", got)
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		t.Fatal("result payload missing")
	}
	if got := result["status"]; got != "stopped" {
		t.Fatalf("result.status = %#v, want stopped", got)
	}
	if got := result["message"]; got != "Task stopped by operator." {
		t.Fatalf("result.message = %#v", got)
	}
	for _, key := range []string{"error", "Failure", "Failure:", "Error details", "Error details:"} {
		if _, ok := result[key]; ok {
			t.Fatalf("result %s present: %#v", key, result[key])
		}
	}
	if got := result["reason"]; got != "task was stopped by operator" {
		t.Fatalf("result.reason = %#v", got)
	}
}

func TestDuplicateDispatchResultPayloadIncludesDuplicateMetadataAndFailureDetails(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Skill: SkillConfig{
			Name:       "code_for_me",
			ResultType: "skill_result",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-dup",
		Skill:     "code_for_me",
		ReplyTo:   "agent-123",
	}

	payload := duplicateDispatchResultPayload(cfg, dispatch, "in_flight", "req-existing")
	if got := payload["status"]; got != "duplicate" {
		t.Fatalf("status = %#v, want duplicate", got)
	}
	if got := payload["failed"]; got != true {
		t.Fatalf("failed = %#v, want true", got)
	}
	if got := payload["reply_to"]; got != "agent-123" {
		t.Fatalf("reply_to = %#v, want agent-123", got)
	}
	if got := payload["error"]; got != "duplicate submission ignored (request_id=req-existing state=in_flight)" {
		t.Fatalf("error = %#v", got)
	}
	if got := payload["duplicate"]; got != true {
		t.Fatalf("duplicate = %#v, want true", got)
	}
	if got := payload["state"]; got != "in_flight" {
		t.Fatalf("state = %#v, want in_flight", got)
	}
	if got := payload["duplicate_of"]; got != "req-existing" {
		t.Fatalf("duplicate_of = %#v, want req-existing", got)
	}
	if got := payload["message"]; got != "Failure: task failed.\nError details: duplicate submission ignored (request_id=req-existing state=in_flight)" {
		t.Fatalf("message = %#v", got)
	}

	result, _ := payload["result"].(map[string]any)
	if result == nil {
		t.Fatal("result payload missing")
	}
	if got := result["status"]; got != "duplicate" {
		t.Fatalf("result.status = %#v, want duplicate", got)
	}
	if got := result["duplicate"]; got != true {
		t.Fatalf("result.duplicate = %#v, want true", got)
	}
	if got := result["state"]; got != "in_flight" {
		t.Fatalf("result.state = %#v, want in_flight", got)
	}
	if got := result["duplicate_of"]; got != "req-existing" {
		t.Fatalf("result.duplicate_of = %#v, want req-existing", got)
	}

	failure, _ := payload["failure"].(map[string]any)
	if failure == nil {
		t.Fatal("failure payload missing")
	}
	if got := failure["duplicate"]; got != true {
		t.Fatalf("failure.duplicate = %#v, want true", got)
	}
	if got := failure["state"]; got != "in_flight" {
		t.Fatalf("failure.state = %#v, want in_flight", got)
	}
	if got := failure["duplicate_of"]; got != "req-existing" {
		t.Fatalf("failure.duplicate_of = %#v, want req-existing", got)
	}
}

func TestHandleDispatchInvokesOnDispatchFailed(t *testing.T) {
	t.Parallel()

	var (
		mu             sync.Mutex
		publishedMsgs  []map[string]any
		offlineReasons []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"activities":["started"]}}}}`))
		case "/v1/agents/me/metadata", "/v1/agents/me/activities":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/a2a":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case "/v1/runtime/messages/publish":
			defer r.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode publish body: %v", err)
			}
			message, _ := body["message"].(map[string]any)
			mu.Lock()
			publishedMsgs = append(publishedMsgs, message)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
		case "/v1/runtime/messages/offline":
			defer r.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode offline body: %v", err)
			}
			mu.Lock()
			offlineReasons = append(offlineReasons, fmt.Sprint(body["reason"]))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	runCfg := config.Config{
		Repo:   "git@github.com:acme/repo.git",
		Prompt: "fix failing checks",
	}
	runCfg.ApplyDefaults()

	d := NewDaemon(failingRunner{err: errors.New("runner exploded")})
	failed := make(chan app.Result, 1)
	d.OnDispatchFailed = func(requestID string, failedRunCfg config.Config, result app.Result) {
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

	mu.Lock()
	publishedResults := nonStatusPayloads(append([]map[string]any(nil), publishedMsgs...))
	offlineReasons = append([]string(nil), offlineReasons...)
	mu.Unlock()
	if len(publishedResults) != 2 {
		t.Fatalf("published result requests = %d, want 2", len(publishedResults))
	}
	if got := fmt.Sprint(publishedResults[0]["status"]); got != "error" {
		t.Fatalf("first publish status = %q, want error", got)
	}
	if got := fmt.Sprint(publishedResults[1]["request_id"]); got != "req-fail-failure-review" {
		t.Fatalf("follow-up request_id = %q, want req-fail-failure-review", got)
	}
	if got := fmt.Sprint(publishedResults[1]["config"]); !strings.Contains(got, config.DefaultRepositoryURL) {
		t.Fatalf("follow-up config = %q, want moltenhub-code repo", got)
	}
	if got := len(offlineReasons); got != 1 {
		t.Fatalf("offline requests = %d, want 1", got)
	}
	if got := offlineReasons[0]; got != transportOfflineReasonExecutionFailure {
		t.Fatalf("offline reason = %q, want %q", got, transportOfflineReasonExecutionFailure)
	}
}

func TestProcessInboundMessagePublishesAcquireFailurePayload(t *testing.T) {
	t.Parallel()

	var (
		mu             sync.Mutex
		publishedMsgs  []map[string]any
		offlineReasons []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"activities":["started"]}}}}`))
		case "/v1/agents/me/metadata", "/v1/agents/me/activities":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/a2a":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case "/v1/runtime/messages/publish":
			defer r.Body.Close()

			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode publish body: %v", err)
			}
			message, _ := body["message"].(map[string]any)

			mu.Lock()
			publishedMsgs = append(publishedMsgs, message)
			mu.Unlock()

			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
		case "/v1/runtime/messages/offline":
			defer r.Body.Close()

			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode offline body: %v", err)
			}
			mu.Lock()
			offlineReasons = append(offlineReasons, fmt.Sprint(body["reason"]))
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := NewDaemon(nil)
	failed := make(chan app.Result, 1)
	d.OnDispatchFailed = func(requestID string, failedRunCfg config.Config, result app.Result) {
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
	publishedResults := nonStatusPayloads(publishedMsgs)
	if len(publishedResults) != 2 {
		t.Fatalf("published result requests = %d, want 2", len(publishedResults))
	}
	if got := fmt.Sprint(publishedResults[0]["status"]); got != "error" {
		t.Fatalf("message.status = %v, want error", publishedResults[0]["status"])
	}
	if got := fmt.Sprint(publishedResults[0]["message"]); !strings.Contains(got, "Failure: task failed.\nError details: dispatch acquire: dispatch controller is closed") {
		t.Fatalf("message.message = %q", got)
	}
	if got := fmt.Sprint(publishedResults[0]["error"]); !strings.Contains(got, "dispatch acquire: dispatch controller is closed") {
		t.Fatalf("message.error = %q", got)
	}
	if got := fmt.Sprint(publishedResults[1]["request_id"]); got != "req-closed-controller-failure-review" {
		t.Fatalf("follow-up request_id = %q, want req-closed-controller-failure-review", got)
	}
	if got := fmt.Sprint(publishedResults[1]["config"]); !strings.Contains(got, config.DefaultRepositoryURL) {
		t.Fatalf("follow-up config = %q, want moltenhub-code repo", got)
	}
	if got := len(offlineReasons); got != 1 {
		t.Fatalf("offline requests = %d, want 1", got)
	}
	if got := offlineReasons[0]; got != transportOfflineReasonExecutionFailure {
		t.Fatalf("offline reason = %q, want %q", got, transportOfflineReasonExecutionFailure)
	}
}

func TestProcessInboundMessageInvokesOnDispatchFailedForAcquireFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"activities":["started"]}}}}`))
		case "/v1/agents/me/metadata", "/v1/agents/me/activities":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/a2a":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case "/v1/runtime/messages/publish":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
		case "/v1/runtime/messages/offline":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := NewDaemon(nil)
	failed := make(chan struct {
		requestID string
		runCfg    config.Config
		result    app.Result
	}, 1)
	d.OnDispatchFailed = func(requestID string, failedRunCfg config.Config, result app.Result) {
		failed <- struct {
			requestID string
			runCfg    config.Config
			result    app.Result
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
		mu                  sync.Mutex
		gotRequestID        string
		gotRepo             string
		gotPrompt           string
		registered          bool
		queuedAfterRegister bool
	)
	d.RegisterTaskControl = func(requestID string, _ context.CancelCauseFunc) DispatchTaskControl {
		mu.Lock()
		defer mu.Unlock()
		if requestID == "req-queued" {
			registered = true
		}
		return nil
	}
	d.OnDispatchQueued = func(requestID string, runCfg config.Config) {
		mu.Lock()
		defer mu.Unlock()
		gotRequestID = requestID
		gotRepo = runCfg.RepoURL
		gotPrompt = runCfg.Prompt
		queuedAfterRegister = registered
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
	if !queuedAfterRegister {
		t.Fatal("OnDispatchQueued ran before task controls were registered")
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
