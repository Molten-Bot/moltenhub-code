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
		_, _ = w.Write([]byte(`{"ok":true,"result":{"delivery_id":"d1","openclaw_message":{"message":{"type":"skill_request","skill":"moltenhub_code_run","config":{"repo":"git@github.com:acme/repo.git","prompt":"x"}}}}}`))
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
	if got := pulled.Message["skill"]; got != "moltenhub_code_run" {
		t.Fatalf("message.skill = %#v", got)
	}
}

func TestPullOpenClawMessageParsesNestedDeliveryAndPrefersOpenClawEnvelope(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/openclaw/messages/pull" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": {
				"status": "leased",
				"delivery": {
					"delivery_id": "d-nested",
					"message_id": "m-nested"
				},
				"message": {
					"message_id": "raw-1",
					"content_type": "application/json",
					"payload": "{\"foo\":\"bar\"}"
				},
				"openclaw_message": {
					"kind": "skill_request",
					"skill_name": "code_for_me",
					"request_id": "req-9",
					"input": {
						"repo": "git@github.com:acme/repo.git",
						"prompt": "x"
					}
				}
			}
		}`))
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
	if pulled.DeliveryID != "d-nested" {
		t.Fatalf("DeliveryID = %q", pulled.DeliveryID)
	}
	if pulled.MessageID != "m-nested" {
		t.Fatalf("MessageID = %q", pulled.MessageID)
	}
	if got := pulled.Message["skill_name"]; got != "code_for_me" {
		t.Fatalf("message.skill_name = %#v", got)
	}
	if _, hasRaw := pulled.Message["content_type"]; hasRaw {
		t.Fatalf("expected parsed message to prefer openclaw envelope over raw message transport map")
	}
}

func TestExtractInboundOpenClawMessageForWebsocketEnvelope(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"status": "received",
		"openclaw_message": map[string]any{
			"kind":       "skill_request",
			"skill_name": "code_for_me",
			"request_id": "req-ws",
			"input": map[string]any{
				"repo":   "git@github.com:acme/repo.git",
				"prompt": "x",
			},
		},
	}

	got := extractInboundOpenClawMessage(root)
	if got.DeliveryID != "" {
		t.Fatalf("DeliveryID = %q, want empty", got.DeliveryID)
	}
	if skill := got.Message["skill_name"]; skill != "code_for_me" {
		t.Fatalf("message.skill_name = %#v", skill)
	}
}

func TestExtractInboundOpenClawMessageCopiesReplyRoutingFromTransportMessage(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"result": map[string]any{
			"delivery": map[string]any{
				"delivery_id": "d-22",
			},
			"message": map[string]any{
				"from_agent_uri":  "https://na.hub.molten.bot/acme/sender",
				"from_agent_uuid": "8b9bc0a9-3e36-49aa-a097-7ba8fe5f0b18",
			},
			"openclaw_message": map[string]any{
				"kind":       "skill_request",
				"skill_name": "code_for_me",
				"request_id": "req-ws-2",
				"input": `{
					"repo":"git@github.com:acme/repo.git",
					"prompt":"x"
				}`,
			},
		},
	}

	got := extractInboundOpenClawMessage(root)
	if got.DeliveryID != "d-22" {
		t.Fatalf("DeliveryID = %q", got.DeliveryID)
	}
	if replyTo := got.Message["reply_to"]; replyTo != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("message.reply_to = %#v", replyTo)
	}
	if fromURI := got.Message["from_agent_uri"]; fromURI != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("message.from_agent_uri = %#v", fromURI)
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

func TestPublishResultUsesURIReplyTarget(t *testing.T) {
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
		"request_id": "req-uri",
		"reply_to":   "https://na.hub.molten.bot/acme/sender",
		"status":     "error",
		"result":     map[string]any{"error": "bad payload"},
	}
	if err := client.PublishResult(context.Background(), "token", payload); err != nil {
		t.Fatalf("PublishResult() error = %v", err)
	}

	if got.Path != "/v1/openclaw/messages/publish" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.Body["to_agent_uri"] != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("to_agent_uri = %#v", got.Body["to_agent_uri"])
	}
	if _, hasUUID := got.Body["to_agent_uuid"]; hasUUID {
		t.Fatalf("unexpected to_agent_uuid in body: %#v", got.Body["to_agent_uuid"])
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
