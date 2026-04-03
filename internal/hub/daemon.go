package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jef/how-i-want-to-code/internal/config"
	"github.com/jef/how-i-want-to-code/internal/execx"
	"github.com/jef/how-i-want-to-code/internal/harness"
)

// Daemon listens for hub skill dispatches and runs harness jobs.
type Daemon struct {
	Runner             execx.Runner
	Logf               func(string, ...any)
	OnDispatchQueued   func(requestID string, runCfg config.Config)
	DispatchController *AdaptiveDispatchController
	ReconnectDelay     time.Duration
}

const wsFallbackWindow = 30 * time.Second
const dispatchDedupTTL = 2 * time.Hour
const agentStatusUpdateTimeout = 5 * time.Second

// NewDaemon returns a hub daemon with defaults.
func NewDaemon(runner execx.Runner) Daemon {
	return Daemon{
		Runner:         runner,
		Logf:           func(string, ...any) {},
		ReconnectDelay: 3 * time.Second,
	}
}

// Run binds/auths, syncs profile, then consumes websocket transport (with pull fallback) for skill runs.
func (d Daemon) Run(ctx context.Context, cfg InitConfig) error {
	if d.Runner == nil {
		d.Runner = execx.OSRunner{}
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.ReconnectDelay <= 0 {
		d.ReconnectDelay = 3 * time.Second
	}

	cfg.ApplyDefaults()
	if stored, err := LoadRuntimeConfig(defaultRuntimeConfigPath); err == nil {
		if applied := applyStoredRuntimeConfig(&cfg, stored); applied {
			d.logf("hub.runtime_config status=loaded path=%s", defaultRuntimeConfigPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		d.logf("hub.runtime_config status=warn err=%q", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("init config: %w", err)
	}

	api := NewAPIClient(cfg.BaseURL)
	api.Logf = d.logf

	token, err := api.ResolveAgentToken(ctx, cfg)
	if err != nil {
		return fmt.Errorf("hub auth: %w", err)
	}
	if strings.TrimSpace(api.BaseURL) != "" {
		cfg.BaseURL = strings.TrimRight(strings.TrimSpace(api.BaseURL), "/")
	}
	d.logf("hub.connection status=configured base_url=%s", cfg.BaseURL)
	d.logf("hub.auth status=ok")
	if err := SaveRuntimeConfig(defaultRuntimeConfigPath, cfg.BaseURL, token, cfg.SessionKey); err != nil {
		return fmt.Errorf("hub runtime config: %w", err)
	}
	d.logf("hub.runtime_config status=saved path=%s", defaultRuntimeConfigPath)

	if err := api.SyncProfile(ctx, token, cfg); err != nil {
		d.logf("hub.profile status=warn err=%q", err)
	} else {
		d.logf("hub.profile status=ok")
	}

	if err := api.UpdateAgentStatus(ctx, token, "online"); err != nil {
		d.logf("hub.agent status=warn state=online err=%q", err)
	} else {
		d.logf("hub.agent status=online")
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), agentStatusUpdateTimeout)
		defer cancel()
		if err := api.UpdateAgentStatus(shutdownCtx, token, "offline"); err != nil {
			d.logf("hub.agent status=warn state=offline err=%q", err)
			return
		}
		d.logf("hub.agent status=offline")
	}()

	d.logf("hub.transport primary=openclaw_ws fallback=openclaw_pull")

	dispatchController := d.DispatchController
	if dispatchController == nil {
		dispatchController = NewAdaptiveDispatchController(cfg.Dispatcher, d.logf)
	}
	dispatchController.Start(ctx)

	var workers sync.WaitGroup
	defer workers.Wait()
	deduper := newDispatchDeduper(dispatchDedupTTL)

	wsURL, wsURLErr := WebsocketURL(cfg.BaseURL, cfg.SessionKey)
	if wsURLErr != nil {
		d.logf("hub.ws status=disabled err=%q", wsURLErr)
		d.logf("hub.transport mode=openclaw_pull")
		return d.runPullLoop(ctx, api, token, cfg, dispatchController, &workers, deduper)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		if err := d.runWebsocketLoop(ctx, wsURL, api, token, cfg, dispatchController, &workers, deduper); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return nil
		} else if !shouldFallbackToPull(err) {
			d.logf("hub.ws status=disconnected err=%q", err)
			if !sleepWithContext(ctx, d.ReconnectDelay) {
				return nil
			}
			continue
		} else {
			d.logf("hub.ws status=error err=%q", err)
		}

		d.logf("hub.transport mode=openclaw_pull")
		fallbackUntil := time.Now().Add(wsFallbackWindow)
		for time.Now().Before(fallbackUntil) {
			if ctx.Err() != nil {
				return nil
			}
			if err := d.pullOnce(ctx, api, token, cfg, dispatchController, &workers, deduper); err != nil {
				d.logf("hub.pull status=error err=%q", err)
				if !sleepWithContext(ctx, d.ReconnectDelay) {
					return nil
				}
			}
		}
	}
}

func (d Daemon) runPullLoop(
	ctx context.Context,
	api APIClient,
	token string,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := d.pullOnce(ctx, api, token, cfg, dispatchController, workers, deduper); err != nil {
			d.logf("hub.pull status=error err=%q", err)
			if !sleepWithContext(ctx, d.ReconnectDelay) {
				return nil
			}
		}
	}
}

