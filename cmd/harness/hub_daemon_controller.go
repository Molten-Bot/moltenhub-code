package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/hub"
)

const hubDaemonStartGracePeriod = 1500 * time.Millisecond

type hubDaemonController struct {
	parentCtx          context.Context
	runner             execx.Runner
	logf               func(string, ...any)
	dispatchController *hub.AdaptiveDispatchController
	taskLogRoot        string
	onDispatchQueued   func(string, config.Config)
	onDispatchFailed   func(string, config.Config, harness.Result)
	startGracePeriod   time.Duration

	updateMu sync.Mutex
	stateMu  sync.Mutex
	cancel   context.CancelFunc
	done     chan error
	errs     chan error
}

func newHubDaemonController(parentCtx context.Context, runner execx.Runner) *hubDaemonController {
	return &hubDaemonController{
		parentCtx:        parentCtx,
		runner:           runner,
		logf:             func(string, ...any) {},
		startGracePeriod: hubDaemonStartGracePeriod,
		errs:             make(chan error, 8),
	}
}

func (c *hubDaemonController) Errors() <-chan error {
	if c == nil {
		return nil
	}
	return c.errs
}

func (c *hubDaemonController) Update(ctx context.Context, cfg hub.InitConfig) error {
	if c == nil {
		return fmt.Errorf("hub daemon controller is required")
	}
	if c.parentCtx == nil {
		return fmt.Errorf("hub daemon controller parent context is required")
	}
	if c.runner == nil {
		c.runner = execx.OSRunner{}
	}
	if c.logf == nil {
		c.logf = func(string, ...any) {}
	}
	if c.startGracePeriod <= 0 {
		c.startGracePeriod = hubDaemonStartGracePeriod
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("init config: %w", err)
	}

	c.updateMu.Lock()
	defer c.updateMu.Unlock()

	prevCancel, prevDone := c.activeRun()
	if prevCancel != nil {
		prevCancel()
		if prevDone != nil {
			<-prevDone
		}
	}

	if err := c.parentCtx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(c.parentCtx)
	done := make(chan error, 1)
	c.setActiveRun(cancel, done)
	go c.run(runCtx, cfg, done)

	timer := time.NewTimer(c.startGracePeriod)
	defer timer.Stop()

	select {
	case err := <-done:
		c.clearActiveRun(done)
		if err == nil {
			return fmt.Errorf("hub transport exited unexpectedly")
		}
		return err
	case <-timer.C:
		return nil
	case <-ctx.Done():
		cancel()
		<-done
		c.clearActiveRun(done)
		return ctx.Err()
	}
}

func (c *hubDaemonController) Stop(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("hub daemon controller is required")
	}

	c.updateMu.Lock()
	defer c.updateMu.Unlock()

	prevCancel, prevDone := c.activeRun()
	if prevCancel == nil {
		return nil
	}

	prevCancel()
	if prevDone == nil {
		return nil
	}

	select {
	case err := <-prevDone:
		c.clearActiveRun(prevDone)
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *hubDaemonController) run(runCtx context.Context, cfg hub.InitConfig, done chan error) {
	daemon := hub.NewDaemon(c.runner)
	daemon.Logf = c.logf
	daemon.DispatchController = c.dispatchController
	daemon.TaskLogRoot = c.taskLogRoot
	daemon.OnDispatchQueued = c.onDispatchQueued
	daemon.OnDispatchFailed = c.onDispatchFailed

	err := daemon.Run(runCtx, cfg)
	if runCtx.Err() != nil || errors.Is(err, context.Canceled) {
		err = nil
	}

	done <- err
	close(done)
	c.clearActiveRun(done)

	if err != nil {
		select {
		case c.errs <- err:
		default:
			c.logf("hub.runtime status=warn err=%q", err)
		}
	}
}

func (c *hubDaemonController) activeRun() (context.CancelFunc, chan error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.cancel, c.done
}

func (c *hubDaemonController) setActiveRun(cancel context.CancelFunc, done chan error) {
	c.stateMu.Lock()
	c.cancel = cancel
	c.done = done
	c.stateMu.Unlock()
}

func (c *hubDaemonController) clearActiveRun(done chan error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if done != nil && c.done != done {
		return
	}
	c.cancel = nil
	c.done = nil
}
