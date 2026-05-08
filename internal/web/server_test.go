package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
)

type duplicateSubmissionStubError struct {
	requestID string
	state     string
}

func (e duplicateSubmissionStubError) Error() string {
	return "duplicate submission ignored"
}

func (e duplicateSubmissionStubError) DuplicateRequestID() string {
	return e.requestID
}

func (e duplicateSubmissionStubError) DuplicateState() string {
	return e.state
}

func TestHandlerStateEndpointReturnsSnapshot(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-1")

	srv := NewServer("", b)
	srv.ResolveTaskControls = func(requestID string) TaskControls {
		if requestID == "req-1" {
			return TaskControls{Stop: true}
		}
		return TaskControls{}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	statusResp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status error = %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("/api/status status = %d", statusResp.StatusCode)
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snap.Tasks))
	}
	if snap.Tasks[0].RequestID != "req-1" {
		t.Fatalf("request id = %q", snap.Tasks[0].RequestID)
	}
	if !snap.Tasks[0].Controls.Stop {
		t.Fatalf("controls.stop = false, want true")
	}
}

func TestHandlerStateEndpointReturnsDashboardStats(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	b := NewBroker()
	b.sessionStartedAt = now.Add(-30 * time.Second)
	b.now = func() time.Time { return now }
	b.IngestLog("dispatch status=start request_id=req-1")
	now = now.Add(10 * time.Second)
	b.IngestLog("dispatch status=start request_id=req-2")
	now = now.Add(20 * time.Second)
	b.IngestLog("dispatch status=completed request_id=req-1")

	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state error = %v", err)
	}
	defer resp.Body.Close()

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Stats.TotalTasks != 2 {
		t.Fatalf("stats.total_tasks = %d, want 2", snap.Stats.TotalTasks)
	}
	if snap.Stats.ActiveTasks != 1 {
		t.Fatalf("stats.active_tasks = %d, want 1", snap.Stats.ActiveTasks)
	}
	if snap.Stats.CompletedTasks != 1 {
		t.Fatalf("stats.completed_tasks = %d, want 1", snap.Stats.CompletedTasks)
	}
	if snap.Stats.MaxConcurrentTasks != 2 {
		t.Fatalf("stats.max_concurrent_tasks = %d, want 2", snap.Stats.MaxConcurrentTasks)
	}
	if snap.Stats.SessionRuntimeSeconds != 60 {
		t.Fatalf("stats.session_runtime_seconds = %f, want 60", snap.Stats.SessionRuntimeSeconds)
	}
	if snap.Stats.ThroughputPerHour <= 0 {
		t.Fatalf("stats.throughput_per_hour = %f, want positive", snap.Stats.ThroughputPerHour)
	}
	if snap.Stats.VelocityPerHour <= snap.Stats.ThroughputPerHour {
		t.Fatalf("stats.velocity_per_hour = %f, throughput = %f, want velocity above throughput", snap.Stats.VelocityPerHour, snap.Stats.ThroughputPerHour)
	}
}

