package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
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

func TestPullOpenClawMessageTimeoutRespectsOpenAPIBounds(t *testing.T) {
	t.Parallel()

	var observedQueries []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedQueries = append(observedQueries, r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	for _, timeoutMs := range []int{-1, 0, 5, 35000} {
		if _, _, err := client.PullOpenClawMessage(context.Background(), "token", timeoutMs); err != nil {
			t.Fatalf("PullOpenClawMessage(timeoutMs=%d) error = %v", timeoutMs, err)
		}
	}

	want := []string{
		"",
		"",
		"timeout_ms=5",
		"timeout_ms=30000",
	}
	if !reflect.DeepEqual(observedQueries, want) {
		t.Fatalf("pull timeout queries = %#v, want %#v", observedQueries, want)
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

func TestExtractInboundOpenClawMessageCopiesReplyRoutingFromOpenClawWrapper(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"result": map[string]any{
			"delivery": map[string]any{
				"delivery_id": "d-24",
			},
			"openclaw_message": map[string]any{
				"from_agent_uri":  "https://na.hub.molten.bot/acme/wrapper-sender",
				"from_agent_uuid": "de14de6e-c4f5-4f5c-9d83-fcb87d4d6dc4",
				"message": map[string]any{
					"type":  "skill_request",
					"skill": "code_for_me",
					"config": map[string]any{
						"repo":   "git@github.com:acme/repo.git",
						"prompt": "fix",
					},
				},
			},
		},
	}

	got := extractInboundOpenClawMessage(root)
	if got.DeliveryID != "d-24" {
		t.Fatalf("DeliveryID = %q", got.DeliveryID)
	}
	if replyTo := got.Message["reply_to"]; replyTo != "https://na.hub.molten.bot/acme/wrapper-sender" {
		t.Fatalf("message.reply_to = %#v", replyTo)
	}
	if fromURI := got.Message["from_agent_uri"]; fromURI != "https://na.hub.molten.bot/acme/wrapper-sender" {
		t.Fatalf("message.from_agent_uri = %#v", fromURI)
	}

	dispatch, matched, err := ParseSkillDispatch(got.Message, "skill_request", "code_for_me")
	if err != nil {
		t.Fatalf("ParseSkillDispatch() error = %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true")
	}
	if got, want := dispatch.ReplyTo, "https://na.hub.molten.bot/acme/wrapper-sender"; got != want {
		t.Fatalf("dispatch.ReplyTo = %q, want %q", got, want)
	}
}

func TestExtractInboundOpenClawMessageUsesClientMsgIDAndIgnoresGenericEnvelopeID(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"result": map[string]any{
			"delivery": map[string]any{
				"delivery_id": "d-23",
			},
			"message": map[string]any{
				"client_msg_id": "client-msg-23",
			},
			"openclaw_message": map[string]any{
				"type":  "skill_request",
				"skill": "code_for_me",
				"id":    "sender-agent-static-id",
				"input": map[string]any{
					"repo":   "git@github.com:acme/repo.git",
					"prompt": "x",
				},
			},
		},
	}

	got := extractInboundOpenClawMessage(root)
	if got.DeliveryID != "d-23" {
		t.Fatalf("DeliveryID = %q", got.DeliveryID)
	}
	if got.MessageID != "client-msg-23" {
		t.Fatalf("MessageID = %q, want client-msg-23", got.MessageID)
	}
	if corr := got.Message["client_msg_id"]; corr != "client-msg-23" {
		t.Fatalf("message.client_msg_id = %#v, want client-msg-23", corr)
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

func TestPublishResultUsesA2ASendMessage(t *testing.T) {
	t.Parallel()

	type captured struct {
		Path string
		Auth string
		Body map[string]any
	}
	var got captured

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		got = captured{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      body["id"],
			"result": map[string]any{
				"task": map[string]any{
					"id":        "a2a-task-1",
					"contextId": "req-7",
					"status": map[string]any{
						"state": "TASK_STATE_SUBMITTED",
					},
				},
			},
		})
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

	if got.Path != "/v1/a2a" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.Auth != "Bearer token" {
		t.Fatalf("Authorization = %q", got.Auth)
	}
	params, ok := got.Body["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing: %#v", got.Body)
	}
	if target := readMapString(params["metadata"], "to_agent_id"); target != "agent-123" {
		t.Fatalf("metadata.to_agent_id = %#v", target)
	}
	msg, ok := params["message"].(map[string]any)
	if !ok {
		t.Fatalf("message missing: %#v", params)
	}
	if msg["messageId"] != "req-7" {
		t.Fatalf("messageId = %#v", msg["messageId"])
	}
	if target := readMapString(msg["metadata"], "to_agent_id"); target != "agent-123" {
		t.Fatalf("message.metadata.to_agent_id = %#v", target)
	}
	parts, _ := msg["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts = %#v", msg["parts"])
	}
	part, _ := parts[0].(map[string]any)
	if part["mediaType"] != a2aResultPartMediaType {
		t.Fatalf("part.mediaType = %#v", part["mediaType"])
	}
	var resultPayload map[string]any
	if err := json.Unmarshal([]byte(readMapString(part, "text")), &resultPayload); err != nil {
		t.Fatalf("part.text JSON decode error = %v", err)
	}
	if resultPayload["type"] != "skill_result" {
		t.Fatalf("result payload type = %#v", resultPayload["type"])
	}
}