func (d Daemon) pullOnce(
	ctx context.Context,
	api APIClient,
	token string,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) error {
	pulled, found, err := api.PullOpenClawMessage(ctx, token, runtimeTimeoutMs)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	d.processInboundMessage(ctx, api, token, cfg, pulled.Message, pulled.DeliveryID, pulled.MessageID, dispatchController, workers, deduper)
	return nil
}

func (d Daemon) runWebsocketLoop(
	ctx context.Context,
	wsURL string,
	api APIClient,
	token string,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) error {
	ws, err := DialWebsocket(ctx, wsURL, token)
	if err != nil {
		return err
	}
	defer ws.Close()

	d.logf("hub.ws status=connected")

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-done:
		}
	}()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if err := ws.WritePing([]byte("hb")); err != nil {
					_ = ws.Close()
					return
				}
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}

		var raw map[string]any
		if err := ws.ReadJSON(&raw); err != nil {
			if errors.Is(err, io.EOF) && ctx.Err() != nil {
				return nil
			}
			return err
		}

		inbound := extractInboundOpenClawMessage(raw)
		if len(inbound.Message) == 0 {
			continue
		}
		d.processInboundMessage(ctx, api, token, cfg, inbound.Message, inbound.DeliveryID, inbound.MessageID, dispatchController, workers, deduper)
	}
}

