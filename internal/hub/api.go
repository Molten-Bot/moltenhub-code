package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/library"
)

type apiAttempt struct {
	Method string
	Path   string
	Body   any
}

var errNoPulledMessage = errors.New("no pulled message")

const (
	runtimeIdentifier    = "moltenhub-code"
	runtimeSkillFallback = "Executes MoltenHub Code tasks."
	agentVisibilityKey   = "visibility"
	agentVisibilityValue = "public"
	gitHubTaskComplete   = "github task complete"
	maxActivityEntries   = 20
)

// PulledOpenClawMessage is one leased inbound message from pull transport.
type PulledOpenClawMessage struct {
	DeliveryID string
	MessageID  string
	Message    map[string]any
}

// AgentProfile captures the profile fields this runtime needs to persist locally.
type AgentProfile struct {
	Handle  string
	Profile ProfileConfig
}

// APIClient wraps hub HTTP interactions.
type APIClient struct {
	BaseURL    string
	HTTPClient *http.Client
	Logf       func(string, ...any)
}

// NewAPIClient returns an API client with defaults.
func NewAPIClient(baseURL string) APIClient {
	return APIClient{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		HTTPClient: &http.Client{
			// Pull transport may long-poll up to 20s. Keep client timeout safely above that.
			Timeout: 35 * time.Second,
		},
		Logf: func(string, ...any) {},
	}
}

// ResolveAgentToken picks a working agent token from init config and bind flow.
func (c *APIClient) ResolveAgentToken(ctx context.Context, cfg InitConfig) (string, error) {
	if c == nil {
		return "", fmt.Errorf("api client is required")
	}

	agentToken := strings.TrimSpace(cfg.AgentToken)
	bindToken := strings.TrimSpace(cfg.BindToken)
	if agentToken != "" {
		if c.verifyToken(ctx, agentToken) {
			c.logf("hub.auth source=agent_config status=verified")
			return agentToken, nil
		}
		c.logf("hub.auth source=agent_config status=unverified")

		if bindToken != "" {
			if bound, ok := c.bindTokenFallback(ctx, bindToken, "bind_config_fallback"); ok {
				return bound, nil
			}
		}
		if bindToken == "" || bindToken != agentToken {
			if bound, ok := c.bindTokenFallback(ctx, agentToken, "agent_config_bind_fallback"); ok {
				return bound, nil
			}
		}
		return agentToken, nil
	}

	if bindToken == "" {
		return "", fmt.Errorf("missing bind_token and agent_token")
	}

	if bound, ok := c.bindTokenFallback(ctx, bindToken, "bind"); ok {
		return bound, nil
	}

	return "", fmt.Errorf("bind flow failed for provided bind_token")
}

func (c *APIClient) bindTokenFallback(ctx context.Context, bindToken, source string) (string, bool) {
	bindToken = strings.TrimSpace(bindToken)
	if bindToken == "" {
		return "", false
	}
	bound, err := c.bindTokenFlow(ctx, bindToken)
	if err != nil {
		c.logf("hub.auth source=%s status=failed err=%q", source, err)
		return "", false
	}
	if c.verifyToken(ctx, bound) {
		c.logf("hub.auth source=%s status=verified", source)
	} else {
		c.logf("hub.auth source=%s status=unverified", source)
	}
	return bound, true
}

func (c *APIClient) bindTokenFlow(ctx context.Context, bindToken string) (string, error) {
	bindToken = strings.TrimSpace(bindToken)
	if bindToken == "" {
		return "", fmt.Errorf("bind token is empty")
	}

	attempts := []struct {
		name      string
		path      string
		authToken string
		body      map[string]any
	}{
		{name: "bind-tokens.bind_token", path: "/agents/bind-tokens", body: map[string]any{"bind_token": bindToken}},
		{name: "bind-tokens.bindToken", path: "/agents/bind-tokens", body: map[string]any{"bindToken": bindToken}},
		{name: "bind-tokens.token", path: "/agents/bind-tokens", body: map[string]any{"token": bindToken}},
		{name: "bind.bind_token", path: "/agents/bind", body: map[string]any{"bind_token": bindToken}},
		{name: "bind.bindToken", path: "/agents/bind", body: map[string]any{"bindToken": bindToken}},
		{name: "bind.token", path: "/agents/bind", body: map[string]any{"token": bindToken}},
		{name: "bind.auth", path: "/agents/bind", authToken: bindToken, body: map[string]any{"bind_token": bindToken}},
	}

	var failures []string
	for _, attempt := range attempts {
		status, body, err := c.doJSON(ctx, http.MethodPost, attempt.path, attempt.authToken, attempt.body)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s network error: %v", attempt.name, err))
			continue
		}

		if status/100 == 2 {
			if token := extractTokenFromJSON(body); token != "" {
				if apiBase := extractAPIBaseFromJSON(body); apiBase != "" {
					c.BaseURL = strings.TrimRight(apiBase, "/")
				}
				c.logf("hub.auth attempt=%s status=ok credential=exchanged", attempt.name)
				return token, nil
			}
			if c.verifyToken(ctx, bindToken) {
				c.logf("hub.auth attempt=%s status=ok credential=reused_bind", attempt.name)
				return bindToken, nil
			}
			failures = append(failures, fmt.Sprintf("%s succeeded but no token in response", attempt.name))
			continue
		}

		failures = append(failures, fmt.Sprintf("%s status=%d", attempt.name, status))
	}

	return "", fmt.Errorf("bind flow failed: %s", strings.Join(failures, "; "))
}

