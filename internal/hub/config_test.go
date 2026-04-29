package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInitDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  // comments are allowed
  "bind_token": "bind_abc123"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if cfg.Version != "v1" {
		t.Fatalf("Version = %q", cfg.Version)
	}
	if cfg.BaseURL != "https://na.hub.molten.bot/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.SessionKey != "main" {
		t.Fatalf("SessionKey = %q", cfg.SessionKey)
	}
	if cfg.Skill.Name != "code_for_me" {
		t.Fatalf("Skill.Name = %q", cfg.Skill.Name)
	}
	if cfg.Skill.DispatchType != "skill_request" {
		t.Fatalf("Skill.DispatchType = %q", cfg.Skill.DispatchType)
	}
	if cfg.Skill.ResultType != "skill_result" {
		t.Fatalf("Skill.ResultType = %q", cfg.Skill.ResultType)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.Dispatcher.MaxParallel < 1 {
		t.Fatalf("Dispatcher.MaxParallel = %d, want >= 1", cfg.Dispatcher.MaxParallel)
	}
	if cfg.Dispatcher.MinParallel != 1 {
		t.Fatalf("Dispatcher.MinParallel = %d, want 1", cfg.Dispatcher.MinParallel)
	}
	if cfg.Dispatcher.SampleWindow != 5 {
		t.Fatalf("Dispatcher.SampleWindow = %d, want 5", cfg.Dispatcher.SampleWindow)
	}
	if cfg.Dispatcher.SampleIntervalMS != 1500 {
		t.Fatalf("Dispatcher.SampleIntervalMS = %d, want 1500", cfg.Dispatcher.SampleIntervalMS)
	}
	if cfg.Dispatcher.CPUHighWatermark != 85 {
		t.Fatalf("Dispatcher.CPUHighWatermark = %.2f, want 85", cfg.Dispatcher.CPUHighWatermark)
	}
	if cfg.Dispatcher.MemoryHighWatermark != 90 {
		t.Fatalf("Dispatcher.MemoryHighWatermark = %.2f, want 90", cfg.Dispatcher.MemoryHighWatermark)
	}
	if cfg.Dispatcher.DiskIOHighWatermarkMBs != 120 {
		t.Fatalf("Dispatcher.DiskIOHighWatermarkMBs = %.2f, want 120", cfg.Dispatcher.DiskIOHighWatermarkMBs)
	}
}

func TestDefaultDispatcherMaxParallelForCores(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		cores int
		want  int
	}{
		{name: "non-positive falls back to adaptive minimum", cores: 0, want: 2},
		{name: "single core allows light overlap", cores: 1, want: 2},
		{name: "dual core allows one extra slot", cores: 2, want: 3},
		{name: "multi core tracks core count", cores: 8, want: 8},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := defaultDispatcherMaxParallelForCores(tc.cores); got != tc.want {
				t.Fatalf("defaultDispatcherMaxParallelForCores(%d) = %d, want %d", tc.cores, got, tc.want)
			}
		})
	}
}

func TestLoadInitAllowsMissingTokensForRuntimeConfigFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "base_url": "https://na.hub.molten.bot/v1"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if cfg.BindToken != "" {
		t.Fatalf("BindToken = %q", cfg.BindToken)
	}
	if cfg.AgentToken != "" {
		t.Fatalf("AgentToken = %q", cfg.AgentToken)
	}
}

