package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/failurefollowup"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/library"
)

// Daemon listens for hub skill dispatches and runs harness jobs.
type Daemon struct {
	Runner             execx.Runner
	Logf               func(string, ...any)
	OnDispatchQueued   func(requestID string, runCfg config.Config)
	OnDispatchFailed   func(requestID string, runCfg config.Config, result harness.Result)
	DispatchController *AdaptiveDispatchController
	ReconnectDelay     time.Duration
	TaskLogRoot        string
}

const wsFallbackWindow = 30 * time.Second
const dispatchDedupTTL = 2 * time.Hour
const agentStatusUpdateTimeout = 5 * time.Second
const failureFollowUpRequestIDSuffix = "-failure-review"
const failureRerunRequestIDSuffix = "-rerun"
const failureFollowUpPromptBase = failurefollowup.RequiredPrompt
const failureFollowUpNoPathGuidance = "No workspace or log path was captured before the failure. Investigate the task history and runtime error details first."
const failureFollowUpBaseBranch = "main"
const failureFollowUpTargetSubdir = "."
const transportOfflineReasonExecutionFailure = "task_execution_failure"

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

	runtimeCfgPath := strings.TrimSpace(cfg.RuntimeConfigPath)
	if runtimeCfgPath == "" {
		runtimeCfgPath = defaultRuntimeConfigPath()
	}
	pullTimeoutMs := runtimeTimeoutMs
	if stored, loadedPath, err := loadStoredRuntimeConfig(runtimeCfgPath); err == nil {
		runtimeCfgPath = loadedPath
		if stored.TimeoutMs > 0 {
			pullTimeoutMs = stored.TimeoutMs
		}
		if applied := applyStoredRuntimeConfig(&cfg, stored); applied {
			d.logf("hub.runtime_config status=loaded path=%s", loadedPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		d.logf("hub.runtime_config status=warn err=%q", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("init config: %w", err)
	}

	transport := NewAPIClient(cfg.BaseURL)
	transport.Logf = d.logf
	api := NewAsyncAPIClientFrom(transport, cfg.AgentToken)

	token, err := api.ResolveAgentToken(ctx, cfg)
	if err != nil {
		return fmt.Errorf("hub auth: %w", err)
	}
	if strings.TrimSpace(api.BaseURL()) != "" {
		cfg.BaseURL = strings.TrimRight(strings.TrimSpace(api.BaseURL()), "/")
	}
	cfg.AgentToken = strings.TrimSpace(token)
	if remoteProfile, profileErr := transport.AgentProfile(ctx, token); profileErr != nil {
		d.logf("hub.profile status=warn action=load err=%q", profileErr)
	} else {
		merged := mergeAgentProfiles(remoteProfile, AgentProfile{
			Handle:  cfg.Handle,
			Profile: cfg.Profile,
		})
		cfg.Handle = strings.TrimSpace(merged.Handle)
		cfg.Profile = merged.Profile
		cfg.ApplyDefaults()
	}
	d.logf("hub.connection status=configured base_url=%s", cfg.BaseURL)
	d.logf("hub.auth status=ok")
	if err := SaveRuntimeConfigHubSettings(runtimeCfgPath, cfg, token); err != nil {
		return fmt.Errorf("hub runtime config: %w", err)
	}
	d.logf("hub.runtime_config status=saved path=%s", runtimeCfgPath)

	libraryCatalog, libraryErr := library.LoadCatalog(library.DefaultDir)
	orderedLibrarySummaries := []library.TaskSummary{}
	if libraryErr != nil {
		d.logf("hub.library status=warn err=%q", libraryErr)
	} else {
		orderedLibrarySummaries = library.OrderSummariesByUsage(
			libraryCatalog.Summaries(),
			ReadRuntimeConfigLibraryTaskUsage(runtimeCfgPath),
		)
		d.logf("hub.library status=loaded tasks=%d", len(libraryCatalog.Tasks))
	}
	if err := api.SyncProfile(ctx, cfg); err != nil {
		d.logf("hub.profile status=warn err=%q", err)
	} else {
		d.logf("hub.profile status=ok")
	}

	if err := api.RegisterRuntime(ctx, cfg, orderedLibrarySummaries); err != nil {
		d.logf("hub.runtime status=warn action=register err=%q", err)
	} else {
		d.logf("hub.runtime status=registered skills=%d library_tasks=%d", len(supportedProfileSkills()), len(libraryCatalog.Tasks))
	}

	if err := api.UpdateAgentStatus(ctx, "online"); err != nil {
		d.logf("hub.agent status=warn state=online err=%q", err)
	} else {
		d.logf("hub.agent status=online")
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), agentStatusUpdateTimeout)
		defer cancel()
		if err := api.MarkOpenClawOffline(shutdownCtx, cfg.SessionKey, transportOfflineReasonAgent); err != nil {
			d.logf("hub.transport status=warn mode=openclaw_ws err=%q", err)
		} else {
			d.logf("hub.transport status=offline mode=openclaw_ws")
		}
		if err := api.UpdateAgentStatus(shutdownCtx, "offline"); err != nil {
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
		return d.runPullLoop(ctx, api, cfg, dispatchController, &workers, deduper, pullTimeoutMs)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		if err := d.runWebsocketLoop(ctx, wsURL, api, cfg, dispatchController, &workers, deduper); err == nil {
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
			if err := d.pullOnce(ctx, api, cfg, dispatchController, &workers, deduper, pullTimeoutMs); err != nil {
				if isUnauthorizedHubError(err) {
					return fmt.Errorf("hub auth: %w", err)
				}
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
	api MoltenHubAPI,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
	pullTimeoutMs int,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := d.pullOnce(ctx, api, cfg, dispatchController, workers, deduper, pullTimeoutMs); err != nil {
			if isUnauthorizedHubError(err) {
				return fmt.Errorf("hub auth: %w", err)
			}
			d.logf("hub.pull status=error err=%q", err)
			if !sleepWithContext(ctx, d.ReconnectDelay) {
				return nil
			}
		}
	}
}

func (d Daemon) pullOnce(
	ctx context.Context,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
	pullTimeoutMs int,
) error {
	if pullTimeoutMs <= 0 {
		pullTimeoutMs = runtimeTimeoutMs
	}
	pulled, found, err := api.PullOpenClawMessage(ctx, pullTimeoutMs)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	d.processInboundMessage(ctx, api, cfg, pulled.Message, pulled.DeliveryID, pulled.MessageID, dispatchController, workers, deduper)
	return nil
}

func (d Daemon) runWebsocketLoop(
	ctx context.Context,
	wsURL string,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) error {
	ws, err := DialWebsocket(ctx, wsURL, api.Token())
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
		d.processInboundMessage(ctx, api, cfg, inbound.Message, inbound.DeliveryID, inbound.MessageID, dispatchController, workers, deduper)
	}
}

func (d Daemon) processInboundMessage(
	ctx context.Context,
	api MoltenHubAPI,
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
			if err := api.AckOpenClawDelivery(ctx, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		return
	}
	if parseErr != nil {
		d.logf("dispatch status=invalid request_id=%s err=%q", dispatch.RequestID, parseErr)
		payload := dispatchParseErrorPayload(cfg, dispatch, parseErr)
		if err := api.PublishResult(ctx, payload); err != nil {
			d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
			if strings.TrimSpace(deliveryID) != "" {
				if nackErr := api.NackOpenClawDelivery(ctx, deliveryID); nackErr != nil {
					d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
				}
			}
			return
		}
		if strings.TrimSpace(deliveryID) != "" {
			if err := api.AckOpenClawDelivery(ctx, deliveryID); err != nil {
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
				if err := api.AckOpenClawDelivery(ctx, deliveryID); err != nil {
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
		if err := api.AckOpenClawDelivery(ctx, deliveryID); err != nil {
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
			if !errors.Is(acquireErr, context.Canceled) {
				failErr := fmt.Errorf("dispatch acquire: %w", acquireErr)
				failRes := harness.Result{
					ExitCode: harness.ExitPreflight,
					Err:      failErr,
				}
				if d.OnDispatchFailed != nil {
					d.OnDispatchFailed(dispatch.RequestID, dispatch.Config, failRes)
				}
				payload := dispatchResultPayload(cfg, dispatch, failRes)
				if err := api.PublishResult(ctx, payload); err != nil {
					d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
					if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
						if nackErr := api.NackOpenClawDelivery(ctx, deliveryID); nackErr != nil {
							d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
						}
					}
				} else {
					d.handleFailedDispatchAfterPublish(ctx, api, cfg, dispatch, failRes)
					if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
						if err := api.AckOpenClawDelivery(ctx, deliveryID); err != nil {
							d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
						}
					}
				}
				d.logf(
					"dispatch status=error request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
					firstNonEmpty(dispatch.RequestID, dedupeKey),
					failRes.ExitCode,
					failRes.WorkspaceDir,
					failRes.Branch,
					failRes.PRURL,
					failRes.Err,
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
		d.handleDispatch(ctx, api, cfg, dispatch, deliveryID, ackedEarly)
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

func isUnauthorizedHubError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "status=401") ||
		strings.Contains(text, "status 401") ||
		strings.Contains(text, "unauthorized")
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
	api MoltenHubAPI,
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
	if taskName := strings.TrimSpace(dispatch.Config.LibraryTaskName); taskName != "" {
		if err := IncrementRuntimeConfigLibraryTaskUsage(cfg.RuntimeConfigPath, cfg, taskName); err != nil {
			d.logf("library.usage status=warn task=%s request_id=%s err=%q", taskName, dispatch.RequestID, err)
		}
	}

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
	if res.Err != nil && d.OnDispatchFailed != nil {
		d.OnDispatchFailed(dispatch.RequestID, dispatch.Config, res)
	}
	payload := dispatchResultPayload(cfg, dispatch, res)
	if err := api.PublishResult(ctx, payload); err != nil {
		d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
		if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
			if nackErr := api.NackOpenClawDelivery(ctx, deliveryID); nackErr != nil {
				d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
			}
		}
		return
	}
	if res.Err != nil {
		d.handleFailedDispatchAfterPublish(ctx, api, cfg, dispatch, res)
	}
	if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
		if err := api.AckOpenClawDelivery(ctx, deliveryID); err != nil {
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
	d.recordGitHubTaskCompleteActivity(ctx, api, dispatch.RequestID)
	if res.NoChanges && !resultHasPR(res) {
		d.logf(
			"dispatch status=no_changes request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s",
			dispatch.RequestID,
			res.WorkspaceDir,
			res.Branch,
			res.PRURL,
			joinAllRepoPRURLs(res.RepoResults),
		)
		return
	}
	d.logf(
		"dispatch status=completed request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s changed_repos=%d",
		dispatch.RequestID,
		res.WorkspaceDir,
		res.Branch,
		res.PRURL,
		completedPRURLs(res),
		countChangedRepoResults(res.RepoResults),
	)
}

func (d Daemon) recordGitHubTaskCompleteActivity(ctx context.Context, api MoltenHubAPI, requestID string) {
	if api == nil {
		return
	}
	if err := api.RecordGitHubTaskCompleteActivity(ctx); err != nil {
		d.logf("dispatch status=warn action=record_github_task_complete request_id=%s err=%q", requestID, err)
	}
}

func dispatchResultPayload(cfg InitConfig, dispatch SkillDispatch, res harness.Result) map[string]any {
	status := "completed"
	if res.Err != nil {
		status = "error"
	} else if res.NoChanges && !resultHasPR(res) {
		status = "no_changes"
	}
	detailStatus := dispatchResultDetailStatus(status, res)
	message := dispatchResultMessage(status, res)

	prURLs := completedPRURLs(res)

	result := map[string]any{
		"exitCode":     res.ExitCode,
		"workspaceDir": res.WorkspaceDir,
		"branch":       res.Branch,
		"prUrl":        res.PRURL,
		"prUrls":       splitNonEmptyCSV(prURLs),
		"changedRepos": countChangedRepoResults(res.RepoResults),
		"repoResults":  repoResultPayloads(res.RepoResults),
		"noChanges":    res.NoChanges,
		"status":       detailStatus,
		"message":      message,
	}
	if res.Err != nil {
		result["error"] = res.Err.Error()
	}

	payload := map[string]any{
		"type":       cfg.Skill.ResultType,
		"skill":      firstNonEmpty(dispatch.Skill, cfg.Skill.Name),
		"request_id": dispatch.RequestID,
		"status":     status,
		"failed":     res.Err != nil,
		"ok":         res.Err == nil,
		"message":    message,
		"result":     result,
	}
	if res.Err != nil {
		errText := res.Err.Error()
		payload["error"] = errText
		payload["failure"] = map[string]any{
			"status":  "failed",
			"message": message,
			"error":   errText,
			"details": result,
		}
	}
	if dispatch.ReplyTo != "" {
		payload["reply_to"] = dispatch.ReplyTo
		payload["to"] = dispatch.ReplyTo
	}
	return payload
}

func dispatchResultDetailStatus(status string, res harness.Result) string {
	if res.Err != nil || status == "error" {
		return "failed"
	}
	return status
}

func dispatchResultMessage(status string, res harness.Result) string {
	if res.Err != nil || status == "error" {
		return failureResponseMessage(res.Err.Error())
	}
	if status == "no_changes" {
		return "No changes: task completed without repository changes or pull requests."
	}
	return "Success: task completed."
}

func failureResponseMessage(errText string) string {
	errText = strings.TrimSpace(errText)
	if errText == "" {
		return "Failure: task failed. Error details: unknown error."
	}
	return "Failure: task failed. Error details: " + errText
}

func (d Daemon) handleFailedDispatchAfterPublish(
	ctx context.Context,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatch SkillDispatch,
	res harness.Result,
) {
	if api == nil {
		return
	}
	if err := api.MarkOpenClawOffline(ctx, cfg.SessionKey, transportOfflineReasonExecutionFailure); err != nil {
		d.logf("dispatch status=warn action=mark_offline request_id=%s err=%q", dispatch.RequestID, err)
	}
	if ok, reason := shouldQueueFailureRerun(dispatch, res); !ok {
		d.logf(
			"dispatch status=warn action=skip_failure_rerun request_id=%s err=%q",
			dispatch.RequestID,
			reason,
		)
	} else if err := queueFailureRerun(ctx, api, cfg, dispatch); err != nil {
		d.logf("dispatch status=rerun_error request_id=%s err=%q", dispatch.RequestID, err)
	}
	if ok, reason := shouldQueueFailureFollowUp(dispatch, res); !ok {
		d.logf(
			"dispatch status=warn action=skip_failure_followup request_id=%s err=%q",
			dispatch.RequestID,
			reason,
		)
	} else if err := queueFailureFollowUp(ctx, api, cfg, dispatch, res, d.TaskLogRoot); err != nil {
		d.logf("dispatch status=follow_up_error request_id=%s err=%q", dispatch.RequestID, err)
	}
}

func shouldQueueFailureRerun(dispatch SkillDispatch, res harness.Result) (bool, string) {
	if res.Err == nil {
		return false, "failed task did not include an error"
	}
	if isFailureFollowUpRequestID(dispatch.RequestID) {
		return false, "run is already a failure follow-up"
	}
	if isFailureRerunRequestID(dispatch.RequestID) {
		return false, "run is already a failure rerun"
	}
	return true, ""
}

func shouldQueueFailureFollowUp(dispatch SkillDispatch, res harness.Result) (bool, string) {
	if res.Err == nil {
		return false, "failed task did not include an error"
	}
	if isFailureFollowUpRequestID(dispatch.RequestID) {
		return false, "run is already a failure follow-up"
	}
	return true, ""
}

func queueFailureRerun(ctx context.Context, api MoltenHubAPI, cfg InitConfig, dispatch SkillDispatch) error {
	if api == nil {
		return fmt.Errorf("moltenhub api client is required")
	}
	runConfig, err := dispatchRunConfigPayload(dispatch.Config)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"type":       firstNonEmpty(cfg.Skill.DispatchType, defaultRuntimeDispatchType),
		"skill":      firstNonEmpty(cfg.Skill.Name, dispatch.Skill),
		"request_id": failureRerunRequestID(dispatch.RequestID),
		"config":     runConfig,
		"rerun_of":   strings.TrimSpace(dispatch.RequestID),
	}

	return api.PublishResult(ctx, payload)
}

func queueFailureFollowUp(ctx context.Context, api MoltenHubAPI, cfg InitConfig, dispatch SkillDispatch, res harness.Result, taskLogRoot string) error {
	if api == nil {
		return fmt.Errorf("moltenhub api client is required")
	}
	repos := failureFollowUpRepos(res, dispatch.Config)
	if len(repos) == 0 {
		return fmt.Errorf("failed dispatch is missing repository context")
	}

	runConfig := map[string]any{
		"repos":        repos,
		"baseBranch":   failureFollowUpBaseBranch,
		"targetSubdir": failureFollowUpTargetSubdir,
		"prompt":       failureFollowUpPrompt(taskLogRoot, dispatch, res),
	}

	payload := map[string]any{
		"type":       firstNonEmpty(cfg.Skill.DispatchType, defaultRuntimeDispatchType),
		"skill":      firstNonEmpty(cfg.Skill.Name, dispatch.Skill),
		"request_id": failureFollowUpRequestID(dispatch.RequestID),
		"config":     runConfig,
	}

	return api.PublishResult(ctx, payload)
}

func dispatchRunConfigPayload(runCfg config.Config) (map[string]any, error) {
	runCfg.ApplyDefaults()
	if err := runCfg.Validate(); err != nil {
		return nil, fmt.Errorf("normalize run config payload: %w", err)
	}

	encoded, err := json.Marshal(runCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal run config payload: %w", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("decode run config payload: %w", err)
	}
	return payload, nil
}

func failureFollowUpRepos(_ harness.Result, _ config.Config) []string {
	if repo := strings.TrimSpace(failurefollowup.FollowUpRepositoryURL); repo != "" {
		return []string{repo}
	}
	return nil
}

func failureFollowUpPrompt(logRoot string, dispatch SkillDispatch, res harness.Result) string {
	paths := failureLogPaths(logRoot, dispatch.RequestID, dispatch.Config, res)
	return failurefollowup.ComposePrompt(
		failureFollowUpPromptBase,
		paths,
		nil,
		failureFollowUpNoPathGuidance,
		failureFollowUpContext(dispatch, res),
	)
}

func failureFollowUpContext(dispatch SkillDispatch, res harness.Result) string {
	lines := []string{
		"Observed failure context:",
		fmt.Sprintf("- request_id=%s", strings.TrimSpace(dispatch.RequestID)),
		fmt.Sprintf("- exit_code=%d", res.ExitCode),
	}
	if res.Err != nil {
		lines = append(lines, fmt.Sprintf("- error=%q", res.Err.Error()))
	}
	if workspaceDir := strings.TrimSpace(res.WorkspaceDir); workspaceDir != "" {
		lines = append(lines, fmt.Sprintf("- workspace_dir=%s", workspaceDir))
	}
	if branch := strings.TrimSpace(res.Branch); branch != "" {
		lines = append(lines, fmt.Sprintf("- branch=%s", branch))
	}
	if prURL := strings.TrimSpace(res.PRURL); prURL != "" {
		lines = append(lines, fmt.Sprintf("- pr_url=%s", prURL))
	}
	if repos := dispatch.Config.RepoList(); len(repos) > 0 {
		lines = append(lines, fmt.Sprintf("- repos=%s", strings.Join(repos, ",")))
	}
	if targetSubdir := strings.TrimSpace(dispatch.Config.TargetSubdir); targetSubdir != "" {
		lines = append(lines, fmt.Sprintf("- target_subdir=%s", targetSubdir))
	}
	if prompt := strings.TrimSpace(dispatch.Config.Prompt); prompt != "" {
		lines = append(lines, "Original task prompt:")
		lines = append(lines, prompt)
	}
	return strings.Join(lines, "\n")
}

func failureLogPaths(logRoot, requestID string, runCfg config.Config, res harness.Result) []string {
	seen := map[string]struct{}{}
	paths := make([]string, 0, len(res.RepoResults)+5)
	appendPath := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, exists := seen[path]; exists {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	for _, path := range failurefollowup.TaskLogPaths(logRoot, requestID) {
		appendPath(path)
	}
	appendPath(res.WorkspaceDir)
	for _, repo := range res.RepoResults {
		appendPath(repo.RepoDir)
		if repoDir := strings.TrimSpace(repo.RepoDir); repoDir != "" {
			targetSubdir := strings.TrimSpace(runCfg.TargetSubdir)
			if targetSubdir != "" && targetSubdir != "." {
				appendPath(filepath.Join(repoDir, targetSubdir))
			}
		}
	}
	return paths
}

func failureFollowUpRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "failure-review"
	}
	if isFailureFollowUpRequestID(requestID) {
		return requestID
	}
	return requestID + failureFollowUpRequestIDSuffix
}

func failureRerunRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "rerun"
	}
	if isFailureRerunRequestID(requestID) {
		return requestID
	}
	return requestID + failureRerunRequestIDSuffix
}

func isFailureFollowUpRequestID(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	return strings.HasSuffix(requestID, failureFollowUpRequestIDSuffix)
}

func isFailureRerunRequestID(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	return strings.HasSuffix(requestID, failureRerunRequestIDSuffix)
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

func joinAllRepoPRURLs(results []harness.RepoResult) string {
	if len(results) == 0 {
		return ""
	}
	urls := make([]string, 0, len(results))
	for _, result := range results {
		url := strings.TrimSpace(result.PRURL)
		if url == "" {
			continue
		}
		urls = append(urls, url)
	}
	return strings.Join(urls, ",")
}

func resultHasPR(result harness.Result) bool {
	if strings.TrimSpace(result.PRURL) != "" {
		return true
	}
	return strings.TrimSpace(joinAllRepoPRURLs(result.RepoResults)) != ""
}

func completedPRURLs(result harness.Result) string {
	if result.NoChanges {
		return joinAllRepoPRURLs(result.RepoResults)
	}
	return joinRepoPRURLs(result.RepoResults)
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
			"repoUrl": result.RepoURL,
			"repoDir": result.RepoDir,
			"branch":  result.Branch,
			"prUrl":   result.PRURL,
			"changed": result.Changed,
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
	if strings.TrimSpace(cfg.AgentToken) != "" || strings.TrimSpace(cfg.BindToken) != "" {
		return false
	}

	token := strings.TrimSpace(stored.AgentToken)
	if token == "" {
		return false
	}

	cfg.AgentToken = token
	cfg.BindToken = ""

	// Keep the init-config endpoint as the source of truth for this run.
	// Persisted runtime config is used for credential/session reuse only.
	if strings.TrimSpace(cfg.BaseURL) == "" {
		baseURL := strings.TrimSpace(stored.BaseURL)
		if baseURL != "" {
			cfg.BaseURL = strings.TrimRight(baseURL, "/")
		}
	}

	sessionKey := strings.TrimSpace(stored.SessionKey)
	if sessionKey != "" {
		cfg.SessionKey = sessionKey
	}

	return true
}

func loadStoredRuntimeConfig(primaryPath string) (RuntimeConfig, string, error) {
	candidates := runtimeConfigCandidatePaths(primaryPath)
	var firstErr error
	for _, candidate := range candidates {
		stored, err := LoadRuntimeConfig(candidate)
		if err == nil {
			return stored, candidate, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return RuntimeConfig{}, candidate, err
	}
	return RuntimeConfig{}, candidates[0], firstErr
}

func dispatchParseErrorPayload(cfg InitConfig, dispatch SkillDispatch, parseErr error) map[string]any {
	payload := dispatchResultPayload(cfg, dispatch, harness.Result{
		ExitCode: harness.ExitConfig,
		Err:      fmt.Errorf("dispatch parse: %w", parseErr),
	})
	result := payload["result"].(map[string]any)
	result["requiredSchema"] = requiredSkillPayloadSchema(cfg.Skill.DispatchType, cfg.Skill.Name, currentLibraryTaskNames())
	return payload
}

func currentLibraryTaskNames() []string {
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err != nil {
		return nil
	}
	return catalog.Names()
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