// SyncProfile applies handle/profile updates.
func (c APIClient) SyncProfile(ctx context.Context, token string, cfg InitConfig) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("profile sync requires token")
	}

	cfg.ApplyDefaults()
	profileBody := map[string]any{
		"profile": buildAgentProfilePayload(cfg),
	}
	updateProfile := func(body map[string]any) (bool, string) {
		return c.tryAny(ctx, token, []apiAttempt{
			{Method: http.MethodPatch, Path: "/agents/me", Body: body},
			{Method: http.MethodPost, Path: "/agents/me", Body: body},
		})
	}

	handle := strings.TrimSpace(cfg.Handle)
	if handle == "" {
		ok, trace := updateProfile(profileBody)
		if !ok {
			return fmt.Errorf("set profile failed: %s", trace)
		}
		return c.syncProfileMetadata(ctx, token, cfg)
	}

	bodyWithHandle := map[string]any{
		"handle":  handle,
		"profile": profileBody["profile"],
	}
	ok, trace := updateProfile(bodyWithHandle)
	if ok {
		return c.syncProfileMetadata(ctx, token, cfg)
	}
	ok, retryTrace := updateProfile(profileBody)
	if ok {
		return c.syncProfileMetadata(ctx, token, cfg)
	}
	return fmt.Errorf("set profile failed: %s; retry_without_handle: %s", trace, retryTrace)
}

func (c APIClient) syncProfileMetadata(ctx context.Context, token string, cfg InitConfig) error {
	metadata, err := c.AgentMetadata(ctx, token)
	if err != nil {
		return fmt.Errorf("load agent metadata: %w", err)
	}

	metadata = cloneMetadataMap(metadata)
	for key, value := range buildAgentMetadata(cfg) {
		metadata[key] = value
	}

	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPost, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPost, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
	})
	if !ok {
		return fmt.Errorf("sync profile metadata failed: %s", trace)
	}
	return nil
}

// UpdateAgentStatus updates the hub-visible lifecycle status for this agent.
func (c APIClient) UpdateAgentStatus(ctx context.Context, token, status string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("update agent status requires token")
	}

	normalizedStatus, err := normalizeAgentStatus(status)
	if err != nil {
		return err
	}

	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPatch, Path: "/agents/me/status", Body: map[string]any{"status": normalizedStatus}},
		{Method: http.MethodPost, Path: "/agents/me/status", Body: map[string]any{"status": normalizedStatus}},
		{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"status": normalizedStatus}},
		{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"status": normalizedStatus}},
		{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"metadata": map[string]any{"status": normalizedStatus}}},
		{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"metadata": map[string]any{"status": normalizedStatus}}},
	})
	if !ok {
		return fmt.Errorf("update agent status failed: %s", trace)
	}

	return nil
}

// MarkOpenClawOffline marks this runtime offline for OpenClaw websocket transport.
func (c APIClient) MarkOpenClawOffline(ctx context.Context, token, sessionKey, reason string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("mark openclaw offline requires token")
	}

	body := map[string]any{}
	if strings.TrimSpace(sessionKey) != "" {
		normalizedSessionKey := strings.TrimSpace(sessionKey)
		body["session_key"] = normalizedSessionKey
		body["sessionKey"] = normalizedSessionKey
	}
	if strings.TrimSpace(reason) != "" {
		body["reason"] = strings.TrimSpace(reason)
	}

	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPost, Path: "/openclaw/messages/offline", Body: body},
	})
	if !ok {
		return fmt.Errorf("mark openclaw offline failed: %s", trace)
	}

	return nil
}

// RecordGitHubTaskCompleteActivity appends a minimal completion entry to metadata.activities.
func (c APIClient) RecordGitHubTaskCompleteActivity(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("record github task complete activity requires token")
	}

	metadata, err := c.AgentMetadata(ctx, token)
	if err != nil {
		return fmt.Errorf("load agent metadata: %w", err)
	}

	metadata = cloneMetadataMap(metadata)
	metadata["activities"] = appendActivityEntries(metadata["activities"], gitHubTaskComplete)

	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
	})
	if !ok {
		return fmt.Errorf("record github task complete activity failed: %s", trace)
	}

	return nil
}

// AgentMetadata loads the current agent metadata for safe merge-style updates.
func (c APIClient) AgentMetadata(ctx context.Context, token string) (map[string]any, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("agent metadata requires token")
	}

	status, body, err := c.doJSON(ctx, http.MethodGet, "/agents/me", token, nil)
	if err != nil {
		return nil, err
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("status=%d body=%s", status, truncateBody(body))
	}

	return extractMetadataFromJSON(body), nil
}

