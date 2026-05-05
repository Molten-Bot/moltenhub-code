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

	"github.com/Molten-Bot/moltenhub-code/internal/app"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
	"github.com/a2aproject/a2a-go/v2/a2a"
)

type stubMoltenHubAPI struct {
	token string

	pullFn           func(ctx context.Context, timeoutMs int) (PulledRuntimeMessage, bool, error)
	recordFn         func(context.Context) error
	recordCodingFn   func(context.Context) error
	recordActivityFn func(context.Context, string) error

	mu           sync.Mutex
	acked        []string
	nacked       []string
	published    []map[string]any
	activities   []string
	codingEvents int
	offlineCalls []struct {
		SessionKey string
		Reason     string
	}
}

type blockingAsyncStatusAPI struct {
	stubMoltenHubAPI
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAsyncStatusAPI) PublishResultAsync(ctx context.Context, payload map[string]any) <-chan error {
	done := make(chan error, 1)
	s.once.Do(func() {
		close(s.started)
	})
	go func() {
		<-s.release
		done <- s.stubMoltenHubAPI.PublishResult(ctx, payload)
		close(done)
	}()
	return done
}

type blockedRunner struct {
	started chan struct{}
	unblock chan struct{}
	err     error
	once    sync.Once
}

func (r *blockedRunner) Run(ctx context.Context, _ execx.Command) (execx.Result, error) {
	if r.started != nil {
		r.once.Do(func() {
			close(r.started)
		})
	}
	if r.unblock != nil {
		select {
		case <-r.unblock:
		case <-ctx.Done():
			return execx.Result{}, ctx.Err()
		}
	}
	if r.err == nil {
		r.err = errors.New("runner failed")
	}
	return execx.Result{}, r.err
}

type stubDispatchTaskControl struct {
	waitErr error
	stopped bool
	paused  bool
	forced  bool
}

func (s *stubDispatchTaskControl) WaitUntilRunnable(context.Context) error { return s.waitErr }
func (s *stubDispatchTaskControl) SetAcquireCancel(context.CancelFunc)     {}
func (s *stubDispatchTaskControl) ClearAcquireCancel(context.CancelFunc)   {}
func (s *stubDispatchTaskControl) SetRunning(bool)                         {}
func (s *stubDispatchTaskControl) ConsumeForceAcquire() bool               { return s.forced }
func (s *stubDispatchTaskControl) HasForceAcquire() bool                   { return s.forced }
func (s *stubDispatchTaskControl) IsPaused() bool                          { return s.paused }
func (s *stubDispatchTaskControl) IsStopped() bool                         { return s.stopped }

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
func (s *stubMoltenHubAPI) MarkRuntimeOffline(_ context.Context, sessionKey, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offlineCalls = append(s.offlineCalls, struct {
		SessionKey string
		Reason     string
	}{SessionKey: sessionKey, Reason: reason})
	return nil
}
func (s *stubMoltenHubAPI) RecordActivity(ctx context.Context, activity string) error {
	if s.recordActivityFn != nil {
		return s.recordActivityFn(ctx, activity)
	}
	s.mu.Lock()
	s.activities = append(s.activities, normalizeActivityEntry(activity))
	s.mu.Unlock()
	return nil
}
func (s *stubMoltenHubAPI) RecordCodingActivityRunning(ctx context.Context) error {
	if s.recordCodingFn != nil {
		return s.recordCodingFn(ctx)
	}
	s.mu.Lock()
	s.codingEvents++
	s.mu.Unlock()
	return nil
}
func (s *stubMoltenHubAPI) RecordGitHubTaskCompleteActivity(ctx context.Context) error {
	if s.recordFn != nil {
		return s.recordFn(ctx)
	}
	return nil
}
func (s *stubMoltenHubAPI) RecordRunStartedActivity(ctx context.Context, runCfg config.Config) error {
	if activity := RunStartedActivity(runCfg); activity != "" {
		return s.RecordActivity(ctx, activity)
	}
	return s.RecordCodingActivityRunning(ctx)
}

func (s *stubMoltenHubAPI) RecordRunCompletedActivity(ctx context.Context, runCfg config.Config) error {
	if activity := RunCompletedActivity(runCfg); activity != "" {
		return s.RecordActivity(ctx, activity)
	}
	return s.RecordGitHubTaskCompleteActivity(ctx)
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
func (s *stubMoltenHubAPI) PullRuntimeMessage(ctx context.Context, timeoutMs int) (PulledRuntimeMessage, bool, error) {
	if s.pullFn == nil {
		return PulledRuntimeMessage{}, false, nil
	}
	return s.pullFn(ctx, timeoutMs)
}
func (s *stubMoltenHubAPI) AckRuntimeDelivery(_ context.Context, deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, deliveryID)
	return nil
}
func (s *stubMoltenHubAPI) AckRuntimeDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	ch := make(chan error, 1)
	ch <- s.AckRuntimeDelivery(ctx, deliveryID)
	close(ch)
	return ch
}
func (s *stubMoltenHubAPI) NackRuntimeDelivery(_ context.Context, deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nacked = append(s.nacked, deliveryID)
	return nil
}
func (s *stubMoltenHubAPI) NackRuntimeDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	ch := make(chan error, 1)
	ch <- s.NackRuntimeDelivery(ctx, deliveryID)
	close(ch)
	return ch
}

