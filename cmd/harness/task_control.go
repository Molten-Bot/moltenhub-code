package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jef/moltenhub-code/internal/hubui"
)

var errTaskStoppedByOperator = errors.New("task was stopped by operator")

type localTaskController struct {
	mu    sync.RWMutex
	tasks map[string]*localTaskHandle
}

type localTaskHandle struct {
	cancel context.CancelCauseFunc

	mu            sync.Mutex
	paused        bool
	running       bool
	stopped       bool
	forceAcquire  bool
	pauseWait     chan struct{}
	acquireCancel context.CancelFunc
}

func newLocalTaskController() *localTaskController {
	return &localTaskController{
		tasks: map[string]*localTaskHandle{},
	}
}

func (c *localTaskController) Register(requestID string, cancel context.CancelCauseFunc) *localTaskHandle {
	if c == nil {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}

	handle := &localTaskHandle{cancel: cancel}
	c.mu.Lock()
	c.tasks[requestID] = handle
	c.mu.Unlock()
	return handle
}

func (c *localTaskController) Complete(requestID string) {
	if c == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	c.mu.Lock()
	delete(c.tasks, requestID)
	c.mu.Unlock()
}

func (c *localTaskController) handleFor(requestID string) (*localTaskHandle, error) {
	if c == nil {
		return nil, hubui.ErrTaskNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, hubui.ErrTaskNotFound
	}

	c.mu.RLock()
	handle, ok := c.tasks[requestID]
	c.mu.RUnlock()
	if !ok || handle == nil {
		return nil, hubui.ErrTaskNotFound
	}
	return handle, nil
}

func (c *localTaskController) Pause(requestID string) error {
	handle, err := c.handleFor(requestID)
	if err != nil {
		return err
	}
	return handle.Pause()
}

func (c *localTaskController) Run(requestID string) error {
	handle, err := c.handleFor(requestID)
	if err != nil {
		return err
	}
	return handle.Run()
}

func (c *localTaskController) Stop(requestID string) error {
	handle, err := c.handleFor(requestID)
	if err != nil {
		return err
	}
	if stopped := handle.Stop(); !stopped {
		return fmt.Errorf("task is already stopped")
	}
	return nil
}

func (c *localTaskController) ForceRun(requestID string) error {
	handle, err := c.handleFor(requestID)
	if err != nil {
		return err
	}
	return handle.ForceRun()
}

func (h *localTaskHandle) Pause() error {
	if h == nil {
		return hubui.ErrTaskNotFound
	}

	var acquireCancel context.CancelFunc
	h.mu.Lock()
	switch {
	case h.stopped:
		h.mu.Unlock()
		return fmt.Errorf("task is already stopped")
	case h.running:
		h.mu.Unlock()
		return fmt.Errorf("task is already running; use stop to kill it")
	case h.paused:
		h.mu.Unlock()
		return fmt.Errorf("task is already paused")
	default:
		h.paused = true
		h.pauseWait = make(chan struct{})
		acquireCancel = h.acquireCancel
		h.mu.Unlock()
	}
	if acquireCancel != nil {
		acquireCancel()
	}
	return nil
}

func (h *localTaskHandle) Run() error {
	if h == nil {
		return hubui.ErrTaskNotFound
	}

	var pauseWait chan struct{}
	h.mu.Lock()
	switch {
	case h.stopped:
		h.mu.Unlock()
		return fmt.Errorf("task is already stopped")
	case !h.paused:
		h.mu.Unlock()
		return fmt.Errorf("task is not paused")
	default:
		h.paused = false
		pauseWait = h.pauseWait
		h.pauseWait = nil
		h.mu.Unlock()
	}

	if pauseWait != nil {
		close(pauseWait)
	}
	return nil
}

func (h *localTaskHandle) ForceRun() error {
	if h == nil {
		return hubui.ErrTaskNotFound
	}

	var (
		pauseWait     chan struct{}
		acquireCancel context.CancelFunc
	)
	h.mu.Lock()
	switch {
	case h.stopped:
		h.mu.Unlock()
		return fmt.Errorf("task is already stopped")
	case h.running:
		h.mu.Unlock()
		return fmt.Errorf("task is already running")
	default:
		h.forceAcquire = true
		if h.paused {
			h.paused = false
			pauseWait = h.pauseWait
			h.pauseWait = nil
		}
		acquireCancel = h.acquireCancel
		h.mu.Unlock()
	}

	if pauseWait != nil {
		close(pauseWait)
	}
	if acquireCancel != nil {
		acquireCancel()
	}
	return nil
}

func (h *localTaskHandle) Stop() bool {
	if h == nil {
		return false
	}

	var (
		pauseWait     chan struct{}
		acquireCancel context.CancelFunc
		cancel        context.CancelCauseFunc
	)
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return false
	}
	h.stopped = true
	h.paused = false
	h.forceAcquire = false
	pauseWait = h.pauseWait
	h.pauseWait = nil
	acquireCancel = h.acquireCancel
	h.acquireCancel = nil
	cancel = h.cancel
	h.mu.Unlock()

	if pauseWait != nil {
		close(pauseWait)
	}
	if acquireCancel != nil {
		acquireCancel()
	}
	if cancel != nil {
		cancel(errTaskStoppedByOperator)
	}
	return true
}

func (h *localTaskHandle) SetAcquireCancel(cancel context.CancelFunc) {
	if h == nil {
		if cancel != nil {
			cancel()
		}
		return
	}

	shouldCancel := false
	h.mu.Lock()
	if h.stopped {
		shouldCancel = true
	} else {
		h.acquireCancel = cancel
	}
	h.mu.Unlock()
	if shouldCancel && cancel != nil {
		cancel()
	}
}

func (h *localTaskHandle) ClearAcquireCancel(cancel context.CancelFunc) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if cancel != nil {
		h.acquireCancel = nil
	}
	h.mu.Unlock()
}

func (h *localTaskHandle) SetRunning(running bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.running = running
	if running {
		h.forceAcquire = false
	}
	h.mu.Unlock()
}

func (h *localTaskHandle) WaitUntilRunnable(ctx context.Context) error {
	if h == nil {
		return hubui.ErrTaskNotFound
	}

	for {
		h.mu.Lock()
		if h.stopped {
			h.mu.Unlock()
			return errTaskStoppedByOperator
		}
		if !h.paused {
			h.mu.Unlock()
			return nil
		}
		waitCh := h.pauseWait
		if waitCh == nil {
			waitCh = make(chan struct{})
			h.pauseWait = waitCh
		}
		h.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitCh:
		}
	}
}

func (h *localTaskHandle) IsPaused() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.paused
}

func (h *localTaskHandle) IsStopped() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

func (h *localTaskHandle) ConsumeForceAcquire() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	force := h.forceAcquire
	h.forceAcquire = false
	return force
}

func (h *localTaskHandle) HasForceAcquire() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.forceAcquire
}