// AgentProfile loads the current agent handle/profile for config persistence.
func (c APIClient) AgentProfile(ctx context.Context, token string) (AgentProfile, error) {
	if strings.TrimSpace(token) == "" {
		return AgentProfile{}, fmt.Errorf("agent profile requires token")
	}

	status, body, err := c.doJSON(ctx, http.MethodGet, "/agents/me", token, nil)
	if err != nil {
		return AgentProfile{}, err
	}
	if status/100 != 2 {
		return AgentProfile{}, fmt.Errorf("status=%d body=%s", status, truncateBody(body))
	}

	return extractAgentProfileFromJSON(body), nil
}

// RegisterRuntime sends plugin/runtime metadata to hub.
func (c APIClient) RegisterRuntime(ctx context.Context, token string, cfg InitConfig, libraryTasks []library.TaskSummary) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("register runtime requires token")
	}

	metadata, err := c.AgentMetadata(ctx, token)
	if err != nil {
		return fmt.Errorf("load agent metadata: %w", err)
	}
	metadata = cloneMetadataMap(metadata)
	mergeRuntimeRegistrationMetadata(metadata, cfg, libraryTasks)

	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPost, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPost, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
	})
	if !ok {
		return fmt.Errorf("register runtime failed: %s", trace)
	}
	return nil
}

// PublishResult posts a skill result using OpenClaw publish transport.
func (c APIClient) PublishResult(ctx context.Context, token string, payload map[string]any) error {
	body := map[string]any{
		"message": payload,
	}
	if toAgentURI := firstString(payload["to_agent_uri"]); toAgentURI != "" {
		body["to_agent_uri"] = toAgentURI
	} else if toAgentUUID := firstString(payload["to_agent_uuid"]); toAgentUUID != "" {
		body["to_agent_uuid"] = toAgentUUID
	} else if routeTarget := firstString(payload["to"], payload["reply_to"]); routeTarget != "" {
		if looksLikeAgentURI(routeTarget) {
			body["to_agent_uri"] = routeTarget
		} else {
			body["to_agent_uuid"] = routeTarget
		}
	}
	if requestID := firstString(payload["request_id"]); requestID != "" {
		body["client_msg_id"] = requestID
	}

	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPost, Path: "/openclaw/messages/publish", Body: body},
	})
	if !ok {
		return fmt.Errorf("publish result failed: %s", trace)
	}
	return nil
}

// PullOpenClawMessage claims the next OpenClaw envelope using long-poll transport.
func (c APIClient) PullOpenClawMessage(ctx context.Context, token string, timeoutMs int) (PulledOpenClawMessage, bool, error) {
	query := ""
	if timeoutMs < 0 {
		timeoutMs = 0
	}
	if timeoutMs > 30000 {
		timeoutMs = 30000
	}
	if timeoutMs > 0 {
		query = fmt.Sprintf("?timeout_ms=%d", timeoutMs)
	}

	status, body, err := c.doJSON(ctx, http.MethodGet, "/openclaw/messages/pull"+query, token, nil)
	if err != nil {
		return PulledOpenClawMessage{}, false, err
	}
	switch status {
	case http.StatusNoContent:
		return PulledOpenClawMessage{}, false, nil
	case http.StatusOK:
		msg, err := parsePulledOpenClawMessage(body)
		if err != nil {
			if errors.Is(err, errNoPulledMessage) {
				return PulledOpenClawMessage{}, false, nil
			}
			return PulledOpenClawMessage{}, false, err
		}
		return msg, true, nil
	default:
		return PulledOpenClawMessage{}, false, fmt.Errorf("pull status=%d", status)
	}
}

// AckOpenClawDelivery acknowledges a leased pull delivery.
func (c APIClient) AckOpenClawDelivery(ctx context.Context, token, deliveryID string) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return fmt.Errorf("delivery id is required")
	}
	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPost, Path: "/openclaw/messages/ack", Body: map[string]any{"delivery_id": deliveryID}},
	})
	if !ok {
		return fmt.Errorf("ack delivery failed: %s", trace)
	}
	return nil
}

// NackOpenClawDelivery releases a leased pull delivery back to the queue.
func (c APIClient) NackOpenClawDelivery(ctx context.Context, token, deliveryID string) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return fmt.Errorf("delivery id is required")
	}
	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPost, Path: "/openclaw/messages/nack", Body: map[string]any{"delivery_id": deliveryID}},
	})
	if !ok {
		return fmt.Errorf("nack delivery failed: %s", trace)
	}
	return nil
}

