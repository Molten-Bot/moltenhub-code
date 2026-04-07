package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	runtimeConfigPath = "./.moltenhub/config.json"
	runtimeTimeoutMs  = 20000
	runtimeSessionKey = "main"
)

// RuntimeConfig is persisted after successful hub auth so subsequent runs can reuse the token.
type RuntimeConfig struct {
	BaseURL    string `json:"baseUrl"`
	Token      string `json:"token"`
	SessionKey string `json:"sessionKey"`
	TimeoutMs  int    `json:"timeoutMs"`
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

	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.SessionKey = strings.TrimSpace(cfg.SessionKey)

	if cfg.BaseURL == "" {
		return RuntimeConfig{}, fmt.Errorf("runtime config baseUrl is required")
	}
	if cfg.Token == "" {
		return RuntimeConfig{}, fmt.Errorf("runtime config token is required")
	}
	if cfg.SessionKey == "" {
		cfg.SessionKey = runtimeSessionKey
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = runtimeTimeoutMs
	}

	return cfg, nil
}

// SaveRuntimeConfig writes a normalized runtime config JSON file.
func SaveRuntimeConfig(path, baseURL, token, sessionKey string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultRuntimeConfigPath()
	}

	cfg := RuntimeConfig{
		BaseURL:    strings.TrimSpace(baseURL),
		Token:      strings.TrimSpace(token),
		SessionKey: strings.TrimSpace(sessionKey),
		TimeoutMs:  runtimeTimeoutMs,
	}
	if cfg.SessionKey == "" {
		cfg.SessionKey = runtimeSessionKey
	}
	if cfg.BaseURL == "" {
		return fmt.Errorf("baseUrl is required")
	}
	if cfg.Token == "" {
		return fmt.Errorf("token is required")
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
	return runtimeConfigPath
}