func TestHandlerLibraryEndpointReturnsTasks(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.LoadLibraryTasks = func() ([]library.TaskSummary, error) {
		return []library.TaskSummary{
			{Name: "security-review", DisplayName: "Security Review", Prompt: "Review the repository."},
			{Name: "unit-test-coverage", DisplayName: "100% Unit Test Coverage", Prompt: "Raise coverage."},
		}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	var body struct {
		OK    bool                  `json:"ok"`
		Tasks []library.TaskSummary `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.OK {
		t.Fatalf("ok = false")
	}
	if got, want := len(body.Tasks), 2; got != want {
		t.Fatalf("len(tasks) = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].Prompt, "Review the repository."; got != want {
		t.Fatalf("tasks[0].prompt = %q, want %q", got, want)
	}
}

func TestHandlerStreamEndpointEmitsInitialSnapshot(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-stream")

	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatalf("GET /api/stream error = %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read stream line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first line = %q", line)
	}
}

func TestHandlerIndexServesHTML(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}

	markup := resp.Body.String()
	if !strings.Contains(markup, `href="/static/style.css"`) {
		t.Fatalf("expected index html to include external stylesheet link")
	}
	if strings.Contains(markup, `tailwind`) || strings.Contains(markup, `cdn.tailwindcss.com`) {
		t.Fatalf("expected index html styling to be owned by the global stylesheet instead of the Tailwind runtime")
	}
	if !strings.Contains(markup, `class="app dashboard-app"`) {
		t.Fatalf("expected dashboard page layout styles to be attached through global stylesheet classes")
	}
	if strings.Contains(markup, `src="/static/emoji-picker.js"`) {
		t.Fatalf("expected index html to use the inline dispatch-style emoji picker instead of the external picker script")
	}
	if !strings.Contains(markup, `src="https://www.googletagmanager.com/gtag/js?id=G-BY33RFG2WB"`) {
		t.Fatalf("expected index html to load the google analytics tag script")
	}
	if !strings.Contains(markup, `window.gtag("config", "G-BY33RFG2WB");`) {
		t.Fatalf("expected index html to configure google analytics with the moltenhub measurement id")
	}
	if !strings.Contains(markup, `<title>Molten Hub Code</title>`) {
		t.Fatalf("expected index html to set app title to Molten Hub Code")
	}
	if !strings.Contains(markup, `<meta property="og:image" content="https://app.molten.bot/logo.svg">`) ||
		!strings.Contains(markup, `<meta name="twitter:image" content="https://app.molten.bot/logo.svg">`) {
		t.Fatalf("expected index html to use the molten hub logo for social previews")
	}
	if !strings.Contains(markup, `src="https://cdn.jsdelivr.net/npm/chart.js@4.4.9/dist/chart.umd.min.js"`) {
		t.Fatalf("expected index html to load Chart.js for dashboard stats")
	}
	if !strings.Contains(markup, `id="dashboard-display"`) ||
		!strings.Contains(markup, `<h1 id="dashboard-title">Session Dashboard</h1>`) ||
		!strings.Contains(markup, `id="dashboard-max-concurrent"`) ||
		!strings.Contains(markup, `id="dashboard-session-runtime"`) ||
		!strings.Contains(markup, `Session Runtime`) ||
		!strings.Contains(markup, `stats.session_runtime_seconds`) ||
		!strings.Contains(markup, `id="dashboard-time-saved"`) ||
		!strings.Contains(markup, `id="dashboard-workflow-times"`) ||
		!strings.Contains(markup, `id="dashboard-agent-times"`) ||
		!strings.Contains(markup, `id="dashboard-task-chart"`) ||
		!strings.Contains(markup, `id="dashboard-source-chart"`) ||
		!strings.Contains(markup, `id="dashboard-source-total"`) {
		t.Fatalf("expected index html to render the dashboard stats panel")
	}
	if !strings.Contains(markup, `<span class="dashboard-stat-label">Total Tasks</span>`) ||
		!strings.Contains(markup, `<strong id="dashboard-total-tasks">0</strong>`) ||
		!strings.Contains(markup, `<span class="dashboard-stat-label">Pull Requests</span>`) ||
		!strings.Contains(markup, `<strong id="dashboard-total-prs">0</strong>`) ||
		!strings.Contains(markup, "function dashboardPullRequestCount(snapshot)") ||
		!strings.Contains(markup, `const dashboardTotalPRs = document.getElementById("dashboard-total-prs");`) ||
		!strings.Contains(markup, "dashboardTotalTasks.textContent = String(Number(stats.total_tasks || 0));") ||
		!strings.Contains(markup, "dashboardTotalPRs.textContent = String(dashboardPullRequestCount(snapshot));") {
		t.Fatalf("expected dashboard to show task and pull-request totals in separate stat cards")
	}
	if strings.Contains(markup, `<span class="dashboard-stat-label">Max Concurrent Tasks</span>`) {
		t.Fatalf("expected dashboard max concurrency label to use concise copy")
	}
	lastDashboardStatLabel := -1
	for _, label := range []string{
		"Time Saved",
		"Throughput",
		"Velocity",
		"Max Concurrency",
		"Total Tasks",
		"Pull Requests",
		"Average Runtime",
		"Session Runtime",
	} {
		statLabel := `<span class="dashboard-stat-label">` + label + `</span>`
		index := strings.Index(markup, statLabel)
		if index < 0 {
			t.Fatalf("expected dashboard stat label %q", label)
		}
		if index <= lastDashboardStatLabel {
			t.Fatalf("expected dashboard stat label %q to appear after previous stat label", label)
		}
		lastDashboardStatLabel = index
	}
	if !strings.Contains(markup, `id="dashboard-share-x"`) ||
		!strings.Contains(markup, `id="dashboard-share-facebook"`) ||
		!strings.Contains(markup, `id="dashboard-share-whatsapp"`) ||
		!strings.Contains(markup, `const DASHBOARD_SHARE_URL = "https://molten.bot/code";`) ||
		!strings.Contains(markup, `const DASHBOARD_SHARE_IMAGE_URL = "https://app.molten.bot/logo.svg";`) ||
		!strings.Contains(markup, `return DASHBOARD_SHARE_URL;`) ||
		!strings.Contains(markup, `function updateDashboardShareLinks(stats)`) ||
		!strings.Contains(markup, `https://twitter.com/intent/tweet`) ||
		!strings.Contains(markup, `https://www.facebook.com/sharer/sharer.php`) ||
		!strings.Contains(markup, `https://wa.me/`) {
		t.Fatalf("expected index html to render dashboard social sharing links")
	}
	if strings.Contains(markup, `const current = new URL(window.location.href);`) ||
		strings.Contains(markup, `current.hash = "dashboard";`) {
		t.Fatalf("expected dashboard sharing links to stop using the local session URL")
	}
	if !strings.Contains(markup, "return `Updated ${dashboardRelativeTime(date.getTime())}`;") ||
		!strings.Contains(markup, "if (seconds < 60) return `${seconds}s ago`;") ||
		!strings.Contains(markup, "if (minutes < 60) return `${minutes}m ago`;") ||
		!strings.Contains(markup, "return `${hours}h ago`;") ||
		!strings.Contains(markup, "refreshDashboardUpdatedLabel();") {
		t.Fatalf("expected dashboard updated label to render relative seconds, minutes, and hours")
	}
	if !strings.Contains(markup, `<moltenhub-code-header agent-harness="codex" agent-label="Codex"></moltenhub-code-header>`) {
		t.Fatalf("expected index html to render app heading through the shared site header")
	}
	if !strings.Contains(markup, `Current Work</span>`) {
		t.Fatalf("expected index html to render the task panel under a Current Work heading")
	}
	if !strings.Contains(markup, "const TASK_PROGRESS_STEP_DEFS = Object.freeze({") {
		t.Fatalf("expected index html to define dynamic task progress step metadata")
	}
	if !strings.Contains(markup, `fork: { id: "fork", label: "Fork Fallback", detail: "Fork route prepared for public repo publishing.", icon: "fork" },`) {
		t.Fatalf("expected index html to include a dedicated fork-fallback task progress step")
	}
	if !strings.Contains(markup, `if (stage === "workflow") {`) {
		t.Fatalf("expected index html to map stage=workflow into task progress rendering")
	}
	if strings.Contains(markup, `Runs move through prepare, clone, agent, and finalize. Re-runs, local clone links, and PRs stay attached here.`) {
		t.Fatalf("expected index html to remove the old task queue supporting copy")
	}
	if !strings.Contains(markup, `Lets get to work!`) {
		t.Fatalf("expected index html to render updated empty queue title")
	}
	if !strings.Contains(markup, `New runs will appear here with their progress, replay controls, local clone actions, and PR links.`) {
		t.Fatalf("expected index html to render updated empty queue copy")
	}
	if strings.Contains(markup, `The queue is clear.`) || strings.Contains(markup, `Start from Studio, Library, or JSON. New runs will appear here with stage progress, replay controls, local clone actions, and PR links.`) {
		t.Fatalf("expected index html to remove legacy empty queue copy")
	}
	if strings.Contains(markup, `task-empty-chip`) {
		t.Fatalf("expected index html to remove empty queue stage chips")
	}
	if !strings.Contains(markup, `id="prompt-panel-title" class="panel-section-title prompt-title-list" aria-label="Prompt Studio"`) ||
		!strings.Contains(markup, `data-lucide="pencil" class="prompt-title-icon"`) ||
		!strings.Contains(markup, `data-prompt-title-label="Prompt Studio" aria-label="Prompt Studio"`) ||
		!strings.Contains(markup, `data-lucide="book-open" class="prompt-title-icon"`) ||
		!strings.Contains(markup, `data-prompt-title-label="Library Studio" aria-label="Library Studio" hidden aria-hidden="true"`) ||
		!strings.Contains(markup, `data-lucide="braces" class="prompt-title-icon"`) ||
		!strings.Contains(markup, `data-prompt-title-label="JSON Studio" aria-label="JSON Studio" hidden aria-hidden="true"`) ||
		!strings.Contains(markup, `<span>Studio</span>`) {
		t.Fatalf("expected index html to label Studio as a single active mode icon title")
	}
	if strings.Contains(markup, `Studio <span class="prompt-title-separator" aria-hidden="true">/</span>`) {
		t.Fatalf("expected index html to remove duplicated Studio mode breadcrumb text")
	}
	if !strings.Contains(markup, `setPromptMode(promptModeFromHash() || "builder");`) {
		t.Fatalf("expected index html to default Studio to the Prompt view")
	}
	if !strings.Contains(markup, `id="prompt-panel-copy"`) || !strings.Contains(markup, `Compose a repository run, start from a library task, or edit the raw JSON payload.`) {
		t.Fatalf("expected index html to include prompt panel supporting copy")
	}
	if strings.Contains(markup, `Compose a repository run with remotes, branch, reviewers, screenshots, and a plain-language task brief.`) {
		t.Fatalf("expected index html to remove the verbose builder mode description")
	}
	if strings.Contains(markup, `Run Studio`) || strings.Contains(markup, `Queue repository work with the same repo, branch, reviewer, and prompt contract the hub executes.`) {
		t.Fatalf("expected index html to remove the Studio overview hero panel")
	}
	if !strings.Contains(markup, `id="local-prompt-submit" class="prompt-action-button prompt-submit" type="submit">`) || !strings.Contains(markup, `class="prompt-action-label">Run</span>`) {
		t.Fatalf("expected index html to render the primary prompt submit action as Run")
	}
	if strings.Contains(markup, `id="configured-agent-subtitle"`) || strings.Contains(markup, "Configured agent: Codex") {
		t.Fatalf("expected index html to remove the configured agent subtitle copy")
	}
	if !strings.Contains(markup, `src="/static/site-header.js"`) {
		t.Fatalf("expected index html to load the shared site header component")
	}
	if !strings.Contains(markup, `<moltenhub-code-header agent-harness="codex" agent-label="Codex"></moltenhub-code-header>`) {
		t.Fatalf("expected index html to render the shared site header component")
	}
	if strings.Contains(markup, `id="moltenhub-logo"`) || strings.Contains(markup, `id="configured-agent-logo"`) {
		t.Fatalf("expected index html to isolate header logo markup inside the shared component")
	}
	if !strings.Contains(markup, `const BACKGROUND_PARTICLE_ANIMATION_ENABLED = false;`) ||
		!strings.Contains(markup, "if (!BACKGROUND_PARTICLE_ANIMATION_ENABLED) {\n        canvas.hidden = true;\n        return;\n      }") {
		t.Fatalf("expected index html to keep the decorative particle canvas disabled by default")
	}
	if !strings.Contains(markup, `const TASK_TIMING_REFRESH_INTERVAL_MS = 30_000;`) {
		t.Fatalf("expected index html to refresh task timing labels on a separate interval")
	}
	if !strings.Contains(markup, `function refreshVisibleTaskTimingSummaries()`) {
		t.Fatalf("expected index html to refresh visible task timing labels without a full task rerender")
	}
	if !strings.Contains(markup, `timing: taskTimingSignature(task),`) {
		t.Fatalf("expected task collection render signatures to use stable task timing data")
	}
	timingSignatureStart := strings.Index(markup, `function taskTimingSignature(task)`)
	timingSignatureEnd := strings.Index(markup, `function applyTaskTimingSummary(node, task)`)
	if timingSignatureStart < 0 || timingSignatureEnd <= timingSignatureStart {
		t.Fatalf("expected index html to include a bounded task timing render signature helper")
	}
	if strings.Contains(markup[timingSignatureStart:timingSignatureEnd], "updatedAt") {
		t.Fatalf("expected task timing render signatures to exclude log-only updated_at churn")
	}
	if !strings.Contains(markup, `function syncTaskTimingSummariesForList(listNode, tasks)`) ||
		!strings.Contains(markup, `syncTaskTimingSummariesForList(listNode, tasks);`) {
		t.Fatalf("expected index html to update task timing summaries in place when render signatures are unchanged")
	}
	if !strings.Contains(markup, `const controls = task?.controls || {};`) || !strings.Contains(markup, `const canStop = Boolean(controls.stop);`) {
		t.Fatalf("expected index html to render task controls from backend-provided capabilities")
	}
	if !strings.Contains(markup, `update.className = "task-timing-summary";`) {
		t.Fatalf("expected task timing labels to render into dedicated nodes for in-place refresh")
	}
	if !strings.Contains(markup, `update.dataset.requestId = requestID;`) {
		t.Fatalf("expected task timing labels to keep request ids for in-place updates")
	}
	if !strings.Contains(markup, `scheduleTaskTimingRefresh();`) {
		t.Fatalf("expected index html to start the task timing refresh scheduler during boot")
	}
	if !strings.Contains(markup, `id="moltenbot-hub-link"`) {
		t.Fatalf("expected index html to include molten bot hub dock link")
	}
	if !strings.Contains(markup, `id="moltenbot-hub-profile-button"`) {
		t.Fatalf("expected index html to include molten bot hub profile gear button")
	}
	if !strings.Contains(markup, `href="https://molten.bot/login?target=hub"`) {
		t.Fatalf("expected index html to link unconfigured dock icon to molten hub sign-in")
	}
	if strings.Contains(markup, `aria-label="Home"`) || strings.Contains(markup, `data-page-nav-link="/"`) || strings.Contains(markup, `data-lucide="home"`) {
		t.Fatalf("expected bottom dock to omit the home navigation button")
	}
	if !strings.Contains(markup, `img src="https://app.molten.bot/logo.svg"`) {
		t.Fatalf("expected index html to use the remote molten bot logo asset")
	}
	if !strings.Contains(markup, `id="moltenbot-hub-plus"`) {
		t.Fatalf("expected index html to include molten hub plus badge")
	}
	if !strings.Contains(markup, `id="hub-setup-gate"`) {
		t.Fatalf("expected index html to include hub setup modal gate")
	}
	if !strings.Contains(markup, `class="onboarding-modal-backdrop hidden"`) ||
		!strings.Contains(markup, `class="onboarding-modal form-surface hub-setup-shell"`) {
		t.Fatalf("expected hub setup modal to use the dispatch onboarding modal layout")
	}
	if !strings.Contains(markup, `id="hub-setup-form"`) {
		t.Fatalf("expected index html to include hub setup form")
	}
	if strings.Contains(markup, `id="hub-setup-copy"`) || strings.Contains(markup, `Agent tokens start with t_; bind tokens start with b_`) {
		t.Fatalf("expected index html to remove the extra Molten Hub setup guidance line")
	}
	if !strings.Contains(markup, `id="hub-setup-token-label" class="prompt-label">Agent Token</span>`) {
		t.Fatalf("expected index html to default the setup token label to Agent Token")
	}
	if !strings.Contains(markup, `id="hub-setup-submit" class="prompt-action-button prompt-submit" type="submit">Connect Runtime</button>`) {
		t.Fatalf("expected index html to use a clearer default hub setup submit label")
	}
	if !strings.Contains(markup, `id="prompt-mode-builder" class="prompt-mode-link active" href="#studio-builder" aria-selected="true" title="Prompt"`) {
		t.Fatalf("expected index html to relabel the primary dock mode as Prompt")
	}
	if strings.Contains(markup, `data-app-display="releases"`) ||
		strings.Contains(markup, `href="#releases"`) ||
		strings.Contains(markup, `data-lucide="tag"`) {
		t.Fatalf("expected index html to omit the releases dock link")
	}
	if !strings.Contains(markup, `id="hub-setup-emoji-picker"`) || !strings.Contains(markup, `id="hub-setup-emoji-panel"`) {
		t.Fatalf("expected index html to include the emoji picker control shell")
	}
	if !strings.Contains(markup, `id="hub-setup-signin-link"`) || !strings.Contains(markup, `https://molten.bot/login?target=hub`) {
		t.Fatalf("expected index html to include molten hub sign-in shortcut inside the setup dialog")
	}
	if !strings.Contains(markup, `class="hub-setup-signin-logo"`) {
		t.Fatalf("expected index html to render the hub sign-in shortcut as a logo")
	}
	if !strings.Contains(markup, `id="hub-setup-token-label"`) {
		t.Fatalf("expected index html to include the dynamic hub setup token label")
	}
	if !strings.Contains(markup, `id="hub-setup-status" class="hub-setup-status submit-status submit-status-inline`) {
		t.Fatalf("expected index html to include a dedicated hub setup status line")
	}
	if !strings.Contains(markup, `class="onboarding-form-actions hub-setup-footer"`) {
		t.Fatalf("expected hub setup actions to use the dispatch onboarding action row")
	}
	if strings.Contains(markup, `id="hub-setup-onboarding"`) || strings.Contains(markup, `id="hub-setup-onboarding-steps"`) {
		t.Fatalf("expected index html to remove the hub setup onboarding progress list")
	}
	if !strings.Contains(markup, `id="hub-setup-region-na-toggle"`) || !strings.Contains(markup, `id="hub-setup-region-eu-toggle"`) {
		t.Fatalf("expected index html to include hub setup region toggles")
	}
	if strings.Contains(markup, `id="hub-setup-connect-agent-row"`) || strings.Contains(markup, `data-agent-mode=`) {
		t.Fatalf("expected index html to remove the manual agent mode controls")
	}
	if strings.Contains(markup, `id="hub-setup-bind-toggle"`) || strings.Contains(markup, `id="hub-setup-agent-toggle"`) {
		t.Fatalf("expected index html to remove the separate hub setup token type toggles")
	}
	if !strings.Contains(markup, `function scheduleHubSetupAutoSubmit()`) {
		t.Fatalf("expected index html to include hub setup auto-submit scheduling")
	}
	if !strings.Contains(markup, `Bind Token`) {
		t.Fatalf("expected index html to relabel new-agent token entry as Bind Token")
	}
	if !strings.Contains(markup, `>Done</button>`) {
		t.Fatalf("expected hub setup submit button copy to be updated")
	}
	if !strings.Contains(markup, `function normalizeHubSetup(raw)`) {
		t.Fatalf("expected index html to include hub setup state normalization")
	}
	if !strings.Contains(markup, `function limitGraphemes(value, maxGraphemes)`) || !strings.Contains(markup, `Intl.Segmenter`) {
		t.Fatalf("expected index html to include grapheme clamp utility with Intl.Segmenter support")
	}
	if !strings.Contains(markup, `function normalizeProfileEmoji(value)`) {
		t.Fatalf("expected index html to include profile emoji normalization helper")
	}
	if !strings.Contains(markup, `emoji: normalizeProfileEmoji(profile.emoji),`) {
		t.Fatalf("expected index html to clamp profile emoji when loading hub setup state")
	}
	if !strings.Contains(markup, `emoji: normalizeProfileEmoji(syncHubSetupEmojiValue()),`) {
		t.Fatalf("expected index html to clamp profile emoji before submitting hub setup payloads")
	}
	if !strings.Contains(markup, `function defaultHubSetupOnboarding(agentMode)`) {
		t.Fatalf("expected index html to include default hub setup onboarding steps")
	}
	if !strings.Contains(markup, `function hubSetupStatusMessageForStep(stepID, fallbackText = "")`) {
		t.Fatalf("expected index html to include hub setup step-to-status mapping")
	}
	if !strings.Contains(markup, `function normalizeHubSetupDialogMode(mode)`) {
		t.Fatalf("expected index html to include hub setup dialog mode normalization")
	}
	if !strings.Contains(markup, `async function submitHubSetup(event, options = {})`) {
		t.Fatalf("expected index html to include hub setup submit handler")
	}
	if !strings.Contains(markup, `async function loadHubSetupStatus()`) {
		t.Fatalf("expected index html to include hub setup status loader")
	}
	if !strings.Contains(markup, `function attachHubEmojiPicker(root)`) || !strings.Contains(markup, `data-hub-emoji-picker`) {
		t.Fatalf("expected index html to initialize the inline dispatch-style emoji picker")
	}
	if !strings.Contains(markup, `class="prompt-mode-divider"`) {
		t.Fatalf("expected dock to include a shared divider between internal and external links")
	}
	if !strings.Contains(markup, `class="prompt-mode-link prompt-mode-link-logo"`) {
		t.Fatalf("expected dock logo links to use shared icon-link styling")
	}
	githubIndex := strings.Index(markup, `id="github-profile-link"`)
	moltenbotIndex := strings.Index(markup, `id="moltenbot-hub-link"`)
	if githubIndex == -1 || moltenbotIndex == -1 || githubIndex > moltenbotIndex {
		t.Fatalf("expected molten bot hub logo to render to the right of the github dock logo")
	}
	profileButtonIndex := strings.Index(markup, `id="moltenbot-hub-profile-button"`)
	if profileButtonIndex == -1 || profileButtonIndex < moltenbotIndex {
		t.Fatalf("expected hub profile button to render to the right of the hub dock icon")
	}
	if !strings.Contains(markup, `Agent Profile`) {
		t.Fatalf("expected index html to include connected profile editor copy")
	}
	if !strings.Contains(markup, `Update how this agent appears in Molten Hub.`) {
		t.Fatalf("expected index html to include updated profile editor message")
	}
	if strings.Contains(markup, `Update how this runtime appears in Molten Hub`) {
		t.Fatalf("expected index html to remove the old runtime profile editor copy")
	}
	if strings.Contains(markup, `Edit Agent Profile`) {
		t.Fatalf("expected index html to remove the old profile editor heading")
	}
	if !strings.Contains(markup, `id="hub-setup-connection-toggle"`) {
		t.Fatalf("expected index html to include the hub connection toggle button")
	}
	if !strings.Contains(markup, `id="hub-setup-connection-toggle" class="secondary-button hub-setup-connection-toggle hidden"`) {
		t.Fatalf("expected index html to render the disconnect action as an icon-only secondary button")
	}
	if !strings.Contains(markup, `id="hub-setup-submit" class="prompt-action-button prompt-submit"`) {
		t.Fatalf("expected index html to render the profile save action with the shared submit button classes")
	}
	if !strings.Contains(markup, `async function submitHubConnectionToggle()`) {
		t.Fatalf("expected index html to include hub connection toggle handler")
	}
	if !strings.Contains(markup, `function renderHubSetupConnectionToggle()`) {
		t.Fatalf("expected index html to include hub connection toggle renderer")
	}
	if !strings.Contains(markup, `hubSetupConnectionToggle.addEventListener("click", submitHubConnectionToggle);`) {
		t.Fatalf("expected index html to wire the hub connection toggle button")
	}
	if !strings.Contains(markup, `<span class="prompt-label">Bio</span>`) {
		t.Fatalf("expected index html to relabel the agent summary field as Bio")
	}
	if !strings.Contains(markup, `hubSetupHandle.readOnly = profileEditor || state.hubSetupBusy;`) {
		t.Fatalf("expected index html to make the handle field readonly in profile edit mode")
	}
	if !strings.Contains(markup, `hubSetupToken.readOnly = state.hubSetupBusy;`) {
		t.Fatalf("expected index html to switch hub setup token entry to readonly while onboarding runs")
	}
	if !strings.Contains(markup, `id="hub-setup-profile" class="prompt-text prompt-control hub-setup-profile-input`) || !strings.Contains(markup, `rows="4"`) {
		t.Fatalf("expected index html to render a dispatch-style four-line bio textarea")
	}
	if !strings.Contains(markup, `if (hubSetupForm) hubSetupForm.setAttribute("aria-busy", state.hubSetupBusy ? "true" : "false");`) {
		t.Fatalf("expected index html to mark the hub setup form busy while saving")
	}
	if !strings.Contains(markup, `if (hubSetupClose) hubSetupClose.disabled = state.hubSetupBusy;`) {
		t.Fatalf("expected index html to lock the setup dialog close control during save")
	}
	if !strings.Contains(markup, `hubSetupSubmit.textContent = profileEditor ? "Save" : (isNew ? "Lets Gooo!" : "Connect Runtime");`) {
		t.Fatalf("expected index html to relabel the hub setup submit button for profile, new-agent, and reconnect flows")
	}
	if !strings.Contains(markup, `function hubSetupModeForToken(token, fallback = state.hubSetup.agentMode)`) ||
		!strings.Contains(markup, `trimmed.startsWith("b_")`) ||
		!strings.Contains(markup, `trimmed.startsWith("t_")`) {
		t.Fatalf("expected index html to infer new/existing agent mode from token prefix")
	}
	if !strings.Contains(markup, `hubSetupStatus.className = value`) || !strings.Contains(markup, `hub-setup-status submit-status submit-status-inline is-visible`) {
		t.Fatalf("expected index html to keep the hub setup status line visible when populated")
	}
	if !strings.Contains(markup, `if (autoSubmit || isHubProfileDialogMode()) {`) || !strings.Contains(markup, `await new Promise((resolve) => window.setTimeout(resolve, 700));`) {
		t.Fatalf("expected index html to close the profile dialog after a successful save confirmation")
	}
	if !strings.Contains(markup, `const HUB_PROFILE_DEEP_LINK_HASH = "#agent-profile";`) ||
		!strings.Contains(markup, `function shouldOpenInitialHubProfileDialog()`) ||
		!strings.Contains(markup, `openHubProfileDialog("deep_link");`) ||
		!strings.Contains(markup, `openHubProfileDialog("dock");`) {
		t.Fatalf("expected index html to support opening the local agent profile modal from a dock deep link")
	}
	hubSetupStatusIndex := strings.Index(markup, `id="hub-setup-status"`)
	hubSetupSaveIndex := strings.Index(markup, `id="hub-setup-submit"`)
	if hubSetupStatusIndex == -1 || hubSetupSaveIndex == -1 || hubSetupStatusIndex > hubSetupSaveIndex {
		t.Fatalf("expected the hub setup status line to render before the action buttons")
	}
	if !strings.Contains(markup, "window.MoltenHubHeader.update({") {
		t.Fatalf("expected index html to update the shared site header component")
	}
	if !strings.Contains(markup, `"task-close"`) {
		t.Fatalf("expected index html to include task close class usage")
	}
	if !strings.Contains(markup, `"task-closing"`) {
		t.Fatalf("expected index html to include task closing class usage")
	}
	if !strings.Contains(markup, `"task-rerun task-icon-button"`) && !strings.Contains(markup, `"task-rerun"`) {
		t.Fatalf("expected index html to include task rerun class usage")
	}
	if !strings.Contains(markup, "function dismissTask(") {
		t.Fatalf("expected index html to include dismissTask handler")
	}
	if !strings.Contains(markup, "function renderTaskCloseButton(task, requestID)") {
		t.Fatalf("expected index html to include shared task close button renderer")
	}
	if !strings.Contains(markup, `close.title = historyOnly ? "Remove task from history view" : "Close finished task";`) {
		t.Fatalf("expected index html to label close actions for both history-only and live completed tasks")
	}
	if !strings.Contains(markup, "const CLOSE_TASK_FADE_MS = 2000;") {
		t.Fatalf("expected index html to include close task fade timing")
	}
	if !strings.Contains(markup, "closingTaskIDs: new Set()") {
		t.Fatalf("expected index html to track closing tasks")
	}
	if !strings.Contains(markup, "function isTaskClosePending(") {
		t.Fatalf("expected index html to include immediate close-button hiding helper")
	}
	if !strings.Contains(markup, "close.hidden = closePending;") {
		t.Fatalf("expected index html to hide the close button immediately while close is pending")
	}
	if !strings.Contains(markup, "completeTaskDismissal(requestID)") {
		t.Fatalf("expected index html to include delayed task dismissal helper")
	}
	if !strings.Contains(markup, "state.taskHistoryByID.delete(requestID);") {
		t.Fatalf("expected index html to remove dismissed tasks from persisted completed history")
	}
	if !strings.Contains(markup, "state.taskHistoryUnseenIDs instanceof Set && state.taskHistoryUnseenIDs.delete(requestID)") {
		t.Fatalf("expected index html to clear unseen completed history state for dismissed tasks")
	}
	if !strings.Contains(markup, "function rerunTask(") {
		t.Fatalf("expected index html to include rerunTask handler")
	}
	if !strings.Contains(markup, `"task-progress"`) {
		t.Fatalf("expected index html to include task progress class usage")
	}
	if !strings.Contains(markup, "function renderTaskProgress(") {
		t.Fatalf("expected index html to include renderTaskProgress handler")
	}
	if !strings.Contains(markup, "function renderTaskCurrentStateBadge(") ||
		!strings.Contains(markup, "function taskCurrentStateSignature(") ||
		!strings.Contains(markup, "appendTaskStepIcon(marker, step, task);") {
		t.Fatalf("expected index html to reuse progress step icons for compact task state badges")
	}
	if !strings.Contains(markup, "currentState: taskCurrentStateSignature(task),") {
		t.Fatalf("expected task render signatures to include compact current-state icon changes")
	}
	if !strings.Contains(markup, `node.classList.add("task-compact-state-left");`) {
		t.Fatalf("expected compact/minimized task rows to render their state icon on the left")
	}
	if !strings.Contains(markup, `icon: "entry_chat"`) || !strings.Contains(markup, `icon: "entry_hub"`) || !strings.Contains(markup, `icon: "entry_prompt"`) || !strings.Contains(markup, `icon: "entry_library"`) || !strings.Contains(markup, `icon: "entry_json"`) || !strings.Contains(markup, `icon: "prepare"`) || !strings.Contains(markup, `icon: "clone"`) || !strings.Contains(markup, `icon: "branch"`) || !strings.Contains(markup, `icon: "publish"`) || !strings.Contains(markup, `icon: "fork"`) || !strings.Contains(markup, `icon: "agent"`) || !strings.Contains(markup, `icon: "commit"`) || !strings.Contains(markup, `icon: "pr"`) || !strings.Contains(markup, `icon: "checks"`) || !strings.Contains(markup, `icon: "github"`) {
		t.Fatalf("expected index html to classify dynamic progress steps by action icon keys")
	}
	if !strings.Contains(markup, `entry_chat: "message-circle"`) || !strings.Contains(markup, `entry_hub: "satellite-dish"`) || !strings.Contains(markup, `entry_prompt: "pencil"`) || !strings.Contains(markup, `entry_library: "book-open"`) || !strings.Contains(markup, `entry_json: "braces"`) || !strings.Contains(markup, `prepare: "wrench"`) || !strings.Contains(markup, `clone: "git-branch-plus"`) || !strings.Contains(markup, `branch: "git-branch"`) || !strings.Contains(markup, `publish: "upload"`) || !strings.Contains(markup, `fork: "git-fork"`) || !strings.Contains(markup, `agent: "bot"`) || !strings.Contains(markup, `commit: "git-commit-horizontal"`) || !strings.Contains(markup, `pr: "git-pull-request"`) || !strings.Contains(markup, `checks: "shield-check"`) {
		t.Fatalf("expected index html to map progress icon keys to lucide action glyphs")
	}
	if !strings.Contains(markup, `finalize: { id: "finalize", label: "GitHub"`) ||
		!strings.Contains(markup, `icon: "github"`) ||
		!strings.Contains(markup, `if (iconKey === "github") {`) ||
		!strings.Contains(markup, `return GITHUB_LOGO_URL;`) {
		t.Fatalf("expected index html to use the GitHub logo as the final progress step")
	}
	if !strings.Contains(markup, `stop: "square"`) {
		t.Fatalf("expected index html to map stop actions to the square glyph")
	}
	if !strings.Contains(markup, `if (status === "stopped") {`) || !strings.Contains(markup, `if (status === "error" || status === "invalid") {`) {
		t.Fatalf("expected index html to separate stopped status icon handling from error/invalid handling")
	}
	if strings.Contains(markup, `status === "error" || status === "invalid" || status === "duplicate" || status === "stopped"`) || !strings.Contains(markup, `status === "error" || status === "invalid" || status === "duplicate"`) {
		t.Fatalf("expected index html to avoid playing the error sound for user-stopped tasks")
	}
	if !strings.Contains(markup, "function taskProgressStepIconURL(") {
		t.Fatalf("expected index html to include task progress icon URL resolver")
	}
	if !strings.Contains(markup, "function taskProgressFallbackIconURL(") {
		t.Fatalf("expected index html to include a moltenhub logo fallback for unknown progress icons")
	}
	if !strings.Contains(markup, "function taskProgressStepsForTask(") || !strings.Contains(markup, "function taskProgressCurrentStepID(") || !strings.Contains(markup, "function taskProgressModel(") {
		t.Fatalf("expected index html to build dynamic progress steps per task")
	}
	if !strings.Contains(markup, `pi: "/static/logos/pi.svg"`) {
		t.Fatalf("expected index html to map the pi harness to the pi logo asset")
	}
	if !strings.Contains(markup, `opencode: "/static/logos/opencode.svg"`) {
		t.Fatalf("expected index html to map the opencode harness to the opencode logo asset")
	}
	if !strings.Contains(markup, "task-progress-step-icon") {
		t.Fatalf("expected index html to render task progress step icons")
	}
	if !strings.Contains(markup, `const TASK_PROGRESS_AGENT_STAGES = new Set(["codex", "claude", "auggie", "pi", "opencode", "augment", "agent", "review"]);`) {
		t.Fatalf("expected index html to define the stage set that maps runtime agent stages into the agent progress step")
	}
	if strings.Contains(markup, "current step:") {
		t.Fatalf("expected index html to remove current step label text from task progress")
	}
	if !strings.Contains(markup, "function formatTaskBranch(") {
		t.Fatalf("expected index html to include branch formatter for task metadata")
	}
	if !strings.Contains(markup, "const baseBranch = String(task?.base_branch || \"\").trim();") {
		t.Fatalf("expected index html to consider task base_branch when formatting branch metadata")
	}
	if !strings.Contains(markup, "return `from:${baseBranch} to:${branch}`;") {
		t.Fatalf("expected index html to render base-to-head branch transitions")
	}
	if !strings.Contains(markup, "function taskCloneCommand(") || !strings.Contains(markup, "function copyTaskCloneCommand(") {
		t.Fatalf("expected index html to include task clone command helpers for completed branches")
	}
	if !strings.Contains(markup, `const TASK_ACTION_ICON_NAMES = Object.freeze({`) ||
		!strings.Contains(markup, `output: "terminal-square",`) {
		t.Fatalf("expected index html task action icon mapping to use lucide terminal glyphs")
	}
	if !strings.Contains(markup, "function openTaskOutput(") {
		t.Fatalf("expected index html to include focused task output opener")
	}
	if strings.Contains(markup, "function toggleTaskOutput(") {
		t.Fatalf("expected index html to remove inline task output toggle handler")
	}
	if strings.Contains(markup, "function toggleTerminalOutput(") {
		t.Fatalf("expected index html to remove terminal output toggle handler")
	}
	if !strings.Contains(markup, "function setTaskFullscreen(") {
		t.Fatalf("expected index html to include full screen task toggle handler")
	}
	if !strings.Contains(markup, "function fullscreenTasks(") {
		t.Fatalf("expected index html to include full screen task list renderer")
	}
	if !strings.Contains(markup, "if (state.taskFullscreenMode === \"console\") {\n        const selectedRequestID = String(state.selectedTaskId || \"\").trim();") ||
		!strings.Contains(markup, "return tasks.filter((task) => String(task?.request_id || \"\").trim() === selectedRequestID);") {
		t.Fatalf("expected console fullscreen task list to show only the selected task")
	}
	if !strings.Contains(markup, "function taskCollectionRenderSig(tasks, options = {})") {
		t.Fatalf("expected index html to compute stable task collection render signatures")
	}
	if !strings.Contains(markup, "if (!state.taskFullscreenOpen) {\n        if (taskFullscreenList) {") {
		t.Fatalf("expected index html to skip hidden fullscreen task-list renders")
	}
	if !strings.Contains(markup, "function clearFullscreenTerminalRender()") ||
		!strings.Contains(markup, "if (!state.taskFullscreenOpen || state.taskFullscreenMode !== \"console\") {\n        clearFullscreenTerminalRender();\n        return;\n      }") {
		t.Fatalf("expected index html to clear and skip terminal renders outside console fullscreen mode")
	}
	if !strings.Contains(markup, "const taskPanelNodes = state.taskFullscreenOpen ? [taskList, taskFullscreenList] : [taskList];") {
		t.Fatalf("expected deferred task-panel flushes to ignore hidden fullscreen nodes")
	}
	if !strings.Contains(markup, "state.taskFullscreenOpen &&\n        taskFullscreenTerminal") {
		t.Fatalf("expected deferred terminal flushes to require open fullscreen state")
	}
	if !strings.Contains(markup, "if (nodeContainsActiveSelection(listNode)) {") {
		t.Fatalf("expected index html to defer task list redraws while text is selected")
	}
	if !strings.Contains(markup, "terminalNode.dataset.pendingRenderSig = renderSig;") {
		t.Fatalf("expected index html to defer task output redraws while text is selected")
	}
	if !strings.Contains(markup, "function scheduleDeferredSelectionRenderFlush() {") ||
		!strings.Contains(markup, "deferredSelectionRenderTimer = window.setTimeout(() => {") {
		t.Fatalf("expected index html to include a timed fallback flush for deferred selection-bound renders")
	}
	if !strings.Contains(markup, "scheduleDeferredSelectionRenderFlush();") {
		t.Fatalf("expected index html to schedule deferred flushes whenever a render is blocked by active selection")
	}
	if !strings.Contains(markup, "document.addEventListener(\"selectionchange\", flushDeferredSelectionRenders);") {
		t.Fatalf("expected index html to flush deferred task renders after text selection clears")
	}
	if !strings.Contains(markup, "const taskPanel = document.getElementById(\"task-panel\");") {
		t.Fatalf("expected index html to cache the task panel element")
	}
	if !strings.Contains(markup, "if (open && !displayTasks(state.snapshot).length) {") {
		t.Fatalf("expected index html to block fullscreen when no tasks exist")
	}
	if !strings.Contains(markup, "function setTaskPanelView(view, options = {})") {
		t.Fatalf("expected index html to include task panel view mode switching")
	}
	if !strings.Contains(markup, "function historyTasks(snapshot)") || !strings.Contains(markup, "function rememberCompletedTaskHistory(snapshot)") {
		t.Fatalf("expected index html to include running completed-task history aggregation")
	}
	if !strings.Contains(markup, "function completedTaskHistoryKey(task)") || !strings.Contains(markup, "function preferCompletedHistoryTask(current, next)") {
		t.Fatalf("expected index html to include completed-task history dedupe helpers")
	}
	if !strings.Contains(markup, "deduped.set(key, preferCompletedHistoryTask(deduped.get(key), task));") {
		t.Fatalf("expected index html to collapse duplicate completed tasks into latest history entry")
	}
	currentTasksStart := strings.Index(markup, "function currentTasks(snapshot) {")
	historyTasksStart := strings.Index(markup, "function historyTasks(snapshot) {")
	if currentTasksStart < 0 || historyTasksStart < 0 || historyTasksStart <= currentTasksStart {
		t.Fatalf("expected index html to include currentTasks and historyTasks definitions in order")
	}
	currentTasksBody := markup[currentTasksStart:historyTasksStart]
	if strings.Contains(currentTasksBody, "for (const task of state.taskHistoryByID.values()) {") {
		t.Fatalf("expected current work list to exclude persisted history-only tasks")
	}
	if !strings.Contains(markup, "const liveByID = new Map();") || !strings.Contains(markup, "for (const task of liveByID.values()) {") {
		t.Fatalf("expected index html history mode to include live run tasks alongside saved history")
	}
	if !strings.Contains(markup, "if (!requestID || state.dismissedTaskIDs.has(requestID) || isTaskHistoryExpired(task, nowMs)) {") {
		t.Fatalf("expected index html to skip dismissed and expired completed tasks while rebuilding history")
	}
	if !strings.Contains(markup, "if (!requestID || liveByID.has(requestID) || state.dismissedTaskIDs.has(requestID) || isTaskHistoryExpired(task, nowMs)) {") {
		t.Fatalf("expected index html history mode to exclude dismissed or expired tasks from persisted history output")
	}
	if !strings.Contains(markup, `const TASK_HISTORY_KEY = "hubui.taskHistory.v1";`) {
		t.Fatalf("expected index html to define a dedicated persisted task history storage key")
	}
	if !strings.Contains(markup, "const TASK_HISTORY_LIMIT = 25;") {
		t.Fatalf("expected index html to cap completed history display to the latest 25 tasks")
	}
	if !strings.Contains(markup, "const TASK_HISTORY_MAX_AGE_MS = 20 * 60 * 60 * 1000;") {
		t.Fatalf("expected index html to define a 20-hour max age for completed task history")
	}
	if !strings.Contains(markup, "function loadTaskHistory()") || !strings.Contains(markup, "function persistTaskHistory()") {
		t.Fatalf("expected index html to include load/persist helpers for run history")
	}
	if !strings.Contains(markup, "if (!requestID || !isCompletedTask(copy) || isTaskHistoryExpired(copy, nowMs)) {") {
		t.Fatalf("expected index html to drop expired entries while loading persisted task history")
	}
	if !strings.Contains(markup, "function isTaskHistoryExpired(task, nowMs = Date.now()) {") {
		t.Fatalf("expected index html to include a helper for completed-task history expiry checks")
	}
	if !strings.Contains(markup, "function historyTaskCompletedAt(task)") {
		t.Fatalf("expected index html to include stable completed-task history ordering timestamps")
	}
	if !strings.Contains(markup, "copy.history_completed_at = existing.history_completed_at;") {
		t.Fatalf("expected index html to preserve first completed-task history position across later updates")
	}
	if !strings.Contains(markup, "history_completed_at: historyCopy?.history_completed_at || task.history_completed_at || task.updated_at || task.started_at || \"\",") {
		t.Fatalf("expected live completed tasks to reuse persisted history ordering timestamps")
	}
	if !strings.Contains(markup, "const stablePrompt = isCompletedTask(task) &&") ||
		!strings.Contains(markup, "? historyCopy.prompt\n          : task.prompt;") {
		t.Fatalf("expected index html history mode to keep completed-task prompts pinned to the stored history snapshot")
	}
	if !strings.Contains(markup, "if (typeof existing?.prompt === \"string\" && existing.prompt.trim() !== \"\") {\n          copy.prompt = existing.prompt;\n        }") {
		t.Fatalf("expected index html to preserve the original completed-task prompt while refreshing stored history metadata")
	}
	if !strings.Contains(markup, "if (!requestID || isTaskHistoryExpired(task, nowMs)) {") {
		t.Fatalf("expected index html to prune expired task history entries from memory")
	}
	if !strings.Contains(markup, "if (!requestID || state.dismissedTaskIDs.has(requestID) || isTaskHistoryExpired(task, nowMs)) {") {
		t.Fatalf("expected index html to skip expired completed tasks when rebuilding task history")
	}
	if !strings.Contains(markup, "if (isCompletedTask(task) && isTaskHistoryExpired(task, nowMs)) {") {
		t.Fatalf("expected index html to hide live completed tasks from history once they exceed max age")
	}
	if !strings.Contains(markup, "if (pruneTaskHistory()) {") ||
		!strings.Contains(markup, "persistTaskHistoryUnseenIDs();") {
		t.Fatalf("expected index html startup to sanitize expired task history and unseen markers")
	}
	if !strings.Contains(markup, ".filter((task) => !isTaskHistoryExpired(task))") {
		t.Fatalf("expected index html to remove expired completed task history from storage")
	}
	if !strings.Contains(markup, "return out.slice(0, TASK_HISTORY_LIMIT);") {
		t.Fatalf("expected index html to limit completed history view to TASK_HISTORY_LIMIT entries")
	}
	if !strings.Contains(markup, "taskHistoryByID: loadTaskHistory(),") {
		t.Fatalf("expected index html to hydrate task history from local storage on startup")
	}
	if !strings.Contains(markup, "const TASK_PROGRESS_VISIBLE = true;") {
		t.Fatalf("expected index html to keep task progress bar rendering enabled")
	}
	if !strings.Contains(markup, "if (!TASK_PROGRESS_VISIBLE || !task || isCompletedTask(task)) {") {
		t.Fatalf("expected index html to guard task progress bar rendering by visibility and task status")
	}
	if !strings.Contains(markup, "taskViewToggle.addEventListener(\"click\", () => {") {
		t.Fatalf("expected index html to wire task view toggle interactions")
	}
	if !strings.Contains(markup, `id="task-history-toggle"`) {
		t.Fatalf("expected index html to include history icon toggle for running/completed task views")
	}
	if !strings.Contains(markup, `id="task-history-toggle-badge" class="task-history-toggle-badge hidden"`) {
		t.Fatalf("expected index html to include hidden unread-count badge for unseen completed task history")
	}
	if !strings.Contains(markup, `id="task-sound-toggle"`) {
		t.Fatalf("expected index html to include task sound mute toggle in Current Work header")
	}
	taskHeaderActionsStart := strings.Index(markup, `<div class="task-panel-actions">`)
	if taskHeaderActionsStart < 0 {
		t.Fatalf("expected index html to include Current Work header actions")
	}
	taskHeaderActionsEnd := strings.Index(markup[taskHeaderActionsStart:], `</div>`)
	if taskHeaderActionsEnd < 0 {
		t.Fatalf("expected index html to close Current Work header actions")
	}
	taskHeaderActions := markup[taskHeaderActionsStart : taskHeaderActionsStart+taskHeaderActionsEnd]
	taskVisibilityIndex := strings.Index(taskHeaderActions, `id="task-visibility-toggle"`)
	taskFullscreenIndex := strings.Index(taskHeaderActions, `id="task-fullscreen-toggle"`)
	if taskVisibilityIndex < 0 || taskFullscreenIndex < 0 || taskVisibilityIndex <= taskFullscreenIndex {
		t.Fatalf("expected task minimize control to render to the right of the expanded-mode control")
	}
	if !strings.Contains(markup, `const TASK_STATUS_FILTER_KEY = "hubui.taskStatusFilter";`) {
		t.Fatalf("expected index html to define a dedicated persisted task status filter storage key")
	}
	if !strings.Contains(markup, `const TASK_HISTORY_UNSEEN_KEY = "hubui.taskHistoryUnseen.v1";`) {
		t.Fatalf("expected index html to define a dedicated unseen completed-task history storage key")
	}
	if !strings.Contains(markup, `const TASK_SOUND_MUTED_KEY = "hubui.taskSoundMuted";`) {
		t.Fatalf("expected index html to define a dedicated persisted task sound mute storage key")
	}
	if !strings.Contains(markup, "taskStatusFilter: loadTaskStatusFilter(),") {
		t.Fatalf("expected index html to hydrate task status filter from local storage on startup")
	}
	if !strings.Contains(markup, "taskHistoryUnseenIDs: loadTaskHistoryUnseenIDs(),") {
		t.Fatalf("expected index html to hydrate unseen completed task history ids from local storage on startup")
	}
	if !strings.Contains(markup, "taskSoundMuted: loadTaskSoundMuted(),") {
		t.Fatalf("expected index html to hydrate task sound mute state from local storage on startup")
	}
	if !strings.Contains(markup, "function setTaskStatusFilter(filter, options = {})") {
		t.Fatalf("expected index html to include task status filter switching")
	}
	if !strings.Contains(markup, "function rememberTaskHistoryUnseen(requestID)") || !strings.Contains(markup, "function clearTaskHistoryUnseen()") {
		t.Fatalf("expected index html to include unseen completed-task history tracking helpers")
	}
	if !strings.Contains(markup, "function syncTaskCompletionSounds(snapshot)") {
		t.Fatalf("expected index html to include task completion sound transition tracking")
	}
	if !strings.Contains(markup, "function playTaskSuccessSound()") || !strings.Contains(markup, "function playTaskErrorSound()") {
		t.Fatalf("expected index html to define distinct success and error task sounds")
	}
	if !strings.Contains(markup, "taskHistoryToggle.addEventListener(\"click\", () => {") ||
		!strings.Contains(markup, "const nextFilter = normalizeTaskStatusFilter(state.taskStatusFilter) === \"completed\"") {
		t.Fatalf("expected index html to wire history icon toggle interactions for completed-history filtering")
	}
	if !strings.Contains(markup, "taskSoundToggle.addEventListener(\"click\", () => {") {
		t.Fatalf("expected index html to wire task sound toggle interactions")
	}
	if !strings.Contains(markup, "document.addEventListener(\"click\", primeTaskAudioContextFromInteraction, true);") {
		t.Fatalf("expected index html to prime task audio context from click interactions")
	}
	if !strings.Contains(markup, "setTaskStatusFilter(nextFilter);") {
		t.Fatalf("expected index html history toggle to switch task filter between running and completed")
	}
	if !strings.Contains(markup, "const unseenHistoryCount = taskHistoryUnseenCount();") ||
		!strings.Contains(markup, "taskHistoryToggle.classList.toggle(\"task-history-toggle-unseen\", hasUnseenHistory);") ||
		!strings.Contains(markup, "taskHistoryToggleBadge.textContent = `${Math.min(99, unseenHistoryCount)}`;") ||
		!strings.Contains(markup, "taskHistoryToggleBadge.classList.toggle(\"hidden\", !hasUnseenHistory);") {
		t.Fatalf("expected index html to surface unseen completed history with highlighted history toggle unread-count badge")
	}
	if !strings.Contains(markup, "function taskHistoryUnseenCount() {") {
		t.Fatalf("expected index html to include helper for unseen completed-task history count")
	}
	if !strings.Contains(markup, "function runningTasks(snapshot)") || !strings.Contains(markup, "function completedTasks(snapshot)") {
		t.Fatalf("expected index html to derive running and completed task lists independently")
	}
	if strings.Contains(markup, "function completedHistoryEmptyState(snapshot)") ||
		strings.Contains(markup, "return `Amazing job: ${safeCount} ${taskWord} running.`;") ||
		strings.Contains(markup, "No completed tasks yet, but this session already has work in flight.") ||
		strings.Contains(markup, "Queue another focused task when ready.") {
		t.Fatalf("expected completed-history empty state to avoid running-task status prompts during active runs")
	}
	if strings.Contains(markup, "const leadingEmptyState = taskStatusFilter === \"completed\" && completedCount === 0") ||
		strings.Contains(markup, "leadingEmptyState,") {
		t.Fatalf("expected completed-history empty state to render only when the displayed task list is empty")
	}
	if !strings.Contains(markup, "function historyFilterTasks(snapshot)") ||
		!strings.Contains(markup, "return [...runningTasks(snapshot), ...completedTasks(snapshot)];") {
		t.Fatalf("expected index html history filter to include current work plus completed task history")
	}
	if !strings.Contains(markup, "if (filter === \"completed\") {") || !strings.Contains(markup, "return historyFilterTasks(snapshot);") {
		t.Fatalf("expected index html to route displayTasks through the additive history filter when requested")
	}
	if !strings.Contains(markup, "const statusFilter = normalizeTaskStatusFilter(state.taskStatusFilter);") {
		t.Fatalf("expected index html to normalize status filter state before applying task panel metadata")
	}
	if !strings.Contains(markup, "statusFilter,") {
		t.Fatalf("expected index html task render signatures to include status filter state")
	}
	if !strings.Contains(markup, "const nextView = normalizeTaskPanelView(state.taskPanelView) === \"prompt\" ? \"main\" : \"prompt\";") {
		t.Fatalf("expected index html to toggle between prompt-only and detailed task card views")
	}
	if !strings.Contains(markup, `toggleLabel: "Show prompt-only tasks",`) || !strings.Contains(markup, `toggleLabel: "Show detailed tasks",`) {
		t.Fatalf("expected index html to expose prompt-only and detailed task-view toggle labels")
	}
	if !strings.Contains(markup, "taskPanelTitle.textContent = \"Current Work\";") || !strings.Contains(markup, "taskFullscreenPanelTitle.textContent = \"Current Work\";") {
		t.Fatalf("expected index html to keep task panel labels anchored to Current Work across view states")
	}
	if !strings.Contains(markup, `<html lang="en" class="light">`) {
		t.Fatalf("expected index html to default to light theme class")
	}
	if strings.Contains(markup, `max-w-[1500px]`) {
		t.Fatalf("expected index page width to be owned by the global app stylesheet")
	}
	if !strings.Contains(markup, "function isMinimizedTask(") {
		t.Fatalf("expected index html to include completed-task minimization handler")
	}
	if strings.Contains(markup, "MAIN_TASK_ID") || strings.Contains(markup, "MAIN_TASK_LABEL") {
		t.Fatalf("expected index html to remove the tasks history pseudo-task constants")
	}
	if strings.Contains(markup, "default thread") {
		t.Fatalf("expected index html to remove default thread pseudo-task rendering")
	}
	if !strings.Contains(markup, `"task-collapsed"`) {
		t.Fatalf("expected index html to include collapsed task class usage")
	}
	if strings.Contains(markup, `id="task-terminal-toggle"`) {
		t.Fatalf("expected index html to remove the standard output panel toggle")
	}
	if strings.Contains(markup, `id="task-output-panel"`) {
		t.Fatalf("expected index html to remove the standard output panel wrapper")
	}
	if !strings.Contains(markup, `id="task-fullscreen-toggle"`) {
		t.Fatalf("expected index html to include tasks full screen toggle")
	}
	if !strings.Contains(markup, `id="task-view-toggle"`) {
		t.Fatalf("expected index html to include a task-view icon toggle in Current Work header")
	}
	if !strings.Contains(markup, `id="task-history-toggle"`) {
		t.Fatalf("expected index html to include a task-history icon toggle in Current Work header")
	}
	if !strings.Contains(markup, `class="task-history-toggle-icon"`) {
		t.Fatalf("expected index html to render the task-history toggle with an icon affordance")
	}
	if !strings.Contains(markup, `class="task-view-toggle-icon"`) {
		t.Fatalf("expected index html to render the task-view toggle with an icon affordance")
	}
	if strings.Contains(markup, `>Full Screen</button>`) {
		t.Fatalf("expected task fullscreen control to render as an icon instead of button text")
	}
	if !strings.Contains(markup, `class="task-fullscreen-toggle-icon"`) {
		t.Fatalf("expected index html to include the task fullscreen expand icon")
	}
	if !strings.Contains(markup, `id="task-panel"`) {
		t.Fatalf("expected index html to include task panel wrapper")
	}
	if !strings.Contains(markup, `class="panel prompt-wrap`) {
		t.Fatalf("expected index html to include prompt wrap panel")
	}
	if !strings.Contains(markup, `promptWrap.classList.toggle("prompt-collapsed", !visible);`) {
		t.Fatalf("expected index html to toggle collapsed studio state")
	}
	if !strings.Contains(markup, `const fullscreenOpen = Boolean(state.taskFullscreenOpen);`) ||
		!strings.Contains(markup, `taskVisibilityToggle.hidden = fullscreenOpen;`) ||
		!strings.Contains(markup, `taskVisibilityToggle.disabled = fullscreenOpen;`) ||
		!strings.Contains(markup, `taskVisibilityToggle.setAttribute("aria-hidden", fullscreenOpen ? "true" : "false");`) {
		t.Fatalf("expected task minimize control to be unavailable while Current Work is expanded")
	}
	if !strings.Contains(markup, `promptVisibilityToggle.hidden = automatic;`) {
		t.Fatalf("expected index html to keep the studio toggle available outside automatic mode")
	}
	if !strings.Contains(markup, `if (!state.promptVisible && !Boolean(state.ui?.automaticMode)) {`) {
		t.Fatalf("expected index html to auto-expand studio when a mode tab is selected")
	}
	if !strings.Contains(markup, `id="task-panel" class="panel brand-login-card-shell" aria-hidden="false"`) {
		t.Fatalf("expected index html to render the task queue panel with globally owned shell styles")
	}
	if !strings.Contains(markup, `id="task-panel-title" class="panel-section-title">Current Work</span>`) {
		t.Fatalf("expected index html to render a dedicated task panel title node for task-view state synchronization")
	}
	if !strings.Contains(markup, `>Current Work</span>`) {
		t.Fatalf("expected index html to render the task panel under a Current Work heading")
	}
	if !strings.Contains(markup, `id="task-fullscreen-panel-title">Current Work</span>`) {
		t.Fatalf("expected index html to render a dedicated fullscreen task panel title node for task-view state synchronization")
	}
	if !strings.Contains(markup, `id="task-fullscreen-list"`) {
		t.Fatalf("expected index html to include full screen task list")
	}
	if !strings.Contains(markup, `id="task-fullscreen-body"`) {
		t.Fatalf("expected index html to include full screen task body wrapper")
	}
	if !strings.Contains(markup, `id="task-fullscreen-output-panel"`) {
		t.Fatalf("expected index html to include full screen output panel wrapper")
	}
	if !strings.Contains(markup, `id="task-fullscreen-head-label" class="task-fullscreen-head-label"`) {
		t.Fatalf("expected full screen task header to render a dedicated wrapping label class")
	}
	if strings.Contains(markup, `id="task-fullscreen-head-label" class="min-w-0 flex-1 truncate"`) {
		t.Fatalf("expected full screen task header to avoid truncating long prompts")
	}
	if !strings.Contains(markup, `id="task-fullscreen-terminal"`) {
		t.Fatalf("expected index html to include full screen terminal output")
	}
	if !strings.Contains(markup, `id="task-fullscreen-close"`) {
		t.Fatalf("expected index html to include a dedicated full screen close control")
	}
	fullscreenTitleIndex := strings.Index(markup, `id="task-fullscreen-panel-title">Current Work</span>`)
	fullscreenCloseIndex := strings.Index(markup, `id="task-fullscreen-close"`)
	fullscreenListIndex := strings.Index(markup, `id="task-fullscreen-list"`)
	if fullscreenTitleIndex == -1 || fullscreenCloseIndex == -1 || fullscreenListIndex == -1 ||
		fullscreenCloseIndex < fullscreenTitleIndex || fullscreenCloseIndex > fullscreenListIndex {
		t.Fatalf("expected full screen close control to render inside the Current Work panel header")
	}
	if !strings.Contains(markup, `class="task-fullscreen-close-icon"`) || !strings.Contains(markup, `data-lucide="x"`) {
		t.Fatalf("expected index html to render the full screen close control as a lucide X icon button")
	}
	if !strings.Contains(markup, `<span class="sr-only">Close full screen tasks</span>`) {
		t.Fatalf("expected index html to preserve an accessible close label for the full screen icon button")
	}
	if strings.Contains(markup, "task-fullscreen-subtitle") || strings.Contains(markup, "Focused task/running/state view") {
		t.Fatalf("expected index html to omit full screen subtitle copy")
	}
	if strings.Contains(markup, `id="task-history-list"`) {
		t.Fatalf("expected index html to remove prompt history list from tasks panel")
	}
	if strings.Contains(markup, `id="task-count"`) {
		t.Fatalf("expected index html to remove prompt history counter from tasks panel")
	}
	if strings.Contains(markup, "function updatePromptHistory(") {
		t.Fatalf("expected index html to remove prompt history updater")
	}
	if strings.Contains(markup, "function renderPromptHistory(") {
		t.Fatalf("expected index html to remove prompt history renderer")
	}
	if !strings.Contains(markup, "function sortTasksByActivity(") {
		t.Fatalf("expected index html to include activity-based task sorting for list rendering")
	}
	if !strings.Contains(markup, "const STREAM_RENDER_INTERVAL_MS = 120;") {
		t.Fatalf("expected index html to keep stream-driven task transitions responsive at 120ms cadence")
	}
	if !strings.Contains(markup, "const TASK_ORDER_TRANSITION_DELAY_MS = 2_000;") || !strings.Contains(markup, "const TASK_ORDER_SYNC_INTERVAL_MS = 4_000;") {
		t.Fatalf("expected index html to delay and synchronize task reordering transitions")
	}
	if !strings.Contains(markup, "taskOrderPendingDesired: [],") {
		t.Fatalf("expected index html state to track pending desired task order for stable transition delay windows")
	}
	if !strings.Contains(markup, "if (!sameTaskOrder(state.taskOrderPendingDesired, desiredOrder)) {") ||
		!strings.Contains(markup, "state.taskOrderPendingSince = nowMs;") {
		t.Fatalf("expected index html to reset task reorder delay timing whenever the desired order changes")
	}
	if !strings.Contains(markup, "const TASK_REFLOW_TRANSITION_MS = 560;") ||
		!strings.Contains(markup, "const TASK_REFLOW_TRANSITION_EASING = \"cubic-bezier(0.16, 1, 0.3, 1)\";") {
		t.Fatalf("expected index html to define smoother task reflow transition parameters")
	}
	if !strings.Contains(markup, "translate ${TASK_REFLOW_TRANSITION_MS}ms ${TASK_REFLOW_TRANSITION_EASING}") {
		t.Fatalf("expected index html to animate task reflow with translate-based transitions for smoother movement")
	}
	if !strings.Contains(markup, "function applyTaskOrderCadence(tasks)") {
		t.Fatalf("expected index html to include cadence-based task order stabilization")
	}
	if !strings.Contains(markup, "function animateTaskReflow(listNode, previousRects)") {
		t.Fatalf("expected index html to animate task reflow transitions")
	}
	if !strings.Contains(markup, "function captureTaskScrollAnchor(listNode)") ||
		!strings.Contains(markup, "restoreTaskScrollAnchor(listNode, scrollAnchor);") ||
		!strings.Contains(markup, "container.scrollTop += nextRect.top - containerRect.top - topOffset;") {
		t.Fatalf("expected index html to preserve visible task scroll position while task order redraws")
	}
	if strings.Contains(markup, "taskFullscreenBody.classList.toggle(\"task-output-hidden\", !outputVisible);") {
		t.Fatalf("expected index html to remove full screen output visibility toggling")
	}
	if !strings.Contains(markup, "const taskFullscreenClose = document.getElementById(\"task-fullscreen-close\");") {
		t.Fatalf("expected index html to cache the dedicated full screen close control")
	}
	if !strings.Contains(markup, "const QUEUED_STATUS_TIMEOUT_MS = 12_000;") {
		t.Fatalf("expected index html to keep prompt success notifications visible for 12s")
	}
	if !strings.Contains(markup, "const LOCAL_PROMPT_STATUS_FADE_MS = 240;") {
		t.Fatalf("expected index html to define a dedicated prompt-status fade duration")
	}
	if !strings.Contains(markup, "const pasteWidth = Math.min(50, 25 + Math.max(0, imageCount-1) * 6.25);") {
		t.Fatalf("expected index html to size pasted screenshot summary width between 25%% and 50%%")
	}
	if !strings.Contains(markup, "builderImagePasteTargetWrap.style.setProperty(\"--prompt-paste-width\", `${pasteWidth}%`);") {
		t.Fatalf("expected index html to drive pasted screenshot width through the action-row wrapper")
	}
	if !strings.Contains(markup, "builderImagePasteTargetWrap.style.removeProperty(\"flex-basis\");") {
		t.Fatalf("expected index html to keep screenshot lane flex-basis responsive to viewport")
	}
	if !strings.Contains(markup, "const localPromptStatusDefaultText = String(localPromptStatus?.dataset.defaultText || \"\").trim();") {
		t.Fatalf("expected index html to cache the default prompt helper copy for status swaps")
	}
	if !strings.Contains(markup, "function restoreLocalPromptStatusDefault() {") ||
		!strings.Contains(markup, "localPromptStatus.textContent = localPromptStatusDefaultText;") {
		t.Fatalf("expected index html to restore prompt helper copy after status clears")
	}
	if !strings.Contains(markup, "localPromptStatus.classList.add(\"is-fading\");") {
		t.Fatalf("expected index html to fade prompt success notifications before clearing them")
	}
	if !strings.Contains(markup, "localPromptStatus.className = kind ? `submit-status submit-status-inline ${kind}` : \"submit-status submit-status-inline\";") {
		t.Fatalf("expected index html to preserve the inline prompt-status layout classes when updating text")
	}
	if !strings.Contains(markup, "renderTaskCollection(tasks, taskFullscreenList, null, {") {
		t.Fatalf("expected index html to render the full task list in fullscreen mode")
	}
	if strings.Contains(markup, "renderTaskCollection(selected ? [selected] : [], taskFullscreenList, null, {") {
		t.Fatalf("expected index html to stop collapsing fullscreen mode to a single selected task")
	}
	if !strings.Contains(markup, "taskFullscreenClose.classList.toggle(\"hidden\", !state.taskFullscreenOpen);") {
		t.Fatalf("expected index html to toggle dedicated full screen close visibility")
	}
	if !strings.Contains(markup, "taskFullscreenClose.addEventListener(\"click\", () => {") {
		t.Fatalf("expected index html to bind the dedicated full screen close control")
	}
	if !strings.Contains(markup, "if (event.key === \"Escape\" && state.taskFullscreenOpen) {") {
		t.Fatalf("expected index html to close full screen tasks on Escape")
	}
	if !strings.Contains(markup, "event.preventDefault();") {
		t.Fatalf("expected index html to treat Escape as a modal-dismiss action for full screen tasks")
	}
	if strings.Contains(markup, "function setTaskOutputPanelVisibility(") {
		t.Fatalf("expected index html to remove standard output panel visibility handler")
	}
	if strings.Contains(markup, "rightCol.classList.toggle(\"task-output-hidden\", !outputVisible);") {
		t.Fatalf("expected index html to remove standard layout output hiding")
	}
	if !strings.Contains(markup, "rightCol.classList.toggle(\"task-list-hidden\", !hasTaskPanelTasks);") {
		t.Fatalf("expected index html to toggle the task layout from task panel content state")
	}
	if !strings.Contains(markup, "function hasTaskPanelContent(snapshot) {") ||
		!strings.Contains(markup, "const runningCount = runningTasks(snapshot).length;") ||
		!strings.Contains(markup, "const completedCount = completedTasks(snapshot).length;") ||
		!strings.Contains(markup, "return normalizeTaskStatusFilter(state.taskStatusFilter) === \"completed\" && runningCount === 0 && completedCount === 0;") {
		t.Fatalf("expected index html to keep completed-history empty state visible only in completed-history view")
	}
	if !strings.Contains(markup, `const showTaskPanel = state.appDisplay === "studio" || state.appDisplay === "chat";`) ||
		!strings.Contains(markup, "taskPanel.classList.toggle(\"hidden\", !showTaskPanel);") {
		t.Fatalf("expected index html to show the task queue panel only on work-oriented app views")
	}
	if !strings.Contains(markup, "taskPanel.setAttribute(\"aria-hidden\", showTaskPanel ? \"false\" : \"true\");") {
		t.Fatalf("expected index html to keep task panel aria visibility in sync with rendered content")
	}
	if !strings.Contains(markup, "openTaskOutput(requestID);") {
		t.Fatalf("expected index html to open focused full screen output from the task action")
	}
	if strings.Contains(markup, "Output hidden. Click Open Output to view terminal logs.") {
		t.Fatalf("expected index html to remove hidden-output placeholder copy")
	}
	if strings.Contains(markup, "stage.textContent = `stage:") {
		t.Fatalf("expected index html to remove stage metadata line from task cards")
	}
	if !strings.Contains(markup, "branch.textContent = `branch: ${formatTaskBranch(task)}`;") {
		t.Fatalf("expected index html to render branch metadata in task cards")
	}
	if !strings.Contains(markup, `update.className = "task-timing-summary";`) || !strings.Contains(markup, "applyTaskTimingSummary(update, task);") {
		t.Fatalf("expected index html to render task updated/started timing summary in a dedicated node")
	}
	if strings.Contains(markup, "return `${id} | ${preview}`;") {
		t.Fatalf("expected index html to remove request id prefix from task display title")
	}
	if strings.Contains(markup, "const TASK_PROMPT_PREVIEW_MAX = 30;") {
		t.Fatalf("expected index html to avoid fixed prompt preview length caps in task titles")
	}
	if strings.Contains(markup, "function taskPromptPreview(") {
		t.Fatalf("expected index html to remove fixed-length task prompt preview helper")
	}
	if !strings.Contains(markup, "const prompt = taskPromptText(task);") || !strings.Contains(markup, "return prompt;") {
		t.Fatalf("expected index html to pass full task prompt text to task title truncation")
	}
	if !strings.Contains(markup, `if (!task || typeof task.prompt !== "string") return "";`) {
		t.Fatalf("expected index html to display available task prompt text")
	}
	if !strings.Contains(markup, `if (requestID.endsWith("-failure-review")) {`) || !strings.Contains(markup, `return "Failure review";`) {
		t.Fatalf("expected index html to give failure follow-up tasks a neutral Current Work label")
	}
	if !strings.Contains(markup, `return taskSystemLabel(task) || "Task";`) {
		t.Fatalf("expected index html to provide task labels without unavailable prompt copy")
	}
	if !strings.Contains(markup, "id.title = prompt;") {
		t.Fatalf("expected index html task title tooltip to contain prompt text only")
	}
	if !strings.Contains(markup, "const promptOnly = normalizeTaskPanelView(state.taskPanelView) === \"prompt\";") {
		t.Fatalf("expected index html to compute a prompt-only task rendering mode")
	}
	if !strings.Contains(markup, "node.classList.add(\"task-prompt-only\");") {
		t.Fatalf("expected index html to apply prompt-only card styling when compact task view is active")
	}
	promptOnlyStart := strings.Index(markup, "if (promptOnly) {")
	if promptOnlyStart < 0 {
		t.Fatalf("expected index html to render a prompt-only task branch")
	}
	promptOnlyCloseAction := strings.Index(markup[promptOnlyStart:], "const closeAction = renderTaskCloseButton(task, requestID);")
	if promptOnlyCloseAction < 0 {
		t.Fatalf("expected index html to render a prompt-only task branch with close actions")
	}
	promptOnlyCloseAction += promptOnlyStart
	if strings.Contains(markup[promptOnlyStart:promptOnlyCloseAction], "renderOutputToggle") {
		t.Fatalf("expected index html prompt-only mode to hide terminal output controls")
	}
	if !strings.Contains(markup[promptOnlyStart:promptOnlyCloseAction], "const githubAction = renderTaskPullRequestAction(task);") {
		t.Fatalf("expected index html prompt-only mode to include completed-task GitHub pull request links")
	}
	if !strings.Contains(markup, "function githubRepoPathFromValue(value)") ||
		!strings.Contains(markup, "function taskRepoRootURL(task)") ||
		!strings.Contains(markup, "function taskGitHubLinkURL(task)") ||
		!strings.Contains(markup, "function taskPullRequestURL(task)") ||
		!strings.Contains(markup, "return `https://github.com/${repoPath}`;") {
		t.Fatalf("expected index html to derive unfinished task GitHub links from repository roots")
	}
	if !strings.Contains(markup, "if (prURL) {") ||
		!strings.Contains(markup, "return prURL;") {
		t.Fatalf("expected index html to prefer pull-request links as soon as a PR URL is available")
	}
	if !strings.Contains(markup, "function renderTaskPullRequestAction(task)") ||
		!strings.Contains(markup, `link.className = "task-github-link";`) ||
		!strings.Contains(markup, `link.href = prURL;`) ||
		!strings.Contains(markup, `logo.src = GITHUB_LOGO_URL;`) ||
		strings.Count(markup, "const githubAction = renderTaskPullRequestAction(task);") < 2 {
		t.Fatalf("expected completed task cards to render GitHub-logo pull request links")
	}
	if !strings.Contains(markup, "function openExternalTaskLink(event, url, trackOpen)") ||
		!strings.Contains(markup, `window.open(url, "_blank", "noopener,noreferrer");`) ||
		strings.Count(markup, "openExternalTaskLink(event, prURL") < 2 ||
		!strings.Contains(markup, "openExternalTaskLink(event, githubHref") {
		t.Fatalf("expected task GitHub links to open on the first primary click")
	}
	if !strings.Contains(markup, `detail.textContent = prURL ? "Pull request ready." : "Completed.";`) ||
		!strings.Contains(markup, `link.className = "task-result-link task-result-github-link";`) ||
		!strings.Contains(markup, `label.textContent = "Open PR";`) {
		t.Fatalf("expected completed task result banners to include pull request links")
	}
	if !strings.Contains(markup, `const isGitHubStep = String(step?.icon || "").trim().toLowerCase() === "github";`) ||
		!strings.Contains(markup, `const githubHref = isGitHubStep ? taskGitHubLinkURL(task) : "";`) ||
		!strings.Contains(markup, `const marker = document.createElement(githubHref ? "a" : "span");`) ||
		!strings.Contains(markup, `marker.href = githubHref;`) {
		t.Fatalf("expected index html to link the final GitHub progress step to the repo before PR creation and the PR once available")
	}
	if strings.Contains(markup, "function createTaskGitHubLink(") ||
		strings.Contains(markup, "taskGitHubLinkIsPR") ||
		strings.Contains(markup, "task-pr-link") {
		t.Fatalf("expected index html to remove standalone task GitHub link rendering")
	}
	if !strings.Contains(markup, "const closeAction = renderTaskCloseButton(task, requestID);") {
		t.Fatalf("expected index html prompt-only mode to include completed-task close actions")
	}
	if !strings.Contains(markup, "completeTaskDismissal(requestID);") || !strings.Contains(markup, "Removed task ${requestID} from history") {
		t.Fatalf("expected index html to clear history-only tasks locally when close is clicked")
	}
	if !strings.Contains(markup, "const showTaskCloneAction = canCopyTaskCloneCommand(task);") ||
		!strings.Contains(markup, "const inlineCompletedActions = isCompletedTask(task) && showTaskCloneAction;") ||
		!strings.Contains(markup, "const showTaskSideActions = showTaskCloneAction && !inlineCompletedActions;") {
		t.Fatalf("expected index html to inline completed-task clone actions and gate side actions to remaining clone actions")
	}
	if !strings.Contains(markup, "const TASK_SIDE_ACTION_SIZE_PX = \"34px\";") {
		t.Fatalf("expected index html to define a stable runtime width for task side actions")
	}
	if !strings.Contains(markup, "node.classList.toggle(\"task-has-side-actions\", showTaskSideActions);") {
		t.Fatalf("expected index html to mark task cards with right-side side-action rails")
	}
	if !strings.Contains(markup, "cloneButton.className = \"task-copy-link\";") ||
		!strings.Contains(markup, "const cloneLogo = createTaskCloneIcon();") ||
		!strings.Contains(markup, "void copyTaskCloneCommand(task, cloneButton);") {
		t.Fatalf("expected index html to render a terminal icon button that copies the branch clone command")
	}
	if !strings.Contains(markup, `cloneButton.title = "Clone locally to test and review changes.";`) ||
		!strings.Contains(markup, `cloneButton.setAttribute("aria-label", "Clone locally to test and review changes.");`) {
		t.Fatalf("expected index html to render the requested terminal icon hover copy")
	}
	if !strings.Contains(markup, "body.className = \"task-body\";") {
		t.Fatalf("expected index html to render a task body container alongside side actions")
	}
	if !strings.Contains(markup, "if (inlineCompletedActions) {") ||
		!strings.Contains(markup, "topActions.appendChild(cloneButton);") {
		t.Fatalf("expected index html to place completed-task clone actions inline")
	}
	taskActionsStart := strings.Index(markup, "const outputAction = renderOutputToggle(requestID);")
	if taskActionsStart < 0 {
		t.Fatalf("expected index html to render task action controls")
	}
	taskActionsEnd := strings.Index(markup[taskActionsStart:], "top.append(id);")
	if taskActionsEnd < 0 {
		t.Fatalf("expected index html to finish task action controls before appending task title")
	}
	taskActionsMarkup := markup[taskActionsStart : taskActionsStart+taskActionsEnd]
	actionOutputIndex := strings.Index(taskActionsMarkup, "topActions.appendChild(outputAction);")
	actionCloneIndex := strings.Index(taskActionsMarkup, "topActions.appendChild(cloneButton);")
	actionRerunIndex := strings.Index(taskActionsMarkup, "topActions.appendChild(rerun);")
	actionGitHubIndex := strings.Index(taskActionsMarkup, "topActions.appendChild(githubAction);")
	actionCloseIndex := strings.Index(taskActionsMarkup, "topActions.appendChild(close);")
	if actionOutputIndex < 0 || actionCloneIndex < 0 || actionRerunIndex < 0 || actionGitHubIndex < 0 || actionCloseIndex < 0 ||
		!(actionOutputIndex < actionCloneIndex && actionCloneIndex < actionRerunIndex && actionRerunIndex < actionGitHubIndex && actionGitHubIndex < actionCloseIndex) {
		t.Fatalf("expected completed task action order: terminal output, git clone, rerun, GitHub link, close")
	}
	if !strings.Contains(markup, "async function copyTextToClipboard(value, buttonNode, options = {}) {") ||
		!strings.Contains(markup, "const preserveContents = Boolean(options && options.preserveContents);") ||
		!strings.Contains(markup, "buttonNode.classList.add(\"is-copied\");") {
		t.Fatalf("expected index html to preserve icon-only copy buttons while showing copied feedback")
	}
	if !strings.Contains(markup, `document.getElementById("local-conn-text")`) {
		t.Fatalf("expected index html to bind the shared local connection indicator")
	}
	if !strings.Contains(markup, `window.MoltenHubHeader.updateLocalConnection(online, text);`) {
		t.Fatalf("expected index html to update local indicator through shared header component")
	}
	if !strings.Contains(markup, `document.getElementById("hub-conn-text")`) {
		t.Fatalf("expected index html to bind the shared moltenhub connection indicator")
	}
	if !strings.Contains(markup, `window.MoltenHubHeader.updateConnectionStatus(snapshot, { hubSetup: state.hubSetup });`) {
		t.Fatalf("expected index html to update hub indicator through shared header component")
	}
	if !strings.Contains(markup, `setIndicator(hubConnItem, hubConnDot, hubConnText, "Molten Hub", online, text);`) {
		t.Fatalf("expected index html to update hub indicator tooltip copy")
	}
	if !strings.Contains(markup, `setIndicator(localConnItem, localConnDot, localConnText, "Local", online, text);`) {
		t.Fatalf("expected index html to render local indicator label as Local")
	}
	if !strings.Contains(markup, `setIndicator(hubConnItem, hubConnDot, hubConnText, "Molten Hub", online, text);`) {
		t.Fatalf("expected index html to render hub indicator label as Molten Hub")
	}
	if !strings.Contains(markup, `const online = connected;`) {
		t.Fatalf("expected index html to style transport-pending connected hub states as online")
	}
	if !strings.Contains(markup, `const actionTone = connected ? "online" : (mode === "disconnected" ? "offline" : "");`) {
		t.Fatalf("expected index html to derive hub action styling from connection state")
	}
	if !strings.Contains(markup, `hubConnItem.classList.toggle("status-item-action-online", actionable && tone === "online");`) {
		t.Fatalf("expected index html to apply online action styling for connected hub states")
	}
	if !strings.Contains(markup, `hubConnItem.classList.toggle("status-item-action-offline", actionable && tone === "offline");`) {
		t.Fatalf("expected index html to preserve offline action styling for disconnected hub states")
	}
	if !strings.Contains(markup, "function applyHubDotMode(") {
		t.Fatalf("expected index html to include hub transport dot mode handler")
	}
	if !strings.Contains(markup, "conn.hub_transport") {
		t.Fatalf("expected index html to read hub transport mode from connection state")
	}
	if !strings.Contains(markup, "conn.hub_detail") {
		t.Fatalf("expected index html to read hub connection detail from connection state")
	}
	if !strings.Contains(markup, "Connected via WebSocket") {
		t.Fatalf("expected index html to include websocket connection copy")
	}
	if !strings.Contains(markup, "Connected via HTTP long polling") {
		t.Fatalf("expected index html to include HTTP long-polling connection copy")
	}
	if !strings.Contains(markup, "Hub endpoint is waking up") {
		t.Fatalf("expected index html to include ping retry connection copy")
	}
	if !strings.Contains(markup, "Hub endpoint is live at") {
		t.Fatalf("expected index html to include ping reachable connection copy")
	}
	if !strings.Contains(markup, `const HUB_LOGIN_URL = "https://molten.bot/login?target=hub";`) {
		t.Fatalf("expected index html to define the molten hub login url for disconnected runtimes")
	}
	if !strings.Contains(markup, `const HUB_DASHBOARD_URL = "https://app.molten.bot/hub";`) {
		t.Fatalf("expected index html to define the molten hub dashboard url for connected runtimes")
	}
	if !strings.Contains(markup, `text = state.hubSetup.configured`) {
		t.Fatalf("expected index html to tailor disconnected hub copy based on saved setup state")
	}
	if !strings.Contains(markup, `hubConnItem.addEventListener("click", maybeOpenHubConnectPage);`) {
		t.Fatalf("expected index html to open the molten hub app when the disconnected indicator is clicked")
	}
	if !strings.Contains(markup, `window.open(hubURL, "_blank", "noopener,noreferrer");`) {
		t.Fatalf("expected index html to open the current molten hub target in a new page")
	}
	if !strings.Contains(markup, `const targetURL = connected || state.hubSetup.configured`) {
		t.Fatalf("expected index html to switch hub indicator targets based on connection state")
	}
	if !strings.Contains(markup, `hubConnItem.setAttribute("data-href", href);`) {
		t.Fatalf("expected index html to persist the current hub target url on the indicator")
	}
	if !strings.Contains(markup, "setHubConnection(\"connected\", `Connected to ${target} (transport pending)`);") {
		t.Fatalf("expected index html to treat transport-pending hub state as connected for dashboard linking")
	}
	if !strings.Contains(markup, `hubConnItem.classList.toggle("status-item-action", actionable);`) {
		t.Fatalf("expected index html to mark actionable hub indicator states")
	}
	if !strings.Contains(markup, `id="prompt-visibility-toggle"`) {
		t.Fatalf("expected index html to include studio visibility toggle")
	}
	if !strings.Contains(markup, `aria-label="Minimize Studio panel"`) || !strings.Contains(markup, `title="Minimize Studio panel">▾</button>`) {
		t.Fatalf("expected index html to initialize the studio toggle as an arrow minimize control")
	}
	if !strings.Contains(markup, `id="prompt-panel-title" class="panel-section-title prompt-title-list"`) {
		t.Fatalf("expected index html to render compact Studio mode title list")
	}
	if !strings.Contains(markup, "library-task-option-subtitle") {
		t.Fatalf("expected index html to include library task subtitles")
	}
	if !strings.Contains(markup, "button.setAttribute(\"aria-pressed\", String(entry.name === selected));") {
		t.Fatalf("expected index html to mark the selected library task")
	}
	if !strings.Contains(markup, `id="library-prompt-modal"`) ||
		!strings.Contains(markup, `role="dialog"`) ||
		!strings.Contains(markup, `promptLink.className = "library-task-option-link";`) ||
		!strings.Contains(markup, `promptLink.title = "Open full prompt";`) ||
		!strings.Contains(markup, `openLibraryPromptModal(entry);`) ||
		!strings.Contains(markup, `libraryPromptModalBody.textContent = entry.prompt || "No prompt text provided.";`) {
		t.Fatalf("expected index html to show full library prompts in a modal from each task link button")
	}
	if strings.Contains(markup, "library-task-option-name") {
		t.Fatalf("expected index html to stop rendering library task internal names")
	}
	if !strings.Contains(markup, `document.getElementById("resource-metrics-text")`) {
		t.Fatalf("expected index html to bind the shared resource metrics indicator")
	}
	if !strings.Contains(markup, `document.getElementById("resource-metrics-item")`) {
		t.Fatalf("expected index html to bind the shared metrics pill wrapper")
	}
	if !strings.Contains(markup, `document.getElementById("resource-cpu-chip")`) ||
		!strings.Contains(markup, `document.getElementById("resource-mem-chip")`) ||
		!strings.Contains(markup, `document.getElementById("resource-disk-chip")`) {
		t.Fatalf("expected index html to bind the shared cpu/mem/io metric chip wrappers")
	}
	if !strings.Contains(markup, "resourceCPUChip.hidden = !showCPU;") ||
		!strings.Contains(markup, "resourceMemChip.hidden = !showMem;") ||
		!strings.Contains(markup, "resourceDiskChip.hidden = !showDisk;") {
		t.Fatalf("expected index html to hide metric chips until valid samples are observed")
	}
	if strings.Contains(markup, `text-slate-200`) {
		t.Fatalf("expected index html to remove hardcoded dark text utilities from studio and status surfaces")
	}
	if strings.Contains(markup, `bg-[#0d1825]`) || strings.Contains(markup, `bg-[#0c1724]`) || strings.Contains(markup, `bg-black/15`) {
		t.Fatalf("expected index html to remove hardcoded dark background utilities from studio surfaces")
	}
	if !strings.Contains(markup, "function renderHubConnection(") {
		t.Fatalf("expected index html to include renderHubConnection handler")
	}
	if !strings.Contains(markup, "function renderResourceMetrics(") {
		t.Fatalf("expected index html to include renderResourceMetrics handler")
	}
	if !strings.Contains(markup, "function markMetricVisible(") || !strings.Contains(markup, "function setMetricChipVisibility(") {
		t.Fatalf("expected index html to include session-sticky metric visibility helpers")
	}
	if !strings.Contains(markup, "resourceMetricsItem.hidden = !(showCPU || showMem || showDisk);") {
		t.Fatalf("expected index html to hide the full metrics pill when no metric has valid samples")
	}
	if !strings.Contains(markup, "const showDisk = markMetricVisible(\"disk\", disk);") {
		t.Fatalf("expected index html to keep I/O metric hidden until a non-zero sample arrives")
	}
	if !strings.Contains(markup, "function formatCompactMetricNumber(") {
		t.Fatalf("expected index html to include compact metric formatter")
	}
	if !strings.Contains(markup, `document.getElementById("resource-cpu-text")`) {
		t.Fatalf("expected index html to bind the CPU metric value inside the shared metrics wrapper")
	}
	if !strings.Contains(markup, `document.getElementById("local-conn-item")`) {
		t.Fatalf("expected index html to bind the shared local connection pill")
	}
	if !strings.Contains(markup, `document.getElementById("hub-conn-item")`) {
		t.Fatalf("expected index html to bind the shared hub connection pill")
	}
	if !strings.Contains(markup, `document.getElementById("resource-mem-text")`) {
		t.Fatalf("expected index html to bind the memory metric value inside the shared metrics wrapper")
	}
	if !strings.Contains(markup, `document.getElementById("resource-metrics-text")`) {
		t.Fatalf("expected index html to bind the I/O metric value inside the shared metrics wrapper")
	}
	if !strings.Contains(markup, `document.getElementById("resource-metrics-unit")`) {
		t.Fatalf("expected index html to bind the shared disk throughput unit element")
	}
	if !strings.Contains(markup, `resourceMetricsUnit.textContent = "MB/s";`) {
		t.Fatalf("expected index html to reset the disk throughput unit to MB/s when needed")
	}
	if strings.Contains(markup, "metric-label-visible") || strings.Contains(markup, "metric-unit-visible") {
		t.Fatalf("expected index html to keep metric labels and units hidden until the status row is hovered")
	}
	if !strings.Contains(markup, "function formatDiskThroughput(") {
		t.Fatalf("expected index html to include a disk throughput formatter")
	}
	if !strings.Contains(markup, `unit: "KB/s"`) || !strings.Contains(markup, `unit: "GB/s"`) {
		t.Fatalf("expected index html to scale disk throughput units between KB/s, MB/s, and GB/s")
	}
	if !strings.Contains(markup, "resourceMetricsUnit.textContent = diskThroughput.unit;") {
		t.Fatalf("expected index html to update the rendered disk throughput unit dynamically")
	}
	if !strings.Contains(markup, "if (value <= 85) return \"metric-icon-warn\";") {
		t.Fatalf("expected index html to keep 85%% usage in the warning icon range")
	}
	if !strings.Contains(markup, "setMetricIcon(resourceDiskIcon, disk);") {
		t.Fatalf("expected index html to color the I/O icon using the same metric severity thresholds")
	}
	if strings.Contains(markup, "setMetricIcon(resourceDiskIcon, NaN);") {
		t.Fatalf("expected index html to stop forcing the I/O icon to neutral")
	}
	if !strings.Contains(markup, `id="prompt-mode-builder"`) {
		t.Fatalf("expected index html to include builder mode toggle")
	}
	if !strings.Contains(markup, `id="prompt-mode-builder" class="prompt-mode-link active" href="#studio-builder" aria-selected="true" title="Prompt"`) {
		t.Fatalf("expected index html to render Prompt as the primary dock icon action")
	}
	chatDockIndex := strings.Index(markup, `data-app-display="chat"`)
	promptDockIndex := strings.Index(markup, `id="prompt-mode-builder"`)
	if chatDockIndex < 0 || promptDockIndex < 0 || chatDockIndex > promptDockIndex {
		t.Fatalf("expected Chat to render before Prompt in the bottom dock")
	}
	if !strings.Contains(markup, `<span class="prompt-mode-link-tooltip" aria-hidden="true">Prompt</span>`) {
		t.Fatalf("expected index html to expose Prompt through dock tooltip text")
	}
	if !strings.Contains(markup, `id="prompt-mode-library"`) {
		t.Fatalf("expected index html to include library mode toggle")
	}
	if !strings.Contains(markup, `id="prompt-mode-json"`) {
		t.Fatalf("expected index html to include json mode toggle")
	}
	if !strings.Contains(markup, "function activatePromptMode(mode)") ||
		!strings.Contains(markup, `setPromptMode(nextMode);`) ||
		!strings.Contains(markup, `setAppDisplay("studio", { updateHash: promptHashFromMode(nextMode) });`) ||
		!strings.Contains(markup, `activatePromptMode("library");`) ||
		!strings.Contains(markup, `activatePromptMode("json");`) {
		t.Fatalf("expected bottom dock Library and JSON controls to activate their Studio views through the shared mode path")
	}
	if !strings.Contains(markup, `let mode = display === "dashboard" || display === "chat" ? display : "studio";`) ||
		!strings.Contains(markup, `if (mode === "chat" && !state.githubReposReady) {`) ||
		!strings.Contains(markup, `mode = "studio";`) ||
		!strings.Contains(markup, `appLayout.hidden = false;`) ||
		!strings.Contains(markup, `promptWrap.hidden = !showStudio;`) ||
		!strings.Contains(markup, `const showTaskPanel = state.appDisplay === "studio" || state.appDisplay === "chat";`) {
		t.Fatalf("expected index html to switch main views while hiding Current Work on dashboard")
	}
	if !strings.Contains(markup, `function syncTaskVisibilityForAppDisplay()`) ||
		!strings.Contains(markup, "if (state.appDisplay === \"chat\") {\n        state.taskVisible = false;\n        return;\n      }") ||
		!strings.Contains(markup, "if (state.appDisplay === \"studio\") {\n        state.taskVisible = true;\n      }") ||
		!strings.Contains(markup, `syncTaskVisibilityForAppDisplay();`) {
		t.Fatalf("expected index html to minimize Current Work on chat entry while expanding it in Studio views")
	}
	if !strings.Contains(markup, `function syncPromptTitleModes()`) ||
		!strings.Contains(markup, `item.hidden = !active;`) ||
		!strings.Contains(markup, `promptPanelTitle.setAttribute("aria-label", label);`) {
		t.Fatalf("expected index html to update and hide inactive compact Studio mode headings when the prompt mode changes")
	}
	if !strings.Contains(markup, `id="prompt-mode-builder" class="prompt-mode-link active" href="#studio-builder" aria-selected="true"`) {
		t.Fatalf("expected builder mode to render as an anchor-style control inside the shared segmented dock")
	}
	if !strings.Contains(markup, `id="prompt-mode-library" class="prompt-mode-link" href="#studio-library" aria-selected="false"`) {
		t.Fatalf("expected library mode to render as an anchor-style control inside the shared segmented dock")
	}
	if !strings.Contains(markup, `id="prompt-mode-json" class="prompt-mode-link" href="#studio-json" aria-selected="false"`) {
		t.Fatalf("expected json mode to render as an anchor-style control inside the shared segmented dock")
	}
	if !strings.Contains(markup, `class="page-bottom-dock"`) || !strings.Contains(markup, `class="prompt-mode-tabs prompt-mode-tabs-dock"`) {
		t.Fatalf("expected index html to render the mode toggles in the bottom dock")
	}
	if strings.Contains(markup, `class="prompt-mode-tabs prompt-mode-tabs-panel"`) {
		t.Fatalf("expected Studio mode toggles to move out of the panel header")
	}
	if !strings.Contains(markup, `aria-label="Main menu"`) {
		t.Fatalf("expected index html to expose the shared dock as the main menu")
	}
	if !strings.Contains(markup, `id="github-profile-link"`) ||
		!strings.Contains(markup, `href="https://github.com/settings/profile"`) ||
		!strings.Contains(markup, `target="_blank"`) {
		t.Fatalf("expected index html to render an integrated GitHub dock link that opens in a new window")
	}
	if !strings.Contains(markup, `fetch("/api/github/profile", { cache: "no-store" })`) {
		t.Fatalf("expected index html to resolve the authenticated GitHub public profile through the hub ui api")
	}
	if !strings.Contains(markup, `void loadHubSetupStatus().then(() => {`) ||
		!strings.Contains(markup, `void loadAgentAuthStatus();`) ||
		!strings.Contains(markup, `void loadGitHubProfileLink();`) ||
		!strings.Contains(markup, `void loadLibraryTasks();`) {
		t.Fatalf("expected index html startup to load validation and setup data in the background")
	}
	if !strings.Contains(markup, `const chatDockLink = document.querySelector('[data-app-display="chat"]');`) ||
		!strings.Contains(markup, `chatDockLink.setAttribute("aria-disabled", String(!available));`) ||
		!strings.Contains(markup, `chatDockLink.removeAttribute("href");`) ||
		!strings.Contains(markup, `let githubReposLoadPromise = null;`) ||
		!strings.Contains(markup, `const response = await fetch("/api/github/repos", { cache: "no-store" });`) ||
		!strings.Contains(markup, `state.githubRepos = Array.isArray(body.repos) ? body.repos : [];`) ||
		!strings.Contains(markup, `const allRepos = Array.isArray(state.githubRepos) ? state.githubRepos : [];`) ||
		!strings.Contains(markup, `const CHAT_REPOS_MAX_PER_PAGE = 15;`) ||
		!strings.Contains(markup, `function chatReposPerPage()`) ||
		!strings.Contains(markup, `const pageRepos = listedRepos.slice(start, start + reposPerPage);`) ||
		!strings.Contains(markup, `function renderChatRepoPagination(totalRepos, totalPages)`) ||
		!strings.Contains(markup, `if (!state.githubReposReady) {`) ||
		!strings.Contains(markup, `id="chat-repo-search" class="chat-search-input" type="search"`) ||
		!strings.Contains(markup, `const chatRepoSearch = document.getElementById("chat-repo-search");`) ||
		!strings.Contains(markup, `function filterChatRepos(repos, query)`) ||
		!strings.Contains(markup, `state.chatRepoSearchQuery = chatRepoSearch.value;`) ||
		!strings.Contains(markup, `empty.textContent = state.chatRepoSearchQuery ? "No repositories match search."`) {
		t.Fatalf("expected index html to gate chat availability on GitHub repository loading")
	}
	if strings.Contains(markup, `const releaseDockLink = document.querySelector('[data-app-display="releases"]');`) ||
		strings.Contains(markup, `function syncReleaseDockLinkAvailability`) ||
		strings.Contains(markup, `releases_open_blocked`) {
		t.Fatalf("expected index html to omit release dock availability handling")
	}
	if !strings.Contains(markup, `id="chat-repo-tabs"`) ||
		!strings.Contains(markup, `function chatPromptedRepoTabs()`) ||
		!strings.Contains(markup, `state.snapshot.prompted_repos`) ||
		!strings.Contains(markup, `reposIcon.innerHTML = `+"`"+`<i data-lucide="brick-wall" aria-hidden="true"></i>`+"`"+`;`) ||
		!strings.Contains(markup, `reposLabel.textContent = "All repositories";`) ||
		!strings.Contains(markup, `function selectedChatRepo(repos, selectedTab)`) ||
		!strings.Contains(markup, `chatRepoGrid.classList.toggle("chat-repo-grid-active-repo", Boolean(openRepo));`) ||
		!strings.Contains(markup, `const viewingRepoChat = Boolean(selectedTab && openRepo);`) ||
		!strings.Contains(markup, `displayRepos = [openRepo];`) {
		t.Fatalf("expected index html chat to render prompted repository tabs, icon-only repos tab, and isolate active repository chat")
	}
	if strings.Contains(markup, `reposButton.textContent = "All";`) ||
		strings.Contains(markup, `repos = repos.filter((repo) => !promptedKeys.has(chatRepoKey(chatRepoRunValue(repo))));`) {
		t.Fatalf("expected chat repository list to keep an icon-only repos tab without filtering prompted repos out of the all-repositories view")
	}
	if strings.Contains(markup, `taskItems.push(githubReposLoadingTask())`) ||
		strings.Contains(markup, `repoRead.textContent = "repos: reading GitHub projects";`) ||
		strings.Contains(markup, `const GITHUB_REPOS_LOADING_TASK_ID = "github-repos-loading";`) ||
		strings.Contains(markup, `Loading repositories...`) {
		t.Fatalf("expected GitHub repository loading to stay out of Current Work and visible chat text")
	}
	if !strings.Contains(markup, `function submitChatRepoPrompt(repo, input, statusNode, images = [], logNode = null, setImages = null)`) ||
		!strings.Contains(markup, `function chatRepoOwnerIconName(repo)`) ||
		!strings.Contains(markup, `return chatRepoOwnerType(repo) === "organization" ? "building-2" : "user";`) ||
		!strings.Contains(markup, `function isChatRepoCardControlTarget(target, card)`) ||
		!strings.Contains(markup, `const card = document.createElement("div");`) ||
		!strings.Contains(markup, `card.setAttribute("role", "button");`) ||
		!strings.Contains(markup, `const expanded = Boolean(repoKey && state.chatOpenRepoKey === repoKey);`) ||
		!strings.Contains(markup, `card.setAttribute("aria-expanded", String(expanded));`) ||
		!strings.Contains(markup, `ownerIcon.className = "chat-repo-card-owner-icon";`) ||
		!strings.Contains(markup, `promptLog.className = "chat-repo-log";`) ||
		!strings.Contains(markup, `function syncChatOpenRepoKey(pageRepos, selectedTab, restoreFocusKey)`) ||
		!strings.Contains(markup, `syncChatOpenRepoKey(reposForSelection, selectedTab, restoreFocusKey);`) ||
		!strings.Contains(markup, `logNode.hidden = false;`) ||
		!strings.Contains(markup, `logNode.dataset.empty = String(!hasMessages);`) ||
		!strings.Contains(markup, `visibilityIcon.className = `) ||
		!strings.Contains(markup, `chat-repo-card-visibility-public`) ||
		!strings.Contains(markup, `chat-repo-card-visibility-private`) ||
		!strings.Contains(markup, `appendChatRepoPromptMessage(repo, {`) ||
		!strings.Contains(markup, `fetch("/api/local-prompt", {`) ||
		!strings.Contains(markup, `payload.baseBranch = branch;`) {
		t.Fatalf("expected index chat repositories to open prompt panels, show visibility icons, show prompt chat logs, and submit repository tasks")
	}
	if !strings.Contains(markup, `function renderChatReposFromSnapshot()`) ||
		!strings.Contains(markup, `if (chatRepoPanelContainsFocus()) {`) ||
		!strings.Contains(markup, `state.chatRepoRenderPending = true;`) ||
		!strings.Contains(markup, `chatRepoGrid.addEventListener("focusout", () => {`) ||
		!strings.Contains(markup, `if (state.appDisplay !== "chat" || !state.githubReposReady) return;`) {
		t.Fatalf("expected index chat repositories to defer snapshot rerenders while a repo prompt panel has focus")
	}
	if !strings.Contains(markup, `function chatPromptTaskStatusLabel(task)`) ||
		!strings.Contains(markup, `function syncChatPromptMessageTaskStatuses(snapshot = state.snapshot)`) ||
		!strings.Contains(markup, `function refreshVisibleChatPromptStatuses()`) ||
		!strings.Contains(markup, `function chatPromptMessageTone(message)`) ||
		!strings.Contains(markup, `function chatPromptCompletion(message)`) ||
		!strings.Contains(markup, `if (!href) {`) ||
		!strings.Contains(markup, `body.appendChild(link);`) ||
		!strings.Contains(markup, `body.appendChild(document.createTextNode(" Your task is complete"));`) ||
		!strings.Contains(markup, `logo.src = GITHUB_LOGO_URL;`) ||
		!strings.Contains(markup, `link.append(logo);`) ||
		!strings.Contains(markup, `bubble.className = "chat-repo-message chat-repo-message-assistant";`) ||
		!strings.Contains(markup, `syncChatPromptCompletionBubble(bubble, message);`) ||
		!strings.Contains(markup, `bubble.dataset.tone = chatPromptMessageTone(message);`) ||
		!strings.Contains(markup, `status === "completed" || status === "no_changes"`) ||
		!strings.Contains(markup, `status === "error" || status === "invalid" || status === "stopped"`) ||
		!strings.Contains(markup, `storedStatus.startsWith("completed") || storedStatus.startsWith("no changes")`) ||
		!strings.Contains(markup, `meta.dataset.statusText = statusText;`) ||
		!strings.Contains(markup, `refreshVisibleChatPromptStatuses();`) ||
		!strings.Contains(markup, `syncChatPromptMessageTaskStatuses(snapshot);`) {
		t.Fatalf("expected index chat prompt history to update queued request labels, message tones, and completion response bubbles from live task status snapshots")
	}
	if !strings.Contains(markup, `class="prompt-mode-link prompt-mode-link-logo"`) ||
		!strings.Contains(markup, `src="/static/logos/github.svg"`) {
		t.Fatalf("expected index html to render GitHub as an icon-only item inside the shared segmented dock using the shared logo-link class")
	}
	if !strings.Contains(markup, `<span class="sr-only">GitHub</span>`) {
		t.Fatalf("expected index html to keep the GitHub dock item screen-reader accessible without visible text")
	}
	if strings.Index(markup, `id="task-panel"`) > strings.Index(markup, `class="panel prompt-wrap`) {
		t.Fatalf("expected index html to render Current Work before Studio in the page layout")
	}
	if !strings.Contains(markup, `id="builder-repo-select"`) {
		t.Fatalf("expected index html to include repo history select")
	}
	if !strings.Contains(markup, `id="library-repo-select"`) {
		t.Fatalf("expected index html to include library mode repo history select")
	}
	if !strings.Contains(markup, `id="library-task-list"`) {
		t.Fatalf("expected index html to include library task list")
	}
	if !strings.Contains(markup, `id="builder-image-paste-target"`) {
		t.Fatalf("expected index html to include screenshot paste target")
	}
	if !strings.Contains(markup, `class="prompt-control prompt-action-paste"`) {
		t.Fatalf("expected index html to render screenshot paste target in the action row style")
	}
	if !strings.Contains(markup, ">Paste screenshots.<") {
		t.Fatalf("expected index html to render concise screenshot paste copy")
	}
	if strings.Contains(markup, ">Paste screenshots here.<") {
		t.Fatalf("expected index html to remove old screenshot paste copy")
	}
	if !strings.Contains(markup, `function promptImageSummary(images)`) {
		t.Fatalf("expected index html to summarize screenshot names inline in the prompt action row")
	}
	if !strings.Contains(markup, `librarytaskname: libraryTaskName,
        images: normalizePromptImages(state.promptImages),`) {
		t.Fatalf("expected index html to include pasted screenshots in library mode payloads")
	}
	if !strings.Contains(markup, `class="prompt-compose-stack"`) {
		t.Fatalf("expected index html to wrap prompt panels and actions in a shared compose stack")
	}
	if !strings.Contains(markup, `return names.join(" | ");`) {
		t.Fatalf("expected index html to join attached screenshot names with a pipe separator")
	}
	if strings.Contains(markup, `id="builder-image-list"`) {
		t.Fatalf("expected index html to remove the stacked screenshot attachment list")
	}
	if strings.Contains(markup, `prompt-image-chip`) {
		t.Fatalf("expected index html to remove stacked screenshot chip rendering")
	}
	if strings.Contains(markup, `class="prompt-hero-chip brand-chip-action">Screenshots</span>`) {
		t.Fatalf("expected index html to remove the Studio overview hero screenshot chip")
	}
	if strings.Contains(markup, "No screenshots attached.") {
		t.Fatalf("expected index html to hide screenshot empty-state copy until images are attached")
	}
	if !strings.Contains(markup, `id="local-prompt-submit"`) || !strings.Contains(markup, `class="prompt-action-label">Run</span>`) {
		t.Fatalf("expected index html to render the studio submit button with label Run")
	}
	if strings.Contains(markup, "Select a repo, branch, directory, and prompt in Builder mode. You can paste PNG screenshots before submitting.") {
		t.Fatalf("expected index html to remove the builder mode helper sentence")
	}
	if strings.Contains(markup, "Paste PNG screenshots here or directly into the prompt field. Attached images are sent to Codex during startup.") {
		t.Fatalf("expected index html to remove verbose screenshot helper copy")
	}
	if !strings.Contains(markup, `class="prompt-actions"`) {
		t.Fatalf("expected index html to render prompt actions container")
	}
	if !strings.Contains(markup, `class="prompt-actions-start"`) {
		t.Fatalf("expected index html to group screenshot actions on the left")
	}
	if !strings.Contains(markup, `class="prompt-actions-end"`) {
		t.Fatalf("expected index html to group Clear and Run on the right")
	}
	if !strings.Contains(markup, `id="builder-images-clear"`) {
		t.Fatalf("expected index html to render screenshot Clear button in prompt actions")
	}
	if !strings.Contains(markup, `id="builder-images-clear"`) || !strings.Contains(markup, `class="prompt-action-button prompt-action-clear`) {
		t.Fatalf("expected index html to render Clear with shared action sizing class")
	}
	if !strings.Contains(markup, `id="local-prompt-submit" class="prompt-action-button prompt-submit"`) {
		t.Fatalf("expected index html to keep the Run button in prompt actions")
	}
	if !strings.Contains(markup, `const QUEUED_STATUS_TIMEOUT_MS = 12_000;`) {
		t.Fatalf("expected index html to keep success notifications visible for 12 seconds")
	}
	if !strings.Contains(markup, `if (kind !== "ok") {`) || !strings.Contains(markup, `return String(text || "").trim() !== "";`) {
		t.Fatalf("expected index html to auto-dismiss only non-empty success status text")
	}
	if !strings.Contains(markup, `}, QUEUED_STATUS_TIMEOUT_MS);`) {
		t.Fatalf("expected index html to clear queued status after timeout")
	}
	statusIdx := strings.Index(markup, `id="local-prompt-status"`)
	pasteIdx := strings.Index(markup, `id="builder-image-paste-target"`)
	clearIdx := strings.Index(markup, `id="builder-images-clear"`)
	runIdx := strings.Index(markup, `id="local-prompt-submit"`)
	if statusIdx == -1 || pasteIdx == -1 || clearIdx == -1 || runIdx == -1 || pasteIdx > statusIdx || statusIdx > clearIdx || clearIdx > runIdx {
		t.Fatalf("expected Paste/status/Clear/Run controls to render in left-to-right order")
	}
	if !strings.Contains(markup, `id="builder-repo-input" class="prompt-control prompt-input"`) || !strings.Contains(markup, `id="builder-target-subdir" class="prompt-control prompt-input"`) {
		t.Fatalf("expected index html to include builder repo and target subdir inputs")
	}
	if !strings.Contains(markup, `id="builder-reviewer-select" class="prompt-control"`) ||
		!strings.Contains(markup, `id="builder-reviewer-input" class="prompt-control prompt-input"`) ||
		!strings.Contains(markup, `id="library-reviewer-select" class="prompt-control"`) ||
		!strings.Contains(markup, `id="library-reviewer-input" class="prompt-control prompt-input"`) {
		t.Fatalf("expected index html to include reviewer history and manual entry controls for prompt and library modes")
	}
	if !strings.Contains(markup, `id="builder-repo-delete"`) ||
		!strings.Contains(markup, `id="library-repo-delete"`) ||
		!strings.Contains(markup, `id="builder-reviewer-delete"`) ||
		!strings.Contains(markup, `id="library-reviewer-delete"`) ||
		!strings.Contains(markup, `class="prompt-history-delete"`) {
		t.Fatalf("expected index html to include delete actions for repo and reviewer history selects")
	}
	if !strings.Contains(markup, `class="prompt-select-action-wrap"`) {
		t.Fatalf("expected index html to wrap history selects and delete actions in a shared inline layout")
	}
	if !strings.Contains(markup, `id="builder-base-branch-clear"`) {
		t.Fatalf("expected index html to include branch clear action")
	}
	if !strings.Contains(markup, `data-has-action="false"`) ||
		!strings.Contains(markup, `aria-hidden="true"`) ||
		!strings.Contains(markup, `hidden`) {
		t.Fatalf("expected index html to hide the branch clear action while using the default branch")
	}
	if !strings.Contains(markup, `class="prompt-grid"`) ||
		!strings.Contains(markup, `id="builder-repo-history-field" class="prompt-field prompt-field-repo-history"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-repository"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-base-branch"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-target-subdir"`) {
		t.Fatalf("expected index html to include the builder row with explicit field layout classes")
	}
	if !strings.Contains(markup, "function syncBaseBranchClearState(") ||
		!strings.Contains(markup, "builderBaseBranchClear.hidden = isDefault;") ||
		!strings.Contains(markup, "branchActionWrap.dataset.hasAction = isDefault ? \"false\" : \"true\";") ||
		!strings.Contains(markup, `trackAnalyticsEvent("base_branch_reset", { prompt_mode: state.promptMode });`) ||
		!strings.Contains(markup, "resetBaseBranchToDefault();") {
		t.Fatalf("expected index html to include branch clear behavior")
	}
	if !strings.Contains(markup, "function resetBuilderTargetSubdir(") || !strings.Contains(markup, "builderTargetSubdir.value = \".\";") {
		t.Fatalf("expected index html to include target subdir reset behavior")
	}
	if !strings.Contains(markup, "function clearBuilderPromptDraft(") {
		t.Fatalf("expected index html to include prompt clear handler")
	}
	if !strings.Contains(markup, "function submitBuilderPromptOnEnter(event)") ||
		!strings.Contains(markup, "if (event.shiftKey || event.altKey || event.ctrlKey || event.metaKey || event.isComposing)") ||
		!strings.Contains(markup, "localPromptForm.requestSubmit();") ||
		!strings.Contains(markup, "builderPromptInput.addEventListener(\"keydown\", submitBuilderPromptOnEnter);") {
		t.Fatalf("expected index html to submit builder prompts on Enter while preserving Shift+Enter multiline input")
	}
	if !strings.Contains(markup, "function hasReviewerDraftToClear(") ||
		!strings.Contains(markup, "function hasBuilderDraftToClear(") ||
		!strings.Contains(markup, "const promptDirty = String(builderPromptInput?.value || \"\").trim() !== \"\";") ||
		!strings.Contains(markup, "const branchDirty = String(builderBaseBranch?.value || \"\").trim() !== \"\";") ||
		!strings.Contains(markup, "const targetSubdirDirty = ![\"\", \".\"].includes(String(builderTargetSubdir?.value || \"\").trim());") ||
		!strings.Contains(markup, "const rawDirty = String(localPromptInput?.value || \"\").trim() !== \"\";") ||
		!strings.Contains(markup, "return promptDirty || branchDirty || targetSubdirDirty || rawDirty || hasReviewerDraftToClear() || state.promptImages.length > 0;") {
		t.Fatalf("expected index html to detect clearable builder draft changes")
	}
	if !strings.Contains(markup, "function syncBuilderDraftClearState(") ||
		!strings.Contains(markup, "builderImagesClear.disabled = !hasBuilderDraftToClear();") {
		t.Fatalf("expected index html to keep the shared Clear button enabled for any clearable draft state")
	}
	if !strings.Contains(markup, "builderImagesClear.addEventListener(\"click\", clearBuilderPromptDraft);") {
		t.Fatalf("expected index html Clear button to reset the full builder draft")
	}
	if !strings.Contains(markup, "function clearReviewerSelection(") ||
		!strings.Contains(markup, "builderReviewerSelect.value = \"none\";") ||
		!strings.Contains(markup, "libraryReviewerSelect.value = \"none\";") ||
		!strings.Contains(markup, "clearReviewerSelection();") {
		t.Fatalf("expected index html Clear button to reset reviewers explicitly")
	}
	if !strings.Contains(markup, "builderPromptInput.addEventListener(\"input\", syncBuilderDraftClearState);") ||
		!strings.Contains(markup, "builderTargetSubdir.addEventListener(\"input\", () => {") ||
		!strings.Contains(markup, "libraryTargetSubdir.addEventListener(\"input\", () => {") ||
		!strings.Contains(markup, "localPromptInput.addEventListener(\"input\", syncBuilderDraftClearState);") {
		t.Fatalf("expected index html to update shared Clear availability as prompt fields change")
	}
	if !strings.Contains(markup, "builderImagePasteTarget.classList.toggle(\"hidden\", isLibrary);") {
		t.Fatalf("expected index html to hide screenshot paste in library mode only")
	}
	if !strings.Contains(markup, "builderImagesClear.classList.toggle(\"hidden\", isLibrary);") {
		t.Fatalf("expected index html to hide screenshot clearing in library mode only")
	}
	if !strings.Contains(markup, `historyField.classList.toggle("hidden", !hasSavedHistory);`) {
		t.Fatalf("expected index html to hide repo history when there are no saved repos")
	}
	if !strings.Contains(markup, "function rememberRepos(") {
		t.Fatalf("expected index html to include repo history persistence")
	}
	if !strings.Contains(markup, "function rememberManualRepoEntry(value) {") ||
		!strings.Contains(markup, "builderRepoInput.addEventListener(\"change\", () => {") ||
		!strings.Contains(markup, "builderRepoInput.addEventListener(\"blur\", () => {") ||
		!strings.Contains(markup, "libraryRepoInput.addEventListener(\"change\", () => {") ||
		!strings.Contains(markup, "libraryRepoInput.addEventListener(\"blur\", () => {") {
		t.Fatalf("expected index html to persist manually entered repositories into history before submission")
	}
	if !strings.Contains(markup, "function rememberReviewers(") ||
		!strings.Contains(markup, "function renderReviewerHistorySelect(") ||
		!strings.Contains(markup, "function renderReviewerHistoryOptions(") {
		t.Fatalf("expected index html to include reviewer history persistence and rendering helpers")
	}
	if !strings.Contains(markup, `historyField.classList.remove("hidden");`) {
		t.Fatalf("expected index html to keep reviewer selector visible even without saved reviewer history")
	}
	if !strings.Contains(markup, "function syncReviewerVisibility(select, reviewerField)") ||
		!strings.Contains(markup, `const manualSelected = String(select.value || "").trim() === "";`) ||
		!strings.Contains(markup, `reviewerField.classList.toggle("hidden", !manualSelected);`) {
		t.Fatalf("expected reviewer manual input to show only when manual reviewer entry is selected")
	}
	if !strings.Contains(markup, "function defaultRepoSelection(") {
		t.Fatalf("expected index html to include repo history default selection helper")
	}
	if !strings.Contains(markup, `"defaultRepository":"`+config.DefaultRepositoryURL+`"`) {
		t.Fatalf("expected index html to inject the default repository")
	}
	if !strings.Contains(markup, "if (state.repoHistory.length > 0 && unique.length > 0)") {
		t.Fatalf("expected index html to default repo selection to saved history when available")
	}
	if !strings.Contains(markup, "return defaultRepository();") {
		t.Fatalf("expected index html to fall back to the configured default repository when history is empty")
	}
	if !strings.Contains(markup, "const keepManualSelection = manualSelected && select.dataset.manual === \"true\";") ||
		!strings.Contains(markup, "const nextValue = keepManualSelection") ||
		!strings.Contains(markup, "defaultRepoSelection(currentValue, manualSelected ? \"\" : selectedValue, unique);") ||
		!strings.Contains(markup, "if (nextValue) {") ||
		!strings.Contains(markup, "input.value = nextValue;") {
		t.Fatalf("expected index html to sync default saved repo selection into the repository input")
	}
	if !strings.Contains(markup, "builderRepoSelect.dataset.manual = value === \"\" ? \"true\" : \"false\";") ||
		!strings.Contains(markup, "libraryRepoSelect.dataset.manual = value === \"\" ? \"true\" : \"false\";") ||
		!strings.Contains(markup, "builderRepoSelect.dataset.manual = \"true\";") ||
		!strings.Contains(markup, "libraryRepoSelect.dataset.manual = \"true\";") {
		t.Fatalf("expected index html to preserve manual repo selection while editing the repository input")
	}
	if !strings.Contains(markup, "Enter reviewers manually") ||
		!strings.Contains(markup, `option value="none">none</option>`) ||
		!strings.Contains(markup, "No saved reviewers yet") ||
		!strings.Contains(markup, "function reviewerListFromValue(") ||
		!strings.Contains(markup, "payload.reviewers = reviewers;") ||
		!strings.Contains(markup, "const reviewers = dedupeReviewerValues([...(Array.isArray(parsed?.reviewers) ? parsed.reviewers : []), parsed?.githubHandle, parsed?.githubhandle]);") ||
		!strings.Contains(markup, "rememberReviewers(reviewers);") ||
		!strings.Contains(markup, "selectSubmittedReviewer(reviewers);") ||
		!strings.Contains(markup, "return /^none$/i.test(normalized) ? \"\" : normalized;") {
		t.Fatalf("expected index html to capture reviewers in prompt payloads and persist reviewer history after submission")
	}
	if !strings.Contains(markup, "const keepManualSelection = manualSelected && currentReviewers.length > 0;") {
		t.Fatalf("expected index html to preserve manual reviewer entry while the user is typing")
	}
	if !strings.Contains(markup, "const noneSelected = rawSelectedValue === \"none\";") {
		t.Fatalf("expected index html to preserve an explicit none reviewer selection")
	}
	if !strings.Contains(markup, `input.value = noneSelected ? "" : nextValue;`) {
		t.Fatalf("expected index html to keep none reviewer selections out of the manual input")
	}
	if !strings.Contains(markup, `"reviewers": [`) || !strings.Contains(markup, `"octocat"`) || !strings.Contains(markup, `"hubot"`) {
		t.Fatalf("expected index html JSON example to include reviewers")
	}
	if !strings.Contains(markup, "const repo = normalizeRepoValue(builderRepoInput.value) || defaultRepository();") ||
		!strings.Contains(markup, "const repo = normalizeRepoValue(libraryRepoInput.value) || defaultRepository();") {
		t.Fatalf("expected index html payload builders to fall back to the configured default repository")
	}
	if !strings.Contains(markup, "const payload = {\n        repos: [repo],\n        targetsubdir:") ||
		!strings.Contains(markup, "if (branch) {\n        payload.branch = branch;\n      }") {
		t.Fatalf("expected index html library payload to emit selected repositories through repos[]")
	}
	if !strings.Contains(markup, "function dropReposFromHistory(") {
		t.Fatalf("expected index html to include repo history cleanup helper")
	}
	if !strings.Contains(markup, "function dropReviewersFromHistory(") {
		t.Fatalf("expected index html to include reviewer history cleanup helper")
	}
	if !strings.Contains(markup, "function removeSelectedRepoFromHistory(") ||
		!strings.Contains(markup, "function removeSelectedReviewerFromHistory(") {
		t.Fatalf("expected index html to include handlers that remove selected repo and reviewer history values")
	}
	if !strings.Contains(markup, "builderRepoDelete.addEventListener(\"click\", () => {") ||
		!strings.Contains(markup, "libraryRepoDelete.addEventListener(\"click\", () => {") ||
		!strings.Contains(markup, "builderReviewerDelete.addEventListener(\"click\", () => {") ||
		!strings.Contains(markup, "libraryReviewerDelete.addEventListener(\"click\", () => {") {
		t.Fatalf("expected index html to wire repo and reviewer delete buttons")
	}
	if !strings.Contains(markup, "function isCloneMissingRepoError(") {
		t.Fatalf("expected index html to include clone failure repo cleanup matcher")
	}
	if !strings.Contains(markup, "function isRepoAccessError(") {
		t.Fatalf("expected index html to include repo access failure cleanup matcher")
	}
	if !strings.Contains(markup, "if (isCloneMissingRepoError(task) || isRepoAccessError(task)) {") {
		t.Fatalf("expected index html to treat clone and repo access failures as saved-repo cleanup triggers")
	}
	if !strings.Contains(markup, "dropReposFromHistory(failedRepoAccessRepos);") {
		t.Fatalf("expected index html to drop inaccessible repositories from history on repo access failures")
	}
	if !strings.Contains(markup, "function togglePromptVisibility(") {
		t.Fatalf("expected index html to include prompt visibility toggle handler")
	}
	if !strings.Contains(markup, "function applyPromptVisibility(") {
		t.Fatalf("expected index html to include prompt visibility renderer")
	}
	if !strings.Contains(markup, `promptVisibilityToggle.textContent = visible ? "▾" : "▸";`) {
		t.Fatalf("expected index html to render studio toggle arrow icons for minimize/expand")
	}
	if !strings.Contains(markup, `pauseRun.appendChild(createTaskActionIcon(canRun ? "play" : "pause"));`) {
		t.Fatalf("expected index html to render task pause/run icon control from backend capabilities")
	}
	if !strings.Contains(markup, `outputToggle.appendChild(createTaskActionIcon("output"));`) ||
		!strings.Contains(markup, `outputToggle.title = "Open task output terminal logs.";`) {
		t.Fatalf("expected index html to render terminal output icon control with descriptive tooltip")
	}
	if !strings.Contains(markup, `forceStart.title = "Force start this queued task now";`) {
		t.Fatalf("expected index html to render force-start control for pending tasks")
	}
	if !strings.Contains(markup, `stop.appendChild(createTaskActionIcon("stop"));`) {
		t.Fatalf("expected index html to render task stop icon control")
	}
	if !strings.Contains(markup, `badge.appendChild(createTaskActionIcon("loader"));`) ||
		!strings.Contains(markup, `badge.title = "Running: task is actively executing.";`) {
		t.Fatalf("expected index html to render running badge loader icon with descriptive tooltip")
	}
	if !strings.Contains(markup, `close.textContent = "X";`) {
		t.Fatalf("expected index html to render task close icon control")
	}
	if !strings.Contains(markup, "function triggerTaskSparkle(") || !strings.Contains(markup, "window.setTimeout(() => {") {
		t.Fatalf("expected index html to include timed task completion sparklet handling")
	}
	if !strings.Contains(markup, `sparklet.className = "task-sparklet";`) {
		t.Fatalf("expected index html to render a sparklet for newly completed tasks")
	}
	if !strings.Contains(markup, "syncTaskCompletionSparkles(previousSnapshot, snapshot);") {
		t.Fatalf("expected index html to trigger sparklets when task status first becomes completed")
	}
	if !strings.Contains(markup, `const PROMPT_VISIBILITY_KEY = "hubui.localPromptVisible";`) {
		t.Fatalf("expected index html to persist prompt visibility preference")
	}
	if !strings.Contains(markup, "function renderConfiguredAgent()") ||
		!strings.Contains(markup, "window.MoltenHubHeader.update({") {
		t.Fatalf("expected index html to delegate dynamic configured agent subtitle copy to the shared header")
	}
	if !strings.Contains(markup, "function handlePromptImagePaste(") {
		t.Fatalf("expected index html to include screenshot paste handler")
	}
	if !strings.Contains(markup, "function clipboardPngFiles(event)") ||
		!strings.Contains(markup, "prompt.addEventListener(\"paste\", handleChatRepoImagePaste);") ||
		!strings.Contains(markup, `chatIcon.className = "chat-repo-card-chat-icon";`) ||
		!strings.Contains(markup, "chatIcon.innerHTML = `<i data-lucide=\"message-circle\" aria-hidden=\"true\"></i>`;") ||
		!strings.Contains(markup, `const openPrompt = () => {`) ||
		!strings.Contains(markup, `if (isChatRepoCardControlTarget(event.target, card)) return;`) ||
		!strings.Contains(markup, `card.setAttribute("aria-expanded", "true");`) ||
		!strings.Contains(markup, "pasteTarget.className = \"prompt-control prompt-action-paste chat-repo-image-paste\"") ||
		!strings.Contains(markup, "imageActions.className = \"chat-repo-image-actions\"") ||
		!strings.Contains(markup, "imageActions.append(pasteTarget, submitStatus);") {
		t.Fatalf("expected chat repository prompts to open from card/icon and accept pasted screenshots")
	}
	if !strings.Contains(markup, "images: promptImages,") ||
		!strings.Contains(markup, "setImages([]);") {
		t.Fatalf("expected chat repository submits to include pasted screenshots and clear them after queueing")
	}
	if !strings.Contains(markup, "chatPromptDrafts: new Map(),") ||
		!strings.Contains(markup, `chatOpenRepoKey: "",`) ||
		!strings.Contains(markup, `card.dataset.repoKey = repoKey;`) ||
		!strings.Contains(markup, `state.chatPromptDrafts.set(repoKey, prompt.value);`) ||
		!strings.Contains(markup, `const activePrompt = document.activeElement instanceof HTMLTextAreaElement &&`) ||
		!strings.Contains(markup, `restoredPrompt.setSelectionRange(Math.min(restoreSelectionStart, end), end);`) {
		t.Fatalf("expected chat repository prompts to preserve open panel, draft text, and cursor focus across re-renders")
	}
	if !strings.Contains(markup, "function clearPromptImages(syncRaw = true)") {
		t.Fatalf("expected index html to allow screenshot clearing without forcing raw prompt resync")
	}
	if !strings.Contains(markup, "function clearSubmittedPromptDraft(") {
		t.Fatalf("expected index html to include submitted prompt clearing helper")
	}
	if !strings.Contains(markup, "function resetPromptInputSize(input)") ||
		!strings.Contains(markup, "input.style.removeProperty(\"height\");") ||
		!strings.Contains(markup, "input.style.removeProperty(\"width\");") {
		t.Fatalf("expected index html to include prompt textarea resize reset behavior")
	}
	if !strings.Contains(markup, "builderPromptInput.value = \"\";") || !strings.Contains(markup, "localPromptInput.value = \"\";") {
		t.Fatalf("expected index html to clear builder and raw prompt inputs after submit")
	}
	if !strings.Contains(markup, "resetPromptInputSize(builderPromptInput);") ||
		!strings.Contains(markup, "resetPromptInputSize(localPromptInput);") {
		t.Fatalf("expected index html to reset prompt textarea size after clearing submitted prompt state")
	}
	if !strings.Contains(markup, "function clearSubmittedPromptState(") {
		t.Fatalf("expected index html to include queued-submit cleanup helper")
	}
	if !strings.Contains(markup, "clearPromptImages(false);") {
		t.Fatalf("expected index html to clear attached screenshots after a successful submit without repopulating raw JSON")
	}
	if !strings.Contains(markup, "resetBuilderTargetSubdir();") || !strings.Contains(markup, "resetBaseBranchToDefault(false);") {
		t.Fatalf("expected index html to reset branch and target subdir as part of queued-submit cleanup")
	}
	if !strings.Contains(markup, "clearSubmittedPromptState();") {
		t.Fatalf("expected index html to clear the submitted prompt state after a successful queue")
	}
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"","configuredAgentLabel":"","defaultRepository":"`+config.DefaultRepositoryURL+`","promptImageHarnesses":["codex","pi"],"githubReposReady":false};`) {
		t.Fatalf("expected index html to include default UI config")
	}
	if !strings.Contains(markup, `id="theme-toggle"`) || !strings.Contains(markup, `function nextThemeMode(theme)`) {
		t.Fatalf("expected index html to include theme toggle control")
	}
	if !strings.Contains(markup, `const THEME_MODES = ["light", "dark", "night", "pink"];`) {
		t.Fatalf("expected index html to include pink in the theme selector cycle")
	}
	if !strings.Contains(markup, `const DEFAULT_THEME_MODE = "light";`) {
		t.Fatalf("expected index html to define light as the default theme mode")
	}
	if !strings.Contains(markup, `const GOOGLE_ANALYTICS_MEASUREMENT_ID = "G-BY33RFG2WB";`) {
		t.Fatalf("expected index html to expose the google analytics measurement id constant to the usage tracker")
	}
	if !strings.Contains(markup, `function trackAnalyticsEvent(name, params = {})`) {
		t.Fatalf("expected index html to include the analytics event helper")
	}
	if !strings.Contains(markup, `const payload = { send_to: GOOGLE_ANALYTICS_MEASUREMENT_ID };`) {
		t.Fatalf("expected analytics events to route to the configured google analytics destination")
	}
	if !strings.Contains(markup, `trackAnalyticsEvent("prompt_submit_succeeded", { prompt_mode: state.promptMode, request_id: requestID });`) {
		t.Fatalf("expected index html to track successful prompt submissions")
	}
	for _, want := range []string{
		`trackAnalyticsEvent("task_view_changed", { task_view: nextView });`,
		`trackAnalyticsEvent("task_pull_request_opened", { request_id: taskRequestID(task) });`,
		`trackAnalyticsEvent("task_github_step_opened", {`,
		`trackAnalyticsEvent("hub_setup_started", hubSetupAnalyticsParams({ auto_submit: autoSubmit }));`,
		`trackAnalyticsEvent("agent_auth_configure_started", agentAuthAnalyticsParams({ auth_method: "github_token" }));`,
		`trackAnalyticsEvent("prompt_screenshots_attached", {`,
	} {
		if !strings.Contains(markup, want) {
			t.Fatalf("expected index html to include analytics event tag %q", want)
		}
	}
	if !strings.Contains(markup, `localPromptInput.value = normalized.pretty;

      localPromptSubmit.disabled = true;`) {
		t.Fatalf("expected index html to keep selected task history filter when Run starts")
	}
	if !strings.Contains(markup, `trackAnalyticsEvent("prompt_submit_duplicate", { prompt_mode: state.promptMode });
            setLocalPromptStatus("warn", duplicateNotice(body));`) {
		t.Fatalf("expected index html to keep selected task history filter when a duplicate submit resolves to an active task")
	}
	if !strings.Contains(markup, `clearSubmittedPromptState();
        trackAnalyticsEvent("prompt_submit_succeeded", { prompt_mode: state.promptMode, request_id: requestID });`) {
		t.Fatalf("expected index html to keep prompt submit success flow without changing task history filter")
	}
	if !strings.Contains(markup, `trackAnalyticsEvent("task_rerun_duplicate", { request_id: requestID, forced: force });
            setLocalPromptStatus("warn", duplicateNotice(body));`) {
		t.Fatalf("expected index html to keep selected task history filter when a duplicate rerun resolves to an active task")
	}
	if !strings.Contains(markup, `const newRequestID = body?.request_id || "(unknown)";
        trackAnalyticsEvent("task_rerun_succeeded", { request_id: requestID, forced: force, rerun_request_id: newRequestID });`) {
		t.Fatalf("expected index html to keep selected task history filter after a successful rerun")
	}
	if !strings.Contains(markup, `return THEME_MODES.includes(raw) ? raw : DEFAULT_THEME_MODE;`) {
		t.Fatalf("expected index html theme loading to fall back to the default light theme")
	}
	if !strings.Contains(markup, `<span class="theme-toggle-icon" id="theme-toggle-icon" aria-hidden="true"><i data-lucide="sun" class="theme-toggle-icon-glyph" aria-hidden="true"></i></span>`) {
		t.Fatalf("expected index html to render a dedicated theme toggle icon slot")
	}
	if !strings.Contains(markup, `<span class="prompt-mode-mobile-label" aria-hidden="true">Theme</span>`) {
		t.Fatalf("expected index html to expose a mobile label for the nav theme selector")
	}
	if !strings.Contains(markup, `<span id="theme-toggle-label">Light</span>`) {
		t.Fatalf("expected index html to render light as the initial theme toggle label")
	}
	if !strings.Contains(markup, `function syncThemeToggle(theme)`) ||
		!strings.Contains(markup, `const iconName = THEME_ICON_NAMES[currentTheme] || THEME_ICON_NAMES[DEFAULT_THEME_MODE];`) ||
		!strings.Contains(markup, "themeToggleIcon.innerHTML = `<i data-lucide=\"${iconName}\" class=\"theme-toggle-icon-glyph\" aria-hidden=\"true\"></i>`;") {
		t.Fatalf("expected index html to keep the theme toggle icon and label in sync")
	}
	if !strings.Contains(markup, `const THEME_ICON_NAMES = {`) {
		t.Fatalf("expected index html to define theme toggle icons")
	}
	if !strings.Contains(markup, `pink: "heart",`) {
		t.Fatalf("expected index html to define a pink theme icon")
	}
	if !strings.Contains(markup, `root.classList.remove("light", "dark", "night", "pink");`) {
		t.Fatalf("expected index html to remove the pink theme class when switching themes")
	}
	if !strings.Contains(markup, "themeToggleButton.setAttribute(\"aria-label\", `Switch theme. Currently: ${currentLabel}`);") {
		t.Fatalf("expected index html to expose the current theme through the toggle aria-label")
	}
	if strings.Contains(markup, `theme-cycle-next`) || strings.Contains(markup, `theme-cycle-current`) || strings.Contains(markup, `Next: Dark`) {
		t.Fatalf("expected index html to remove the legacy theme cycle markup")
	}
	if strings.Contains(markup, `rgb(var(--hub-panel-rgb) / <alpha-value>)`) || strings.Contains(markup, `rgb(var(--hub-text-rgb) / <alpha-value>)`) {
		t.Fatalf("expected index html to keep theme color wiring in the global stylesheet")
	}
	if strings.Contains(markup, `id="hover-select"`) || strings.Contains(markup, ">Hover<") {
		t.Fatalf("expected index html to remove the docked hover selector")
	}
}

func TestIndexPromptReviewerClearBehavior(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	markup := resp.Body.String()
	for _, needle := range []string{
		"function hasReviewerDraftToClear()",
		"function clearReviewerSelection()",
		"function selectSubmittedReviewer(reviewers)",
		"selectSubmittedReviewer(reviewers);",
		`input.value = noneSelected ? "" : nextValue;`,
	} {
		if !strings.Contains(markup, needle) {
			t.Fatalf("expected index html to include %q", needle)
		}
	}

	submittedStart := strings.Index(markup, "function clearSubmittedPromptState()")
	reviewerStart := strings.Index(markup, "function clearReviewerSelection()")
	if submittedStart == -1 || reviewerStart == -1 || reviewerStart < submittedStart {
		t.Fatalf("expected submitted prompt cleanup before reviewer clear helper")
	}
	if strings.Contains(markup[submittedStart:reviewerStart], "clearReviewerSelection();") {
		t.Fatalf("expected submit cleanup to preserve selected reviewer")
	}

	clearStart := strings.Index(markup, "function clearBuilderPromptDraft()")
	defaultStart := strings.Index(markup, "function defaultRepoSelection(")
	if clearStart == -1 || defaultStart == -1 || defaultStart < clearStart {
		t.Fatalf("expected builder clear handler before repo default helper")
	}
	if !strings.Contains(markup[clearStart:defaultStart], "clearReviewerSelection();") {
		t.Fatalf("expected manual Clear to clear selected reviewer")
	}
}

func TestHandlerIndexInjectsAutomaticModeConfig(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.AutomaticMode = true
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	markup := resp.Body.String()
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":true,"configuredHarness":"","configuredAgentLabel":"","defaultRepository":"`+config.DefaultRepositoryURL+`","promptImageHarnesses":["codex","pi"],"githubReposReady":false};`) {
		t.Fatalf("expected automatic mode UI config, got %q", markup)
	}
}

func TestHandlerIndexInjectsConfiguredHarness(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfiguredHarness = "claude"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	markup := resp.Body.String()
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"claude","configuredAgentLabel":"Claude","defaultRepository":"`+config.DefaultRepositoryURL+`","promptImageHarnesses":["codex","pi"],"githubReposReady":false};`) {
		t.Fatalf("expected configured harness UI config, got %q", markup)
	}
}

func TestHandlerIndexInjectsPiHarnessConfig(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfiguredHarness = "pi"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	markup := resp.Body.String()
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"pi","configuredAgentLabel":"Pi","defaultRepository":"`+config.DefaultRepositoryURL+`","promptImageHarnesses":["codex","pi"],"githubReposReady":false};`) {
		t.Fatalf("expected configured pi harness UI config, got %q", markup)
	}
	if !strings.Contains(markup, `pi: "/static/logos/pi.svg"`) {
		t.Fatalf("expected configured pi harness markup to include the pi logo asset mapping")
	}
}

func TestHandlerIndexIncludesClaudeBrowserCodeFlow(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ConfiguredHarness = "claude"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	markup := resp.Body.String()
	required := []string{
		`id="agent-auth-url-logo"`,
		`src="/static/logos/claude-code.svg"`,
		`agentAuthURLLogo.addEventListener("error", () => {`,
		`state.agentAuthURLLogoBroken = true;`,
		`function authHarness(auth) {`,
		`return configuredHarnessName();`,
		`function isClaudeBrowserCodeAwaitingSubmission(auth) {`,
		`const showBrowserCode = isClaudePendingBrowserLoginState();`,
		`id="agent-auth-device-code-row" class="agent-auth-command-box agent-auth-command-box-inline hidden"`,
		`const agentAuthDeviceCodeRow = document.getElementById("agent-auth-device-code-row");`,
		`agentAuthDeviceCodeRow.classList.toggle("hidden", !state.agentAuth.deviceCode);`,
		`id="agent-auth-copy"`,
		`aria-label="Copy device code"`,
		`id="agent-auth-browser-command-primary"`,
		`class="agent-auth-command-box agent-auth-command-box-inline"`,
		`aria-label="Copy Claude credentials command"`,
		`id="agent-auth-browser-command-primary-copy"`,
		`id="agent-auth-browser-command-secondary"`,
		`class="agent-auth-command-box"`,
		`aria-label="Copy Claude credentials command"`,
		`id="agent-auth-browser-command-secondary-copy"`,
		`id="agent-auth-configure-copy"`,
		`aria-label="Copy configure command"`,
		`id="agent-auth-configure-secret-input" class="prompt-text agent-auth-configure-input-github agent-auth-configure-input-single-line hidden" type="password"`,
		`agentAuthConfigureSecretInput.value = "";`,
		`cat ~/.pi/agent/auth.json`,
		`Paste ~/.pi/agent/auth.json contents...`,
		`agent-auth-configure-input-single-line`,
		`function clearAgentAuthConfigureInputIfSensitive()`,
		"state.agentAuth.configureCommand}|${state.agentAuth.configurePlaceholder}",
		`agentAuthConfigureInput.value = "";`,
		`GitHub token does not belong in PI auth JSON`,
		`const useClaudeLogoLink = authHarness(state.agentAuth) === "claude" && authURL !== "" && !useClaudeCommandFlow;`,
		`const code = claudeBrowserCodeValue();`,
		`trackAnalyticsEvent("agent_auth_link_opened", agentAuthAnalyticsParams());`,
		`copiedLabel: "Copied device code"`,
		`copiedLabel: "Copied configure command"`,
	}
	for _, needle := range required {
		if !strings.Contains(markup, needle) {
			t.Fatalf("expected index html to include %q", needle)
		}
	}
}

func TestHandlerOmitsReleasesIndexDisplay(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}

	markup := resp.Body.String()
	if !strings.Contains(markup, `class="page-bottom-dock"`) ||
		!strings.Contains(markup, `class="prompt-mode-tabs prompt-mode-tabs-dock"`) ||
		!strings.Contains(markup, `src="/static/bottom-dock.js"`) ||
		strings.Contains(markup, `<!-- hub-bottom-dock -->`) {
		t.Fatalf("expected index html to render shared dock component")
	}
	for _, removed := range []string{
		`id="releases-display"`,
		`class="releases-display`,
		`id="release-list"`,
		`class="release-empty"`,
		`data-app-display="releases"`,
		`href="#releases"`,
		`No releases yet.`,
	} {
		if strings.Contains(markup, removed) {
			t.Fatalf("expected index html to omit release UI %q", removed)
		}
	}
}

