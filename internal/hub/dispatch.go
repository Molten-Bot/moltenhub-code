package hub

import (
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

// ParseSkillDispatch parses a websocket JSON message into a runnable dispatch.
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
	if strings.TrimSpace(expectedType) != "" && eventType != "" && !strings.EqualFold(eventType, expectedType) {
		return SkillDispatch{}, false, nil
	}

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
	if strings.TrimSpace(expectedSkill) != "" {
		if skillName == "" {
			skillName = strings.TrimSpace(expectedSkill)
		} else if !strings.EqualFold(skillName, expectedSkill) {
			return SkillDispatch{}, false, nil
		}
	}

	configValue, ok := extractConfigValue(msg)
	if !ok {
		return SkillDispatch{}, false, nil
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
			stringAt(msg, "from"),
			stringAt(msg, "source"),
			stringAt(msg, "source_agent_id"),
			stringAtPath(msg, "payload", "reply_to"),
			stringAtPath(msg, "payload", "from"),
			stringAtPath(msg, "data", "reply_to"),
			stringAtPath(msg, "data", "from"),
		),
	}

	cfg, err := parseRunConfigValue(configValue)
	if err != nil {
		return dispatch, true, err
	}
	dispatch.Config = cfg
	return dispatch, true, nil
}

func parseRunConfigValue(v any) (config.Config, error) {
	if path, ok := v.(string); ok && strings.TrimSpace(path) != "" {
		return config.Load(strings.TrimSpace(path))
	}

	if m, ok := v.(map[string]any); ok {
		if path := firstNonEmpty(stringAt(m, "config_path"), stringAt(m, "path")); path != "" && !looksLikeRunConfigMap(m) {
			return config.Load(path)
		}
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		return config.Config{}, fmt.Errorf("marshal run config payload: %w", err)
	}

	var cfg config.Config
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		return config.Config{}, fmt.Errorf("decode run config payload: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func extractConfigValue(msg map[string]any) (any, bool) {
	paths := [][]string{
		{"config"},
		{"run_config"},
		{"input"},
		{"payload", "config"},
		{"payload", "run_config"},
		{"payload", "input"},
		{"data", "config"},
		{"data", "run_config"},
		{"data", "input"},
		{"config_path"},
		{"payload", "config_path"},
		{"data", "config_path"},
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
	prompt := firstNonEmpty(stringAt(v, "prompt"), stringAtPath(v, "config", "prompt"))
	repo := firstNonEmpty(stringAt(v, "repo"), stringAt(v, "repo_url"), stringAt(v, "repoUrl"))
	return prompt != "" && repo != ""
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
