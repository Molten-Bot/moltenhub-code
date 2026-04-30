package hubui

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
)

//go:embed static/*
var staticFiles embed.FS

const maxLocalPromptBodyBytes = 16 << 20
const maxAgentAuthConfigureBodyBytes = 1 << 20
const maxHubSetupConfigureBodyBytes = 1 << 20
const streamSnapshotInterval = 120 * time.Millisecond
const maxStreamTaskLogs = 500

// Server provides an HTTP UI for live hub/task monitoring.
type Server struct {
	Addr                    string
	Broker                  *Broker
	AutomaticMode           bool
	ConfiguredHarness       string
	Logf                    func(string, ...any)
	SubmitLocalPrompt       func(context.Context, []byte) (string, error)
	SubmitTaskRerun         func(context.Context, string, []byte, bool) (string, error)
	CloseTask               func(context.Context, string) error
	PauseTask               func(context.Context, string) error
	RunTask                 func(context.Context, string) error
	ForceRunTask            func(context.Context, string) error
	StopTask                func(context.Context, string) error
	ResolveTaskControls     func(string) TaskControls
	ListScheduledPrompts    func(context.Context) ([]ScheduledPrompt, error)
	CreateScheduledPrompt   func(context.Context, ScheduledPromptCreateRequest) (ScheduledPrompt, error)
	DeleteScheduledPrompt   func(context.Context, string) error
	LoadLibraryTasks        func() ([]library.TaskSummary, error)
	AgentAuthStatus         func(context.Context) (AgentAuthState, error)
	StartAgentAuth          func(context.Context) (AgentAuthState, error)
	VerifyAgentAuth         func(context.Context) (AgentAuthState, error)
	ConfigureAgentAuth      func(context.Context, string) (AgentAuthState, error)
	HubSetupStatus          func(context.Context) (HubSetupState, error)
	ConfigureHubSetup       func(context.Context, HubSetupRequest) (HubSetupState, error)
	ConnectHubSetup         func(context.Context) (HubSetupState, error)
	DisconnectHubSetup      func(context.Context) (HubSetupState, error)
	ResolveGitHubProfileURL func(context.Context) (string, error)
}

// AgentAuthState describes current runtime agent-auth readiness and device flow hints.
type AgentAuthState struct {
	Harness              string            `json:"harness,omitempty"`
	Required             bool              `json:"required"`
	Ready                bool              `json:"ready"`
	State                string            `json:"state,omitempty"`
	Message              string            `json:"message,omitempty"`
	AuthURL              string            `json:"auth_url,omitempty"`
	DeviceCode           string            `json:"device_code,omitempty"`
	AcceptsBrowserCode   bool              `json:"accepts_browser_code,omitempty"`
	ConfigureCommand     string            `json:"configure_command,omitempty"`
	ConfigurePlaceholder string            `json:"configure_placeholder,omitempty"`
	ConfigureOptions     []AgentAuthOption `json:"configure_options,omitempty"`
	UpdatedAt            string            `json:"updated_at,omitempty"`
}

