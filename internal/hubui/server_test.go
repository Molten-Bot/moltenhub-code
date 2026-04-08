package hubui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jef/moltenhub-code/internal/library"
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
	if !strings.Contains(markup, `src="https://cdn.tailwindcss.com"`) {
		t.Fatalf("expected index html to include tailwind runtime")
	}
	if !strings.Contains(markup, `href="/static/style.css"`) {
		t.Fatalf("expected index html to include external stylesheet link")
	}
	if !strings.Contains(markup, `<title>Molten Hub Code</title>`) {
		t.Fatalf("expected index html to set app title to Molten Hub Code")
	}
	if !strings.Contains(markup, `>Molten Hub Code</div>`) {
		t.Fatalf("expected index html to render app heading as Molten Hub Code")
	}
	if strings.Contains(markup, `id="configured-agent-subtitle"`) || strings.Contains(markup, "Configured agent: Codex") {
		t.Fatalf("expected index html to remove the configured agent subtitle copy")
	}
	if !strings.Contains(markup, `id="configured-agent-gorilla-subtitle"`) || !strings.Contains(markup, "Codex is now a 600LB Gorilla!") {
		t.Fatalf("expected index html to include gorilla subtitle copy")
	}
	if !strings.Contains(markup, `id="configured-agent-gorilla-subtitle" class="text-base font-semibold text-hub-meta"`) {
		t.Fatalf("expected index html to render a larger gorilla subtitle")
	}
	if !strings.Contains(markup, `src="https://molten.bot/logo.svg"`) {
		t.Fatalf("expected index html to include moltenhub logo")
	}
	if !strings.Contains(markup, `"task-close"`) {
		t.Fatalf("expected index html to include task close class usage")
	}
	if !strings.Contains(markup, `"task-closing"`) {
		t.Fatalf("expected index html to include task closing class usage")
	}
	if !strings.Contains(markup, `"task-rerun"`) {
		t.Fatalf("expected index html to include task rerun class usage")
	}
	if !strings.Contains(markup, "function dismissTask(") {
		t.Fatalf("expected index html to include dismissTask handler")
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
	if !strings.Contains(markup, "function rerunTask(") {
		t.Fatalf("expected index html to include rerunTask handler")
	}
	if !strings.Contains(markup, `"task-progress"`) {
		t.Fatalf("expected index html to include task progress class usage")
	}
	if !strings.Contains(markup, "function renderTaskProgress(") {
		t.Fatalf("expected index html to include renderTaskProgress handler")
	}
	if strings.Contains(markup, "current step:") {
		t.Fatalf("expected index html to remove current step label text from task progress")
	}
	if !strings.Contains(markup, "function formatTaskBranch(") {
		t.Fatalf("expected index html to include branch formatter for task metadata")
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
	if !strings.Contains(markup, "const taskPanel = document.getElementById(\"task-panel\");") {
		t.Fatalf("expected index html to cache the task panel element")
	}
	if !strings.Contains(markup, "if (open && !displayTasks(state.snapshot).length) {") {
		t.Fatalf("expected index html to block fullscreen when no tasks exist")
	}
	if !strings.Contains(markup, `<html lang="en" class="dark">`) {
		t.Fatalf("expected index html to default to dark theme class")
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
	if !strings.Contains(markup, `promptVisibilityToggle.hidden = automatic;`) {
		t.Fatalf("expected index html to keep the studio toggle available outside automatic mode")
	}
	if !strings.Contains(markup, `if (!state.promptVisible && !Boolean(state.ui?.automaticMode)) {`) {
		t.Fatalf("expected index html to auto-expand studio when a mode tab is selected")
	}
	if !strings.Contains(markup, `id="task-panel" class="panel min-h-[220px] overflow-hidden rounded-2xl border border-hub-border bg-hub-panel bg-[linear-gradient(170deg,rgba(255,255,255,0.02),rgba(255,255,255,0.01))] hidden" aria-hidden="true"`) {
		t.Fatalf("expected index html to keep task panel hidden before tasks exist")
	}
	if !strings.Contains(markup, `>Task View</span>`) {
		t.Fatalf("expected index html to render the task panel under a Task View heading")
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
	if !strings.Contains(markup, `id="task-fullscreen-terminal"`) {
		t.Fatalf("expected index html to include full screen terminal output")
	}
	if !strings.Contains(markup, `id="task-fullscreen-close"`) {
		t.Fatalf("expected index html to include a dedicated full screen close control")
	}
	if !strings.Contains(markup, `class="task-fullscreen-close-icon"`) || !strings.Contains(markup, "&times;") {
		t.Fatalf("expected index html to render the full screen close control as an X icon button")
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
	if strings.Contains(markup, "taskFullscreenBody.classList.toggle(\"task-output-hidden\", !outputVisible);") {
		t.Fatalf("expected index html to remove full screen output visibility toggling")
	}
	if !strings.Contains(markup, "const taskFullscreenClose = document.getElementById(\"task-fullscreen-close\");") {
		t.Fatalf("expected index html to cache the dedicated full screen close control")
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
	if !strings.Contains(markup, "rightCol.classList.toggle(\"task-list-hidden\", !hasTasks);") {
		t.Fatalf("expected index html to collapse the standard layout when there are no tasks")
	}
	if !strings.Contains(markup, "taskPanel.classList.toggle(\"hidden\", !hasTasks);") {
		t.Fatalf("expected index html to hide the task panel when there are no tasks")
	}
	if !strings.Contains(markup, "openTaskOutput(task.request_id);") {
		t.Fatalf("expected index html to open focused full screen output from the task action")
	}
	if strings.Contains(markup, "Output hidden. Click Open Output to view terminal logs.") {
		t.Fatalf("expected index html to remove hidden-output placeholder copy")
	}
	if strings.Contains(markup, "stage.textContent = `stage:") {
		t.Fatalf("expected index html to remove stage metadata line from task cards")
	}
	if !strings.Contains(markup, "branch.textContent = `branch: ${formatTaskBranch(task)}`;") {
		t.Fatalf("expected index html to render branch metadata beneath repos")
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
	if !strings.Contains(markup, "return \"(prompt unavailable)\";") {
		t.Fatalf("expected index html to provide prompt-only task titles with fallback text")
	}
	if !strings.Contains(markup, "id.title = prompt;") {
		t.Fatalf("expected index html task title tooltip to contain prompt text only")
	}
	if !strings.Contains(markup, `id="local-conn-text"`) {
		t.Fatalf("expected index html to include local connection indicator")
	}
	if !strings.Contains(markup, `title="Local: Connecting..."`) {
		t.Fatalf("expected index html to initialize local indicator tooltip copy")
	}
	if !strings.Contains(markup, `id="hub-conn-text"`) {
		t.Fatalf("expected index html to include moltenhub connection indicator")
	}
	if !strings.Contains(markup, `title="Molten Hub: Waiting for hub status..."`) {
		t.Fatalf("expected index html to initialize hub indicator tooltip copy")
	}
	if !strings.Contains(markup, `setIndicator(localConnItem, localConnDot, localConnText, "Local", online, text);`) {
		t.Fatalf("expected index html to render local indicator label as Local")
	}
	if !strings.Contains(markup, `setIndicator(hubConnItem, hubConnDot, hubConnText, "Molten Hub", online, text);`) {
		t.Fatalf("expected index html to render hub indicator label as Molten Hub")
	}
	if !strings.Contains(markup, "function applyHubDotMode(") {
		t.Fatalf("expected index html to include hub transport dot mode handler")
	}
	if !strings.Contains(markup, "conn.hub_transport") {
		t.Fatalf("expected index html to read hub transport mode from connection state")
	}
	if !strings.Contains(markup, "Connected via WebSocket") {
		t.Fatalf("expected index html to include websocket connection copy")
	}
	if !strings.Contains(markup, "Connected via HTTP long polling") {
		t.Fatalf("expected index html to include HTTP long-polling connection copy")
	}
	if !strings.Contains(markup, `const HUB_CONNECT_URL = "https://app.molten.bot";`) {
		t.Fatalf("expected index html to define the molten hub connect url")
	}
	if !strings.Contains(markup, `text = actionable ? "Connect to Molten Hub" : text;`) {
		t.Fatalf("expected index html to render connect CTA copy when hub is disconnected")
	}
	if !strings.Contains(markup, `hubConnItem.addEventListener("click", maybeOpenHubConnectPage);`) {
		t.Fatalf("expected index html to open the molten hub app when the disconnected indicator is clicked")
	}
	if !strings.Contains(markup, `window.open(HUB_CONNECT_URL, "_blank", "noopener,noreferrer");`) {
		t.Fatalf("expected index html to open the molten hub app in a new page")
	}
	if !strings.Contains(markup, `hubConnItem.classList.toggle("status-item-action", actionable);`) {
		t.Fatalf("expected index html to mark the disconnected hub indicator as actionable")
	}
	if !strings.Contains(markup, `id="prompt-visibility-toggle"`) {
		t.Fatalf("expected index html to include studio visibility toggle")
	}
	if !strings.Contains(markup, `aria-label="Minimize Studio panel"`) || !strings.Contains(markup, `title="Minimize Studio panel">▾</button>`) {
		t.Fatalf("expected index html to initialize the studio toggle as an arrow minimize control")
	}
	if !strings.Contains(markup, `>Studio</span>`) {
		t.Fatalf("expected index html to render the studio panel under a Studio heading")
	}
	if !strings.Contains(markup, "library-task-option-prompt") {
		t.Fatalf("expected index html to include expandable library prompt sections")
	}
	if !strings.Contains(markup, "button.setAttribute(\"aria-expanded\", String(entry.name === selected));") {
		t.Fatalf("expected index html to mark the selected library task as expanded")
	}
	if strings.Contains(markup, "library-task-option-name") {
		t.Fatalf("expected index html to stop rendering library task internal names")
	}
	if !strings.Contains(markup, `id="resource-metrics-text"`) {
		t.Fatalf("expected index html to include resource metrics indicator")
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
	if !strings.Contains(markup, `window.matchMedia("(max-width: 720px)")`) {
		t.Fatalf("expected index html to switch metrics into compact mode across mobile widths")
	}
	if !strings.Contains(markup, "function formatCompactMetricNumber(") {
		t.Fatalf("expected index html to include compact metric formatter")
	}
	if !strings.Contains(markup, `id="prompt-mode-builder"`) {
		t.Fatalf("expected index html to include builder mode toggle")
	}
	if !strings.Contains(markup, `id="prompt-mode-library"`) {
		t.Fatalf("expected index html to include library mode toggle")
	}
	if !strings.Contains(markup, `id="prompt-mode-json"`) {
		t.Fatalf("expected index html to include json mode toggle")
	}
	if !strings.Contains(markup, `class="prompt-mode-link active" href="#studio-builder" role="tab" aria-selected="true"`) {
		t.Fatalf("expected builder mode to render as an anchor-style control instead of a button section")
	}
	if !strings.Contains(markup, `class="prompt-mode-link" href="#studio-library" role="tab" aria-selected="false"`) {
		t.Fatalf("expected library mode to render as an anchor-style control instead of a button section")
	}
	if !strings.Contains(markup, `class="prompt-mode-link" href="#studio-json" role="tab" aria-selected="false"`) {
		t.Fatalf("expected json mode to render as an anchor-style control instead of a button section")
	}
	if !strings.Contains(markup, `class="page-bottom-dock"`) || !strings.Contains(markup, `class="prompt-mode-tabs prompt-mode-tabs-dock"`) {
		t.Fatalf("expected index html to render the mode toggles in the bottom dock")
	}
	if strings.Index(markup, `id="task-panel"`) > strings.Index(markup, `class="panel prompt-wrap`) {
		t.Fatalf("expected index html to render Task View before Studio in the page layout")
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
	if !strings.Contains(markup, `id="builder-image-field" class="prompt-field grid gap-2 w-full max-w-full"`) {
		t.Fatalf("expected index html to render screenshot field at full width")
	}
	if !strings.Contains(markup, ">Paste screenshots.<") {
		t.Fatalf("expected index html to render concise screenshot paste copy")
	}
	if strings.Contains(markup, ">Paste screenshots here.<") {
		t.Fatalf("expected index html to remove old screenshot paste copy")
	}
	if !strings.Contains(markup, `id="builder-image-list"`) {
		t.Fatalf("expected index html to include screenshot attachment list")
	}
	if !strings.Contains(markup, `row.className = "prompt-image-chip";`) {
		t.Fatalf("expected index html to render screenshot attachments with a dedicated light-theme chip class")
	}
	if strings.Contains(markup, ">Screenshots<") {
		t.Fatalf("expected index html to remove the screenshots title label")
	}
	if strings.Contains(markup, "No screenshots attached.") {
		t.Fatalf("expected index html to hide screenshot empty-state copy until images are attached")
	}
	if !strings.Contains(markup, `id="local-prompt-submit"`) || !strings.Contains(markup, `>Run</button>`) {
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
	if !strings.Contains(markup, `id="builder-images-clear"`) || !strings.Contains(markup, `class="prompt-action-button prompt-action-clear"`) {
		t.Fatalf("expected index html to render Clear with shared action sizing class")
	}
	if !strings.Contains(markup, `id="local-prompt-submit" class="prompt-action-button prompt-submit"`) {
		t.Fatalf("expected index html to keep the Run button in prompt actions")
	}
	if !strings.Contains(markup, `const QUEUED_STATUS_TIMEOUT_MS = 8_000;`) {
		t.Fatalf("expected index html to include queued status timeout constant")
	}
	if !strings.Contains(markup, `return String(text || "").startsWith("Queued request ");`) {
		t.Fatalf("expected index html to auto-dismiss queued status text")
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
	if !strings.Contains(markup, `id="builder-base-branch-clear"`) {
		t.Fatalf("expected index html to include branch clear action")
	}
	if !strings.Contains(markup, `data-has-action="false"`) ||
		!strings.Contains(markup, `aria-hidden="true"`) ||
		!strings.Contains(markup, `hidden`) {
		t.Fatalf("expected index html to hide the branch clear action while already on main")
	}
	if !strings.Contains(markup, `class="prompt-grid"`) ||
		!strings.Contains(markup, `id="builder-repo-history-field" class="prompt-field prompt-field-repo-history"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-repository"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-base-branch"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-target-subdir"`) {
		t.Fatalf("expected index html to include the builder row with explicit field layout classes")
	}
	if !strings.Contains(markup, "function syncBaseBranchClearState(") ||
		!strings.Contains(markup, "builderBaseBranchClear.hidden = isMain;") ||
		!strings.Contains(markup, "branchActionWrap.dataset.hasAction = isMain ? \"false\" : \"true\";") ||
		!strings.Contains(markup, "builderBaseBranchClear.addEventListener(\"click\", resetBaseBranchToMain);") {
		t.Fatalf("expected index html to include branch clear behavior")
	}
	if !strings.Contains(markup, "function resetBuilderTargetSubdir(") || !strings.Contains(markup, "builderTargetSubdir.value = \".\";") {
		t.Fatalf("expected index html to include target subdir reset behavior")
	}
	if !strings.Contains(markup, "function clearBuilderPromptDraft(") {
		t.Fatalf("expected index html to include prompt clear handler")
	}
	if !strings.Contains(markup, "function hasBuilderDraftToClear(") ||
		!strings.Contains(markup, "const promptDirty = String(builderPromptInput?.value || \"\").trim() !== \"\";") ||
		!strings.Contains(markup, "const branchDirty = ![\"\", \"main\"].includes(String(builderBaseBranch?.value || \"\").trim());") ||
		!strings.Contains(markup, "const targetSubdirDirty = ![\"\", \".\"].includes(String(builderTargetSubdir?.value || \"\").trim());") ||
		!strings.Contains(markup, "const rawDirty = String(localPromptInput?.value || \"\").trim() !== \"\";") {
		t.Fatalf("expected index html to detect clearable builder draft changes")
	}
	if !strings.Contains(markup, "function syncBuilderDraftClearState(") ||
		!strings.Contains(markup, "builderImagesClear.disabled = !hasBuilderDraftToClear();") {
		t.Fatalf("expected index html to keep the shared Clear button enabled for any clearable draft state")
	}
	if !strings.Contains(markup, "builderImagesClear.addEventListener(\"click\", clearBuilderPromptDraft);") {
		t.Fatalf("expected index html Clear button to reset the full builder draft")
	}
	if !strings.Contains(markup, "builderPromptInput.addEventListener(\"input\", syncBuilderDraftClearState);") ||
		!strings.Contains(markup, "builderTargetSubdir.addEventListener(\"input\", syncBuilderDraftClearState);") ||
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
	if !strings.Contains(markup, "function defaultRepoSelection(") {
		t.Fatalf("expected index html to include repo history default selection helper")
	}
	if !strings.Contains(markup, "if (state.repoHistory.length > 0 && unique.length > 0)") {
		t.Fatalf("expected index html to default repo selection to saved history when available")
	}
	if !strings.Contains(markup, "const nextValue = manualSelected && currentValue") ||
		!strings.Contains(markup, "defaultRepoSelection(currentValue, manualSelected ? \"\" : selectedValue, unique);") ||
		!strings.Contains(markup, "if (nextValue) {") ||
		!strings.Contains(markup, "input.value = nextValue;") {
		t.Fatalf("expected index html to sync default saved repo selection into the repository input")
	}
	if !strings.Contains(markup, "function dropReposFromHistory(") {
		t.Fatalf("expected index html to include repo history cleanup helper")
	}
	if !strings.Contains(markup, "function isCloneMissingRepoError(") {
		t.Fatalf("expected index html to include clone failure repo cleanup matcher")
	}
	if !strings.Contains(markup, "dropReposFromHistory(failedCloneRepos);") {
		t.Fatalf("expected index html to drop missing repositories from history on clone failures")
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
	if !strings.Contains(markup, `pauseRun.textContent = paused ? "▶" : "||";`) {
		t.Fatalf("expected index html to render task pause/run icon control")
	}
	if !strings.Contains(markup, `stop.textContent = "■";`) {
		t.Fatalf("expected index html to render task stop icon control")
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
	if !strings.Contains(markup, "configuredAgentGorillaSubtitle.textContent = `${label} is now a 600LB Gorilla!`;") {
		t.Fatalf("expected index html to render dynamic gorilla subtitle copy")
	}
	if !strings.Contains(markup, "function handlePromptImagePaste(") {
		t.Fatalf("expected index html to include screenshot paste handler")
	}
	if !strings.Contains(markup, "function clearPromptImages(syncRaw = true)") {
		t.Fatalf("expected index html to allow screenshot clearing without forcing raw prompt resync")
	}
	if !strings.Contains(markup, "function clearSubmittedPromptDraft(") {
		t.Fatalf("expected index html to include submitted prompt clearing helper")
	}
	if !strings.Contains(markup, "builderPromptInput.value = \"\";") || !strings.Contains(markup, "localPromptInput.value = \"\";") {
		t.Fatalf("expected index html to clear builder and raw prompt inputs after submit")
	}
	if !strings.Contains(markup, "function clearSubmittedPromptState(") {
		t.Fatalf("expected index html to include queued-submit cleanup helper")
	}
	if !strings.Contains(markup, "clearPromptImages(false);") {
		t.Fatalf("expected index html to clear attached screenshots after a successful submit without repopulating raw JSON")
	}
	if !strings.Contains(markup, "resetBuilderTargetSubdir();") || !strings.Contains(markup, "resetBaseBranchToMain(false);") {
		t.Fatalf("expected index html to reset branch and target subdir as part of queued-submit cleanup")
	}
	if !strings.Contains(markup, "clearSubmittedPromptState();") {
		t.Fatalf("expected index html to clear the submitted prompt state after a successful queue")
	}
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"","configuredAgentLabel":"Codex"};`) {
		t.Fatalf("expected index html to include default UI config")
	}
	if !strings.Contains(markup, `id="theme-cycle"`) || !strings.Contains(markup, `function nextThemeMode(theme)`) {
		t.Fatalf("expected index html to include docked theme cycle control")
	}
	if !strings.Contains(markup, `const DEFAULT_THEME_MODE = "dark";`) {
		t.Fatalf("expected index html to define dark as the default theme mode")
	}
	if !strings.Contains(markup, `return THEME_MODES.includes(raw) ? raw : DEFAULT_THEME_MODE;`) {
		t.Fatalf("expected index html theme loading to fall back to the default dark theme")
	}
	if !strings.Contains(markup, `<span id="theme-cycle-current" class="theme-cycle-current">Dark</span>`) {
		t.Fatalf("expected index html to render dark as the initial theme cycle label")
	}
	if strings.Contains(markup, `theme-cycle-next`) || strings.Contains(markup, `>Theme<`) || strings.Contains(markup, `Next: Dark`) {
		t.Fatalf("expected index html to render the theme dock as a single cycling label")
	}
	if !strings.Contains(markup, `rgb(var(--hub-panel-rgb) / <alpha-value>)`) || !strings.Contains(markup, `rgb(var(--hub-text-rgb) / <alpha-value>)`) {
		t.Fatalf("expected index html to drive tailwind hub colors from CSS theme variables")
	}
	if strings.Contains(markup, `id="hover-select"`) || strings.Contains(markup, ">Hover<") {
		t.Fatalf("expected index html to remove the docked hover selector")
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
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":true,"configuredHarness":"","configuredAgentLabel":"Codex"};`) {
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
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":false,"configuredHarness":"claude","configuredAgentLabel":"Claude"};`) {
		t.Fatalf("expected configured harness UI config, got %q", markup)
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
	if !strings.Contains(css, ".panel-header,\n.task-head {\n  display: flex;\n  justify-content: space-between;\n  align-items: center;\n  gap: 8px;\n  padding: 13px 16px;\n  border-bottom: 1px solid var(--surface-header-border);\n  background: var(--surface-header);\n  color: var(--surface-label);") {
		t.Fatalf("expected stylesheet to style task and output headers with theme-aware surface tokens")
	}
	if !strings.Contains(css, ".theme-controls") || !strings.Contains(css, ".theme-cycle-button") {
		t.Fatalf("expected stylesheet to include docked theme cycle styles")
	}
	if strings.Contains(css, ".theme-cycle-button::after") || strings.Contains(css, ".theme-control-label") || strings.Contains(css, ".theme-cycle-next") {
		t.Fatalf("expected stylesheet to remove the extra theme dock label, next-state text, and chevron")
	}
	if !strings.Contains(css, "--theme-button-bg:") || !strings.Contains(css, "--surface-control-bg:") {
		t.Fatalf("expected stylesheet to define reusable theme tokens for controls")
	}
	if !strings.Contains(css, "html.dark .theme-controls") || !strings.Contains(css, "html.night .theme-controls") {
		t.Fatalf("expected stylesheet to include dark and night docked theme control treatments")
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
	if !strings.Contains(css, ".task-output-toggle") {
		t.Fatalf("expected stylesheet to include task output toggle styles")
	}
	if !strings.Contains(css, ".task-terminal-toggle") {
		t.Fatalf("expected stylesheet to include terminal output toggle styles")
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
	if !strings.Contains(css, "top: max(16px, env(safe-area-inset-top));") || !strings.Contains(css, "right: max(16px, env(safe-area-inset-right));") {
		t.Fatalf("expected stylesheet to keep the full screen close control clear of viewport edges")
	}
	if !strings.Contains(css, "background: var(--surface-fullscreen-close-bg);") || !strings.Contains(css, "color: #fff;") {
		t.Fatalf("expected stylesheet to give the full screen close control high-contrast styling")
	}
	if !strings.Contains(css, "inline-size: 48px;") {
		t.Fatalf("expected stylesheet to size the full screen close control as a compact icon button")
	}
	if !strings.Contains(css, ".task-fullscreen") {
		t.Fatalf("expected stylesheet to include full screen task layout styles")
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
	if strings.Contains(css, ".task-history") {
		t.Fatalf("expected stylesheet to remove prompt history section styles")
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
	if !strings.Contains(css, ".status-item-metrics") {
		t.Fatalf("expected stylesheet to include metrics pill styles")
	}
	if !strings.Contains(css, ".dot.http") {
		t.Fatalf("expected stylesheet to include HTTP long-poll dot styles")
	}
	if !strings.Contains(css, ".dot.disconnected") {
		t.Fatalf("expected stylesheet to include disconnected dot styles")
	}
	if strings.Contains(css, "cursor:") {
		t.Fatalf("expected stylesheet to avoid custom cursor styles")
	}
	if strings.Contains(css, "cursor-not-allowed") {
		t.Fatalf("expected stylesheet to avoid cursor utility classes")
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

func TestHandlerTaskRerunAccepted(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	requestID := "req-100"
	payload := `{"repo":"git@github.com:acme/repo.git","baseBranch":"main","targetSubdir":".","prompt":"rerun this"}`
	b.RecordTaskRunConfig(requestID, []byte(payload))

	var gotBody string
	srv := NewServer("", b)
	srv.SubmitLocalPrompt = func(_ context.Context, body []byte) (string, error) {
		gotBody = string(body)
		return "local-456", nil
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
	b.IngestLog("dispatch status=ok request_id=req-close workspace=/tmp/run branch=moltenhub-close")

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