// WebsocketURL builds the websocket endpoint from API base URL.
func WebsocketURL(baseURL, sessionKey string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("base url must use http or https")
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/openclaw/messages/ws"
	q := u.Query()
	if strings.TrimSpace(sessionKey) != "" {
		q.Set("session_key", sessionKey)
		q.Set("sessionKey", sessionKey)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c APIClient) verifyToken(ctx context.Context, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	status, _, err := c.doJSON(ctx, http.MethodGet, "/agents/me", token, nil)
	if err != nil {
		c.logf("hub.auth verify status=network_error err=%q", err)
		return false
	}
	return status/100 == 2
}

func (c APIClient) doJSON(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	if strings.TrimSpace(method) == "" {
		method = http.MethodPost
	}
	base := strings.TrimRight(c.BaseURL, "/")
	target := base + "/" + strings.TrimLeft(path, "/")

	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

func (c APIClient) tryAny(ctx context.Context, token string, attempts []apiAttempt) (bool, string) {
	if len(attempts) == 0 {
		return false, "no attempts configured"
	}
	var traces []string
	for _, attempt := range attempts {
		status, _, err := c.doJSON(ctx, attempt.Method, attempt.Path, token, attempt.Body)
		if err != nil {
			traces = append(traces, fmt.Sprintf("%s %s network=%v", attempt.Method, attempt.Path, err))
			continue
		}
		if status/100 == 2 {
			return true, fmt.Sprintf("%s %s", attempt.Method, attempt.Path)
		}
		traces = append(traces, fmt.Sprintf("%s %s status=%d", attempt.Method, attempt.Path, status))
	}
	return false, strings.Join(traces, "; ")
}

func (c APIClient) logf(format string, args ...any) {
	if c.Logf == nil {
		return
	}
	c.Logf("%s", redactSensitiveLogText(fmt.Sprintf(format, args...)))
}

func extractTokenFromJSON(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return extractTokenFromAny(parsed)
}

func extractAPIBaseFromJSON(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return extractAPIBaseFromAny(parsed)
}

func parsePulledOpenClawMessage(body []byte) (PulledOpenClawMessage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return PulledOpenClawMessage{}, errNoPulledMessage
	}

	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return PulledOpenClawMessage{}, fmt.Errorf("decode pull body: %w", err)
	}

	inbound := extractInboundOpenClawMessage(root)
	deliveryID := inbound.DeliveryID
	message := inbound.Message
	if deliveryID == "" {
		if len(message) == 0 {
			return PulledOpenClawMessage{}, errNoPulledMessage
		}
		return PulledOpenClawMessage{}, fmt.Errorf("pull response missing delivery_id")
	}
	if len(message) == 0 {
		return PulledOpenClawMessage{}, fmt.Errorf("pull response missing openclaw message")
	}

	return inbound, nil
}

func extractPulledMessage(result, root map[string]any) map[string]any {
	candidates := []any{
		valueAt(result, "openclaw_message", "message"),
		valueAt(result, "openclaw_message"),
		valueAt(result, "message"),
		valueAt(root, "openclaw_message", "message"),
		valueAt(root, "openclaw_message"),
		valueAt(root, "message"),
	}
	for _, candidate := range candidates {
		msg := toMap(candidate)
		if len(msg) == 0 {
			continue
		}
		// Some transports wrap the real envelope under message.message.
		if nested := toMap(msg["message"]); len(nested) > 0 && looksLikeDispatchEnvelope(nested) {
			return nested
		}
		// Some transports place the envelope inside message.payload.
		if nested := toMap(msg["payload"]); len(nested) > 0 && looksLikeDispatchEnvelope(nested) {
			return nested
		}
		return msg
	}
	return nil
}

func extractInboundOpenClawMessage(root map[string]any) PulledOpenClawMessage {
	result := root
	if nested := toMap(root["result"]); len(nested) > 0 {
		result = nested
	}

	message := extractPulledMessage(result, root)
	if len(message) == 0 {
		switch {
		case looksLikeDispatchEnvelope(result):
			message = result
		case looksLikeDispatchEnvelope(root):
			message = root
		}
	}
	message = enrichInboundMessageRouting(message, result, root)

	return PulledOpenClawMessage{
		DeliveryID: firstNonEmpty(
			stringAt(result, "delivery_id"),
			stringAt(result, "deliveryId"),
			stringAt(root, "delivery_id"),
			stringAt(root, "deliveryId"),
			stringAtPath(result, "delivery", "id"),
			stringAtPath(result, "delivery", "delivery_id"),
			stringAtPath(result, "delivery", "deliveryId"),
			stringAtPath(root, "delivery", "id"),
			stringAtPath(root, "delivery", "delivery_id"),
			stringAtPath(root, "delivery", "deliveryId"),
		),
		MessageID: firstNonEmpty(
			stringAt(result, "message_id"),
			stringAt(result, "messageId"),
			stringAt(root, "message_id"),
			stringAt(root, "messageId"),
			stringAtPath(result, "openclaw_message", "message_id"),
			stringAtPath(result, "openclaw_message", "messageId"),
			stringAtPath(result, "delivery", "message_id"),
			stringAtPath(result, "delivery", "messageId"),
			stringAtPath(root, "delivery", "message_id"),
			stringAtPath(root, "delivery", "messageId"),
			stringAt(message, "message_id"),
			stringAt(message, "id"),
		),
		Message: message,
	}
}

