package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/hub"
)

func TestStringListFlagSetAndString(t *testing.T) {
	t.Parallel()

	var values stringListFlag
	if err := values.Set("a.json"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := values.Set("b.json"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got, want := values.String(), "a.json,b.json"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestWriteStdoutAndStderrLineCaptureToSink(t *testing.T) {
	t.Parallel()

	sink := &recordingTerminalLogSink{}
	var out strings.Builder
	logger := newTerminalLogger(&out, false)
	logger.sink = sink

	writeStdoutLine(logger, "  status=ok  ")
	writeStderrLine(logger, "  error: boom  ")
	writeStdoutLine(logger, "   ")
	writeStderrLine(logger, "")

	if got, want := len(sink.lines), 2; got != want {
		t.Fatalf("len(sink.lines) = %d, want %d (%v)", got, want, sink.lines)
	}
	if got, want := sink.lines[0], "status=ok"; got != want {
		t.Fatalf("sink.lines[0] = %q, want %q", got, want)
	}
	if got, want := sink.lines[1], "error: boom"; got != want {
		t.Fatalf("sink.lines[1] = %q, want %q", got, want)
	}
}

func TestHubExitCodeMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want int
	}{
		{err: fmt.Errorf("init config: invalid"), want: harness.ExitConfig},
		{err: fmt.Errorf("hub auth: invalid token"), want: harness.ExitAuth},
		{err: fmt.Errorf("hub profile: invalid handle"), want: harness.ExitAuth},
		{err: fmt.Errorf("hub websocket url: malformed"), want: harness.ExitConfig},
		{err: fmt.Errorf("something else"), want: harness.ExitPreflight},
	}
	for _, tt := range tests {
		if got := hubExitCode(tt.err); got != tt.want {
			t.Fatalf("hubExitCode(%q) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

func TestJoinPRURLsAndCountChangedRepos(t *testing.T) {
	t.Parallel()

	results := []harness.RepoResult{
		{Changed: true, PRURL: " https://github.com/acme/repo-a/pull/1 "},
		{Changed: false, PRURL: "https://github.com/acme/repo-b/pull/2"},
		{Changed: true, PRURL: ""},
		{Changed: true, PRURL: "https://github.com/acme/repo-c/pull/3"},
	}
	if got, want := joinPRURLs(results), "https://github.com/acme/repo-a/pull/1,https://github.com/acme/repo-c/pull/3"; got != want {
		t.Fatalf("joinPRURLs() = %q, want %q", got, want)
	}
	if got, want := countChangedRepos(results), 3; got != want {
		t.Fatalf("countChangedRepos() = %d, want %d", got, want)
	}
}

func TestMarshalRunConfigJSONReturnsJSONPayload(t *testing.T) {
	t.Parallel()

	payload, ok := marshalRunConfigJSON(config.Config{
		RepoURL: "git@github.com:acme/repo.git",
		Prompt:  "fix tests",
	})
	if !ok {
		t.Fatal("marshalRunConfigJSON() ok = false, want true")
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := decoded["repoUrl"], "git@github.com:acme/repo.git"; got != want {
		t.Fatalf("repoUrl = %v, want %q", got, want)
	}
}

func TestFailureFollowUpPromptDefaultWhenNoPaths(t *testing.T) {
	t.Parallel()

	got := failureFollowUpPrompt(nil)
	if !strings.Contains(got, failureFollowUpRequiredPrompt) {
		t.Fatalf("prompt missing required instructions: %q", got)
	}
	if !strings.Contains(got, ".log/local/<request timestamp>/<request sequence>/terminal.log") {
		t.Fatalf("prompt missing default log path hint: %q", got)
	}
}

func TestTaskLogDirAndTaskLogPathsValidateInputs(t *testing.T) {
	t.Parallel()

	if got, ok := taskLogDir("", "req-1"); ok || got != "" {
		t.Fatalf("taskLogDir(empty root) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := taskLogDir("/tmp/.log", ""); ok || got != "" {
		t.Fatalf("taskLogDir(empty request) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got := taskLogPaths("", "req-1"); got != nil {
		t.Fatalf("taskLogPaths(empty root) = %v, want nil", got)
	}
}

func TestHubPingURLValidationAndCheckHubPingFailures(t *testing.T) {
	t.Parallel()

	if _, err := hubPingURL("ftp://example.com/v1"); err == nil {
		t.Fatal("hubPingURL(ftp) error = nil, want non-nil")
	}
	if _, err := hubPingURL("https:///v1"); err == nil {
		t.Fatal("hubPingURL(missing host) error = nil, want non-nil")
	}

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 200), http.StatusServiceUnavailable)
	}))
	defer pingServer.Close()

	if _, err := checkHubPing(context.Background(), pingServer.URL); err == nil {
		t.Fatal("checkHubPing(non-2xx) error = nil, want non-nil")
	}
}

func TestRunHubBootDiagnosticsWithRuntimeLoaderRejectsNilDeps(t *testing.T) {
	t.Parallel()

	ok := runHubBootDiagnosticsWithRuntimeLoader(context.Background(), nil, nil, hub.InitConfig{}, nil)
	if ok {
		t.Fatal("runHubBootDiagnosticsWithRuntimeLoader(nil,nil,...) = true, want false")
	}
}

func TestRunHubBootDiagnosticsWrapper(t *testing.T) {
	t.Parallel()

	pingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer pingServer.Close()

	runner := &stubExecRunner{
		results: map[string]stubExecResult{
			stubCommandKey(execx.Command{Name: "git", Args: []string{"--version"}}): {result: execx.Result{Stdout: "git version"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"--version"}}):  {result: execx.Result{Stdout: "gh version"}},
			stubCommandKey(execx.Command{Name: "codex", Args: []string{"--help"}}):  {result: execx.Result{Stdout: "codex help"}},
			stubCommandKey(execx.Command{Name: "gh", Args: []string{"auth", "status"}}): {
				err: errors.New("not authenticated"),
			},
		},
	}

	var logs []string
	ok := runHubBootDiagnostics(
		context.Background(),
		runner,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		hub.InitConfig{BaseURL: pingServer.URL + "/v1"},
	)
	if !ok {
		t.Fatal("runHubBootDiagnostics() = false, want true")
	}
	assertLogContains(t, logs, "boot.diagnosis status=ok requirement=git_cli")
}

func TestRunLocalDispatchReportsErrorState(t *testing.T) {
	t.Parallel()

	var logs []string
	outcome := runLocalDispatch(
		context.Background(),
		nil,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		"code_for_me",
		"req-1",
		config.Config{
			RepoURL: "git@github.com:acme/repo.git",
			Prompt:  "fix tests",
		},
		nil,
	)

	if outcome.State != "error" {
		t.Fatalf("runLocalDispatch() state = %q, want error", outcome.State)
	}
	if outcome.Result.Err == nil {
		t.Fatal("runLocalDispatch() result error = nil, want non-nil")
	}
	assertLogContains(t, logs, "dispatch status=error request_id=req-1")
}
