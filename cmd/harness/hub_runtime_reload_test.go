package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func TestHubRuntimeConfigReloaderConnectsOnConfigChange(t *testing.T) {
	t.Parallel()

	loads := []struct {
		cfg hub.InitConfig
		err error
	}{
		{cfg: hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1"}},
		{cfg: hub.InitConfig{BaseURL: "https://eu.hub.molten.bot/v1", AgentToken: "agent_saved"}},
	}
	var (
		loadIndex int
		applied   []hub.InitConfig
	)

	r := &hubRuntimeConfigReloader{
		baseCfg: hub.InitConfig{RuntimeConfigPath: "/tmp/config.json"},
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			got := loads[loadIndex]
			if loadIndex < len(loads)-1 {
				loadIndex++
			}
			return got.cfg, got.err
		},
		apply: func(_ context.Context, cfg hub.InitConfig) error {
			applied = append(applied, cfg)
			return nil
		},
		stop: func(context.Context) error {
			t.Fatal("stop should not be called")
			return nil
		},
	}

	r.Poll(context.Background())
	r.Poll(context.Background())

	if got, want := len(applied), 1; got != want {
		t.Fatalf("len(applied) = %d, want %d", got, want)
	}
	if got, want := applied[0].AgentToken, "agent_saved"; got != want {
		t.Fatalf("applied token = %q, want %q", got, want)
	}
	if got, want := applied[0].BaseURL, "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("applied base_url = %q, want %q", got, want)
	}
}

func TestHubRuntimeConfigReloaderDisconnectsWhenCredentialsDisappear(t *testing.T) {
	t.Parallel()

	loads := []hub.InitConfig{
		{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "agent_saved"},
		{BaseURL: "https://na.hub.molten.bot/v1"},
	}
	var (
		loadIndex int
		stopCalls int
	)

	r := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			cfg := loads[loadIndex]
			if loadIndex < len(loads)-1 {
				loadIndex++
			}
			return cfg, nil
		},
		apply: func(context.Context, hub.InitConfig) error {
			t.Fatal("apply should not be called")
			return nil
		},
		stop: func(context.Context) error {
			stopCalls++
			return nil
		},
	}

	r.Poll(context.Background())
	r.Poll(context.Background())

	if got, want := stopCalls, 1; got != want {
		t.Fatalf("stopCalls = %d, want %d", got, want)
	}
}

func TestHubRuntimeConfigReloaderDoesNotReconnectWithoutConfigChange(t *testing.T) {
	t.Parallel()

	cfg := hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "agent_saved"}
	applyCalls := 0

	r := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			return cfg, nil
		},
		apply: func(context.Context, hub.InitConfig) error {
			applyCalls++
			return nil
		},
		stop: func(context.Context) error {
			t.Fatal("stop should not be called")
			return nil
		},
	}

	r.Poll(context.Background())
	r.Poll(context.Background())
	r.Poll(context.Background())

	if applyCalls != 0 {
		t.Fatalf("applyCalls = %d, want 0", applyCalls)
	}
}

func TestHubRuntimeConfigReloaderLogsReloadErrorsOncePerObservedConfig(t *testing.T) {
	t.Parallel()

	loads := []hub.InitConfig{
		{BaseURL: "https://na.hub.molten.bot/v1"},
		{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "agent_saved"},
		{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "agent_saved"},
	}
	var (
		loadIndex int
		logs      []string
	)

	r := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			cfg := loads[loadIndex]
			if loadIndex < len(loads)-1 {
				loadIndex++
			}
			return cfg, nil
		},
		apply: func(context.Context, hub.InitConfig) error {
			return errors.New("hub daemon start failed")
		},
		logf: func(format string, args ...any) {
			logs = append(logs, format)
		},
	}

	r.Poll(context.Background())
	r.Poll(context.Background())
	r.Poll(context.Background())

	if got, want := len(logs), 1; got != want {
		t.Fatalf("len(logs) = %d, want %d (%v)", got, want, logs)
	}
	if !strings.Contains(logs[0], "hub.runtime_config status=warn action=reload") {
		t.Fatalf("log = %q, want reload warning", logs[0])
	}
}

func TestHubRuntimeConfigReloaderConstructorsAndRunLoop(t *testing.T) {
	t.Parallel()

	var nilReloader *hubRuntimeConfigReloader
	nilReloader.Run(context.Background(), 1*time.Millisecond)
	nilReloader.Poll(context.Background())

	applyCalls := 0
	reloader := newHubRuntimeConfigReloader(
		hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "token"},
		func(context.Context, hub.InitConfig) error {
			applyCalls++
			return nil
		},
		func(context.Context) error {
			return nil
		},
		nil,
	)
	reloader.loadEffective = func(cfg hub.InitConfig) (hub.InitConfig, error) {
		return cfg, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reloader.Run(ctx, 1*time.Millisecond)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	<-done

	if applyCalls != 0 {
		t.Fatalf("Run() should not apply unchanged configuration, got %d apply calls", applyCalls)
	}
}

func TestHubRuntimeConfigReloaderPollWarnBranches(t *testing.T) {
	t.Parallel()

	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	loadErrReloader := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			return hub.InitConfig{}, errors.New("runtime config unavailable")
		},
		logf: logf,
	}
	loadErrReloader.Poll(context.Background())

	applyNilReloader := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			return hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1"}, nil
		},
		logf: logf,
	}
	applyNilReloader.Poll(context.Background()) // seed
	applyNilReloader.loadEffective = func(hub.InitConfig) (hub.InitConfig, error) {
		return hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "saved"}, nil
	}
	applyNilReloader.Poll(context.Background())

	stopNilReloader := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			return hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "saved"}, nil
		},
		logf: logf,
	}
	stopNilReloader.Poll(context.Background()) // seed
	stopNilReloader.loadEffective = func(hub.InitConfig) (hub.InitConfig, error) {
		return hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1"}, nil
	}
	stopNilReloader.Poll(context.Background())

	stopErrReloader := &hubRuntimeConfigReloader{
		loadEffective: func(hub.InitConfig) (hub.InitConfig, error) {
			return hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1", AgentToken: "saved"}, nil
		},
		stop: func(context.Context) error {
			return errors.New("disconnect failed")
		},
		logf: logf,
	}
	stopErrReloader.Poll(context.Background()) // seed
	stopErrReloader.loadEffective = func(hub.InitConfig) (hub.InitConfig, error) {
		return hub.InitConfig{BaseURL: "https://na.hub.molten.bot/v1"}, nil
	}
	stopErrReloader.Poll(context.Background())

	if len(logs) < 4 {
		t.Fatalf("expected warning logs for poll error branches, got %d (%v)", len(logs), logs)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		`runtime config unavailable`,
		`live hub apply is unavailable`,
		`live hub disconnect is unavailable`,
		`disconnect failed`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning logs missing %q: %s", want, joined)
		}
	}
}
