package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/library"
)

// SkillDispatch represents one inbound skill request ready for execution.
type SkillDispatch struct {
	RequestID string
	Skill     string
	ReplyTo   string
	RouteTo   string
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
			stringAt(msg, "client_msg_id"),
			stringAt(msg, "clientMsgId"),
			stringAt(msg, "message_id"),
			stringAt(msg, "messageId"),
			stringAt(msg, "delivery_id"),
			stringAt(msg, "deliveryId"),
			stringAtPath(msg, "payload", "request_id"),
			stringAtPath(msg, "payload", "client_msg_id"),
			stringAtPath(msg, "payload", "clientMsgId"),
			stringAtPath(msg, "payload", "message_id"),
			stringAtPath(msg, "payload", "messageId"),
			stringAtPath(msg, "payload", "delivery_id"),
			stringAtPath(msg, "payload", "deliveryId"),
			stringAtPath(msg, "data", "request_id"),
			stringAtPath(msg, "data", "client_msg_id"),
			stringAtPath(msg, "data", "clientMsgId"),
			stringAtPath(msg, "data", "message_id"),
			stringAtPath(msg, "data", "messageId"),
			stringAtPath(msg, "data", "delivery_id"),
			stringAtPath(msg, "data", "deliveryId"),
		),
		Skill: firstNonEmpty(skillName, strings.TrimSpace(expectedSkill)),
		RouteTo: firstNonEmpty(
			stringAt(msg, "to"),
			stringAt(msg, "to_agent_uri"),
			stringAt(msg, "to_agent_uuid"),
			stringAt(msg, "to_agent_id"),
			stringAtPath(msg, "payload", "to"),
			stringAtPath(msg, "payload", "to_agent_uri"),
			stringAtPath(msg, "payload", "to_agent_uuid"),
			stringAtPath(msg, "payload", "to_agent_id"),
			stringAtPath(msg, "data", "to"),
			stringAtPath(msg, "data", "to_agent_uri"),
			stringAtPath(msg, "data", "to_agent_uuid"),
			stringAtPath(msg, "data", "to_agent_id"),
		),
		ReplyTo: firstNonEmpty(
			stringAt(msg, "reply_to"),
			stringAt(msg, "replyTo"),
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
			stringAtPath(msg, "payload", "source"),
			stringAtPath(msg, "payload", "source_agent_uri"),
			stringAtPath(msg, "payload", "source_agent_uuid"),
			stringAtPath(msg, "payload", "source_agent_id"),
			stringAtPath(msg, "payload", "from_agent_uri"),
			stringAtPath(msg, "payload", "from_agent_uuid"),
			stringAtPath(msg, "payload", "from_agent_id"),
			stringAtPath(msg, "data", "reply_to"),
			stringAtPath(msg, "data", "from"),
			stringAtPath(msg, "data", "source"),
			stringAtPath(msg, "data", "source_agent_uri"),
			stringAtPath(msg, "data", "source_agent_uuid"),
			stringAtPath(msg, "data", "source_agent_id"),
			stringAtPath(msg, "data", "from_agent_uri"),
			stringAtPath(msg, "data", "from_agent_uuid"),
			stringAtPath(msg, "data", "from_agent_id"),
			stringAt(msg, "to_agent_uri"),
			stringAt(msg, "to_agent_uuid"),
			stringAt(msg, "to_agent_id"),
			stringAtPath(msg, "payload", "to_agent_uri"),
			stringAtPath(msg, "payload", "to_agent_uuid"),
			stringAtPath(msg, "payload", "to_agent_id"),
			stringAtPath(msg, "data", "to_agent_uri"),
			stringAtPath(msg, "data", "to_agent_uuid"),
			stringAtPath(msg, "data", "to_agent_id"),
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

	cfg, err := parseRunConfigValue(configValue, dispatch.Skill)
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
	return parseRunConfigValue(decoded, "")
}