func TestPublishResultUsesTargetedA2AEndpointAndTaskFields(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      body["id"],
			"result": map[string]any{
				"task": map[string]any{
					"id":        "reply-task",
					"contextId": "a2a-context-1",
					"status": map[string]any{
						"state": "TASK_STATE_SUBMITTED",
					},
				},
			},
		})
	}))
	defer ts.Close()

	targetUUID := "123e4567-e89b-12d3-a456-426614174000"
	client := NewAPIClient(ts.URL + "/v1")
	payload := map[string]any{
		"type":           "skill_result",
		"request_id":     "req-7",
		"to_agent_uuid":  targetUUID,
		"a2a_task_id":    "a2a-task-1",
		"a2a_context_id": "a2a-context-1",
		"status":         "ok",
		"result":         map[string]any{"ok": true},
	}
	if err := client.PublishResult(context.Background(), "token", payload); err != nil {
		t.Fatalf("PublishResult() error = %v", err)
	}

	if got.Path != "/v1/a2a/agents/"+targetUUID {
		t.Fatalf("path = %q", got.Path)
	}
	params, ok := got.Body["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing: %#v", got.Body)
	}
	msg, ok := params["message"].(map[string]any)
	if !ok {
		t.Fatalf("message missing: %#v", params)
	}
	if msg["taskId"] != "a2a-task-1" {
		t.Fatalf("message.taskId = %#v", msg["taskId"])
	}
	if msg["contextId"] != "a2a-context-1" {
		t.Fatalf("message.contextId = %#v", msg["contextId"])
	}
}

func TestPublishResultFallsBackToOpenClaw(t *testing.T) {
	t.Parallel()

	type captured struct {
		Path string
		Body map[string]any
	}
	var calls []captured

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		calls = append(calls, captured{Path: r.URL.Path, Body: body})
		if r.URL.Path == "/v1/a2a" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
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

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Path != "/v1/a2a" {
		t.Fatalf("first path = %q", calls[0].Path)
	}
	if calls[1].Path != "/v1/openclaw/messages/publish" {
		t.Fatalf("second path = %q", calls[1].Path)
	}
	if calls[1].Body["to_agent_id"] != "agent-123" {
		t.Fatalf("to_agent_id = %#v", calls[1].Body["to_agent_id"])
	}
	if calls[1].Body["client_msg_id"] != "req-7" {
		t.Fatalf("client_msg_id = %#v", calls[1].Body["client_msg_id"])
	}
	msg, ok := calls[1].Body["message"].(map[string]any)
	if !ok {
		t.Fatalf("message missing: %#v", calls[1].Body)
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      body["id"],
			"result": map[string]any{
				"task": map[string]any{
					"id": "a2a-task-uri",
					"status": map[string]any{
						"state": "TASK_STATE_SUBMITTED",
					},
				},
			},
		})
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

	if got.Path != "/v1/a2a" {
		t.Fatalf("path = %q", got.Path)
	}
	params, ok := got.Body["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing: %#v", got.Body)
	}
	if target := readMapString(params["metadata"], "to_agent_uri"); target != "https://na.hub.molten.bot/acme/sender" {
		t.Fatalf("metadata.to_agent_uri = %#v", target)
	}
	if _, hasUUID := params["metadata"].(map[string]any)["to_agent_uuid"]; hasUUID {
		t.Fatalf("unexpected to_agent_uuid in metadata: %#v", params["metadata"])
	}
}

