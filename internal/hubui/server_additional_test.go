package hubui

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/library"
)

func TestHandleHubSetupStatusAndConfigure(t *testing.T) {
	t.Parallel()

	const token = "f9mju6sL6Qns5WX1H09ghY5X4HJHHRTlcc6nzfiOdxs"

	srv := NewServer("", NewBroker())
	srv.HubSetupStatus = func(context.Context) (HubSetupState, error) {
		state := defaultHubSetupState()
		state.Configured = true
		state.Region = "eu"
		state.Handle = "molten-agent"
		state.Profile.DisplayName = "Molten Agent"
		return state, nil
	}
	srv.ConfigureHubSetup = func(_ context.Context, req HubSetupRequest) (HubSetupState, error) {
		if req.Token != token {
			return defaultHubSetupState(), fmt.Errorf("unexpected token")
		}
		if req.Region != "eu" {
			return defaultHubSetupState(), fmt.Errorf("unexpected region %q", req.Region)
		}
		state := defaultHubSetupState()
		state.Configured = true
		state.AgentMode = req.AgentMode
		state.TokenType = req.TokenType
		state.Region = req.Region
		state.Handle = "saved-agent"
		state.Profile.DisplayName = "Saved Agent"
		state.NeedsRestart = false
		return state, nil
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/hub-setup", nil)
	getResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET /api/hub-setup status = %d, want 200", getResp.Code)
	}

	var getBody struct {
		OK  bool          `json:"ok"`
		Hub HubSetupState `json:"hub"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&getBody); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if !getBody.OK || !getBody.Hub.Configured {
		t.Fatalf("GET hub setup body = %#v, want configured ok response", getBody)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/api/hub-setup", strings.NewReader(fmt.Sprintf(`{"agent_mode":"new","token_type":"bind","region":"eu","token":%q}`, token)))
	postReq.Header.Set("Content-Type", "application/json")
	postResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(postResp, postReq)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST /api/hub-setup status = %d, want 200", postResp.Code)
	}

	var postBody struct {
		OK  bool          `json:"ok"`
		Hub HubSetupState `json:"hub"`
	}
	if err := json.NewDecoder(postResp.Body).Decode(&postBody); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if !postBody.OK || postBody.Hub.Handle != "saved-agent" || postBody.Hub.NeedsRestart || postBody.Hub.AgentMode != "new" || postBody.Hub.TokenType != "bind" || postBody.Hub.Region != "eu" {
		t.Fatalf("POST hub setup body = %#v, want saved state", postBody)
	}
}

func TestHandleHubSetupConfigureRejectsBadPayload(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfigureHubSetup = func(context.Context, HubSetupRequest) (HubSetupState, error) {
		t.Fatal("ConfigureHubSetup should not be called for malformed JSON")
		return HubSetupState{}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/hub-setup", strings.NewReader("{"))
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/hub-setup malformed status = %d, want 400", resp.Code)
	}
}

func TestHandleHubSetupConnectAndDisconnect(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConnectHubSetup = func(context.Context) (HubSetupState, error) {
		state := defaultHubSetupState()
		state.Configured = true
		state.Message = "Molten Hub connected."
		return state, nil
	}
	srv.DisconnectHubSetup = func(context.Context) (HubSetupState, error) {
		state := defaultHubSetupState()
		state.Configured = true
		state.Message = "Molten Hub disconnected."
		return state, nil
	}

	connectReq := httptest.NewRequest(http.MethodPost, "/api/hub-setup/connect", nil)
	connectResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(connectResp, connectReq)
	if connectResp.Code != http.StatusOK {
		t.Fatalf("POST /api/hub-setup/connect status = %d, want 200", connectResp.Code)
	}

	var connectBody struct {
		OK  bool          `json:"ok"`
		Hub HubSetupState `json:"hub"`
	}
	if err := json.NewDecoder(connectResp.Body).Decode(&connectBody); err != nil {
		t.Fatalf("decode connect body: %v", err)
	}
	if !connectBody.OK || connectBody.Hub.Message != "Molten Hub connected." {
		t.Fatalf("connect body = %#v", connectBody)
	}

	disconnectReq := httptest.NewRequest(http.MethodPost, "/api/hub-setup/disconnect", nil)
	disconnectResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(disconnectResp, disconnectReq)
	if disconnectResp.Code != http.StatusOK {
		t.Fatalf("POST /api/hub-setup/disconnect status = %d, want 200", disconnectResp.Code)
	}

	var disconnectBody struct {
		OK  bool          `json:"ok"`
		Hub HubSetupState `json:"hub"`
	}
	if err := json.NewDecoder(disconnectResp.Body).Decode(&disconnectBody); err != nil {
		t.Fatalf("decode disconnect body: %v", err)
	}
	if !disconnectBody.OK || disconnectBody.Hub.Message != "Molten Hub disconnected." {
		t.Fatalf("disconnect body = %#v", disconnectBody)
	}
}

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

func TestIndexAndStaticCacheHeaders(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	indexResp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	defer indexResp.Body.Close()
	if got, want := indexResp.Header.Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("GET / cache-control = %q, want %q", got, want)
	}

	staticResp, err := http.Get(ts.URL + "/static/style.css")
	if err != nil {
		t.Fatalf("GET /static/style.css error = %v", err)
	}
	defer staticResp.Body.Close()
	if got, want := staticResp.Header.Get("Cache-Control"), "public, max-age=3600"; got != want {
		t.Fatalf("GET /static/style.css cache-control = %q, want %q", got, want)
	}
}

func TestStaticStyleIncludesSharedDockIconStyles(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("GET /static/style.css status = %d, want 200", resp.Code)
	}

	stylesheet := resp.Body.String()
	if !strings.Contains(stylesheet, `.prompt-mode-link-logo {`) {
		t.Fatalf("expected stylesheet to define shared dock icon link styles")
	}
	if !strings.Contains(stylesheet, `.prompt-mode-divider {`) {
		t.Fatalf("expected stylesheet to define the shared dock divider style")
	}
	if !strings.Contains(stylesheet, `.prompt-mode-link-icon img {`) {
		t.Fatalf("expected stylesheet to size dock icon images through shared logo styles")
	}
	if !strings.Contains(stylesheet, `.prompt-mode-link-icon svg {`) {
		t.Fatalf("expected stylesheet to size dock svg icons through shared icon styles")
	}
	if !strings.Contains(stylesheet, `filter: var(--agent-logo-filter);`) {
		t.Fatalf("expected stylesheet to keep dock icons theme-reactive via agent logo filter")
	}
	if !strings.Contains(stylesheet, `#moltenbot-hub-link:hover img,`) {
		t.Fatalf("expected stylesheet to give molten bot hub icon a hover-specific treatment")
	}
	if !strings.Contains(stylesheet, `#moltenbot-hub-link:focus-visible img {`) {
		t.Fatalf("expected stylesheet to give molten bot hub icon a keyboard-focus treatment")
	}
	if !strings.Contains(stylesheet, `filter: none;`) {
		t.Fatalf("expected molten bot hub icon hover treatment to restore the native molten hub logo colors")
	}
	if !strings.Contains(stylesheet, `.hub-dock-plus {`) {
		t.Fatalf("expected stylesheet to include molten hub plus badge styles")
	}
	if !strings.Contains(stylesheet, `.hub-profile-button {`) {
		t.Fatalf("expected stylesheet to include hub profile button base styles")
	}
	if !strings.Contains(stylesheet, ".hub-profile-button {\n  opacity: 1;\n  pointer-events: auto;") {
		t.Fatalf("expected stylesheet to keep the hub profile button visible by default")
	}
	if !strings.Contains(stylesheet, ".hub-profile-button {\n  opacity: 1;\n  pointer-events: auto;\n  color: var(--theme-button-text);") {
		t.Fatalf("expected hub profile button to use a readable default icon color without hover")
	}
	if strings.Contains(stylesheet, `.hub-dock-group[data-configured="true"]:hover .hub-profile-button`) {
		t.Fatalf("expected stylesheet to remove hover-only hub profile button visibility")
	}
	if !strings.Contains(stylesheet, `.hub-profile-button svg {`) {
		t.Fatalf("expected stylesheet to define hub profile button icon sizing")
	}
	if !strings.Contains(stylesheet, ".hub-profile-button:hover,\n.hub-profile-button:focus-visible {\n  color: var(--running);\n  background: transparent;\n  box-shadow: none;\n}") {
		t.Fatalf("expected hub profile button hover treatment to avoid background and box-shadow")
	}
	if !strings.Contains(stylesheet, `.hub-setup-toggle {`) {
		t.Fatalf("expected stylesheet to include hub setup toggle styles")
	}
	if !strings.Contains(stylesheet, `.hub-setup-profile-grid {`) {
		t.Fatalf("expected stylesheet to include hub setup profile grid styles")
	}
	if !strings.Contains(stylesheet, `grid-template-columns: repeat(2, minmax(0, 1fr));`) {
		t.Fatalf("expected profile grid to keep two equal columns and move emoji controls onto their own row")
	}
	if !strings.Contains(stylesheet, `.hub-setup-profile-emoji {`) || !strings.Contains(stylesheet, `grid-column: 1 / -1;`) {
		t.Fatalf("expected stylesheet to make the emoji profile field span the full profile grid width")
	}
	if !strings.Contains(stylesheet, `.hub-setup-profile-text {`) {
		t.Fatalf("expected stylesheet to keep the profile textarea spanning the full profile grid width")
	}
	if strings.Contains(stylesheet, `.hub-setup-onboarding {`) || strings.Contains(stylesheet, `.hub-setup-onboarding-step {`) {
		t.Fatalf("expected stylesheet to remove the hub setup onboarding progress list styles")
	}
	if !strings.Contains(stylesheet, `.hub-setup-profile-input {`) || !strings.Contains(stylesheet, `min-height: calc(2 * 1.45em + 24px);`) {
		t.Fatalf("expected stylesheet to size the hub profile textarea to two lines by default while keeping it resizable")
	}
	if !strings.Contains(stylesheet, `.hub-setup-signin-logo {`) {
		t.Fatalf("expected stylesheet to include the hub setup sign-in logo styles")
	}
	if !strings.Contains(stylesheet, `.hub-setup-copy {`) {
		t.Fatalf("expected stylesheet to include hub setup supporting copy styles")
	}
	if !strings.Contains(stylesheet, `.hub-setup-status {`) {
		t.Fatalf("expected stylesheet to include hub setup status line styles")
	}
	if !strings.Contains(stylesheet, `.hub-setup-actions {`) || !strings.Contains(stylesheet, `margin-left: auto;`) {
		t.Fatalf("expected stylesheet to keep the hub setup actions docked to the right edge")
	}
	if !strings.Contains(stylesheet, `.hub-emoji-picker-toggle {`) || !strings.Contains(stylesheet, `width: 100%;`) {
		t.Fatalf("expected stylesheet to render the emoji picker as a full-width button control")
	}
	if !strings.Contains(stylesheet, `.hub-emoji-picker-input {`) || !strings.Contains(stylesheet, `clip-path: inset(50%);`) {
		t.Fatalf("expected stylesheet to hide the raw emoji input and rely on the picker button presentation")
	}
	if !strings.Contains(stylesheet, `.hub-emoji-picker-body {`) || !strings.Contains(stylesheet, `.hub-emoji-mart {`) {
		t.Fatalf("expected stylesheet to include the embedded emoji-mart shell styles")
	}
	if !strings.Contains(stylesheet, `.brand-login-card-shell {`) || !strings.Contains(stylesheet, `.brand-chip-action {`) {
		t.Fatalf("expected stylesheet to expose user-portal-derived glass primitives for hub surfaces and chips")
	}
	if strings.Contains(stylesheet, `.prompt-hero {`) {
		t.Fatalf("expected stylesheet to remove the Studio hero styles")
	}
	if !strings.Contains(stylesheet, `.prompt-field-copy {`) {
		t.Fatalf("expected stylesheet to retain helper copy styles")
	}
	if !strings.Contains(stylesheet, `.task-empty {`) || !strings.Contains(stylesheet, `.task-empty-copy {`) {
		t.Fatalf("expected stylesheet to include the richer task queue empty state styles")
	}
}

func TestStreamEndpointCompactsEventsPayload(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-stream-compact")
	b.IngestLog("dispatch request_id=req-stream-compact stage=codex status=running")

	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatalf("GET /api/stream error = %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read stream line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first stream line = %q, want data frame", line)
	}

	var snap Snapshot
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &snap); err != nil {
		t.Fatalf("decode stream snapshot: %v", err)
	}
	if got := len(snap.Events); got != 0 {
		t.Fatalf("len(stream snapshot events) = %d, want 0 for compact payload", got)
	}
	if got := len(snap.Tasks); got == 0 {
		t.Fatal("stream snapshot tasks missing")
	}
}

func TestCompactStreamSnapshotTrimsTaskLogs(t *testing.T) {
	t.Parallel()

	logs := make([]TaskLog, maxStreamTaskLogs+9)
	for i := range logs {
		logs[i] = TaskLog{Text: fmt.Sprintf("line-%d", i)}
	}

	snap := compactStreamSnapshot(Snapshot{
		Events: []Event{{ID: 1}},
		Tasks: []Task{
			{
				RequestID: "req-1",
				Logs:      logs,
			},
		},
	})

	if len(snap.Events) != 0 {
		t.Fatalf("len(events) = %d, want 0", len(snap.Events))
	}
	if got, want := len(snap.Tasks[0].Logs), maxStreamTaskLogs; got != want {
		t.Fatalf("len(task logs) = %d, want %d", got, want)
	}
	if got, want := snap.Tasks[0].Logs[0].Text, "line-9"; got != want {
		t.Fatalf("first retained log = %q, want %q", got, want)
	}
}

func TestHandlerGzipCompressionForIndexAndNoCompressionForSSE(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-gzip")
	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	indexReq, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatalf("new index request: %v", err)
	}
	indexReq.Header.Set("Accept-Encoding", "gzip")
	indexResp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(indexReq)
	if err != nil {
		t.Fatalf("GET / with gzip accept: %v", err)
	}
	defer indexResp.Body.Close()

	if got := indexResp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("GET / content-encoding = %q, want gzip", got)
	}
	if vary := indexResp.Header.Get("Vary"); !strings.Contains(strings.ToLower(vary), "accept-encoding") {
		t.Fatalf("GET / vary = %q, want Accept-Encoding", vary)
	}

	zr, err := gzip.NewReader(indexResp.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	indexBody, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close gzip reader: %v", err)
	}
	if !strings.Contains(string(indexBody), "<!doctype html>") {
		t.Fatalf("decompressed index body missing html doctype")
	}

	streamReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	streamReq.Header.Set("Accept-Encoding", "gzip")
	streamResp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(streamReq)
	if err != nil {
		t.Fatalf("GET /api/stream with gzip accept: %v", err)
	}
	defer streamResp.Body.Close()

	if got := streamResp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("GET /api/stream content-encoding = %q, want empty", got)
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

	configureResp, err := http.Post(ts.URL+"/api/agent-auth/configure", "application/json", bytes.NewBufferString(`{"augment_session_auth":"{}"}`))
	if err != nil {
		t.Fatalf("POST /api/agent-auth/configure error = %v", err)
	}
	defer configureResp.Body.Close()
	if configureResp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST /api/agent-auth/configure status = %d, want 501", configureResp.StatusCode)
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

func TestEmbeddedPromptActionStylesCoverPasteWidthAndStatusFade(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(staticFiles, "static/style.css")
	if err != nil {
		t.Fatalf("read embedded style.css: %v", err)
	}
	css := string(data)

	for _, want := range []string{
		"flex: 0 1 var(--prompt-paste-width, 25%);",
		"width: min(100%, var(--prompt-paste-width, 25%));",
		"max-width: 50%;",
		"flex: 1 1 0;",
		"transition: opacity 220ms ease;",
		".submit-status-inline.is-visible {",
		".submit-status-inline.is-fading {",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("embedded style.css missing %q", want)
		}
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
	srv.ConfigureAgentAuth = func(_ context.Context, sessionAuth string) (AgentAuthState, error) {
		if got, want := strings.TrimSpace(sessionAuth), `{"accessToken":"token_saved","tenantURL":"https://tenant.example/","scopes":["email"]}`; got != want {
			t.Fatalf("sessionAuth = %q, want %q", got, want)
		}
		return AgentAuthState{
			Harness:  "auggie",
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

	configureResp, err := http.Post(ts.URL+"/api/agent-auth/configure", "application/json", bytes.NewBufferString(`{"augment_session_auth":"{\"accessToken\":\"token_saved\",\"tenantURL\":\"https://tenant.example/\",\"scopes\":[\"email\"]}"}`))
	if err != nil {
		t.Fatalf("POST /api/agent-auth/configure error = %v", err)
	}
	defer configureResp.Body.Close()
	if configureResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/configure status = %d, want 200", configureResp.StatusCode)
	}
}

func TestAgentAuthConfigureEndpointAcceptsGitHubTokenPayload(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfigureAgentAuth = func(_ context.Context, value string) (AgentAuthState, error) {
		if got, want := strings.TrimSpace(value), "ghp_saved_token"; got != want {
			t.Fatalf("configure value = %q, want %q", got, want)
		}
		return AgentAuthState{
			Harness:  "claude",
			Required: true,
			Ready:    true,
			State:    "ready",
			Message:  "ready",
		}, nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/api/agent-auth/configure",
		"application/json",
		bytes.NewBufferString(`{"github_token":"ghp_saved_token"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/configure error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/configure status = %d, want 200", resp.StatusCode)
	}
}

func TestAgentAuthConfigureEndpointAcceptsClaudeAuthCodePayload(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfigureAgentAuth = func(_ context.Context, value string) (AgentAuthState, error) {
		if got, want := strings.TrimSpace(value), "code-from-claude"; got != want {
			t.Fatalf("configure value = %q, want %q", got, want)
		}
		return AgentAuthState{
			Harness:  "claude",
			Required: true,
			Ready:    false,
			State:    "pending_browser_login",
			Message:  "code received",
		}, nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/api/agent-auth/configure",
		"application/json",
		bytes.NewBufferString(`{"claude_auth_code":"code-from-claude"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/configure error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/configure status = %d, want 200", resp.StatusCode)
	}
}

func TestAgentAuthConfigureEndpointAcceptsPiProviderAuthPayload(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfigureAgentAuth = func(_ context.Context, value string) (AgentAuthState, error) {
		if got, want := strings.TrimSpace(value), `{"env_var":"OPENAI_API_KEY","value":"sk-saved"}`; got != want {
			t.Fatalf("configure value = %q, want %q", got, want)
		}
		return AgentAuthState{
			Harness:  "pi",
			Required: true,
			Ready:    true,
			State:    "ready",
			Message:  "ready",
		}, nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/api/agent-auth/configure",
		"application/json",
		bytes.NewBufferString(`{"pi_provider_auth":"{\"env_var\":\"OPENAI_API_KEY\",\"value\":\"sk-saved\"}"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/configure error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/configure status = %d, want 200", resp.StatusCode)
	}
}

func TestAgentAuthConfigureEndpointAcceptsPiAuthJSONPayload(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfigureAgentAuth = func(_ context.Context, value string) (AgentAuthState, error) {
		if got, want := strings.TrimSpace(value), `{"provider":"pi","token":"saved"}`; got != want {
			t.Fatalf("configure value = %q, want %q", got, want)
		}
		return AgentAuthState{
			Harness:  "pi",
			Required: true,
			Ready:    true,
			State:    "ready",
			Message:  "ready",
		}, nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/api/agent-auth/configure",
		"application/json",
		bytes.NewBufferString(`{"pi_auth_json":"{\"provider\":\"pi\",\"token\":\"saved\"}"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/agent-auth/configure error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/agent-auth/configure status = %d, want 200", resp.StatusCode)
	}
}

func TestGitHubProfileEndpointReturnsResolvedPublicProfile(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ResolveGitHubProfileURL = func(context.Context) (string, error) {
		return "https://github.com/jefking", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/github/profile")
	if err != nil {
		t.Fatalf("GET /api/github/profile error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/github/profile status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		OK         bool   `json:"ok"`
		ProfileURL string `json:"profileUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /api/github/profile: %v", err)
	}
	if !body.OK {
		t.Fatalf("profile response ok = false, want true")
	}
	if got, want := body.ProfileURL, "https://github.com/jefking"; got != want {
		t.Fatalf("profileUrl = %q, want %q", got, want)
	}
}

func TestGitHubProfileEndpointReturnsOkFalseWhenUnavailable(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ResolveGitHubProfileURL = func(context.Context) (string, error) {
		return "", errors.New("github token is not configured")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/github/profile", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/github/profile status = %d, want 200", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), `"ok":false`) {
		t.Fatalf("response = %q, want ok=false", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `github token is not configured`) {
		t.Fatalf("response = %q, want missing-token detail", resp.Body.String())
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
	if !strings.Contains(css, ".task-meta {\n  display: flex;\n  flex-wrap: wrap;\n  align-items: center;\n  gap: 4px 0;\n  color: var(--muted);\n  font-size: 0.74rem;\n  min-width: 0;\n}") {
		t.Fatalf("expected task metadata to share one line when space allows")
	}
	if !strings.Contains(css, ".task-output-list {\n  margin-top: 8px;\n  border-top: 1px dashed rgba(125, 154, 185, 0.32);\n  padding-top: 7px;\n  display: grid;\n  grid-template-columns: minmax(0, 1fr);") {
		t.Fatalf("expected task output list to clamp width to panel columns")
	}
	if !strings.Contains(css, ".task-meta > div {\n  min-width: 0;\n  overflow: hidden;\n  text-overflow: ellipsis;\n  white-space: nowrap;\n}") {
		t.Fatalf("expected task metadata rows to truncate instead of widening cards")
	}
	if !strings.Contains(css, ".task-meta > div + div::before {\n  content: \"|\";\n  position: absolute;\n  left: 4px;\n  color: var(--meta);\n}") {
		t.Fatalf("expected task metadata rows to display inline separators on wider layouts")
	}
	if !strings.Contains(css, ".task-meta {\n    flex-direction: column;\n    align-items: flex-start;\n    gap: 4px;\n  }") {
		t.Fatalf("expected task metadata rows to stack in compressed layouts")
	}
	if !strings.Contains(css, ".task-scroll {\n  scrollbar-width: thin;\n  scrollbar-color: var(--surface-scroll-thumb) rgba(17, 28, 42, 0.35);\n  overflow-x: hidden;\n}") {
		t.Fatalf("expected task scroll containers to hide horizontal overflow")
	}
	if !strings.Contains(css, ".task-fullscreen-shell {\n  position: relative;\n  display: grid;\n  grid-template-rows: minmax(0, 1fr);\n  width: 100%;\n  min-height: 100dvh;\n  height: 100dvh;\n}") {
		t.Fatalf("expected full screen shell to fill the dynamic viewport height")
	}
	if !strings.Contains(css, ".task-fullscreen-output-panel {\n  min-height: 0;\n  overflow: hidden;\n  display: grid;\n  grid-template-rows: auto auto minmax(0, 1fr);\n}") {
		t.Fatalf("expected full screen output panel to reserve the remaining viewport for terminal output")
	}
	if !strings.Contains(css, ".task-fullscreen-head-label {\n  min-width: 0;\n  flex: 1 1 auto;\n  white-space: normal;\n  overflow-wrap: anywhere;\n}") {
		t.Fatalf("expected full screen task headers to wrap long prompts instead of truncating")
	}
	if !strings.Contains(css, "#task-fullscreen-terminal {\n  min-height: 0;\n  height: 100%;\n  padding-right: 10px;\n  scrollbar-gutter: stable;\n  overscroll-behavior: contain;\n}") {
		t.Fatalf("expected full screen terminal to stabilize its vertical scrollbar gutter")
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
	if !strings.Contains(css, ".prompt-wrap .panel-header {\n  border-bottom-color: var(--surface-header-border);\n  background: var(--surface-header);\n  color: var(--surface-label);\n  letter-spacing: 0.11em;\n  justify-content: space-between;\n}") {
		t.Fatalf("expected Studio title bar to keep theme-aware styling while separating the section title from minimize controls")
	}
	if !strings.Contains(css, ".prompt-titlebar {\n  display: flex;\n  align-items: center;\n  gap: 12px;\n  justify-content: space-between;\n  min-height: 52px;\n}") {
		t.Fatalf("expected Studio title bar to keep a compact header while exposing both the section label and minimize control")
	}
	if !strings.Contains(css, ".page-bottom-dock {\n  position: fixed;\n  left: 50%;\n  bottom: max(16px, env(safe-area-inset-bottom));\n  z-index: 61;\n  display: flex;\n  align-items: center;\n  gap: 10px;\n  justify-content: center;\n  width: min-content;\n  max-width: calc(100vw - 28px);\n  transform: translateX(-50%);\n}") {
		t.Fatalf("expected Studio mode tabs to dock at the absolute bottom center of the page")
	}
	if !strings.Contains(css, "--hub-content-bottom-padding: calc(var(--hub-floating-bottom) + var(--hub-floating-stack-height) + var(--hub-studio-dock-gap) + 28px);") {
		t.Fatalf("expected Studio shell to compute bottom content clearance from floating dock stack tokens")
	}
	if !strings.Contains(css, ".app {\n  width: min(1500px, 100%);\n  margin: 0 auto;\n  padding: 28px 20px var(--hub-content-bottom-padding);\n  display: grid;\n  gap: 24px;\n}") {
		t.Fatalf("expected app shell to reserve dynamic bottom space for the floating dock")
	}
	if !strings.Contains(css, ".prompt-mode-tabs-dock {\n  width: max-content;\n  max-width: calc(100vw - 28px);\n  overflow: visible;\n}") {
		t.Fatalf("expected Studio mode tabs to remain scrollable within the bottom dock on narrow viewports")
	}
	if !strings.Contains(css, ".prompt-wrap.panel {\n  display: flex;\n  flex-direction: column;\n  position: relative;") {
		t.Fatalf("expected studio panel to participate in the page flow instead of docking itself to the viewport")
	}
	if !strings.Contains(css, ".prompt-mode-tabs {\n  display: inline-flex;\n  align-items: center;\n  gap: 6px;\n  padding: 8px 10px;\n  border-radius: var(--radius-card);\n  border: 1px solid var(--glass-border);\n  background: var(--glass-bg);") {
		t.Fatalf("expected studio mode tabs to use theme-aware segmented-control treatment")
	}
	if !strings.Contains(css, ".prompt-mode-link {\n  position: relative;\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;") {
		t.Fatalf("expected Studio mode controls to render as clickable icon pills inside the dock")
	}
	if !strings.Contains(css, ".prompt-mode-link.active {\n  color: var(--running);\n}") {
		t.Fatalf("expected active Studio mode link to stay visually highlighted without button sections")
	}
	if !strings.Contains(css, ".prompt-mode-link-icon img {\n  display: block;\n  width: 15px;\n  height: 15px;\n  object-fit: contain;\n  filter: var(--agent-logo-filter);\n}") {
		t.Fatalf("expected integrated dock icons to inherit the shared monochrome treatment")
	}
	if !strings.Contains(css, ".prompt-mode-link-logo {\n  min-width: 40px;\n  padding-inline: 0;\n}") {
		t.Fatalf("expected icon-only dock items to align with the main segmented menu")
	}
	if !strings.Contains(css, ".prompt-mode-divider {\n  width: 1px;\n  height: 24px;\n  margin-inline: 4px;") {
		t.Fatalf("expected the leading dock icon to share the segmented main-menu treatment instead of rendering as a detached control")
	}
	if !strings.Contains(css, ".prompt-form {\n  display: grid;\n  gap: 10px;\n  padding: 14px 14px 18px;\n  min-width: 0;\n  min-height: 0;\n  overflow-y: auto;\n}") {
		t.Fatalf("expected studio form content to use the full panel now that the mode dock lives outside it")
	}
	if !strings.Contains(css, ".prompt-compose-stack {\n  display: grid;\n  gap: 8px;\n  min-width: 0;\n}") {
		t.Fatalf("expected prompt panels and actions to keep a slight visual break below the prompt editor")
	}
	if !strings.Contains(css, ".prompt-field-repository {\n  flex: 1 1 320px;\n  min-width: 280px;\n}") {
		t.Fatalf("expected repository input to retain enough width beside the history selector")
	}
	if !strings.Contains(css, ".prompt-field-target-subdir {\n  flex: 0 1 clamp(75px, 8.5%, 110px);\n  max-width: clamp(90px, 9%, 110px);\n}") {
		t.Fatalf("expected directory input to use the reduced desktop width")
	}
	if !strings.Contains(css, ".prompt-actions {\n  display: flex;\n  align-items: center;\n  gap: 10px;\n  min-width: 0;\n}") {
		t.Fatalf("expected prompt actions to keep the status between screenshots and the right-aligned buttons")
	}
	if !strings.Contains(css, ".prompt-actions-start {\n  display: flex;\n  flex: 0 1 var(--prompt-paste-width, 25%);\n  width: min(100%, var(--prompt-paste-width, 25%));\n  max-width: 50%;\n  min-width: 0;\n}") {
		t.Fatalf("expected prompt actions to keep the screenshot lane between 25%% and 50%% of the row")
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
	if !strings.Contains(css, ".hub-setup-actions {\n  display: grid;\n  grid-template-columns: auto auto;\n  align-items: center;\n  column-gap: 14px;\n  row-gap: 10px;\n  width: auto;\n  margin-left: auto;\n}") {
		t.Fatalf("expected the profile modal action row to keep only the connection toggle and save button")
	}
	if !strings.Contains(css, ".hub-setup-status {\n  display: flex;\n  align-items: center;\n  justify-content: flex-start;\n  width: 100%;\n") {
		t.Fatalf("expected the profile modal status copy to render as a single left-aligned line")
	}
	if !strings.Contains(css, ".hub-setup-connection-toggle {\n  min-inline-size: 72px;\n") {
		t.Fatalf("expected the profile modal disconnect button to reuse the shared action button footprint")
	}
	if !strings.Contains(css, ".submit-status-inline {\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;\n  flex: 1 1 0;\n  min-width: 0;\n  text-align: center;\n  white-space: nowrap;\n  overflow: hidden;\n  text-overflow: ellipsis;\n  opacity: 0;\n  transition: opacity 220ms ease;\n}") {
		t.Fatalf("expected inline status to stay centered in the whitespace between screenshots and the action buttons")
	}
	if !strings.Contains(css, ".prompt-action-paste[data-empty=\"false\"] {\n  color: var(--text);\n}") {
		t.Fatalf("expected attached screenshot summaries to switch the paste target to active text styling")
	}
	if strings.Contains(css, ".prompt-image-chip {") {
		t.Fatalf("expected stacked screenshot chip styles to be removed")
	}
	if !strings.Contains(css, "@media (max-width: 700px) {\n  .page-bottom-dock {\n    bottom: max(12px, env(safe-area-inset-bottom));\n    max-width: calc(100vw - 24px);\n  }\n\n  .prompt-mode-tabs-dock {\n    max-width: calc(100vw - 24px);\n    overflow-x: auto;\n    overflow-y: visible;\n  }\n\n  .theme-toggle {\n    right: 12px;\n    bottom: 12px;\n    left: auto;\n  }\n\n  :root {\n    --hub-floating-bottom: max(12px, env(safe-area-inset-bottom));\n    --hub-floating-stack-height: 128px;\n    --hub-studio-dock-gap: 12px;\n  }") {
		t.Fatalf("expected mobile layout to coordinate the bottom dock stack and theme toggle spacing")
	}
	if !strings.Contains(css, "@media (max-width: 640px) {\n  .panel-section-copy {\n    max-width: none;\n  }\n\n  .prompt-actions {\n    flex-wrap: wrap;\n    gap: 6px;\n  }\n\n  .prompt-actions-start,\n  .submit-status-inline,\n  .prompt-actions-end {\n    flex: 1 1 100%;\n    width: 100%;\n  }\n\n  .prompt-actions-end {\n    justify-content: flex-end;\n    margin-left: 0;\n  }\n\n  .prompt-action-paste {\n    max-width: none;\n  }\n\n  .submit-status-inline {\n    min-width: 0;\n  }\n\n  .prompt-action-button {\n    flex: 1 1 0;\n    min-inline-size: 0;\n  }\n\n  .task-empty {\n    min-height: 0;\n    padding: 18px;\n  }") {
		t.Fatalf("expected mobile layout to keep Studio action controls fully visible")
	}
	if !strings.Contains(css, ".app {\n    padding: 20px 14px var(--hub-content-bottom-padding);\n  }") {
		t.Fatalf("expected compact layouts to keep the same dynamic dock clearance padding")
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
	if !strings.Contains(css, ".prompt-wrap.panel {\n  display: flex;\n  flex-direction: column;\n  position: relative;\n  left: auto;\n  right: auto;\n  bottom: auto;\n  z-index: auto;\n  width: 100%;\n  max-width: none;\n  max-height: none;") {
		t.Fatalf("expected studio panel to use theme-aware shell tokens")
	}
	if !strings.Contains(css, ".prompt-wrap .panel-header {\n  border-bottom-color: var(--surface-header-border);\n  background: var(--surface-header);\n  color: var(--surface-label);\n  letter-spacing: 0.11em;\n  justify-content: space-between;\n}") {
		t.Fatalf("expected studio header to separate its title from the minimize control in the stacked layout")
	}
	if !strings.Contains(css, ".prompt-mode-link.active {\n  color: var(--running);\n}") {
		t.Fatalf("expected active studio mode link to stay highlighted after removing button sections")
	}
	if !strings.Contains(css, ".prompt-control,\n.prompt-text,\n.prompt-action-paste {\n  width: 100%;\n  border: 1px solid var(--surface-control-border);\n  border-radius: 16px;\n  background: var(--surface-control-bg);") {
		t.Fatalf("expected studio controls to use theme-aware input tokens")
	}
	if !strings.Contains(css, "select.prompt-control {\n  appearance: none;\n  background-image:\n    linear-gradient(45deg, transparent 50%, var(--surface-control-arrow) 50%),\n    linear-gradient(135deg, var(--surface-control-arrow) 50%, transparent 50%);") {
		t.Fatalf("expected repository selector to use theme-aware chevron treatment")
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
	if !strings.Contains(css, ".status-item-compact {\n  position: relative;\n  justify-content: center;\n  gap: 0;\n  width: 42px;\n  min-width: 42px;\n  height: 42px;\n  min-height: 42px;\n  padding: 0;\n  overflow: hidden;") {
		t.Fatalf("expected compact status dots to stay clipped until the row expands them")
	}
	if !strings.Contains(css, ".header {\n  position: relative;\n  z-index: 5;") {
		t.Fatalf("expected header to create a higher stacking context above the studio panel")
	}
	if !strings.Contains(css, ".status-row:hover .status-item-compact,\n.status-row:focus-within .status-item-compact {\n  width: auto;\n  min-width: 42px;\n  padding-left: 11px;\n  padding-right: 11px;\n  gap: 8px;\n}") {
		t.Fatalf("expected connection status pills to slide open when the status row is hovered or focused")
	}
	if !strings.Contains(css, ".status-item-metrics {\n  gap: 12px;\n  padding-left: 12px;\n  padding-right: 14px;\n  min-height: 42px;\n  height: 42px;") {
		t.Fatalf("expected metrics pill to use stronger spacing and height")
	}
	if !strings.Contains(css, ".metric-copy {\n  display: inline-flex;\n  align-items: center;\n  min-width: 0;\n  overflow: hidden;\n}") {
		t.Fatalf("expected metrics pill to wrap labels, values, and units in an overflow-safe inline layout")
	}
	if !strings.Contains(css, ".status-row:hover .metric-label,\n.status-row:hover .metric-unit,\n.status-row:focus-within .metric-label,\n.status-row:focus-within .metric-unit {\n  max-width: 56px;\n  opacity: 1;\n  transform: translateX(0);\n}") {
		t.Fatalf("expected metric labels and units to reveal on status row hover")
	}
	if !strings.Contains(css, ".metric-unit-visible {\n  max-width: 56px;\n  opacity: 1;\n  overflow: visible;\n  transform: translateX(0);\n  margin-left: 3px;\n}") {
		t.Fatalf("expected the disk metrics suffix to remain visible without hovering the header")
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
	if !strings.Contains(html, "const codexFlow = authHarness(state.agentAuth) === \"codex\";") ||
		!strings.Contains(html, "agentAuthVerify.disabled = state.agentAuthBusy || state.agentAuthVerifyPending;") {
		t.Fatalf("expected Done button logic to keep Codex visible and lock it while verification is pending")
	}
	if !strings.Contains(html, "const visible = state.agentAuth.required && !state.agentAuth.ready;") {
		t.Fatalf("expected Codex Done button visibility to remain stable while auth is incomplete")
	}
	if !strings.Contains(html, "Codex auth is required. Open browser auth, then click Done again.") ||
		!strings.Contains(html, "Codex auth is still pending. Complete browser auth, then click Done again.") {
		t.Fatalf("expected Done verification to emit explicit Codex auth validation feedback")
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
	if !strings.Contains(html, "Authorize Agent to get started") {
		t.Fatalf("expected generic auth gate heading")
	}
	if !strings.Contains(html, "function agentAuthLabel(harness)") {
		t.Fatalf("expected auth gate labels to be harness-aware")
	}
	if !strings.Contains(html, "Setup Auggie") {
		t.Fatalf("expected auggie setup heading support")
	}
	if strings.Contains(html, "Auggie Configure") {
		t.Fatalf("expected legacy auggie configure heading to be removed")
	}
	if strings.Contains(html, "Run in your terminal locally") {
		t.Fatalf("expected legacy auggie configure instruction copy to be removed")
	}
	if strings.Contains(html, "Paste Auggie session JSON") {
		t.Fatalf("expected legacy auggie configure label copy to be removed")
	}
	if !strings.Contains(html, "id=\"agent-auth-configure\"") {
		t.Fatalf("expected auggie configure panel markup")
	}
	if !strings.Contains(html, "class=\"agent-auth-shell flex min-h-[220px] w-full max-w-xl flex-col\"") {
		t.Fatalf("expected auth gate content to render inside a theme-aware auth shell")
	}
	if !strings.Contains(html, "normalizeAuggieSessionAuthPayload") {
		t.Fatalf("expected auggie configure JSON schema validator")
	}
	if !strings.Contains(html, "function isGitHubTokenConfigureState(auth)") {
		t.Fatalf("expected auth gate to detect GitHub token configure flows across harnesses")
	}
	if !strings.Contains(html, "id=\"agent-auth-url-logo\"") {
		t.Fatalf("expected auth gate to include Claude logo link element")
	}
	if !strings.Contains(html, "id=\"agent-auth-browser-code-input\"") {
		t.Fatalf("expected auth gate to include Claude browser-code input")
	}
	if !strings.Contains(html, "id=\"agent-auth-browser-command-primary\"") ||
		!strings.Contains(html, "id=\"agent-auth-browser-command-primary-copy\"") {
		t.Fatalf("expected auth gate to include Claude primary command copy controls")
	}
	if !strings.Contains(html, "id=\"agent-auth-browser-command-secondary\"") ||
		!strings.Contains(html, "id=\"agent-auth-browser-command-secondary-copy\"") {
		t.Fatalf("expected auth gate to include Claude credentials command copy controls")
	}
	if !strings.Contains(html, "function isClaudeBrowserCodeState(auth)") {
		t.Fatalf("expected auth gate to detect Claude browser-code submission state")
	}
	if strings.Contains(html, "(!requiresClaudeBrowserCode || hasClaudeBrowserCode)") {
		t.Fatalf("expected Done button visibility to allow verify flows even when Claude browser code is not pasted")
	}
	if strings.Contains(html, "!claudeBrowserCodeSubmitted") {
		t.Fatalf("expected Done button to remain available after Claude browser code submission for browser-login verification retries")
	}
	if !strings.Contains(html, "claude_auth_code: code") {
		t.Fatalf("expected Claude browser code configure payload support in auth UI")
	}
	if strings.Contains(html, `id="agent-auth-configure-option"`) ||
		!strings.Contains(html, "pi_auth_json: authJSON") ||
		!strings.Contains(html, "cat ~/.pi/agent/auth.json") ||
		!strings.Contains(html, "function isPiConfigureState(auth) {") ||
		!strings.Contains(html, `return authHarness(current) === "pi" && isManualConfigureState(current) && !isGitHubTokenConfigureState(current);`) {
		t.Fatalf("expected PI auth.json configure support in auth UI")
	}
	if !strings.Contains(html, "if (isClaudePendingBrowserLoginState()) {") ||
		!strings.Contains(html, "const code = claudeBrowserCodeValue();") ||
		!strings.Contains(html, "if (code !== \"\") {") {
		t.Fatalf("expected Done handler to submit Claude browser code only when a code is provided")
	}
}

func TestAuthGateVerifyButtonUsesReadableContrastToken(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	css := resp.Body.String()
	if !strings.Contains(css, "--surface-auth-verify-text: #ffffff;") {
		t.Fatalf("expected auth verify button to define a dedicated readable foreground token")
	}
	if !strings.Contains(css, ".agent-auth-verify {\n  border-color: var(--border);\n  background: var(--surface-auth-verify-bg);\n  color: var(--surface-auth-verify-text);\n}") {
		t.Fatalf("expected auth verify button to use the readable foreground token")
	}
}