func TestQueueFailureRerunPublishesNormalizedRunConfig(t *testing.T) {
	t.Parallel()

	api := &stubMoltenHubAPI{}
	dispatch := SkillDispatch{
		Skill:     "code_for_me",
		RequestID: "req-1",
		RouteTo:   "agent-next",
		ReplyTo:   "agent-source",
		Config: config.Config{
			Repo:   "git@github.com:acme/repo.git",
			Prompt: "fix failing tests",
		},
	}

	if err := queueFailureRerun(context.Background(), api, InitConfig{}, dispatch); err != nil {
		t.Fatalf("queueFailureRerun() error = %v", err)
	}
	if len(api.published) != 1 {
		t.Fatalf("published payload count = %d, want 1", len(api.published))
	}
	payload := api.published[0]
	if got, want := payload["request_id"], "req-1-rerun"; got != want {
		t.Fatalf("request_id = %#v, want %q", got, want)
	}
	if got, want := payload["to"], "agent-next"; got != want {
		t.Fatalf("to = %#v, want %q", got, want)
	}
	configPayload, ok := payload["config"].(map[string]any)
	if !ok {
		t.Fatalf("config payload = %#v, want map", payload["config"])
	}
	if got, want := configPayload["repo"], "git@github.com:acme/repo.git"; got != want {
		t.Fatalf("config.repo = %#v, want %q", got, want)
	}
	if got, want := configPayload["prompt"], "fix failing tests"; got != want {
		t.Fatalf("config.prompt = %#v, want %q", got, want)
	}
}

func TestPublishDispatchStatusReturnsBeforeAsyncPublishCompletes(t *testing.T) {
	t.Parallel()

	api := &blockingAsyncStatusAPI{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	daemon := NewDaemon(nil)

	returned := make(chan struct{})
	go func() {
		daemon.publishDispatchStatus(
			context.Background(),
			api,
			InitConfig{Skill: SkillConfig{Name: "code_for_me"}},
			SkillDispatch{RequestID: "req-status-async", Skill: "code_for_me"},
			"working",
			a2a.TaskStateWorking,
			"Task running.",
			nil,
		)
		close(returned)
	}()

	select {
	case <-api.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("PublishResultAsync was not called")
	}
	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("publishDispatchStatus blocked on async publish")
	}

	close(api.release)
	deadline := time.After(2 * time.Second)
	for {
		api.mu.Lock()
		published := len(api.published)
		api.mu.Unlock()
		if published == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("published payload count = %d, want 1", published)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDispatchRunConfigPayloadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	_, err := dispatchRunConfigPayload(config.Config{Repo: "git@github.com:acme/repo.git"})
	if err == nil || !strings.Contains(err.Error(), "normalize run config payload") {
		t.Fatalf("dispatchRunConfigPayload(invalid) error = %v, want normalization failure", err)
	}
}

func nonStatusPayloads(payloads []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(payloads))
	for _, payload := range payloads {
		if payload["type"] == dispatchTaskStatusType {
			continue
		}
		out = append(out, payload)
	}
	return out
}

func statusPayloads(payloads []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(payloads))
	for _, payload := range payloads {
		if payload["type"] == dispatchTaskStatusType {
			out = append(out, payload)
		}
	}
	return out
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
		pullFn: func(context.Context, int) (PulledRuntimeMessage, bool, error) {
			return PulledRuntimeMessage{}, false, errors.New("pull status=401")
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

func TestProcessInboundMessageIgnoresHubAcknowledgements(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("runner should not be called")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	var workers sync.WaitGroup

	ack := map[string]any{
		"type":  "acknowledgement",
		"skill": "code_for_me",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "do not run",
		},
	}
	d.processInboundMessage(context.Background(), api, cfg, ack, "delivery-ack", "message-ack", nil, &workers, nil)
	workers.Wait()

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.acked) != 1 || api.acked[0] != "delivery-ack" {
		t.Fatalf("acked deliveries = %v, want [delivery-ack]", api.acked)
	}
	if len(api.published) != 0 {
		t.Fatalf("published payload count = %d, want 0 for acknowledgement", len(api.published))
	}
	if len(api.activities) != 0 || api.codingEvents != 0 {
		t.Fatalf("activity events = activities:%v coding:%d, want none", api.activities, api.codingEvents)
	}
}

