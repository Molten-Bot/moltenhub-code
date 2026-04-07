package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jef/moltenhub-code/internal/execx"
)

var (
	errTaskControlNotFound       = errors.New("task not found")
	errTaskControlCompleted      = errors.New("task is already completed")
	errTaskControlAlreadyPaused  = errors.New("task is already paused")
	errTaskControlAlreadyRunning = errors.New("task is already running")
	errTaskControlStopRequested  = errors.New("task stop already requested")
	errTaskControlStopped        = errors.New("task stopped")
)

type taskControlRegistry struct {
	mu    sync.Mutex
	tasks map[string]*taskControlState
	logf  func(string, ...any)
}

type taskControlState struct {
	requestID     string
	runGate       chan struct{}
	cancel        context.CancelFunc
	stopRequested bool
	completed     bool
	process       *os.Process
	processPaused bool
}

type taskProcessObserver struct {
	registry  *taskControlRegistry
	requestID string
}

func newTaskControlRegistry(logf func(string, ...any)) *taskControlRegistry {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &taskControlRegistry{
		tasks: map[string]*taskControlState{},
		logf:  logf,
	}
}

func (r *taskControlRegistry) register(requestID string) {
	if r == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.ensureTaskLocked(requestID)
}

func (r *taskControlRegistry) bindContext(parent context.Context, requestID string) (context.Context, func()) {
	requestID = strings.TrimSpace(requestID)
	ctx, cancel := context.WithCancel(parent)
	if r == nil || requestID == "" {
		return ctx, cancel
	}

	r.mu.Lock()
	task := r.ensureTaskLocked(requestID)
	task.cancel = cancel
	r.mu.Unlock()

	observer := &taskProcessObserver{registry: r, requestID: requestID}
	ctx = execx.WithProcessObserver(ctx, observer)

	cleanup := func() {
		cancel()
		r.mu.Lock()
		defer r.mu.Unlock()
		task := r.tasks[requestID]
		if task == nil {
			return
		}
		task.cancel = nil
		task.process = nil
		task.processPaused = false
	}
	return ctx, cleanup
}

func (r *taskControlRegistry) waitUntilRunnable(ctx context.Context, requestID string) error {
	if r == nil {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errTaskControlNotFound
	}

	for {
		r.mu.Lock()
		task, ok := r.tasks[requestID]
		if !ok {
			r.mu.Unlock()
			return errTaskControlNotFound
		}
		if task.completed {
			r.mu.Unlock()
			return errTaskControlCompleted
		}
		if task.stopRequested {
			r.mu.Unlock()
			return errTaskControlStopped
		}
		gate := task.runGate
		r.mu.Unlock()

		if gate == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
	}
}

func (r *taskControlRegistry) pause(requestID string) error {
	if r == nil {
		return errTaskControlNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errTaskControlNotFound
	}

	r.mu.Lock()
	task, ok := r.tasks[requestID]
	if !ok {
		r.mu.Unlock()
		return errTaskControlNotFound
	}
	if task.completed {
		r.mu.Unlock()
		return errTaskControlCompleted
	}
	if task.stopRequested {
		r.mu.Unlock()
		return errTaskControlStopRequested
	}
	if task.runGate != nil {
		r.mu.Unlock()
		return errTaskControlAlreadyPaused
	}

	task.runGate = make(chan struct{})
	proc := task.process
	r.mu.Unlock()

	if proc != nil {
		if err := pauseProcess(proc); err != nil {
			r.mu.Lock()
			task := r.tasks[requestID]
			if task != nil && task.runGate != nil {
				close(task.runGate)
				task.runGate = nil
			}
			r.mu.Unlock()
			return fmt.Errorf("pause task process: %w", err)
		}

		r.mu.Lock()
		task := r.tasks[requestID]
		if task != nil && task.process == proc {
			task.processPaused = true
		}
		r.mu.Unlock()
	}

	r.logf("dispatch status=paused request_id=%s action=user_pause", requestID)
	return nil
}