func parseRunConfigValue(v any, skillName string) (config.Config, error) {
	m, err := normalizeRunConfigMap(v, skillName)
	if err != nil {
		return config.Config{}, err
	}

	encoded, err := json.Marshal(m)
	if err != nil {
		return config.Config{}, fmt.Errorf("marshal run config payload: %w", err)
	}

	var cfg config.Config
	dec := json.NewDecoder(bytes.NewReader(encoded))
	if err := dec.Decode(&cfg); err != nil {
		return config.Config{}, fmt.Errorf("decode run config payload: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("validate run config payload: %w", err)
	}
	return cfg, nil
}

func normalizeRunConfigMap(v any, skillName string) (map[string]any, error) {
	var parsed map[string]any
	switch typed := v.(type) {
	case map[string]any:
		parsed = typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, fmt.Errorf("run config payload must be a JSON object")
		}
		var decoded any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
			return nil, fmt.Errorf("decode run config payload string: %w", err)
		}
		m, ok := decoded.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("run config payload must be a JSON object")
		}
		parsed = m
	default:
		return nil, fmt.Errorf("run config payload must be a JSON object")
	}

	if err := normalizeRunConfigAliases(parsed); err != nil {
		return nil, err
	}
	if err := applySkillSpecificRunConfigDefaults(parsed, skillName); err != nil {
		return nil, err
	}
	if taskName := firstNonEmpty(stringAt(parsed, "libraryTaskName")); taskName != "" {
		return expandLibraryTaskRunConfig(parsed, taskName)
	}
	return parsed, nil
}

func applySkillSpecificRunConfigDefaults(parsed map[string]any, skillName string) error {
	switch normalizeNamedSkill(skillName) {
	case codeReviewSkillName:
		if firstNonEmpty(stringAt(parsed, "prompt")) != "" {
			return fmt.Errorf("%s skill does not accept prompt; send repo + branch or prNumber", codeReviewSkillName)
		}
		if !ensureReviewSelector(parsed) {
			return fmt.Errorf("%s skill requires branch, prNumber, or review.prUrl", codeReviewSkillName)
		}
		if firstNonEmpty(stringAt(parsed, "libraryTaskName")) == "" {
			parsed["libraryTaskName"] = codeReviewLibraryTaskName
		}
	case libraryTaskSkillName:
		if firstNonEmpty(stringAt(parsed, "prompt")) != "" {
			return fmt.Errorf("%s skill does not accept prompt; send repo + branch + libraryTaskName", libraryTaskSkillName)
		}
		if firstNonEmpty(stringAt(parsed, "libraryTaskName")) == "" {
			return fmt.Errorf("%s skill requires libraryTaskName", libraryTaskSkillName)
		}
	}
	return nil
}

func extractConfigValue(msg map[string]any) (any, bool) {
	paths := [][]string{
		{"config"},
		{"input"},
		{"payload", "config"},
		{"payload", "input"},
		{"data", "config"},
		{"data", "input"},
		{"payload"},
		{"data"},
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
	if firstNonEmpty(stringAt(v, "libraryTaskName")) != "" {
		repo := firstNonEmpty(stringAtAny(v, "repo", "repoUrl"))
		return repo != "" || hasSingleNonEmptyStringArray(v["repos"])
	}
	prompt := firstNonEmpty(stringAt(v, "prompt"))
	repo := firstNonEmpty(stringAtAny(v, "repo", "repoUrl"))
	return prompt != "" && (repo != "" || hasNonEmptyStringArray(v["repos"]))
}

func requiredSkillPayloadSchema(dispatchType, skillName string, libraryTaskNames []string) map[string]any {
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
			"additionalProperties": true,
			"oneOf": []map[string]any{
				{"required": []string{"repo"}},
				{"required": []string{"repoUrl"}},
				{"required": []string{"repos"}},
			},
			"anyOf": []map[string]any{
				{"required": []string{"prompt"}},
				{"required": []string{"libraryTaskName"}},
			},
			"properties": map[string]any{
				"version": propertyStringEnum("v1"),
				"repo":    propertyNonEmptyString(),
				"repoUrl": propertyNonEmptyString(),
				"repos": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
				},
				"baseBranch": propertyNonEmptyString(),
				"branch":     propertyNonEmptyString(),
				"prNumber": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
				"targetSubdir": propertyNonEmptyString(),
				"agentHarness": propertyStringEnum("codex", "claude", "auggie", "pi"),
				"agentCommand": propertyNonEmptyString(),
				"prompt":       propertyNonEmptyString(),
				"responseMode": propertyStringEnum(config.SupportedResponseModesWithDefault()...),
				"images": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":       propertyNonEmptyString(),
							"mediaType":  propertyNonEmptyString(),
							"dataBase64": propertyNonEmptyString(),
						},
					},
				},
				"libraryTaskName": propertyStringEnum(libraryTaskNames...),
				"commitMessage":   propertyNonEmptyString(),
				"prTitle":         propertyNonEmptyString(),
				"prBody":          propertyNonEmptyString(),
				"labels": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
				"githubHandle": propertyNonEmptyString(),
				"reviewers": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
				"review": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"prNumber": map[string]any{
							"type":    "integer",
							"minimum": 1,
						},
						"prUrl":      propertyNonEmptyString(),
						"headBranch": propertyNonEmptyString(),
					},
					"anyOf": []map[string]any{
						{"required": []string{"prNumber"}},
						{"required": []string{"prUrl"}},
						{"required": []string{"headBranch"}},
					},
				},
			},
		},
	}
}