func TestProcessInboundMessageIgnoresA2AResponseEchoes(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("runner should not be called")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}
	var workers sync.WaitGroup

	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      "send-ack-1",
		"result": map[string]any{
			"task": map[string]any{
				"id": "task-echo",
				"status": map[string]any{
					"state": "submitted",
				},
				"history": []any{
					map[string]any{
						"messageId": "original-request",
						"role":      "user",
						"parts": []any{
							map[string]any{
								"data": map[string]any{
									"repo":   "git@github.com:acme/repo.git",
									"prompt": "do not run echoed task",
								},
							},
						},
					},
				},
			},
		},
	}
	d.processInboundMessage(context.Background(), api, cfg, response, "delivery-response", "message-response", nil, &workers, nil)
	workers.Wait()

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.acked) != 1 || api.acked[0] != "delivery-response" {
		t.Fatalf("acked deliveries = %v, want [delivery-response]", api.acked)
	}
	if len(api.published) != 0 {
		t.Fatalf("published payload count = %d, want 0 for A2A response echo", len(api.published))
	}
	if len(api.activities) != 0 || api.codingEvents != 0 {
		t.Fatalf("activity events = activities:%v coding:%d, want none", api.activities, api.codingEvents)
	}
}

func TestProcessInboundMessageDuplicateDeliveryPublishesFailureToCallerAndAcks(t *testing.T) {
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

	dupCfg := config.Config{
		Repo:   "git@github.com:acme/repo.git",
		Prompt: "fix tests",
	}
	dupCfg.ApplyDefaults()
	dupKey := dedupeKeyForRunConfig(dupCfg)
	if strings.TrimSpace(dupKey) == "" {
		t.Fatal("dedupe key is empty, want non-empty")
	}
	deduper := newDispatchDeduper(time.Hour)
	if ok, state, duplicateOf := deduper.Begin(dupKey, "req-dup"); !ok || state != "accepted" || duplicateOf != "" {
		t.Fatalf("deduper.Begin(dupKey) = (%v, %q, %q), want (true, accepted, empty)", ok, state, duplicateOf)
	}

	msg := map[string]any{
		"type":            "skill_request",
		"skill":           "code_for_me",
		"request_id":      "req-dup",
		"from_agent_uuid": "caller-agent-uuid",
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
	statusUpdates := statusPayloads(api.published)
	if len(statusUpdates) != 1 {
		t.Fatalf("published status updates = %d, want 1 for duplicate dispatch", len(statusUpdates))
	}
	if got := statusUpdates[0]["status"]; got != "duplicate" {
		t.Fatalf("status update status = %#v, want duplicate", got)
	}
	if got := statusUpdates[0]["a2a_state"]; got != "TASK_STATE_REJECTED" {
		t.Fatalf("status update a2a_state = %#v, want rejected", got)
	}
	resultPayloads := nonStatusPayloads(api.published)
	if len(resultPayloads) != 1 {
		t.Fatalf("published results = %d, want 1 for duplicate dispatch", len(resultPayloads))
	}
	resultPayload := resultPayloads[0]
	if got := resultPayload["status"]; got != "duplicate" {
		t.Fatalf("result status = %#v, want duplicate", got)
	}
	if got := resultPayload["failed"]; got != true {
		t.Fatalf("result failed = %#v, want true", got)
	}
	if got := resultPayload["reply_to"]; got != "caller-agent-uuid" {
		t.Fatalf("result reply_to = %#v, want caller-agent-uuid", got)
	}
	if got := resultPayload["duplicate"]; got != true {
		t.Fatalf("result duplicate = %#v, want true", got)
	}
	if got := resultPayload["state"]; got != "in_flight" {
		t.Fatalf("result state = %#v, want in_flight", got)
	}
	if got := resultPayload["duplicate_of"]; got != "req-dup" {
		t.Fatalf("result duplicate_of = %#v, want req-dup", got)
	}
	if got := fmt.Sprint(resultPayload["message"]); !strings.Contains(got, "Failure: task failed. Error details: duplicate submission ignored (request_id=req-dup state=in_flight)") {
		t.Fatalf("result message = %q", got)
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

	// Each failing dispatch publishes one failure result and one failure review request.
	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 4; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	gotRequestIDs := map[string]bool{}
	for _, payload := range publishedResults {
		requestID, _ := payload["request_id"].(string)
		if requestID != "" {
			gotRequestIDs[requestID] = true
		}
	}
	for _, expected := range []string{
		"msg-a",
		"msg-a-failure-review",
		"msg-b",
		"msg-b-failure-review",
	} {
		if !gotRequestIDs[expected] {
			t.Fatalf("missing request_id %q in published payloads: %#v", expected, gotRequestIDs)
		}
	}
}

func TestProcessInboundMessageDedupesIdenticalConfigAcrossRequestIDs(t *testing.T) {
	t.Parallel()

	runner := &blockedRunner{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
		err:     errors.New("runner exploded"),
	}
	d := NewDaemon(runner)
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
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-a",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix same task",
		},
	}
	msgB := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-b",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "fix same task",
		},
	}

	d.processInboundMessage(context.Background(), api, cfg, msgA, "", "", nil, &workers, deduper)
	<-runner.started
	d.processInboundMessage(context.Background(), api, cfg, msgB, "", "", nil, &workers, deduper)
	close(runner.unblock)
	workers.Wait()

	api.mu.Lock()
	defer api.mu.Unlock()
	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 3; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	gotRequestIDs := map[string]map[string]any{}
	for _, payload := range publishedResults {
		requestID, _ := payload["request_id"].(string)
		if requestID != "" {
			gotRequestIDs[requestID] = payload
		}
	}
	for _, expected := range []string{"req-a", "req-a-failure-review"} {
		if gotRequestIDs[expected] == nil {
			t.Fatalf("missing request_id %q in published payloads: %#v", expected, gotRequestIDs)
		}
	}
	duplicatePayload := gotRequestIDs["req-b"]
	if duplicatePayload == nil {
		t.Fatalf("missing duplicate payload for req-b: %#v", gotRequestIDs)
	}
	if got := duplicatePayload["status"]; got != "duplicate" {
		t.Fatalf("duplicate payload status = %#v, want duplicate", got)
	}
	if got := duplicatePayload["duplicate_of"]; got != "req-a" {
		t.Fatalf("duplicate payload duplicate_of = %#v, want req-a", got)
	}
	if gotRequestIDs["req-b-rerun"] != nil || gotRequestIDs["req-b-failure-review"] != nil {
		t.Fatalf("duplicate request unexpectedly executed: %#v", gotRequestIDs)
	}
}