func enrichInboundMessageRouting(message, result, root map[string]any) map[string]any {
	if len(message) == 0 {
		return message
	}

	transportMessage := firstNonEmptyMap(
		toMap(valueAt(result, "message")),
		toMap(valueAt(root, "message")),
	)
	if len(transportMessage) == 0 {
		return message
	}

	copyIfMissing := func(key string, candidates ...string) {
		if strings.TrimSpace(stringAt(message, key)) != "" {
			return
		}
		for _, candidate := range candidates {
			if value := stringAt(transportMessage, candidate); value != "" {
				message[key] = value
				return
			}
		}
	}

	copyIfMissing("from_agent_uri", "from_agent_uri")
	copyIfMissing("from_agent_uuid", "from_agent_uuid")
	copyIfMissing("from_agent_id", "from_agent_id")
	copyIfMissing("source_agent_uri", "from_agent_uri")
	copyIfMissing("source_agent_uuid", "from_agent_uuid")
	copyIfMissing("source_agent_id", "from_agent_id")
	copyIfMissing("reply_to", "from_agent_uri", "from_agent_uuid", "from_agent_id")
	return message
}

func looksLikeDispatchEnvelope(msg map[string]any) bool {
	return firstNonEmpty(
		stringAt(msg, "type"),
		stringAt(msg, "event"),
		stringAt(msg, "message_type"),
		stringAt(msg, "skill"),
		stringAt(msg, "skill_name"),
	) != ""
}

func toMap(v any) map[string]any {
	switch typed := v.(type) {
	case map[string]any:
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			return parsed
		}
	}
	return nil
}