func TestHandlerKeepsReleasesDockRemovedWhenSnapshotHasReleases(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	now := time.Date(2026, 5, 7, 12, 5, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	b.RecordReleaseFromTask(Task{
		RequestID:       "req-release",
		Prompt:          "ship release panel",
		PRURL:           "https://github.com/acme/repo/pull/42",
		StartedAt:       now.Add(-3 * time.Minute).Format(time.RFC3339),
		UpdatedAt:       now.Add(-1 * time.Minute).Format(time.RFC3339),
		DurationSeconds: 120,
	}, now.Add(-30*time.Second).Format(time.RFC3339))

	srv := NewServer("", b)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	markup := resp.Body.String()
	if strings.Contains(markup, `data-app-display="releases"`) ||
		strings.Contains(markup, `href="#releases"`) {
		t.Fatalf("expected index html to keep releases dock link removed even when releases exist")
	}
}

func TestHandlerRemovesStandaloneSitePages(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	for _, path := range []string{"/releases", "/dashboard"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		srv.Handler().ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, resp.Code)
		}
	}
}

func renderHandlerMarkup(t *testing.T, srv Server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("%s status = %d", path, resp.Code)
	}
	return resp.Body.String()
}

func TestHandlerServesChatView(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}

	markup := resp.Body.String()
	required := []string{
		`<title>Molten Hub Code Chat</title>`,
		`src="/static/site-header.js"`,
		`<moltenhub-code-header agent-harness="codex" agent-label="Codex"></moltenhub-code-header>`,
		`class="page-bottom-dock"`,
		`data-app-display="chat"`,
		`<i data-lucide="message-circle" aria-hidden="true"></i>`,
		`id="chat-repo-search" class="chat-search-input" type="search"`,
		`id="chat-repo-grid" class="chat-repo-grid" aria-label="GitHub repositories"`,
		`id="chat-repo-pagination" class="chat-repo-pagination hidden" aria-label="GitHub repository pages"`,
		`const search = document.getElementById("chat-repo-search");`,
		`const CHAT_REPOS_PER_PAGE = 15;`,
		`const pageRepos = repos.slice(start, start + CHAT_REPOS_PER_PAGE);`,
		`function filterRepos(repos, query)`,
		`empty.textContent = repoSearchQuery ? "No repositories match search." : "No repositories found.";`,
		`repoSearchQuery = search.value;`,
		`fetch("/api/github/repos", { cache: "no-store" })`,
		`function repoOwnerIconName(repo)`,
		`return repoOwnerType(repo) === "organization" ? "building-2" : "user";`,
		`function isRepoCardControlTarget(target, card) {`,
		`const card = document.createElement("div");`,
		`card.setAttribute("role", "button");`,
		`ownerIcon.className = "chat-repo-card-owner-icon";`,
		`chatIcon.className = "chat-repo-card-chat-icon";`,
		`chatIcon.innerHTML = '<i data-lucide="message-circle" aria-hidden="true"></i>';`,
		`visibilityIcon.className = "chat-repo-card-visibility " + (repo.private ? "chat-repo-card-visibility-private" : "chat-repo-card-visibility-public");`,
		`visibilityIcon.innerHTML = '<i data-lucide="' + (repo.private ? "lock" : "globe") + '" aria-hidden="true"></i>';`,
		`const openPrompt = () => {`,
		`if (isRepoCardControlTarget(event.target, card)) return;`,
		`card.setAttribute("aria-expanded", "true");`,
		`fetch("/api/local-prompt", {`,
		`payload.baseBranch = branch;`,
		`window.MoltenHubHeader.startConnectionStatus();`,
	}
	for _, needle := range required {
		if !strings.Contains(markup, needle) {
			t.Fatalf("expected chat html to include %q", needle)
		}
	}
	if strings.Contains(markup, `Loading repositories...`) {
		t.Fatalf("expected chat html to avoid showing repository loading text before repositories are ready")
	}
	if strings.Contains(markup, bottomDockPlaceholder) {
		t.Fatalf("expected chat page bottom dock placeholder to be replaced")
	}
}

