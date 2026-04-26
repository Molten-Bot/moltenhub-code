package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func validHubControllerInitConfig() hub.InitConfig {
	cfg := hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "codex",
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestHubDaemonControllerErrorsChannel(t *testing.T) {
	t.Parallel()

	var nilController *hubDaemonController
	if got := nilController.Errors(); got != nil {
		t.Fatalf("nil controller Errors() = %v, want nil", got)
	}

	controller := newHubDaemonController(context.Background(), nil)
	if got := controller.Errors(); got == nil {
		t.Fatal("new controller Errors() = nil, want non-nil channel")
	}
}

func TestHubDaemonControllerRunning(t *testing.T) {
	t.Parallel()

	var nilController *hubDaemonController
	if nilController.Running() {
		t.Fatal("nil controller Running() = true, want false")
	}

	controller := newHubDaemonController(context.Background(), nil)
	if controller.Running() {
		t.Fatal("new controller Running() = true, want false")
	}
	done := make(chan error)
	controller.setActiveRun(func() {}, done)
	if !controller.Running() {
		t.Fatal("active controller Running() = false, want true")
	}
	controller.clearActiveRun(done)
	if controller.Running() {
		t.Fatal("cleared controller Running() = true, want false")
	}
}

func TestHubDaemonControllerUpdateValidatesInputs(t *testing.T) {
	t.Parallel()

	var nilController *hubDaemonController
	if err := nilController.Update(context.Background(), hub.InitConfig{}); err == nil || !strings.Contains(err.Error(), "hub daemon controller is required") {
		t.Fatalf("nil controller Update() error = %v, want required-controller error", err)
	}

	controller := &hubDaemonController{}
	if err := controller.Update(context.Background(), hub.InitConfig{}); err == nil || !strings.Contains(err.Error(), "parent context is required") {
		t.Fatalf("missing parent Update() error = %v, want required-parent-context error", err)
	}

	controller = newHubDaemonController(context.Background(), nil)
	invalidCfg := validHubControllerInitConfig()
	invalidCfg.Version = "v2"
	if err := controller.Update(context.Background(), invalidCfg); err == nil || !strings.Contains(err.Error(), "init config: unsupported version") {
		t.Fatalf("invalid config Update() error = %v, want wrapped init config validation error", err)
	}
}

func TestHubDaemonControllerUpdateHandlesExistingRunAndGracefulStart(t *testing.T) {
	t.Parallel()

	controller := newHubDaemonController(context.Background(), nil)
	controller.startGracePeriod = 5 * time.Millisecond

	prevCanceled := make(chan struct{})
	prevDone := make(chan error, 1)
	controller.setActiveRun(func() {
		close(prevCanceled)
		prevDone <- nil
		close(prevDone)
	}, prevDone)

	started := make(chan struct{})
	controller.runDaemon = func(ctx context.Context, _ hub.InitConfig) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}

	if err := controller.Update(context.Background(), validHubControllerInitConfig()); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}

	select {
	case <-prevCanceled:
	default:
		t.Fatal("expected previous run cancel function to be called")
	}
	select {
	case <-started:
	default:
		t.Fatal("expected new run to start")
	}

	cancel, done := controller.activeRun()
	if cancel == nil || done == nil {
		t.Fatalf("activeRun() = (%v, %v), want non-nil active run", cancel, done)
	}

	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v, want nil", err)
	}
}

func TestHubDaemonControllerUpdateReturnsEarlyRunErrors(t *testing.T) {
	t.Parallel()

	cfg := validHubControllerInitConfig()

	controller := newHubDaemonController(context.Background(), nil)
	controller.startGracePeriod = 100 * time.Millisecond
	controller.runDaemon = func(context.Context, hub.InitConfig) error {
		return errors.New("daemon exploded")
	}
	if err := controller.Update(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "daemon exploded") {
		t.Fatalf("Update() error = %v, want daemon error", err)
	}

	controller = newHubDaemonController(context.Background(), nil)
	controller.startGracePeriod = 100 * time.Millisecond
	controller.runDaemon = func(context.Context, hub.InitConfig) error {
		return nil
	}
	if err := controller.Update(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "hub transport exited unexpectedly") {
		t.Fatalf("Update() error = %v, want unexpected-exit error", err)
	}
}

func TestHubDaemonControllerUpdateHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	cfg := validHubControllerInitConfig()

	parentCtx, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	controller := newHubDaemonController(parentCtx, nil)
	if err := controller.Update(context.Background(), cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update() with canceled parent error = %v, want context.Canceled", err)
	}

	controller = newHubDaemonController(context.Background(), nil)
	reqCtx, cancelReq := context.WithCancel(context.Background())
	cancelReq()
	if err := controller.Update(reqCtx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update() with canceled request context error = %v, want context.Canceled", err)
	}

	controller = newHubDaemonController(context.Background(), nil)
	controller.startGracePeriod = 200 * time.Millisecond
	controller.runDaemon = func(ctx context.Context, _ hub.InitConfig) error {
		<-ctx.Done()
		return ctx.Err()
	}
	reqCtx, cancelReq = context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancelReq()
	}()
	if err := controller.Update(reqCtx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update() cancellation while starting error = %v, want context.Canceled", err)
	}
}

func TestHubDaemonControllerStopPaths(t *testing.T) {
	t.Parallel()

	var nilController *hubDaemonController
	if err := nilController.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "hub daemon controller is required") {
		t.Fatalf("nil Stop() error = %v, want required-controller error", err)
	}

	controller := newHubDaemonController(context.Background(), nil)
	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() with no active run error = %v, want nil", err)
	}

	controller.setActiveRun(func() {}, nil)
	if err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() with nil done channel error = %v, want nil", err)
	}

	done := make(chan error, 1)
	done <- errors.New("daemon stop error")
	close(done)
	canceled := false
	controller.setActiveRun(func() { canceled = true }, done)
	if err := controller.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "daemon stop error") {
		t.Fatalf("Stop() error = %v, want daemon stop error", err)
	}
	if !canceled {
		t.Fatal("expected Stop() to call active cancel function")
	}
	cancel, activeDone := controller.activeRun()
	if cancel != nil || activeDone != nil {
		t.Fatalf("activeRun() after stop = (%v, %v), want nil active run", cancel, activeDone)
	}

	blockedDone := make(chan error)
	controller.setActiveRun(func() {}, blockedDone)
	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelTimeout()
	err := controller.Stop(timeoutCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() timeout error = %v, want context.DeadlineExceeded", err)
	}
}

func TestHubDaemonControllerRunErrorPublishingAndActiveState(t *testing.T) {
	t.Parallel()

	cfg := validHubControllerInitConfig()
	controller := newHubDaemonController(context.Background(), nil)
	controller.errs = make(chan error, 1)

	var logs []string
	controller.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	controller.errs <- errors.New("already buffered")
	controller.runDaemon = func(context.Context, hub.InitConfig) error {
		return errors.New("daemon failed")
	}
	done := make(chan error, 1)
	controller.setActiveRun(func() {}, done)
	controller.run(context.Background(), cfg, done)

	if err := <-done; err == nil || !strings.Contains(err.Error(), "daemon failed") {
		t.Fatalf("run() done error = %v, want daemon failure", err)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], `hub.runtime status=warn err="daemon failed"`) {
		t.Fatalf("run() logs = %v, want dropped-error warning log", logs)
	}
	cancel, activeDone := controller.activeRun()
	if cancel != nil || activeDone != nil {
		t.Fatalf("activeRun() after run = (%v, %v), want nil active run", cancel, activeDone)
	}
}

func TestHubDaemonControllerRunSuppressesErrorAfterCancellation(t *testing.T) {
	t.Parallel()

	cfg := validHubControllerInitConfig()
	controller := newHubDaemonController(context.Background(), nil)
	controller.errs = make(chan error, 1)
	controller.runDaemon = func(context.Context, hub.InitConfig) error {
		return errors.New("daemon failed")
	}

	runCtx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	controller.setActiveRun(func() {}, done)
	controller.run(runCtx, cfg, done)

	if err := <-done; err != nil {
		t.Fatalf("run() done error = %v, want nil after cancellation", err)
	}
	select {
	case err := <-controller.errs:
		t.Fatalf("errs channel received unexpected error: %v", err)
	default:
	}
}

func TestHubDaemonControllerClearActiveRunIgnoresMismatchedDone(t *testing.T) {
	t.Parallel()

	controller := newHubDaemonController(context.Background(), nil)
	doneA := make(chan error)
	doneB := make(chan error)
	controller.setActiveRun(func() {}, doneA)

	controller.clearActiveRun(doneB)
	cancel, done := controller.activeRun()
	if cancel == nil || done != doneA {
		t.Fatalf("clearActiveRun(mismatched) active run = (%v, %v), want unchanged", cancel, done)
	}

	controller.clearActiveRun(doneA)
	cancel, done = controller.activeRun()
	if cancel != nil || done != nil {
		t.Fatalf("clearActiveRun(matched) active run = (%v, %v), want nil", cancel, done)
	}
}
