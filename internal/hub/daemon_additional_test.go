package hub

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/library"
)

type stubMoltenHubAPI struct {
	token string

	pullFn   func(ctx context.Context, timeoutMs int) (PulledOpenClawMessage, bool, error)
	recordFn func(context.Context) error

	mu           sync.Mutex
	acked        []string
	nacked       []string
	published    []map[string]any
	offlineCalls []struct {
		SessionKey string
		Reason     string
	}
}

func (s *stubMoltenHubAPI) BaseURL() string { return "" }
func (s *stubMoltenHubAPI) Token() string   { return s.token }
func (s *stubMoltenHubAPI) ResolveAgentToken(context.Context, InitConfig) (string, error) {
	if strings.TrimSpace(s.token) == "" {
		return "", errors.New("missing token")
	}
	return s.token, nil
}
func (s *stubMoltenHubAPI) SyncProfile(context.Context, InitConfig) error   { return nil }
func (s *stubMoltenHubAPI) UpdateAgentStatus(context.Context, string) error { return nil }
func (s *stubMoltenHubAPI) MarkOpenClawOffline(_ context.Context, sessionKey, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offlineCalls = append(s.offlineCalls, struct {
		SessionKey string
		Reason     string
	}{SessionKey: sessionKey, Reason: reason})
	return nil
}
func (s *stubMoltenHubAPI) RecordGitHubTaskCompleteActivity(ctx context.Context) error {
	if s.recordFn != nil {
		return s.recordFn(ctx)
	}
	return nil
}
func (s *stubMoltenHubAPI) RegisterRuntime(context.Context, InitConfig, []library.TaskSummary) error {
	return nil
}
func (s *stubMoltenHubAPI) PublishResult(_ context.Context, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published = append(s.published, payload)
	return nil
}
func (s *stubMoltenHubAPI) PublishResultAsync(ctx context.Context, payload map[string]any) <-chan error {
	ch := make(chan error, 1)
	ch <- s.PublishResult(ctx, payload)
	close(ch)
	return ch
}
func (s *stubMoltenHubAPI) PullOpenClawMessage(ctx context.Context, timeoutMs int) (PulledOpenClawMessage, bool, error) {
	if s.pullFn == nil {
		return PulledOpenClawMessage{}, false, nil
	}
	return s.pullFn(ctx, timeoutMs)
}
func (s *stubMoltenHubAPI) AckOpenClawDelivery(_ context.Context, deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, deliveryID)
	return nil
}
func (s *stubMoltenHubAPI) AckOpenClawDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	ch := make(chan error, 1)
	ch <- s.AckOpenClawDelivery(ctx, deliveryID)
	close(ch)
	return ch
}
func (s *stubMoltenHubAPI) NackOpenClawDelivery(_ context.Context, deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nacked = append(s.nacked, deliveryID)
	return nil
}
func (s *stubMoltenHubAPI) NackOpenClawDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	ch := make(chan error, 1)
	ch <- s.NackOpenClawDelivery(ctx, deliveryID)
	close(ch)
	return ch
}

func TestRunPullLoopEarlyExitAndUnauthorizedError(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.runPullLoop(canceledCtx, &stubMoltenHubAPI{token: "t"}, InitConfig{}, nil, &sync.WaitGroup{}, nil, 1000); err != nil {
		t.Fatalf("runPullLoop(canceled) error = %v, want nil", err)
	}

	authAPI := &stubMoltenHubAPI{
		token: "t",
		pullFn: func(context.Context, int) (PulledOpenClawMessage, bool, error) {
			return PulledOpenClawMessage{}, false, errors.New("pull status=401")
		},
	}
	err := d.runPullLoop(context.Background(), authAPI, InitConfig{}, nil, &sync.WaitGroup{}, nil, 1000)
	if err == nil || !strings.Contains(err.Error(), "hub auth:") {
		t.Fatalf("runPullLoop(unauthorized) error = %v, want hub auth error", err)
	}
}

