package hub

import (
	"encoding/json"
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
	if strings.TrimSpace(c.AgentToken) == "" && strings.TrimSpace(c.BindToken) == "" {
		return fmt.Errorf("runtime config requires agent_token or bind_token")
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

	cfg := RuntimeConfig{
		InitConfig: initCfg,
		TimeoutMs:  runtimeTimeoutMs,
	}
	cfg.RuntimeConfigPath = path
	cfg.AgentToken = strings.TrimSpace(token)
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	data = append(data, '\n')

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
