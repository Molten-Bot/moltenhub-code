package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
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
	TimeoutMs        int            `json:"timeout_ms,omitempty"`
	LibraryTaskUsage map[string]int `json:"library_task_usage,omitempty"`
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
		LogLevel   string `json:"logLevel"`
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
	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = legacy.LogLevel
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
	_ = ensureRuntimeConfigLogLevelPersisted(path, cfg.LogLevel)

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
	cfg.LibraryTaskUsage = ReadRuntimeConfigLibraryTaskUsage(path)
	cfg.ApplyDefaults()
	if strings.TrimSpace(cfg.AgentHarness) == "" {
		return fmt.Errorf(unboundAgentRuntimeErrorMessage)
	}
	if strings.TrimSpace(cfg.AgentToken) == "" && strings.TrimSpace(cfg.BindToken) == "" {
		return fmt.Errorf("runtime config requires agent_token or bind_token")
	}
	if err := ValidatePersistedHubBaseURL(cfg.BaseURL); err != nil {
		return err
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
	if strings.TrimSpace(initCfg.AgentHarness) == "" {
		return fmt.Errorf(unboundAgentRuntimeErrorMessage)
	}
	resolvedAgentToken = strings.TrimSpace(resolvedAgentToken)
	if resolvedAgentToken == "" && strings.TrimSpace(initCfg.BindToken) == "" {
		return fmt.Errorf("runtime config requires agent_token or bind_token")
	}
	if err := ValidatePersistedHubBaseURL(initCfg.BaseURL); err != nil {
		return err
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
		"profile":      initCfg.Profile.ProfileText,
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
	ensureRuntimeConfigLogLevel(doc, initCfg.LogLevel)

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
}

// ClearRuntimeConfigHubSettings removes saved Hub identity, credentials, and
// profile fields while preserving unrelated runtime configuration.
func ClearRuntimeConfigHubSettings(path string, initCfg InitConfig) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	initCfg.ApplyDefaults()
	doc, err := loadRuntimeConfigDoc(path, initCfg)
	if err != nil {
		return err
	}

	if docStringValue(doc["version"]) == "" {
		doc["version"] = initCfg.Version
	}
	if docStringValue(doc["base_url"]) == "" && docStringValue(doc["baseUrl"]) == "" {
		doc["base_url"] = initCfg.BaseURL
	}
	if docStringValue(doc["session_key"]) == "" && docStringValue(doc["sessionKey"]) == "" {
		doc["session_key"] = initCfg.SessionKey
	}
	if docStringValue(doc["agent_harness"]) == "" && docStringValue(doc["agentHarness"]) == "" && strings.TrimSpace(initCfg.AgentHarness) != "" {
		doc["agent_harness"] = initCfg.AgentHarness
	}
	if docStringValue(doc["agent_command"]) == "" && docStringValue(doc["agentCommand"]) == "" && strings.TrimSpace(initCfg.AgentCommand) != "" {
		doc["agent_command"] = initCfg.AgentCommand
	}
	if _, ok := doc["timeout_ms"]; !ok {
		if _, legacyOK := doc["timeoutMs"]; !legacyOK {
			doc["timeout_ms"] = runtimeTimeoutMs
		}
	}
	ensureRuntimeConfigLogLevel(doc, initCfg.LogLevel)

	for _, key := range []string{
		"agent_token",
		"agentToken",
		"bind_token",
		"bindToken",
		"token",
		"handle",
		"profile",
		"profile_markdown",
		"display_name",
		"emoji",
	} {
		delete(doc, key)
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

// SaveRuntimeConfigPiAuthJSON persists pi_auth_json to the runtime
// config JSON while preserving other configuration fields.
func SaveRuntimeConfigPiAuthJSON(path string, initCfg InitConfig, piAuthJSON string) error {
	return saveRuntimeConfigStringField(
		path,
		initCfg,
		piAuthJSON,
		"pi auth json is required",
		"pi_auth_json",
	)
}

// SaveRuntimeConfigPiProviderAuth persists pi_provider_auth to the runtime
// config JSON while preserving other configuration fields.
func SaveRuntimeConfigPiProviderAuth(path string, initCfg InitConfig, piProviderAuth string) error {
	return saveRuntimeConfigStringField(
		path,
		initCfg,
		piProviderAuth,
		"pi provider auth is required",
		"pi_provider_auth",
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

// SaveRuntimeConfigAgentRuntime persists agent_harness/agent_command to the
// runtime config while preserving other configuration fields. Once configured,
// switching to a different harness or command through this API is rejected.
func SaveRuntimeConfigAgentRuntime(path string, initCfg InitConfig, harness, command string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	runtime, err := agentruntime.Resolve(harness, command)
	if err != nil {
		return err
	}

	doc, err := loadRuntimeConfigDoc(path, initCfg)
	if err != nil {
		return err
	}

	existingHarness := strings.ToLower(docStringValue(doc["agent_harness"]))
	if existingHarness == "" {
		existingHarness = strings.ToLower(docStringValue(doc["agentHarness"]))
	}
	existingCommand := docStringValue(doc["agent_command"])
	if existingCommand == "" {
		existingCommand = docStringValue(doc["agentCommand"])
	}

	if existingHarness != "" && existingHarness != runtime.Harness {
		return fmt.Errorf("agent harness is already configured as %q", existingHarness)
	}
	if existingHarness != "" && existingCommand != "" && existingCommand != runtime.Command {
		return fmt.Errorf(
			"agent command is already configured as %q for harness %q",
			existingCommand,
			existingHarness,
		)
	}

	doc["agent_harness"] = runtime.Harness
	doc["agent_command"] = runtime.Command
	ensureRuntimeConfigLogLevel(doc, initCfg.LogLevel)

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
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

// ReadRuntimeConfigLibraryTaskUsage returns normalized per-task usage counts
// from the persisted runtime config. Invalid or non-positive entries are
// ignored.
func ReadRuntimeConfigLibraryTaskUsage(path string) map[string]int {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	doc, err := readRuntimeConfigDoc(path)
	if err != nil {
		return nil
	}
	return runtimeConfigLibraryTaskUsage(doc["library_task_usage"])
}

// IncrementRuntimeConfigLibraryTaskUsage records one successful local selection
// of a library task in config.json while preserving unrelated runtime config
// settings already stored on disk.
func IncrementRuntimeConfigLibraryTaskUsage(path string, initCfg InitConfig, taskName string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	taskName = strings.TrimSpace(taskName)
	if taskName == "" {
		return fmt.Errorf("library task name is required")
	}

	doc, err := loadRuntimeConfigDoc(path, initCfg)
	if err != nil {
		return err
	}

	usage := runtimeConfigLibraryTaskUsage(doc["library_task_usage"])
	if usage == nil {
		usage = map[string]int{}
	}
	usage[taskName]++
	doc["library_task_usage"] = usage
	ensureRuntimeConfigLogLevel(doc, initCfg.LogLevel)

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
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
	ensureRuntimeConfigLogLevel(doc, initCfg.LogLevel)

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

func ensureRuntimeConfigLogLevel(doc map[string]any, preferred string) {
	if doc == nil {
		return
	}
	if existing := runtimeConfigLogLevelFromDoc(doc); existing != "" {
		doc["log_level"] = existing
		return
	}
	level := NormalizeLogLevel(preferred)
	if level == "" {
		level = DefaultLogLevel
	}
	doc["log_level"] = level
}

func runtimeConfigLogLevelFromDoc(doc map[string]any) string {
	if doc == nil {
		return ""
	}
	for _, key := range []string{"log_level", "logLevel", "LOG_LEVEL"} {
		if level := NormalizeLogLevel(docStringValue(doc[key])); level != "" {
			return level
		}
	}
	return ""
}

func ensureRuntimeConfigLogLevelPersisted(path, preferred string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	doc, err := readRuntimeConfigDoc(path)
	if err != nil {
		return fmt.Errorf("read runtime config: %w", err)
	}
	if runtimeConfigLogLevelFromDoc(doc) != "" {
		return nil
	}
	ensureRuntimeConfigLogLevel(doc, preferred)
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeRuntimeConfigFile(path, encoded)
}

func runtimeConfigLibraryTaskUsage(v any) map[string]int {
	doc, ok := v.(map[string]any)
	if !ok || len(doc) == 0 {
		return nil
	}

	usage := make(map[string]int, len(doc))
	for rawName, rawCount := range doc {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		count, ok := runtimeConfigLibraryTaskUsageCount(rawCount)
		if !ok || count <= 0 {
			continue
		}
		usage[name] = count
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}

func runtimeConfigLibraryTaskUsageCount(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
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
	ensureRuntimeConfigLogLevel(doc, initCfg.LogLevel)

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
