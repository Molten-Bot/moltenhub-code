package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/app"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/failurefollowup"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
	"github.com/a2aproject/a2a-go/v2/a2a"
)

// DispatchTaskControl exposes per-request runtime controls shared with the UI.
type DispatchTaskControl interface {
	WaitUntilRunnable(context.Context) error
	SetAcquireCancel(context.CancelFunc)
	ClearAcquireCancel(context.CancelFunc)
	SetRunning(bool)
	ConsumeForceAcquire() bool
	HasForceAcquire() bool
	IsPaused() bool
	IsStopped() bool
}

// Daemon listens for hub skill dispatches and runs harness jobs.
type Daemon struct {
	Runner              execx.Runner
	Logf                func(string, ...any)
	OnDispatchQueued    func(requestID string, runCfg config.Config)
	OnDispatchFailed    func(requestID string, runCfg config.Config, result app.Result)
	RegisterTaskControl func(requestID string, cancel context.CancelCauseFunc) DispatchTaskControl
	CompleteTaskControl func(requestID string)
	DispatchController  *AdaptiveDispatchController
	ReconnectDelay      time.Duration
	TaskLogRoot         string
}

const wsUpgradePullProbeTimeoutMs = 1000
const dispatchDedupTTL = 2 * time.Hour
const agentStatusUpdateTimeout = 5 * time.Second
const failureFollowUpRequestIDSuffix = "-failure-review"
const failureRerunRequestIDSuffix = "-rerun"
const automaticFailureRerunDisabledReason = "automatic failure rerun disabled; queue failure follow-up in moltenhub-code"
const failureFollowUpPromptBase = failurefollowup.RequiredPrompt
const failureFollowUpNoPathGuidance = "No workspace or log path was captured before the failure. Investigate the task history and runtime error details first."
const failureFollowUpTargetSubdir = "."
const transportOfflineReasonExecutionFailure = "task_execution_failure"
const dispatchTaskStatusType = "task_status_update"

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
		d.logf("hub.runtime_config status=warn action=save err=%q", err)
	} else {
		d.logf("hub.runtime_config status=saved path=%s", runtimeCfgPath)
	}

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
		if err := api.MarkRuntimeOffline(shutdownCtx, cfg.SessionKey, transportOfflineReasonAgent); err != nil {
			d.logf("hub.transport status=warn mode=runtime_ws err=%q", err)
		} else {
			d.logf("hub.transport status=offline mode=runtime_ws")
		}
		if err := api.UpdateAgentStatus(shutdownCtx, "offline"); err != nil {
			d.logf("hub.agent status=warn state=offline err=%q", err)
			return
		}
		d.logf("hub.agent status=offline")
	}()

	d.logf("hub.transport primary=runtime_ws fallback=runtime_pull")

	dispatchController := d.DispatchController
	if dispatchController == nil {
		dispatchController = NewAdaptiveDispatchController(cfg.Dispatcher, d.logf)
	}
	dispatchController.Start(ctx)

	var workers sync.WaitGroup
	defer workers.Wait()
	deduper := newDispatchDeduper(dispatchDedupTTL)

	wsURL, wsURLErr := WebsocketURL(cfg.BaseURL, cfg.SessionKey)
	legacyWSURL, legacyWSURLErr := OpenClawWebsocketURL(cfg.BaseURL, cfg.SessionKey)
	if wsURLErr != nil {
		d.logf("hub.ws status=disabled err=%q", wsURLErr)
		d.logf("hub.transport mode=runtime_pull")
		return d.runPullLoop(ctx, api, cfg, dispatchController, &workers, deduper, pullTimeoutMs)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := d.tryWebsocketUpgrade(ctx, transport, wsURL, api, cfg, dispatchController, &workers, deduper)
		if err != nil && isUnsupportedRuntimeWebsocketError(err) && legacyWSURLErr == nil {
			d.logf("hub.ws status=fallback from=runtime_ws to=openclaw_ws err=%q", err)
			err = d.tryWebsocketUpgrade(ctx, transport, legacyWSURL, api, cfg, dispatchController, &workers, deduper)
		}
		if err == nil {
			return nil
		} else if ctx.Err() != nil {
			return nil
		} else if isUnauthorizedHubError(err) {
			return fmt.Errorf("hub auth: %w", err)
		} else if !shouldFallbackToPull(err) {
			d.logf("hub.ws status=disconnected err=%q", err)
			if !sleepWithContext(ctx, d.ReconnectDelay) {
				return nil
			}
			continue
		} else {
			d.logf("hub.ws status=error err=%q", err)
		}

		d.logf("hub.transport mode=runtime_pull")
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

func (d Daemon) tryWebsocketUpgrade(
	ctx context.Context,
	transport APIClient,
	wsURL string,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) error {
	if err := transport.Ping(ctx); err != nil {
		return fmt.Errorf("ping precheck: %w", err)
	}
	if err := transport.Health(ctx); err != nil {
		return fmt.Errorf("health precheck: %w", err)
	}
	if err := d.pullProbeOnce(ctx, api, cfg, dispatchController, workers, deduper); err != nil {
		return fmt.Errorf("pull precheck: %w", err)
	}
	return d.runWebsocketLoop(ctx, wsURL, api, cfg, dispatchController, workers, deduper)
}

func (d Daemon) pullProbeOnce(
	ctx context.Context,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatchController *AdaptiveDispatchController,
	workers *sync.WaitGroup,
	deduper *dispatchDeduper,
) error {
	pulled, found, err := api.PullRuntimeMessage(ctx, wsUpgradePullProbeTimeoutMs)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	d.processInboundMessage(ctx, api, cfg, pulled.Message, pulled.DeliveryID, pulled.MessageID, dispatchController, workers, deduper)
	return nil
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
	pulled, found, err := api.PullRuntimeMessage(ctx, pullTimeoutMs)
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

		inbound := extractInboundRuntimeMessage(raw)
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
	if isNonDispatchHubEvent(msg, cfg) {
		if strings.TrimSpace(deliveryID) != "" {
			if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		return
	}

	dispatch, matched, parseErr := ParseSkillDispatch(msg, cfg.Skill.DispatchType, cfg.Skill.Name)
	if !matched {
		if skill := incomingSkillName(msg); skill != "unknown" {
			d.logf("dispatch status=ignored skill=%s", skill)
		}
		if strings.TrimSpace(deliveryID) != "" {
			if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		return
	}
	fallbackRequestID := dedupeKeyForDispatchFallback(dispatch, messageID, deliveryID)
	if strings.TrimSpace(dispatch.RequestID) == "" {
		dispatch.RequestID = strings.TrimSpace(fallbackRequestID)
	}
	if parseErr != nil {
		d.logf("dispatch status=invalid request_id=%s err=%q", dispatch.RequestID, parseErr)
		d.publishDispatchStatus(
			ctx,
			api,
			cfg,
			dispatch,
			"error",
			a2a.TaskStateFailed,
			failureResponseMessage("dispatch parse: "+parseErr.Error()),
			map[string]any{"error": "dispatch parse: " + parseErr.Error()},
		)
		payload := dispatchParseErrorPayload(cfg, dispatch, parseErr)
		if err := api.PublishResult(ctx, payload); err != nil {
			d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
			if strings.TrimSpace(deliveryID) != "" {
				if nackErr := api.NackRuntimeDelivery(ctx, deliveryID); nackErr != nil {
					d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
				}
			}
			return
		}
		if strings.TrimSpace(deliveryID) != "" {
			if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		return
	}

	dupKey := dedupeKeyForDispatch(dispatch, messageID, deliveryID)
	if strings.TrimSpace(dispatch.RequestID) == "" {
		dispatch.RequestID = firstNonEmpty(strings.TrimSpace(fallbackRequestID), strings.TrimSpace(dupKey))
	}
	if deduper != nil && dupKey != "" {
		if accepted, state, duplicateOf := deduper.Begin(dupKey, dispatch.RequestID); !accepted {
			requestID := firstNonEmpty(dispatch.RequestID, duplicateOf, fallbackRequestID, strings.TrimSpace(dupKey))
			d.logf(
				"dispatch status=duplicate request_id=%s state=%s duplicate_of=%s",
				requestID,
				state,
				duplicateOf,
			)
			d.publishDispatchStatus(
				ctx,
				api,
				cfg,
				dispatch,
				"duplicate",
				a2a.TaskStateRejected,
				duplicateDispatchErrorText(duplicateOf, state),
				map[string]any{
					"duplicate":    true,
					"state":        strings.TrimSpace(state),
					"duplicate_of": strings.TrimSpace(duplicateOf),
				},
			)
			payload := duplicateDispatchResultPayload(cfg, dispatch, state, duplicateOf)
			if err := api.PublishResult(ctx, payload); err != nil {
				d.logf("dispatch status=publish_error request_id=%s err=%q", requestID, err)
				if strings.TrimSpace(deliveryID) != "" {
					if nackErr := api.NackRuntimeDelivery(ctx, deliveryID); nackErr != nil {
						d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
					}
				}
				return
			}
			if strings.TrimSpace(deliveryID) != "" {
				if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
					d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
				}
			}
			return
		}
	}
	if dispatchController == nil {
		dispatchController = NewAdaptiveDispatchController(cfg.Dispatcher, d.logf)
		dispatchController.Start(ctx)
	}

	runCtx, cancelRun := context.WithCancelCause(ctx)
	var taskControl DispatchTaskControl
	if d.RegisterTaskControl != nil {
		taskControl = d.RegisterTaskControl(dispatch.RequestID, cancelRun)
	}
	if d.OnDispatchQueued != nil {
		d.OnDispatchQueued(dispatch.RequestID, dispatch.Config)
	}
	d.publishDispatchStatus(ctx, api, cfg, dispatch, "queued", a2a.TaskStateSubmitted, "Task queued.", nil)

	ackedEarly := false
	if strings.TrimSpace(deliveryID) != "" {
		if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
			d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
		} else {
			ackedEarly = true
			d.logf("dispatch status=ack request_id=%s delivery_id=%s", firstNonEmpty(dispatch.RequestID, dupKey), deliveryID)
		}
	}

	workers.Add(1)
	go func(
		dispatch SkillDispatch,
		deliveryID string,
		dedupeKey string,
		ackedEarly bool,
		runCtx context.Context,
		cancelRun context.CancelCauseFunc,
		taskControl DispatchTaskControl,
	) {
		defer workers.Done()
		defer cancelRun(nil)
		if d.CompleteTaskControl != nil {
			defer d.CompleteTaskControl(dispatch.RequestID)
		}

		finalState := "completed"
		if deduper != nil {
			defer func() {
				deduper.Done(dedupeKey, dispatch.RequestID, finalState)
			}()
		}

		publishFailure := func(status, stage string, err error, triggerFollowUps bool) {
			finalState = "error"
			if err == nil {
				err = errors.New("unknown error")
			}
			failErr := err
			if strings.TrimSpace(stage) != "" {
				failErr = fmt.Errorf("%s: %w", stage, err)
			}
			failRes := app.Result{
				ExitCode: app.ExitPreflight,
				Err:      failErr,
			}
			if triggerFollowUps && d.OnDispatchFailed != nil {
				d.OnDispatchFailed(dispatch.RequestID, dispatch.Config, failRes)
			}
			taskState := a2a.TaskStateFailed
			if firstNonEmpty(status, "error") == "stopped" {
				taskState = a2a.TaskStateCanceled
			}
			d.publishDispatchStatus(
				runCtx,
				api,
				cfg,
				dispatch,
				firstNonEmpty(status, "error"),
				taskState,
				dispatchResultMessage("error", failRes),
				map[string]any{"error": failErr.Error()},
			)
			payload := dispatchResultPayload(cfg, dispatch, failRes)
			if publishErr := api.PublishResult(runCtx, payload); publishErr != nil {
				d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, publishErr)
				if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
					if nackErr := api.NackRuntimeDelivery(runCtx, deliveryID); nackErr != nil {
						d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
					}
				}
			} else {
				if triggerFollowUps {
					d.handleFailedDispatchAfterPublish(runCtx, api, cfg, dispatch, failRes)
				}
				if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
					if ackErr := api.AckRuntimeDelivery(runCtx, deliveryID); ackErr != nil {
						d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, ackErr)
					}
				}
			}
			d.logf(
				"dispatch status=%s request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
				firstNonEmpty(status, "error"),
				firstNonEmpty(dispatch.RequestID, dedupeKey),
				failRes.ExitCode,
				failRes.WorkspaceDir,
				failRes.Branch,
				failRes.PRURL,
				failRes.Err,
			)
		}

		for {
			if taskControl != nil {
				d.publishDispatchStatus(runCtx, api, cfg, dispatch, "waiting", a2a.TaskStateSubmitted, "Task waiting for local controls.", nil)
				if waitErr := taskControl.WaitUntilRunnable(runCtx); waitErr != nil {
					if taskControl.IsStopped() || isStoppedByOperatorErr(context.Cause(runCtx)) {
						stopErr := context.Cause(runCtx)
						if stopErr == nil {
							stopErr = errors.New("task was stopped by operator")
						}
						publishFailure("stopped", "", stopErr, false)
						return
					}
					if errors.Is(waitErr, context.Canceled) {
						return
					}
					publishFailure("error", "dispatch wait", waitErr, true)
					return
				}
			}

			acquireCtx, cancelAcquire := context.WithCancel(runCtx)
			if taskControl != nil {
				taskControl.SetAcquireCancel(cancelAcquire)
			}
			forceAcquire := false
			if taskControl != nil {
				forceAcquire = taskControl.ConsumeForceAcquire()
			}
			acquire := dispatchController.Acquire
			if forceAcquire {
				acquire = dispatchController.AcquireForce
			}
			release, acquireErr := acquire(acquireCtx, dispatch.RequestID)
			if taskControl != nil {
				taskControl.ClearAcquireCancel(cancelAcquire)
			}
			cancelAcquire()

			if acquireErr != nil {
				if taskControl != nil && taskControl.IsStopped() {
					stopErr := context.Cause(runCtx)
					if stopErr == nil {
						stopErr = errors.New("task was stopped by operator")
					}
					publishFailure("stopped", "", stopErr, false)
					return
				}
				if taskControl != nil && taskControl.IsPaused() && errors.Is(acquireErr, context.Canceled) && runCtx.Err() == nil {
					continue
				}
				if taskControl != nil && taskControl.HasForceAcquire() && errors.Is(acquireErr, context.Canceled) && runCtx.Err() == nil {
					continue
				}
				if errors.Is(acquireErr, context.Canceled) {
					return
				}
				publishFailure("error", "dispatch acquire", acquireErr, true)
				return
			}

			if taskControl != nil {
				taskControl.SetRunning(true)
			}
			finalState = d.handleDispatch(runCtx, api, cfg, dispatch, deliveryID, ackedEarly)
			if taskControl != nil {
				taskControl.SetRunning(false)
			}
			release()
			return
		}
	}(dispatch, deliveryID, dupKey, ackedEarly, runCtx, cancelRun, taskControl)
}

