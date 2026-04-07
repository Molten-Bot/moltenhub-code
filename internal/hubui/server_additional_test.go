package hubui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/library"
)

func TestNewServerDefaultsAndLogfHelper(t *testing.T) {
	t.Parallel()

	srv := NewServer(" 127.0.0.1:7777 ", NewBroker())
	if got, want := srv.Addr, "127.0.0.1:7777"; got != want {
		t.Fatalf("NewServer().Addr = %q, want %q", got, want)
	}
	if srv.Broker == nil {
		t.Fatal("NewServer().Broker = nil, want non-nil")
	}
	if srv.Logf == nil {
		t.Fatal("NewServer().Logf = nil, want non-nil")
	}
	if srv.LoadLibraryTasks == nil {
		t.Fatal("NewServer().LoadLibraryTasks = nil, want non-nil")
	}

	var lines []string
	srv.Logf = func(format string, args ...any) {
		lines = append(lines, format)
	}
	srv.logf("hub.ui status=ok")
	if len(lines) != 1 {
		t.Fatalf("logf() line count = %d, want 1", len(lines))
	}
}

func TestNewServerDefaultLibraryLoaderSuccessAndFailure(t *testing.T) {
	srv := NewServer("", NewBroker())

	tasks, err := srv.LoadLibraryTasks()
	if err != nil {
		t.Fatalf("default LoadLibraryTasks() error = %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("default LoadLibraryTasks() returned no tasks")
	}
}

func TestServerRunValidationAndShutdownPaths(t *testing.T) {
	t.Parallel()

	if err := (Server{Addr: ""}).Run(context.Background()); err != nil {
		t.Fatalf("Run(empty addr) error = %v, want nil", err)
	}

	if err := (Server{Addr: "127.0.0.1:0"}).Run(context.Background()); err == nil {
		t.Fatal("Run(nil broker) error = nil, want non-nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer("127.0.0.1:0", NewBroker())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run(cancel) error = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run(cancel) did not stop in time")
	}
}

func TestHealthEndpointAndWriteJSONMarshalFailure(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), `"ok":true`) {
		t.Fatalf("GET /healthz body = %q, want ok=true JSON", resp.Body.String())
	}

	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"bad": func() {}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("writeJSON(marshal failure) status = %d, want 500", rec.Code)
	}
}

func TestDuplicateSubmissionDetailsNonMatchAndNil(t *testing.T) {
	t.Parallel()

	if _, _, ok := duplicateSubmissionDetails(nil); ok {
		t.Fatal("duplicateSubmissionDetails(nil) ok = true, want false")
	}
	if _, _, ok := duplicateSubmissionDetails(errors.New("plain error")); ok {
		t.Fatal("duplicateSubmissionDetails(non duplicate error) ok = true, want false")
	}
}

func TestLibraryEndpointMethodAndLoaderVariants(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())

	req := httptest.NewRequest(http.MethodPost, "/api/library", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/library status = %d, want 405", resp.Code)
	}

	srv.LoadLibraryTasks = nil
	req = httptest.NewRequest(http.MethodGet, "/api/library", nil)
	resp = httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/library (nil loader) status = %d, want 200", resp.Code)
	}

	srv.LoadLibraryTasks = func() ([]library.TaskSummary, error) {
		return nil, errors.New("catalog missing")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/library", nil)
	resp = httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/library (loader error) status = %d, want 500", resp.Code)
	}
}

func TestAgentAuthEndpointsDefaultAndMethodHandling(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/agent-auth")
	if err != nil {
		t.Fatalf("GET /api/agent-auth error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/agent-auth status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /api/agent-auth: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}

	startResp, err := http.Post(ts.URL+"/api/agent-auth/start-device", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/start-device error = %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST /api/agent-auth/start-device status = %d, want 501", startResp.StatusCode)
	}

	verifyResp, err := http.Post(ts.URL+"/api/agent-auth/verify", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/verify error = %v", err)
	}
	defer verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST /api/agent-auth/verify status = %d, want 501", verifyResp.StatusCode)
	}

	methodResp, err := http.Post(ts.URL+"/api/agent-auth", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/agent-auth error = %v", err)
	}
	defer methodResp.Body.Close()
	if methodResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/agent-auth status = %d, want 405", methodResp.StatusCode)
	}
}

func TestAgentAuthEndpointsWithCallbacks(t *testing.T) {
	t.Parallel()

	want := AgentAuthState{
		Harness:    "codex",
		Required:   true,
		Ready:      false,
		State:      "pending_device_auth",
		Message:    "waiting",
		AuthURL:    "https://auth.openai.com/codex/device",
		DeviceCode: "ABCD-EFGH",
	}

	srv := NewServer("", NewBroker())
	srv.AgentAuthStatus = func(context.Context) (AgentAuthState, error) {
		return want, nil
	}
	srv.StartAgentAuth = func(context.Context) (AgentAuthState, error) {
		return want, nil
	}
	srv.VerifyAgentAuth = func(context.Context) (AgentAuthState, error) {
		return AgentAuthState{
			Harness:  "codex",
			Required: true,
			Ready:    true,
			State:    "ready",
			Message:  "ready",
		}, nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	statusResp, err := http.Get(ts.URL + "/api/agent-auth")
	if err != nil {
		t.Fatalf("GET /api/agent-auth error = %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/agent-auth status = %d, want 200", statusResp.StatusCode)
	}

	startResp, err := http.Post(ts.URL+"/api/agent-auth/start-device", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/start-device error = %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/start-device status = %d, want 200", startResp.StatusCode)
	}

	verifyResp, err := http.Post(ts.URL+"/api/agent-auth/verify", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/verify error = %v", err)
	}
	defer verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/verify status = %d, want 200", verifyResp.StatusCode)
	}
}

func TestTaskPanelStylesConstrainHorizontalOverflow(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	css := resp.Body.String()
	if !strings.Contains(css, ".task-output-list {\n  margin-top: 8px;\n  border-top: 1px dashed rgba(125, 154, 185, 0.32);\n  padding-top: 7px;\n  display: grid;\n  grid-template-columns: minmax(0, 1fr);") {
		t.Fatalf("expected task output list to clamp width to panel columns")
	}
	if !strings.Contains(css, ".task-meta > div {\n  min-width: 0;\n  overflow: hidden;\n  text-overflow: ellipsis;\n  white-space: nowrap;\n}") {
		t.Fatalf("expected task metadata rows to truncate instead of widening cards")
	}
	if !strings.Contains(css, ".task-scroll {\n  scrollbar-width: thin;\n  scrollbar-color: rgba(136, 162, 189, 0.55) rgba(17, 28, 42, 0.35);\n  overflow-x: hidden;\n}") {
		t.Fatalf("expected task scroll containers to hide horizontal overflow")
	}
}