type AgentAuthOption struct {
	Value       string `json:"value,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// HubSetupState describes whether Molten Hub is configured locally and what
// profile details should be reflected in config.json.
type HubSetupState struct {
	Configured bool   `json:"configured"`
	AgentMode  string `json:"agent_mode,omitempty"`
	TokenType  string `json:"token_type,omitempty"`
	Region     string `json:"region,omitempty"`
	Handle     string `json:"handle,omitempty"`
	Profile    struct {
		ProfileText string `json:"profile"`
		DisplayName string `json:"display_name"`
		Emoji       string `json:"emoji"`
	} `json:"profile"`
	ConnectURL       string         `json:"connect_url,omitempty"`
	DashboardURL     string         `json:"dashboard_url,omitempty"`
	Message          string         `json:"message,omitempty"`
	NeedsRestart     bool           `json:"needs_restart,omitempty"`
	Onboarding       []HubSetupStep `json:"onboarding,omitempty"`
	OnboardingActive bool           `json:"onboarding_active,omitempty"`
	OnboardingStage  string         `json:"onboarding_stage,omitempty"`
	ActivationReady  bool           `json:"activation_ready,omitempty"`
}

type HubSetupStep struct {
	ID     string `json:"id,omitempty"`
	Label  string `json:"label,omitempty"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ScheduledPrompt is the UI/API shape for one persisted prompt schedule.
type ScheduledPrompt struct {
	ID            string          `json:"id"`
	Name          string          `json:"name,omitempty"`
	EveryMinutes  int             `json:"every_minutes"`
	CreatedAt     string          `json:"created_at,omitempty"`
	NextRunAt     string          `json:"next_run_at,omitempty"`
	LastRunAt     string          `json:"last_run_at,omitempty"`
	LastRequestID string          `json:"last_request_id,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	Config        json.RawMessage `json:"config,omitempty"`
}

// ScheduledPromptCreateRequest captures a local schedule request.
type ScheduledPromptCreateRequest struct {
	Name         string          `json:"name"`
	EveryMinutes int             `json:"every_minutes"`
	Config       json.RawMessage `json:"config"`
}

// HubSetupRequest captures the late-stage Hub connect modal payload.
type HubSetupRequest struct {
	AgentMode string `json:"agent_mode"`
	TokenType string `json:"token_type"`
	Region    string `json:"region"`
	Token     string `json:"token"`
	Handle    string `json:"handle"`
	Profile   struct {
		ProfileText string `json:"profile"`
		DisplayName string `json:"display_name"`
		Emoji       string `json:"emoji"`
	} `json:"profile"`
}

// NewServer returns a monitor HTTP server.
func NewServer(addr string, broker *Broker) Server {
	return Server{
		Addr:   strings.TrimSpace(addr),
		Broker: broker,
		Logf:   func(string, ...any) {},
		LoadLibraryTasks: func() ([]library.TaskSummary, error) {
			catalog, err := library.LoadCatalog(library.DefaultDir)
			if err != nil {
				return nil, err
			}
			return catalog.Summaries(), nil
		},
	}
}

// Run serves the monitor UI until ctx is canceled.
func (s Server) Run(ctx context.Context) error {
	if strings.TrimSpace(s.Addr) == "" {
		return nil
	}
	if s.Broker == nil {
		return fmt.Errorf("broker is required")
	}
	if s.Logf == nil {
		s.Logf = func(string, ...any) {}
	}

	httpServer := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	s.logf("hub.ui status=starting listen=%s", s.Addr)
	err := httpServer.ListenAndServe()
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		s.logf("hub.ui status=stopped")
		return nil
	}
	return err
}

// Handler returns the HTTP handler for the monitor UI/API.
func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		s.logf("hub.ui status=warn event=load_static_files err=%q", err)
	} else {
		staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
		mux.Handle("/static/", withCacheControl(staticHandler, "public, max-age=3600"))
	}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/releases", s.handleReleases)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/library", s.handleLibrary)
	mux.HandleFunc("/api/library/run", s.handleLibraryRun)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/local-prompt", s.handleLocalPrompt)
	mux.HandleFunc("/api/schedules", s.handleSchedules)
	mux.HandleFunc("/api/schedules/", s.handleScheduleAction)
	mux.HandleFunc("/api/github/profile", s.handleGitHubProfile)
	mux.HandleFunc("/api/hub-setup", s.handleHubSetup)
	mux.HandleFunc("/api/hub-setup/connect", s.handleHubSetupConnect)
	mux.HandleFunc("/api/hub-setup/disconnect", s.handleHubSetupDisconnect)
	mux.HandleFunc("/api/agent-auth", s.handleAgentAuthStatus)
	mux.HandleFunc("/api/agent-auth/start-device", s.handleAgentAuthStart)
	mux.HandleFunc("/api/agent-auth/verify", s.handleAgentAuthVerify)
	mux.HandleFunc("/api/agent-auth/configure", s.handleAgentAuthConfigure)
	mux.HandleFunc("/api/tasks/", s.handleTaskAction)
	mux.HandleFunc("/healthz", s.handleHealth)
	return withGzip(mux)
}

func defaultAgentAuthState() AgentAuthState {
	return AgentAuthState{
		Required: false,
		Ready:    true,
		State:    "ready",
		Message:  "Agent auth is ready.",
	}
}

func defaultHubSetupState() HubSetupState {
	state := HubSetupState{
		Configured:      false,
		AgentMode:       "existing",
		TokenType:       "agent",
		Region:          "na",
		ConnectURL:      "https://app.molten.bot/signin?target=hub",
		DashboardURL:    "https://app.molten.bot/hub",
		Onboarding:      DefaultHubSetupOnboarding("existing"),
		OnboardingStage: "bind",
	}
	state.Profile.ProfileText = ""
	state.Profile.DisplayName = ""
	state.Profile.Emoji = ""
	return state
}

func DefaultHubSetupOnboarding(agentMode string) []HubSetupStep {
	steps := []HubSetupStep{
		{ID: "bind", Label: "Bind", Status: "pending"},
		{ID: "work_bind", Label: "Work", Status: "pending", Detail: "Resolve and verify Molten Hub credentials."},
		{ID: "profile_set", Label: "Profile Set", Status: "pending", Detail: "Persist the agent profile in Molten Hub."},
		{ID: "work_activate", Label: "Work", Status: "pending", Detail: "Apply the runtime transport and confirm activation."},
	}
	if strings.EqualFold(strings.TrimSpace(agentMode), "existing") {
		steps[0].Detail = "Verify the existing Molten Hub agent credential."
	} else {
		steps[0].Detail = "Exchange the bind token for an agent credential."
	}
	return steps
}

func (s Server) handleGitHubProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	profileURL, err := s.resolveGitHubProfileURL(r.Context())
	if err != nil {
		s.logf("hub.ui status=warn endpoint=github_profile err=%q", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"profileUrl": profileURL,
	})
}

func (s Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(staticFiles, "static/index.html")
	if err != nil {
		http.Error(w, "monitor ui is unavailable", http.StatusInternalServerError)
		return
	}
	data = s.injectIndexConfig(data)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/releases" {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(staticFiles, "static/releases.html")
	if err != nil {
		http.Error(w, "releases page is unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s Server) injectIndexConfig(data []byte) []byte {
	type indexConfig struct {
		AutomaticMode        bool     `json:"automaticMode"`
		ConfiguredHarness    string   `json:"configuredHarness"`
		ConfiguredAgentLabel string   `json:"configuredAgentLabel"`
		DefaultRepository    string   `json:"defaultRepository"`
		PromptImageHarnesses []string `json:"promptImageHarnesses"`
	}
	configuredHarness := strings.TrimSpace(s.ConfiguredHarness)
	configuredAgentLabel := ""
	if configuredHarness != "" {
		configuredAgentLabel = agentruntime.DisplayName(configuredHarness)
	}
	cfg, err := json.Marshal(indexConfig{
		AutomaticMode:        s.AutomaticMode,
		ConfiguredHarness:    configuredHarness,
		ConfiguredAgentLabel: configuredAgentLabel,
		DefaultRepository:    config.DefaultRepositoryURL,
		PromptImageHarnesses: agentruntime.SupportedPromptImageHarnesses(),
	})
	if err != nil {
		s.logf("hub.ui status=warn event=marshal_index_config err=%q", err)
		return data
	}

	return bytes.Replace(
		data,
		[]byte(`window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"","configuredAgentLabel":"","defaultRepository":"git@github.com:Molten-Bot/moltenhub-code.git","promptImageHarnesses":["codex","pi"]};`),
		[]byte("window.__HUB_UI_CONFIG__ = "+string(cfg)+";"),
		1,
	)
}

func (s Server) handleState(w http.ResponseWriter, _ *http.Request) {
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.snapshot())
}

func (s Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.LoadLibraryTasks == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"tasks": []library.TaskSummary{},
		})
		return
	}

	tasks, err := s.LoadLibraryTasks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("load library tasks: %v", err),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tasks": tasks,
	})
}

func (s Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	updates, cancel := s.Broker.Subscribe()
	defer cancel()
	lastSnapshotAt := time.Now()
	var snapshotTimer *time.Timer
	var snapshotTimerCh <-chan time.Time

	writeSSESnapshot := func() bool {
		payload, err := json.Marshal(compactStreamSnapshot(s.snapshot()))
		if err != nil {
			s.logf("hub.ui status=warn event=marshal_snapshot err=%q", err)
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		lastSnapshotAt = time.Now()
		return true
	}
	stopSnapshotTimer := func() {
		if snapshotTimer == nil {
			return
		}
		if !snapshotTimer.Stop() {
			select {
			case <-snapshotTimer.C:
			default:
			}
		}
		snapshotTimer = nil
		snapshotTimerCh = nil
	}
	scheduleSnapshot := func() {
		if snapshotTimer != nil {
			return
		}
		wait := streamSnapshotInterval - time.Since(lastSnapshotAt)
		if wait < 0 {
			wait = 0
		}
		snapshotTimer = time.NewTimer(wait)
		snapshotTimerCh = snapshotTimer.C
	}
	defer stopSnapshotTimer()

	if !writeSSESnapshot() {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			if time.Since(lastSnapshotAt) >= streamSnapshotInterval {
				stopSnapshotTimer()
				if !writeSSESnapshot() {
					return
				}
				continue
			}
			scheduleSnapshot()
		case <-snapshotTimerCh:
			stopSnapshotTimer()
			if !writeSSESnapshot() {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s Server) snapshot() Snapshot {
	if s.Broker == nil {
		return Snapshot{}
	}

	snapshot := s.Broker.Snapshot()
	if s.ResolveTaskControls == nil {
		return snapshot
	}

	for i := range snapshot.Tasks {
		snapshot.Tasks[i].Controls = s.ResolveTaskControls(snapshot.Tasks[i].RequestID)
	}
	return snapshot
}

func (s Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s Server) currentHubSetupState(ctx context.Context) (HubSetupState, error) {
	state := defaultHubSetupState()
	if s.HubSetupStatus == nil {
		return state, nil
	}
	next, err := s.HubSetupStatus(ctx)
	if strings.TrimSpace(next.ConnectURL) == "" {
		next.ConnectURL = state.ConnectURL
	}
	if strings.TrimSpace(next.DashboardURL) == "" {
		next.DashboardURL = state.DashboardURL
	}
	if strings.TrimSpace(next.AgentMode) == "" {
		next.AgentMode = state.AgentMode
	}
	if strings.TrimSpace(next.TokenType) == "" {
		next.TokenType = state.TokenType
	}
	return next, err
}

func (s Server) handleHubSetup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := s.currentHubSetupState(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("load hub setup state: %v", err),
				"hub":   state,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":  true,
			"hub": state,
		})
	case http.MethodPost:
		if s.ConfigureHubSetup == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"ok":    false,
				"error": "hub setup is unavailable",
				"hub":   defaultHubSetupState(),
			})
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxHubSetupConfigureBodyBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("read request body: %v", err),
				"hub":   defaultHubSetupState(),
			})
			return
		}

		var req HubSetupRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("decode request body: %v", err),
				"hub":   defaultHubSetupState(),
			})
			return
		}

		state, err := s.ConfigureHubSetup(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
				"hub":   state,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":  true,
			"hub": state,
		})
	default:
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodPost}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s Server) handleHubSetupConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.ConnectHubSetup == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "hub connect is unavailable",
			"hub":   defaultHubSetupState(),
		})
		return
	}

	state, err := s.ConnectHubSetup(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hub":   state,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"hub": state,
	})
}

func (s Server) handleHubSetupDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.DisconnectHubSetup == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "hub disconnect is unavailable",
			"hub":   defaultHubSetupState(),
		})
		return
	}

	state, err := s.DisconnectHubSetup(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hub":   state,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"hub": state,
	})
}

func (s Server) handleAgentAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state, err := s.currentAgentAuthState(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("load agent auth state: %v", err),
			"auth":  state,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

func (s Server) handleAgentAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.StartAgentAuth == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "agent device auth is unavailable",
			"auth":  defaultAgentAuthState(),
		})
		return
	}
	state, err := s.StartAgentAuth(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"auth":  state,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

func (s Server) handleAgentAuthVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.logf("hub.ui status=start endpoint=agent_auth_verify")
	if s.VerifyAgentAuth == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "agent auth verification is unavailable",
			"auth":  defaultAgentAuthState(),
		})
		return
	}
	state, err := s.VerifyAgentAuth(r.Context())
	if err != nil {
		s.logf("hub.ui status=error endpoint=agent_auth_verify state=%s err=%q", strings.TrimSpace(state.State), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"auth":  state,
		})
		return
	}
	s.logf("hub.ui status=ok endpoint=agent_auth_verify state=%s ready=%t", strings.TrimSpace(state.State), state.Ready)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

type agentAuthConfigureRequest struct {
	AugmentSessionAuth      string `json:"augment_session_auth"`
	AugmentSessionAuthAlias string `json:"augmentSessionAuth"`
	PiAuthJSON              string `json:"pi_auth_json"`
	PiAuthJSONAlias         string `json:"piAuthJSON"`
	PiProviderAuth          string `json:"pi_provider_auth"`
	PiProviderAuthAlias     string `json:"piProviderAuth"`
	SessionAuth             string `json:"session_auth"`
	SessionAuthAlias        string `json:"sessionAuth"`
	GitHubToken             string `json:"github_token"`
	GitHubTokenAlias        string `json:"githubToken"`
	ClaudeAuthCode          string `json:"claude_auth_code"`
	ClaudeAuthCodeAlias     string `json:"claudeAuthCode"`
	Value                   string `json:"value"`
}

func (s Server) handleAgentAuthConfigure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.logf("hub.ui status=start endpoint=agent_auth_configure")
	if s.ConfigureAgentAuth == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "agent auth configure is unavailable",
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxAgentAuthConfigureBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("read request body: %v", err),
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	var req agentAuthConfigureRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("decode request body: %v", err),
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	sessionAuth := firstNonEmptyString(
		req.AugmentSessionAuth,
		req.AugmentSessionAuthAlias,
		req.PiAuthJSON,
		req.PiAuthJSONAlias,
		req.PiProviderAuth,
		req.PiProviderAuthAlias,
		req.SessionAuth,
		req.SessionAuthAlias,
		req.GitHubToken,
		req.GitHubTokenAlias,
		req.ClaudeAuthCode,
		req.ClaudeAuthCodeAlias,
		req.Value,
	)
	if sessionAuth == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "configure value is required",
			"auth":  defaultAgentAuthState(),
		})
		return
	}

	state, err := s.ConfigureAgentAuth(r.Context(), sessionAuth)
	if err != nil {
		s.logf("hub.ui status=error endpoint=agent_auth_configure state=%s err=%q", strings.TrimSpace(state.State), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"auth":  state,
		})
		return
	}
	s.logf("hub.ui status=ok endpoint=agent_auth_configure state=%s ready=%t", strings.TrimSpace(state.State), state.Ready)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"auth": state,
	})
}

func (s Server) currentAgentAuthState(ctx context.Context) (AgentAuthState, error) {
	if s.AgentAuthStatus == nil {
		return defaultAgentAuthState(), nil
	}
	state, err := s.AgentAuthStatus(ctx)
	if strings.TrimSpace(state.State) == "" {
		if state.Ready {
			state.State = "ready"
		} else {
			state.State = "needs_device_auth"
		}
	}
	return state, err
}

func (s Server) resolveGitHubProfileURL(ctx context.Context) (string, error) {
	if s.ResolveGitHubProfileURL != nil {
		return s.ResolveGitHubProfileURL(ctx)
	}
	return resolveAuthenticatedGitHubProfileURL(ctx, http.DefaultClient)
}

func resolveAuthenticatedGitHubProfileURL(ctx context.Context, client *http.Client) (string, error) {
	token := strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		return "", fmt.Errorf("github token is not configured")
	}
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", fmt.Errorf("build github profile request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "moltenhub-code")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("load github profile: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Login   string `json:"login"`
		HTMLURL string `json:"html_url"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("decode github profile: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		message := strings.TrimSpace(body.Message)
		if message == "" {
			message = fmt.Sprintf("github api status=%d", resp.StatusCode)
		}
		return "", fmt.Errorf("github profile lookup failed: %s", message)
	}
	if profileURL := strings.TrimSpace(body.HTMLURL); profileURL != "" {
		return profileURL, nil
	}
	if login := strings.TrimSpace(body.Login); login != "" {
		return "https://github.com/" + login, nil
	}
	return "", fmt.Errorf("github profile lookup failed: missing profile url")
}