func isNonDispatchHubEvent(msg map[string]any, cfg InitConfig) bool {
	if len(msg) == 0 {
		return false
	}

	eventType := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringAt(msg, "type"),
		stringAt(msg, "event"),
		stringAt(msg, "kind"),
		stringAt(msg, "message_type"),
		stringAtPath(msg, "payload", "type"),
		stringAtPath(msg, "payload", "event"),
		stringAtPath(msg, "data", "type"),
		stringAtPath(msg, "data", "event"),
	)))
	resultType := strings.ToLower(strings.TrimSpace(cfg.Skill.ResultType))
	switch eventType {
	case "ack", "acks", "acknowledgement", "acknowledgment",
		"message_ack", "message_acknowledgement", "message_acknowledgment",
		"delivery_ack", "delivery_acknowledgement", "delivery_acknowledgment",
		"openclaw_ack", "openclaw.ack":
		return true
	case dispatchTaskStatusType, "task_status":
		return true
	}
	if resultType != "" && eventType == resultType {
		return true
	}

	method := strings.ToLower(strings.TrimSpace(stringAt(msg, "method")))
	if method == "" && (toMap(msg["result"]) != nil || toMap(msg["task"]) != nil || toMap(msg["status"]) != nil || toMap(msg["statusUpdate"]) != nil) {
		return true
	}
	if strings.TrimSpace(stringAtPath(msg, "params", "status", "state")) != "" ||
		strings.TrimSpace(stringAtPath(msg, "result", "status", "state")) != "" {
		return method == "" || method == "tasks/get" || method == "tasks/cancel"
	}
	return false
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

