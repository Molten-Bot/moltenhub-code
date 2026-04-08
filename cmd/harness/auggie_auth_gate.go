package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

const (
	auggieSessionAuthEnv            = "AUGMENT_SESSION_AUTH"
	auggieConfigureCommand          = "auggie token print"
	auggieConfigurePlaceholderValue = `{"accessToken":"XXX","tenantURL":"https://YYY/","scopes":["email"]}`
)

type auggieAuthGate struct {
	mu sync.Mutex

	runtimeConfigPath string
	initCfg           hub.InitConfig

	required bool
	ready    bool
	state    string
	message  string

	configureCommand     string
	configurePlaceholder string
	updatedAt            time.Time
}

type auggieSessionAuth struct {
	AccessToken string   `json:"accessToken"`
	TenantURL   string   `json:"tenantURL"`
	Scopes      []string `json:"scopes"`
}

func newAuggieAuthGate(runtimeConfigPath string, initCfg hub.InitConfig) *auggieAuthGate {
	g := &auggieAuthGate{
		runtimeConfigPath:    strings.TrimSpace(runtimeConfigPath),
		initCfg:              initCfg,
		required:             true,
		ready:                false,
		state:                "needs_configure",
		configureCommand:     auggieConfigureCommand,
		configurePlaceholder: auggieConfigurePlaceholderValue,
		updatedAt:            time.Now().UTC(),
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if configured, source := firstConfiguredAuggieSessionAuth(g.runtimeConfigPath, initCfg); configured != "" {
		if canonical, err := normalizeAuggieSessionAuth(configured); err == nil {
			if source != "environment" {
				_ = os.Setenv(auggieSessionAuthEnv, canonical)
			}
			g.initCfg.AugmentSessionAuth = canonical
			g.ready = true
			g.state = "ready"
			g.message = "Auggie session auth is ready."
			g.updatedAt = time.Now().UTC()
			return g
		} else {
			g.message = fmt.Sprintf(
				"Auggie session auth from %s is invalid: %v. Run `%s` in your terminal locally, paste the JSON, then click Done.",
				source,
				err,
				auggieConfigureCommand,
			)
			g.updatedAt = time.Now().UTC()
			return g
		}
	}

	g.message = fmt.Sprintf(
		"Auggie session auth is required. Run `%s` in your terminal locally, paste the JSON output below, then click Done.",
		auggieConfigureCommand,
	)
	g.updatedAt = time.Now().UTC()
	return g
}

func (g *auggieAuthGate) Status(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked(), nil
}

func (g *auggieAuthGate) StartDeviceAuth(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked(), nil
}

func (g *auggieAuthGate) Verify(_ context.Context) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked(), nil
}

func (g *auggieAuthGate) Configure(_ context.Context, rawInput string) (hubui.AgentAuthState, error) {
	if g == nil {
		return hubui.AgentAuthState{
			Required: false,
			Ready:    true,
			State:    "ready",
			Message:  "Agent auth is ready.",
		}, nil
	}

	canonical, err := normalizeAuggieSessionAuth(rawInput)
	if err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf(
			"Auggie session auth is invalid: %v. Run `%s` in your terminal locally, paste the JSON, then click Done.",
			err,
			auggieConfigureCommand,
		)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	g.mu.Lock()
	initCfg := g.initCfg
	runtimeConfigPath := g.runtimeConfigPath
	g.mu.Unlock()

	if err := hub.SaveRuntimeConfigAuggieAuth(runtimeConfigPath, initCfg, canonical); err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("save auggie config.json: %v", err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}
	if err := os.Setenv(auggieSessionAuthEnv, canonical); err != nil {
		g.mu.Lock()
		g.ready = false
		g.state = "needs_configure"
		g.message = fmt.Sprintf("set %s: %v", auggieSessionAuthEnv, err)
		g.updatedAt = time.Now().UTC()
		snap := g.snapshotLocked()
		g.mu.Unlock()
		return snap, err
	}

	g.mu.Lock()
	g.initCfg.AugmentSessionAuth = canonical
	g.ready = true
	g.state = "ready"
	g.message = "Auggie session auth is ready."
	g.updatedAt = time.Now().UTC()
	snap := g.snapshotLocked()
	g.mu.Unlock()
	return snap, nil
}

