package hubui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/githubutil"
)

const defaultPRMergePollInterval = 30 * time.Second

// PRMergeMonitor removes completed tasks from the monitor once their PRs merge.
type PRMergeMonitor struct {
	Runner       execx.Runner
	Broker       *Broker
	Logf         func(string, ...any)
	CleanupTask  func(context.Context, string) error
	PollInterval time.Duration

	mu       sync.Mutex
	inFlight map[string]struct{}
}

type prViewState struct {
	State    string `json:"state"`
	MergedAt string `json:"mergedAt"`
}

// Run polls tracked PRs until ctx is canceled.
func (m *PRMergeMonitor) Run(ctx context.Context) error {
	if m == nil || m.Broker == nil || m.Runner == nil {
		return nil
	}
	if m.Logf == nil {
		m.Logf = func(string, ...any) {}
	}
	if m.PollInterval <= 0 {
		m.PollInterval = defaultPRMergePollInterval
	}

	ticker := time.NewTicker(m.PollInterval)
	defer ticker.Stop()

	m.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.pollOnce(ctx)
		}
	}
}

func (m *PRMergeMonitor) pollOnce(ctx context.Context) {
	snapshot := m.Broker.Snapshot()
	for _, task := range snapshot.Tasks {
		if !shouldMonitorTaskPR(task) {
			continue
		}
		if !m.beginCheck(task.RequestID) {
			continue
		}
		go func(task Task) {
			defer m.endCheck(task.RequestID)
			m.checkTaskPR(ctx, task)
		}(task)
	}
}

func shouldMonitorTaskPR(task Task) bool {
	if strings.TrimSpace(task.PRURL) == "" {
		return false
	}
	switch strings.TrimSpace(task.Status) {
	case "completed", "ok", "no_changes":
		return true
	default:
		return false
	}
}

func (m *PRMergeMonitor) beginCheck(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight == nil {
		m.inFlight = map[string]struct{}{}
	}
	if _, exists := m.inFlight[requestID]; exists {
		return false
	}
	m.inFlight[requestID] = struct{}{}
	return true
}

func (m *PRMergeMonitor) endCheck(requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inFlight, strings.TrimSpace(requestID))
}

func (m *PRMergeMonitor) checkTaskPR(ctx context.Context, task Task) {
	state, err := m.prState(ctx, task.PRURL)
	if err != nil {
		m.Logf("hub.ui status=warn event=pr_monitor request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
		return
	}
	if !state.Merged() {
		return
	}
	if err := m.Broker.CloseTask(task.RequestID); err != nil {
		if !errors.Is(err, ErrTaskNotFound) {
			m.Logf("hub.ui status=warn event=pr_monitor_close request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
		}
		return
	}
	if m.CleanupTask != nil {
		if err := m.CleanupTask(ctx, task.RequestID); err != nil {
			m.Logf("hub.ui status=warn event=pr_monitor_cleanup request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
		}
	}
	m.Logf("hub.ui status=ok event=pr_merged request_id=%s pr_url=%s", task.RequestID, task.PRURL)
}

func (m *PRMergeMonitor) prState(ctx context.Context, prURL string) (prViewState, error) {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return prViewState{}, fmt.Errorf("pull request url is required")
	}
	res, err := m.Runner.Run(ctx, execx.Command{
		Name: "gh",
		Args: []string{"pr", "view", githubutil.PullRequestSelector(prURL), "--json", "state,mergedAt"},
	})
	if err != nil {
		return prViewState{}, err
	}
	var state prViewState
	if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &state); decodeErr != nil {
		return prViewState{}, fmt.Errorf("decode gh pr view response: %w", decodeErr)
	}
	return state, nil
}

func (s prViewState) Merged() bool {
	return strings.EqualFold(strings.TrimSpace(s.State), "merged") || strings.TrimSpace(s.MergedAt) != ""
}
