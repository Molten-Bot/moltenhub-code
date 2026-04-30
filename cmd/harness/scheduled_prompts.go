package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

const scheduledPromptSource = "scheduled_prompt"

type scheduledPromptStore struct {
	mu      sync.Mutex
	path    string
	initCfg hub.InitConfig
	items   []hub.ScheduledPrompt
}

func newScheduledPromptStore(path string, initCfg hub.InitConfig) *scheduledPromptStore {
	return &scheduledPromptStore{
		path:    strings.TrimSpace(path),
		initCfg: initCfg,
		items:   hub.ReadRuntimeConfigScheduledPrompts(path),
	}
}

func (s *scheduledPromptStore) List() []hub.ScheduledPrompt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneScheduledPrompts(s.items)
}

func (s *scheduledPromptStore) Add(now time.Time, name string, everyMinutes int, runCfg config.Config) (hub.ScheduledPrompt, error) {
	if s == nil {
		return hub.ScheduledPrompt{}, fmt.Errorf("schedule store is unavailable")
	}
	if everyMinutes < 1 {
		return hub.ScheduledPrompt{}, fmt.Errorf("schedule interval must be at least 1 minute")
	}
	runCfg.ApplyDefaults()
	if err := runCfg.Validate(); err != nil {
		return hub.ScheduledPrompt{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	item := hub.ScheduledPrompt{
		ID:           nextScheduledPromptID(now, s.items),
		Name:         strings.TrimSpace(name),
		EveryMinutes: everyMinutes,
		CreatedAt:    now.Format(time.RFC3339),
		NextRunAt:    now.Add(time.Duration(everyMinutes) * time.Minute).Format(time.RFC3339),
		Config:       runCfg,
	}
	next := append(cloneScheduledPrompts(s.items), item)
	if err := hub.SaveRuntimeConfigScheduledPrompts(s.path, s.initCfg, next); err != nil {
		return hub.ScheduledPrompt{}, err
	}
	s.items = next
	return item, nil
}

func (s *scheduledPromptStore) Delete(id string) error {
	if s == nil {
		return fmt.Errorf("schedule store is unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("schedule id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]hub.ScheduledPrompt, 0, len(s.items))
	found := false
	for _, item := range s.items {
		if item.ID == id {
			found = true
			continue
		}
		next = append(next, item)
	}
	if !found {
		return fmt.Errorf("schedule %q not found", id)
	}
	if err := hub.SaveRuntimeConfigScheduledPrompts(s.path, s.initCfg, next); err != nil {
		return err
	}
	s.items = next
	return nil
}

func (s *scheduledPromptStore) Due(now time.Time) []hub.ScheduledPrompt {
	if s == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var due []hub.ScheduledPrompt
	for _, item := range s.items {
		nextRunAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.NextRunAt))
		if err != nil || nextRunAt.IsZero() || !nextRunAt.After(now) {
			due = append(due, item)
		}
	}
	return due
}

func (s *scheduledPromptStore) MarkRun(id, requestID string, runAt time.Time, runErr error) {
	if s == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if runAt.IsZero() {
		runAt = time.Now().UTC()
	} else {
		runAt = runAt.UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		s.items[i].LastRunAt = runAt.Format(time.RFC3339)
		s.items[i].LastRequestID = strings.TrimSpace(requestID)
		s.items[i].LastError = ""
		if runErr != nil {
			s.items[i].LastError = runErr.Error()
		}
		s.items[i].NextRunAt = runAt.Add(time.Duration(s.items[i].EveryMinutes) * time.Minute).Format(time.RFC3339)
		changed = true
		break
	}
	if changed {
		_ = hub.SaveRuntimeConfigScheduledPrompts(s.path, s.initCfg, s.items)
	}
}

func startScheduledPromptRunner(
	ctx context.Context,
	store *scheduledPromptStore,
	enqueue func(context.Context, config.Config, bool, string, bool) (string, error),
	logf func(string, ...any),
) {
	if store == nil || enqueue == nil {
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ticker := time.NewTicker(time.Minute)
	go func() {
		defer ticker.Stop()
		runDueScheduledPrompts(ctx, store, enqueue, logf, time.Now().UTC())
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				runDueScheduledPrompts(ctx, store, enqueue, logf, now.UTC())
			}
		}
	}()
}

func runDueScheduledPrompts(
	ctx context.Context,
	store *scheduledPromptStore,
	enqueue func(context.Context, config.Config, bool, string, bool) (string, error),
	logf func(string, ...any),
	now time.Time,
) {
	for _, item := range store.Due(now) {
		requestID, err := enqueue(ctx, item.Config, true, scheduledPromptSource, false)
		if err != nil {
			logf("schedule status=error id=%s err=%q", item.ID, err)
		} else {
			logf("schedule status=queued id=%s request_id=%s", item.ID, requestID)
		}
		store.MarkRun(item.ID, requestID, now, err)
	}
}

func scheduledPromptToUI(item hub.ScheduledPrompt) hubui.ScheduledPrompt {
	configJSON, _ := json.Marshal(item.Config)
	return hubui.ScheduledPrompt{
		ID:            item.ID,
		Name:          item.Name,
		EveryMinutes:  item.EveryMinutes,
		CreatedAt:     item.CreatedAt,
		NextRunAt:     item.NextRunAt,
		LastRunAt:     item.LastRunAt,
		LastRequestID: item.LastRequestID,
		LastError:     item.LastError,
		Config:        configJSON,
	}
}

func cloneScheduledPrompts(items []hub.ScheduledPrompt) []hub.ScheduledPrompt {
	if len(items) == 0 {
		return nil
	}
	out := make([]hub.ScheduledPrompt, len(items))
	copy(out, items)
	return out
}

func nextScheduledPromptID(now time.Time, existing []hub.ScheduledPrompt) string {
	seen := map[string]struct{}{}
	for _, item := range existing {
		seen[item.ID] = struct{}{}
	}
	base := fmt.Sprintf("schedule-%d", now.Unix())
	if _, exists := seen[base]; !exists {
		return base
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("%s-%d", base, i)
		if _, exists := seen[id]; !exists {
			return id
		}
	}
}
