package hubui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

// Server provides an HTTP UI for live hub/task monitoring.
type Server struct {
	Addr   string
	Broker *Broker
	Logf   func(string, ...any)
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
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/stream", s.handleStream)
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
