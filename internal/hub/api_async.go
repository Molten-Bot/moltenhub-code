package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
)

// MoltenHubAPI defines the runtime hub API surface with token-bound calls.
// Async methods return a buffered channel with one terminal error value.
type MoltenHubAPI interface {
	BaseURL() string
	Token() string

	ResolveAgentToken(ctx context.Context, cfg InitConfig) (string, error)
	SyncProfile(ctx context.Context, cfg InitConfig) error
	UpdateAgentStatus(ctx context.Context, status string) error
	MarkRuntimeOffline(ctx context.Context, sessionKey, reason string) error
	RecordActivity(ctx context.Context, activity string) error
	RecordCodingActivityRunning(ctx context.Context) error
	RecordGitHubTaskCompleteActivity(ctx context.Context) error
	RecordRunStartedActivity(ctx context.Context, runCfg config.Config) error
	RecordRunCompletedActivity(ctx context.Context, runCfg config.Config) error
	RegisterRuntime(ctx context.Context, cfg InitConfig, libraryTasks []library.TaskSummary) error
	PublishResult(ctx context.Context, payload map[string]any) error
	PublishResultAsync(ctx context.Context, payload map[string]any) <-chan error
	PullRuntimeMessage(ctx context.Context, timeoutMs int) (PulledRuntimeMessage, bool, error)
	AckRuntimeDelivery(ctx context.Context, deliveryID string) error
	AckRuntimeDeliveryAsync(ctx context.Context, deliveryID string) <-chan error
	NackRuntimeDelivery(ctx context.Context, deliveryID string) error
	NackRuntimeDeliveryAsync(ctx context.Context, deliveryID string) <-chan error
}

// AsyncAPIClient wraps APIClient with token-bound methods and async helpers.
type AsyncAPIClient struct {
	client APIClient

	tokenMu sync.RWMutex
	token   string

	publishQueueOnce sync.Once
	publishQueue     *asyncPublishQueue
}

type asyncPublishJob struct {
	ctx     context.Context
	payload map[string]any
	done    chan error
}

type asyncPublishQueue struct {
	mu     sync.Mutex
	notify chan struct{}
	jobs   []asyncPublishJob
}

// NewAsyncAPIClient returns a token-bound async hub API wrapper.
func NewAsyncAPIClient(baseURL, token string) *AsyncAPIClient {
	return NewAsyncAPIClientFrom(NewAPIClient(baseURL), token)
}

// NewAsyncAPIClientFrom wraps an existing transport-level API client.
func NewAsyncAPIClientFrom(client APIClient, token string) *AsyncAPIClient {
	return &AsyncAPIClient{
		client: client,
		token:  strings.TrimSpace(token),
	}
}

// BaseURL returns the normalized API base URL.
func (c *AsyncAPIClient) BaseURL() string {
	if c == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(c.client.BaseURL), "/")
}

