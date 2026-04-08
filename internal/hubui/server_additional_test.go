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

func TestParseTruthyQueryParam(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"1", "true", "TRUE", "yes", "on", " y "} {
		if !parseTruthyQueryParam(raw) {
			t.Fatalf("parseTruthyQueryParam(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "off", "no"} {
		if parseTruthyQueryParam(raw) {
			t.Fatalf("parseTruthyQueryParam(%q) = true, want false", raw)
		}
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

func TestStudioStylesKeepPromptActionsVisible(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	css := resp.Body.String()
	if !strings.Contains(css, ".prompt-wrap .panel-header {\n  border-bottom-color: rgba(116, 160, 213, 0.2);\n  background: linear-gradient(180deg, rgba(255, 255, 255, 0.26), rgba(255, 255, 255, 0.08));\n  color: #6f88ad;\n  letter-spacing: 0.11em;\n  position: relative;\n  justify-content: flex-start;\n}") {
		t.Fatalf("expected Studio title bar to align its controls with the panel content")
	}
	if !strings.Contains(css, ".prompt-titlebar {\n  display: flex;\n  align-items: center;\n  gap: 12px;\n}") {
		t.Fatalf("expected Studio title bar to use a simple left-aligned control row")
	}
	if !strings.Contains(css, ".prompt-mode-tabs-titlebar {\n  justify-self: start;\n  align-self: center;\n}") {
		t.Fatalf("expected Studio mode tabs to stay left-aligned inside the title bar")
	}
	if !strings.Contains(css, ".prompt-wrap.panel {\n  display: flex;\n  flex-direction: column;\n  border-color: rgba(74, 118, 178, 0.18);") {
		t.Fatalf("expected studio panel to stack header/form without clipping")
	}
	if !strings.Contains(css, ".prompt-mode-tabs {\n  display: inline-flex;\n  gap: 4px;\n  padding: 5px;\n  border-radius: 14px;\n  border: 1px solid rgba(112, 163, 221, 0.28);\n  background: linear-gradient(180deg, rgba(248, 252, 255, 0.96), rgba(226, 239, 255, 0.92));") {
		t.Fatalf("expected studio mode tabs to use the refreshed light segmented-control treatment")
	}
	if !strings.Contains(css, ".prompt-form {\n  display: grid;\n  gap: 10px;\n  padding: 14px 14px 13px;\n  min-width: 0;\n  min-height: 0;\n  overflow-y: auto;\n}") {
		t.Fatalf("expected studio form content to scroll instead of clipping controls")
	}
	if !strings.Contains(css, ".prompt-field-repository {\n  flex: 1 1 320px;\n  min-width: 280px;\n}") {
		t.Fatalf("expected repository input to retain enough width beside the history selector")
	}
	if !strings.Contains(css, ".prompt-actions {\n  display: flex;\n  align-items: center;\n  gap: 10px;\n  min-width: 0;\n}") {
		t.Fatalf("expected prompt actions to keep the status between screenshots and the right-aligned buttons")
	}
	if !strings.Contains(css, ".prompt-actions-start {\n  display: flex;\n  flex: 1 1 30%;\n  min-width: 0;\n}") {
		t.Fatalf("expected prompt actions to reserve the left side for screenshots")
	}
	if !strings.Contains(css, ".prompt-actions-end {\n  display: flex;\n  align-items: center;\n  justify-content: flex-end;\n  gap: 10px;\n  margin-left: auto;\n  flex: 0 0 auto;\n}") {
		t.Fatalf("expected prompt actions to right-align Clear and Run")
	}
	if !strings.Contains(css, ".prompt-action-paste {\n  display: flex;\n  align-items: center;\n  flex: 1 1 auto;\n  width: 100%;\n  max-width: 100%;") {
		t.Fatalf("expected screenshot paste target to fill the left action group")
	}
	if !strings.Contains(css, ".prompt-action-button {\n  width: auto;\n  display: inline-flex;") {
		t.Fatalf("expected action buttons to avoid full-width auto-column overflow")
	}
	if !strings.Contains(css, ".submit-status-inline {\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;\n  flex: 0 1 280px;\n  min-width: 140px;\n  text-align: center;\n}") {
		t.Fatalf("expected inline status to sit between the screenshot area and the action buttons")
	}
	if !strings.Contains(css, ".prompt-image-chip {\n  border-radius: 14px;\n  border: 1px solid var(--border);\n  background: linear-gradient(160deg, rgba(255, 255, 255, 0.94), rgba(240, 246, 255, 0.88));") {
		t.Fatalf("expected screenshot chips to use the shared light panel treatment")
	}
	if !strings.Contains(css, "@media (max-width: 640px) {\n  .prompt-actions {\n    flex-wrap: wrap;\n    gap: 6px;\n  }\n\n  .prompt-actions-start,\n  .submit-status-inline,\n  .prompt-actions-end {\n    flex: 1 1 100%;\n    width: 100%;\n  }\n\n  .prompt-actions-end {\n    justify-content: flex-end;\n    margin-left: 0;\n  }\n\n  .prompt-action-paste {\n    max-width: none;\n  }\n\n  .submit-status-inline {\n    min-width: 0;\n  }\n\n  .prompt-action-button {\n    flex: 1 1 0;\n    min-inline-size: 0;\n  }") {
		t.Fatalf("expected mobile layout to keep Studio action controls fully visible")
	}
}

func TestLibraryTaskListUsesDesktopTwoColumnAndMobileSingleColumnLayout(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	css := resp.Body.String()
	if !strings.Contains(css, ".library-task-list {\n  display: grid;\n  grid-template-columns: repeat(2, minmax(0, 1fr));") {
		t.Fatalf("expected library task list to use two columns in wide layouts")
	}
	if !strings.Contains(css, "@media (max-width: 720px) {\n  .library-task-list {\n    grid-template-columns: minmax(0, 1fr);\n  }") {
		t.Fatalf("expected library task list to collapse to one column only on mobile")
	}
}

func TestStudioStylesUseRefinedPanelAndInputTreatment(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	css := resp.Body.String()
	if !strings.Contains(css, ".prompt-wrap.panel {\n  display: flex;\n  flex-direction: column;\n  border-color: rgba(74, 118, 178, 0.18);\n  background:\n    linear-gradient(180deg, rgba(223, 241, 255, 0.96), rgba(245, 250, 255, 0.9) 18%, rgba(255, 255, 255, 0.92) 100%),") {
		t.Fatalf("expected studio panel to use the refreshed blue-tint shell treatment")
	}
	if !strings.Contains(css, ".prompt-wrap .panel-header {\n  border-bottom-color: rgba(116, 160, 213, 0.2);\n  background: linear-gradient(180deg, rgba(255, 255, 255, 0.26), rgba(255, 255, 255, 0.08));\n  color: #6f88ad;\n  letter-spacing: 0.11em;\n  position: relative;\n  justify-content: flex-start;\n}") {
		t.Fatalf("expected studio header to keep its controls aligned to the left")
	}
	if !strings.Contains(css, ".prompt-mode-tab.active {\n  background: linear-gradient(135deg, #fefefe 0%, #e7f2ff 48%, #d7eaff 100%);\n  border: 1px solid rgba(112, 163, 221, 0.34);") {
		t.Fatalf("expected active studio mode tab to use the light shell treatment instead of the dark accent pill")
	}
	if !strings.Contains(css, ".prompt-control,\n.prompt-text,\n.prompt-action-paste {\n  width: 100%;\n  border: 1px solid rgba(112, 163, 221, 0.34);\n  border-radius: 16px;\n  background: linear-gradient(180deg, rgba(251, 254, 255, 0.98), rgba(234, 245, 255, 0.88));") {
		t.Fatalf("expected studio controls to use the updated light-blue input treatment")
	}
	if !strings.Contains(css, "select.prompt-control {\n  appearance: none;\n  background-image:\n    linear-gradient(45deg, transparent 50%, #3d82dc 50%),\n    linear-gradient(135deg, #3d82dc 50%, transparent 50%);") {
		t.Fatalf("expected repository selector to use the refreshed blue chevron treatment")
	}
}

func TestHeaderStatusStylesStayReadable(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	css := resp.Body.String()
	if !strings.Contains(css, ".status-item-compact {\n  position: relative;\n  justify-content: center;\n  gap: 0;\n  width: 42px;\n  min-width: 42px;\n  height: 42px;\n  min-height: 42px;") {
		t.Fatalf("expected compact status dots to use the larger readable pill sizing")
	}
	if !strings.Contains(css, ".header {\n  position: relative;\n  z-index: 5;") {
		t.Fatalf("expected header to create a higher stacking context above the studio panel")
	}
	if !strings.Contains(css, ".status-item-compact:hover,\n.status-item-compact:focus-visible,\n.status-item-compact:focus-within {\n  z-index: 7;\n}") {
		t.Fatalf("expected connection status hover state to rise above adjacent panels")
	}
	if !strings.Contains(css, ".status-item-metrics {\n  gap: 12px;\n  padding-left: 12px;\n  padding-right: 14px;\n  min-height: 42px;\n  height: 42px;") {
		t.Fatalf("expected metrics pill to use stronger spacing and height")
	}
	if !strings.Contains(css, ".status-item-metrics .status-value {\n  color: var(--text-soft);\n  font-size: 0.9rem;") {
		t.Fatalf("expected metrics text to use readable status color tokens")
	}
	if !strings.Contains(css, "@media (max-width: 720px) {\n  .library-task-list {\n    grid-template-columns: minmax(0, 1fr);\n  }\n\n  .status-row {\n    flex-wrap: nowrap;\n    gap: 8px;\n  }\n\n  .status-item-metrics {\n    flex: 1 1 auto;\n    width: auto;\n    min-width: 0;") {
		t.Fatalf("expected status row to stay on one line through mobile widths")
	}
}

func TestAuthGateVerifyButtonHidesWhileVerificationIsPending(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	html := resp.Body.String()
	if !strings.Contains(html, "agentAuthVerifyPending: false") {
		t.Fatalf("expected auth gate state to track pending verification")
	}
	if !strings.Contains(html, "!state.agentAuthVerifyPending;") {
		t.Fatalf("expected Done button visibility to depend on pending verification state")
	}
	if !strings.Contains(html, "function setAgentAuthVerifyPending(pending)") {
		t.Fatalf("expected helper to toggle pending verification state")
	}
	if !strings.Contains(html, "setAgentAuthVerifyPending(true);") {
		t.Fatalf("expected verify action to hide Done button before verification completes")
	}
	if !strings.Contains(html, "setAgentAuthVerifyPending(false);") {
		t.Fatalf("expected verify action to restore Done button after failed verification")
	}
}