func (s Server) handleLocalPrompt(w http.ResponseWriter, r *http.Request) {
	s.handlePromptSubmit(w, r, s.SubmitLocalPrompt, "studio submit is unavailable")
}

func (s Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.ListScheduledPrompts == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":        true,
				"schedules": []ScheduledPrompt{},
			})
			return
		}
		schedules, err := s.ListScheduledPrompts(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("load schedules: %v", err),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"schedules": schedules,
		})
	case http.MethodPost:
		if s.CreateScheduledPrompt == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"ok":    false,
				"error": "scheduled prompts are unavailable",
			})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalPromptBodyBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("read request body: %v", err),
			})
			return
		}
		var req ScheduledPromptCreateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("decode request body: %v", err),
			})
			return
		}
		schedule, err := s.CreateScheduledPrompt(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"ok":       true,
			"schedule": schedule,
		})
	default:
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodPost}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s Server) handleScheduleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.DeleteScheduledPrompt == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "scheduled prompts are unavailable",
		})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/schedules/")
	if id == r.URL.Path || id == "" {
		http.NotFound(w, r)
		return
	}
	decoded, err := url.PathUnescape(strings.Trim(id, "/"))
	if err != nil || strings.TrimSpace(decoded) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "schedule id is required",
		})
		return
	}
	if err := s.DeleteScheduledPrompt(r.Context(), strings.TrimSpace(decoded)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s Server) handleLibraryRun(w http.ResponseWriter, r *http.Request) {
	s.handlePromptSubmit(w, r, s.SubmitLocalPrompt, "library task submit is unavailable")
}

