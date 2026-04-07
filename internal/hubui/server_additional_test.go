package hubui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/library"
)

func TestNewServerDefaultsAndLogfHelper(t *testing.T) {
	t.Parallel()

	srv := NewServer(" 127.0.0.1:7777 ", NewBroker())
	if got, want := srv.Addr, "127.0.0.1:7777"; got != want {
		t.Fatalf("NewServer().Addr = %q, want %q", got, want)
	}
	if srv.Broker == nil {
		t.Fatal("NewServer().Broker = nil, want non-nil")
	}
	if srv.Logf == nil {
		t.Fatal("NewServer().Logf = nil, want non-nil")
	}
	if srv.LoadLibraryTasks == nil {
		t.Fatal("NewServer().LoadLibraryTasks = nil, want non-nil")
	}

	var lines []string
	srv.Logf = func(format string, args ...any) {
		lines = append(lines, format)
	}
	srv.logf("hub.ui status=ok")
	if len(lines) != 1 {
		t.Fatalf("logf() line count = %d, want 1", len(lines))
	}
}

func TestServerRunValidationAndShutdownPaths(t *testing.T) {
	t.Parallel()

	if err := (Server{Addr: ""}).Run(context.Background()); err != nil {
		t.Fatalf("Run(empty addr) error = %v, want nil", err)
	}

	if err := (Server{Addr: "127.0.0.1:0"}).Run(context.Background()); err == nil {
		t.Fatal("Run(nil broker) error = nil, want non-nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer("127.0.0.1:0", NewBroker())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run(cancel) error = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run(cancel) did not stop in time")
	}
}

func TestHealthEndpointAndWriteJSONMarshalFailure(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), `"ok":true`) {
		t.Fatalf("GET /healthz body = %q, want ok=true JSON", resp.Body.String())
	}

	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"bad": func() {}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("writeJSON(marshal failure) status = %d, want 500", rec.Code)
	}
}

func TestDuplicateSubmissionDetailsNonMatchAndNil(t *testing.T) {
	t.Parallel()

	if _, _, ok := duplicateSubmissionDetails(nil); ok {
		t.Fatal("duplicateSubmissionDetails(nil) ok = true, want false")
	}
	if _, _, ok := duplicateSubmissionDetails(errors.New("plain error")); ok {
		t.Fatal("duplicateSubmissionDetails(non duplicate error) ok = true, want false")
	}
}

func TestLibraryEndpointMethodAndLoaderVariants(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())

	req := httptest.NewRequest(http.MethodPost, "/api/library", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/library status = %d, want 405", resp.Code)
	}

	srv.LoadLibraryTasks = nil
	req = httptest.NewRequest(http.MethodGet, "/api/library", nil)
	resp = httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/library (nil loader) status = %d, want 200", resp.Code)
	}

	srv.LoadLibraryTasks = func() ([]library.TaskSummary, error) {
		return nil, errors.New("catalog missing")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/library", nil)
	resp = httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/library (loader error) status = %d, want 500", resp.Code)
	}
}