func TestHandlerGitHubReposUsesOverride(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ResolveGitHubRepos = func(context.Context) ([]GitHubRepo, error) {
		return []GitHubRepo{{
			Name:          "repo",
			FullName:      "acme/repo",
			Description:   "Docs",
			HTMLURL:       "https://github.com/acme/repo",
			OwnerType:     "Organization",
			DefaultBranch: "main",
			Language:      "Go",
			Private:       true,
		}}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/repos", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"ok":true`) ||
		!strings.Contains(body, `"full_name":"acme/repo"`) ||
		!strings.Contains(body, `"html_url":"https://github.com/acme/repo"`) ||
		!strings.Contains(body, `"owner_type":"Organization"`) ||
		!strings.Contains(body, `"default_branch":"main"`) ||
		!strings.Contains(body, `"private":true`) {
		t.Fatalf("unexpected github repos response %q", body)
	}
}

func TestHandlerGitHubReposCachesSuccessfulLoad(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	calls := 0
	srv.ResolveGitHubRepos = func(context.Context) ([]GitHubRepo, error) {
		calls++
		return []GitHubRepo{{
			Name:     "repo",
			FullName: "acme/repo",
			HTMLURL:  "https://github.com/acme/repo",
		}}, nil
	}
	handler := srv.Handler()

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/github/repos", nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i+1, resp.Code)
		}
		if !strings.Contains(resp.Body.String(), `"full_name":"acme/repo"`) {
			t.Fatalf("request %d unexpected github repos response %q", i+1, resp.Body.String())
		}
	}

	if calls != 1 {
		t.Fatalf("ResolveGitHubRepos calls = %d, want 1", calls)
	}
}

