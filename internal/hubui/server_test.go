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
			{Name: "security-review", Description: "Audit security boundaries."},
			{Name: "unit-test-coverage"},
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
	if !strings.Contains(markup, `src="https://molten.bot/logo.svg"`) {
		t.Fatalf("expected index html to include moltenhub logo")
	}
	if !strings.Contains(markup, `"task-close"`) {
		t.Fatalf("expected index html to include task close class usage")
	}
	if !strings.Contains(markup, `"task-rerun"`) {
		t.Fatalf("expected index html to include task rerun class usage")
	}
	if !strings.Contains(markup, "function dismissTask(") {
		t.Fatalf("expected index html to include dismissTask handler")
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
	if !strings.Contains(markup, "function toggleTaskOutput(") {
		t.Fatalf("expected index html to include task output toggle handler")
	}
	if !strings.Contains(markup, "function toggleTerminalOutput(") {
		t.Fatalf("expected index html to include terminal output toggle handler")
	}
	if !strings.Contains(markup, "function setTaskFullscreen(") {
		t.Fatalf("expected index html to include full screen task toggle handler")
	}
	if !strings.Contains(markup, "function fullscreenTasks(") {
		t.Fatalf("expected index html to include full screen task list renderer")
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
	if !strings.Contains(markup, `id="task-terminal-toggle"`) {
		t.Fatalf("expected index html to include terminal output open/close button")
	}
	if !strings.Contains(markup, `id="task-output-panel"`) {
		t.Fatalf("expected index html to include standard output panel wrapper")
	}
	if !strings.Contains(markup, `id="task-output-panel" class="panel log-wrap hidden`) {
		t.Fatalf("expected index html to keep standard output panel hidden by default")
	}
	if !strings.Contains(markup, `id="task-fullscreen-toggle"`) {
		t.Fatalf("expected index html to include tasks full screen toggle")
	}
	if strings.Contains(markup, "<span>Tasks</span>") {
		t.Fatalf("expected index html to remove Tasks title label from panel header")
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
	if strings.Contains(markup, `id="task-fullscreen-close"`) {
		t.Fatalf("expected index html to use the primary full screen toggle as the close control")
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
	if !strings.Contains(markup, "taskFullscreenBody.classList.toggle(\"task-output-hidden\", !outputVisible);") {
		t.Fatalf("expected index html to include full screen task-only mode when output is hidden")
	}
	if !strings.Contains(markup, "taskFullscreenToggle.textContent = state.taskFullscreenOpen ? \"X\" : \"Full Screen\";") {
		t.Fatalf("expected index html to set full screen toggle text to X when open")
	}
	if !strings.Contains(markup, "taskFullscreenToggle.classList.toggle(\"task-fullscreen-close\", state.taskFullscreenOpen);") {
		t.Fatalf("expected index html to apply close-button styling to full screen toggle when open")
	}
	if !strings.Contains(markup, "taskFullscreenToggle.setAttribute(\"aria-label\", state.taskFullscreenOpen ? \"Close full screen tasks\" : \"Open full screen tasks\");") {
		t.Fatalf("expected index html to update full screen toggle aria-label for open and close states")
	}
	if !strings.Contains(markup, "function setTaskOutputPanelVisibility(") {
		t.Fatalf("expected index html to include standard output panel visibility handler")
	}
	if !strings.Contains(markup, "rightCol.classList.toggle(\"task-output-hidden\", !outputVisible);") {
		t.Fatalf("expected index html to collapse the standard layout when output is hidden")
	}
	if !strings.Contains(markup, "setTerminalOutputOpen(task.request_id, nextOpen);") {
		t.Fatalf("expected index html to open full terminal output from task Open Output action")
	}
	if strings.Contains(markup, "Output hidden. Use Open Output to preview.") {
		t.Fatalf("expected index html to remove collapsed task output placeholder copy")
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
	if !strings.Contains(markup, `id="prompt-visibility-toggle"`) {
		t.Fatalf("expected index html to include studio visibility toggle")
	}
	if !strings.Contains(markup, `aria-label="Minimize Studio panel"`) || !strings.Contains(markup, `title="Minimize Studio panel">▾</button>`) {
		t.Fatalf("expected index html to initialize the studio toggle as an arrow minimize control")
	}
	if !strings.Contains(markup, ">Studio<") {
		t.Fatalf("expected index html to label the prompt panel as Studio")
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
	if !strings.Contains(markup, `id="prompt-mode-builder"`) {
		t.Fatalf("expected index html to include builder mode toggle")
	}
	if !strings.Contains(markup, `id="prompt-mode-library"`) {
		t.Fatalf("expected index html to include library mode toggle")
	}
	if !strings.Contains(markup, `id="prompt-mode-json"`) {
		t.Fatalf("expected index html to include json mode toggle")
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
	if !strings.Contains(markup, `class="prompt-grid"`) ||
		!strings.Contains(markup, `id="builder-repo-history-field" class="prompt-field prompt-field-repo-history"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-repository"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-base-branch"`) ||
		!strings.Contains(markup, `class="prompt-field prompt-field-target-subdir"`) {
		t.Fatalf("expected index html to include the builder row with explicit field layout classes")
	}
	if !strings.Contains(markup, "function syncBaseBranchClearState(") || !strings.Contains(markup, "builderBaseBranchClear.addEventListener(\"click\", resetBaseBranchToMain);") {
		t.Fatalf("expected index html to include branch clear behavior")
	}
	if !strings.Contains(markup, "function resetBuilderTargetSubdir(") || !strings.Contains(markup, "builderTargetSubdir.value = \".\";") {
		t.Fatalf("expected index html to include target subdir reset behavior")
	}
	if !strings.Contains(markup, "function clearBuilderPromptDraft(") {
		t.Fatalf("expected index html to include prompt clear handler")
	}
	if !strings.Contains(markup, "builderImagesClear.addEventListener(\"click\", clearBuilderPromptDraft);") {
		t.Fatalf("expected index html Clear button to reset the full builder draft")
	}
	if !strings.Contains(markup, `historyField.classList.toggle("hidden", !hasSavedHistory);`) {
		t.Fatalf("expected index html to hide repo history when there are no saved repos")
	}
	if !strings.Contains(markup, "function rememberRepos(") {
		t.Fatalf("expected index html to include repo history persistence")
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
	if !strings.Contains(markup, "clearSubmittedPromptState();") {
		t.Fatalf("expected index html to clear the submitted prompt state after a successful queue")
	}
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":false};`) {
		t.Fatalf("expected index html to include default UI config")
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
	if !strings.Contains(markup, `window.__HUB_UI_CONFIG__ = {"automaticMode":true};`) {
		t.Fatalf("expected automatic mode UI config, got %q", markup)
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
	if !strings.Contains(css, ".task-fullscreen-close") {
		t.Fatalf("expected stylesheet to include full screen close-state button styles")
	}
	if !strings.Contains(css, "body.task-fullscreen-open #task-fullscreen-toggle") {
		t.Fatalf("expected stylesheet to pin the full screen toggle in the viewport while full screen is open")
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
	if !strings.Contains(css, ".task-fullscreen-body.task-output-hidden") {
		t.Fatalf("expected stylesheet to include full screen hidden-output task layout styles")
	}
	if !strings.Contains(css, ".right-col.task-output-hidden") {
		t.Fatalf("expected stylesheet to include standard hidden-output task layout styles")
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
	if !strings.Contains(css, ".prompt-mode-tab") {
		t.Fatalf("expected stylesheet to include prompt mode tab styles")
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
