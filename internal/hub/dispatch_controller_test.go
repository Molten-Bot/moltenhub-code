package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

type staticSample struct {
	value resourceSample
	err   error
}

func (s staticSample) Sample() (resourceSample, error) {
	return s.value, s.err
}

func TestAdaptiveDispatchControllerQueuesNewestRequests(t *testing.T) {
	t.Parallel()

	controller := NewAdaptiveDispatchController(DispatcherConfig{
		MaxParallel:            1,
		MinParallel:            1,
		SampleWindow:           1,
		SampleIntervalMS:       1000,
		CPUHighWatermark:       85,
		MemoryHighWatermark:    90,
		DiskIOHighWatermarkMBs: 120,
	}, nil)
	controller.sample = staticSample{value: resourceSample{CPUPercent: 10, MemoryPercent: 20, DiskIOMBs: 1}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller.Start(ctx)

	releaseFirst, err := controller.Acquire(ctx, "req-1")
	if err != nil {
		t.Fatalf("Acquire(req-1) error = %v", err)
	}

	orderCh := make(chan string, 2)
	errCh := make(chan error, 2)
	releaseReq2 := make(chan struct{})
	releaseReq3 := make(chan struct{})

	go func() {
		release, acquireErr := controller.Acquire(ctx, "req-2")
		if acquireErr != nil {
			errCh <- acquireErr
			return
		}
		orderCh <- "req-2"
		<-releaseReq2
		release()
	}()
	waitForQueuedRequest(t, controller, "req-2")

	go func() {
		release, acquireErr := controller.Acquire(ctx, "req-3")
		if acquireErr != nil {
			errCh <- acquireErr
			return
		}
		orderCh <- "req-3"
		<-releaseReq3
		release()
	}()

	select {
	case got := <-orderCh:
		t.Fatalf("unexpected grant before release: %s", got)
	case err := <-errCh:
		t.Fatalf("unexpected acquire error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()

	select {
	case got := <-orderCh:
		if got != "req-2" {
			t.Fatalf("first granted request = %s, want req-2", got)
		}
	case err := <-errCh:
		t.Fatalf("unexpected acquire error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for req-2 grant")
	}

	select {
	case got := <-orderCh:
		t.Fatalf("req-3 granted too early: %s", got)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseReq2)

	select {
	case got := <-orderCh:
		if got != "req-3" {
			t.Fatalf("second granted request = %s, want req-3", got)
		}
	case err := <-errCh:
		t.Fatalf("unexpected acquire error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for req-3 grant")
	}

	close(releaseReq3)
}

func TestAdaptiveDispatchControllerScalesDownOnPressure(t *testing.T) {
	t.Parallel()

	controller := NewAdaptiveDispatchController(DispatcherConfig{
		MaxParallel:            2,
		MinParallel:            1,
		SampleWindow:           1,
		SampleIntervalMS:       20,
		CPUHighWatermark:       50,
		MemoryHighWatermark:    90,
		DiskIOHighWatermarkMBs: 100,
	}, nil)
	controller.sample = staticSample{value: resourceSample{CPUPercent: 95, MemoryPercent: 30, DiskIOMBs: 5}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseFirst, err := controller.Acquire(ctx, "req-1")
	if err != nil {
		t.Fatalf("Acquire(req-1) error = %v", err)
	}
	controller.sampleAndUpdate()

	acquiredReq2 := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	releaseReq2 := make(chan struct{})
	go func() {
		release, acquireErr := controller.Acquire(ctx, "req-2")
		if acquireErr != nil {
			errCh <- acquireErr
			return
		}
		acquiredReq2 <- struct{}{}
		<-releaseReq2
		release()
	}()

	select {
	case <-acquiredReq2:
		t.Fatal("req-2 should remain queued while req-1 is running under throttled capacity")
	case err := <-errCh:
		t.Fatalf("unexpected acquire error: %v", err)
	case <-time.After(120 * time.Millisecond):
	}

	releaseFirst()

	select {
	case <-acquiredReq2:
	case err := <-errCh:
		t.Fatalf("unexpected acquire error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for req-2 grant after release")
	}

	close(releaseReq2)
}

func TestAcquireReturnsClosedErrorWhenStopped(t *testing.T) {
	t.Parallel()

	controller := NewAdaptiveDispatchController(DispatcherConfig{
		MaxParallel:            1,
		MinParallel:            1,
		SampleWindow:           1,
		SampleIntervalMS:       50,
		CPUHighWatermark:       85,
		MemoryHighWatermark:    90,
		DiskIOHighWatermarkMBs: 120,
	}, nil)
	controller.sample = staticSample{value: resourceSample{CPUPercent: 10, MemoryPercent: 20}}

	ctx, cancel := context.WithCancel(context.Background())
	controller.Start(ctx)
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := controller.Acquire(context.Background(), "req-x")
		if errors.Is(err, errDispatchControllerClosed) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Acquire() error = %v, want errDispatchControllerClosed", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestComputeAllowedParallelScalesByPressure(t *testing.T) {
	t.Parallel()

	cfg := DispatcherConfig{
		MaxParallel:            8,
		MinParallel:            1,
		CPUHighWatermark:       80,
		MemoryHighWatermark:    90,
		DiskIOHighWatermarkMBs: 100,
	}
	avg := resourceSample{
		CPUPercent:    160,
		MemoryPercent: 40,
		DiskIOMBs:     10,
	}
	if got := computeAllowedParallel(cfg, avg); got != 4 {
		t.Fatalf("computeAllowedParallel() = %d, want 4", got)
	}
}

func waitForQueuedRequest(t *testing.T, controller *AdaptiveDispatchController, requestID string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		controller.mu.Lock()
		for _, waiter := range controller.waiters {
			if waiter.requestID == requestID {
				controller.mu.Unlock()
				return
			}
		}
		controller.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for request %s to enter queue", requestID)
}
