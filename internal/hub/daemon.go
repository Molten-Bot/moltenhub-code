package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jef/how-i-want-to-code/internal/execx"
	"github.com/jef/how-i-want-to-code/internal/harness"
)

// Daemon listens for hub skill dispatches and runs harness jobs.
type Daemon struct {
	Runner         execx.Runner
	Logf           func(string, ...any)
	ReconnectDelay time.Duration
}

// NewDaemon returns a hub daemon with defaults.
func NewDaemon(runner execx.Runner) Daemon {
	return Daemon{
		Runner:         runner,
		Logf:           func(string, ...any) {},
		ReconnectDelay: 3 * time.Second,
	}
}

// Run binds/auths, syncs profile, then consumes pull transport and dispatches skill runs.
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

	d.logf("hub.transport mode=openclaw_pull")

	workerSem := make(chan struct{}, cfg.Dispatcher.MaxParallel)
	var workers sync.WaitGroup
	defer workers.Wait()

	for {
		if ctx.Err() != nil {
			return nil
		}

		pulled, found, err := api.PullOpenClawMessage(ctx, token, runtimeTimeoutMs)
		if err != nil {
			d.logf("hub.pull status=error err=%q", err)
			if !sleepWithContext(ctx, d.ReconnectDelay) {
				return nil
			}
			continue
		}
		if !found {
			continue
		}

		msg := pulled.Message
		dispatch, matched, parseErr := ParseSkillDispatch(msg, cfg.Skill.DispatchType, cfg.Skill.Name)
		if !matched {
			d.logf("dispatch status=ignored skill=%s", incomingSkillName(msg))
			if err := api.AckOpenClawDelivery(ctx, token, pulled.DeliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", pulled.DeliveryID, err)
			}
			continue
		}
		if parseErr != nil {
			d.logf("dispatch status=invalid request_id=%s err=%q", dispatch.RequestID, parseErr)
			payload := dispatchParseErrorPayload(cfg, dispatch, parseErr)
			if err := api.PublishResult(ctx, token, payload); err != nil {
				d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
				if nackErr := api.NackOpenClawDelivery(ctx, token, pulled.DeliveryID); nackErr != nil {
					d.logf("dispatch status=nack_error delivery_id=%s err=%q", pulled.DeliveryID, nackErr)
				}
				continue
			}
			if err := api.AckOpenClawDelivery(ctx, token, pulled.DeliveryID); err != nil {
				d.logf("dispatch status=ack_error delivery_id=%s err=%q", pulled.DeliveryID, err)
			}
			continue
		}

		select {
		case workerSem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}

		workers.Add(1)
		go func(dispatch SkillDispatch, deliveryID string) {
			defer workers.Done()
			defer func() { <-workerSem }()
			d.handleDispatch(ctx, api, token, cfg, dispatch, deliveryID)
		}(dispatch, pulled.DeliveryID)
	}
}

func (d Daemon) handleDispatch(
	ctx context.Context,
	api APIClient,
	token string,
	cfg InitConfig,
	dispatch SkillDispatch,
	deliveryID string,
) {
	d.logf("dispatch status=start request_id=%s skill=%s repo=%s", dispatch.RequestID, dispatch.Skill, dispatch.Config.RepoURL)

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
		if nackErr := api.NackOpenClawDelivery(ctx, token, deliveryID); nackErr != nil {
			d.logf("dispatch status=nack_error delivery_id=%s err=%q", deliveryID, nackErr)
		}
		return
	}
	if err := api.AckOpenClawDelivery(ctx, token, deliveryID); err != nil {
		d.logf("dispatch status=ack_error delivery_id=%s err=%q", deliveryID, err)
	}

	if res.Err != nil {
		d.logf("dispatch status=error request_id=%s exit_code=%d err=%q", dispatch.RequestID, res.ExitCode, res.Err)
		return
	}
	if res.NoChanges {
		d.logf("dispatch status=no_changes request_id=%s workspace=%s branch=%s", dispatch.RequestID, res.WorkspaceDir, res.Branch)
		return
	}
	d.logf("dispatch status=ok request_id=%s workspace=%s branch=%s pr_url=%s", dispatch.RequestID, res.WorkspaceDir, res.Branch, res.PRURL)
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
