package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
)

// InitConfig is the init.json contract for hub runtime mode.
type InitConfig struct {
	Version    string           `json:"version"`
	BaseURL    string           `json:"base_url"`
	BindToken  string           `json:"bind_token"`
	AgentToken string           `json:"agent_token"`
	SessionKey string           `json:"session_key"`
	Handle     string           `json:"handle"`
	Profile    ProfileConfig    `json:"profile"`
	Skill      SkillConfig      `json:"skill"`
	Dispatcher DispatcherConfig `json:"dispatcher"`
}

// ProfileConfig controls optional agent profile sync on startup.
type ProfileConfig struct {
	DisplayName string         `json:"display_name"`
	Emoji       string         `json:"emoji"`
	Bio         string         `json:"bio"`
	Metadata    map[string]any `json:"metadata"`
}

// SkillConfig defines the inbound dispatch and outbound result contract.
type SkillConfig struct {
	Name         string `json:"name"`
	DispatchType string `json:"dispatch_type"`
	ResultType   string `json:"result_type"`
}

// DispatcherConfig controls local worker behavior.
type DispatcherConfig struct {
	MaxParallel            int     `json:"max_parallel"`
	MinParallel            int     `json:"min_parallel"`
	SampleWindow           int     `json:"sample_window"`
	SampleIntervalMS       int     `json:"sample_interval_ms"`
	CPUHighWatermark       float64 `json:"cpu_high_watermark"`
	MemoryHighWatermark    float64 `json:"memory_high_watermark"`
	DiskIOHighWatermarkMBs float64 `json:"disk_io_high_watermark_mb_s"`
}

// LoadInit reads and validates JSON/JSONC init config.
func LoadInit(path string) (InitConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InitConfig{}, fmt.Errorf("read init config: %w", err)
	}

	cleaned := stripLineComments(data)
	var cfg InitConfig
	dec := json.NewDecoder(bytes.NewReader(cleaned))
	if err := dec.Decode(&cfg); err != nil {
		return InitConfig{}, fmt.Errorf("parse init json: %w", err)
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return InitConfig{}, err
	}
	return cfg, nil
}

// ApplyDefaults normalizes and fills optional values.
func (c *InitConfig) ApplyDefaults() {
	c.Version = strings.TrimSpace(c.Version)
	if c.Version == "" {
		c.Version = "v1"
	}

	c.BaseURL = strings.TrimSpace(c.BaseURL)
	if c.BaseURL == "" {
		c.BaseURL = "https://na.hub.molten.bot/v1"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")

	c.BindToken = strings.TrimSpace(c.BindToken)
	c.AgentToken = strings.TrimSpace(c.AgentToken)
	c.SessionKey = strings.TrimSpace(c.SessionKey)
	if c.SessionKey == "" {
		c.SessionKey = "main"
	}

	c.Handle = strings.TrimSpace(c.Handle)
	c.Profile.DisplayName = strings.TrimSpace(c.Profile.DisplayName)
	c.Profile.Emoji = strings.TrimSpace(c.Profile.Emoji)
	c.Profile.Bio = strings.TrimSpace(c.Profile.Bio)
	if c.Profile.Metadata == nil {
		c.Profile.Metadata = map[string]any{}
	}

	c.Skill.Name = strings.TrimSpace(c.Skill.Name)
	if c.Skill.Name == "" {
		c.Skill.Name = "code_for_me"
	}
	c.Skill.DispatchType = strings.TrimSpace(c.Skill.DispatchType)
	if c.Skill.DispatchType == "" {
		c.Skill.DispatchType = "skill_request"
	}
	c.Skill.ResultType = strings.TrimSpace(c.Skill.ResultType)
	if c.Skill.ResultType == "" {
		c.Skill.ResultType = "skill_result"
	}

	if c.Dispatcher.MinParallel < 1 {
		c.Dispatcher.MinParallel = 1
	}
	if c.Dispatcher.MaxParallel < 1 {
		c.Dispatcher.MaxParallel = defaultDispatcherMaxParallel()
	}
	if c.Dispatcher.MaxParallel < c.Dispatcher.MinParallel {
		c.Dispatcher.MaxParallel = c.Dispatcher.MinParallel
	}
	if c.Dispatcher.SampleWindow < 1 {
		c.Dispatcher.SampleWindow = 5
	}
	if c.Dispatcher.SampleIntervalMS < 250 {
		c.Dispatcher.SampleIntervalMS = 1500
	}
	if c.Dispatcher.CPUHighWatermark <= 0 {
		c.Dispatcher.CPUHighWatermark = 85
	}
	if c.Dispatcher.MemoryHighWatermark <= 0 {
		c.Dispatcher.MemoryHighWatermark = 90
	}
	if c.Dispatcher.DiskIOHighWatermarkMBs <= 0 {
		c.Dispatcher.DiskIOHighWatermarkMBs = 120
	}
}

// Validate checks required values.
func (c InitConfig) Validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if c.Version != "v1" {
		return fmt.Errorf("unsupported version %q", c.Version)
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url must use http or https")
	}
	if strings.TrimSpace(c.Skill.Name) == "" {
		return fmt.Errorf("skill.name is required")
	}
	if strings.TrimSpace(c.Skill.DispatchType) == "" {
		return fmt.Errorf("skill.dispatch_type is required")
	}
	if strings.TrimSpace(c.Skill.ResultType) == "" {
		return fmt.Errorf("skill.result_type is required")
	}
	if c.Dispatcher.MaxParallel < 1 {
		return fmt.Errorf("dispatcher.max_parallel must be >= 1")
	}
	if c.Dispatcher.MinParallel < 1 {
		return fmt.Errorf("dispatcher.min_parallel must be >= 1")
	}
	if c.Dispatcher.MinParallel > c.Dispatcher.MaxParallel {
		return fmt.Errorf("dispatcher.min_parallel must be <= dispatcher.max_parallel")
	}
	if c.Dispatcher.SampleWindow < 1 {
		return fmt.Errorf("dispatcher.sample_window must be >= 1")
	}
	if c.Dispatcher.SampleIntervalMS < 250 {
		return fmt.Errorf("dispatcher.sample_interval_ms must be >= 250")
	}
	if c.Dispatcher.CPUHighWatermark <= 0 || c.Dispatcher.CPUHighWatermark > 100 {
		return fmt.Errorf("dispatcher.cpu_high_watermark must be > 0 and <= 100")
	}
	if c.Dispatcher.MemoryHighWatermark <= 0 || c.Dispatcher.MemoryHighWatermark > 100 {
		return fmt.Errorf("dispatcher.memory_high_watermark must be > 0 and <= 100")
	}
	if c.Dispatcher.DiskIOHighWatermarkMBs <= 0 {
		return fmt.Errorf("dispatcher.disk_io_high_watermark_mb_s must be > 0")
	}
	return nil
}

func defaultDispatcherMaxParallel() int {
	cores := runtime.NumCPU()
	switch {
	case cores <= 1:
		return 1
	case cores == 2:
		return 2
	default:
		return cores
	}
}

func stripLineComments(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}

		if ch == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) && data[i] == '\n' {
				out = append(out, '\n')
			}
			continue
		}

		out = append(out, ch)
	}

	return out
}