func (r *taskControlRegistry) run(requestID string) error {
	if r == nil {
		return errTaskControlNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errTaskControlNotFound
	}

	r.mu.Lock()
	task, ok := r.tasks[requestID]
	if !ok {
		r.mu.Unlock()
		return errTaskControlNotFound
	}
	if task.completed {
		r.mu.Unlock()
		return errTaskControlCompleted
	}
	if task.stopRequested {
		r.mu.Unlock()
		return errTaskControlStopRequested
	}
	if task.runGate == nil {
		r.mu.Unlock()
		return errTaskControlAlreadyRunning
	}

	gate := task.runGate
	task.runGate = nil
	proc := task.process
	resumeProcessFirst := task.processPaused && proc != nil
	task.processPaused = false
	r.mu.Unlock()

	if resumeProcessFirst {
		if err := resumeProcess(proc); err != nil {
			r.mu.Lock()
			task := r.tasks[requestID]
			if task != nil && !task.completed && !task.stopRequested && task.runGate == nil {
				task.runGate = gate
				task.processPaused = true
			}
			r.mu.Unlock()
			return fmt.Errorf("resume task process: %w", err)
		}
	}

	close(gate)
	r.logf("dispatch status=running request_id=%s action=user_run", requestID)
	return nil
}

func (r *taskControlRegistry) stop(requestID string) error {
	if r == nil {
		return errTaskControlNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errTaskControlNotFound
	}

	r.mu.Lock()
	task, ok := r.tasks[requestID]
	if !ok {
		r.mu.Unlock()
		return errTaskControlNotFound
	}
	if task.completed {
		r.mu.Unlock()
		return errTaskControlCompleted
	}
	if task.stopRequested {
		r.mu.Unlock()
		return errTaskControlStopRequested
	}

	task.stopRequested = true
	gate := task.runGate
	task.runGate = nil
	cancel := task.cancel
	proc := task.process
	task.processPaused = false
	r.mu.Unlock()

	if gate != nil {
		close(gate)
	}
	if cancel != nil {
		cancel()
	}
	if proc != nil {
		if err := killProcess(proc); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill task process: %w", err)
		}
	}

	r.logf("dispatch status=stopped request_id=%s action=user_stop", requestID)
	return nil
}

func (r *taskControlRegistry) complete(requestID string) {
	if r == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	task := r.tasks[requestID]
	if task == nil {
		return
	}
	if task.runGate != nil {
		close(task.runGate)
		task.runGate = nil
	}
	task.cancel = nil
	task.process = nil
	task.processPaused = false
	task.completed = true
}

func (r *taskControlRegistry) isStopRequested(requestID string) bool {
	if r == nil {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	task := r.tasks[requestID]
	return task != nil && task.stopRequested
}

func (r *taskControlRegistry) onProcessStart(requestID string, process *os.Process) {
	if r == nil || process == nil {
		return
	}

	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	r.mu.Lock()
	task := r.tasks[requestID]
	if task == nil {
		r.mu.Unlock()
		return
	}
	task.process = process
	shouldStop := task.stopRequested
	shouldPause := task.runGate != nil && !shouldStop
	r.mu.Unlock()

	if shouldStop {
		_ = killProcess(process)
		return
	}
	if !shouldPause {
		return
	}
	if err := pauseProcess(process); err != nil {
		r.logf("dispatch status=warn request_id=%s action=user_pause err=%q", requestID, err)
		return
	}

	r.mu.Lock()
	task = r.tasks[requestID]
	if task != nil && task.process == process {
		task.processPaused = true
	}
	r.mu.Unlock()
}

func (r *taskControlRegistry) onProcessExit(requestID string) {
	if r == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	task := r.tasks[requestID]
	if task == nil {
		return
	}
	task.process = nil
	task.processPaused = false
}

func (r *taskControlRegistry) ensureTaskLocked(requestID string) *taskControlState {
	if task, ok := r.tasks[requestID]; ok {
		return task
	}
	task := &taskControlState{requestID: requestID}
	r.tasks[requestID] = task
	return task
}

func (o *taskProcessObserver) OnProcessStart(process *os.Process) {
	if o == nil || o.registry == nil {
		return
	}
	o.registry.onProcessStart(o.requestID, process)
}

func (o *taskProcessObserver) OnProcessExit() {
	if o == nil || o.registry == nil {
		return
	}
	o.registry.onProcessExit(o.requestID)
}