func isUnsupportedRuntimeWebsocketError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, status := range []string{"status=404", "status=405", "status=501"} {
		if strings.Contains(text, "websocket handshake") && strings.Contains(text, status) {
			return true
		}
	}
	return false
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

func isStoppedByOperatorErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "stopped by operator")
}

func dedupeKeyForDispatch(dispatch SkillDispatch, messageID, deliveryID string) string {
	if key := dedupeKeyForRunConfig(dispatch.Config); key != "" {
		return key
	}
	return dedupeKeyForDispatchFallback(dispatch, messageID, deliveryID)
}

func dedupeKeyForDispatchFallback(dispatch SkillDispatch, messageID, deliveryID string) string {
	return firstNonEmpty(
		dispatch.RequestID,
		strings.TrimSpace(messageID),
		strings.TrimSpace(deliveryID),
	)
}

type dispatchDeduper struct {
	mu        sync.Mutex
	inFlight  map[string]string
	completed map[string]dispatchDedupeRecord
	ttl       time.Duration
}

type dispatchDedupeRecord struct {
	requestID   string
	completedAt time.Time
}

func newDispatchDeduper(ttl time.Duration) *dispatchDeduper {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &dispatchDeduper{
		inFlight:  map[string]string{},
		completed: map[string]dispatchDedupeRecord{},
		ttl:       ttl,
	}
}