func TestProcessInboundMessagePublishesStoppedFailureWhenTaskControlStopsDispatch(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{MaxParallel: 1},
	}

	var (
		mu                sync.Mutex
		registeredID      string
		completedID       string
		registerCancelSet bool
	)
	d.RegisterTaskControl = func(requestID string, cancel context.CancelCauseFunc) DispatchTaskControl {
		mu.Lock()
		registeredID = requestID
		registerCancelSet = cancel != nil
		mu.Unlock()
		return &stubDispatchTaskControl{
			waitErr: errors.New("task was stopped by operator"),
			stopped: true,
		}
	}
	d.CompleteTaskControl = func(requestID string) {
		mu.Lock()
		completedID = requestID
		mu.Unlock()
	}

	msg := map[string]any{
		"type":       "skill_request",
		"skill":      "code_for_me",
		"request_id": "req-stop-control",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "stop this task",
		},
	}

	var workers sync.WaitGroup
	d.processInboundMessage(context.Background(), api, cfg, msg, "", "", nil, &workers, nil)
	workers.Wait()

	mu.Lock()
	gotRegisteredID := registeredID
	gotCompletedID := completedID
	gotCancelSet := registerCancelSet
	mu.Unlock()

	if gotRegisteredID != "req-stop-control" {
		t.Fatalf("RegisterTaskControl requestID = %q, want %q", gotRegisteredID, "req-stop-control")
	}
	if !gotCancelSet {
		t.Fatal("RegisterTaskControl cancel = nil, want non-nil")
	}
	if gotCompletedID != "req-stop-control" {
		t.Fatalf("CompleteTaskControl requestID = %q, want %q", gotCompletedID, "req-stop-control")
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 1; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	if got, want := fmt.Sprint(publishedResults[0]["status"]), "error"; got != want {
		t.Fatalf("result status = %q, want %q", got, want)
	}
	if got := fmt.Sprint(publishedResults[0]["message"]); !strings.Contains(got, "Failure: task failed. Error details: task was stopped by operator") {
		t.Fatalf("result message = %q", got)
	}
	if got := fmt.Sprint(publishedResults[0]["error"]); got != "task was stopped by operator" {
		t.Fatalf("result error = %q, want %q", got, "task was stopped by operator")
	}
	if got := len(api.offlineCalls); got != 0 {
		t.Fatalf("offline calls = %d, want 0 for operator stop", got)
	}
}