func (d Daemon) processInboundMessage(
	ctx context.Context,
	api APIClient,
	token string,
	cfg InitConfig,
	msg map[string]any,
	deliveryID string,
	messageID string,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) {
	dispatch, matched, parseErr := ParseSkillDispatch(msg, cfg.Skill.DispatchType, cfg.Skill.Name)
	if !matched {
		if skill := incomingSkillName(msg); skill != "unknown" {
			d.logf("dispatch status=ignored skill=%s", skill)
		}
		if strings.TrimSpace(deliveryID) != "" {
			if err := api.AckOpenClawDelivery(ctx, token, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		return
	}
	if parseErr != nil {
		d.logf("dispatch status=invalid request_id=%s err=%q", dispatch.RequestID, parseErr)
		payload := dispatchParseErrorPayload(cfg, dispatch, parseErr)
		if err := api.PublishResult(ctx, token, payload); err != nil {
			d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
			if strings.TrimSpace(deliveryID) != "" {
				if nackErr := api.NackOpenClawDelivery(ctx, token, deliveryID); nackErr != nil {
					d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
				}
			}
			return
		}
		if strings.TrimSpace(deliveryID) != "" {
			if err := api.AckOpenClawDelivery(ctx, token, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		return
	}

	dupKey := dedupeKeyForDispatch(dispatch, messageID, deliveryID)
	if deduper != nil && dupKey != "" {
		if accepted, state := deduper.Begin(dupKey); !accepted {
			d.logf("dispatch status=duplicate request_id=%s state=%s", firstNonEmpty(dispatch.RequestID, dupKey), state)
			if strings.TrimSpace(deliveryID) != "" {
				if err := api.AckOpenClawDelivery(ctx, token, deliveryID); err != nil {
					d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
				}
			}
			return
		}
	}
	if d.OnDispatchQueued != nil {
		d.OnDispatchQueued(dispatch.RequestID, dispatch.Config)
	}
	if dispatchController == nil {
		dispatchController = NewAdaptiveDispatchController(cfg.Dispatcher, d.logf)
		dispatchController.Start(ctx)
	}

	ackedEarly := false
	if strings.TrimSpace(deliveryID) != "" {
		if err := api.AckOpenClawDelivery(ctx, token, deliveryID); err != nil {
			d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
		} else {
			ackedEarly = true
			d.logf("dispatch status=ack request_id=%s delivery_id=%s", firstNonEmpty(dispatch.RequestID, dupKey), deliveryID)
		}
	}

	workers.Add(1)
	go func(dispatch SkillDispatch, deliveryID, dedupeKey string, ackedEarly bool) {
		defer workers.Done()

		release, acquireErr := dispatchController.Acquire(ctx, dispatch.RequestID)
		if acquireErr != nil {
			if !errors.Is(acquireErr, context.Canceled) && !errors.Is(acquireErr, errDispatchControllerClosed) {
				d.logf(
					"dispatch status=error request_id=%s err=%q",
					firstNonEmpty(dispatch.RequestID, dedupeKey),
					acquireErr,
				)
			}
			if deduper != nil {
				deduper.Done(dedupeKey)
			}
			return
		}
		defer release()
		defer func() {
			if deduper != nil {
				deduper.Done(dedupeKey)
			}
		}()
		d.handleDispatch(ctx, api, token, cfg, dispatch, deliveryID, ackedEarly)
	}(dispatch, deliveryID, dupKey, ackedEarly)
}

func shouldFallbackToPull(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"use of closed network connection",
		"connection reset by peer",
		"broken pipe",
	} {
		if strings.Contains(text, marker) {
			return false
		}
	}
	return true
}

func dedupeKeyForDispatch(dispatch SkillDispatch, messageID, deliveryID string) string {
	return firstNonEmpty(
		dispatch.RequestID,
		strings.TrimSpace(messageID),
		strings.TrimSpace(deliveryID),
	)
}

type dispatchDeduper struct {
	mu        sync.Mutex
	inFlight  map[string]struct{}
	completed map[string]time.Time
	ttl       time.Duration
}

func newDispatchDeduper(ttl time.Duration) *dispatchDeduper {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &dispatchDeduper{
		inFlight:  map[string]struct{}{},
		completed: map[string]time.Time{},
		ttl:       ttl,
	}
}

func (d *dispatchDeduper) Begin(key string) (bool, string) {
	if d == nil {
		return true, ""
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true, ""
	}

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gcLocked(now)

	if _, exists := d.inFlight[key]; exists {
		return false, "in_flight"
	}
	if _, exists := d.completed[key]; exists {
		return false, "completed"
	}
	d.inFlight[key] = struct{}{}
	return true, "accepted"
}

func (d *dispatchDeduper) Done(key string) {
	if d == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.inFlight, key)
	d.completed[key] = now
	d.gcLocked(now)
}

func (d *dispatchDeduper) gcLocked(now time.Time) {
	if d == nil || d.ttl <= 0 {
		return
	}
	for key, ts := range d.completed {
		if now.Sub(ts) > d.ttl {
			delete(d.completed, key)
		}
	}
}

func (d Daemon) handleDispatch(
	ctx context.Context,
	api APIClient,
	token string,
	cfg InitConfig,
	dispatch SkillDispatch,
	deliveryID string,
	ackedEarly bool,
) {
	d.logf(
		"dispatch status=start request_id=%s skill=%s repo=%s repos=%s",
		dispatch.RequestID,
		dispatch.Skill,
		dispatch.Config.RepoURL,
		strings.Join(dispatch.Config.RepoList(), ","),
	)

	h := harness.New(d.Runner)
	h.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if dispatch.RequestID != "" {
			d.logf("dispatch request_id=%s %s", dispatch.RequestID, line)
			return
		}
		d.logf("dispatch %s", line)
	}

	res := h.Run(ctx, dispatch.Config)
	payload := dispatchResultPayload(cfg, dispatch, res)
	if err := api.PublishResult(ctx, token, payload); err != nil {
		d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
		if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
			if nackErr := api.NackOpenClawDelivery(ctx, token, deliveryID); nackErr != nil {
				d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
			}
		}
		return
	}
	if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
		if err := api.AckOpenClawDelivery(ctx, token, deliveryID); err != nil {
			d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
		}
	}

	if res.Err != nil {
		d.logf(
			"dispatch status=error request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
			dispatch.RequestID,
			res.ExitCode,
			res.WorkspaceDir,
			res.Branch,
			res.PRURL,
			res.Err,
		)
		return
	}
	if res.NoChanges {
		d.logf("dispatch status=no_changes request_id=%s workspace=%s branch=%s", dispatch.RequestID, res.WorkspaceDir, res.Branch)
		return
	}
	d.logf(
		"dispatch status=ok request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s changed_repos=%d",
		dispatch.RequestID,
		res.WorkspaceDir,
		res.Branch,
		res.PRURL,
		joinRepoPRURLs(res.RepoResults),
		countChangedRepoResults(res.RepoResults),
	)
}

