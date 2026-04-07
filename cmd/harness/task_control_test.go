package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/hubui"
)

func TestTaskControlPauseRunLifecycle(t *testing.T) {
	t.Parallel()

	controls := newTaskControlRegistry(nil)
	controls.register("local-1")

	runCtx, cleanup := controls.bindContext(context.Background(), "local-1")
	defer cleanup()

	if err := controls.pause("local-1"); err != nil {
		t.Fatalf("pause() error = %v", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- controls.waitUntilRunnable(runCtx, "local-1")
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("waitUntilRunnable() returned early with err=%v", err)
	case <-time.After(40 * time.Millisecond):
		// Expected while paused.
	}

	if err := controls.run("local-1"); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("waitUntilRunnable() err = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitUntilRunnable() did not unblock after run()")
	}

	controls.complete("local-1")
	if err := controls.pause("local-1"); !errors.Is(err, errTaskControlCompleted) {
		t.Fatalf("pause() after complete error = %v, want %v", err, errTaskControlCompleted)
	}
}

func TestTaskControlStopUnblocksWaiters(t *testing.T) {
	t.Parallel()

	controls := newTaskControlRegistry(nil)
	controls.register("local-2")

	runCtx, cleanup := controls.bindContext(context.Background(), "local-2")
	defer cleanup()

	if err := controls.pause("local-2"); err != nil {
		t.Fatalf("pause() error = %v", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- controls.waitUntilRunnable(runCtx, "local-2")
	}()

	if err := controls.stop("local-2"); err != nil {
		t.Fatalf("stop() error = %v", err)
	}
	if !controls.isStopRequested("local-2") {
		t.Fatal("isStopRequested() = false, want true")
	}

	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, errTaskControlStopped) {
			t.Fatalf("waitUntilRunnable() err = %v, want context.Canceled or %v", err, errTaskControlStopped)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitUntilRunnable() did not unblock after stop()")
	}
}

func TestMapTaskControlError(t *testing.T) {
	t.Parallel()

	if got := mapTaskControlError(errTaskControlNotFound); !errors.Is(got, hubui.ErrTaskNotFound) {
		t.Fatalf("mapTaskControlError(not found) = %v, want wrapping %v", got, hubui.ErrTaskNotFound)
	}
	if got := mapTaskControlError(errTaskControlAlreadyPaused); !errors.Is(got, hubui.ErrTaskActionConflict) {
		t.Fatalf("mapTaskControlError(conflict) = %v, want wrapping %v", got, hubui.ErrTaskActionConflict)
	}
}