func TestProcessInboundMessageAcksIgnoredAndPublishesParseErrors(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	var workers sync.WaitGroup

	ignored := map[string]any{
		"type":  "skill_request",
		"skill": "other_skill",
	}
	d.processInboundMessage(context.Background(), api, cfg, ignored, "delivery-1", "message-1", nil, &workers, nil)

	api.mu.Lock()
	ackedAfterIgnored := append([]string(nil), api.acked...)
	api.mu.Unlock()
	if len(ackedAfterIgnored) != 1 || ackedAfterIgnored[0] != "delivery-1" {
		t.Fatalf("acked after ignored dispatch = %v, want [delivery-1]", ackedAfterIgnored)
	}

	invalid := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-invalid",
	}
	d.processInboundMessage(context.Background(), api, cfg, invalid, "delivery-2", "message-2", nil, &workers, nil)

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.published) == 0 {
		t.Fatal("published results is empty, want parse error result payload")
	}
	if len(api.acked) < 2 || api.acked[1] != "delivery-2" {
		t.Fatalf("acked deliveries = %v, want delivery-2 ack", api.acked)
	}
}

func TestProcessInboundMessageDuplicateDeliveryIsAckedWithoutDispatch(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	var workers sync.WaitGroup

	deduper := newDispatchDeduper(time.Hour)
	if ok, state := deduper.Begin("req-dup"); !ok || state != "accepted" {
		t.Fatalf("deduper.Begin(req-dup) = (%v, %q), want (true, accepted)", ok, state)
	}

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-dup",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix tests",
		},
	}
	d.processInboundMessage(context.Background(), api, cfg, msg, "delivery-dup", "message-dup", nil, &workers, deduper)

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.acked) != 1 || api.acked[0] != "delivery-dup" {
		t.Fatalf("acked deliveries = %v, want [delivery-dup]", api.acked)
	}
	if len(api.published) != 0 {
		t.Fatalf("published results = %d, want 0 for duplicate dispatch", len(api.published))
	}
}

func TestProcessInboundMessageDoesNotDedupeDistinctClientMsgIDWithSharedEnvelopeID(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("runner exploded")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	var workers sync.WaitGroup
	deduper := newDispatchDeduper(time.Hour)

	msgA := map[string]any{
		"type":          "skill_request",
		"skill":         "code_for_me",
		"id":            "sender-agent-static-id",
		"client_msg_id": "msg-a",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix tests A",
		},
	}
	msgB := map[string]any{
		"type":          "skill_request",
		"skill":         "code_for_me",
		"id":            "sender-agent-static-id",
		"client_msg_id": "msg-b",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix tests B",
		},
	}

	d.processInboundMessage(context.Background(), api, cfg, msgA, "", "", nil, &workers, deduper)
	d.processInboundMessage(context.Background(), api, cfg, msgB, "", "", nil, &workers, deduper)
	workers.Wait()

	api.mu.Lock()
	defer api.mu.Unlock()

	// Each failing dispatch publishes one failure result, one rerun request, and one follow-up task request.
	if got, want := len(api.published), 6; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	gotRequestIDs := map[string]bool{}
	for _, payload := range api.published {
		requestID, _ := payload["request_id"].(string)
		if requestID != "" {
			gotRequestIDs[requestID] = true
		}
	}
	for _, expected := range []string{
		"msg-a",
		"msg-a-rerun",
		"msg-a-failure-review",
		"msg-b",
		"msg-b-rerun",
		"msg-b-failure-review",
	} {
		if !gotRequestIDs[expected] {
			t.Fatalf("missing request_id %q in published payloads: %#v", expected, gotRequestIDs)
		}
	}
}

