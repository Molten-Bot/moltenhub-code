package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
)

func TestShouldFallbackToPull(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "eof disconnect",
			err:  io.EOF,
			want: false,
		},
		{
			name: "closed network connection",
			err:  errors.New("read tcp 127.0.0.1:1234->127.0.0.1:8080: use of closed network connection"),
			want: false,
		},
		{
			name: "connection reset by peer",
			err:  errors.New("read tcp: connection reset by peer"),
			want: false,
		},
		{
			name: "websocket handshake unauthorized",
			err:  errors.New("websocket handshake status=401 body=unauthorized"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldFallbackToPull(tt.err); got != tt.want {
				t.Fatalf("shouldFallbackToPull() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsUnauthorizedHubError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "pull status 401",
			err:  errors.New("pull status=401"),
			want: true,
		},
		{
			name: "ws unauthorized",
			err:  errors.New("websocket handshake status=401 body=unauthorized"),
			want: true,
		},
		{
			name: "network disconnect",
			err:  errors.New("use of closed network connection"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isUnauthorizedHubError(tt.err); got != tt.want {
				t.Fatalf("isUnauthorizedHubError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDaemonRunRepeatsPingHealthPullBeforeEachWebsocketAttempt(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		events      []string
		wsAttempts  int
		secondWS    sync.Once
		secondWSHit = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := func(event string) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}

		switch r.URL.Path {
		case "/ping":
			record("ping")
			w.WriteHeader(http.StatusNoContent)
		case "/health":
			record("health")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/openclaw/messages/pull":
			record("pull")
			w.WriteHeader(http.StatusNoContent)
		case "/v1/openclaw/messages/ws":
			record("ws")
			mu.Lock()
			wsAttempts++
			currentAttempts := wsAttempts
			mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"moltenhub is starting","status":"starting"}`))
			if currentAttempts >= 2 {
				secondWS.Do(func() { close(secondWSHit) })
			}
		case "/v1/agents/me", "/v1/agents/me/status", "/v1/agents/me/metadata", "/v1/agents/me/activities":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/openclaw/messages/offline":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/v1/agents/me") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, InitConfig{
			BaseURL:    server.URL + "/v1",
			AgentToken: "agent_saved",
			SessionKey: "main",
		})
	}()

	select {
	case <-secondWSHit:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for second websocket attempt")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Daemon.Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for daemon shutdown")
	}

	mu.Lock()
	gotEvents := append([]string(nil), events...)
	gotAttempts := wsAttempts
	mu.Unlock()

	if gotAttempts < 2 {
		t.Fatalf("websocket attempts = %d, want >= 2", gotAttempts)
	}

	prevWS := -1
	for idx, event := range gotEvents {
		if event != "ws" {
			continue
		}
		window := gotEvents[prevWS+1 : idx]
		pingIdx := indexOfEvent(window, "ping", 0)
		healthIdx := indexOfEvent(window, "health", pingIdx+1)
		pullIdx := indexOfEvent(window, "pull", healthIdx+1)
		if pingIdx < 0 || healthIdx < 0 || pullIdx < 0 {
			t.Fatalf("missing ordered prechecks before ws attempt %d, events=%v", idx, gotEvents)
		}
		prevWS = idx
	}
}

func indexOfEvent(events []string, want string, start int) int {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(events); i++ {
		if events[i] == want {
			return i
		}
	}
	return -1
}
