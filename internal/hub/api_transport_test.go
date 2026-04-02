package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPullOpenClawMessageParsesResult(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/openclaw/messages/pull" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("timeout_ms"); got != "20000" {
			t.Fatalf("timeout_ms = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"delivery_id":"d1","openclaw_message":{"message":{"type":"skill_request","skill":"codex_harness_run","config":{"repo":"git@github.com:acme/repo.git","prompt":"x"}}}}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	pulled, found, err := client.PullOpenClawMessage(context.Background(), "token", 20000)
	if err != nil {
		t.Fatalf("PullOpenClawMessage() error = %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if pulled.DeliveryID != "d1" {
		t.Fatalf("DeliveryID = %q", pulled.DeliveryID)
	}
	if got := pulled.Message["skill"]; got != "codex_harness_run" {
		t.Fatalf("message.skill = %#v", got)
	}
}

func TestPullOpenClawMessageNoContent(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	_, found, err := client.PullOpenClawMessage(context.Background(), "token", 15000)
	if err != nil {
		t.Fatalf("PullOpenClawMessage() error = %v", err)
	}
	if found {
		t.Fatal("found = true, want false")
	}
}

func TestPullOpenClawMessageEmptyResultIsNoMessage(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	_, found, err := client.PullOpenClawMessage(context.Background(), "token", 15000)
	if err != nil {
		t.Fatalf("PullOpenClawMessage() error = %v", err)
	}
	if found {
		t.Fatal("found = true, want false")
	}
}

func TestPublishResultUsesOpenClawEnvelope(t *testing.T) {
	t.Parallel()

	type captured struct {
		Path string
		Body map[string]any
	}
	var got captured

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		got = captured{Path: r.URL.Path, Body: body}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"queued"}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	payload := map[string]any{
		"type":       "skill_result",
		"request_id": "req-7",
		"reply_to":   "agent-123",
		"status":     "ok",
		"result":     map[string]any{"ok": true},
	}
	if err := client.PublishResult(context.Background(), "token", payload); err != nil {
		t.Fatalf("PublishResult() error = %v", err)
	}

	if got.Path != "/v1/openclaw/messages/publish" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.Body["to_agent_uuid"] != "agent-123" {
		t.Fatalf("to_agent_uuid = %#v", got.Body["to_agent_uuid"])
	}
	if got.Body["client_msg_id"] != "req-7" {
		t.Fatalf("client_msg_id = %#v", got.Body["client_msg_id"])
	}
	msg, ok := got.Body["message"].(map[string]any)
	if !ok {
		t.Fatalf("message missing: %#v", got.Body)
	}
	if msg["type"] != "skill_result" {
		t.Fatalf("message.type = %#v", msg["type"])
	}
}

func TestAckAndNackOpenClawDelivery(t *testing.T) {
	t.Parallel()

	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"ok"}}`))
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.AckOpenClawDelivery(context.Background(), "token", "d-1"); err != nil {
		t.Fatalf("AckOpenClawDelivery() error = %v", err)
	}
	if err := client.NackOpenClawDelivery(context.Background(), "token", "d-2"); err != nil {
		t.Fatalf("NackOpenClawDelivery() error = %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0] != "/v1/openclaw/messages/ack" {
		t.Fatalf("ack path = %q", calls[0])
	}
	if calls[1] != "/v1/openclaw/messages/nack" {
		t.Fatalf("nack path = %q", calls[1])
	}
}
