package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jef/moltenhub-code/internal/library"
)

// MoltenHubAPI defines the runtime hub API surface with token-bound calls.
// Async methods return a buffered channel with one terminal error value.
type MoltenHubAPI interface {
	BaseURL() string
	Token() string

	ResolveAgentToken(ctx context.Context, cfg InitConfig) (string, error)
	SyncProfile(ctx context.Context, cfg InitConfig) error
	UpdateAgentStatus(ctx context.Context, status string) error
	MarkOpenClawOffline(ctx context.Context, sessionKey, reason string) error
	RecordActivity(ctx context.Context, activity string) error
	RecordCodingActivityRunning(ctx context.Context) error
	RecordGitHubTaskCompleteActivity(ctx context.Context) error
	RegisterRuntime(ctx context.Context, cfg InitConfig, libraryTasks []library.TaskSummary) error
	PublishResult(ctx context.Context, payload map[string]any) error
	PublishResultAsync(ctx context.Context, payload map[string]any) <-chan error
	PullOpenClawMessage(ctx context.Context, timeoutMs int) (PulledOpenClawMessage, bool, error)
	AckOpenClawDelivery(ctx context.Context, deliveryID string) error
	AckOpenClawDeliveryAsync(ctx context.Context, deliveryID string) <-chan error
	NackOpenClawDelivery(ctx context.Context, deliveryID string) error
	NackOpenClawDeliveryAsync(ctx context.Context, deliveryID string) <-chan error
}

// AsyncAPIClient wraps APIClient with token-bound methods and async helpers.
type AsyncAPIClient struct {
	client APIClient

	tokenMu sync.RWMutex
	token   string
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

// MarkOpenClawOffline marks websocket transport offline for the configured token.
func (c *AsyncAPIClient) MarkOpenClawOffline(ctx context.Context, sessionKey, reason string) error {
	return c.withToken(func(token string) error {
		return c.client.MarkOpenClawOffline(ctx, token, sessionKey, reason)
	})
}

// RecordActivity appends a custom activity entry to metadata.activities.
func (c *AsyncAPIClient) RecordActivity(ctx context.Context, activity string) error {
	return c.withToken(func(token string) error {
		return c.client.RecordActivity(ctx, token, activity)
	})
}

// RecordCodingActivityRunning appends a generic active-coding activity entry.
func (c *AsyncAPIClient) RecordCodingActivityRunning(ctx context.Context) error {
	return c.withToken(func(token string) error {
		return c.client.RecordCodingActivityRunning(ctx, token)
	})
}

// RecordGitHubTaskCompleteActivity appends a minimal completion activity entry.
func (c *AsyncAPIClient) RecordGitHubTaskCompleteActivity(ctx context.Context) error {
	return c.withToken(func(token string) error {
		return c.client.RecordGitHubTaskCompleteActivity(ctx, token)
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
	return c.runAsync(ctx, func(ctx context.Context) error {
		return c.PublishResult(ctx, payload)
	})
}

// PullOpenClawMessage pulls one inbound transport envelope.
func (c *AsyncAPIClient) PullOpenClawMessage(ctx context.Context, timeoutMs int) (PulledOpenClawMessage, bool, error) {
	return c.withTokenMessage(func(token string) (PulledOpenClawMessage, bool, error) {
		return c.client.PullOpenClawMessage(ctx, token, timeoutMs)
	})
}

// AckOpenClawDelivery acknowledges a leased delivery.
func (c *AsyncAPIClient) AckOpenClawDelivery(ctx context.Context, deliveryID string) error {
	return c.withToken(func(token string) error {
		return c.client.AckOpenClawDelivery(ctx, token, deliveryID)
	})
}

// AckOpenClawDeliveryAsync acknowledges a delivery on a background goroutine.
func (c *AsyncAPIClient) AckOpenClawDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	return c.runAsync(ctx, func(ctx context.Context) error {
		return c.AckOpenClawDelivery(ctx, deliveryID)
	})
}

// NackOpenClawDelivery releases a leased delivery back to the queue.
func (c *AsyncAPIClient) NackOpenClawDelivery(ctx context.Context, deliveryID string) error {
	return c.withToken(func(token string) error {
		return c.client.NackOpenClawDelivery(ctx, token, deliveryID)
	})
}

// NackOpenClawDeliveryAsync releases a delivery on a background goroutine.
func (c *AsyncAPIClient) NackOpenClawDeliveryAsync(ctx context.Context, deliveryID string) <-chan error {
	return c.runAsync(ctx, func(ctx context.Context) error {
		return c.NackOpenClawDelivery(ctx, deliveryID)
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
	call func(string) (PulledOpenClawMessage, bool, error),
) (PulledOpenClawMessage, bool, error) {
	token, err := c.requireToken()
	if err != nil {
		return PulledOpenClawMessage{}, false, err
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
