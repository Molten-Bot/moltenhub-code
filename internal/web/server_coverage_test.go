package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type nonFlushingResponseWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *nonFlushingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *nonFlushingResponseWriter) Write(payload []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.body.Write(payload)
}

func (w *nonFlushingResponseWriter) WriteHeader(statusCode int) {
	w.code = statusCode
}

type flushingRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (w *flushingRecorder) Flush() {
	w.flushed = true
}

func TestHandleStateAndStreamErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", nil)

	rec := httptest.NewRecorder()
	srv.handleState(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleState(nil broker) status = %d, want 503", rec.Code)
	}

	noFlush := &nonFlushingResponseWriter{}
	srv.Broker = NewBroker()
	srv.handleStream(noFlush, httptest.NewRequest(http.MethodGet, "/api/stream", nil))
	if noFlush.code != http.StatusInternalServerError {
		t.Fatalf("handleStream(non flusher) status = %d, want 500", noFlush.code)
	}
}

func TestGzipResponseWriterFlushAndHeaderHelpers(t *testing.T) {
	t.Parallel()

	rec := &flushingRecorder{ResponseRecorder: httptest.NewRecorder()}
	gzw := &gzipResponseWriter{ResponseWriter: rec, writer: nil}
	gzw.Flush()
	if !rec.flushed {
		t.Fatal("Flush() did not flush underlying ResponseWriter")
	}

	header := make(http.Header)
	addVaryHeader(header, "Accept-Encoding")
	addVaryHeader(header, "accept-encoding")
	if got := header.Values("Vary"); len(got) != 1 {
		t.Fatalf("len(Vary) = %d, want 1 (%v)", len(got), got)
	}
	addVaryHeader(header, " ")
	if got := header.Values("Vary"); len(got) != 1 {
		t.Fatalf("blank vary value mutated header: %v", got)
	}
}

func TestRequestAndPathCompressionHelpersAndFirstNonEmptyString(t *testing.T) {
	t.Parallel()

	if requestWantsGzip(nil) {
		t.Fatal("requestWantsGzip(nil) = true, want false")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	if !requestWantsGzip(req) {
		t.Fatal("requestWantsGzip(gzip) = false, want true")
	}
	if isCompressiblePath(" ") {
		t.Fatal("isCompressiblePath(blank) = true, want false")
	}

	if got := firstNonEmptyString("", " ", "\t"); got != "" {
		t.Fatalf("firstNonEmptyString(all blank) = %q, want empty", got)
	}
}

func TestAgentAuthStatusAndConfigureErrorPaths(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.AgentAuthStatus = func(context.Context) (AgentAuthState, error) {
		return AgentAuthState{Ready: false}, errors.New("status unavailable")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agent-auth", nil)
	srv.handleAgentAuthStatus(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("handleAgentAuthStatus(error) status = %d, want 500", rec.Code)
	}

	srv.ConfigureAgentAuth = func(context.Context, string) (AgentAuthState, error) {
		return AgentAuthState{}, errors.New("configure failed")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/agent-auth/configure", bytes.NewBufferString(`{"value":"token"}`))
	srv.handleAgentAuthConfigure(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleAgentAuthConfigure(callback error) status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/agent-auth/configure", bytes.NewBufferString(`{"value":" "}`))
	srv.handleAgentAuthConfigure(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleAgentAuthConfigure(empty value) status = %d, want 400", rec.Code)
	}
}

func TestCurrentAgentAuthStateFillsDefaultStateFromReadyFlag(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.AgentAuthStatus = func(context.Context) (AgentAuthState, error) {
		return AgentAuthState{Ready: true, State: ""}, nil
	}
	state, err := srv.currentAgentAuthState(context.Background())
	if err != nil {
		t.Fatalf("currentAgentAuthState() error = %v", err)
	}
	if state.State != "ready" {
		t.Fatalf("state = %+v, want ready state", state)
	}

	srv.AgentAuthStatus = func(context.Context) (AgentAuthState, error) {
		return AgentAuthState{Ready: false, State: ""}, nil
	}
	state, err = srv.currentAgentAuthState(context.Background())
	if err != nil {
		t.Fatalf("currentAgentAuthState() error = %v", err)
	}
	if state.State != "needs_device_auth" {
		t.Fatalf("state = %+v, want needs_device_auth", state)
	}

	srv.AgentAuthStatus = nil
	state, err = srv.currentAgentAuthState(context.Background())
	if err != nil {
		t.Fatalf("currentAgentAuthState(nil status) error = %v", err)
	}
	if !state.Ready || state.State != "ready" {
		t.Fatalf("currentAgentAuthState(nil status) = %+v", state)
	}
}

func TestHandleAgentAuthConfigureDecodeFailure(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfigureAgentAuth = func(context.Context, string) (AgentAuthState, error) {
		return AgentAuthState{Ready: true, State: "ready"}, nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agent-auth/configure", bytes.NewBufferString(`{"value"`))
	srv.handleAgentAuthConfigure(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("decode failure status = %d, want 400", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("ok = true, want false (%v)", body)
	}
}
