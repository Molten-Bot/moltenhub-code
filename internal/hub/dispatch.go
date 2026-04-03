package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jef/how-i-want-to-code/internal/config"
)

// SkillDispatch represents one inbound skill request ready for execution.
type SkillDispatch struct {
	RequestID string
	Skill     string
	ReplyTo   string
	Config    config.Config
}

// ParseSkillDispatch parses an inbound transport JSON message into a runnable dispatch.
func ParseSkillDispatch(msg map[string]any, expectedType, expectedSkill string) (SkillDispatch, bool, error) {
	if len(msg) == 0 {
		return SkillDispatch{}, false, nil
	}

	eventType := firstNonEmpty(
		stringAt(msg, "type"),
		stringAt(msg, "event"),
		stringAt(msg, "kind"),
		stringAt(msg, "message_type"),
		stringAtPath(msg, "payload", "type"),
		stringAtPath(msg, "payload", "event"),
		stringAtPath(msg, "data", "type"),
		stringAtPath(msg, "data", "event"),
	)
	skillName := firstNonEmpty(
		stringAt(msg, "skill"),
		stringAt(msg, "skill_name"),
		stringAt(msg, "name"),
		stringAtPath(msg, "payload", "skill"),
		stringAtPath(msg, "payload", "skill_name"),
		stringAtPath(msg, "payload", "name"),
		stringAtPath(msg, "data", "skill"),
		stringAtPath(msg, "data", "skill_name"),
		stringAtPath(msg, "data", "name"),
	)
	expectedSkill = strings.TrimSpace(expectedSkill)
	if expectedSkill != "" {
		if skillName == "" {
			return SkillDispatch{}, false, nil
		}
		if !skillNamesEqual(skillName, expectedSkill) {
			return SkillDispatch{}, false, nil
		}
	}

	dispatch := SkillDispatch{
		RequestID: firstNonEmpty(
			stringAt(msg, "request_id"),
			stringAt(msg, "id"),
			stringAt(msg, "message_id"),
			stringAt(msg, "delivery_id"),
			stringAtPath(msg, "payload", "request_id"),
			stringAtPath(msg, "payload", "id"),
			stringAtPath(msg, "data", "request_id"),
			stringAtPath(msg, "data", "id"),
		),
		Skill: firstNonEmpty(skillName, strings.TrimSpace(expectedSkill)),
		ReplyTo: firstNonEmpty(
			stringAt(msg, "reply_to"),
			stringAt(msg, "replyTo"),
			stringAt(msg, "to_agent_uri"),
			stringAt(msg, "to_agent_uuid"),
			stringAt(msg, "from"),
			stringAt(msg, "source"),
			stringAt(msg, "source_agent_uri"),
			stringAt(msg, "source_agent_uuid"),
			stringAt(msg, "source_agent_id"),
			stringAt(msg, "from_agent_uri"),
			stringAt(msg, "from_agent_uuid"),
			stringAt(msg, "from_agent_id"),
			stringAtPath(msg, "payload", "reply_to"),
			stringAtPath(msg, "payload", "from"),
			stringAtPath(msg, "data", "reply_to"),
			stringAtPath(msg, "data", "from"),
		),
	}

	expectedType = strings.TrimSpace(expectedType)
	if expectedType != "" {
		if eventType == "" {
			return dispatch, true, fmt.Errorf("missing dispatch type")
		}
		if !strings.EqualFold(eventType, expectedType) {
			return dispatch, true, fmt.Errorf("unexpected dispatch type %q", eventType)
		}
	}

	configValue, ok := extractConfigValue(msg)
	if !ok {
		return dispatch, true, fmt.Errorf("missing run config payload")
	}

	cfg, err := parseRunConfigValue(configValue)
	if err != nil {
		return dispatch, true, err
	}
	dispatch.Config = cfg
	return dispatch, true, nil
}

// ParseRunConfigJSON parses one inline run config JSON object into a validated config.
func ParseRunConfigJSON(payload []byte) (config.Config, error) {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return config.Config{}, fmt.Errorf("run config payload is empty")
	}

	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return config.Config{}, fmt.Errorf("decode run config payload: %w", err)
	}
	return parseRunConfigValue(decoded)
}

