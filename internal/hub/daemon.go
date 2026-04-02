package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Run binds/auths, syncs profile, connects websocket, and dispatches skill runs.
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
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("init config: %w", err)
	}

	api := NewAPIClient(cfg.BaseURL)
	api.Logf = d.logf

	token, err := api.ResolveAgentToken(ctx, cfg)
	if err != nil {
		return fmt.Errorf("hub auth: %w", err)
	}
	d.logf("hub.auth status=ok")

	if err := api.SyncProfile(ctx, token, cfg); err != nil {
		return fmt.Errorf("hub profile: %w", err)
	}
	d.logf("hub.profile status=ok")

	if err := api.RegisterRuntime(ctx, token, cfg); err != nil {
		d.logf("hub.register status=warn err=%q", err)
	} else {
		d.logf("hub.register status=ok")
	}

	wsURL, err := WebsocketURL(cfg.BaseURL, cfg.SessionKey)
	if err != nil {
		return fmt.Errorf("hub websocket url: %w", err)
	}

	workerSem := make(chan struct{}, cfg.Dispatcher.MaxParallel)
	var workers sync.WaitGroup
	defer workers.Wait()

	for {
		if ctx.Err() != nil {
			return nil
		}

		ws, err := DialWebsocket(ctx, wsURL, token)
		if err != nil {
			d.logf("hub.ws status=connect_error err=%q", err)
			if !sleepWithContext(ctx, d.ReconnectDelay) {
				return nil
			}
			continue
		}
		d.logf("hub.ws status=connected")

		readErr := d.readLoop(ctx, ws, api, token, cfg, workerSem, &workers)
		_ = ws.Close()

		if ctx.Err() != nil {
			return nil
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			d.logf("hub.ws status=disconnected err=%q", readErr)
		} else {
			d.logf("hub.ws status=disconnected")
		}
		if !sleepWithContext(ctx, d.ReconnectDelay) {
			return nil
		}
	}
}

func (d Daemon) readLoop(
	ctx context.Context,
	ws *WSClient,
	api APIClient,
	token string,
	cfg InitConfig,
	workerSem chan struct{},
	workers *sync.WaitGroup,
) error {
	for {
		var msg map[string]any
		if err := ws.ReadJSON(&msg); err != nil {
			return err
		}

		dispatch, matched, parseErr := ParseSkillDispatch(msg, cfg.Skill.DispatchType, cfg.Skill.Name)
		if !matched {
			continue
		}
		if parseErr != nil {
			d.logf("dispatch status=invalid request_id=%s err=%q", dispatch.RequestID, parseErr)
			payload := dispatchResultPayload(cfg, dispatch, harness.Result{
				ExitCode: harness.ExitConfig,
				Err:      fmt.Errorf("dispatch parse: %w", parseErr),
			})
			if err := d.publishResult(ctx, ws, api, token, payload); err != nil {
				d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
			}
			continue
		}

		select {
		case workerSem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}

		workers.Add(1)
		go func(dispatch SkillDispatch) {
			defer workers.Done()
			defer func() { <-workerSem }()
			d.handleDispatch(ctx, ws, api, token, cfg, dispatch)
		}(dispatch)
	}
}

func (d Daemon) handleDispatch(
	ctx context.Context,
	ws *WSClient,
	api APIClient,
	token string,
	cfg InitConfig,
	dispatch SkillDispatch,
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
	if err := d.publishResult(ctx, ws, api, token, payload); err != nil {
		d.logf("dispatch status=publish_error request_id=%s err=%q", dispatch.RequestID, err)
		return
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

func (d Daemon) publishResult(ctx context.Context, ws *WSClient, api APIClient, token string, payload map[string]any) error {
	if err := ws.WriteJSON(payload); err == nil {
		return nil
	}
	return api.PublishResult(ctx, token, payload)
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
	d.Logf(format, args...)
}
