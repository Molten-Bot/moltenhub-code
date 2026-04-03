package hubui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerStateEndpointReturnsSnapshot(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-1")

	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].RequestID != "req-1" {
		t.Fatalf("request id = %q", snap.Tasks[0].RequestID)
	}
}

func TestHandlerStreamEndpointEmitsInitialSnapshot(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-stream")

	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatalf("GET /api/stream error = %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read stream line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first line = %q", line)
	}
}

func TestHandlerIndexServesHTML(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	markup := string(body)
	if !strings.Contains(markup, ".task-close") {
		t.Fatalf("expected index html to include task close styles")
	}
	if !strings.Contains(markup, ".task-rerun") {
		t.Fatalf("expected index html to include task rerun styles")
	}
	if !strings.Contains(markup, "function dismissTask(") {
		t.Fatalf("expected index html to include dismissTask handler")
	}
	if !strings.Contains(markup, "function rerunTask(") {
		t.Fatalf("expected index html to include rerunTask handler")
	}
	if !strings.Contains(markup, ".task-progress") {
		t.Fatalf("expected index html to include task progress styles")
	}
	if !strings.Contains(markup, "function renderTaskProgress(") {
		t.Fatalf("expected index html to include renderTaskProgress handler")
	}
}

func TestHandlerLocalPromptSubmitAccepted(t *testing.T) {
	t.Parallel()

	var gotBody string
	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-123", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"repo":"git@github.com:acme/repo.git","base_branch":"main","target_subdir":".","prompt":"update docs"}`
	resp, err := http.Post(ts.URL+"/api/local-prompt", "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
	if requestID, _ := body["request_id"].(string); requestID != "local-123" {
		t.Fatalf("request_id = %q", requestID)
	}
}

func TestHandlerLocalPromptSubmitUnavailable(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/local-prompt", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}

func TestHandlerLocalPromptMethodNotAllowed(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/local-prompt")
	if err != nil {
		t.Fatalf("GET /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}

func TestHandlerTaskRerunAccepted(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-100"
	payload := `{"repo":"git@github.com:acme/repo.git","base_branch":"main","target_subdir":".","prompt":"rerun this"}`
	b.RecordTaskRunConfig(requestID, []byte(payload))

	var gotBody string
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-456", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/"+requestID+"/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/%s/rerun error = %v", requestID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
	if gotRequestID, _ := body["request_id"].(string); gotRequestID != "local-456" {
		t.Fatalf("request_id = %q, want %q", gotRequestID, "local-456")
	}
	if gotRerunOf, _ := body["rerun_of"].(string); gotRerunOf != requestID {
		t.Fatalf("rerun_of = %q, want %q", gotRerunOf, requestID)
	}
}

func TestHandlerTaskRerunUnavailable(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.RecordTaskRunConfig("req-1", []byte(`{"repo":"x","prompt":"x"}`))
	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-1/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-1/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}

func TestHandlerTaskRerunMissingConfig(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		return "local-777", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-missing/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-missing/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandlerTaskRerunMethodNotAllowed(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.RecordTaskRunConfig("req-2", []byte(`{"repo":"x","prompt":"x"}`))
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		return "local-789", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/tasks/req-2/rerun")
	if err != nil {
		t.Fatalf("GET /api/tasks/req-2/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}