func TestProcessInboundMessageFallsBackToDeliveryIDForTaskControlRequestID(t *testing.T) {
	t.Parallel()

	d := NewDaemon(nil)
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{MaxParallel: 1},
	}

	var (
		mu           sync.Mutex
		registeredID string
		completedID  string
	)
	d.RegisterTaskControl = func(requestID string, _ context.CancelCauseFunc) DispatchTaskControl {
		mu.Lock()
		registeredID = requestID
		mu.Unlock()
		return &stubDispatchTaskControl{
			waitErr: errors.New("task was stopped by operator"),
			stopped: true,
		}
	}
	d.CompleteTaskControl = func(requestID string) {
		mu.Lock()
		completedID = requestID
		mu.Unlock()
	}

	msg := map[string]any{
		"type":  "skill_request",
		"skill": "code_for_me",
		"config": map[string]any{
			"repo":   "git@github.com:acme/repo.git",
			"prompt": "stop this task",
		},
	}

	var workers sync.WaitGroup
	d.processInboundMessage(context.Background(), api, cfg, msg, "delivery-fallback", "", nil, &workers, nil)
	workers.Wait()

	mu.Lock()
	gotRegisteredID := registeredID
	gotCompletedID := completedID
	mu.Unlock()

	if gotRegisteredID != "delivery-fallback" {
		t.Fatalf("RegisterTaskControl requestID = %q, want %q", gotRegisteredID, "delivery-fallback")
	}
	if gotCompletedID != "delivery-fallback" {
		t.Fatalf("CompleteTaskControl requestID = %q, want %q", gotCompletedID, "delivery-fallback")
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 1; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	if got := fmt.Sprint(publishedResults[0]["request_id"]); got != "delivery-fallback" {
		t.Fatalf("result request_id = %q, want %q", got, "delivery-fallback")
	}
	if got := fmt.Sprint(publishedResults[0]["message"]); !strings.Contains(got, "Failure: task failed. Error details: task was stopped by operator") {
		t.Fatalf("result message = %q", got)
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
			RouteTo:   "worker-agent-uuid",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()

	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 2; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}

	resultPayload := publishedResults[0]
	if got := resultPayload["type"]; got != "skill_result" {
		t.Fatalf("result payload type = %#v", got)
	}
	if got := resultPayload["reply_to"]; got != "agent-123" {
		t.Fatalf("result payload reply_to = %#v", got)
	}

	followUpPayload := publishedResults[1]
	if got := followUpPayload["type"]; got != "skill_request" {
		t.Fatalf("follow-up payload type = %#v", got)
	}
	if got := followUpPayload["request_id"]; got != "req-follow-up-failure-review" {
		t.Fatalf("follow-up request_id = %#v", got)
	}
	if got := followUpPayload["to"]; got != "worker-agent-uuid" {
		t.Fatalf("follow-up to = %#v, want worker-agent-uuid", got)
	}
	if got := followUpPayload["reply_to"]; got != "agent-123" {
		t.Fatalf("follow-up reply_to = %#v, want agent-123", got)
	}
	followUpConfig, _ := followUpPayload["config"].(map[string]any)
	if followUpConfig == nil {
		t.Fatalf("follow-up config missing: %#v", followUpPayload)
	}
	repos, _ := followUpConfig["repos"].([]string)
	if len(repos) != 1 || repos[0] != config.DefaultRepositoryURL {
		t.Fatalf("follow-up repos = %#v", followUpConfig["repos"])
	}
	if got := followUpConfig["targetSubdir"]; got != "." {
		t.Fatalf("follow-up targetSubdir = %#v, want .", got)
	}
	if got := followUpConfig["responseMode"]; got != nil {
		t.Fatalf("follow-up responseMode = %#v, want omitted", got)
	}

	if got, want := len(api.offlineCalls), 1; got != want {
		t.Fatalf("offline call count = %d, want %d", got, want)
	}
	if got := api.offlineCalls[0].Reason; got != transportOfflineReasonExecutionFailure {
		t.Fatalf("offline reason = %q, want %q", got, transportOfflineReasonExecutionFailure)
	}
	if got := api.codingEvents; got != 1 {
		t.Fatalf("coding activity events = %d, want 1", got)
	}
}

func TestHandleDispatchPublishesHarnessStageStatusUpdates(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("preflight exploded")})
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
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       "fix preflight",
	}
	runCfg.ApplyDefaults()

	d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-stage-updates",
			Skill:     "code_for_me",
			ReplyTo:   "caller-agent",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()
	statusUpdates := statusPayloads(api.published)
	if len(statusUpdates) < 3 {
		t.Fatalf("status update count = %d, want at least 3", len(statusUpdates))
	}

	var stageStart, stageError map[string]any
	for _, payload := range statusUpdates {
		details, _ := payload["details"].(map[string]any)
		if details == nil || details["stage"] != "preflight" {
			continue
		}
		switch details["stage_status"] {
		case "start":
			stageStart = payload
		case "error":
			stageError = payload
		}
	}
	if stageStart == nil {
		t.Fatalf("preflight start status update missing: %#v", statusUpdates)
	}
	if got := stageStart["to"]; got != "caller-agent" {
		t.Fatalf("preflight start to = %#v, want caller-agent", got)
	}
	if stageError == nil {
		t.Fatalf("preflight error status update missing: %#v", statusUpdates)
	}
	if got := stageError["a2a_state"]; got != "TASK_STATE_FAILED" {
		t.Fatalf("preflight error a2a_state = %#v, want TASK_STATE_FAILED", got)
	}
	if got := fmt.Sprint(stageError["message"]); !strings.Contains(got, "Failure: task failed. Error details:") {
		t.Fatalf("preflight error message = %q", got)
	}
	if got := fmt.Sprint(stageError["Error details"]); !strings.Contains(got, "preflight exploded") {
		t.Fatalf("preflight error details = %q, want runner error", got)
	}
}

