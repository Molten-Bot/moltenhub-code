package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

func TestLocalTaskControllerPauseAndRun(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	handle := controller.Register("local-1", cancel)
	if handle == nil {
		t.Fatal("Register() returned nil handle")
	}

	if err := controller.Pause("local-1"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if !handle.IsPaused() {
		t.Fatal("handle.IsPaused() = false, want true after Pause()")
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- handle.WaitUntilRunnable(waitCtx)
	}()

	time.Sleep(20 * time.Millisecond)
	if err := controller.Run("local-1"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitUntilRunnable() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitUntilRunnable() did not unblock after Run()")
	}
}

func TestLocalTaskControllerForceRunQueuedTask(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	handle := controller.Register("local-force", cancel)
	if handle == nil {
		t.Fatal("Register() returned nil handle")
	}

	if err := controller.ForceRun("local-force"); err != nil {
		t.Fatalf("ForceRun() error = %v", err)
	}
	if !handle.HasForceAcquire() {
		t.Fatal("HasForceAcquire() = false, want true after ForceRun()")
	}
	if !handle.ConsumeForceAcquire() {
		t.Fatal("ConsumeForceAcquire() = false, want true on first consume")
	}
	if handle.ConsumeForceAcquire() {
		t.Fatal("ConsumeForceAcquire() = true, want false after consume")
	}
}

func TestLocalTaskControllerStopCancelsContextWithStopCause(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	ctx, cancel := context.WithCancelCause(context.Background())
	handle := controller.Register("local-2", cancel)
	if handle == nil {
		t.Fatal("Register() returned nil handle")
	}

	if err := controller.Stop("local-2"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !handle.IsStopped() {
		t.Fatal("handle.IsStopped() = false, want true")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, errTaskStoppedByOperator) {
		t.Fatalf("context.Cause() = %v, want %v", cause, errTaskStoppedByOperator)
	}
}

func TestLocalTaskControllerPauseRunningTaskDefersUntilNextRunnableWait(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	_, cancel := context.WithCancelCause(context.Background())
	handle := controller.Register("local-3", cancel)
	if handle == nil {
		t.Fatal("Register() returned nil handle")
	}
	handle.SetRunning(true)

	if err := controller.Pause("local-3"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if !handle.IsPaused() {
		t.Fatal("handle.IsPaused() = false, want true after Pause()")
	}

	handle.SetRunning(false)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer waitCancel()
	if err := handle.WaitUntilRunnable(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitUntilRunnable(paused) error = %v, want deadline exceeded", err)
	}
}

func TestLocalTaskControllerMissingTaskReturnsNotFound(t *testing.T) {
	t.Parallel()

	controller := newLocalTaskController()
	for _, action := range []func(string) error{controller.Pause, controller.Run, controller.ForceRun, controller.Stop} {
		if err := action("missing"); !errors.Is(err, web.ErrTaskNotFound) {
			t.Fatalf("action(missing) error = %v, want %v", err, web.ErrTaskNotFound)
		}
	}
}
