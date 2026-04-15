package hubui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type noFlushResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *noFlushResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *noFlushResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *noFlushResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func decodeJSONMap(t *testing.T, body *bytes.Buffer) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.NewDecoder(bytes.NewReader(body.Bytes())).Decode(&decoded); err != nil {
		t.Fatalf("decode json body error = %v (body=%q)", err, body.String())
	}
	return decoded
}

func TestServerGitHubProfileAndIndexErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/github/profile", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/github/profile status = %d, want 405", resp.Code)
	}

	srv.ResolveGitHubProfileURL = func(context.Context) (string, error) {
		return "", errors.New("profile lookup failed")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/github/profile", nil)
	resp = httptest.NewRecorder()
	handler = srv.Handler()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/github/profile status = %d, want 200", resp.Code)
	}
	body := decodeJSONMap(t, resp.Body)
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("github profile error response ok = %#v, want false", body["ok"])
	}
	if got, _ := body["error"].(string); !strings.Contains(got, "profile lookup failed") {
		t.Fatalf("github profile error = %q, want profile lookup failure", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("GET /does-not-exist status = %d, want 404", resp.Code)
	}
}

func TestServerHubSetupErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPut, "/api/hub-setup", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/hub-setup status = %d, want 405", resp.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/hub-setup", strings.NewReader(`{"token":"abc"}`))
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("POST /api/hub-setup (nil configure) status = %d, want 501", resp.Code)
	}

	srv.ConfigureHubSetup = func(context.Context, HubSetupRequest) (HubSetupState, error) {
		return defaultHubSetupState(), errors.New("invalid token")
	}
	handler = srv.Handler()

	req = httptest.NewRequest(http.MethodPost, "/api/hub-setup", strings.NewReader("{"))
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/hub-setup malformed status = %d, want 400", resp.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/hub-setup", strings.NewReader(`{"token":"abc","agent_mode":"existing","token_type":"agent","region":"na"}`))
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/hub-setup configure error status = %d, want 400", resp.Code)
	}

	srv.HubSetupStatus = func(context.Context) (HubSetupState, error) {
		return defaultHubSetupState(), errors.New("state unavailable")
	}
	handler = srv.Handler()
	req = httptest.NewRequest(http.MethodGet, "/api/hub-setup", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/hub-setup status = %d, want 500", resp.Code)
	}
}

func TestServerHubConnectDisconnectErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/hub-setup/connect", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/hub-setup/connect status = %d, want 405", resp.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/hub-setup/connect", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("POST /api/hub-setup/connect (nil callback) status = %d, want 501", resp.Code)
	}

	srv.ConnectHubSetup = func(context.Context) (HubSetupState, error) {
		return defaultHubSetupState(), errors.New("connect failed")
	}
	handler = srv.Handler()
	req = httptest.NewRequest(http.MethodPost, "/api/hub-setup/connect", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/hub-setup/connect error status = %d, want 400", resp.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/hub-setup/disconnect", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/hub-setup/disconnect status = %d, want 405", resp.Code)
	}

	srv.DisconnectHubSetup = func(context.Context) (HubSetupState, error) {
		return defaultHubSetupState(), errors.New("disconnect failed")
	}
	handler = srv.Handler()
	req = httptest.NewRequest(http.MethodPost, "/api/hub-setup/disconnect", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/hub-setup/disconnect error status = %d, want 400", resp.Code)
	}
}

func TestServerAgentAuthStartVerifyErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/agent-auth/start-device", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/agent-auth/start-device status = %d, want 405", resp.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/agent-auth/start-device", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("POST /api/agent-auth/start-device (nil callback) status = %d, want 501", resp.Code)
	}

	srv.StartAgentAuth = func(context.Context) (AgentAuthState, error) {
		return AgentAuthState{State: "needs_device_auth"}, errors.New("auth start failed")
	}
	handler = srv.Handler()
	req = httptest.NewRequest(http.MethodPost, "/api/agent-auth/start-device", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/agent-auth/start-device error status = %d, want 400", resp.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/agent-auth/verify", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/agent-auth/verify status = %d, want 405", resp.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/agent-auth/verify", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("POST /api/agent-auth/verify (nil callback) status = %d, want 501", resp.Code)
	}

	srv.VerifyAgentAuth = func(context.Context) (AgentAuthState, error) {
		return AgentAuthState{State: "needs_device_auth"}, errors.New("auth verify failed")
	}
	handler = srv.Handler()
	req = httptest.NewRequest(http.MethodPost, "/api/agent-auth/verify", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/agent-auth/verify error status = %d, want 400", resp.Code)
	}
}

func TestServerStreamAndHubStateHelperErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/stream with nil broker status = %d, want 503", resp.Code)
	}

	srv = NewServer("", NewBroker())
	noFlush := &noFlushResponseWriter{}
	srv.handleStream(noFlush, httptest.NewRequest(http.MethodGet, "/api/stream", nil))
	if noFlush.status != http.StatusInternalServerError {
		t.Fatalf("handleStream(no flusher) status = %d, want 500", noFlush.status)
	}

	srv = NewServer("", NewBroker())
	state, err := srv.currentHubSetupState(context.Background())
	if err != nil {
		t.Fatalf("currentHubSetupState(default) error = %v", err)
	}
	if state.ConnectURL == "" || state.DashboardURL == "" || state.AgentMode == "" || state.TokenType == "" {
		t.Fatalf("currentHubSetupState(default) = %#v, want default URLs and mode fields", state)
	}

	srv.HubSetupStatus = func(context.Context) (HubSetupState, error) {
		return HubSetupState{}, errors.New("status failed")
	}
	state, err = srv.currentHubSetupState(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status failed") {
		t.Fatalf("currentHubSetupState(error) err = %v, want status failure", err)
	}
	if state.ConnectURL == "" || state.DashboardURL == "" || state.AgentMode == "" || state.TokenType == "" {
		t.Fatalf("currentHubSetupState(error) missing default fallbacks: %#v", state)
	}
}