func TestHandlerIndexHydratesCachedGitHubRepos(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ResolveGitHubRepos = func(context.Context) ([]GitHubRepo, error) {
		return []GitHubRepo{{
			Name:     "repo",
			FullName: "acme/repo",
			HTMLURL:  "https://github.com/acme/repo",
		}}, nil
	}
	handler := srv.Handler()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/github/repos", nil))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	markup := resp.Body.String()
	if !strings.Contains(markup, `"githubReposReady":true`) ||
		!strings.Contains(markup, `"githubRepos":[{"name":"repo","full_name":"acme/repo"`) {
		t.Fatalf("expected index html to hydrate cached github repositories, got %q", markup)
	}
}

func TestHandlerIndexPreloadsGitHubReposOnce(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	srv.ResolveGitHubRepos = func(context.Context) ([]GitHubRepo, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []GitHubRepo{{
			Name:     "repo",
			FullName: "acme/repo",
			HTMLURL:  "https://github.com/acme/repo",
		}}, nil
	}
	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for github repo preload")
	}
	close(release)
	deadline := time.After(2 * time.Second)
	for {
		if _, ready := srv.cachedGitHubRepos(); ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for preloaded github repos to cache")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("second status = %d", resp.Code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ResolveGitHubRepos calls = %d, want 1", got)
	}
	if !strings.Contains(resp.Body.String(), `"githubReposReady":true`) {
		t.Fatalf("expected second index render to hydrate preloaded github repositories")
	}
}

