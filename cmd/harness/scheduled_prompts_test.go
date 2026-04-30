package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func TestScheduledPromptStorePersistsAddDelete(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	initCfg := hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "codex",
	}
	if err := hub.SaveRuntimeConfig(path, initCfg, "agent_abc"); err != nil {
		t.Fatalf("SaveRuntimeConfig() error = %v", err)
	}

	store := newScheduledPromptStore(path, initCfg)
	item, err := store.Add(time.Unix(100, 0).UTC(), "hourly", 60, config.Config{
		Repos:        []string{"git@github.com:acme/repo.git"},
		TargetSubdir: ".",
		Prompt:       "ship it",
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	loaded := hub.ReadRuntimeConfigScheduledPrompts(path)
	if len(loaded) != 1 || loaded[0].ID != item.ID {
		t.Fatalf("persisted schedules = %#v, want %q", loaded, item.ID)
	}

	if err := store.Delete(item.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := hub.ReadRuntimeConfigScheduledPrompts(path); len(got) != 0 {
		t.Fatalf("persisted schedules after delete = %#v, want empty", got)
	}
}

func TestScheduledPromptStoreDueAndMarkRun(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".moltenhub", "config.json")
	initCfg := hub.InitConfig{
		BaseURL:      "https://na.hub.molten.bot/v1",
		AgentHarness: "codex",
	}
	store := newScheduledPromptStore(path, initCfg)
	now := time.Unix(200, 0).UTC()
	item, err := store.Add(now.Add(-2*time.Minute), "minute", 1, config.Config{
		Repos:        []string{"git@github.com:acme/repo.git"},
		TargetSubdir: ".",
		Prompt:       "run due",
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	due := store.Due(now)
	if len(due) != 1 || due[0].ID != item.ID {
		t.Fatalf("Due() = %#v, want %q", due, item.ID)
	}

	store.MarkRun(item.ID, "local-1", now, nil)
	next := store.List()
	if len(next) != 1 || next[0].LastRequestID != "local-1" {
		t.Fatalf("List() after MarkRun = %#v", next)
	}
	if due := store.Due(now); len(due) != 0 {
		t.Fatalf("Due() after MarkRun = %#v, want empty", due)
	}
}