func valueAt(root map[string]any, path ...string) any {
	if len(path) == 0 || root == nil {
		return root
	}
	var current any = root
	for _, p := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := m[p]
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func firstNonEmptyMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func looksLikeAgentURI(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

func extractTokenFromAny(v any) string {
	switch typed := v.(type) {
	case map[string]any:
		for _, key := range []string{"agent_token", "access_token", "bearer_token", "token"} {
			if raw, ok := typed[key]; ok {
				if token, ok := raw.(string); ok && strings.TrimSpace(token) != "" {
					return strings.TrimSpace(token)
				}
			}
		}
		for _, key := range []string{"data", "result", "agent", "payload"} {
			if nested, ok := typed[key]; ok {
				if token := extractTokenFromAny(nested); token != "" {
					return token
				}
			}
		}
		for _, value := range typed {
			if token := extractTokenFromAny(value); token != "" {
				return token
			}
		}
	case []any:
		for _, entry := range typed {
			if token := extractTokenFromAny(entry); token != "" {
				return token
			}
		}
	}
	return ""
}

func extractMetadataFromJSON(body []byte) map[string]any {
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return map[string]any{}
	}
	return extractMetadataFromAny(parsed)
}

func extractAgentProfileFromJSON(body []byte) AgentProfile {
	if len(bytes.TrimSpace(body)) == 0 {
		return AgentProfile{}
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return AgentProfile{}
	}
	return extractAgentProfileFromAny(parsed)
}

func extractAPIBaseFromAny(v any) string {
	switch typed := v.(type) {
	case map[string]any:
		for _, key := range []string{"api_base", "apiBase", "base_url", "baseUrl"} {
			if raw, ok := typed[key]; ok {
				if base, ok := raw.(string); ok && strings.TrimSpace(base) != "" {
					return strings.TrimSpace(base)
				}
			}
		}
		for _, key := range []string{"data", "result", "agent", "payload"} {
			if nested, ok := typed[key]; ok {
				if base := extractAPIBaseFromAny(nested); base != "" {
					return base
				}
			}
		}
	case []any:
		for _, entry := range typed {
			if base := extractAPIBaseFromAny(entry); base != "" {
				return base
			}
		}
	}
	return ""
}

func extractMetadataFromAny(v any) map[string]any {
	switch typed := v.(type) {
	case map[string]any:
		if metadata, ok := typed["metadata"]; ok {
			if out := toMap(metadata); out != nil {
				return out
			}
		}
		for _, key := range []string{"result", "agent", "data", "payload"} {
			if nested, ok := typed[key]; ok {
				if out := extractMetadataFromAny(nested); len(out) > 0 {
					return out
				}
			}
		}
	case []any:
		for _, entry := range typed {
			if out := extractMetadataFromAny(entry); len(out) > 0 {
				return out
			}
		}
	}
	return map[string]any{}
}

func extractAgentProfileFromAny(v any) AgentProfile {
	switch typed := v.(type) {
	case map[string]any:
		profile := AgentProfile{
			Handle: firstNonEmpty(
				stringAt(typed, "handle"),
				stringAt(typed, "agent_handle"),
				stringAt(typed, "username"),
			),
		}

		if nested := toMap(typed["profile"]); len(nested) > 0 {
			profile.Profile = extractProfileConfig(nested)
		}
		if profileConfigEmpty(profile.Profile) {
			profile.Profile = extractProfileConfig(extractMetadataFromAny(typed))
		}
		if nested, ok := typed["result"]; ok {
			merged := mergeAgentProfiles(profile, extractAgentProfileFromAny(nested))
			if !agentProfileEmpty(merged) {
				return merged
			}
		}
		for _, key := range []string{"agent", "data", "payload"} {
			if nested, ok := typed[key]; ok {
				merged := mergeAgentProfiles(profile, extractAgentProfileFromAny(nested))
				if !agentProfileEmpty(merged) {
					return merged
				}
			}
		}
		return profile
	case []any:
		for _, entry := range typed {
			if profile := extractAgentProfileFromAny(entry); !agentProfileEmpty(profile) {
				return profile
			}
		}
	}
	return AgentProfile{}
}

func mergeAgentProfiles(primary, secondary AgentProfile) AgentProfile {
	merged := primary
	if strings.TrimSpace(merged.Handle) == "" {
		merged.Handle = strings.TrimSpace(secondary.Handle)
	}
	if strings.TrimSpace(merged.Profile.DisplayName) == "" {
		merged.Profile.DisplayName = strings.TrimSpace(secondary.Profile.DisplayName)
	}
	if strings.TrimSpace(merged.Profile.Emoji) == "" {
		merged.Profile.Emoji = strings.TrimSpace(secondary.Profile.Emoji)
	}
	if strings.TrimSpace(merged.Profile.ProfileText) == "" {
		merged.Profile.ProfileText = strings.TrimSpace(secondary.Profile.ProfileText)
	}
	if strings.TrimSpace(merged.Profile.LLM) == "" {
		merged.Profile.LLM = strings.TrimSpace(secondary.Profile.LLM)
	}
	if strings.TrimSpace(merged.Profile.Harness) == "" {
		merged.Profile.Harness = strings.TrimSpace(secondary.Profile.Harness)
	}
	if len(merged.Profile.Skills) == 0 && len(secondary.Profile.Skills) > 0 {
		merged.Profile.Skills = append([]string(nil), secondary.Profile.Skills...)
	}
	return merged
}

func extractProfileConfig(raw map[string]any) ProfileConfig {
	skills := parseProfileSkills(raw["skills"])
	return ProfileConfig{
		DisplayName: firstNonEmpty(
			stringAt(raw, "display_name"),
			stringAt(raw, "displayName"),
			stringAt(raw, "name"),
		),
		Emoji: firstNonEmpty(
			stringAt(raw, "emoji"),
			stringAt(raw, "icon"),
		),
		ProfileText: firstNonEmpty(
			stringAt(raw, "profile"),
			stringAt(raw, "bio"),
			stringAt(raw, "description"),
			stringAt(raw, "summary"),
		),
		LLM: firstNonEmpty(
			stringAt(raw, "llm"),
			stringAt(raw, "model"),
		),
		Harness: firstNonEmpty(
			stringAt(raw, "harness"),
			stringAt(raw, "runtime"),
		),
		Skills: skills,
	}
}

func agentProfileEmpty(profile AgentProfile) bool {
	return strings.TrimSpace(profile.Handle) == "" && profileConfigEmpty(profile.Profile)
}

func profileConfigEmpty(profile ProfileConfig) bool {
	return strings.TrimSpace(profile.DisplayName) == "" &&
		strings.TrimSpace(profile.Emoji) == "" &&
		strings.TrimSpace(profile.ProfileText) == "" &&
		strings.TrimSpace(profile.LLM) == "" &&
		strings.TrimSpace(profile.Harness) == "" &&
		len(profile.Skills) == 0
}

func buildAgentProfilePayload(cfg InitConfig) map[string]any {
	return map[string]any{
		"profile":      strings.TrimSpace(cfg.Profile.ProfileText),
		"display_name": strings.TrimSpace(cfg.Profile.DisplayName),
		"emoji":        strings.TrimSpace(cfg.Profile.Emoji),
		"llm":          strings.TrimSpace(cfg.Profile.LLM),
		"harness":      runtimeIdentifier,
		"skills":       append([]string(nil), cfg.Profile.Skills...),
	}
}

func parseProfileSkills(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		if len(typed) == 0 {
			return nil
		}
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			out = append(out, entry)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			switch value := entry.(type) {
			case string:
				value = strings.TrimSpace(value)
				if value != "" {
					out = append(out, value)
				}
			case map[string]any:
				name := strings.TrimSpace(firstString(value["name"], value["skill"], value["id"]))
				if name != "" {
					out = append(out, name)
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func truncateBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) <= 200 {
		return trimmed
	}
	return trimmed[:200] + "..."
}

func buildAgentMetadata(cfg InitConfig) map[string]any {
	harness := agentruntime.Default().Harness
	if runtime, err := agentruntime.Resolve(cfg.AgentHarness, cfg.AgentCommand); err == nil {
		harness = runtime.Harness
	}
	metadata := map[string]any{
		"agent_type":          normalizeAgentType(nil),
		"runtime":             runtimeIdentifier,
		"harness":             runtimeIdentifier + "@v1",
		"agent_harness":       harness,
		"agent_harness_label": agentruntime.DisplayName(harness),
	}

	if strings.TrimSpace(cfg.Profile.DisplayName) != "" {
		metadata["display_name"] = strings.TrimSpace(cfg.Profile.DisplayName)
	}
	if strings.TrimSpace(cfg.Profile.Emoji) != "" {
		metadata["emoji"] = strings.TrimSpace(cfg.Profile.Emoji)
	}
	if strings.TrimSpace(cfg.Profile.ProfileText) != "" {
		metadata["profile"] = strings.TrimSpace(cfg.Profile.ProfileText)
	}
	if markdown := buildProfileMarkdown(cfg.Profile.DisplayName, cfg.Profile.Emoji, cfg.Profile.ProfileText); markdown != "" {
		metadata["profile_markdown"] = markdown
	}
	metadata["public"] = true
	metadata["is_public"] = true
	metadata[agentVisibilityKey] = agentVisibilityValue

	metadata["skills"] = buildSupportedSkillsMetadata()

	return metadata
}

func mergeRuntimeRegistrationMetadata(metadata map[string]any, cfg InitConfig, libraryTasks []library.TaskSummary) {
	if metadata == nil {
		return
	}

	for key, value := range buildAgentMetadata(cfg) {
		metadata[key] = value
	}

	libraryNames := make([]string, 0, len(libraryTasks))
	for _, task := range libraryTasks {
		name := strings.TrimSpace(task.Name)
		if name == "" {
			continue
		}
		libraryNames = append(libraryNames, name)
	}

	metadata["library_task_names"] = libraryNames
	metadata["library_task_count"] = len(libraryNames)
	metadata["library_tasks"] = libraryTasks
	metadata["skill_catalog"] = buildRuntimeSkillCatalog(cfg.Skill, libraryTasks)
	metadata["run_config_modes"] = []string{"prompt", "review", "library_task"}
	metadata["supports_branch_key"] = true
}

func buildRuntimeSkillCatalog(skillCfg SkillConfig, libraryTasks []library.TaskSummary) []map[string]any {
	dispatchType := strings.TrimSpace(skillCfg.DispatchType)
	if dispatchType == "" {
		dispatchType = defaultRuntimeDispatchType
	}

	buildActivation := func(skillName string, runConfig map[string]any) map[string]any {
		return map[string]any{
			"type":  dispatchType,
			"skill": normalizeSkillName(skillName),
			"input": map[string]any{
				"config": runConfig,
			},
		}
	}

	out := make([]map[string]any, 0, 3)
	out = append(out, map[string]any{
		"name":        normalizeSkillName(skillCfg.Name),
		"handle":      normalizeSkillName(skillCfg.Name),
		"mode":        "prompt",
		"description": "Prompt-driven repository task run.",
		"activation": buildActivation(skillCfg.Name, map[string]any{
			"repos":        []string{"<git@github.com:owner/repo.git>"},
			"baseBranch":   "main",
			"targetSubdir": ".",
			"prompt":       "<describe the requested change>",
			"images":       []any{},
		}),
	})

	out = append(out, map[string]any{
		"name":        normalizeSkillName(codeReviewSkillName),
		"handle":      normalizeSkillName(codeReviewSkillName),
		"mode":        "review",
		"displayName": "Pull Request Code Review",
		"description": fmt.Sprintf("Runs the %s workflow using repo + branch context.", codeReviewLibraryTaskName),
		"activation": buildActivation(codeReviewSkillName, map[string]any{
			"repos":        []string{"<git@github.com:owner/repo.git>"},
			"baseBranch":   "main",
			"targetSubdir": ".",
		}),
	})

	libraryTaskDescription := "Runs a checked-in library task by handle."
	if len(libraryTasks) > 0 {
		names := make([]string, 0, len(libraryTasks))
		for _, task := range libraryTasks {
			name := strings.TrimSpace(task.Name)
			if name == "" {
				continue
			}
			names = append(names, name)
		}
		if len(names) > 0 {
			libraryTaskDescription = fmt.Sprintf("Runs a checked-in library task by handle. Available handles: %s.", strings.Join(names, ", "))
		}
	}
	out = append(out, map[string]any{
		"name":        normalizeSkillName(libraryTaskSkillName),
		"handle":      normalizeSkillName(libraryTaskSkillName),
		"mode":        "library_task",
		"displayName": "Library Task",
		"description": libraryTaskDescription,
		"activation": buildActivation(libraryTaskSkillName, map[string]any{
			"repos":           []string{"<git@github.com:owner/repo.git>"},
			"baseBranch":      "main",
			"targetSubdir":    ".",
			"libraryTaskName": "<library-handle>",
		}),
	})

	return out
}

func buildSupportedSkillsMetadata() []map[string]any {
	runtimeSkill := runtimeSkillConfig()
	return []map[string]any{
		{
			"name":        normalizeSkillName(runtimeSkill.Name),
			"description": "Prompt-driven repository task run.",
		},
		{
			"name":        normalizeSkillName(codeReviewSkillName),
			"description": "Repository code review run that targets the checked-in review workflow.",
		},
		{
			"name":        normalizeSkillName(libraryTaskSkillName),
			"description": "Generic library task entrypoint that requires a library task handle.",
		},
	}
}

func cloneMetadataMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func appendActivityEntries(raw any, entry string) []string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}

	activities := make([]string, 0, maxActivityEntries)
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		activities = append(activities, value)
	}

	switch typed := raw.(type) {
	case []string:
		for _, value := range typed {
			appendValue(value)
		}
	case []any:
		for _, value := range typed {
			if text, ok := value.(string); ok {
				appendValue(text)
			}
		}
	case string:
		appendValue(typed)
	}

	if len(activities) == 0 || activities[len(activities)-1] != entry {
		appendValue(entry)
	}
	if len(activities) > maxActivityEntries {
		activities = append([]string(nil), activities[len(activities)-maxActivityEntries:]...)
	}
	return activities
}

