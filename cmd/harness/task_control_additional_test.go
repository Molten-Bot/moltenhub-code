package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

func TestLocalTaskControllerCompleteRemovesTask(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	_, cancel := context.WithCancelCause(context.Background())
	controller.Register("local-10", cancel)
	controller.Complete("local-10")

	if err := controller.Pause("local-10"); err == nil {
		t.Fatal("Pause(completed) error = nil, want not found")
	}
}

func TestLocalTaskHandlePauseRunAndStopErrorPaths(t *testing.T) {
	t.Parallel()

	handle := &localTaskHandle{}
	if err := handle.Run(); err == nil {
		t.Fatal("Run(not paused) error = nil, want non-nil")
	}

	if err := handle.Pause(); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := handle.Pause(); err == nil {
		t.Fatal("Pause(already paused) error = nil, want non-nil")
	}
	if err := handle.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := handle.Run(); err == nil {
		t.Fatal("Run(not paused) error = nil, want non-nil")
	}
	if !handle.Stop() {
		t.Fatal("Stop() = false, want true on first call")
	}
	if handle.Stop() {
		t.Fatal("Stop() = true, want false on second call")
	}
	if err := handle.Pause(); err == nil {
		t.Fatal("Pause(stopped) error = nil, want non-nil")
	}
	if err := handle.Run(); err == nil {
		t.Fatal("Run(stopped) error = nil, want non-nil")
	}
	if err := handle.ForceRun(); err == nil {
		t.Fatal("ForceRun(stopped) error = nil, want non-nil")
	}
}

func TestLocalTaskHandleForceRunUnpausesAndCancelsAcquire(t *testing.T) {
	t.Parallel()

	canceled := false
	handle := &localTaskHandle{
		paused:        true,
		pauseWait:     make(chan struct{}),
		acquireCancel: func() { canceled = true },
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- handle.WaitUntilRunnable(context.Background())
	}()

	if err := handle.ForceRun(); err != nil {
		t.Fatalf("ForceRun() error = %v", err)
	}
	if !canceled {
		t.Fatal("ForceRun() did not invoke acquire cancel function")
	}
	if !handle.HasForceAcquire() {
		t.Fatal("HasForceAcquire() = false, want true after ForceRun()")
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitUntilRunnable() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitUntilRunnable() did not unblock after ForceRun()")
	}
}

func TestLocalTaskHandleForceRunRejectsRunningTask(t *testing.T) {
	t.Parallel()

	handle := &localTaskHandle{running: true}
	if err := handle.ForceRun(); err == nil {
		t.Fatal("ForceRun(running) error = nil, want non-nil")
	}
}

func TestSetAcquireCancelAndClearAcquireCancel(t *testing.T) {
	t.Parallel()

	var canceled bool
	cancelFn := func() { canceled = true }

	var nilHandle *localTaskHandle
	nilHandle.SetAcquireCancel(cancelFn)
	if !canceled {
		t.Fatal("SetAcquireCancel(nil handle) did not invoke cancel")
	}

	handle := &localTaskHandle{}
	handle.SetAcquireCancel(cancelFn)
	handle.ClearAcquireCancel(nil)
	handle.mu.Lock()
	if handle.acquireCancel == nil {
		t.Fatal("ClearAcquireCancel(nil) cleared cancel function unexpectedly")
	}
	handle.mu.Unlock()

	handle.ClearAcquireCancel(cancelFn)
	handle.mu.Lock()
	if handle.acquireCancel != nil {
		t.Fatal("ClearAcquireCancel(non-nil) did not clear acquire cancel")
	}
	handle.mu.Unlock()

	handle.stopped = true
	canceled = false
	handle.SetAcquireCancel(cancelFn)
	if !canceled {
		t.Fatal("SetAcquireCancel(stopped handle) did not invoke cancel")
	}
}

func TestWaitUntilRunnableContextCancelAndStoppedHandle(t *testing.T) {
	t.Parallel()

	handle := &localTaskHandle{
		paused:    true,
		pauseWait: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := handle.WaitUntilRunnable(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitUntilRunnable(timeout) error = %v, want deadline exceeded", err)
	}

	handle.Stop()
	if err := handle.WaitUntilRunnable(context.Background()); !errors.Is(err, errTaskStoppedByOperator) {
		t.Fatalf("WaitUntilRunnable(stopped) error = %v, want %v", err, errTaskStoppedByOperator)
	}
}

func TestLocalTaskControllerControlsReflectHandleState(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	_, cancel := context.WithCancelCause(context.Background())
	handle := controller.Register("local-controls", cancel)
	if handle == nil {
		t.Fatal("Register() returned nil handle")
	}

	assertControls := func(label string, got, want hubui.TaskControls) {
		t.Helper()
		if got != want {
			t.Fatalf("%s controls = %#v, want %#v", label, got, want)
		}
	}

	assertControls("pending", controller.Controls("local-controls"), hubui.TaskControls{
		Pause:    true,
		ForceRun: true,
		Stop:     true,
	})

	if err := controller.Pause("local-controls"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	assertControls("paused", controller.Controls("local-controls"), hubui.TaskControls{
		Run:  true,
		Stop: true,
	})
	if err := controller.Run("local-controls"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	handle.SetRunning(true)
	assertControls("running", controller.Controls("local-controls"), hubui.TaskControls{
		Pause: true,
		Stop:  true,
	})

	if err := controller.Pause("local-controls"); err != nil {
		t.Fatalf("Pause(running) error = %v", err)
	}
	assertControls("running paused", controller.Controls("local-controls"), hubui.TaskControls{
		Run:  true,
		Stop: true,
	})

	if !handle.Stop() {
		t.Fatal("Stop() = false, want true")
	}
	assertControls("stopped", controller.Controls("local-controls"), hubui.TaskControls{})
	assertControls("missing", controller.Controls("missing"), hubui.TaskControls{})
}
