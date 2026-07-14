package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestIsRetryableHubConnectionError(t *testing.T) {
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
			name: "service unavailable",
			err:  errors.New("load agent profile: status=503 body=moltenhub is starting"),
			want: true,
		},
		{
			name: "network error",
			err:  errors.New("bind flow failed: bind status=500; bind.auth network error: dial tcp: connection refused"),
			want: true,
		},
		{
			name: "network unreachable",
			err:  errors.New("Get https://na.hub.molten.bot/v1/agents/me: dial tcp: connect: network is unreachable"),
			want: true,
		},
		{
			name: "no route to host",
			err:  errors.New("dial tcp 203.0.113.1:443: connect: no route to host"),
			want: true,
		},
		{
			name: "dns server starting",
			err:  errors.New("lookup na.hub.molten.bot on 127.0.0.11:53: server misbehaving"),
			want: true,
		},
		{
			name: "truncated response",
			err:  fmt.Errorf("read body: %w", io.ErrUnexpectedEOF),
			want: true,
		},
		{
			name: "cloud proxy unavailable",
			err:  errors.New("bind flow failed: bind-tokens.bind_token status=522"),
			want: true,
		},
		{
			name: "not found",
			err:  errors.New("load agent profile: status=404 body=missing"),
			want: false,
		},
		{
			name: "not implemented",
			err:  errors.New("load agent profile: status=501 body=unsupported"),
			want: false,
		},
		{
			name: "bad credentials",
			err:  errors.New("websocket handshake status=401 body=unauthorized"),
			want: false,
		},
		{
			name: "canceled",
			err:  context.Canceled,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableHubConnectionError(tt.err); got != tt.want {
				t.Fatalf("isRetryableHubConnectionError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsUnsupportedRuntimeWebsocketError(t *testing.T) {
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
			name: "websocket handshake 404",
			err:  errors.New("websocket handshake failed: status=404"),
			want: true,
		},
		{
			name: "websocket handshake 405",
			err:  errors.New("websocket handshake failed: status=405"),
			want: true,
		},
		{
			name: "websocket handshake 501",
			err:  errors.New("websocket handshake failed: status=501"),
			want: true,
		},
		{
			name: "unsupported status without websocket handshake",
			err:  errors.New("http status=404"),
			want: false,
		},
		{
			name: "websocket handshake unauthorized",
			err:  errors.New("websocket handshake failed: status=401"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isUnsupportedRuntimeWebsocketError(tt.err); got != tt.want {
				t.Fatalf("isUnsupportedRuntimeWebsocketError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizeDispatchStatusMessageIDPart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "keeps lowercase digits dash and underscore",
			value: "Stage_1-OK",
			want:  "stage_1-ok",
		},
		{
			name:  "replaces punctuation and trims separators",
			value: "  *** waiting: repo/clone ***  ",
			want:  "waiting--repo-clone",
		},
		{
			name:  "empty after sanitizing",
			value: "!!!",
			want:  "status",
		},
		{
			name:  "truncates long values and trims trailing separators",
			value: strings.Repeat("a", 39) + "!!!tail",
			want:  strings.Repeat("a", 39),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeDispatchStatusMessageIDPart(tt.value); got != tt.want {
				t.Fatalf("sanitizeDispatchStatusMessageIDPart(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDaemonRunStartsTransportWhenOptionalBootstrapCallsFail(t *testing.T) {
	t.Parallel()

	var (
		mu              sync.Mutex
		agentMeGETs     int
		statusHit       int
		agentMeGETsAtWS int
		statusHitAtWS   int
		wsHit           = make(chan struct{})
		wsOnce          sync.Once
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/a2a/agent-card":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"agent","description":"ok","url":"http://example.test","version":"1.0.0","capabilities":{},"skills":[]}`))
		case "/v1/agents/me":
			if r.Method == http.MethodGet {
				mu.Lock()
				agentMeGETs++
				mu.Unlock()
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"profile service is unavailable"}`))
		case "/v1/agents/me/status", "/v1/agents/me/metadata":
			if r.URL.Path == "/v1/agents/me/status" {
				mu.Lock()
				statusHit++
				mu.Unlock()
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"metadata service is unavailable"}`))
		case "/ping", "/health", "/v1/runtime/messages/pull":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/runtime/messages/ws":
			mu.Lock()
			agentMeGETsAtWS = agentMeGETs
			statusHitAtWS = statusHit
			mu.Unlock()
			wsOnce.Do(func() { close(wsHit) })
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"moltenhub is starting"}`))
		case "/v1/runtime/messages/offline":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = time.Millisecond
	runtimeCfgPath := filepath.Join(t.TempDir(), "runtime.json")

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, InitConfig{
			BaseURL:           server.URL + "/v1",
			AgentToken:        "agent-token",
			SessionKey:        "main",
			RuntimeConfigPath: runtimeCfgPath,
		})
	}()

	select {
	case <-wsHit:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for websocket attempt during optional bootstrap outage")
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
	defer mu.Unlock()
	if agentMeGETsAtWS < 3 {
		t.Fatalf("GET /agents/me requests before websocket = %d, want at least 3", agentMeGETsAtWS)
	}
	if statusHitAtWS == 0 {
		t.Fatal("online status update was not attempted before websocket")
	}
}

func TestDaemonRunRetriesTransientBindTokenOnlyBootstrapFailure(t *testing.T) {
	t.Parallel()

	var (
		mu                    sync.Mutex
		bindHits              int
		retryLogged           bool
		secondBindBeforeRetry bool
		wsHit                 = make(chan struct{})
		wsOnce                sync.Once
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens", "/v1/agents/bind":
			mu.Lock()
			bindHits++
			hit := bindHits
			if hit == 2 && !retryLogged {
				secondBindBeforeRetry = true
			}
			mu.Unlock()
			if hit == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"message":"moltenhub is starting"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"agent_token":"agent-token"}`))
		case "/v1/a2a/agent-card":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"agent","description":"ok","url":"http://example.test","version":"1.0.0","capabilities":{},"skills":[]}`))
		case "/v1/agents/me":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"handle":"saved"}`))
		case "/v1/agents/me/status", "/v1/agents/me/metadata":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ping", "/health", "/v1/runtime/messages/pull":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/runtime/messages/ws":
			wsOnce.Do(func() { close(wsHit) })
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"moltenhub is starting"}`))
		case "/v1/runtime/messages/offline":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = time.Millisecond
	runtimeCfgPath := filepath.Join(t.TempDir(), "runtime.json")
	d.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if strings.HasPrefix(line, "hub.connection status=retrying") {
			mu.Lock()
			retryLogged = true
			mu.Unlock()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, InitConfig{
			BaseURL:           server.URL + "/v1",
			BindToken:         "bind-token",
			SessionKey:        "main",
			RuntimeConfigPath: runtimeCfgPath,
		})
	}()

	select {
	case <-wsHit:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for websocket attempt after bind-token bootstrap retry")
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
	defer mu.Unlock()
	if bindHits != 2 {
		t.Fatalf("bind hits = %d, want 2", bindHits)
	}
	if !retryLogged {
		t.Fatal("missing hub.connection retrying log")
	}
	if secondBindBeforeRetry {
		t.Fatal("bind compatibility fallback ran before transient failure backoff")
	}
}

func TestDaemonRunKeepsResolvedBindStateAndAPIBaseAfterProfileFailure(t *testing.T) {
	t.Parallel()

	var (
		mu              sync.Mutex
		bindHits        int
		oldBaseHits     int
		agentProfileHit int
		wsHit           = make(chan struct{})
		wsOnce          sync.Once
	)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/a2a/agent-card":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"agent","description":"ok","url":"http://example.test","version":"1.0.0","capabilities":{},"skills":[]}`))
		case "/v1/agents/me":
			mu.Lock()
			agentProfileHit++
			hit := agentProfileHit
			mu.Unlock()
			if hit == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"message":"profile service is starting"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"handle":"saved"}`))
		case "/v1/agents/me/status", "/v1/agents/me/metadata":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ping", "/health", "/v1/runtime/messages/pull":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/runtime/messages/ws":
			wsOnce.Do(func() { close(wsHit) })
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"moltenhub is starting"}`))
		case "/v1/runtime/messages/offline":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	bindServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind-tokens", "/v1/agents/bind":
			mu.Lock()
			bindHits++
			hit := bindHits
			mu.Unlock()
			if hit > 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"agent_token":"agent-token","api_base_url":%q}`, apiServer.URL+"/v1")
		default:
			mu.Lock()
			oldBaseHits++
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer bindServer.Close()

	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = time.Millisecond
	runtimeCfgPath := filepath.Join(t.TempDir(), "runtime.json")
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, InitConfig{
			BaseURL:           bindServer.URL + "/v1",
			BindToken:         "single-use-bind-token",
			SessionKey:        "main",
			RuntimeConfigPath: runtimeCfgPath,
		})
	}()

	select {
	case <-wsHit:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for websocket attempt on returned API base")
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
	defer mu.Unlock()
	if bindHits != 1 {
		t.Fatalf("bind hits = %d, want 1", bindHits)
	}
	if oldBaseHits != 0 {
		t.Fatalf("requests to stale bind base = %d, want 0", oldBaseHits)
	}
	if agentProfileHit < 2 {
		t.Fatalf("API-base profile hits = %d, want at least 2", agentProfileHit)
	}
}

func TestDaemonRunCancelsDuringBootstrapWithoutOfflineUpdate(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	requestRelease := make(chan struct{})
	var requestOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/bind-tokens" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requestOnce.Do(func() { close(requestStarted) })
		<-requestRelease
	}))
	defer server.Close()

	var (
		mu            sync.Mutex
		offlineLogged bool
	)
	d := NewDaemon(execx.OSRunner{})
	d.ReconnectDelay = time.Millisecond
	d.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if strings.Contains(line, "state=offline") || strings.Contains(line, "status=offline") {
			mu.Lock()
			offlineLogged = true
			mu.Unlock()
		}
	}
	runtimeCfgPath := filepath.Join(t.TempDir(), "runtime.json")

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx, InitConfig{
			BaseURL:           server.URL + "/v1",
			BindToken:         "bind-token",
			SessionKey:        "main",
			RuntimeConfigPath: runtimeCfgPath,
		})
	}()

	select {
	case <-requestStarted:
		cancel()
		close(requestRelease)
	case <-time.After(3 * time.Second):
		cancel()
		close(requestRelease)
		t.Fatal("timed out waiting for bootstrap request")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Daemon.Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled bootstrap shutdown")
	}

	mu.Lock()
	defer mu.Unlock()
	if offlineLogged {
		t.Fatal("offline update logged after bootstrap was canceled")
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
		case "/v1/runtime/messages/pull":
			record("pull")
			w.WriteHeader(http.StatusNoContent)
		case "/v1/runtime/messages/ws":
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
		case "/v1/runtime/messages/offline":
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