func normalizeAgentType(raw any) string {
	s, _ := raw.(string)
	return normalizeIdentifier(s, runtimeIdentifier)
}

func normalizeSkillsMetadata(raw any, fallbackName, fallbackDescription string) []map[string]any {
	fallbackName = normalizeSkillName(fallbackName)
	fallbackDescription = normalizeDescription(fallbackDescription, runtimeSkillFallback)

	out := make([]map[string]any, 0, 1)
	seen := map[string]struct{}{}
	appendSkill := func(name, description string) {
		normalizedName := normalizeSkillName(name)
		if normalizedName == "" {
			normalizedName = fallbackName
		}
		if _, exists := seen[normalizedName]; exists {
			return
		}
		seen[normalizedName] = struct{}{}
		out = append(out, map[string]any{
			"name":        normalizedName,
			"description": normalizeDescription(description, fallbackDescription),
		})
	}

	switch typed := raw.(type) {
	case []any:
		for _, entry := range typed {
			switch v := entry.(type) {
			case map[string]any:
				appendSkill(firstString(v["name"], v["skill"], v["id"]), firstString(v["description"], v["summary"], v["bio"]))
			case string:
				appendSkill(v, "")
			}
		}
	case []map[string]any:
		for _, entry := range typed {
			appendSkill(firstString(entry["name"], entry["skill"], entry["id"]), firstString(entry["description"], entry["summary"], entry["bio"]))
		}
	case map[string]any:
		appendSkill(firstString(typed["name"], typed["skill"], typed["id"]), firstString(typed["description"], typed["summary"], typed["bio"]))
	case string:
		appendSkill(typed, "")
	}

	if len(out) == 0 {
		appendSkill(fallbackName, fallbackDescription)
	}
	return out
}