func parseRunConfigValue(v any) (config.Config, error) {
	m, err := normalizeRunConfigMap(v)
	if err != nil {
		return config.Config{}, err
	}

	encoded, err := json.Marshal(m)
	if err != nil {
		return config.Config{}, fmt.Errorf("marshal run config payload: %w", err)
	}

	var cfg config.Config
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return config.Config{}, fmt.Errorf("decode run config payload: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("validate run config payload: %w", err)
	}
	return cfg, nil
}

func normalizeRunConfigMap(v any) (map[string]any, error) {
	switch typed := v.(type) {
	case map[string]any:
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, fmt.Errorf("run config payload must be a JSON object")
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, fmt.Errorf("decode run config payload string: %w", err)
		}
		m, ok := parsed.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("run config payload must be a JSON object")
		}
		return m, nil
	default:
		return nil, fmt.Errorf("run config payload must be a JSON object")
	}
}

func extractConfigValue(msg map[string]any) (any, bool) {
	paths := [][]string{
		{"config"},
		{"input"},
		{"payload", "config"},
		{"payload", "input"},
		{"data", "config"},
		{"data", "input"},
	}
	for _, path := range paths {
		if value, ok := valueAtPath(msg, path...); ok {
			return value, true
		}
	}

	if payload, ok := valueAtPath(msg, "payload"); ok {
		if m, ok := payload.(map[string]any); ok && looksLikeRunConfigMap(m) {
			return m, true
		}
	}
	if data, ok := valueAtPath(msg, "data"); ok {
		if m, ok := data.(map[string]any); ok && looksLikeRunConfigMap(m) {
			return m, true
		}
	}
	if looksLikeRunConfigMap(msg) {
		return msg, true
	}
	return nil, false
}

func looksLikeRunConfigMap(v map[string]any) bool {
	prompt := firstNonEmpty(stringAt(v, "prompt"))
	repo := firstNonEmpty(stringAt(v, "repo"), stringAt(v, "repo_url"))
	return prompt != "" && (repo != "" || hasNonEmptyStringArray(v["repos"]))
}

func requiredSkillPayloadSchema(dispatchType, skillName string) map[string]any {
	dispatchType = strings.TrimSpace(dispatchType)
	if dispatchType == "" {
		dispatchType = "skill_request"
	}
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		skillName = "code_for_me"
	}

	return map[string]any{
		"dispatch_envelope": map[string]any{
			"type":  dispatchType,
			"skill": skillName,
		},
		"accepted_payload_paths": []string{
			"config",
			"input",
			"payload.config",
			"payload.input",
			"data.config",
			"data.input",
		},
		"run_config_schema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"prompt"},
			"anyOf": []map[string]any{
				{"required": []string{"repo"}},
				{"required": []string{"repo_url"}},
				{"required": []string{"repos"}},
			},
			"properties": map[string]any{
				"version": map[string]any{
					"type": "string",
					"enum": []string{"v1"},
				},
				"repo": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"repo_url": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"repos": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
				},
				"base_branch": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"target_subdir": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"prompt": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"commit_message": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"pr_title": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"pr_body": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"labels": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
				"reviewers": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
		},
	}
}

func valueAtPath(root map[string]any, path ...string) (any, bool) {
	if len(path) == 0 {
		return root, true
	}
	var current any = root
	for _, p := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[p]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func stringAt(root map[string]any, key string) string {
	value, ok := root[key]
	if !ok {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func hasNonEmptyStringArray(v any) bool {
	switch typed := v.(type) {
	case []string:
		for _, entry := range typed {
			if strings.TrimSpace(entry) != "" {
				return true
			}
		}
	case []any:
		for _, entry := range typed {
			s, ok := entry.(string)
			if ok && strings.TrimSpace(s) != "" {
				return true
			}
		}
	}
	return false
}

func stringAtPath(root map[string]any, path ...string) string {
	value, ok := valueAtPath(root, path...)
	if !ok {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func skillNamesEqual(a, b string) bool {
	return normalizeSkillMatcherName(a) == normalizeSkillMatcherName(b)
}

func normalizeSkillMatcherName(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "codex_harness_run", "code_for_me":
		return "code_for_me"
	default:
		return normalized
	}
}