func TestHandlerServesStaticCSS(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("content-type = %q", ct)
	}

	css := resp.Body.String()
	if !strings.Contains(css, ".task-close") {
		t.Fatalf("expected stylesheet to include task close styles")
	}
	if !strings.Contains(css, ".hub-emoji-picker-panel") || !strings.Contains(css, ".hub-emoji-picker-mart") {
		t.Fatalf("expected stylesheet to include emoji picker styles")
	}
	if !strings.Contains(css, "moltenhub-code-header {\n  display: block;") ||
		!strings.Contains(css, "moltenhub-code-nav {\n  display: block;") ||
		!strings.Contains(css, ".site-page-footer {") {
		t.Fatalf("expected stylesheet to include shared site page shell styles")
	}
	if strings.Contains(css, ".releases-display {") ||
		strings.Contains(css, ".release-card {") ||
		strings.Contains(css, ".release-empty {") {
		t.Fatalf("expected stylesheet to omit release display styles")
	}
	if !strings.Contains(css, "--hub-page-width: 1500px;") ||
		!strings.Contains(css, "width: min(var(--hub-page-width), 100%);") ||
		!strings.Contains(css, "width: min(var(--hub-page-width), calc(100vw - 32px));") {
		t.Fatalf("expected stylesheet to define shared app and dashboard page widths")
	}
	if !strings.Contains(css, ".dashboard-share-links") ||
		!strings.Contains(css, ".dashboard-share-link") ||
		!strings.Contains(css, ".dashboard-share-icon") {
		t.Fatalf("expected stylesheet to include dashboard share link styles")
	}
	if !strings.Contains(css, ".app.dashboard-app {\n  width: 100%;\n  max-width: 1500px;\n  padding: 20px 20px var(--hub-content-bottom-padding);") ||
		!strings.Contains(css, ".site-page {\n  width: min(var(--hub-page-width), calc(100vw - 32px));\n  min-height: 100vh;\n  margin: 0 auto;\n  padding: 28px 0 var(--hub-content-bottom-padding);") {
		t.Fatalf("expected app and standalone site pages to reserve vertical space below content for the floating bottom dock")
	}
	if !strings.Contains(css, "@media (max-width: 640px) {\n  .site-page {\n    width: min(100% - 24px, var(--hub-page-width));\n    padding: 20px 0 var(--hub-content-bottom-padding);") {
		t.Fatalf("expected mobile site pages to preserve floating dock clearance")
	}
	if !strings.Contains(css, ".prompt-select-action-wrap") || !strings.Contains(css, ".prompt-history-delete") {
		t.Fatalf("expected stylesheet to include inline delete controls for repository and reviewer history selects")
	}
	if !strings.Contains(css, ".chat-repo-card[aria-expanded=\"true\"] {\n  grid-column: 1 / -1;") {
		t.Fatalf("expected expanded chat repository cards to span the full repository grid width")
	}
	if !strings.Contains(css, ".chat-shell {\n  display: flex;\n  flex-direction: column;") ||
		!strings.Contains(css, ".chat-repo-grid-active-repo {\n  flex: 1 1 auto;") ||
		!strings.Contains(css, ".chat-repo-grid-active-repo .chat-repo-card[aria-expanded=\"true\"] {") ||
		!strings.Contains(css, ".chat-repo-grid-active-repo .chat-repo-log:not([hidden]) {") {
		t.Fatalf("expected selected chat repository panels to fill the Git / Chat shell")
	}
	if !strings.Contains(css, ".chat-repo-message[data-tone=\"success\"] {") ||
		!strings.Contains(css, ".chat-repo-message[data-tone=\"failure\"] {") ||
		!strings.Contains(css, ".chat-repo-message[data-tone=\"warning\"] {") ||
		!strings.Contains(css, ".chat-repo-message-assistant {\n  justify-self: start;") ||
		!strings.Contains(css, ".chat-repo-message-link {") ||
		!strings.Contains(css, ".chat-repo-message-link-logo {") ||
		!strings.Contains(css, "color: var(--surface-success);") ||
		!strings.Contains(css, "color: var(--surface-danger);") {
		t.Fatalf("expected stylesheet to color-code chat prompt messages by terminal task result")
	}
	if !strings.Contains(css, ".chat-repo-card-head {") ||
		!strings.Contains(css, ".chat-repo-card {\n  position: relative;") ||
		!strings.Contains(css, ".chat-repo-card-owner-icon {") ||
		!strings.Contains(css, ".chat-repo-card-chat-icon {") ||
		!strings.Contains(css, ".chat-repo-card-visibility {") ||
		!strings.Contains(css, ".chat-repo-card-visibility-private {") ||
		!strings.Contains(css, "  position: absolute;\n  top: 10px;\n  right: 10px;") ||
		!strings.Contains(css, ".chat-repo-card:hover .chat-repo-card-chat-icon,") {
		t.Fatalf("expected stylesheet to render chat and visibility icons inside repository cards")
	}
	if !strings.Contains(css, ".hub-emoji-picker-panel-header") || !strings.Contains(css, ".hub-emoji-picker-grid") || !strings.Contains(css, ".hub-emoji-picker-option") {
		t.Fatalf("expected stylesheet to include the refreshed emoji picker layout styles")
	}
	if !strings.Contains(css, ".panel-header,\n.task-head {\n  display: flex;\n  justify-content: space-between;\n  align-items: center;\n  gap: 8px;\n  padding: 13px 16px;\n  border-bottom: 1px solid var(--surface-header-border);\n  background: var(--surface-header);\n  color: var(--surface-label);") {
		t.Fatalf("expected stylesheet to style task and output headers with theme-aware surface tokens")
	}
	if !strings.Contains(css, ".theme-toggle") || !strings.Contains(css, ".theme-toggle-icon") {
		t.Fatalf("expected stylesheet to include theme toggle styles")
	}
	if !strings.Contains(css, ".prompt-mode-mobile-label {\n  display: none;\n}") ||
		!strings.Contains(css, ".prompt-mode-link-tooltip {\n    display: none;\n  }") {
		t.Fatalf("expected stylesheet to keep docked nav controls icon-only on mobile")
	}
	if !strings.Contains(css, ".prompt-mode-link-icon {\n  position: relative;\n  z-index: 1;\n  display: flex;\n  align-items: center;\n  justify-content: center;\n  width: 18px;\n  height: 18px;\n  overflow: hidden;\n  border: 0;\n  background: transparent;\n  box-shadow: none;") {
		t.Fatalf("expected stylesheet to render dock link icons without circular button chrome")
	}
	if !strings.Contains(css, ".theme-toggle.theme-toggle-dock .theme-toggle-icon {\n  position: relative;\n  z-index: 1;\n  display: inline-flex;\n  width: 18px;\n  height: 18px;\n  align-items: center;\n  justify-content: center;\n  overflow: hidden;\n  border: 0;\n  background: transparent;\n  box-shadow: none;") {
		t.Fatalf("expected stylesheet to render dock theme icon without circular button chrome")
	}
	if !strings.Contains(css, "--input-background: var(--surface-control-bg-strong);") ||
		!strings.Contains(css, "--popover: var(--bg-panel-strong);") ||
		!strings.Contains(css, "background: var(--input-background);") ||
		!strings.Contains(css, "background: var(--popover);") ||
		!strings.Contains(css, "outline: 2px solid rgb(var(--hub-running-rgb) / 0.24);") {
		t.Fatalf("expected emoji picker surfaces to inherit active theme tokens")
	}
	if !strings.Contains(css, ".agent-auth-command-box {") ||
		!strings.Contains(css, ".agent-auth-command-copy svg {") ||
		!strings.Contains(css, ".agent-auth-command-copy.is-copied {") {
		t.Fatalf("expected stylesheet to include Claude auth command code-box and icon copy styles")
	}
	if strings.Contains(css, ".theme-cycle-button") || strings.Contains(css, ".theme-control-label") || strings.Contains(css, ".theme-cycle-next") {
		t.Fatalf("expected stylesheet to remove the legacy theme cycle selectors")
	}
	if !strings.Contains(css, "--theme-button-bg:") || !strings.Contains(css, "--surface-control-bg:") {
		t.Fatalf("expected stylesheet to define reusable theme tokens for controls")
	}
	if !strings.Contains(css, "--agent-logo-filter: brightness(0) saturate(100%);") {
		t.Fatalf("expected stylesheet to define a light-theme monochrome logo filter token")
	}
	if strings.Count(css, "--agent-logo-filter: brightness(0) saturate(100%) invert(1);") < 2 {
		t.Fatalf("expected stylesheet to define dark and night monochrome logo filter tokens")
	}
	if !strings.Contains(css, ".theme-toggle {\n  position: fixed;\n  right: 16px;\n  bottom: 16px;") {
		t.Fatalf("expected stylesheet to dock the theme toggle in the bottom-right corner")
	}
	if !strings.Contains(css, ".theme-toggle {\n  position: fixed;\n  right: 16px;\n  bottom: 16px;\n  z-index: 96;") {
		t.Fatalf("expected stylesheet to keep the theme toggle above onboarding overlays")
	}
	if !strings.Contains(css, ".theme-toggle:hover { transform: scale(1.04); }") || !strings.Contains(css, ".theme-toggle:active { transform: scale(.96); }") {
		t.Fatalf("expected stylesheet to include the theme toggle hover and active treatments")
	}
	if !strings.Contains(css, `/* Selectable pink theme. */`) || !strings.Contains(css, `html.pink {`) {
		t.Fatalf("expected stylesheet to include the selectable pink theme")
	}
	if !strings.Contains(css, `--primary: #ec4899;`) || !strings.Contains(css, `--accent: #db2777;`) || !strings.Contains(css, `--theme-button-text: #ffe4f1;`) {
		t.Fatalf("expected stylesheet to define pink theme palette tokens")
	}
	if !strings.Contains(css, ".agent-auth-shell {\n  display: flex;\n  flex-direction: column;\n  width: 100%;\n  max-width: 36rem;\n  min-height: 220px;\n  padding: clamp(24px, 3vw, 32px);\n  border: 1px solid var(--surface-auth-panel-border);\n  border-radius: var(--radius-card);\n  background: var(--surface-auth-panel-bg);\n  box-shadow: var(--surface-auth-panel-shadow);\n}") {
		t.Fatalf("expected stylesheet to render onboarding content inside a readable auth panel")
	}
	if !strings.Contains(css, ".agent-auth-secret-input {\n  -webkit-text-security: disc;\n}") ||
		!strings.Contains(css, ".agent-auth-secret-input::placeholder {\n  -webkit-text-security: none;\n}") {
		t.Fatalf("expected auth credential text boxes to mask entered secret values")
	}
	if !strings.Contains(css, ".agent-auth-github-shell {\n  max-width: calc(46ch + 2px);\n}") {
		t.Fatalf("expected stylesheet to keep the GitHub token setup shell no wider than the token input")
	}
	if !strings.Contains(css, "--surface-auth-panel-bg:") || !strings.Contains(css, "--surface-auth-panel-border:") || !strings.Contains(css, "--surface-auth-panel-shadow:") {
		t.Fatalf("expected stylesheet to define theme-aware auth panel surface tokens")
	}
	if !strings.Contains(css, "--hub-panel-rgb: 255 255 255;") || !strings.Contains(css, "--hub-panel-rgb: 15 22 38;") {
		t.Fatalf("expected stylesheet to define theme-aware rgb tokens for hub panels")
	}
	if !strings.Contains(css, "--body-linear: linear-gradient(180deg, #0d1424, #0a1120 58%, #09101d);") || !strings.Contains(css, "--body-linear: linear-gradient(180deg, #05070d, #070b14 55%, #090f1a);") {
		t.Fatalf("expected stylesheet to define distinct dark and night backgrounds")
	}
	if !strings.Contains(css, ".task.task-closing") {
		t.Fatalf("expected stylesheet to include task closing styles")
	}
	if !strings.Contains(css, ".task.task-closing {\n  pointer-events: none;\n  opacity: 0;") {
		t.Fatalf("expected stylesheet to fade closing tasks instead of animating them")
	}
	if strings.Contains(css, "@keyframes taskCloseSlideFade") || strings.Contains(css, "@keyframes taskCloseWiggleFade") || strings.Contains(css, "@keyframes taskCloseButtonWiggle") {
		t.Fatalf("expected stylesheet to remove close animations")
	}
	if !strings.Contains(css, ".task-rerun") {
		t.Fatalf("expected stylesheet to include task rerun styles")
	}
	if !strings.Contains(css, ".task-progress-step.current {\n  background: #fff;\n  border-color: rgba(10, 132, 255, 0.34);\n  color: #101626;\n  box-shadow: 0 0 0 3px rgba(10, 132, 255, 0.16);") {
		t.Fatalf("expected stylesheet to render the active task progress step with stronger contrast instead of size scaling")
	}
	if !strings.Contains(css, ".task-progress-step-icon") {
		t.Fatalf("expected stylesheet to include task progress step icon styles")
	}
	if !strings.Contains(css, "filter: brightness(0) saturate(100%);") ||
		!strings.Contains(css, ".task-progress-track {\n  position: relative;\n  height: 42px;\n}") ||
		!strings.Contains(css, ".task-progress-line {\n  position: absolute;\n  left: 26px;\n  right: 26px;\n  top: 50%;") ||
		!strings.Contains(css, ".task-progress-steps {\n  position: relative;\n  display: flex;\n  align-items: center;\n  justify-content: space-between;\n  min-height: 42px;\n}") ||
		!strings.Contains(css, ".task-progress-step {\n  width: 24px;\n  height: 24px;\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;\n  overflow: visible;") ||
		!strings.Contains(css, ".task-progress-step.has-icon {\n  background: color-mix(in srgb, var(--surface-progress-step) 92%, var(--surface-task-button-bg));\n  border: 1px solid rgba(113, 136, 177, 0.32);\n}") ||
		!strings.Contains(css, ".task-progress-step-icon {\n  width: 21.33px;\n  height: 21.33px;") ||
		!strings.Contains(css, ".task-progress-step-glyph {\n  width: 21.33px;\n  height: 21.33px;") ||
		!strings.Contains(css, ".task-progress-step.current .task-progress-step-icon {\n  width: 26.56px;\n  height: 26.56px;\n  opacity: 1;\n}") ||
		!strings.Contains(css, ".task-progress-step.current .task-progress-step-glyph {\n  width: 26.56px;\n  height: 26.56px;\n  opacity: 1;\n}") ||
		!strings.Contains(css, ".task-progress.is-running .task-progress-step.current .task-progress-step-icon,\n.task-progress.is-running .task-progress-step.current .task-progress-step-glyph {\n  animation: taskProgressCurrentSpin 10s linear infinite;\n  transform-origin: center;\n  will-change: transform;\n}") ||
		!strings.Contains(css, "@keyframes taskProgressCurrentSpin {\n  0%,\n  80% {\n    transform: rotate(0deg);\n  }\n  100% {\n    transform: rotate(360deg);\n  }\n}") {
		t.Fatalf("expected stylesheet to render larger progress icons, oversized active icons, and a running spin animation")
	}
	if !strings.Contains(css, ".badge.stopped {\n  background: color-mix(in srgb, var(--surface-badge-idle) 82%, #5f7395);\n  color: #fff;\n}") || !strings.Contains(css, ".task-result.stopped {\n  color: var(--surface-badge-idle);\n  background: rgba(113, 136, 177, 0.13);\n}") {
		t.Fatalf("expected stylesheet to distinguish user-stopped task visuals from hard error states")
	}
	if !strings.Contains(css, ".task-action-glyph-stop {\n  width: 17px;\n  height: 17px;\n  fill: currentColor;\n  stroke: none;\n}") ||
		!strings.Contains(css, ".task-stop {\n  border: 1px solid rgba(17, 34, 68, 0.12);\n  background: var(--surface-task-button-bg);\n  color: var(--text-soft);\n}") ||
		!strings.Contains(css, ".task-stop:hover,\n.task-stop:focus-visible {\n  border-color: color-mix(in srgb, var(--surface-danger) 36%, var(--border));\n  background: color-mix(in srgb, var(--surface-danger) 10%, var(--surface-task-button-bg));\n  color: color-mix(in srgb, var(--surface-danger) 72%, var(--text-soft));\n}") ||
		!strings.Contains(css, ".task-control-toggle.task-icon-button,\n.task-stop.task-icon-button {\n  width: 34px;\n  min-width: 34px;\n  height: 34px;\n  min-height: 34px;\n}") {
		t.Fatalf("expected stylesheet to render neutral stop controls with a larger stop glyph and shared icon-button sizing")
	}
	if !strings.Contains(css, ".task-control-toggle {\n  border: 1px solid rgba(17, 34, 68, 0.12);\n  background: var(--surface-task-button-bg);\n  color: var(--text-soft);\n}") ||
		!strings.Contains(css, ".task-control-toggle.task-control-pause:hover,\n.task-control-toggle.task-control-pause:focus-visible {\n  border-color: color-mix(in srgb, var(--running) 36%, var(--border));\n  background: color-mix(in srgb, var(--running) 8%, var(--surface-task-button-bg));\n  color: var(--running);\n}") ||
		strings.Contains(css, ".task-control-toggle.task-control-pause {\n  border-color: color-mix(in srgb, var(--surface-warning)") {
		t.Fatalf("expected stylesheet to render pause controls with neutral task-button styling instead of warning/yellow treatment")
	}
	if !strings.Contains(css, ".task-body") {
		t.Fatalf("expected stylesheet to include task body column styles")
	}
	if !strings.Contains(css, ".task-top {\n  display: grid;\n  grid-template-columns: minmax(0, 1fr) auto;\n  align-items: center;") {
		t.Fatalf("expected stylesheet to pin task actions in a dedicated trailing column")
	}
	if !strings.Contains(css, ".task.task-prompt-only") {
		t.Fatalf("expected stylesheet to include dedicated compact prompt-only task card styling")
	}
	if !strings.Contains(css, ".task-top-actions {\n  display: flex;\n  align-items: center;\n  justify-content: flex-end;\n  flex-wrap: nowrap;") {
		t.Fatalf("expected stylesheet to keep task action controls on a single right-aligned row")
	}
	if strings.Contains(css, ".task-pr-link") {
		t.Fatalf("expected stylesheet to remove legacy task-pr-link styles")
	}
	if !strings.Contains(css, ".task-output-toggle") {
		t.Fatalf("expected stylesheet to include task output toggle styles")
	}
	if !strings.Contains(css, ".task-terminal-toggle") {
		t.Fatalf("expected stylesheet to include terminal output toggle styles")
	}
	if !strings.Contains(css, ".task-panel-actions {\n  display: inline-flex;\n  align-items: center;\n  gap: 6px;\n}") {
		t.Fatalf("expected stylesheet to group task header action icons on the right side")
	}
	if !strings.Contains(css, ".task-history-toggle,\n.task-view-toggle,\n.task-sound-toggle {") {
		t.Fatalf("expected stylesheet to include compact icon button styles for history, task-view, and task-sound toggles")
	}
	if !strings.Contains(css, ".task-history-toggle-icon") {
		t.Fatalf("expected stylesheet to include task-history icon styles")
	}
	if !strings.Contains(css, ".task-history-toggle[aria-pressed=\"true\"]") {
		t.Fatalf("expected stylesheet to include active-state treatment for task-history mode")
	}
	if !strings.Contains(css, ".task-history-toggle {\n  position: relative;\n}") {
		t.Fatalf("expected stylesheet to anchor the unseen-history plus badge to the history toggle")
	}
	if !strings.Contains(css, ".task-history-toggle-unseen") {
		t.Fatalf("expected stylesheet to highlight history toggle when unseen completed tasks exist")
	}
	if !strings.Contains(css, ".task-history-toggle-badge") {
		t.Fatalf("expected stylesheet to include unread-count badge styles for unseen completed task history")
	}
	if !strings.Contains(css, ".task-history-toggle,\n.task-view-toggle,\n.task-sound-toggle {\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;\n  width: 32px;\n  height: 32px;") {
		t.Fatalf("expected stylesheet to size history, task-view, and task-sound toggles as compact icon affordances")
	}
	if !strings.Contains(css, ".task-view-toggle-icon") {
		t.Fatalf("expected stylesheet to include task-view icon styles")
	}
	if !strings.Contains(css, ".task-view-toggle[aria-pressed=\"true\"]") {
		t.Fatalf("expected stylesheet to include active-state treatment for prompt-only task mode")
	}
	if !strings.Contains(css, ".task-fullscreen-toggle") {
		t.Fatalf("expected stylesheet to include task full screen toggle styles")
	}
	if !strings.Contains(css, ".task-fullscreen-toggle-icon") {
		t.Fatalf("expected stylesheet to include task full screen icon styles")
	}
	if !strings.Contains(css, ".task-fullscreen-toggle {\n  display: inline-flex;\n  width: 32px;\n  height: 32px;") {
		t.Fatalf("expected stylesheet to size the task full screen control as a compact icon affordance")
	}
	if !strings.Contains(css, "display: inline-flex;") {
		t.Fatalf("expected stylesheet to center the task full screen icon with inline-flex button layout")
	}
	if !strings.Contains(css, "background: transparent;") || !strings.Contains(css, "border: 0;") {
		t.Fatalf("expected stylesheet to remove button chrome from the task full screen control")
	}
	if !strings.Contains(css, ".task-fullscreen-close") {
		t.Fatalf("expected stylesheet to include full screen close-state button styles")
	}
	if !strings.Contains(css, ".task-fullscreen-close-icon") {
		t.Fatalf("expected stylesheet to include dedicated full screen close icon styles")
	}
	if !strings.Contains(css, ".sr-only") {
		t.Fatalf("expected stylesheet to include screen-reader-only utility styles for icon buttons")
	}
	if strings.Contains(css, "body.task-fullscreen-open #task-fullscreen-toggle") {
		t.Fatalf("expected stylesheet to stop reusing the panel toggle as the fullscreen close control")
	}
	if strings.Contains(css, "top: max(16px, env(safe-area-inset-top));") || strings.Contains(css, "right: max(16px, env(safe-area-inset-right));") {
		t.Fatalf("expected stylesheet to keep the full screen close control integrated with the panel header instead of the viewport edge")
	}
	if !strings.Contains(css, "background: var(--surface-fullscreen-close-bg);") || !strings.Contains(css, "color: #fff;") {
		t.Fatalf("expected stylesheet to give the full screen close control high-contrast styling")
	}
	if !strings.Contains(css, "inline-size: 32px;") || !strings.Contains(css, "block-size: 32px;") {
		t.Fatalf("expected stylesheet to size the full screen close control as a compact title-bar icon button")
	}
	if !strings.Contains(css, ".task-fullscreen") {
		t.Fatalf("expected stylesheet to include full screen task layout styles")
	}
	if !strings.Contains(css, "#task-panel.task-panel-collapsed.is-empty .task-scroll {\n  display: none;\n}") {
		t.Fatalf("expected stylesheet to keep minimized task panel body hidden for empty task states")
	}
	if !strings.Contains(css, ".task-copy-link {") ||
		!strings.Contains(css, "width: 34px;") ||
		!strings.Contains(css, "height: 34px;") ||
		!strings.Contains(css, "align-self: center;") {
		t.Fatalf("expected stylesheet to render task clone links as fixed-size controls that do not affect task card height")
	}
	if !strings.Contains(css, ".task-side-actions {\n  display: inline-flex;\n  align-items: center;\n  gap: 6px;") {
		t.Fatalf("expected stylesheet to group terminal clone actions in a compact side rail")
	}
	if !strings.Contains(css, ".task-copy-link {") {
		t.Fatalf("expected stylesheet to include task clone icon-button sizing")
	}
	if strings.Contains(css, "align-self: stretch;") {
		t.Fatalf("expected stylesheet to avoid stretching task action links to task card height")
	}
	if strings.Contains(css, ".task.task-has-side-actions {\n  padding-right: 0;\n  gap: 0;") {
		t.Fatalf("expected stylesheet to remove the dedicated right-side PR rail layout")
	}
	if strings.Contains(css, "aspect-ratio: 1 / 1;") {
		t.Fatalf("expected stylesheet to avoid aspect-ratio-driven task action stretching")
	}
	if !strings.Contains(css, ".task-copy-link img {\n  display: block;\n  width: 100%;\n  height: 100%;\n  object-fit: contain;\n  filter: var(--agent-logo-filter);") {
		t.Fatalf("expected stylesheet to scale terminal clone icons inside task clone controls")
	}
	if !strings.Contains(css, ".task-copy-link.is-copied {") {
		t.Fatalf("expected stylesheet to include copied-state feedback for the terminal clone action")
	}
	if !strings.Contains(css, ".task-github-link {") ||
		!strings.Contains(css, ".task-github-link img {\n  display: block;\n  width: 100%;\n  height: 100%;\n  object-fit: contain;\n  filter: var(--agent-logo-filter);") {
		t.Fatalf("expected stylesheet to render completed-task GitHub pull request links as logo buttons")
	}
	if !strings.Contains(css, ".prompt-mode-link.is-disabled,\n.prompt-mode-link[aria-disabled=\"true\"] {") ||
		strings.Contains(css, ".task.task-github-repos-loading .task-id {") {
		t.Fatalf("expected stylesheet to style unavailable chat dock link without repository-loading current work task styles")
	}
	if !strings.Contains(css, ".chat-repo-pagination {") ||
		!strings.Contains(css, ".chat-repo-page-button {") ||
		!strings.Contains(css, ".chat-repo-page-label {") {
		t.Fatalf("expected stylesheet to include chat repository pagination controls")
	}
	if !strings.Contains(css, ".chat-controls {") ||
		!strings.Contains(css, ".chat-search-field {") ||
		!strings.Contains(css, ".chat-search-input {") {
		t.Fatalf("expected stylesheet to include chat repository search controls")
	}
	if !strings.Contains(css, ".task-result-github-link {") ||
		!strings.Contains(css, ".task-result-link-logo {") {
		t.Fatalf("expected stylesheet to size pull request links in task result banners")
	}
	if !strings.Contains(css, ".page-bottom-dock {\n  position: fixed;\n  left: 50%;\n  bottom: max(16px, env(safe-area-inset-bottom));\n  z-index: 61;\n  display: flex;\n  align-items: center;\n  gap: 10px;\n  justify-content: center;") {
		t.Fatalf("expected stylesheet to align the bottom dock tabs and GitHub profile link on a shared row")
	}
	if !strings.Contains(css, ".prompt-mode-link {\n  position: relative;\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;\n  width: 40px;\n  height: 40px;") {
		t.Fatalf("expected segmented dock links to use icon pill sizing within the shared menu")
	}
	if !strings.Contains(css, ".prompt-mode-link-icon img {\n  display: block;\n  width: 15px;\n  height: 15px;") {
		t.Fatalf("expected stylesheet to size dock icons for integrated menu items")
	}
	if !strings.Contains(css, ".prompt-mode-link-logo {\n  min-width: 40px;\n  padding-inline: 0;\n}") {
		t.Fatalf("expected stylesheet to keep icon-only dock items balanced with the text tabs")
	}
	if !strings.Contains(css, ".prompt-mode-divider {\n  width: 1px;\n  height: 24px;\n  margin-inline: 4px;") {
		t.Fatalf("expected stylesheet to visually integrate the leading icon-only dock item into the shared dock with a divider element")
	}
	if !strings.Contains(css, ".task-fullscreen {\n  position: fixed;\n  inset: 0;\n  z-index: 80;\n  padding: 0;") {
		t.Fatalf("expected stylesheet to make full screen task layout use full viewport padding")
	}
	if !strings.Contains(css, ".task-fullscreen-shell {\n  position: relative;") || !strings.Contains(css, "width: 100%;") {
		t.Fatalf("expected stylesheet to make full screen task shell span viewport width")
	}
	if !strings.Contains(css, "min-height: 100dvh;") || !strings.Contains(css, "height: 100dvh;") {
		t.Fatalf("expected stylesheet to size the full screen shell to the dynamic viewport height")
	}
	if strings.Contains(css, ".task-fullscreen-body.task-output-hidden") {
		t.Fatalf("expected stylesheet to remove full screen hidden-output task layout styles")
	}
	if strings.Contains(css, ".right-col.task-output-hidden") {
		t.Fatalf("expected stylesheet to remove standard hidden-output task layout styles")
	}
	if !strings.Contains(css, ".task-fullscreen-task-panel .scroll") {
		t.Fatalf("expected stylesheet to cap focused task metadata height in full screen view")
	}
	if !strings.Contains(css, ".task-fullscreen-output-panel") {
		t.Fatalf("expected stylesheet to include focused full screen output panel styles")
	}
	if !strings.Contains(css, "grid-template-rows: auto auto minmax(0, 1fr);") {
		t.Fatalf("expected stylesheet to dedicate remaining full screen height to the task output terminal")
	}
	if !strings.Contains(css, ".task.task-collapsed") {
		t.Fatalf("expected stylesheet to include collapsed task styles")
	}
	if !strings.Contains(css, ".task.task-compact-state-left {\n  align-items: center;\n  gap: 8px;\n}") ||
		!strings.Contains(css, ".task-current-state.is-running .task-current-state-icon,\n.task-current-state.is-running .task-current-state-glyph {\n  animation: taskProgressCurrentSpin 10s linear infinite;") {
		t.Fatalf("expected stylesheet to position compact task state icons on the left and keep the periodic running spin")
	}
	if strings.Contains(css, ".task-history-list") {
		t.Fatalf("expected stylesheet to remove prompt history list styles")
	}
	if !strings.Contains(css, ".prompt-mode-link") {
		t.Fatalf("expected stylesheet to include prompt mode link styles")
	}
	if !strings.Contains(css, ".prompt-visibility-toggle") {
		t.Fatalf("expected stylesheet to include studio visibility toggle styles")
	}
	if !strings.Contains(css, ".prompt-grid") {
		t.Fatalf("expected stylesheet to include prompt grid styles")
	}
	if !strings.Contains(css, ".brand-logo") {
		t.Fatalf("expected stylesheet to include brand logo styles")
	}
	if !strings.Contains(css, ".brand-logo-group {\n  position: relative;\n  width: 56px;\n  height: 56px;\n  flex-shrink: 0;\n}") {
		t.Fatalf("expected stylesheet to size the rotating header logos to match the title and subtitle stack")
	}
	if !strings.Contains(css, ".brand-logo {\n  display: block;\n  padding: 0;\n  border: 0;\n  border-radius: 0;\n  background: transparent;\n  box-shadow: none;\n}") {
		t.Fatalf("expected stylesheet to keep the moltenhub logo transparent instead of rendering it inside a tile")
	}
	if !strings.Contains(css, ".rotating-brand-logo {\n  position: absolute;\n  inset: 0;\n  display: block;\n  width: 100%;\n  height: 100%;") {
		t.Fatalf("expected stylesheet to make rotating header logos fill the shared logo frame")
	}
	if !strings.Contains(css, ".configured-agent-logo {\n  padding: 0;\n  border: 0;\n  border-radius: 0;\n  background: transparent;\n  box-shadow: none;\n  filter: var(--agent-logo-filter);") {
		t.Fatalf("expected stylesheet to keep rotating configured-agent logos transparent and theme-tinted")
	}
	if !strings.Contains(css, ".agent-auth-url-logo {\n  display: block;\n  width: 58px;\n  height: 58px;\n  padding: 9px;\n  border: 0;\n  border-radius: var(--radius-card);\n  background: transparent;\n  box-shadow: none;\n  filter: var(--agent-logo-filter);") {
		t.Fatalf("expected stylesheet to tint auth-gate agent logos based on active theme")
	}
	if !strings.Contains(css, ".rotating-brand-logo") || !strings.Contains(css, ".brand-logo-visible") {
		t.Fatalf("expected stylesheet to include rotating brand logo cross-fade styles")
	}
	if !strings.Contains(css, ".status-item-metrics") {
		t.Fatalf("expected stylesheet to include metrics pill styles")
	}
	if !strings.Contains(css, ".dot.http") {
		t.Fatalf("expected stylesheet to include HTTP long-poll dot styles")
	}
	if !strings.Contains(css, ".dot.disconnected") {
		t.Fatalf("expected stylesheet to include disconnected dot styles")
	}
	if !strings.Contains(css, ".theme-toggle {\n  position: fixed;\n  right: 16px;\n  bottom: 16px;") || !strings.Contains(css, "  cursor: pointer;\n") {
		t.Fatalf("expected stylesheet to use a pointer cursor for the interactive theme toggle")
	}
	if !strings.Contains(css, ".hub-emoji-picker-toggle {\n  display: inline-flex;") || !strings.Contains(css, "  cursor: pointer;\n") {
		t.Fatalf("expected stylesheet to use a pointer cursor for the interactive emoji picker")
	}
	if !strings.Contains(css, ".hub-emoji-picker-toggle:disabled {\n  opacity: 0.6;\n  transform: none;\n  cursor: not-allowed;\n}") {
		t.Fatalf("expected stylesheet to mark disabled emoji picker buttons as unavailable")
	}
	if strings.Count(css, "cursor:") != 3 {
		t.Fatalf("expected stylesheet to avoid unrelated custom cursor styles")
	}
	if strings.Contains(css, "cursor-not-allowed") {
		t.Fatalf("expected stylesheet to avoid cursor utility classes")
	}
}

