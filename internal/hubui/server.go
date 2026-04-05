package hubui

import (
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
)

//go:embed static/*
var staticFiles embed.FS

// Server provides an HTTP UI for live hub/task monitoring.
type Server struct {
	Addr              string
	Broker            *Broker
	Logf              func(string, ...any)
	SubmitLocalPrompt func(context.Context, []byte) (string, error)
	CloseTask         func(context.Context, string) error
}

// NewServer returns a monitor HTTP server.
func NewServer(addr string, broker *Broker) Server {
	return Server{
		Addr:   strings.TrimSpace(addr),
		Broker: broker,
		Logf:   func(string, ...any) {},
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
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/local-prompt", s.handleLocalPrompt)
	mux.HandleFunc("/api/tasks/", s.handleTaskAction)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s Server) handleState(w http.ResponseWriter, _ *http.Request) {
	if s.Broker == nil {
		http.Error(w, "monitor broker is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.Broker.Snapshot())
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

func (s Server) handleLocalPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.SubmitLocalPrompt == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":    false,
			"error": "local prompt submit is unavailable",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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
	default:
		http.NotFound(w, r)
	}
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
	if s.SubmitLocalPrompt == nil {
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

	newRequestID, err := s.SubmitLocalPrompt(r.Context(), runConfigJSON)
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
