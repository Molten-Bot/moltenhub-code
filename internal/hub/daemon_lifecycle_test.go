package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonRunPublishesAgentLifecycleStatus(t *testing.T) {
	type statusCall struct {
		Status string
	}
	type offlineCall struct {
		SessionKey string
		Reason     string
	}
	type metadataCall struct {
		Method string
		Path   string
	}
	var (
		mu            sync.Mutex
		statuses      []statusCall
		offlineCalls  []offlineCall
		metadataCalls []metadataCall
		onlineCh      = make(chan struct{})
		once          sync.Once
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/agents/me/metadata":
			mu.Lock()
			metadataCalls = append(metadataCalls, metadataCall{
				Method: r.Method,
				Path:   r.URL.Path,
			})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/agents/me/status":
			defer r.Body.Close()
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)

			status, _ := body["status"].(string)
			mu.Lock()
			statuses = append(statuses, statusCall{Status: status})
			mu.Unlock()

			if status == "online" {
				once.Do(func() {
					close(onlineCh)
				})
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/openclaw/messages/offline":
			defer r.Body.Close()
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)

			mu.Lock()
			offlineCalls = append(offlineCalls, offlineCall{
				SessionKey: firstString(body["session_key"]),
				Reason:     firstString(body["reason"]),
			})
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/openclaw/messages/ws":
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		case "/v1/openclaw/messages/pull":
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	defer ts.Close()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tmpDir, err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	d := NewDaemon(nil)
	d.ReconnectDelay = 10 * time.Millisecond

	cfg := InitConfig{
		BaseURL:    ts.URL + "/v1",
		AgentToken: "agent_token",
		SessionKey: "main",
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel: 1,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, cfg)
	}()

	select {
	case <-onlineCh:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for online status update")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for daemon shutdown")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(statuses) < 2 {
		t.Fatalf("status calls = %d, want at least 2", len(statuses))
	}
	if len(offlineCalls) < 1 {
		t.Fatalf("offline calls = %d, want at least 1", len(offlineCalls))
	}
	if len(metadataCalls) < 1 {
		t.Fatalf("metadata calls = %d, want at least 1", len(metadataCalls))
	}
	if metadataCalls[0].Method != http.MethodPost {
		t.Fatalf("first metadata method = %q, want %q", metadataCalls[0].Method, http.MethodPost)
	}
	if metadataCalls[0].Path != "/v1/agents/me/metadata" {
		t.Fatalf("first metadata path = %q, want %q", metadataCalls[0].Path, "/v1/agents/me/metadata")
	}
	if statuses[0].Status != "online" {
		t.Fatalf("first status = %q, want online", statuses[0].Status)
	}
	last := statuses[len(statuses)-1]
	if last.Status != "offline" {
		t.Fatalf("last status = %q, want offline", last.Status)
	}
	lastOffline := offlineCalls[len(offlineCalls)-1]
	if lastOffline.SessionKey != "main" {
		t.Fatalf("last offline session_key = %q, want main", lastOffline.SessionKey)
	}
	if lastOffline.Reason != transportOfflineReasonAgent {
		t.Fatalf("last offline reason = %q, want %q", lastOffline.Reason, transportOfflineReasonAgent)
	}
}

func TestDaemonRunReturnsAuthErrorWhenTransportIsUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/openclaw/messages/register-plugin":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/agents/me/metadata", "/v1/agents/me/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		case "/v1/openclaw/messages/ws":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		case "/v1/openclaw/messages/pull":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	defer ts.Close()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmpDir := t.TempDir()
	// This test mutates process working directory; keep it non-parallel.
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tmpDir, err)
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	d := NewDaemon(nil)
	d.ReconnectDelay = 10 * time.Millisecond

	cfg := InitConfig{
		BaseURL:    ts.URL + "/v1",
		AgentToken: "agent_token",
		SessionKey: "main",
		Skill: SkillConfig{
			Name:         "code_for_me",
			DispatchType: "skill_request",
			ResultType:   "skill_result",
		},
		Dispatcher: DispatcherConfig{
			MaxParallel: 1,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = d.Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run() error = nil, want auth error")
	}
	if !strings.Contains(err.Error(), "hub auth: pull status=401") {
		t.Fatalf("Run() error = %q, want pull unauthorized auth error", err)
	}
}