func TestRegisterRuntimePublishesLibraryTaskMetadata(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var calls []captured
	client := NewAPIClient("http://example.test/v1")
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			if r.Body != nil {
				defer r.Body.Close()
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &body)
			}
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
			if r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"agent":{"metadata":{"existing":"keep"}}}}`)),
					Request:    r,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    r,
			}, nil
		}),
	}
	cfg := InitConfig{}
	cfg.ApplyDefaults()
	if err := client.RegisterRuntime(context.Background(), "token", cfg, []library.TaskSummary{
		{Name: "security-review", Description: "Audit the repository."},
		{Name: "unit-test-coverage"},
	}); err != nil {
		t.Fatalf("RegisterRuntime() error = %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Method != http.MethodGet || calls[0].Path != "/v1/agents/me" {
		t.Fatalf("first call = %s %s, want GET /v1/agents/me", calls[0].Method, calls[0].Path)
	}
	if calls[1].Method != http.MethodPatch || calls[1].Path != "/v1/agents/me/metadata" {
		t.Fatalf("second call = %s %s, want PATCH /v1/agents/me/metadata", calls[1].Method, calls[1].Path)
	}

	meta, ok := calls[1].Body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata wrapper missing: %#v", calls[1].Body)
	}
	if got := meta["existing"]; got != "keep" {
		t.Fatalf("existing metadata = %#v, want preserved", got)
	}
	if got := meta["agent_harness"]; got != "codex" {
		t.Fatalf("agent_harness = %#v", got)
	}
	if got := meta["llm"]; got != "Codex" {
		t.Fatalf("llm = %#v, want Codex", got)
	}
	skills, ok := meta["skills"].([]any)
	if !ok || len(skills) != 2 {
		t.Fatalf("skills = %#v, want two advertised skills", meta["skills"])
	}
	firstSkill, ok := skills[0].(map[string]any)
	if !ok {
		t.Fatalf("skills[0] = %#v, want map[string]any", skills[0])
	}
	parameters, ok := firstSkill["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("skills[0].parameters = %#v, want map[string]any", firstSkill["parameters"])
	}
	if got := parameters["format"]; got != "json" {
		t.Fatalf("skills[0].parameters.format = %#v, want json", got)
	}
	if got, want := parameterNames(parameters["required"]), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skills[0].parameters.required names = %#v, want %#v", got, want)
	}
	optionalNames := parameterNames(parameters["optional"])
	for _, want := range []string{"repo", "prompt", "repos", "repourl", "librarytaskname", "basebranch", "targetsubdir", "responsemode"} {
		if !containsParameterName(optionalNames, want) {
			t.Fatalf("skills[0].parameters.optional names = %#v, want %q present", optionalNames, want)
		}
	}
	if gotNames, want := meta["library_task_names"], []any{"security-review", "unit-test-coverage"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("library_task_names = %#v, want %#v", gotNames, want)
	}
	if got := meta["library_task_count"]; got != float64(2) {
		t.Fatalf("library_task_count = %#v, want 2", got)
	}
	skillCatalog, ok := meta["skill_catalog"].([]any)
	if !ok {
		t.Fatalf("skill_catalog = %#v, want []any", meta["skill_catalog"])
	}
	if got, want := len(skillCatalog), 2; got != want {
		t.Fatalf("len(skill_catalog) = %d, want %d", got, want)
	}
	first, ok := skillCatalog[0].(map[string]any)
	if !ok {
		t.Fatalf("skill_catalog[0] = %#v, want map[string]any", skillCatalog[0])
	}
	if got := first["handle"]; got != "code_for_me" {
		t.Fatalf("skill_catalog[0].handle = %#v, want code_for_me", got)
	}
	activation, ok := first["activation"].(map[string]any)
	if !ok {
		t.Fatalf("skill_catalog[0].activation = %#v, want map[string]any", first["activation"])
	}
	if got := activation["type"]; got != "skill_request" {
		t.Fatalf("skill_catalog[0].activation.type = %#v, want skill_request", got)
	}
	if got := activation["request_id"]; got != "<caller-generated-request-id>" {
		t.Fatalf("skill_catalog[0].activation.request_id = %#v, want request id placeholder", got)
	}
	if got := activation["skill_name"]; got != "code_for_me" {
		t.Fatalf("skill_catalog[0].activation.skill_name = %#v, want code_for_me", got)
	}
	if got := activation["reply_required"]; got != true {
		t.Fatalf("skill_catalog[0].activation.reply_required = %#v, want true", got)
	}
	if got := activation["payload_format"]; got != "json" {
		t.Fatalf("skill_catalog[0].activation.payload_format = %#v, want json", got)
	}
	if _, exists := activation["skill"]; exists {
		t.Fatalf("skill_catalog[0].activation.skill unexpectedly present: %#v", activation["skill"])
	}
	if _, exists := activation["input"]; exists {
		t.Fatalf("skill_catalog[0].activation.input unexpectedly present: %#v", activation["input"])
	}
	firstPayload, ok := activation["payload"].(map[string]any)
	if !ok {
		t.Fatalf("skill_catalog[0].activation.payload = %#v, want map[string]any", activation["payload"])
	}
	if got := firstPayload["repo"]; got != "<git@github.com:owner/repo.git>" {
		t.Fatalf("skill_catalog[0].activation.payload.repo = %#v, want repository placeholder", got)
	}
	if _, exists := firstPayload["repos"]; exists {
		t.Fatalf("skill_catalog[0].activation.payload.repos unexpectedly present: %#v", firstPayload["repos"])
	}
	second, ok := skillCatalog[1].(map[string]any)
	if !ok || second["handle"] != "code_review" {
		t.Fatalf("skill_catalog[1] = %#v, want handle code_review", skillCatalog[1])
	}
	secondActivation, ok := second["activation"].(map[string]any)
	if !ok {
		t.Fatalf("skill_catalog[1].activation = %#v, want map[string]any", second["activation"])
	}
	secondPayload, ok := secondActivation["payload"].(map[string]any)
	if !ok {
		t.Fatalf("skill_catalog[1].activation.payload = %#v, want map[string]any", secondActivation["payload"])
	}
	if got := secondActivation["skill_name"]; got != "code_review" {
		t.Fatalf("skill_catalog[1].activation.skill_name = %#v, want code_review", got)
	}
	if got := secondActivation["reply_required"]; got != true {
		t.Fatalf("skill_catalog[1].activation.reply_required = %#v, want true", got)
	}
	if got := secondPayload["repo"]; got != "<git@github.com:owner/repo.git>" {
		t.Fatalf("skill_catalog[1].activation.payload.repo = %#v, want repository placeholder", got)
	}
	if got := secondPayload["branch"]; got != "<pull-request-head-branch>" {
		t.Fatalf("skill_catalog[1].activation.payload.branch = %#v, want pull request head placeholder", got)
	}
	if _, exists := secondPayload["responseMode"]; exists {
		t.Fatalf("skill_catalog[1].activation.payload.responseMode unexpectedly present: %#v", secondPayload["responseMode"])
	}
	if _, exists := secondPayload["prNumber"]; exists {
		t.Fatalf("skill_catalog[1].activation.payload.prNumber unexpectedly present: %#v", secondPayload["prNumber"])
	}
}

func parameterNames(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		names = append(names, name)
	}
	return names
}

func containsParameterName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readMapString(v any, key string) string {
	m, _ := v.(map[string]any)
	value, _ := m[key].(string)
	return value
}

func TestRecordGitHubTaskCompleteActivityMergesExistingMetadata(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var calls []captured

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		switch r.URL.Path {
		case "/v1/agents/me/activities":
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path})
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case "/v1/agents/me":
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"display_name":"MoltenHub Code","activities":["started"],"visibility":"public"}}}}`))
		case "/v1/agents/me/metadata":
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.RecordGitHubTaskCompleteActivity(context.Background(), "token"); err != nil {
		t.Fatalf("RecordGitHubTaskCompleteActivity() error = %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/v1/agents/me/activities" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Method != http.MethodGet || calls[1].Path != "/v1/agents/me" {
		t.Fatalf("second call = %#v", calls[1])
	}
	if calls[2].Method != http.MethodPatch || calls[2].Path != "/v1/agents/me/metadata" {
		t.Fatalf("third call = %#v", calls[2])
	}

	meta, _ := calls[2].Body["metadata"].(map[string]any)
	if meta == nil {
		t.Fatalf("metadata wrapper missing: %#v", calls[2].Body)
	}
	if got := meta["display_name"]; got != "MoltenHub Code" {
		t.Fatalf("display_name = %#v", got)
	}
	if got := meta["visibility"]; got != "public" {
		t.Fatalf("visibility = %#v", got)
	}
	activities, ok := meta["activities"].([]any)
	if !ok {
		t.Fatalf("activities = %#v", meta["activities"])
	}
	if got, want := len(activities), 2; got != want {
		t.Fatalf("len(activities) = %d, want %d", got, want)
	}
	if activities[0] != "started" || activities[1] != gitHubTaskComplete {
		t.Fatalf("activities = %#v", activities)
	}
}

func TestRecordRunCompletedActivityPublishesAgentActivity(t *testing.T) {
	t.Parallel()

	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/agents/me/activities" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode activity body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	runCfg := config.Config{
		Repo:            "git@github.com:acme/repo.git",
		BaseBranch:      "main",
		LibraryTaskName: "security-review",
	}
	runCfg.ApplyDefaults()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.RecordRunCompletedActivity(context.Background(), "token", runCfg); err != nil {
		t.Fatalf("RecordRunCompletedActivity() error = %v", err)
	}

	if body["activity"] != "completed library task: security-review" {
		t.Fatalf("activity = %#v", body["activity"])
	}
	if body["category"] != "coding" || body["status"] != "completed" {
		t.Fatalf("activity state fields = %#v", body)
	}
}