func TestHandleDispatchQueuesFailureFollowUpAfterPublishingFailureResult(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("runner exploded")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	runCfg := config.Config{
		Repo:         "git@github.com:acme/repo.git",
		BaseBranch:   "release",
		TargetSubdir: "internal/hub",
		Prompt:       "fix failing checks",
	}
	runCfg.ApplyDefaults()

	d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-follow-up",
			Skill:     "code_for_me",
			ReplyTo:   "agent-123",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()

	if got, want := len(api.published), 3; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}

	resultPayload := api.published[0]
	if got := resultPayload["type"]; got != "skill_result" {
		t.Fatalf("result payload type = %#v", got)
	}
	if got := resultPayload["reply_to"]; got != "agent-123" {
		t.Fatalf("result payload reply_to = %#v", got)
	}

	rerunPayload := api.published[1]
	if got := rerunPayload["type"]; got != "skill_request" {
		t.Fatalf("rerun payload type = %#v", got)
	}
	if got := rerunPayload["request_id"]; got != "req-follow-up-rerun" {
		t.Fatalf("rerun request_id = %#v", got)
	}
	if got := rerunPayload["rerun_of"]; got != "req-follow-up" {
		t.Fatalf("rerun rerun_of = %#v", got)
	}
	rerunConfig, _ := rerunPayload["config"].(map[string]any)
	if rerunConfig == nil {
		t.Fatalf("rerun config missing: %#v", rerunPayload)
	}
	if got := rerunConfig["baseBranch"]; got != "release" {
		t.Fatalf("rerun baseBranch = %#v, want release", got)
	}
	if got := rerunConfig["targetSubdir"]; got != "internal/hub" {
		t.Fatalf("rerun targetSubdir = %#v, want internal/hub", got)
	}

	followUpPayload := api.published[2]
	if got := followUpPayload["type"]; got != "skill_request" {
		t.Fatalf("follow-up payload type = %#v", got)
	}
	if got := followUpPayload["skill"]; got != "code_for_me" {
		t.Fatalf("follow-up payload skill = %#v", got)
	}
	if got := followUpPayload["request_id"]; got != "req-follow-up-failure-review" {
		t.Fatalf("follow-up request_id = %#v", got)
	}
	if _, hasReplyTo := followUpPayload["reply_to"]; hasReplyTo {
		t.Fatalf("follow-up payload unexpectedly routed to caller: %#v", followUpPayload["reply_to"])
	}

	runConfig, _ := followUpPayload["config"].(map[string]any)
	if runConfig == nil {
		t.Fatalf("follow-up config missing: %#v", followUpPayload)
	}
	if got := runConfig["baseBranch"]; got != "main" {
		t.Fatalf("follow-up baseBranch = %#v, want main", got)
	}
	if got := runConfig["targetSubdir"]; got != "." {
		t.Fatalf("follow-up targetSubdir = %#v, want .", got)
	}
	repos, _ := runConfig["repos"].([]string)
	if len(repos) != 1 || repos[0] != config.DefaultRepositoryURL {
		t.Fatalf("follow-up repos = %#v", runConfig["repos"])
	}
	prompt, _ := runConfig["prompt"].(string)
	if !strings.Contains(prompt, failureFollowUpPromptBase) {
		t.Fatalf("follow-up prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "Relevant failing log path(s):") {
		t.Fatalf("follow-up prompt missing log path heading: %q", prompt)
	}
	if !strings.Contains(prompt, failureFollowUpNoPathGuidance) {
		t.Fatalf("follow-up prompt missing empty-path guidance: %q", prompt)
	}
	if !strings.Contains(prompt, "Observed failure context:") {
		t.Fatalf("follow-up prompt missing failure context: %q", prompt)
	}
	if !strings.Contains(prompt, `- error="preflight: runner exploded"`) {
		t.Fatalf("follow-up prompt missing error details: %q", prompt)
	}
	if !strings.Contains(prompt, `- request_id=req-follow-up`) {
		t.Fatalf("follow-up prompt missing request id: %q", prompt)
	}
	if !strings.Contains(prompt, `- target_subdir=internal/hub`) {
		t.Fatalf("follow-up prompt missing target subdir: %q", prompt)
	}
	if !strings.Contains(prompt, "Original task prompt:\nfix failing checks") {
		t.Fatalf("follow-up prompt missing original prompt: %q", prompt)
	}
	if !strings.Contains(prompt, `Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.`) {
		t.Fatalf("follow-up prompt missing offline review instruction: %q", prompt)
	}
	if !strings.Contains(prompt, "Do not stop work just because you cannot create a pull request or watch remote CI/CD from inside this agent runtime.") {
		t.Fatalf("follow-up prompt missing remote operations handoff: %q", prompt)
	}
	if !strings.Contains(prompt, "If no file changes are required, return a clear no-op result with concrete evidence instead of forcing an empty PR.") {
		t.Fatalf("follow-up prompt missing no-op completion carve-out: %q", prompt)
	}
	if got, want := len(api.offlineCalls), 1; got != want {
		t.Fatalf("offline call count = %d, want %d", got, want)
	}
	if got := api.offlineCalls[0].Reason; got != transportOfflineReasonExecutionFailure {
		t.Fatalf("offline reason = %q, want %q", got, transportOfflineReasonExecutionFailure)
	}
}

