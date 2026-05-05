package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

const hubRuntimeConfigReloadInterval = 2 * time.Second

type hubRuntimeConfigReloader struct {
	baseCfg hub.InitConfig

	loadEffective func(hub.InitConfig) (hub.InitConfig, error)
	apply         func(context.Context, hub.InitConfig) error
	stop          func(context.Context) error
	logf          func(string, ...any)

	lastSeenSignature  string
	lastSeenConfigured bool
	seeded             bool
}

func newHubRuntimeConfigReloader(
	baseCfg hub.InitConfig,
	apply func(context.Context, hub.InitConfig) error,
	stop func(context.Context) error,
	logf func(string, ...any),
) *hubRuntimeConfigReloader {
	return &hubRuntimeConfigReloader{
		baseCfg:       baseCfg,
		loadEffective: effectiveHubSetupConfig,
		apply:         apply,
		stop:          stop,
		logf:          logf,
	}
}

func (r *hubRuntimeConfigReloader) Run(ctx context.Context, interval time.Duration) {
	if r == nil {
		return
	}
	if interval <= 0 {
		interval = hubRuntimeConfigReloadInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Poll(ctx)
		}
	}
}

func (r *hubRuntimeConfigReloader) Poll(ctx context.Context) {
	if r == nil {
		return
	}
	if r.loadEffective == nil {
		r.loadEffective = effectiveHubSetupConfig
	}
	if r.logf == nil {
		r.logf = func(string, ...any) {}
	}

	activeCfg, err := r.loadEffective(r.baseCfg)
	if err != nil {
		r.logf("hub.runtime_config status=warn action=reload err=%q", err)
		return
	}

	signature := hubRuntimeReloadSignature(activeCfg)
	configured := hubCredentialsConfigured(activeCfg, nil)
	if !r.seeded {
		r.lastSeenSignature = signature
		r.lastSeenConfigured = configured
		r.seeded = true
		return
	}
	if signature == r.lastSeenSignature && configured == r.lastSeenConfigured {
		return
	}

	r.lastSeenSignature = signature
	r.lastSeenConfigured = configured

	if configured {
		if r.apply == nil {
			r.logf("hub.runtime_config status=warn action=reload err=%q", "live hub apply is unavailable")
			return
		}
		if err := r.apply(ctx, activeCfg); err != nil {
			r.logf("hub.runtime_config status=warn action=reload err=%q", err)
			return
		}
		r.logf("hub.runtime_config status=reloaded action=connect")
		return
	}

	if r.stop == nil {
		r.logf("hub.runtime_config status=warn action=reload err=%q", "live hub disconnect is unavailable")
		return
	}
	if err := r.stop(ctx); err != nil {
		r.logf("hub.runtime_config status=warn action=reload err=%q", err)
		return
	}
	r.logf("hub.runtime_config status=reloaded action=disconnect")
}

func hubRuntimeReloadSignature(cfg hub.InitConfig) string {
	cfg.ApplyDefaults()
	doc := struct {
		BaseURL      string            `json:"base_url"`
		BindToken    string            `json:"bind_token"`
		AgentToken   string            `json:"agent_token"`
		SessionKey   string            `json:"session_key"`
		Handle       string            `json:"handle"`
		AgentHarness string            `json:"agent_harness"`
		AgentCommand string            `json:"agent_command"`
		Profile      hub.ProfileConfig `json:"profile"`
	}{
		BaseURL:      strings.TrimSpace(cfg.BaseURL),
		BindToken:    strings.TrimSpace(cfg.BindToken),
		AgentToken:   strings.TrimSpace(cfg.AgentToken),
		SessionKey:   strings.TrimSpace(cfg.SessionKey),
		Handle:       strings.TrimSpace(cfg.Handle),
		AgentHarness: strings.TrimSpace(cfg.AgentHarness),
		AgentCommand: strings.TrimSpace(cfg.AgentCommand),
		Profile:      cfg.Profile,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	return string(data)
}