func (s Server) handlePromptSubmit(
	w http.ResponseWriter,
	r *http.Request,
	submit func(context.Context, []byte) (string, error),
	unavailableMessage string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if submit == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": unavailableMessage,
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalPromptBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("read request body: %v", err),
		})
		return
	}

	requestID, err := submit(r.Context(), body)
	if err != nil {
		if duplicateRequestID, duplicateState, ok := duplicateSubmissionDetails(err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":         false,
				"error":      err.Error(),
				"duplicate":  true,
				"request_id": duplicateRequestID,
				"state":      duplicateState,
			})
			return
		}
		if s.Broker != nil {
			s.Broker.RecordRejectedPromptSubmission(body, "invalid", err)
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"request_id": requestID,
	})
}

func (s Server) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if path == r.URL.Path || path == "" {
		http.NotFound(w, r)
		return
	}
	action := ""
	switch {
	case strings.HasSuffix(path, "/rerun"):
		action = "rerun"
	case strings.HasSuffix(path, "/close"):
		action = "close"
	case strings.HasSuffix(path, "/pause"):
		action = "pause"
	case strings.HasSuffix(path, "/run"):
		action = "run"
	case strings.HasSuffix(path, "/stop"):
		action = "stop"
	default:
		http.NotFound(w, r)
		return
	}

	requestID := strings.TrimSuffix(path, "/"+action)
	requestID = strings.TrimSuffix(requestID, "/")
	decoded, err := url.PathUnescape(requestID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "invalid request id",
		})
		return
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "request id is required",
		})
		return
	}

	switch action {
	case "rerun":
		s.handleTaskRerun(w, r, decoded)
	case "close":
		s.handleTaskClose(w, r, decoded)
	case "pause":
		s.handleTaskControl(w, r, decoded, "pause", "paused", s.PauseTask)
	case "run":
		runHandler := s.RunTask
		force := parseTruthyQueryParam(r.URL.Query().Get("force"))
		if force && s.ForceRunTask != nil {
			runHandler = s.ForceRunTask
		}
		s.handleTaskControlWithMeta(
			w,
			r,
			decoded,
			"run",
			"running",
			runHandler,
			map[string]any{
				"forced": force,
			},
		)
	case "stop":
		s.handleTaskControl(w, r, decoded, "stop", "stopped", s.StopTask)
	default:
		http.NotFound(w, r)
	}
}