func (d *dispatchDeduper) Begin(key, requestID string) (bool, string, string) {
	if d == nil {
		return true, "accepted", ""
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true, "accepted", ""
	}
	requestID = strings.TrimSpace(requestID)

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gcLocked(now)

	if existingRequestID, exists := d.inFlight[key]; exists {
		return false, "in_flight", existingRequestID
	}
	if existingRecord, exists := d.completed[key]; exists {
		return false, "completed", existingRecord.requestID
	}
	d.inFlight[key] = requestID
	return true, "accepted", ""
}

func (d *dispatchDeduper) Done(key, requestID, finalState string) {
	if d == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	requestID = strings.TrimSpace(requestID)
	finalState = strings.TrimSpace(finalState)

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	if existingRequestID, exists := d.inFlight[key]; exists {
		delete(d.inFlight, key)
		if existingRequestID != "" {
			requestID = existingRequestID
		}
	}
	if finalState == "error" {
		d.gcLocked(now)
		return
	}
	if requestID != "" {
		d.completed[key] = dispatchDedupeRecord{
			requestID:   requestID,
			completedAt: now,
		}
	}
	d.gcLocked(now)
}

func (d *dispatchDeduper) gcLocked(now time.Time) {
	if d == nil || d.ttl <= 0 {
		return
	}
	for key, record := range d.completed {
		if now.Sub(record.completedAt) > d.ttl {
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
) string {
	boundRunCfg, bindErr := ApplyBoundAgentRuntime(dispatch.Config, cfg)
	if bindErr != nil {
		failRes := app.Result{
			ExitCode: app.ExitConfig,
			Err:      fmt.Errorf("apply bound agent runtime: %w", bindErr),
		}
		if d.OnDispatchFailed != nil {
			d.OnDispatchFailed(dispatch.RequestID, dispatch.Config, failRes)
		}
		d.publishDispatchStatus(
			ctx,
			api,
			cfg,
			dispatch,
			"error",
			a2a.TaskStateFailed,
			dispatchResultMessage("error", failRes),
			map[string]any{"error": failRes.Err.Error()},
		)
		payload := dispatchResultPayload(cfg, dispatch, failRes)
		if err := api.PublishResult(ctx, payload); err != nil {
			d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
			if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
				if nackErr := api.NackRuntimeDelivery(ctx, deliveryID); nackErr != nil {
					d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
				}
			}
			return "error"
		}
		if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
			if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
			}
		}
		d.logf(
			"dispatch status=error request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
			dispatch.RequestID,
			failRes.ExitCode,
			failRes.WorkspaceDir,
			failRes.Branch,
			failRes.PRURL,
			failRes.Err,
		)
		return "error"
	}
	dispatch.Config = boundRunCfg

	d.logf(
		"dispatch status=start request_id=%s skill=%s repo=%s repos=%s",
		dispatch.RequestID,
		dispatch.Skill,
		dispatch.Config.RepoURL,
		strings.Join(dispatch.Config.RepoList(), ","),
	)
	d.publishDispatchStatus(ctx, api, cfg, dispatch, "working", a2a.TaskStateWorking, dispatchStartStatusMessage(dispatch.Config), nil)
	if api != nil {
		if err := api.RecordRunStartedActivity(ctx, dispatch.Config); err != nil {
			d.logf("dispatch status=warn action=record_run_started_activity request_id=%s err=%q", dispatch.RequestID, err)
		}
	}
	if taskName := strings.TrimSpace(dispatch.Config.LibraryTaskName); taskName != "" {
		if err := IncrementRuntimeConfigLibraryTaskUsage(cfg.RuntimeConfigPath, cfg, taskName); err != nil {
			d.logf("library.usage status=warn task=%s request_id=%s err=%q", taskName, dispatch.RequestID, err)
		}
	}

	h := app.New(d.Runner)
	h.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if dispatch.RequestID != "" {
			d.logf("dispatch request_id=%s %s", dispatch.RequestID, line)
			if status, state, message, details, ok := dispatchStatusFromHarnessLogLine(line); ok {
				d.publishDispatchStatus(ctx, api, cfg, dispatch, status, state, message, details)
			}
			return
		}
		d.logf("dispatch %s", line)
	}

	res := h.Run(ctx, dispatch.Config)
	stoppedByOperator := false
	if stopErr := context.Cause(ctx); isStoppedByOperatorErr(stopErr) {
		stoppedByOperator = true
		if res.ExitCode == app.ExitSuccess {
			res.ExitCode = app.ExitPreflight
		}
		res.Err = stopErr
	}
	if res.Err != nil && !stoppedByOperator && d.OnDispatchFailed != nil {
		d.OnDispatchFailed(dispatch.RequestID, dispatch.Config, res)
	}
	status := "completed"
	taskState := a2a.TaskStateCompleted
	if res.Err != nil {
		status = "error"
		taskState = a2a.TaskStateFailed
		if stoppedByOperator {
			status = "stopped"
			taskState = a2a.TaskStateCanceled
		}
	} else if res.NoChanges && !resultHasPR(res) {
		status = "no_changes"
	}
	statusDetails := map[string]any{
		"exitCode":     res.ExitCode,
		"workspaceDir": res.WorkspaceDir,
		"branch":       res.Branch,
		"prUrl":        res.PRURL,
		"prUrls":       splitNonEmptyCSV(completedPRURLs(res)),
		"changedRepos": countChangedRepoResults(res.RepoResults),
		"noChanges":    res.NoChanges,
	}
	if res.Err != nil {
		statusDetails["error"] = res.Err.Error()
	}
	d.publishDispatchStatus(ctx, api, cfg, dispatch, status, taskState, dispatchResultMessage(status, res), statusDetails)
	payload := dispatchResultPayload(cfg, dispatch, res)
	if err := api.PublishResult(ctx, payload); err != nil {
		d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
		if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
			if nackErr := api.NackRuntimeDelivery(ctx, deliveryID); nackErr != nil {
				d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
			}
		}
		if res.Err != nil {
			return "error"
		}
		if res.NoChanges && !resultHasPR(res) {
			return "no_changes"
		}
		return "completed"
	}
	if res.Err != nil && !stoppedByOperator {
		d.handleFailedDispatchAfterPublish(ctx, api, cfg, dispatch, res)
	}
	if !ackedEarly && strings.TrimSpace(deliveryID) != "" {
		if err := api.AckRuntimeDelivery(ctx, deliveryID); err != nil {
			d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
		}
	}

	if res.Err != nil {
		status := "error"
		if stoppedByOperator {
			status = "stopped"
		}
		d.logf(
			"dispatch status=%s request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
			status,
			dispatch.RequestID,
			res.ExitCode,
			res.WorkspaceDir,
			res.Branch,
			res.PRURL,
			res.Err,
		)
		return "error"
	}
	if api != nil {
		if err := api.RecordRunCompletedActivity(ctx, dispatch.Config); err != nil {
			d.logf("dispatch status=warn action=record_run_completed_activity request_id=%s err=%q", dispatch.RequestID, err)
		}
	}
	if res.NoChanges && !resultHasPR(res) {
		d.logf(
			"dispatch status=no_changes request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s",
			dispatch.RequestID,
			res.WorkspaceDir,
			res.Branch,
			res.PRURL,
			joinAllRepoPRURLs(res.RepoResults),
		)
		return "no_changes"
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
	return "completed"
}