func dispatchResultPayload(cfg InitConfig, dispatch SkillDispatch, res harness.Result) map[string]any {
	status := "ok"
	if res.Err != nil {
		status = "error"
	} else if res.NoChanges {
		status = "no_changes"
	}

	result := map[string]any{
		"exit_code":     res.ExitCode,
		"workspace_dir": res.WorkspaceDir,
		"branch":        res.Branch,
		"pr_url":        res.PRURL,
		"pr_urls":       splitNonEmptyCSV(joinRepoPRURLs(res.RepoResults)),
		"changed_repos": countChangedRepoResults(res.RepoResults),
		"repo_results":  repoResultPayloads(res.RepoResults),
		"no_changes":    res.NoChanges,
	}
	if res.Err != nil {
		result["error"] = res.Err.Error()
	}

	payload := map[string]any{
		"type":       cfg.Skill.ResultType,
		"skill":      firstNonEmpty(dispatch.Skill, cfg.Skill.Name),
		"request_id": dispatch.RequestID,
		"status":     status,
		"ok":         res.Err == nil,
		"result":     result,
	}
	if dispatch.ReplyTo != "" {
		payload["reply_to"] = dispatch.ReplyTo
		payload["to"] = dispatch.ReplyTo
	}
	return payload
}

func joinRepoPRURLs(results []harness.RepoResult) string {
	if len(results) == 0 {
		return ""
	}
	urls := make([]string, 0, len(results))
	for _, result := range results {
		if !result.Changed {
			continue
		}
		url := strings.TrimSpace(result.PRURL)
		if url == "" {
			continue
		}
		urls = append(urls, url)
	}
	return strings.Join(urls, ",")
}

func countChangedRepoResults(results []harness.RepoResult) int {
	count := 0
	for _, result := range results {
		if result.Changed {
			count++
		}
	}
	return count
}

func repoResultPayloads(results []harness.RepoResult) []map[string]any {
	if len(results) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		out = append(out, map[string]any{
			"repo_url": result.RepoURL,
			"repo_dir": result.RepoDir,
			"branch":   result.Branch,
			"pr_url":   result.PRURL,
			"changed":  result.Changed,
		})
	}
	return out
}

func splitNonEmptyCSV(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (d Daemon) logf(format string, args ...any) {
	if d.Logf == nil {
		return
	}
	d.Logf("%s", redactSensitiveLogText(fmt.Sprintf(format, args...)))
}

func applyStoredRuntimeConfig(cfg *InitConfig, stored RuntimeConfig) bool {
	if cfg == nil {
		return false
	}

	token := strings.TrimSpace(stored.Token)
	if token == "" {
		return false
	}

	cfg.AgentToken = token
	cfg.BindToken = ""

	baseURL := strings.TrimSpace(stored.BaseURL)
	if baseURL != "" {
		cfg.BaseURL = strings.TrimRight(baseURL, "/")
	}

	sessionKey := strings.TrimSpace(stored.SessionKey)
	if sessionKey != "" {
		cfg.SessionKey = sessionKey
	}

	return true
}

func dispatchParseErrorPayload(cfg InitConfig, dispatch SkillDispatch, parseErr error) map[string]any {
	payload := dispatchResultPayload(cfg, dispatch, harness.Result{
		ExitCode: harness.ExitConfig,
		Err:      fmt.Errorf("dispatch parse: %w", parseErr),
	})
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	result["required_schema"] = requiredSkillPayloadSchema(cfg.Skill.DispatchType, cfg.Skill.Name)
	payload["result"] = result
	return payload
}

func incomingSkillName(msg map[string]any) string {
	skill := firstNonEmpty(
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
	if skill == "" {
		return "unknown"
	}
	return skill
}