func (s Server) handleTaskControl(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	action string,
	status string,
	handler func(context.Context, string) error,
) {
	s.handleTaskControlWithMeta(w, r, requestID, action, status, handler, nil)
}

func (s Server) handleTaskControlWithMeta(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	action string,
	status string,
	handler func(context.Context, string) error,
	meta map[string]any,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("task %s is unavailable", action),
		})
		return
	}
	if err := handler(r.Context(), requestID); err != nil {
		switch {
		case errors.Is(err, ErrTaskNotFound):
			statusCode, errText := s.taskControlUnavailableResponse(requestID, action)
			if statusCode != 0 {
				writeJSON(w, statusCode, map[string]any{
					"ok":    false,
					"error": errText,
				})
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]any{
				"ok":    false,
				"error": "task not found",
			})
		default:
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		}
		return
	}
	response := map[string]any{
		"ok":         true,
		"request_id": requestID,
		"action":     action,
		"status":     status,
	}
	for key, value := range meta {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
}

func (s Server) taskControlUnavailableResponse(requestID string, action string) (int, string) {
	if s.Broker == nil {
		return 0, ""
	}

	task, ok := s.Broker.Task(requestID)
	if !ok {
		return 0, ""
	}

	switch normalizeTaskTerminalStatus(task.Status) {
	case "completed", "no_changes", "duplicate":
		return http.StatusConflict, "task is already completed"
	case "stopped":
		return http.StatusConflict, "task is already stopped"
	case "error", "invalid":
		return http.StatusConflict, "task is already finished"
	}

	action = strings.TrimSpace(action)
	if action == "" {
		action = "control"
	}
	return http.StatusConflict, fmt.Sprintf("task %s is unavailable for this task", action)
}