func (d Daemon) recordActivity(ctx context.Context, api MoltenHubAPI, requestID, activity, action string) {
	if api == nil {
		return
	}
	if err := api.RecordActivity(ctx, activity); err != nil {
		d.logf("dispatch status=warn action=%s request_id=%s err=%q", action, requestID, err)
	}
}

func (d Daemon) recordGitHubTaskCompleteActivity(ctx context.Context, api MoltenHubAPI, requestID string) {
	if api == nil {
		return
	}
	if err := api.RecordGitHubTaskCompleteActivity(ctx); err != nil {
		d.logf("dispatch status=warn action=record_github_task_complete request_id=%s err=%q", requestID, err)
	}
}

func (d Daemon) recordCodingActivityRunning(ctx context.Context, api MoltenHubAPI, requestID string) {
	if api == nil {
		return
	}
	if err := api.RecordCodingActivityRunning(ctx); err != nil {
		d.logf("dispatch status=warn action=record_coding_activity_running request_id=%s err=%q", requestID, err)
	}
}

func (d Daemon) publishDispatchStatus(
	ctx context.Context,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatch SkillDispatch,
	status string,
	state a2a.TaskState,
	message string,
	details map[string]any,
) {
	if api == nil {
		return
	}
	payload := dispatchStatusPayload(cfg, dispatch, status, state, message, details)
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	errCh := api.PublishResultAsync(ctx, payload)
	go func() {
		if err := <-errCh; err != nil {
			d.logf(
				"dispatch status=warn action=publish_status_update request_id=%s task_status=%s err=%q",
				dispatch.RequestID,
				strings.TrimSpace(status),
				err,
			)
		}
	}()
}

func dispatchStatusPayload(
	cfg InitConfig,
	dispatch SkillDispatch,
	status string,
	state a2a.TaskState,
	message string,
	details map[string]any,
) map[string]any {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "working"
	}
	if state == a2a.TaskStateUnspecified {
		state = a2a.TaskStateWorking
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Task status updated."
	}

	taskID := dispatchStatusTaskID(dispatch)
	contextID := dispatchStatusContextID(dispatch)
	skill := firstNonEmpty(dispatch.Skill, cfg.Skill.Name)
	metadata := map[string]any{
		"request_id": dispatch.RequestID,
		"skill":      skill,
		"status":     status,
	}
	if taskName := strings.TrimSpace(dispatch.Config.LibraryTaskName); taskName != "" {
		metadata["library_task_name"] = taskName
	}
	if displayName := strings.TrimSpace(dispatch.Config.LibraryTaskDisplayName); displayName != "" {
		metadata["library_task_display_name"] = displayName
	}
	if stage := firstString(details["stage"]); stage != "" {
		metadata["stage"] = stage
	}
	if stageStatus := firstString(details["stage_status"], details["status"]); stageStatus != "" {
		metadata["stage_status"] = stageStatus
	}
	if originator := dispatchOriginatorPayload(dispatch); originator != nil {
		metadata["originator"] = originator
	}

	payload := map[string]any{
		"type":          dispatchTaskStatusType,
		"protocol":      "a2a.v1",
		"skill":         skill,
		"request_id":    dispatch.RequestID,
		"client_msg_id": dispatchStatusMessageID(dispatch, status, details),
		"status":        status,
		"a2a_state":     state.String(),
		"task_state":    state.String(),
		"message":       message,
		"statusUpdate":  a2aStatusUpdatePayload(taskID, contextID, state, message, metadata),
	}
	if taskID != "" {
		payload["a2a_task_id"] = taskID
		payload["task_id"] = taskID
	}
	if hubTaskID := strings.TrimSpace(dispatch.HubTaskID); hubTaskID != "" {
		payload["hub_task_id"] = hubTaskID
	}
	if contextID != "" {
		payload["a2a_context_id"] = contextID
		payload["context_id"] = contextID
	}
	if len(details) > 0 {
		payload["details"] = cloneMetadataMap(details)
	}
	if originator := dispatchOriginatorPayload(dispatch); originator != nil {
		payload["originator"] = originator
		if id := firstString(originator["id"]); id != "" {
			payload["originator_id"] = id
		}
	}
	if state == a2a.TaskStateFailed || state == a2a.TaskStateRejected {
		payload["failed"] = true
		errText := firstNonEmpty(firstString(details["error"]), message)
		payload["error"] = errText
		addExplicitFailureFields(payload, errText)
	} else {
		payload["failed"] = false
	}
	payload["ok"] = a2aTaskStateOK(state)
	applyDispatchResponseRouting(payload, dispatch)
	return payload
}