func TestHandleDispatchQueuesFailureFollowUpWithTaskLogPaths(t *testing.T) {
	t.Parallel()

	logRoot := filepath.Join("/tmp", ".log")
	d := NewDaemon(failingRunner{err: errors.New("runner exploded")})
	d.TaskLogRoot = logRoot
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	runCfg := config.Config{
		Repo:       "git@github.com:acme/repo.git",
		BaseBranch: "release",
		Prompt:     "fix failing checks",
	}
	runCfg.ApplyDefaults()

	d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-follow-up-logs",
			Skill:     "code_for_me",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()

	if got, want := len(api.published), 3; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}

	followUpPayload := api.published[2]
	runConfig, _ := followUpPayload["config"].(map[string]any)
	if runConfig == nil {
		t.Fatalf("follow-up config missing: %#v", followUpPayload)
	}
	prompt, _ := runConfig["prompt"].(string)
	expectedLogDir := filepath.Join(logRoot, "req", "follow", "up", "logs")
	for _, path := range []string{
		expectedLogDir,
		filepath.Join(expectedLogDir, "term"),
		filepath.Join(expectedLogDir, "terminal.log"),
		filepath.Join(logRoot, "main", "term"),
		filepath.Join(logRoot, "main", "terminal.log"),
	} {
		if !strings.Contains(prompt, path) {
			t.Fatalf("follow-up prompt missing task log path %q: %q", path, prompt)
		}
	}
	if strings.Contains(prompt, filepath.Join(logRoot, "terminal.log")) {
		t.Fatalf("follow-up prompt should exclude aggregate terminal log path to avoid recursive reads: %q", prompt)
	}
	if strings.Contains(prompt, failureFollowUpNoPathGuidance) {
		t.Fatalf("follow-up prompt should prefer concrete task log paths when available: %q", prompt)
	}
}