func (s Server) handleTaskRerun(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.SubmitLocalPrompt == nil && s.SubmitTaskRerun == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "task rerun is unavailable",
		})
		return
	}

	runConfigJSON, ok := s.Broker.TaskRunConfig(requestID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":    false,
			"error": "run config for task is unavailable",
		})
		return
	}

	force := parseTruthyQueryParam(r.URL.Query().Get("force"))

	submit := func(ctx context.Context, body []byte) (string, error) {
		return s.SubmitLocalPrompt(ctx, body)
	}
	if s.SubmitTaskRerun != nil {
		submit = func(ctx context.Context, body []byte) (string, error) {
			return s.SubmitTaskRerun(ctx, requestID, body, force)
		}
	}

	sourceTask, sourceTaskFound := s.Broker.Task(requestID)
	newRequestID, err := submit(r.Context(), runConfigJSON)
	if err != nil {
		if duplicateRequestID, duplicateState, ok := duplicateSubmissionDetails(err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":           false,
				"error":        err.Error(),
				"duplicate":    true,
				"request_id":   duplicateRequestID,
				"state":        duplicateState,
				"duplicate_of": requestID,
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	s.Broker.RecordTaskRerunAttempt(requestID, newRequestID)
	if sourceTaskFound && isCompletedTaskStatus(sourceTask.Status) {
		if err := s.Broker.CloseTask(requestID); err != nil {
			s.logf("hub.ui status=warn event=task_rerun_close_source request_id=%s rerun_request_id=%s err=%q", requestID, newRequestID, err)
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"forced":     force,
		"request_id": newRequestID,
		"rerun_of":   requestID,
	})
}

func (s Server) handleTaskClose(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}

	if err := s.Broker.CloseTask(requestID); err != nil {
		switch {
		case errors.Is(err, ErrTaskNotFound):
			writeJSON(w, http.StatusNotFound, map[string]any{
				"ok":    false,
				"error": "task not found",
			})
		case errors.Is(err, ErrTaskNotCompleted):
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":    false,
				"error": "task is not completed",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		}
		return
	}

	if s.CloseTask != nil {
		if err := s.CloseTask(r.Context(), requestID); err != nil {
			s.logf("hub.ui status=warn event=task_close_cleanup request_id=%s err=%q", requestID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"request_id": requestID,
		"closed":     true,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func compactStreamSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Events = nil
	for i := range snapshot.Tasks {
		logs := snapshot.Tasks[i].Logs
		if len(logs) <= maxStreamTaskLogs {
			continue
		}
		snapshot.Tasks[i].Logs = logs[len(logs)-maxStreamTaskLogs:]
	}
	return snapshot
}

func withCacheControl(next http.Handler, cacheControl string) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	cacheControl = strings.TrimSpace(cacheControl)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cacheControl != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Cache-Control", cacheControl)
		}
		next.ServeHTTP(w, r)
	})
}