func dispatchStartStatusMessage(runCfg config.Config) string {
	if activity := RunStartedActivity(runCfg); activity != "" {
		return activity
	}
	return "Task running."
}

func dispatchStatusFromHarnessLogLine(line string) (string, a2a.TaskState, string, map[string]any, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.Contains(line, "stage=") || !strings.Contains(line, "status=") {
		return "", a2a.TaskStateUnspecified, "", nil, false
	}
	fields := parseDispatchLogKVFields(line)
	stage := strings.TrimSpace(fields["stage"])
	stageStatus := strings.TrimSpace(fields["status"])
	if stage == "" || stageStatus == "" {
		return "", a2a.TaskStateUnspecified, "", nil, false
	}

	details := map[string]any{
		"stage":        stage,
		"stage_status": stageStatus,
	}
	for key, value := range fields {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		details[key] = value
	}

	status := "working"
	state := a2a.TaskStateWorking
	message := dispatchStageStatusMessage(stage, stageStatus)
	if stageStatus == "error" || stageStatus == "failed" {
		status = "error"
		state = a2a.TaskStateFailed
		errText := firstNonEmpty(fields["err"], fields["error"], message)
		details["error"] = errText
		message = failureResponseMessage(errText)
	}
	return status, state, message, details, true
}

func dispatchStageStatusMessage(stage, status string) string {
	stage = strings.TrimSpace(stage)
	status = strings.TrimSpace(status)
	if stage == "" {
		return "Task status updated."
	}
	if status == "" {
		return "Task stage updated: " + stage + "."
	}
	return "Task stage updated: " + stage + " " + status + "."
}

func parseDispatchLogKVFields(line string) map[string]string {
	if !strings.Contains(line, "=") {
		return nil
	}
	out := make(map[string]string, 8)
	for idx := 0; idx < len(line); {
		for idx < len(line) && isDispatchLogKVSpace(line[idx]) {
			idx++
		}
		if idx >= len(line) {
			break
		}

		keyStart := idx
		for idx < len(line) && !isDispatchLogKVSpace(line[idx]) && line[idx] != '=' {
			idx++
		}
		if idx >= len(line) || line[idx] != '=' {
			for idx < len(line) && !isDispatchLogKVSpace(line[idx]) {
				idx++
			}
			continue
		}

		key := strings.TrimSpace(line[keyStart:idx])
		idx++
		if key == "" {
			continue
		}

		value, next := parseDispatchLogKVValue(line, idx)
		out[key] = value
		idx = next
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseDispatchLogKVValue(line string, idx int) (string, int) {
	if idx >= len(line) {
		return "", idx
	}
	if line[idx] == '"' {
		if token, ok := parseDispatchLogQuotedToken(line[idx:]); ok {
			if decoded, err := strconv.Unquote(token); err == nil {
				return strings.TrimSpace(decoded), idx + len(token)
			}
			return strings.TrimSpace(strings.Trim(token, `"`)), idx + len(token)
		}
	}

	start := idx
	for idx < len(line) && !isDispatchLogKVSpace(line[idx]) {
		idx++
	}
	return strings.TrimSpace(line[start:idx]), idx
}

func parseDispatchLogQuotedToken(text string) (string, bool) {
	if !strings.HasPrefix(text, "\"") {
		return "", false
	}
	for i := 1; i < len(text); i++ {
		if text[i] != '"' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && text[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return text[:i+1], true
		}
	}
	return "", false
}

func isDispatchLogKVSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func a2aStatusUpdatePayload(taskID, contextID string, state a2a.TaskState, message string, metadata map[string]any) map[string]any {
	taskInfo := a2a.TaskInfo{TaskID: a2a.TaskID(taskID), ContextID: contextID}
	msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, taskInfo, a2a.NewTextPart(message))
	event := a2a.NewStatusUpdateEvent(taskInfo, state, msg)
	event.Metadata = cloneMetadataMap(metadata)

	encoded, err := json.Marshal(a2a.StreamResponse{Event: event})
	if err == nil {
		var envelope map[string]any
		if json.Unmarshal(encoded, &envelope) == nil {
			if statusUpdate, ok := envelope["statusUpdate"].(map[string]any); ok {
				return statusUpdate
			}
		}
	}
	return map[string]any{
		"taskId":    taskID,
		"contextId": contextID,
		"status": map[string]any{
			"state": state.String(),
		},
		"metadata": cloneMetadataMap(metadata),
	}
}

func dispatchStatusTaskID(dispatch SkillDispatch) string {
	return firstNonEmpty(dispatch.HubTaskID, dispatch.RequestID)
}

func dispatchStatusContextID(dispatch SkillDispatch) string {
	return firstNonEmpty(dispatch.ContextID, dispatch.RequestID)
}

func dispatchStatusMessageID(dispatch SkillDispatch, status string, details map[string]any) string {
	base := firstNonEmpty(dispatch.RequestID, dispatch.HubTaskID)
	status = strings.TrimSpace(status)
	if base == "" {
		base = "task"
	}
	if status == "" {
		status = "status"
	}
	parts := []string{base, "status", status}
	if stage := firstString(details["stage"]); stage != "" {
		parts = append(parts, sanitizeDispatchStatusMessageIDPart(stage))
	}
	if stageStatus := firstString(details["stage_status"], details["status"]); stageStatus != "" {
		parts = append(parts, sanitizeDispatchStatusMessageIDPart(stageStatus))
	}
	if len(details) > 0 {
		encoded, err := json.Marshal(details)
		if err == nil {
			hash := fnv.New32a()
			_, _ = hash.Write(encoded)
			parts = append(parts, fmt.Sprintf("%08x", hash.Sum32()))
		}
	}
	return strings.Join(parts, "-")
}

func sanitizeDispatchStatusMessageIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "status"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
		if out == "" {
			return "status"
		}
	}
	return out
}