func normalizeSkillName(name string) string {
	return normalizeIdentifier(name, "code_for_me")
}

func normalizeIdentifier(value, fallback string) string {
	normalized := sanitizeIdentifier(value)
	if normalized != "" {
		return normalized
	}
	normalized = sanitizeIdentifier(fallback)
	if normalized != "" {
		return normalized
	}
	return "unknown"
}

func sanitizeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(value))
	needsSeparator := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			if needsSeparator && b.Len() > 0 {
				b.WriteByte('-')
			}
			needsSeparator = false
			b.WriteRune(r)
			continue
		}
		needsSeparator = true
	}

	out := strings.Trim(b.String(), "-._")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-._")
	}
	if len(out) < 2 {
		return ""
	}
	return out
}

func skillDescription(cfg SkillConfig) string {
	dispatchType := strings.TrimSpace(cfg.DispatchType)
	resultType := strings.TrimSpace(cfg.ResultType)
	switch {
	case dispatchType != "" && resultType != "":
		return normalizeDescription(
			fmt.Sprintf("Handles %s requests and returns %s responses.", dispatchType, resultType),
			runtimeSkillFallback,
		)
	case dispatchType != "":
		return normalizeDescription(
			fmt.Sprintf("Handles %s requests.", dispatchType),
			runtimeSkillFallback,
		)
	case resultType != "":
		return normalizeDescription(
			fmt.Sprintf("Returns %s responses.", resultType),
			runtimeSkillFallback,
		)
	default:
		return runtimeSkillFallback
	}
}

func normalizeDescription(value, fallback string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		value = strings.Join(strings.Fields(strings.TrimSpace(fallback)), " ")
	}
	if value == "" {
		value = runtimeSkillFallback
	}
	if len(value) > 240 {
		value = strings.TrimSpace(value[:240])
	}
	if value == "" {
		value = runtimeSkillFallback
	}
	return value
}

func normalizeAgentStatus(status string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "online", "offline":
		return normalized, nil
	default:
		return "", fmt.Errorf("agent status must be online or offline")
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

func buildProfileMarkdown(displayName, emoji, profileText string) string {
	displayName = strings.TrimSpace(displayName)
	emoji = strings.TrimSpace(emoji)
	profileText = strings.TrimSpace(profileText)

	header := strings.TrimSpace(strings.Join([]string{emoji, displayName}, " "))
	switch {
	case header != "" && profileText != "":
		return "# " + header + "\n\n" + profileText
	case header != "":
		return "# " + header
	default:
		return profileText
	}
}