func propertyNonEmptyString() map[string]any {
	return map[string]any{
		"type":      "string",
		"minLength": 1,
	}
}

func propertyStringEnum(values ...string) map[string]any {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	if len(filtered) == 0 {
		return propertyNonEmptyString()
	}
	return map[string]any{
		"type": "string",
		"enum": filtered,
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

func stringAtAny(root map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringAt(root, key); value != "" {
			return value
		}
	}
	return ""
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

func hasSingleNonEmptyStringArray(v any) bool {
	return len(nonEmptyStringArray(v)) == 1
}

func normalizeRunConfigAliases(m map[string]any) error {
	if m == nil {
		return fmt.Errorf("run config payload must be a JSON object")
	}

	if firstNonEmpty(stringAt(m, "baseBranch")) == "" {
		if branch := firstNonEmpty(stringAt(m, "branch")); branch != "" {
			m["baseBranch"] = branch
		}
	}
	if firstNonEmpty(stringAt(m, "prompt")) != "" && firstNonEmpty(stringAt(m, "libraryTaskName")) != "" {
		return fmt.Errorf("run config payload cannot include both prompt and libraryTaskName")
	}
	if prNumber, ok := positiveIntValue(m["prNumber"]); ok {
		review := ensureReviewMap(m)
		if _, exists := review["prNumber"]; !exists {
			review["prNumber"] = prNumber
		}
	}
	return nil
}

func ensureReviewSelector(m map[string]any) bool {
	if m == nil {
		return false
	}
	review := ensureReviewMap(m)
	if _, ok := positiveIntValue(review["prNumber"]); ok {
		return true
	}
	if firstNonEmpty(stringAt(review, "prUrl"), stringAt(review, "headBranch")) != "" {
		return true
	}
	if branch := firstNonEmpty(stringAt(m, "branch"), stringAt(m, "baseBranch")); branch != "" {
		review["headBranch"] = branch
		return true
	}
	return false
}

func ensureReviewMap(m map[string]any) map[string]any {
	if existing, ok := m["review"].(map[string]any); ok {
		return existing
	}
	review := map[string]any{}
	m["review"] = review
	return review
}

func positiveIntValue(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, typed > 0
	case int32:
		value := int(typed)
		return value, value > 0
	case int64:
		value := int(typed)
		return value, value > 0
	case float64:
		value := int(typed)
		return value, typed == float64(value) && value > 0
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		intValue := int(value)
		return intValue, intValue > 0
	default:
		return 0, false
	}
}

func expandLibraryTaskRunConfig(m map[string]any, taskName string) (map[string]any, error) {
	repo := firstNonEmpty(stringAtAny(m, "repo", "repoUrl"))
	repos := nonEmptyStringArray(m["repos"])
	if repo == "" {
		if len(repos) == 1 {
			repo = repos[0]
		} else if len(repos) > 1 {
			repo = repos[0]
		}
	}

	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err != nil {
		return nil, fmt.Errorf("load library catalog: %w", err)
	}
	cfg, err := catalog.ExpandRunConfig(taskName, repo, firstNonEmpty(stringAt(m, "baseBranch"), stringAt(m, "branch")))
	if err != nil {
		return nil, err
	}
	expanded, err := configToMap(cfg)
	if err != nil {
		return nil, err
	}
	for key, value := range m {
		if key == "libraryTaskName" {
			continue
		}
		if _, exists := expanded[key]; exists {
			continue
		}
		expanded[key] = value
	}
	if len(repos) > 0 {
		expanded["repos"] = repos
	}
	return expanded, nil
}

func configToMap(cfg config.Config) (map[string]any, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized run config: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("decode normalized run config: %w", err)
	}
	return out, nil
}

func nonEmptyStringArray(v any) []string {
	switch typed := v.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			entry = strings.TrimSpace(entry)
			if entry != "" {
				out = append(out, entry)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			s, ok := entry.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
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
	normalized := normalizeNamedSkill(value)
	switch normalized {
	case defaultRuntimeSkillName, codeReviewSkillName, libraryTaskSkillName:
		return defaultRuntimeSkillName
	default:
		return normalized
	}
}

func normalizeNamedSkill(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "moltenhub_code_run", "code_for_me":
		return "code_for_me"
	case "code_review":
		return codeReviewSkillName
	case "library_task":
		return libraryTaskSkillName
	default:
		return normalized
	}
}