// Token returns the currently configured bearer token.
func (c *AsyncAPIClient) Token() string {
	if c == nil {
		return ""
	}
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

// ResolveAgentToken resolves and stores a working token for subsequent calls.
func (c *AsyncAPIClient) ResolveAgentToken(ctx context.Context, cfg InitConfig) (string, error) {
	if c == nil {
		return "", fmt.Errorf("moltenhub api client is required")
	}
	token, err := c.client.ResolveAgentToken(ctx, cfg)
	if err != nil {
		return "", err
	}
	c.setToken(token)
	return token, nil
}

// SyncProfile syncs profile metadata for the configured token.
func (c *AsyncAPIClient) SyncProfile(ctx context.Context, cfg InitConfig) error {
	return c.withToken(func(token string) error {
		return c.client.SyncProfile(ctx, token, cfg)
	})
}

// UpdateAgentStatus updates agent lifecycle status for the configured token.
func (c *AsyncAPIClient) UpdateAgentStatus(ctx context.Context, status string) error {
	return c.withToken(func(token string) error {
		return c.client.UpdateAgentStatus(ctx, token, status)
	})
}

// MarkRuntimeOffline marks websocket transport offline for the configured token.
func (c *AsyncAPIClient) MarkRuntimeOffline(ctx context.Context, sessionKey, reason string) error {
	return c.withToken(func(token string) error {
		return c.client.MarkRuntimeOffline(ctx, token, sessionKey, reason)
	})
}

// RecordActivity publishes a custom agent activity.
func (c *AsyncAPIClient) RecordActivity(ctx context.Context, activity string) error {
	return c.withToken(func(token string) error {
		return c.client.RecordActivity(ctx, token, activity)
	})
}

// RecordCodingActivityRunning publishes a generic active-coding activity.
func (c *AsyncAPIClient) RecordCodingActivityRunning(ctx context.Context) error {
	return c.withToken(func(token string) error {
		return c.client.RecordCodingActivityRunning(ctx, token)
	})
}

// RecordGitHubTaskCompleteActivity publishes a minimal completion activity.
func (c *AsyncAPIClient) RecordGitHubTaskCompleteActivity(ctx context.Context) error {
	return c.withToken(func(token string) error {
		return c.client.RecordGitHubTaskCompleteActivity(ctx, token)
	})
}

// RecordRunStartedActivity publishes the standard activity entry for one task run.
func (c *AsyncAPIClient) RecordRunStartedActivity(ctx context.Context, runCfg config.Config) error {
	return c.withToken(func(token string) error {
		return c.client.RecordRunStartedActivity(ctx, token, runCfg)
	})
}

// RecordRunCompletedActivity publishes the standard activity entry for one completed task run.
func (c *AsyncAPIClient) RecordRunCompletedActivity(ctx context.Context, runCfg config.Config) error {
	return c.withToken(func(token string) error {
		return c.client.RecordRunCompletedActivity(ctx, token, runCfg)
	})
}

// RegisterRuntime registers runtime metadata for the configured token.
func (c *AsyncAPIClient) RegisterRuntime(ctx context.Context, cfg InitConfig, libraryTasks []library.TaskSummary) error {
	return c.withToken(func(token string) error {
		return c.client.RegisterRuntime(ctx, token, cfg, libraryTasks)
	})
}

// PublishResult publishes a skill result for the configured token.
func (c *AsyncAPIClient) PublishResult(ctx context.Context, payload map[string]any) error {
	return c.withToken(func(token string) error {
		return c.client.PublishResult(ctx, token, payload)
	})
}

// PublishResultAsync publishes a result on a background goroutine.
func (c *AsyncAPIClient) PublishResultAsync(ctx context.Context, payload map[string]any) <-chan error {
	done := make(chan error, 1)
	if c == nil {
		done <- fmt.Errorf("moltenhub api client is required")
		close(done)
		return done
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job := asyncPublishJob{ctx: ctx, payload: payload, done: done}
	queue := c.publishResultQueue()
	queue.enqueue(job)
	return done
}

// PullRuntimeMessage pulls one inbound transport envelope.
func (c *AsyncAPIClient) PullRuntimeMessage(ctx context.Context, timeoutMs int) (PulledRuntimeMessage, bool, error) {
	return c.withTokenMessage(func(token string) (PulledRuntimeMessage, bool, error) {
		return c.client.PullRuntimeMessage(ctx, token, timeoutMs)
	})
}

// AckRuntimeDelivery acknowledges a leased delivery.
func (c *AsyncAPIClient) AckRuntimeDelivery(ctx context.Context, deliveryID string) error {
	return c.withToken(func(token string) error {
		return c.client.AckRuntimeDelivery(ctx, token, deliveryID)
	})
}

// AckRuntimeDeliveryAsync acknowledges a delivery on a background goroutine.
func (c *AsyncAPIClient) AckRuntimeDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	return c.runAsync(ctx, func(ctx context.Context) error {
		return c.AckRuntimeDelivery(ctx, deliveryID)
	})
}

// NackRuntimeDelivery releases a leased delivery back to the queue.
func (c *AsyncAPIClient) NackRuntimeDelivery(ctx context.Context, deliveryID string) error {
	return c.withToken(func(token string) error {
		return c.client.NackRuntimeDelivery(ctx, token, deliveryID)
	})
}

// NackRuntimeDeliveryAsync releases a delivery on a background goroutine.
func (c *AsyncAPIClient) NackRuntimeDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	return c.runAsync(ctx, func(ctx context.Context) error {
		return c.NackRuntimeDelivery(ctx, deliveryID)
	})
}

func (c *AsyncAPIClient) requireToken() (string, error) {
	token := strings.TrimSpace(c.Token())
	if token == "" {
		return "", fmt.Errorf("moltenhub api token is required")
	}
	return token, nil
}

func (c *AsyncAPIClient) setToken(token string) {
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
}

func (c *AsyncAPIClient) withToken(call func(string) error) error {
	token, err := c.requireToken()
	if err != nil {
		return err
	}
	return call(token)
}

func (c *AsyncAPIClient) withTokenMessage(
	call func(string) (PulledRuntimeMessage, bool, error),
) (PulledRuntimeMessage, bool, error) {
	token, err := c.requireToken()
	if err != nil {
		return PulledRuntimeMessage{}, false, err
	}
	return call(token)
}

func (c *AsyncAPIClient) runAsync(ctx context.Context, fn func(context.Context) error) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- fn(ctx)
	}()
	return done
}

func (c *AsyncAPIClient) publishResultQueue() *asyncPublishQueue {
	c.publishQueueOnce.Do(func() {
		c.publishQueue = &asyncPublishQueue{
			notify: make(chan struct{}, 1),
		}
		go c.runPublishResultQueue(c.publishQueue)
	})
	return c.publishQueue
}

func (c *AsyncAPIClient) runPublishResultQueue(queue *asyncPublishQueue) {
	for {
		job := queue.dequeue()
		job.done <- c.PublishResult(job.ctx, job.payload)
		close(job.done)
	}
}

func (q *asyncPublishQueue) enqueue(job asyncPublishJob) {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *asyncPublishQueue) dequeue() asyncPublishJob {
	for {
		q.mu.Lock()
		if len(q.jobs) > 0 {
			job := q.jobs[0]
			copy(q.jobs, q.jobs[1:])
			q.jobs[len(q.jobs)-1] = asyncPublishJob{}
			q.jobs = q.jobs[:len(q.jobs)-1]
			q.mu.Unlock()
			return job
		}
		q.mu.Unlock()
		<-q.notify
	}
}