func withGzip(next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestWantsGzip(r) || !isCompressiblePath(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/api/stream") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzip.NewWriter(w)
		defer func() {
			_ = gz.Close()
		}()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, writer: gz}, r)
	})
}

func requestWantsGzip(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

func isCompressiblePath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if path == "/" || strings.HasPrefix(path, "/api/") {
		return true
	}
	return strings.HasSuffix(path, ".css") ||
		strings.HasSuffix(path, ".js") ||
		strings.HasSuffix(path, ".html") ||
		strings.HasSuffix(path, ".svg") ||
		strings.HasSuffix(path, ".json")
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	headers := w.ResponseWriter.Header()
	if strings.TrimSpace(headers.Get("Content-Encoding")) == "" {
		headers.Set("Content-Encoding", "gzip")
	}
	addVaryHeader(headers, "Accept-Encoding")
	headers.Del("Content-Length")
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *gzipResponseWriter) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.writer.Write(payload)
}

func (w *gzipResponseWriter) Flush() {
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func addVaryHeader(header http.Header, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	existing := header.Values("Vary")
	for _, current := range existing {
		for _, token := range strings.Split(current, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func (s Server) logf(format string, args ...any) {
	if s.Logf == nil {
		return
	}
	s.Logf(format, args...)
}

type duplicateSubmission interface {
	error
	DuplicateRequestID() string
	DuplicateState() string
}

func duplicateSubmissionDetails(err error) (requestID string, state string, ok bool) {
	if err == nil {
		return "", "", false
	}
	var duplicateErr duplicateSubmission
	if !errors.As(err, &duplicateErr) {
		return "", "", false
	}
	return strings.TrimSpace(duplicateErr.DuplicateRequestID()), strings.TrimSpace(duplicateErr.DuplicateState()), true
}

func parseTruthyQueryParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