func TestHandlerServesStaticSiteHeaderComponent(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/site-header.js", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q", ct)
	}

	script := resp.Body.String()
	required := []string{
		`customElements.define("moltenhub-code-header", MoltenHubCodeHeader);`,
		`customElements.define("moltenhub-code-nav", MoltenHubCodeNav);`,
		`const NAV_ITEMS = Object.freeze([`,
		`{ href: "/", label: "Home" },`,
		"return `<nav class=\"site-page-nav\" aria-label=\"Primary\">${links}</nav>`;",
		`const LOGO_ROTATION_INTERVAL_MS = 8_000;`,
		`localStorage.getItem(THEME_KEY)`,
		`<header class="header site-header">`,
		`<a class="brand-lockup site-header-home" href="/" aria-label="Molten Hub Code home" data-site-header-home>`,
		`id="moltenhub-logo"`,
		`id="configured-agent-logo"`,
		`id="hub-logo-link"`,
		`class="status-item status-item-compact hub-logo-link hub-logo-link-offline"`,
		`href="${HUB_LOGIN_URL}"`,
		`src="https://app.molten.bot/logo.svg"`,
		`id="hub-logo-plus" class="hub-logo-link-plus">+</span>`,
		`opencode: "/static/logos/opencode.svg"`,
		`id="configured-agent-gorilla-subtitle" class="site-header-subtitle">Codex is now a 600LB Gorilla!</span>`,
		`id="local-conn-item" class="status-item status-item-compact status-item-compact-expandable"`,
		`id="hub-conn-item" class="status-item status-item-compact status-item-compact-expandable"`,
		`id="resource-metrics-item" class="status-item status-item-metrics"`,
		`data-lucide="cpu"`,
		`data-lucide="memory-stick"`,
		`data-lucide="hard-drive"`,
		`updateResourceMetrics(snapshot)`,
		`startConnectionStatus`,
		`const response = await fetch("/api/status", { cache: "no-store" });`,
		`connectionStatusStream = new EventSource("/api/stream");`,
		`hubLogoLink.classList.toggle("hidden", connected);`,
		`setHubLogoLink(connected, logoTargetURL);`,
		`${headerState.label} is now a 600LB Gorilla!`,
	}
	for _, needle := range required {
		if !strings.Contains(script, needle) {
			t.Fatalf("expected shared site header component to include %q", needle)
		}
	}
}