func TestQueueFailureFollowUpUsesDefaultRepository(t *testing.T) {
	t.Parallel()

	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-multi",
		Skill:     "code_for_me",
		Config: config.Config{
			Repos: []string{
				"git@github.com:acme/repo-a.git",
				"git@github.com:acme/repo-b.git",
			},
		},
	}
	res := harness.Result{
		Err: errors.New("task failed"),
		RepoResults: []harness.RepoResult{
			{RepoURL: "git@github.com:acme/repo-b.git"},
			{RepoURL: "git@github.com:acme/repo-b.git"},
		},
	}

	if err := queueFailureFollowUp(context.Background(), api, cfg, dispatch, res, ""); err != nil {
		t.Fatalf("queueFailureFollowUp() error = %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()

	if len(api.published) != 1 {
		t.Fatalf("published payload count = %d, want 1", len(api.published))
	}

	runConfig, _ := api.published[0]["config"].(map[string]any)
	if runConfig == nil {
		t.Fatalf("follow-up config missing: %#v", api.published[0])
	}
	repos, _ := runConfig["repos"].([]string)
	if len(repos) != 1 || repos[0] != config.DefaultRepositoryURL {
		t.Fatalf("follow-up repos = %#v", runConfig["repos"])
	}
}

func TestHandleDispatchQueuesFailureFollowUpForNoDeltaFailures(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("task failed: this branch has no delta from `main`; No commits between main and moltenhub-fix")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	runCfg := config.Config{
		Repo:   "git@github.com:acme/repo.git",
		Prompt: "fix failing checks",
	}
	runCfg.ApplyDefaults()

	d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-no-delta",
			Skill:     "code_for_me",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()

	if got, want := len(api.published), 3; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	if got := api.published[0]["status"]; got != "error" {
		t.Fatalf("result payload status = %#v, want error", got)
	}
	if got := api.published[2]["request_id"]; got != "req-no-delta-failure-review" {
		t.Fatalf("follow-up request_id = %#v, want req-no-delta-failure-review", got)
	}
}

func TestShouldQueueFailureFollowUpSkipsNestedFailureReviewRequests(t *testing.T) {
	t.Parallel()

	dispatch := SkillDispatch{RequestID: "req-123-failure-review"}
	ok, reason := shouldQueueFailureFollowUp(dispatch, harness.Result{Err: errors.New("still failing")})
	if ok || reason != "run is already a failure follow-up" {
		t.Fatalf("shouldQueueFailureFollowUp() = (%v, %q), want (false, %q)", ok, reason, "run is already a failure follow-up")
	}
}

func TestShouldQueueFailureRerunSkipsNestedRerunRequests(t *testing.T) {
	t.Parallel()

	dispatch := SkillDispatch{RequestID: "req-123-rerun"}
	ok, reason := shouldQueueFailureRerun(dispatch, harness.Result{Err: errors.New("still failing")})
	if ok || reason != "run is already a failure rerun" {
		t.Fatalf("shouldQueueFailureRerun() = (%v, %q), want (false, %q)", ok, reason, "run is already a failure rerun")
	}
}

func TestShouldQueueFailureFollowUpQueuesRepoAccessFailures(t *testing.T) {
	t.Parallel()

	dispatch := SkillDispatch{RequestID: "req-123"}
	ok, reason := shouldQueueFailureFollowUp(dispatch, harness.Result{
		Err: errors.New("git: verify remote write access for repo https://github.com/acme/repo.git branch \"moltenhub-fix\": exit status 128: remote: Write access to repository not granted. fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403"),
	})
	if !ok || reason != "" {
		t.Fatalf("shouldQueueFailureFollowUp(repo access) = (%v, %q), want (true, \"\")", ok, reason)
	}

	ok, reason = shouldQueueFailureFollowUp(dispatch, harness.Result{
		Err: errors.New("git: run git [push -u origin moltenhub-branch]: exit status 1: remote: refusing to allow an OAuth App to create or update workflow `.github/workflows/docker-release.yml` without `workflow` scope"),
	})
	if !ok || reason != "" {
		t.Fatalf("shouldQueueFailureFollowUp(workflow scope) = (%v, %q), want (true, \"\")", ok, reason)
	}
}

func TestFailureFollowUpPromptIncludesWorkspaceAndTargetPath(t *testing.T) {
	t.Parallel()

	runCfg := config.Config{
		Repo:         "git@github.com:acme/repo.git",
		BaseBranch:   "main",
		TargetSubdir: "internal/hub",
		Prompt:       "fix failing checks",
	}
	runCfg.ApplyDefaults()

	result := harness.Result{
		WorkspaceDir: "/tmp/run-123",
		RepoResults: []harness.RepoResult{{
			RepoURL: "git@github.com:acme/repo.git",
			RepoDir: "/tmp/run-123/repo",
		}},
	}

	prompt := failureFollowUpPrompt("", SkillDispatch{
		RequestID: "req-123",
		Config:    runCfg,
	}, result)
	if !strings.Contains(prompt, "/tmp/run-123") {
		t.Fatalf("prompt missing workspace dir: %q", prompt)
	}
	if !strings.Contains(prompt, "/tmp/run-123/repo") {
		t.Fatalf("prompt missing repo dir: %q", prompt)
	}
	if !strings.Contains(prompt, "/tmp/run-123/repo/internal/hub") {
		t.Fatalf("prompt missing repo target dir: %q", prompt)
	}
	if !strings.Contains(prompt, "Observed failure context:") {
		t.Fatalf("prompt missing failure context: %q", prompt)
	}
	if !strings.Contains(prompt, "- repos=git@github.com:acme/repo.git") {
		t.Fatalf("prompt missing repo context: %q", prompt)
	}
	if !strings.Contains(prompt, `When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.`) {
		t.Fatalf("prompt missing response contract: %q", prompt)
	}
}

func TestRecordGitHubTaskCompleteActivityLogsWarningOnFailure(t *testing.T) {
	t.Parallel()

	var logs []string
	d := NewDaemon(nil)
	d.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	api := &stubMoltenHubAPI{
		token: "t",
		recordFn: func(context.Context) error {
			return errors.New("metadata rejected")
		},
	}

	d.recordGitHubTaskCompleteActivity(context.Background(), api, "req-17")

	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}
	if !strings.Contains(logs[0], "action=record_github_task_complete") {
		t.Fatalf("log = %q", logs[0])
	}
	if !strings.Contains(logs[0], "req-17") {
		t.Fatalf("log missing request id: %q", logs[0])
	}
}

func TestRunWebsocketLoopReadsMessageThenReturnsOnDisconnect(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		req, readErr := http.ReadRequest(reader)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		key := strings.TrimSpace(req.Header.Get("Sec-WebSocket-Key"))
		if _, writeErr := fmt.Fprintf(
			conn,
			"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			websocketAccept(key),
		); writeErr != nil {
			serverDone <- writeErr
			return
		}
		if writeErr := writeFrameToConn(conn, true, opcodeText, []byte(`{"type":"noop","skill":"other_skill"}`), false); writeErr != nil {
			serverDone <- writeErr
			return
		}
		serverDone <- nil
	}()

	d := NewDaemon(nil)
	api := &stubMoltenHubAPI{token: "token"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	var workers sync.WaitGroup

	wsURL := "ws://" + listener.Addr().String() + "/openclaw/messages/ws"
	err = d.runWebsocketLoop(context.Background(), wsURL, api, cfg, nil, &workers, nil)
	if err == nil {
		t.Fatal("runWebsocketLoop() error = nil, want disconnect error")
	}

	if serverErr := <-serverDone; serverErr != nil {
		t.Fatalf("websocket server error = %v", serverErr)
	}
}