func TestHandleDispatchRejectsRunConfigAgentHarnessOverride(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("runner should not be called")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		AgentHarness: "claude",
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	runCfg := config.Config{
		Repo:         "git@github.com:acme/repo.git",
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       "fix failing checks",
		AgentHarness: "codex",
	}
	runCfg.ApplyDefaults()

	finalState := d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-bound-runtime-override",
			Skill:     "code_for_me",
			Config:    runCfg,
		},
		"",
		false,
	)
	if finalState != "error" {
		t.Fatalf("handleDispatch() final state = %q, want error", finalState)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 1; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	message := fmt.Sprint(publishedResults[0]["message"])
	if !strings.Contains(message, "conflicts with bound agent") {
		t.Fatalf("result message = %q, want bound-runtime conflict details", message)
	}
}

func TestHandleDispatchLibraryTaskUsesDedicatedActivitySignal(t *testing.T) {
	t.Parallel()

	d := NewDaemon(failingRunner{err: errors.New("runner exploded")})
	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		RuntimeConfigPath: filepath.Join(t.TempDir(), "runtime.json"),
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
	}

	runCfg := config.Config{
		Repo:                   "git@github.com:acme/repo.git",
		BaseBranch:             "release",
		LibraryTaskName:        "  unit-test-coverage ",
		LibraryTaskDisplayName: "  100% Unit Test Coverage  ",
	}
	runCfg.ApplyDefaults()

	d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-library-activity",
			Skill:     "library_task",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()

	if got, want := api.codingEvents, 0; got != want {
		t.Fatalf("coding events = %d, want %d when library task activity path is used", got, want)
	}
	if got, want := len(api.activities), 1; got != want {
		t.Fatalf("activity count = %d, want %d", got, want)
	}
	if got, want := api.activities[0], "working on library task: 100% Unit Test Coverage"; got != want {
		t.Fatalf("library task activity = %q, want %q", got, want)
	}

	var startStatus map[string]any
	for _, payload := range statusPayloads(api.published) {
		if payload["status"] == "working" {
			if _, hasDetails := payload["details"]; !hasDetails {
				startStatus = payload
				break
			}
		}
	}
	if startStatus == nil {
		t.Fatalf("start status update missing: %#v", api.published)
	}
	if got, want := startStatus["message"], "working on library task: 100% Unit Test Coverage"; got != want {
		t.Fatalf("start status message = %#v, want %q", got, want)
	}
	statusUpdate, _ := startStatus["statusUpdate"].(map[string]any)
	status, _ := statusUpdate["status"].(map[string]any)
	message, _ := status["message"].(map[string]any)
	parts, _ := message["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	if got, want := part["text"], "working on library task: 100% Unit Test Coverage"; got != want {
		t.Fatalf("a2a status text = %#v, want %q", got, want)
	}
	metadata, _ := statusUpdate["metadata"].(map[string]any)
	if got, want := metadata["library_task_display_name"], "100% Unit Test Coverage"; got != want {
		t.Fatalf("a2a metadata library_task_display_name = %#v, want %q", got, want)
	}
}