func TestHandlerServesStaticEmojiPickerScript(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/emoji-picker.js", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "text/javascript") && !strings.Contains(ct, "application/javascript") {
		t.Fatalf("content-type = %q", ct)
	}

	body := resp.Body.String()
	if !strings.Contains(body, "global.MoltenEmojiPicker") {
		t.Fatalf("expected emoji picker script to expose the picker API")
	}
	if !strings.Contains(body, `hub.ui.emoji.recent`) {
		t.Fatalf("expected emoji picker script to persist recent emoji selections")
	}
	if !strings.Contains(body, `function limitGraphemes(value, maxGraphemes)`) || !strings.Contains(body, `Intl.Segmenter`) {
		t.Fatalf("expected emoji picker script to clamp emoji values by grapheme cluster")
	}
	if !strings.Contains(body, `/static/emoji-mart-browser.js`) || !strings.Contains(body, `/static/emoji-mart-data.json`) || !strings.Contains(body, `new emojiMart.Picker({`) {
		t.Fatalf("expected emoji picker script to lazy-load the vendored emoji-mart assets")
	}
	if !strings.Contains(body, `clearButton.addEventListener("click", () => {`) || !strings.Contains(body, `setValue("");`) {
		t.Fatalf("expected emoji picker script to support clearing the selected emoji")
	}
	if !strings.Contains(body, `Pick one emoji`) || !strings.Contains(body, `Frequently used`) {
		t.Fatalf("expected emoji picker script to render the refreshed panel heading and recent section labels")
	}
	if !strings.Contains(body, `global.addEventListener("mousedown", handleOutsidePointer);`) || !strings.Contains(body, `if (root.contains(target) || panel.contains(target)) {`) {
		t.Fatalf("expected emoji picker script to close on outside click")
	}
	if !strings.Contains(body, `function handleEscape(event) {`) || !strings.Contains(body, `event.key !== "Escape"`) || !strings.Contains(body, `setOpen(false);`) {
		t.Fatalf("expected emoji picker script to close on Escape")
	}
	if !strings.Contains(body, `if (nextDisabled) {`) || !strings.Contains(body, `setOpen(false);`) {
		t.Fatalf("expected emoji picker script to close when disabled")
	}
	if !strings.Contains(body, `if (!emoji || !emoji.native) {`) || !strings.Contains(body, `setValue(emoji.native);`) {
		t.Fatalf("expected emoji picker script to ignore invalid selections and apply valid emoji selections")
	}
}

func TestHandlerServesStaticLogoAsset(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/logos/codex-cli.svg", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "image/svg+xml") {
		t.Fatalf("content-type = %q", ct)
	}
	if body := resp.Body.String(); !strings.Contains(body, "<svg") {
		t.Fatalf("expected svg payload, got %q", body)
	}
}

func TestHandlerServesStaticPiLogoAsset(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/logos/pi.svg", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "image/svg+xml") {
		t.Fatalf("content-type = %q", ct)
	}
	if body := resp.Body.String(); !strings.Contains(body, "<svg") {
		t.Fatalf("expected svg payload, got %q", body)
	}
}

func TestHandlerServesStaticOpenCodeLogoAsset(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/logos/opencode.svg", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "image/svg+xml") {
		t.Fatalf("content-type = %q", ct)
	}
	if body := resp.Body.String(); !strings.Contains(body, `aria-label="OpenCode logo"`) {
		t.Fatalf("expected OpenCode svg payload, got %q", body)
	}
}

func TestHandlerServesTransparentMoltenHubLogoAsset(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/static/logo.svg", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.Contains(ct, "image/svg+xml") {
		t.Fatalf("content-type = %q", ct)
	}

	body := resp.Body.String()
	if !strings.Contains(body, "<svg") {
		t.Fatalf("expected svg payload, got %q", body)
	}
	if strings.Contains(body, "<rect") {
		t.Fatalf("expected moltenhub logo svg to avoid embedded background boxes, got %q", body)
	}
}

func TestIndexLibraryModeUsesDedicatedRunEndpointAndShowsLoadErrors(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}

	markup := resp.Body.String()
	if !strings.Contains(markup, `"/api/library/run"`) {
		t.Fatalf("expected index html to submit library mode runs through /api/library/run")
	}
	if !strings.Contains(markup, `state.libraryLoadError || "No library tasks are available."`) {
		t.Fatalf("expected index html to surface library load errors in the task list")
	}
	if !strings.Contains(markup, `id="library-target-subdir"`) {
		t.Fatalf("expected index html to render a directory input in library mode")
	}
	if !strings.Contains(markup, `targetsubdir: String(libraryTargetSubdir.value || "").trim() || ".",`) {
		t.Fatalf("expected index html to include targetsubdir in the library payload")
	}
	if !strings.Contains(markup, `libraryTargetSubdir.value = targetSubdir;`) {
		t.Fatalf("expected index html to restore the library directory when syncing from JSON payloads")
	}
	if !strings.Contains(markup, `builderTargetSubdir.addEventListener("input", () => {`) ||
		!strings.Contains(markup, `libraryTargetSubdir.value = builderTargetSubdir.value;`) {
		t.Fatalf("expected index html to mirror prompt directory changes into library mode")
	}
	if !strings.Contains(markup, `libraryTargetSubdir.addEventListener("input", () => {`) ||
		!strings.Contains(markup, `builderTargetSubdir.value = libraryTargetSubdir.value;`) {
		t.Fatalf("expected index html to mirror library directory changes back into prompt mode")
	}
}

func TestHandlerLocalPromptSubmitAccepted(t *testing.T) {
	t.Parallel()

	var gotBody string
	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-123", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"update docs"}`
	resp, err := http.Post(ts.URL+"/api/local-prompt", "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
	if requestID, _ := body["request_id"].(string); requestID != "local-123" {
		t.Fatalf("request_id = %q", requestID)
	}
}

func TestHandlerLocalPromptSubmitRecordsTaskSourceHeader(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		b.RecordTaskRunConfig("local-source", body)
		return "local-source", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"update docs"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/local-prompt", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(taskSourceHeader, "chat")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	task, ok := b.Task("local-source")
	if !ok {
		t.Fatal("Task() found = false, want true")
	}
	if got, want := task.Source, "chat"; got != want {
		t.Fatalf("task.Source = %q, want %q", got, want)
	}
}

func TestHandlerLocalPromptSubmitAcceptedWithImages(t *testing.T) {
	t.Parallel()

	var gotBody string
	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-789", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"inspect screenshot","images":[{"name":"shot.png","mediaType":"image/png","dataBase64":"aGVsbG8="}]}`
	resp, err := http.Post(ts.URL+"/api/local-prompt", "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}
}

func TestHandlerLocalPromptSubmitUnavailable(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/local-prompt", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := body["error"].(string); got != "studio submit is unavailable" {
		t.Fatalf("error = %q, want %q", got, "studio submit is unavailable")
	}
}

func TestHandlerLocalPromptSubmitDuplicate(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, _ []byte) (string, error) {
		return "", duplicateSubmissionStubError{
			requestID: "local-111",
			state:     "in_flight",
		}
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/local-prompt", "application/json", bytes.NewBufferString(`{"repo":"x","prompt":"x"}`))
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if duplicate, _ := body["duplicate"].(bool); !duplicate {
		t.Fatalf("duplicate = %#v, want true", body["duplicate"])
	}
	if gotRequestID, _ := body["request_id"].(string); gotRequestID != "local-111" {
		t.Fatalf("request_id = %q, want %q", gotRequestID, "local-111")
	}
	if gotState, _ := body["state"].(string); gotState != "in_flight" {
		t.Fatalf("state = %q, want %q", gotState, "in_flight")
	}
}

func TestHandlerLocalPromptSubmitFailureCreatesVisibleRejectedTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, _ []byte) (string, error) {
		return "", errors.New("invalid run config: prompt failed checks")
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"show this failed prompt"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/local-prompt", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(taskSourceHeader, "json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(snap.Tasks))
	}

	task := snap.Tasks[0]
	if task.Status != "invalid" {
		t.Fatalf("task.Status = %q, want invalid", task.Status)
	}
	if task.Source != "json" {
		t.Fatalf("task.Source = %q, want json", task.Source)
	}
	if task.Prompt != "show this failed prompt" {
		t.Fatalf("task.Prompt = %q, want %q", task.Prompt, "show this failed prompt")
	}
	if task.Error != "invalid run config: prompt failed checks" {
		t.Fatalf("task.Error = %q, want detailed failure", task.Error)
	}
	if task.CanRerun {
		t.Fatal("task.CanRerun = true, want false")
	}
}

func TestHandlerLocalPromptMethodNotAllowed(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/local-prompt")
	if err != nil {
		t.Fatalf("GET /api/local-prompt error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}

func TestHandlerLibraryRunSubmitAccepted(t *testing.T) {
	t.Parallel()

	var gotBody string
	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-lib-123", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"repos":["git@github.com:acme/repo.git","git@github.com:acme/repo-two.git"],"branch":"main","targetSubdir":"internal/hub","libraryTaskName":"unit-test-coverage","images":[{"name":"shot.png","mediaType":"image/png","dataBase64":"aGVsbG8="}]}`
	resp, err := http.Post(ts.URL+"/api/library/run", "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("POST /api/library/run error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
	if requestID, _ := body["request_id"].(string); requestID != "local-lib-123" {
		t.Fatalf("request_id = %q", requestID)
	}
}

func TestHandlerLibraryRunUnavailable(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/library/run", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST /api/library/run error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := body["error"].(string); got != "library task submit is unavailable" {
		t.Fatalf("error = %q, want %q", got, "library task submit is unavailable")
	}
}

func TestHandlerLibraryRunMethodNotAllowed(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/library/run")
	if err != nil {
		t.Fatalf("GET /api/library/run error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}

func TestHandlerTaskRerunAcceptedRemovesCompletedSourceTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-100"
	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"rerun this"}`
	b.RecordTaskRunConfig(requestID, []byte(payload))
	b.IngestLog("dispatch status=start request_id=req-100")
	b.IngestLog("dispatch status=completed request_id=req-100 workspace=/tmp/run branch=moltenhub-rerun")

	var gotBody string
	var closeCalls []string
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-456", nil
	}
	srv.CloseTask = func(_ context.Context, requestID string) error {
		closeCalls = append(closeCalls, requestID)
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/"+requestID+"/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/%s/rerun error = %v", requestID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
	if forced, _ := body["forced"].(bool); forced {
		t.Fatalf("forced = %#v, want false", body["forced"])
	}
	if gotRequestID, _ := body["request_id"].(string); gotRequestID != "local-456" {
		t.Fatalf("request_id = %q, want %q", gotRequestID, "local-456")
	}
	if gotRerunOf, _ := body["rerun_of"].(string); gotRerunOf != requestID {
		t.Fatalf("rerun_of = %q, want %q", gotRerunOf, requestID)
	}
	if len(closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", closeCalls)
	}
	if _, ok := b.TaskRunConfig(requestID); !ok {
		t.Fatalf("TaskRunConfig(%q) missing after rerun, want preserved", requestID)
	}
	snap := b.Snapshot()
	if got := len(snap.Tasks); got != 0 {
		t.Fatalf("len(tasks) after rerun = %d, want 0", got)
	}
	attempts := b.TaskAttempts(requestID)
	if len(attempts) != 2 {
		t.Fatalf("len(attempts) = %d, want 2: %#v", len(attempts), attempts)
	}
	if got, want := attempts[1].RequestID, "local-456"; got != want {
		t.Fatalf("attempt[1].RequestID = %q, want %q", got, want)
	}
	if got, want := attempts[1].RerunOf, requestID; got != want {
		t.Fatalf("attempt[1].RerunOf = %q, want %q", got, want)
	}
}

func TestHandlerTaskRerunUsesDedicatedSubmitterWhenConfigured(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-rerun-hook"
	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"rerun this"}`
	b.RecordTaskRunConfig(requestID, []byte(payload))

	var (
		gotRequestID string
		gotBody      string
		gotForce     bool
	)
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, _ []byte) (string, error) {
		t.Fatal("SubmitLocalPrompt should not be called when SubmitTaskRerun is configured")
		return "", nil
	}
	srv.SubmitTaskRerun = func(_ context.Context, rerunOf string, body []byte, force bool) (string, error) {
		gotRequestID = rerunOf
		gotBody = string(body)
		gotForce = force
		return "local-999", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/"+requestID+"/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/%s/rerun error = %v", requestID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotRequestID != requestID {
		t.Fatalf("rerunOf = %q, want %q", gotRequestID, requestID)
	}
	if gotBody != payload {
		t.Fatalf("submitted body = %q, want %q", gotBody, payload)
	}
	if gotForce {
		t.Fatal("force = true, want false")
	}
}

func TestHandlerTaskRerunLeavesIncompleteSourceTaskVisible(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-rerun-running"
	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"rerun this"}`
	b.RecordTaskRunConfig(requestID, []byte(payload))
	b.IngestLog("dispatch status=start request_id=req-rerun-running")

	var cleanupCalls int
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		if string(body) != payload {
			t.Fatalf("submitted body = %q, want %q", string(body), payload)
		}
		return "local-457", nil
	}
	srv.CloseTask = func(_ context.Context, requestID string) error {
		cleanupCalls++
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/"+requestID+"/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/%s/rerun error = %v", requestID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if cleanupCalls != 0 {
		t.Fatalf("cleanup calls = %d, want 0", cleanupCalls)
	}
	if _, ok := b.TaskRunConfig(requestID); !ok {
		t.Fatalf("TaskRunConfig(%q) missing after rerun of incomplete task", requestID)
	}
	if got := len(b.Snapshot().Tasks); got != 1 {
		t.Fatalf("len(tasks) after rerun of incomplete task = %d, want 1", got)
	}
}

func TestHandlerTaskRerunPropagatesForceFlag(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-rerun-force"
	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"rerun this"}`
	b.RecordTaskRunConfig(requestID, []byte(payload))

	var gotForce bool
	srv := NewServer("", b)
	srv.SubmitTaskRerun = func(_ context.Context, rerunOf string, body []byte, force bool) (string, error) {
		if rerunOf != requestID {
			t.Fatalf("rerunOf = %q, want %q", rerunOf, requestID)
		}
		if string(body) != payload {
			t.Fatalf("submitted body = %q, want %q", string(body), payload)
		}
		gotForce = force
		return "local-force-1", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/"+requestID+"/rerun?force=yes", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/%s/rerun?force=yes error = %v", requestID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if !gotForce {
		t.Fatal("force = false, want true")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if forced, _ := body["forced"].(bool); !forced {
		t.Fatalf("forced = %#v, want true", body["forced"])
	}
}

func TestHandlerTaskRerunUnavailable(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.RecordTaskRunConfig("req-1", []byte(`{"repo":"x","prompt":"x"}`))
	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-1/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-1/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}

func TestHandlerTaskRerunDuplicate(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.RecordTaskRunConfig("req-dup-rerun", []byte(`{"repo":"x","prompt":"x"}`))

	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, _ []byte) (string, error) {
		return "", duplicateSubmissionStubError{
			requestID: "local-222",
			state:     "completed",
		}
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-dup-rerun/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-dup-rerun/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if duplicate, _ := body["duplicate"].(bool); !duplicate {
		t.Fatalf("duplicate = %#v, want true", body["duplicate"])
	}
	if gotRequestID, _ := body["request_id"].(string); gotRequestID != "local-222" {
		t.Fatalf("request_id = %q, want %q", gotRequestID, "local-222")
	}
	if gotState, _ := body["state"].(string); gotState != "completed" {
		t.Fatalf("state = %q, want %q", gotState, "completed")
	}
}

func TestHandlerTaskRerunMissingConfig(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		return "local-777", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-missing/rerun", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-missing/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandlerTaskRerunMethodNotAllowed(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.RecordTaskRunConfig("req-2", []byte(`{"repo":"x","prompt":"x"}`))
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		return "local-789", nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/tasks/req-2/rerun")
	if err != nil {
		t.Fatalf("GET /api/tasks/req-2/rerun error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}

func TestHandlerTaskCloseAccepted(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.RecordTaskRunConfig("req-close", []byte(`{"repo":"x","prompt":"x"}`))
	b.IngestLog("dispatch status=start request_id=req-close")
	b.IngestLog("dispatch status=completed request_id=req-close workspace=/tmp/run branch=moltenhub-close")

	var closedID string
	srv := NewServer("", b)
	srv.CloseTask = func(_ context.Context, requestID string) error {
		closedID = requestID
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-close/close", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-close/close error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if closedID != "req-close" {
		t.Fatalf("closed request id = %q, want %q", closedID, "req-close")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("ok = %#v, want true", body["ok"])
	}
	if closed, _ := body["closed"].(bool); !closed {
		t.Fatalf("closed = %#v, want true", body["closed"])
	}

	snap := b.Snapshot()
	if len(snap.Tasks) != 0 {
		t.Fatalf("len(tasks) = %d, want 0", len(snap.Tasks))
	}
}

func TestHandlerTaskCloseRejectsRunningTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-running")
	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-running/close", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-running/close error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestHandlerTaskCloseMissingTask(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-missing/close", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-missing/close error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandlerTaskCloseMethodNotAllowed(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-close-method")
	b.IngestLog("dispatch status=error request_id=req-close-method err=\"failed\"")
	srv := NewServer("", b)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/tasks/req-close-method/close")
	if err != nil {
		t.Fatalf("GET /api/tasks/req-close-method/close error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}

func TestHandlerTaskPauseAccepted(t *testing.T) {
	t.Parallel()

	var gotRequestID string
	srv := NewServer("", NewBroker())
	srv.PauseTask = func(_ context.Context, requestID string) error {
		gotRequestID = requestID
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-pause/pause", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-pause/pause error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if gotRequestID != "req-pause" {
		t.Fatalf("pause request id = %q, want %q", gotRequestID, "req-pause")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := body["action"].(string); got != "pause" {
		t.Fatalf("action = %q, want %q", got, "pause")
	}
	if got, _ := body["status"].(string); got != "paused" {
		t.Fatalf("status = %q, want %q", got, "paused")
	}
}

func TestHandlerTaskRunAccepted(t *testing.T) {
	t.Parallel()

	var gotRequestID string
	srv := NewServer("", NewBroker())
	srv.RunTask = func(_ context.Context, requestID string) error {
		gotRequestID = requestID
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-run/run", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-run/run error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if gotRequestID != "req-run" {
		t.Fatalf("run request id = %q, want %q", gotRequestID, "req-run")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if forced, _ := body["forced"].(bool); forced {
		t.Fatalf("forced = %#v, want false", body["forced"])
	}
}

func TestHandlerTaskRunForceAccepted(t *testing.T) {
	t.Parallel()

	var (
		forceRequestID string
		runCalled      bool
	)
	srv := NewServer("", NewBroker())
	srv.RunTask = func(_ context.Context, requestID string) error {
		runCalled = true
		return nil
	}
	srv.ForceRunTask = func(_ context.Context, requestID string) error {
		forceRequestID = requestID
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-force/run?force=yes", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-force/run?force=yes error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if runCalled {
		t.Fatal("RunTask handler called for forced run, want ForceRunTask handler")
	}
	if forceRequestID != "req-force" {
		t.Fatalf("force run request id = %q, want %q", forceRequestID, "req-force")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if forced, _ := body["forced"].(bool); !forced {
		t.Fatalf("forced = %#v, want true", body["forced"])
	}
}

func TestHandlerTaskRunForceFallsBackToRunHandlerWhenNoForceHandler(t *testing.T) {
	t.Parallel()

	var gotRequestID string
	srv := NewServer("", NewBroker())
	srv.RunTask = func(_ context.Context, requestID string) error {
		gotRequestID = requestID
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-force-fallback/run?force=1", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-force-fallback/run?force=1 error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if gotRequestID != "req-force-fallback" {
		t.Fatalf("run request id = %q, want %q", gotRequestID, "req-force-fallback")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if forced, _ := body["forced"].(bool); !forced {
		t.Fatalf("forced = %#v, want true", body["forced"])
	}
}

func TestHandlerTaskStopAccepted(t *testing.T) {
	t.Parallel()

	var gotRequestID string
	srv := NewServer("", NewBroker())
	srv.StopTask = func(_ context.Context, requestID string) error {
		gotRequestID = requestID
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-stop/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-stop/stop error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if gotRequestID != "req-stop" {
		t.Fatalf("stop request id = %q, want %q", gotRequestID, "req-stop")
	}
}

func TestHandlerTaskControlReturnsNotFound(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.PauseTask = func(_ context.Context, requestID string) error {
		return ErrTaskNotFound
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-missing/pause", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-missing/pause error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandlerTaskControlReturnsUnavailableForExistingUncontrolledTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog("dispatch status=start request_id=req-remote")

	srv := NewServer("", b)
	srv.StopTask = func(_ context.Context, requestID string) error {
		if requestID != "req-remote" {
			t.Fatalf("requestID = %q, want %q", requestID, "req-remote")
		}
		return ErrTaskNotFound
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-remote/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-remote/stop error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["error"]; got != "task stop is unavailable for this task" {
		t.Fatalf("error = %#v, want unavailable task control message", got)
	}
}

func TestHandlerTaskControlReturnsAlreadyStoppedForFinishedTask(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.IngestLog(`dispatch status=stopped request_id=req-stop-finished err="task was stopped by operator"`)

	srv := NewServer("", b)
	srv.StopTask = func(_ context.Context, requestID string) error {
		if requestID != "req-stop-finished" {
			t.Fatalf("requestID = %q, want %q", requestID, "req-stop-finished")
		}
		return ErrTaskNotFound
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/tasks/req-stop-finished/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/req-stop-finished/stop error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["error"]; got != "task is already stopped" {
		t.Fatalf("error = %#v, want already stopped message", got)
	}
}

func TestHandlerTaskControlMethodNotAllowed(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.StopTask = func(_ context.Context, requestID string) error {
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/tasks/req-stop/stop")
	if err != nil {
		t.Fatalf("GET /api/tasks/req-stop/stop error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}
}
