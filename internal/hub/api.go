package hub

import (
	"bytes"
	"context"
	"encoding/json"
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
			Timeout: 20 * time.Second,
		},
		Logf: func(string, ...any) {},
	}
}

// ResolveAgentToken picks a working agent token from init config and bind flow.
func (c APIClient) ResolveAgentToken(ctx context.Context, cfg InitConfig) (string, error) {
	if strings.TrimSpace(cfg.AgentToken) != "" {
		if c.verifyToken(ctx, cfg.AgentToken) {
			return cfg.AgentToken, nil
		}
		c.logf("hub.auth token=agent_token status=invalid")
	}

	if strings.TrimSpace(cfg.BindToken) == "" {
		return "", fmt.Errorf("missing bind_token and usable agent_token")
	}

	bound, err := c.bindTokenFlow(ctx, cfg.BindToken)
	if err != nil {
		return "", err
	}
	if c.verifyToken(ctx, bound) {
		return bound, nil
	}
	return "", fmt.Errorf("resolved token failed verification")
}

func (c APIClient) bindTokenFlow(ctx context.Context, bindToken string) (string, error) {
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
				c.logf("hub.auth attempt=%s status=ok token=exchanged", attempt.name)
				return token, nil
			}
			if c.verifyToken(ctx, bindToken) {
				c.logf("hub.auth attempt=%s status=ok token=bind_token", attempt.name)
				return bindToken, nil
			}
			failures = append(failures, fmt.Sprintf("%s succeeded but no token in response", attempt.name))
			continue
		}

		failures = append(failures, fmt.Sprintf("%s status=%d body=%s", attempt.name, status, truncateBody(body)))
	}

	return "", fmt.Errorf("bind flow failed: %s", strings.Join(failures, "; "))
}

// SyncProfile applies optional handle/profile/metadata updates.
func (c APIClient) SyncProfile(ctx context.Context, token string, cfg InitConfig) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("profile sync requires token")
	}

	if cfg.Handle != "" {
		handleBody := map[string]any{"handle": cfg.Handle}
		ok, trace := c.tryAny(ctx, token, []apiAttempt{
			{Method: http.MethodPost, Path: "/agents/me/handle/finalize", Body: handleBody},
			{Method: http.MethodPatch, Path: "/agents/me/handle/finalize", Body: handleBody},
			{Method: http.MethodPatch, Path: "/agents/me", Body: handleBody},
			{Method: http.MethodPut, Path: "/agents/me", Body: handleBody},
		})
		if !ok {
			return fmt.Errorf("set handle failed: %s", trace)
		}
	}

	profilePatch := map[string]any{}
	if cfg.Profile.DisplayName != "" {
		profilePatch["display_name"] = cfg.Profile.DisplayName
		profilePatch["name"] = cfg.Profile.DisplayName
	}
	if cfg.Profile.Emoji != "" {
		profilePatch["emoji"] = cfg.Profile.Emoji
	}
	if cfg.Profile.Bio != "" {
		profilePatch["bio"] = cfg.Profile.Bio
	}
	if len(profilePatch) > 0 {
		ok, trace := c.tryAny(ctx, token, []apiAttempt{
			{Method: http.MethodPatch, Path: "/agents/me", Body: profilePatch},
			{Method: http.MethodPut, Path: "/agents/me", Body: profilePatch},
		})
		if !ok {
			return fmt.Errorf("set profile failed: %s", trace)
		}
	}

	metadata := map[string]any{}
	for k, v := range cfg.Profile.Metadata {
		metadata[k] = v
	}
	if _, ok := metadata["agent_type"]; !ok {
		metadata["agent_type"] = "codex-harness"
	}
	if _, ok := metadata["runtime"]; !ok {
		metadata["runtime"] = "codex-harness"
	}
	if _, ok := metadata["skills"]; !ok {
		metadata["skills"] = []map[string]any{{
			"name":          cfg.Skill.Name,
			"dispatch_type": cfg.Skill.DispatchType,
			"result_type":   cfg.Skill.ResultType,
		}}
	}
	if len(metadata) > 0 {
		ok, trace := c.tryAny(ctx, token, []apiAttempt{
			{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: metadata},
			{Method: http.MethodPatch, Path: "/agents/me/metadata", Body: map[string]any{"metadata": metadata}},
			{Method: http.MethodPatch, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
			{Method: http.MethodPut, Path: "/agents/me", Body: map[string]any{"metadata": metadata}},
		})
		if !ok {
			return fmt.Errorf("set metadata failed: %s", trace)
		}
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

// PublishResult posts a skill result fallback when websocket publish fails.
func (c APIClient) PublishResult(ctx context.Context, token string, payload map[string]any) error {
	ok, trace := c.tryAny(ctx, token, []apiAttempt{
		{Method: http.MethodPost, Path: "/openclaw/messages/publish", Body: payload},
	})
	if !ok {
		return fmt.Errorf("publish result failed: %s", trace)
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
		status, body, err := c.doJSON(ctx, attempt.Method, attempt.Path, token, attempt.Body)
		if err != nil {
			traces = append(traces, fmt.Sprintf("%s %s network=%v", attempt.Method, attempt.Path, err))
			continue
		}
		if status/100 == 2 {
			return true, fmt.Sprintf("%s %s", attempt.Method, attempt.Path)
		}
		traces = append(traces, fmt.Sprintf("%s %s status=%d body=%s", attempt.Method, attempt.Path, status, truncateBody(body)))
	}
	return false, strings.Join(traces, "; ")
}

func (c APIClient) logf(format string, args ...any) {
	if c.Logf == nil {
		return
	}
	c.Logf(format, args...)
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

func truncateBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) <= 200 {
		return trimmed
	}
	return trimmed[:200] + "..."
}