func a2aTaskStateOK(state a2a.TaskState) bool {
	return state != a2a.TaskStateFailed &&
		state != a2a.TaskStateRejected &&
		state != a2a.TaskStateCanceled
}

func applyDispatchResponseRouting(payload map[string]any, dispatch SkillDispatch) {
	if payload == nil {
		return
	}
	if replyTo := strings.TrimSpace(dispatch.ReplyTo); replyTo != "" {
		payload["reply_to"] = replyTo
		payload["to"] = replyTo
	}
}

func applyDispatchOriginator(payload map[string]any, dispatch SkillDispatch) {
	if payload == nil {
		return
	}
	originator := dispatchOriginatorPayload(dispatch)
	if originator == nil {
		return
	}
	payload["originator"] = originator
	if id := firstString(originator["id"]); id != "" {
		payload["originator_id"] = id
	}
}

func dispatchOriginatorPayload(dispatch SkillDispatch) map[string]any {
	originatorID := firstNonEmpty(
		dispatch.Originator,
		dispatch.OriginatorAgentURI,
		dispatch.OriginatorAgentUUID,
		dispatch.OriginatorAgentID,
		dispatch.OriginatorHumanID,
	)
	originator := map[string]any{}
	if originatorID != "" {
		originator["id"] = originatorID
	}
	if agentURI := strings.TrimSpace(dispatch.OriginatorAgentURI); agentURI != "" {
		originator["agent_uri"] = agentURI
	}
	if agentUUID := strings.TrimSpace(dispatch.OriginatorAgentUUID); agentUUID != "" {
		originator["agent_uuid"] = agentUUID
	}
	if agentID := strings.TrimSpace(dispatch.OriginatorAgentID); agentID != "" {
		originator["agent_id"] = agentID
	}
	if humanID := strings.TrimSpace(dispatch.OriginatorHumanID); humanID != "" {
		originator["human_id"] = humanID
	}
	switch {
	case strings.TrimSpace(dispatch.OriginatorAgentURI) != "" ||
		strings.TrimSpace(dispatch.OriginatorAgentUUID) != "" ||
		strings.TrimSpace(dispatch.OriginatorAgentID) != "":
		originator["type"] = "agent"
	case strings.TrimSpace(dispatch.OriginatorHumanID) != "":
		originator["type"] = "human"
	}
	if len(originator) == 0 {
		return nil
	}
	return originator
}

func dispatchResultPayload(cfg InitConfig, dispatch SkillDispatch, res app.Result) map[string]any {
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
		errText := res.Err.Error()
		result["error"] = errText
		addExplicitFailureFields(result, errText)
	}
	if hubTaskID := strings.TrimSpace(dispatch.HubTaskID); hubTaskID != "" {
		result["hubTaskId"] = hubTaskID
		result["a2aTaskId"] = hubTaskID
	}
	if contextID := strings.TrimSpace(dispatch.ContextID); contextID != "" {
		result["contextId"] = contextID
		result["a2aContextId"] = contextID
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
	if hubTaskID := strings.TrimSpace(dispatch.HubTaskID); hubTaskID != "" {
		payload["hub_task_id"] = hubTaskID
		payload["a2a_task_id"] = hubTaskID
	}
	if contextID := strings.TrimSpace(dispatch.ContextID); contextID != "" {
		payload["context_id"] = contextID
		payload["a2a_context_id"] = contextID
	}
	if res.Err != nil {
		errText := res.Err.Error()
		payload["error"] = errText
		addExplicitFailureFields(payload, errText)
		failure := map[string]any{
			"status":  "failed",
			"message": message,
			"error":   errText,
			"details": result,
		}
		addExplicitFailureFields(failure, errText)
		payload["failure"] = failure
	}
	applyDispatchResponseRouting(payload, dispatch)
	applyDispatchOriginator(payload, dispatch)
	if originator := dispatchOriginatorPayload(dispatch); originator != nil {
		result["originator"] = originator
		if failure, ok := payload["failure"].(map[string]any); ok {
			failure["originator"] = originator
		}
	}
	return payload
}

func addExplicitFailureFields(payload map[string]any, errText string) {
	if payload == nil {
		return
	}
	errText = strings.TrimSpace(errText)
	if errText == "" {
		errText = "unknown error"
	}
	payload["Failure"] = "task failed"
	payload["Failure:"] = "task failed"
	payload["Error details"] = errText
	payload["Error details:"] = errText
}

func dispatchResultDetailStatus(status string, res app.Result) string {
	if res.Err != nil || status == "error" {
		return "failed"
	}
	return status
}