func TestRecordActivityMergesExistingMetadata(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var calls []captured

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		switch r.URL.Path {
		case "/v1/agents/me/activities":
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path})
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case "/v1/agents/me":
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"display_name":"MoltenHub Code","activities":["started"],"visibility":"public"}}}}`))
		case "/v1/agents/me/metadata":
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.RecordActivity(context.Background(), "token", "working on  library task:   code-review"); err != nil {
		t.Fatalf("RecordActivity() error = %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/v1/agents/me/activities" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Method != http.MethodGet || calls[1].Path != "/v1/agents/me" {
		t.Fatalf("second call = %#v", calls[1])
	}
	if calls[2].Method != http.MethodPatch || calls[2].Path != "/v1/agents/me/metadata" {
		t.Fatalf("third call = %#v", calls[2])
	}

	meta, _ := calls[2].Body["metadata"].(map[string]any)
	if meta == nil {
		t.Fatalf("metadata wrapper missing: %#v", calls[2].Body)
	}
	activities, ok := meta["activities"].([]any)
	if !ok {
		t.Fatalf("activities = %#v", meta["activities"])
	}
	if got, want := len(activities), 2; got != want {
		t.Fatalf("len(activities) = %d, want %d", got, want)
	}
	if activities[0] != "started" || activities[1] != "working on library task: code-review" {
		t.Fatalf("activities = %#v", activities)
	}
}

func TestRecordActivityRequiresNonEmptyText(t *testing.T) {
	t.Parallel()

	client := NewAPIClient("https://example.test/v1")
	if err := client.RecordActivity(context.Background(), "token", "   "); err == nil {
		t.Fatal("RecordActivity() error = nil, want non-nil")
	}
}

func TestRecordCodingActivityRunningMergesExistingMetadata(t *testing.T) {
	t.Parallel()

	type captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var calls []captured

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		switch r.URL.Path {
		case "/v1/agents/me/activities":
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path})
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case "/v1/agents/me":
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"agent":{"metadata":{"display_name":"MoltenHub Code","activities":["started"],"visibility":"public"}}}}`))
		case "/v1/agents/me/metadata":
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)
			calls = append(calls, captured{Method: r.Method, Path: r.URL.Path, Body: body})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL + "/v1")
	if err := client.RecordCodingActivityRunning(context.Background(), "token"); err != nil {
		t.Fatalf("RecordCodingActivityRunning() error = %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/v1/agents/me/activities" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Method != http.MethodGet || calls[1].Path != "/v1/agents/me" {
		t.Fatalf("second call = %#v", calls[1])
	}
	if calls[2].Method != http.MethodPatch || calls[2].Path != "/v1/agents/me/metadata" {
		t.Fatalf("third call = %#v", calls[2])
	}

	meta, _ := calls[2].Body["metadata"].(map[string]any)
	if meta == nil {
		t.Fatalf("metadata wrapper missing: %#v", calls[2].Body)
	}
	if got := meta["display_name"]; got != "MoltenHub Code" {
		t.Fatalf("display_name = %#v", got)
	}
	if got := meta["visibility"]; got != "public" {
		t.Fatalf("visibility = %#v", got)
	}
	activities, ok := meta["activities"].([]any)
	if !ok {
		t.Fatalf("activities = %#v", meta["activities"])
	}
	if got, want := len(activities), 2; got != want {
		t.Fatalf("len(activities) = %d, want %d", got, want)
	}
	if activities[0] != "started" || activities[1] != codingActivityRun {
		t.Fatalf("activities = %#v", activities)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
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
