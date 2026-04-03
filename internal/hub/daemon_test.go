package hub

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jef/how-i-want-to-code/internal/config"
	"github.com/jef/how-i-want-to-code/internal/harness"
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
			Name:         "codex_harness_run",
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
		"skill":      "codex_harness_run",
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