func dispatchResultMessage(status string, res app.Result) string {
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

func duplicateDispatchResultPayload(cfg InitConfig, dispatch SkillDispatch, state, duplicateOf string) map[string]any {
	state = strings.TrimSpace(state)
	duplicateOf = strings.TrimSpace(duplicateOf)

	payload := dispatchResultPayload(cfg, dispatch, app.Result{
		ExitCode: app.ExitPreflight,
		Err:      errors.New(duplicateDispatchErrorText(duplicateOf, state)),
	})
	payload["status"] = "duplicate"
	payload["duplicate"] = true
	if state != "" {
		payload["state"] = state
	}
	if duplicateOf != "" {
		payload["duplicate_of"] = duplicateOf
	}

	if result, ok := payload["result"].(map[string]any); ok {
		result["status"] = "duplicate"
		result["duplicate"] = true
		if state != "" {
			result["state"] = state
		}
		if duplicateOf != "" {
			result["duplicate_of"] = duplicateOf
		}
	}
	if failure, ok := payload["failure"].(map[string]any); ok {
		failure["duplicate"] = true
		if state != "" {
			failure["state"] = state
		}
		if duplicateOf != "" {
			failure["duplicate_of"] = duplicateOf
		}
		if details, ok := failure["details"].(map[string]any); ok {
			details["status"] = "duplicate"
			details["duplicate"] = true
			if state != "" {
				details["state"] = state
			}
			if duplicateOf != "" {
				details["duplicate_of"] = duplicateOf
			}
		}
	}

	return payload
}

func duplicateDispatchErrorText(duplicateOf, state string) string {
	duplicateOf = strings.TrimSpace(duplicateOf)
	state = strings.TrimSpace(state)
	if duplicateOf == "" && state == "" {
		return "duplicate submission ignored"
	}
	if duplicateOf == "" {
		return fmt.Sprintf("duplicate submission ignored (state=%s)", state)
	}
	if state == "" {
		return fmt.Sprintf("duplicate submission ignored (request_id=%s)", duplicateOf)
	}
	return fmt.Sprintf("duplicate submission ignored (request_id=%s state=%s)", duplicateOf, state)
}

func (d Daemon) handleFailedDispatchAfterPublish(
	ctx context.Context,
	api MoltenHubAPI,
	cfg InitConfig,
	dispatch SkillDispatch,
	res app.Result,
) {
	if api == nil {
		return
	}
	if err := api.MarkRuntimeOffline(ctx, cfg.SessionKey, transportOfflineReasonExecutionFailure); err != nil {
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

func shouldQueueFailureRerun(dispatch SkillDispatch, res app.Result) (bool, string) {
	if res.Err == nil {
		return false, "failed task did not include an error"
	}
	if isFailureFollowUpRequestID(dispatch.RequestID) {
		return false, "run is already a failure follow-up"
	}
	if isFailureRerunRequestID(dispatch.RequestID) {
		return false, "run is already a failure rerun"
	}
	return false, automaticFailureRerunDisabledReason
}

func shouldQueueFailureFollowUp(dispatch SkillDispatch, res app.Result) (bool, string) {
	if res.Err == nil {
		return false, "failed task did not include an error"
	}
	if isFailureFollowUpRequestID(dispatch.RequestID) {
		return false, "run is already a failure follow-up"
	}
	if reason := failurefollowup.NonRemediableFailureReason(res.Err); reason != "" {
		return false, "failure is not remediable by code changes: " + reason
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
	applyQueuedDispatchRouting(payload, dispatch)

	return api.PublishResult(ctx, payload)
}

func queueFailureFollowUp(ctx context.Context, api MoltenHubAPI, cfg InitConfig, dispatch SkillDispatch, res app.Result, taskLogRoot string) error {
	if api == nil {
		return fmt.Errorf("moltenhub api client is required")
	}
	repos := failureFollowUpRepos(res, dispatch.Config)
	if len(repos) == 0 {
		return fmt.Errorf("failed dispatch is missing repository context")
	}

	runConfig := map[string]any{
		"repos":        repos,
		"targetSubdir": failureFollowUpTargetSubdir,
		"prompt":       failureFollowUpPrompt(taskLogRoot, dispatch, res),
	}

	payload := map[string]any{
		"type":       firstNonEmpty(cfg.Skill.DispatchType, defaultRuntimeDispatchType),
		"skill":      firstNonEmpty(cfg.Skill.Name, dispatch.Skill),
		"request_id": failureFollowUpRequestID(dispatch.RequestID),
		"config":     runConfig,
	}
	applyQueuedDispatchRouting(payload, dispatch)

	return api.PublishResult(ctx, payload)
}

func applyQueuedDispatchRouting(payload map[string]any, dispatch SkillDispatch) {
	if payload == nil {
		return
	}
	routeTo := strings.TrimSpace(dispatch.RouteTo)
	if routeTo == "" {
		return
	}
	payload["to"] = routeTo
	if replyTo := strings.TrimSpace(dispatch.ReplyTo); replyTo != "" {
		payload["reply_to"] = replyTo
	}
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

func failureFollowUpRepos(_ app.Result, _ config.Config) []string {
	if repo := strings.TrimSpace(failurefollowup.FollowUpRepositoryURL); repo != "" {
		return []string{repo}
	}
	return nil
}

func failureFollowUpPrompt(logRoot string, dispatch SkillDispatch, res app.Result) string {
	paths := failureLogPaths(logRoot, dispatch.RequestID, dispatch.Config, res)
	return failurefollowup.ComposePrompt(
		failureFollowUpPromptBase,
		paths,
		nil,
		failureFollowUpNoPathGuidance,
		failureFollowUpContext(dispatch, res),
	)
}

func failureFollowUpContext(dispatch SkillDispatch, res app.Result) string {
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

func failureLogPaths(logRoot, requestID string, runCfg config.Config, res app.Result) []string {
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

func joinRepoPRURLs(results []app.RepoResult) string {
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

func joinAllRepoPRURLs(results []app.RepoResult) string {
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

func resultHasPR(result app.Result) bool {
	if strings.TrimSpace(result.PRURL) != "" {
		return true
	}
	return strings.TrimSpace(joinAllRepoPRURLs(result.RepoResults)) != ""
}

func completedPRURLs(result app.Result) string {
	if result.NoChanges {
		return joinAllRepoPRURLs(result.RepoResults)
	}
	return joinRepoPRURLs(result.RepoResults)
}

func countChangedRepoResults(results []app.RepoResult) int {
	count := 0
	for _, result := range results {
		if result.Changed {
			count++
		}
	}
	return count
}

func repoResultPayloads(results []app.RepoResult) []map[string]any {
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
	payload := dispatchResultPayload(cfg, dispatch, app.Result{
		ExitCode: app.ExitConfig,
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
