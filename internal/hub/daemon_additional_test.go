package hub

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/library"
)

type stubMoltenHubAPI struct {
	token string

	pullFn func(ctx context.Context, timeoutMs int) (PulledOpenClawMessage, bool, error)
	recordFn func(context.Context) error

	mu        sync.Mutex
	acked     []string
	nacked    []string
	published []map[string]any
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
func (s *stubMoltenHubAPI) MarkOpenClawOffline(context.Context, string, string) error {
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
