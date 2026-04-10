package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	runtimeConfigPath           = "./.moltenhub/config.json"
	legacyRuntimeConfigPath     = "./.moltenhub/config/config.json"
	runtimeConfigPathEnv        = "HARNESS_RUNTIME_CONFIG_PATH"
	runtimeTimeoutMs            = 20000
	runtimeSessionKey           = "main"
	transportOfflineReasonAgent = "harness_shutdown"
)

// RuntimeConfig is persisted after successful hub auth so subsequent runs can
// start directly from config.json without requiring init.json again.
type RuntimeConfig struct {
	InitConfig
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// UnmarshalJSON accepts the current init-style snake_case config and the legacy
// minimal runtime config shape.
func (c *RuntimeConfig) UnmarshalJSON(data []byte) error {
	type runtimeAlias RuntimeConfig
	var parsed runtimeAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*c = RuntimeConfig(parsed)

	var legacy struct {
		BaseURL    string `json:"baseUrl"`
		Token      string `json:"token"`
		SessionKey string `json:"sessionKey"`
		TimeoutMs  int    `json:"timeoutMs"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}

	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = legacy.BaseURL
	}
	if strings.TrimSpace(c.AgentToken) == "" {
		c.AgentToken = legacy.Token
	}
	if strings.TrimSpace(c.SessionKey) == "" {
		c.SessionKey = legacy.SessionKey
	}
	if c.TimeoutMs <= 0 {
		c.TimeoutMs = legacy.TimeoutMs
	}

	return nil
}

// ApplyDefaults normalizes a persisted runtime config.
func (c *RuntimeConfig) ApplyDefaults() {
	c.InitConfig.ApplyDefaults()
	if c.TimeoutMs <= 0 {
		c.TimeoutMs = runtimeTimeoutMs
	}
}

// Validate checks required values for hub boot from config.json.
func (c RuntimeConfig) Validate() error {
	if err := c.InitConfig.Validate(); err != nil {
		return err
	}
	if c.TimeoutMs <= 0 {
		return fmt.Errorf("runtime config timeout_ms must be > 0")
	}
	return nil
}

func (c RuntimeConfig) Init() InitConfig {
	initCfg := c.InitConfig
	initCfg.RuntimeConfigPath = c.RuntimeConfigPath
	return initCfg
}

// LoadRuntimeConfig reads and validates a persisted runtime config JSON file.
func LoadRuntimeConfig(path string) (RuntimeConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("read runtime config: %w", err)
	}

	var cfg RuntimeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RuntimeConfig{}, fmt.Errorf("parse runtime config: %w", err)
	}

	cfg.RuntimeConfigPath = path
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return RuntimeConfig{}, err
	}

	return cfg, nil
}

// SaveRuntimeConfig writes a normalized, hub-bootable runtime config JSON file.
func SaveRuntimeConfig(path string, initCfg InitConfig, token string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	token = strings.TrimSpace(token)
	cfg := RuntimeConfig{
		InitConfig: initCfg,
		TimeoutMs:  runtimeTimeoutMs,
	}
	cfg.RuntimeConfigPath = path
	cfg.AgentToken = token
	cfg.ApplyDefaults()
	if strings.TrimSpace(cfg.AgentToken) == "" && strings.TrimSpace(cfg.BindToken) == "" {
		return fmt.Errorf("runtime config requires agent_token or bind_token")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	data = append(data, '\n')
	return writeRuntimeConfigFile(path, data)
}

// SaveRuntimeConfigHubSettings persists hub credentials plus handle/profile
// fields while preserving unrelated runtime config keys already on disk.
func SaveRuntimeConfigHubSettings(path string, initCfg InitConfig, resolvedAgentToken string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	initCfg.ApplyDefaults()
	resolvedAgentToken = strings.TrimSpace(resolvedAgentToken)
	if resolvedAgentToken == "" && strings.TrimSpace(initCfg.BindToken) == "" {
		return fmt.Errorf("runtime config requires agent_token or bind_token")
	}

	doc, err := loadRuntimeConfigDoc(path, initCfg)
	if err != nil {
		return err
	}

	doc["version"] = initCfg.Version
	doc["base_url"] = initCfg.BaseURL
	doc["session_key"] = initCfg.SessionKey
	doc["handle"] = initCfg.Handle
	doc["profile"] = map[string]any{
		"bio":          initCfg.Profile.Bio,
		"display_name": initCfg.Profile.DisplayName,
		"emoji":        initCfg.Profile.Emoji,
		"llm":          initCfg.Profile.LLM,
		"harness":      initCfg.Profile.Harness,
		"skills":       append([]string(nil), initCfg.Profile.Skills...),
	}
	doc["agent_token"] = resolvedAgentToken
	if strings.TrimSpace(initCfg.BindToken) != "" {
		doc["bind_token"] = initCfg.BindToken
	} else {
		delete(doc, "bind_token")
	}
	if strings.TrimSpace(initCfg.AgentHarness) != "" {
		doc["agent_harness"] = initCfg.AgentHarness
	}
	if strings.TrimSpace(initCfg.AgentCommand) != "" {
		doc["agent_command"] = initCfg.AgentCommand
	}
	if _, ok := doc["timeout_ms"]; !ok {
		doc["timeout_ms"] = runtimeTimeoutMs
	}
	if dispatcherDoc, ok := runtimeConfigDispatcherDoc(initCfg); ok {
		doc["dispatcher"] = dispatcherDoc
	}

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
}

// SaveRuntimeConfigAuggieAuth persists augment_session_auth to the runtime
// config JSON while preserving other configuration fields.
func SaveRuntimeConfigAuggieAuth(path string, initCfg InitConfig, augmentSessionAuth string) error {
	return saveRuntimeConfigStringField(
		path,
		initCfg,
		augmentSessionAuth,
		"augment session auth is required",
		"augment_session_auth",
	)
}

// SaveRuntimeConfigGitHubToken persists github_token to the runtime config JSON
// while preserving other configuration fields.
func SaveRuntimeConfigGitHubToken(path string, initCfg InitConfig, gitHubToken string) error {
	return saveRuntimeConfigStringField(
		path,
		initCfg,
		gitHubToken,
		"github token is required",
		"github_token",
	)
}

// ReadRuntimeConfigString returns the first non-empty string for the provided
// keys from a runtime config file. It returns an empty string when the file
// cannot be read or parsed.
func ReadRuntimeConfigString(path string, keys ...string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if len(keys) == 0 {
		return ""
	}

	doc, err := readRuntimeConfigDoc(path)
	if err != nil {
		return ""
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value := docStringValue(doc[key]); value != "" {
			return value
		}
	}
	return ""
}

func saveRuntimeConfigStringField(
	path string,
	initCfg InitConfig,
	value string,
	emptyErr string,
	field string,
) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New(emptyErr)
	}

	doc, err := loadRuntimeConfigDoc(path, initCfg)
	if err != nil {
		return err
	}
	doc[field] = value

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
}

func loadRuntimeConfigDoc(path string, initCfg InitConfig) (map[string]any, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		doc := map[string]any{}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse runtime config: %w", err)
		}
		return doc, nil
	case errors.Is(err, os.ErrNotExist):
		baseDoc, buildErr := runtimeConfigBaseDoc(initCfg)
		if buildErr != nil {
			return nil, buildErr
		}
		return baseDoc, nil
	default:
		return nil, fmt.Errorf("read runtime config: %w", err)
	}
}

func readRuntimeConfigDoc(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	doc := map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func docStringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// SaveRuntimeConfigClaudeOAuthToken persists claude_code_oauth_token to the
// runtime config JSON while preserving other configuration fields.
func SaveRuntimeConfigClaudeOAuthToken(path string, initCfg InitConfig, oauthToken string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	oauthToken = strings.TrimSpace(oauthToken)
	if oauthToken == "" {
		return fmt.Errorf("claude oauth token is required")
	}

	doc := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse runtime config: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		baseDoc, buildErr := runtimeConfigBaseDoc(initCfg)
		if buildErr != nil {
			return buildErr
		}
		doc = baseDoc
	default:
		return fmt.Errorf("read runtime config: %w", err)
	}

	doc["claude_code_oauth_token"] = oauthToken

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
}

func runtimeConfigBaseDoc(initCfg InitConfig) (map[string]any, error) {
	initCfg.ApplyDefaults()
	encoded, err := json.Marshal(initCfg)
	if err != nil {
		return nil, fmt.Errorf("encode runtime config base: %w", err)
	}

	doc := map[string]any{}
	if err := json.Unmarshal(encoded, &doc); err != nil {
		return nil, fmt.Errorf("decode runtime config base: %w", err)
	}
	return doc, nil
}

func runtimeConfigDispatcherDoc(initCfg InitConfig) (map[string]any, bool) {
	baseDoc, err := runtimeConfigBaseDoc(initCfg)
	if err != nil {
		return nil, false
	}
	dispatcherDoc, ok := baseDoc["dispatcher"].(map[string]any)
	return dispatcherDoc, ok
}

func writeRuntimeConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create runtime config dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp runtime config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp runtime config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp runtime config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp runtime config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace runtime config: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod runtime config: %w", err)
	}
	return nil
}

func defaultRuntimeConfigPath() string {
	if path := strings.TrimSpace(os.Getenv(runtimeConfigPathEnv)); path != "" {
		return path
	}
	return runtimeConfigPath
}

func ResolveRuntimeConfigPath(initPath string) string {
	if path := strings.TrimSpace(os.Getenv(runtimeConfigPathEnv)); path != "" {
		return path
	}

	initPath = strings.TrimSpace(initPath)
	if initPath == "" {
		return runtimeConfigPath
	}

	return filepath.Join(filepath.Dir(initPath), "config.json")
}

func runtimeConfigCandidatePaths(path string) []string {
	path = strings.TrimSpace(path)
	if path != "" {
		candidates := []string{path}
		if legacyPath := legacyRuntimeConfigPathFor(path); legacyPath != "" && legacyPath != path {
			candidates = append(candidates, legacyPath)
		}
		return candidates
	}
	return []string{runtimeConfigPath, legacyRuntimeConfigPath}
}

func legacyRuntimeConfigPathFor(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return legacyRuntimeConfigPath
	}
	if path == runtimeConfigPath {
		return legacyRuntimeConfigPath
	}
	return filepath.Join(filepath.Dir(path), "config", filepath.Base(path))
}
