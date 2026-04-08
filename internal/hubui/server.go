package hubui

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/library"
)

//go:embed static/*
var staticFiles embed.FS

const maxLocalPromptBodyBytes = 16 << 20
const maxAgentAuthConfigureBodyBytes = 1 << 20

// Server provides an HTTP UI for live hub/task monitoring.
type Server struct {
	Addr               string
	Broker             *Broker
	AutomaticMode      bool
	ConfiguredHarness  string
	Logf               func(string, ...any)
	SubmitLocalPrompt  func(context.Context, []byte) (string, error)
	SubmitTaskRerun    func(context.Context, string, []byte, bool) (string, error)
	CloseTask          func(context.Context, string) error
	PauseTask          func(context.Context, string) error
	RunTask            func(context.Context, string) error
	StopTask           func(context.Context, string) error
	LoadLibraryTasks   func() ([]library.TaskSummary, error)
	AgentAuthStatus    func(context.Context) (AgentAuthState, error)
	StartAgentAuth     func(context.Context) (AgentAuthState, error)
	VerifyAgentAuth    func(context.Context) (AgentAuthState, error)
	ConfigureAgentAuth func(context.Context, string) (AgentAuthState, error)
}

// AgentAuthState describes current runtime agent-auth readiness and device flow hints.
type AgentAuthState struct {
	Harness              string `json:"harness,omitempty"`
	Required             bool   `json:"required"`
	Ready                bool   `json:"ready"`
	State                string `json:"state,omitempty"`
	Message              string `json:"message,omitempty"`
	AuthURL              string `json:"auth_url,omitempty"`
	DeviceCode           string `json:"device_code,omitempty"`
	ConfigureCommand     string `json:"configure_command,omitempty"`
	ConfigurePlaceholder string `json:"configure_placeholder,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
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
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/library", s.handleLibrary)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/local-prompt", s.handleLocalPrompt)
	mux.HandleFunc("/api/agent-auth", s.handleAgentAuthStatus)
	mux.HandleFunc("/api/agent-auth/start-device", s.handleAgentAuthStart)
	mux.HandleFunc("/api/agent-auth/verify", s.handleAgentAuthVerify)
	mux.HandleFunc("/api/agent-auth/configure", s.handleAgentAuthConfigure)
	mux.HandleFunc("/api/tasks/", s.handleTaskAction)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

func defaultAgentAuthState() AgentAuthState {
	return AgentAuthState{
		Required: false,
		Ready:    true,
		State:    "ready",
		Message:  "Agent auth is ready.",
	}
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
	_, _ = w.Write(data)
}

func (s Server) injectIndexConfig(data []byte) []byte {
	type indexConfig struct {
		AutomaticMode        bool   `json:"automaticMode"`
		ConfiguredHarness    string `json:"configuredHarness"`
		ConfiguredAgentLabel string `json:"configuredAgentLabel"`
	}
	cfg, err := json.Marshal(indexConfig{
		AutomaticMode:        s.AutomaticMode,
		ConfiguredHarness:    strings.TrimSpace(s.ConfiguredHarness),
		ConfiguredAgentLabel: agentruntime.DisplayName(s.ConfiguredHarness),
	})
	if err != nil {
		s.logf("hub.ui status=warn event=marshal_index_config err=%q", err)
		return data
	}

	return bytes.Replace(
		data,
		[]byte(`window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"codex","configuredAgentLabel":"Codex"};`),
		[]byte("window.__HUB_UI_CONFIG__ = "+string(cfg)+";"),
		1,
	)
}

func (s Server) handleState(w http.ResponseWriter, _ *http.Request) {
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.Broker.Snapshot())
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

	writeSSESnapshot := func() bool {
		payload, err := json.Marshal(s.Broker.Snapshot())
		if err != nil {
			s.logf("hub.ui status=warn event=marshal_snapshot err=%q", err)
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

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

func (s Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

type agentAuthConfigureRequest struct {
	AugmentSessionAuth      string `json:"augment_session_auth"`
	AugmentSessionAuthAlias string `json:"augmentSessionAuth"`
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

func (s Server) handleLocalPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.SubmitLocalPrompt == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "studio submit is unavailable",
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

	requestID, err := s.SubmitLocalPrompt(r.Context(), body)
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
		s.handleTaskControl(w, r, decoded, "run", "running", s.RunTask)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"request_id": requestID,
		"action":     action,
		"status":     status,
	})
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
