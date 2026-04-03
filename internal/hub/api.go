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
)

type apiAttempt struct {
	Method string
	Path   string
	Body   any
}

var errNoPulledMessage = errors.New("no pulled message")

// PulledOpenClawMessage is one leased inbound message from pull transport.
type PulledOpenClawMessage struct {
	DeliveryID string
	MessageID  string
	Message    map[string]any
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
	if agentToken != "" {
		if c.verifyToken(ctx, agentToken) {
			c.logf("hub.auth source=agent_config status=verified")
		} else {
			c.logf("hub.auth source=agent_config status=unverified")
		}
		return agentToken, nil
	}

	bindToken := strings.TrimSpace(cfg.BindToken)
	if bindToken == "" {
		return "", fmt.Errorf("missing bind_token and agent_token")
	}

	bound, err := c.bindTokenFlow(ctx, bindToken)
	if err != nil {
		return "", err
	}
	if c.verifyToken(ctx, bound) {
		return bound, nil
	}
	c.logf("hub.auth source=bind status=unverified")
	return bound, nil
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

// SyncProfile applies optional handle/profile/metadata updates.
func (c APIClient) SyncProfile(ctx context.Context, token string, cfg InitConfig) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("profile sync requires token")
	}

	if handle := strings.TrimSpace(cfg.Handle); handle != "" {
		handleBody := map[string]any{"handle": handle}
		ok, trace := c.tryAny(ctx, token, []apiAttempt{
			{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: handleBody},
			{Method: http.MethodPatch, Path: "/agents/me", Body: handleBody},
		})
		if !ok {
			return fmt.Errorf("set handle failed: %s", trace)
		}
	}

	metadata := buildAgentMetadata(cfg)
	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
		{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
	})
	if !ok {
		return fmt.Errorf("set metadata failed: %s", trace)
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

// RegisterRuntime sends plugin/runtime metadata to hub.
func (c APIClient) RegisterRuntime(ctx context.Context, token string, cfg InitConfig) error {
	body := map[string]any{
		"plugin_id":   "codex-harness",
		"runtime_id":  "codex-harness",
		"session_key": cfg.SessionKey,
		"skills": []map[string]any{{
			"name":          cfg.Skill.Name,
			"dispatch_type": cfg.Skill.DispatchType,
			"result_type":   cfg.Skill.ResultType,
		}},
		"metadata": map[string]any{
			"agent_type": "codex-harness",
		},
	}
	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPost, Path: "/openclaw/messages/register-plugin", Body: body},
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

func truncateBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) <= 200 {
		return trimmed
	}
	return trimmed[:200] + "..."
}

func buildAgentMetadata(cfg InitConfig) map[string]any {
	metadata := map[string]any{}
	for k, v := range cfg.Profile.Metadata {
		metadata[k] = v
	}

	metadata["agent_type"] = normalizeAgentType(metadata["agent_type"])
	if _, ok := metadata["runtime"]; !ok {
		metadata["runtime"] = "codex-harness"
	}
	if _, ok := metadata["harness"]; !ok {
		metadata["harness"] = "codex-harness@v1"
	}

	if _, ok := metadata["display_name"]; !ok && strings.TrimSpace(cfg.Profile.DisplayName) != "" {
		metadata["display_name"] = strings.TrimSpace(cfg.Profile.DisplayName)
	}
	if _, ok := metadata["emoji"]; !ok && strings.TrimSpace(cfg.Profile.Emoji) != "" {
		metadata["emoji"] = strings.TrimSpace(cfg.Profile.Emoji)
	}
	if _, ok := metadata["bio"]; !ok && strings.TrimSpace(cfg.Profile.Bio) != "" {
		metadata["bio"] = strings.TrimSpace(cfg.Profile.Bio)
	}
	if _, ok := metadata["profile_markdown"]; !ok {
		if markdown := buildProfileMarkdown(cfg.Profile.DisplayName, cfg.Profile.Emoji, cfg.Profile.Bio); markdown != "" {
			metadata["profile_markdown"] = markdown
		}
	}

	fallbackName := normalizeSkillName(cfg.Skill.Name)
	fallbackDescription := skillDescription(cfg.Skill)
	metadata["skills"] = normalizeSkillsMetadata(metadata["skills"], fallbackName, fallbackDescription)

	return metadata
}

func normalizeAgentType(raw any) string {
	s, _ := raw.(string)
	return normalizeIdentifier(s, "codex-harness")
}

func normalizeSkillsMetadata(raw any, fallbackName, fallbackDescription string) []map[string]any {
	fallbackName = normalizeSkillName(fallbackName)
	fallbackDescription = normalizeDescription(fallbackDescription, "Executes Codex harness tasks.")

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
			"Executes Codex harness tasks.",
		)
	case dispatchType != "":
		return normalizeDescription(
			fmt.Sprintf("Handles %s requests.", dispatchType),
			"Executes Codex harness tasks.",
		)
	case resultType != "":
		return normalizeDescription(
			fmt.Sprintf("Returns %s responses.", resultType),
			"Executes Codex harness tasks.",
		)
	default:
		return "Executes Codex harness tasks."
	}
}

func normalizeDescription(value, fallback string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		value = strings.Join(strings.Fields(strings.TrimSpace(fallback)), " ")
	}
	if value == "" {
		value = "Executes Codex harness tasks."
	}
	if len(value) > 240 {
		value = strings.TrimSpace(value[:240])
	}
	if value == "" {
		value = "Executes Codex harness tasks."
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

func buildProfileMarkdown(displayName, emoji, bio string) string {
	displayName = strings.TrimSpace(displayName)
	emoji = strings.TrimSpace(emoji)
	bio = strings.TrimSpace(bio)

	header := strings.TrimSpace(strings.Join([]string{emoji, displayName}, " "))
	switch {
	case header != "" && bio != "":
		return "# " + header + "\n\n" + bio
	case header != "":
		return "# " + header
	default:
		return bio
	}
}