func (g *auggieAuthGate) snapshotLocked() hubui.AgentAuthState {
	updatedAt := g.updatedAt.UTC().Format(time.RFC3339Nano)
	return hubui.AgentAuthState{
		Harness:              agentruntime.HarnessAuggie,
		Required:             g.required,
		Ready:                g.ready,
		State:                strings.TrimSpace(g.state),
		Message:              strings.TrimSpace(g.message),
		ConfigureCommand:     strings.TrimSpace(g.configureCommand),
		ConfigurePlaceholder: strings.TrimSpace(g.configurePlaceholder),
		UpdatedAt:            updatedAt,
	}
}

func firstConfiguredAuggieSessionAuth(runtimeConfigPath string, initCfg hub.InitConfig) (value string, source string) {
	if env := strings.TrimSpace(os.Getenv(auggieSessionAuthEnv)); env != "" {
		return env, "environment"
	}
	if init := strings.TrimSpace(initCfg.AugmentSessionAuth); init != "" {
		return init, "init config"
	}
	if persisted := loadPersistedAuggieSessionAuth(runtimeConfigPath); persisted != "" {
		return persisted, "runtime config"
	}
	return "", ""
}

func loadPersistedAuggieSessionAuth(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	return firstNonEmptyString(
		stringValue(doc["augment_session_auth"]),
		stringValue(doc["augmentSessionAuth"]),
		stringValue(doc["AUGMENT_SESSION_AUTH"]),
	)
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func normalizeAuggieSessionAuth(rawInput string) (string, error) {
	parsed, err := decodeAuggieSessionAuth(rawInput)
	if err != nil {
		return "", err
	}

	parsed.AccessToken = strings.TrimSpace(parsed.AccessToken)
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("accessToken is required")
	}

	parsed.TenantURL = strings.TrimSpace(parsed.TenantURL)
	if parsed.TenantURL == "" {
		return "", fmt.Errorf("tenantURL is required")
	}
	parsedURL, err := url.Parse(parsed.TenantURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", fmt.Errorf("tenantURL must be an absolute URL")
	}
	if !strings.EqualFold(parsedURL.Scheme, "https") {
		return "", fmt.Errorf("tenantURL must use https")
	}

	scopes := make([]string, 0, len(parsed.Scopes))
	hasEmailScope := false
	for _, scope := range parsed.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		scopes = append(scopes, scope)
		if strings.EqualFold(scope, "email") {
			hasEmailScope = true
		}
	}
	if len(scopes) == 0 {
		return "", fmt.Errorf("scopes must include at least one value")
	}
	if !hasEmailScope {
		return "", fmt.Errorf("scopes must include \"email\"")
	}
	parsed.Scopes = scopes

	encoded, err := json.Marshal(parsed)
	if err != nil {
		return "", fmt.Errorf("encode augment session auth: %w", err)
	}
	return string(encoded), nil
}

func decodeAuggieSessionAuth(rawInput string) (auggieSessionAuth, error) {
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		return auggieSessionAuth{}, fmt.Errorf("session auth JSON is required")
	}

	var parsed auggieSessionAuth
	if err := decodeJSONStrict(rawInput, &parsed); err == nil {
		return parsed, nil
	}

	var wrapped string
	if err := decodeJSONStrict(rawInput, &wrapped); err == nil {
		return decodeAuggieSessionAuth(wrapped)
	}

	return auggieSessionAuth{}, fmt.Errorf("expected JSON object with accessToken, tenantURL, and scopes")
}

func decodeJSONStrict(raw string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON data")
		}
		return err
	}
	return nil
}