func TestValidateRejectsUnsupportedBaseURLScheme(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Version:    "v1",
		BaseURL:    "ftp://example.com/v1",
		BindToken:  "token",
		SessionKey: "main",
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{MaxParallel: 1},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoadInitAllowsAgentTokenWithoutBindToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "agent_token": "agent_live_token",
  "skill": {
    "name": "moltenhub_code_run"
  },
  "dispatcher": {
    "max_parallel": 4
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if cfg.AgentToken != "agent_live_token" {
		t.Fatalf("AgentToken = %q", cfg.AgentToken)
	}
	if cfg.Dispatcher.MaxParallel != 4 {
		t.Fatalf("Dispatcher.MaxParallel = %d", cfg.Dispatcher.MaxParallel)
	}
}

func TestValidateRejectsInvalidDispatcherThresholds(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Version:    "v1",
		BaseURL:    "https://na.hub.molten.bot/v1",
		BindToken:  "token",
		SessionKey: "main",
		Skill: SkillConfig{
			Name:         "moltenhub_code_run",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel:            2,
			MinParallel:            1,
			SampleWindow:           5,
			SampleIntervalMS:       500,
			CPUHighWatermark:       0,
			MemoryHighWatermark:    90,
			DiskIOHighWatermarkMBs: 100,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadInitRejectsUnknownLogLevel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_token": "agent_live_token",
  "log_level": "chatty"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	if _, err := LoadInit(path); err == nil {
		t.Fatal("LoadInit() error = nil, want invalid log_level error")
	} else if !strings.Contains(err.Error(), "log_level must be one of error, warn, info, debug") {
		t.Fatalf("LoadInit() error = %v", err)
	}
}

func TestLoadInitSupportsAgentHarnessAndCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_harness": "CLAUDE",
  "agent_command": "claude-custom"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if got, want := cfg.AgentHarness, "claude"; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := cfg.AgentCommand, "claude-custom"; got != want {
		t.Fatalf("AgentCommand = %q, want %q", got, want)
	}
}

func TestValidateRejectsUnknownAgentHarness(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		Version:      "v1",
		BaseURL:      "https://na.hub.molten.bot/v1",
		BindToken:    "token",
		AgentHarness: "unknown",
		SessionKey:   "main",
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel:            1,
			MinParallel:            1,
			SampleWindow:           1,
			SampleIntervalMS:       250,
			CPUHighWatermark:       85,
			MemoryHighWatermark:    90,
			DiskIOHighWatermarkMBs: 120,
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want unsupported agent harness error")
	}
	if !strings.Contains(err.Error(), "unsupported agentHarness") {
		t.Fatalf("Validate() error = %v, want unsupported agentHarness", err)
	}
}

func TestLoadInitReadsAgentRuntimeFromEnv(t *testing.T) {
	t.Setenv("HARNESS_AGENT_HARNESS", "AUGGIE")
	t.Setenv("HARNESS_AGENT_COMMAND", "auggie-custom")

	dir := t.TempDir()
	path := filepath.Join(dir, "init.json")
	data := `{
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_token": "token"
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	cfg, err := LoadInit(path)
	if err != nil {
		t.Fatalf("LoadInit() error = %v", err)
	}
	if got, want := cfg.AgentHarness, "auggie"; got != want {
		t.Fatalf("AgentHarness = %q, want %q", got, want)
	}
	if got, want := cfg.AgentCommand, "auggie-custom"; got != want {
		t.Fatalf("AgentCommand = %q, want %q", got, want)
	}
}

func TestApplyDefaultsDerivesProfileRuntimeFields(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		AgentHarness: "claude",
		Profile: ProfileConfig{
			DisplayName: "Molten Agent",
			Emoji:       "🤖",
			ProfileText: "Owns automation",
		},
	}
	cfg.ApplyDefaults()

	if got, want := cfg.Profile.LLM, "claude"; got != want {
		t.Fatalf("Profile.LLM = %q, want %q", got, want)
	}
	if got, want := cfg.Profile.Harness, runtimeIdentifier; got != want {
		t.Fatalf("Profile.Harness = %q, want %q", got, want)
	}
	if len(cfg.Profile.Skills) != 2 || cfg.Profile.Skills[0] != defaultRuntimeSkillName || cfg.Profile.Skills[1] != codeReviewSkillName {
		t.Fatalf("Profile.Skills = %#v, want [%q %q]", cfg.Profile.Skills, defaultRuntimeSkillName, codeReviewSkillName)
	}
}

func TestApplyDefaultsForcesImmutableProfileRuntimeFields(t *testing.T) {
	t.Parallel()

	cfg := InitConfig{
		AgentHarness: "codex",
		Profile: ProfileConfig{
			LLM:     "manual",
			Harness: "custom",
			Skills:  []string{"other"},
		},
	}
	cfg.ApplyDefaults()

	if got, want := cfg.Profile.LLM, "codex"; got != want {
		t.Fatalf("Profile.LLM = %q, want %q", got, want)
	}
	if got, want := cfg.Profile.Harness, runtimeIdentifier; got != want {
		t.Fatalf("Profile.Harness = %q, want %q", got, want)
	}
	if len(cfg.Profile.Skills) != 2 || cfg.Profile.Skills[0] != defaultRuntimeSkillName || cfg.Profile.Skills[1] != codeReviewSkillName {
		t.Fatalf("Profile.Skills = %#v, want [%q %q]", cfg.Profile.Skills, defaultRuntimeSkillName, codeReviewSkillName)
	}
}