func TestHandleDispatchQueuesFailureFollowUpWithExplicitResponseModeOptOut(t *testing.T) {
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
		ResponseMode: config.DisabledResponseMode,
	}
	runCfg.ApplyDefaults()

	d.handleDispatch(
		context.Background(),
		api,
		cfg,
		SkillDispatch{
			RequestID: "req-follow-up-off",
			Skill:     "code_for_me",
			Config:    runCfg,
		},
		"",
		false,
	)

	api.mu.Lock()
	defer api.mu.Unlock()

	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 2; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}

	followUpPayload := publishedResults[1]
	followUpConfig, _ := followUpPayload["config"].(map[string]any)
	if followUpConfig == nil {
		t.Fatalf("follow-up config missing: %#v", followUpPayload)
	}
	if got := followUpConfig["responseMode"]; got != nil {
		t.Fatalf("follow-up responseMode = %#v, want omitted", got)
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

	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 2; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
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
	res := app.Result{
		Err: errors.New("task failed"),
		RepoResults: []app.RepoResult{
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

func TestQueueFailureFollowUpSkipsCallerRoutingWhenTargetMissing(t *testing.T) {
	t.Parallel()

	api := &stubMoltenHubAPI{token: "t"}
	cfg := InitConfig{
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
		},
	}
	dispatch := SkillDispatch{
		RequestID: "req-missing-target",
		Skill:     "code_for_me",
		ReplyTo:   "agent-caller",
		Config: config.Config{
			Repo:   "git@github.com:acme/repo.git",
			Prompt: "fix issue",
		},
	}
	dispatch.Config.ApplyDefaults()
	res := app.Result{Err: errors.New("task failed")}

	if err := queueFailureFollowUp(context.Background(), api, cfg, dispatch, res, ""); err != nil {
		t.Fatalf("queueFailureFollowUp() error = %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.published) != 1 {
		t.Fatalf("published payload count = %d, want 1", len(api.published))
	}
	payload := api.published[0]
	if _, exists := payload["to"]; exists {
		t.Fatalf("payload unexpectedly includes to without route target: %#v", payload["to"])
	}
	if _, exists := payload["reply_to"]; exists {
		t.Fatalf("payload unexpectedly includes reply_to without route target: %#v", payload["reply_to"])
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

	publishedResults := nonStatusPayloads(api.published)
	if got, want := len(publishedResults), 2; got != want {
		t.Fatalf("published payload count = %d, want %d", got, want)
	}
	if got := publishedResults[0]["status"]; got != "error" {
		t.Fatalf("result payload status = %#v, want error", got)
	}
	if got := publishedResults[1]["request_id"]; got != "req-no-delta-failure-review" {
		t.Fatalf("follow-up request_id = %#v, want req-no-delta-failure-review", got)
	}
}

func TestShouldQueueFailureFollowUpSkipsNestedFailureReviewRequests(t *testing.T) {
	t.Parallel()

	dispatch := SkillDispatch{RequestID: "req-123-failure-review"}
	ok, reason := shouldQueueFailureFollowUp(dispatch, app.Result{Err: errors.New("still failing")})
	if ok || reason != "run is already a failure follow-up" {
		t.Fatalf("shouldQueueFailureFollowUp() = (%v, %q), want (false, %q)", ok, reason, "run is already a failure follow-up")
	}
}

func TestShouldQueueFailureRerunSkipsNestedRerunRequests(t *testing.T) {
	t.Parallel()

	dispatch := SkillDispatch{RequestID: "req-123-rerun"}
	ok, reason := shouldQueueFailureRerun(dispatch, app.Result{Err: errors.New("still failing")})
	if ok || reason != "run is already a failure rerun" {
		t.Fatalf("shouldQueueFailureRerun() = (%v, %q), want (false, %q)", ok, reason, "run is already a failure rerun")
	}
}

func TestShouldQueueFailureFollowUpSkipsNonRemediableFailures(t *testing.T) {
	t.Parallel()

	dispatch := SkillDispatch{RequestID: "req-123"}
	ok, reason := shouldQueueFailureFollowUp(dispatch, app.Result{
		Err: errors.New("git: verify remote write access for repo https://github.com/acme/repo.git branch \"moltenhub-fix\": exit status 128: remote: Write access to repository not granted. fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403"),
	})
	if ok || !strings.Contains(reason, "write access to repository not granted") {
		t.Fatalf("shouldQueueFailureFollowUp(repo access) = (%v, %q), want non-remediable repo access skip", ok, reason)
	}

	ok, reason = shouldQueueFailureFollowUp(dispatch, app.Result{
		Err: errors.New("git: run git [push -u origin moltenhub-branch]: exit status 1: remote: refusing to allow an OAuth App to create or update workflow `.github/workflows/docker-release.yml` without `workflow` scope"),
	})
	if ok || !strings.Contains(reason, "refusing to allow an oauth app to create or update workflow") {
		t.Fatalf("shouldQueueFailureFollowUp(workflow scope) = (%v, %q), want non-remediable workflow-scope skip", ok, reason)
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

	result := app.Result{
		WorkspaceDir: "/tmp/run-123",
		RepoResults: []app.RepoResult{{
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
	if !strings.Contains(prompt, "When failures occur, send a response back to the calling agent that clearly states failure and includes the error details. Use explicit `Failure:` and `Error details:` fields.") {
		t.Fatalf("prompt missing response contract: %q", prompt)
	}
	if !strings.Contains(prompt, "If a repository is not initialized after clone, use only gh CLI/git tools to create and push a main branch, then continue once git state is ready for work.") {
		t.Fatalf("prompt missing uninitialized-repo instruction: %q", prompt)
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

func TestRecordCodingActivityRunningLogsWarningOnFailure(t *testing.T) {
	t.Parallel()

	var logs []string
	d := NewDaemon(nil)
	d.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	api := &stubMoltenHubAPI{
		token: "t",
		recordCodingFn: func(context.Context) error {
			return errors.New("metadata rejected")
		},
	}

	d.recordCodingActivityRunning(context.Background(), api, "req-18")

	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}
	if !strings.Contains(logs[0], "action=record_coding_activity_running") {
		t.Fatalf("log = %q", logs[0])
	}
	if !strings.Contains(logs[0], "req-18") {
		t.Fatalf("log missing request id: %q", logs[0])
	}
}

func TestRecordActivityLogsWarningOnFailure(t *testing.T) {
	t.Parallel()

	var logs []string
	d := NewDaemon(nil)
	d.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	api := &stubMoltenHubAPI{
		token: "t",
		recordActivityFn: func(context.Context, string) error {
			return errors.New("metadata rejected")
		},
	}

	d.recordActivity(context.Background(), api, "req-19", "working on library task: code-review", "record_library_task_activity_running")

	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}
	if !strings.Contains(logs[0], "action=record_library_task_activity_running") {
		t.Fatalf("log = %q", logs[0])
	}
	if !strings.Contains(logs[0], "req-19") {
		t.Fatalf("log missing request id: %q", logs[0])
	}
}

func TestLibraryTaskActivityBuilders(t *testing.T) {
	t.Parallel()

	if got, want := libraryTaskStartActivity(config.Config{
		LibraryTaskName: "  security-review  ",
	}), "working on library task: security-review"; got != want {
		t.Fatalf("libraryTaskStartActivity() = %q, want %q", got, want)
	}
	if got, want := libraryTaskStartActivity(config.Config{
		LibraryTaskName:        "readme-upkeep",
		LibraryTaskDisplayName: " README Upkeep ",
	}), "working on library task: README Upkeep"; got != want {
		t.Fatalf("libraryTaskStartActivity(display) = %q, want %q", got, want)
	}
	if got, want := libraryTaskCompleteActivity(config.Config{
		LibraryTaskName:        "unit-test-coverage",
		LibraryTaskDisplayName: "100% Unit Test Coverage",
	}), "completed library task: 100% Unit Test Coverage"; got != want {
		t.Fatalf("libraryTaskCompleteActivity() = %q, want %q", got, want)
	}
	if got := libraryTaskStartActivity(config.Config{LibraryTaskName: "   "}); got != "" {
		t.Fatalf("libraryTaskStartActivity(blank) = %q, want empty", got)
	}
	if got := libraryTaskStartActivity(config.Config{LibraryTaskDisplayName: "README Upkeep"}); got != "" {
		t.Fatalf("libraryTaskStartActivity(display without name) = %q, want empty", got)
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

	wsURL := "ws://" + listener.Addr().String() + "/runtime/messages/ws"
	err = d.runWebsocketLoop(context.Background(), wsURL, api, cfg, nil, &workers, nil)
	if err == nil {
		t.Fatal("runWebsocketLoop() error = nil, want disconnect error")
	}

	if serverErr := <-serverDone; serverErr != nil {
		t.Fatalf("websocket server error = %v", serverErr)
	}
}

func TestRunWebsocketLoopProcessesRawA2AJSONRPCFrame(t *testing.T) {
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
		frame := `{
			"jsonrpc":"2.0",
			"method":"message/send",
			"params":{
				"metadata":{"from_agent_uri":"https://na.hub.molten.bot/acme/sender"},
				"message":{
					"messageId":"a2a-msg-ws-invalid",
					"contextId":"a2a-context-ws-invalid",
					"taskId":"a2a-task-ws-invalid",
					"role":"user",
					"parts":[{"kind":"data","data":{"repo":"git@github.com:acme/repo.git"}}]
				}
			}
		}`
		if writeErr := writeFrameToConn(conn, true, opcodeText, []byte(frame), false); writeErr != nil {
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

	wsURL := "ws://" + listener.Addr().String() + "/runtime/messages/ws"
	err = d.runWebsocketLoop(context.Background(), wsURL, api, cfg, nil, &workers, nil)
	if err == nil {
		t.Fatal("runWebsocketLoop() error = nil, want disconnect error")
	}
	workers.Wait()

	if serverErr := <-serverDone; serverErr != nil {
		t.Fatalf("websocket server error = %v", serverErr)
	}

	results := nonStatusPayloads(api.published)
	if len(results) != 1 {
		t.Fatalf("non-status payloads = %d, want 1; payloads=%#v", len(results), api.published)
	}
	payload := results[0]
	if got, want := payload["request_id"], "a2a-msg-ws-invalid"; got != want {
		t.Fatalf("request_id = %#v, want %q", got, want)
	}
	if got, want := payload["status"], "error"; got != want {
		t.Fatalf("status = %#v, want %q", got, want)
	}
	if got, want := payload["to"], "https://na.hub.molten.bot/acme/sender"; got != want {
		t.Fatalf("to = %#v, want %q", got, want)
	}
}
