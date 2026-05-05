package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/failurefollowup"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
)

const (
	defaultMaxEvents           = 600
	defaultMaxTaskLogs         = 2000
	defaultClosedTaskRetention = 24 * time.Hour
)

var (
	ErrTaskNotFound     = errors.New("task not found")
	ErrTaskNotCompleted = errors.New("task is not completed")
)

const (
	hubTransportWS           = "ws"
	hubTransportHTTPLongPoll = "http_long_poll"
	hubTransportDisconnected = "disconnected"
	hubTransportReachable    = "reachable"
	hubTransportRetrying     = "retrying"
)

// Event is one monitor timeline entry.
type Event struct {
	ID        int64  `json:"id"`
	Time      string `json:"time"`
	Kind      string `json:"kind"`
	RequestID string `json:"request_id,omitempty"`
	Line      string `json:"line"`
}

// TaskLog is one terminal/log line associated with a request.
type TaskLog struct {
	Time   string `json:"time"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// TaskControls describes which runtime controls are currently supported.
type TaskControls struct {
	Pause    bool `json:"pause,omitempty"`
	Run      bool `json:"run,omitempty"`
	ForceRun bool `json:"force_run,omitempty"`
	Stop     bool `json:"stop,omitempty"`
}

// Task represents one hub dispatch execution state.
type Task struct {
	RequestID         string       `json:"request_id"`
	Prompt            string       `json:"prompt,omitempty"`
	PromptIsUserInput bool         `json:"prompt_is_user_input"`
	Skill             string       `json:"skill,omitempty"`
	Repo              string       `json:"repo,omitempty"`
	Repos             []string     `json:"repos,omitempty"`
	BaseBranch        string       `json:"base_branch,omitempty"`
	Status            string       `json:"status"`
	Stage             string       `json:"stage,omitempty"`
	StageStatus       string       `json:"stage_status,omitempty"`
	ExitCode          int          `json:"exit_code,omitempty"`
	WorkspaceDir      string       `json:"workspace_dir,omitempty"`
	Branch            string       `json:"branch,omitempty"`
	PRURL             string       `json:"pr_url,omitempty"`
	Error             string       `json:"error,omitempty"`
	StartedAt         string       `json:"started_at"`
	UpdatedAt         string       `json:"updated_at"`
	CanRerun          bool         `json:"can_rerun,omitempty"`
	Controls          TaskControls `json:"controls,omitempty"`
	Logs              []TaskLog    `json:"logs"`
}

// TaskAttempt is an internal record of queued/running terminal attempts for one original task.
// It is intentionally not included in Snapshot responses.
type TaskAttempt struct {
	RequestID string
	RerunOf   string
	Status    string
	Error     string
	StartedAt string
	UpdatedAt string
}

// Connection captures current monitor connectivity state.
type Connection struct {
	HubConnected bool   `json:"hub_connected"`
	HubTransport string `json:"hub_transport,omitempty"`
	HubDomain    string `json:"hub_domain,omitempty"`
	HubBaseURL   string `json:"hub_base_url,omitempty"`
	HubDetail    string `json:"hub_detail,omitempty"`
}

// ResourceMetrics captures the current dispatcher sample window values.
type ResourceMetrics struct {
	CPUPercent    float64 `json:"cpu_percent,omitempty"`
	MemoryPercent float64 `json:"memory_percent,omitempty"`
	DiskIOMBs     float64 `json:"disk_io_mb_s,omitempty"`
	UpdatedAt     string  `json:"updated_at,omitempty"`
}

// Snapshot is the complete monitor payload for the web UI.
type Snapshot struct {
	GeneratedAt string          `json:"generated_at"`
	Connection  Connection      `json:"connection"`
	Resources   ResourceMetrics `json:"resources"`
	Events      []Event         `json:"events"`
	Tasks       []Task          `json:"tasks"`
}

// Broker collects daemon logs and exposes monitor state snapshots.
type Broker struct {
	mu sync.RWMutex

	now        func() time.Time
	maxEvents  int
	maxTaskLog int

	nextEventID  int64
	events       []Event
	tasks        map[string]*taskState
	closedTasks  map[string]time.Time
	runConfigs   map[string][]byte
	attempts     map[string][]taskAttemptState
	attemptRoots map[string]string
	rejectedSeq  uint64
	subs         map[chan struct{}]struct{}

	hubConnected bool
	hubTransport string
	hubBaseURL   string
	hubDomain    string
	hubDetail    string
	resources    ResourceMetrics
}

type taskState struct {
	RequestID         string
	Prompt            string
	PromptIsUserInput bool
	Skill             string
	Repo              string
	Repos             []string
	BaseBranch        string
	Status            string
	Stage             string
	StageStatus       string
	ExitCode          int
	WorkspaceDir      string
	Branch            string
	PRURL             string
	Error             string
	StartedAt         time.Time
	UpdatedAt         time.Time
	Logs              []TaskLog
}

type taskAttemptState struct {
	RequestID string
	RerunOf   string
	Status    string
	Error     string
	StartedAt time.Time
	UpdatedAt time.Time
}

// NewBroker returns a monitor state broker with safe defaults.
func NewBroker() *Broker {
	return &Broker{
		now:          time.Now,
		maxEvents:    defaultMaxEvents,
		maxTaskLog:   defaultMaxTaskLogs,
		tasks:        map[string]*taskState{},
		closedTasks:  map[string]time.Time{},
		runConfigs:   map[string][]byte{},
		attempts:     map[string][]taskAttemptState{},
		attemptRoots: map[string]string{},
		subs:         map[chan struct{}]struct{}{},
	}
}

// IngestLog consumes one daemon log line and updates monitor state.
func (b *Broker) IngestLog(line string) {
	if b == nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	now := b.now().UTC()
	fields := parseKVFields(line)
	if shouldDropNoisyCommandLine(line, fields) {
		return
	}
	requestID := fields["request_id"]

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)

	b.nextEventID++
	b.events = appendCappedEvent(b.events, b.maxEvents, Event{
		ID:        b.nextEventID,
		Time:      now.Format(time.RFC3339Nano),
		Kind:      classifyEventKind(line),
		RequestID: requestID,
		Line:      line,
	})

	if requestID != "" &&
		!isDuplicateTaskDispatchLine(fields) &&
		!b.isClosedTaskLocked(requestID, now) {
		t := b.ensureTaskLocked(requestID, now)
		b.updateTaskFromLineLocked(t, line, fields, now)
	}
	b.updateHubConnectionFromLineLocked(line, fields)
	b.updateResourceMetricsFromLineLocked(line, fields, now)

	b.notifySubscribersLocked()
}

// Snapshot returns a deep copy of current monitor state.
func (b *Broker) Snapshot() Snapshot {
	if b == nil {
		return Snapshot{}
	}

	now := b.now().UTC()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)

	snapshot := Snapshot{
		GeneratedAt: now.Format(time.RFC3339Nano),
		Connection: Connection{
			HubConnected: b.hubConnected,
			HubTransport: b.hubTransport,
			HubDomain:    b.hubDomain,
			HubBaseURL:   b.hubBaseURL,
			HubDetail:    strings.TrimSpace(b.hubDetail),
		},
		Resources: b.resources,
		Events:    append([]Event(nil), b.events...),
	}

	tasks := make([]*taskState, 0, len(b.tasks))
	for _, t := range b.tasks {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].RequestID > tasks[j].RequestID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})

	snapshot.Tasks = make([]Task, 0, len(tasks))
	for _, t := range tasks {
		_, canRerun := b.runConfigs[t.RequestID]
		snapshot.Tasks = append(snapshot.Tasks, Task{
			RequestID:         t.RequestID,
			Prompt:            t.Prompt,
			PromptIsUserInput: t.PromptIsUserInput,
			Skill:             t.Skill,
			Repo:              t.Repo,
			Repos:             append([]string(nil), t.Repos...),
			BaseBranch:        t.BaseBranch,
			Status:            normalizeTaskTerminalStatus(t.Status),
			Stage:             t.Stage,
			StageStatus:       t.StageStatus,
			ExitCode:          t.ExitCode,
			WorkspaceDir:      t.WorkspaceDir,
			Branch:            t.Branch,
			PRURL:             t.PRURL,
			Error:             t.Error,
			StartedAt:         t.StartedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339Nano),
			CanRerun:          canRerun,
			Logs:              append([]TaskLog(nil), t.Logs...),
		})
	}

	return snapshot
}

// Task returns a copy of the current task snapshot for one request id.
func (b *Broker) Task(requestID string) (Task, bool) {
	if b == nil {
		return Task{}, false
	}

	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return Task{}, false
	}

	now := b.now().UTC()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)

	t, ok := b.tasks[requestID]
	if !ok || t == nil {
		return Task{}, false
	}
	_, canRerun := b.runConfigs[requestID]
	return Task{
		RequestID:         t.RequestID,
		Prompt:            t.Prompt,
		PromptIsUserInput: t.PromptIsUserInput,
		Skill:             t.Skill,
		Repo:              t.Repo,
		Repos:             append([]string(nil), t.Repos...),
		BaseBranch:        t.BaseBranch,
		Status:            normalizeTaskTerminalStatus(t.Status),
		Stage:             t.Stage,
		StageStatus:       t.StageStatus,
		ExitCode:          t.ExitCode,
		WorkspaceDir:      t.WorkspaceDir,
		Branch:            t.Branch,
		PRURL:             t.PRURL,
		Error:             t.Error,
		StartedAt:         t.StartedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		CanRerun:          canRerun,
		Logs:              append([]TaskLog(nil), t.Logs...),
	}, true
}

// RecordTaskRunConfig stores a parsed task run config payload for future reruns.
func (b *Broker) RecordTaskRunConfig(requestID string, runConfigJSON []byte) {
	if b == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	runConfigJSON = bytes.TrimSpace(runConfigJSON)
	if requestID == "" || len(runConfigJSON) == 0 {
		return
	}
	cfgCopy := append([]byte(nil), runConfigJSON...)
	prompt := promptFromRunConfigJSON(cfgCopy)
	baseBranch := branchFromRunConfigJSON(cfgCopy)
	repos := reposFromRunConfigJSON(cfgCopy)
	now := b.now().UTC()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)

	changed := false
	if existing, ok := b.runConfigs[requestID]; !ok || !bytes.Equal(existing, cfgCopy) {
		b.runConfigs[requestID] = cfgCopy
		changed = true
	}
	t, taskExists := b.tasks[requestID]
	if !taskExists && !b.isClosedTaskLocked(requestID, now) {
		t = &taskState{
			RequestID:         requestID,
			Prompt:            prompt,
			PromptIsUserInput: promptIsUserInputForTask(requestID, prompt),
			Repo:              firstRepo(repos),
			Repos:             append([]string(nil), repos...),
			BaseBranch:        baseBranch,
			Branch:            baseBranch,
			Status:            "pending",
			Stage:             "dispatch",
			StageStatus:       "queued",
			StartedAt:         now,
			UpdatedAt:         now,
		}
		b.tasks[requestID] = t
		b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, now)
		changed = true
	}
	if prompt != "" {
		if t != nil {
			promptIsUserInput := promptIsUserInputForTask(requestID, prompt)
			if t.Prompt != prompt {
				t.Prompt = prompt
				changed = true
			}
			if t.PromptIsUserInput != promptIsUserInput {
				t.PromptIsUserInput = promptIsUserInput
				changed = true
			}
		}
	}
	if len(repos) > 0 && t != nil {
		if !sameStringSlice(t.Repos, repos) {
			t.Repos = append([]string(nil), repos...)
			changed = true
		}
		if repo := firstRepo(repos); repo != "" && t.Repo != repo {
			t.Repo = repo
			changed = true
		}
	}
	if baseBranch != "" {
		if t != nil {
			if t.BaseBranch != baseBranch {
				t.BaseBranch = baseBranch
				changed = true
			}
			if t.Branch == "" {
				t.Branch = baseBranch
				changed = true
			}
		}
	}
	if changed {
		if t != nil {
			t.UpdatedAt = now
		}
		b.notifySubscribersLocked()
	}
}

// RecordRejectedPromptSubmission stores a failed prompt submission so it remains visible in the task list.
func (b *Broker) RecordRejectedPromptSubmission(runConfigJSON []byte, status string, err error) string {
	if b == nil {
		return ""
	}

	runConfigJSON = bytes.TrimSpace(runConfigJSON)
	status = strings.TrimSpace(status)
	if status == "" {
		status = "invalid"
	}

	now := b.now().UTC()
	repos := reposFromRunConfigJSON(runConfigJSON)
	baseBranch := branchFromRunConfigJSON(runConfigJSON)
	prompt := promptFromRunConfigJSON(runConfigJSON)
	errText := strings.TrimSpace(errorText(err))
	if errText == "" {
		errText = "prompt submission failed"
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)
	b.rejectedSeq++
	requestID := fmt.Sprintf("local-rejected-%d-%06d", now.Unix(), b.rejectedSeq)
	t := &taskState{
		RequestID:         requestID,
		Prompt:            prompt,
		PromptIsUserInput: promptIsUserInputForTask(requestID, prompt),
		Repo:              firstRepo(repos),
		Repos:             append([]string(nil), repos...),
		BaseBranch:        baseBranch,
		Status:            status,
		Branch:            baseBranch,
		Error:             errText,
		StartedAt:         now,
		UpdatedAt:         now,
		Stage:             "submit",
		StageStatus:       status,
		Logs: []TaskLog{
			{
				Time:   now.Format(time.RFC3339Nano),
				Stream: "meta",
				Text:   "prompt submission failed: " + errText,
			},
		},
	}
	b.tasks[requestID] = t
	b.notifySubscribersLocked()
	return requestID
}

func (b *Broker) taskBaseBranchLocked(requestID string) string {
	if b == nil {
		return ""
	}
	return branchFromRunConfigJSON(b.runConfigs[requestID])
}

func (b *Broker) taskInitialBranchLocked(requestID string) string {
	baseBranch := b.taskBaseBranchLocked(requestID)
	if baseBranch != "" {
		return baseBranch
	}
	return ""
}

func (b *Broker) taskPromptLocked(requestID string) string {
	if b == nil {
		return ""
	}
	return promptFromRunConfigJSON(b.runConfigs[requestID])
}

// TaskRunConfig returns a copy of the stored run config payload for requestID.
func (b *Broker) TaskRunConfig(requestID string) ([]byte, bool) {
	if b == nil {
		return nil, false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, false
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	runConfigJSON, ok := b.runConfigs[requestID]
	if !ok || len(runConfigJSON) == 0 {
		return nil, false
	}
	return append([]byte(nil), runConfigJSON...), true
}

// DropTaskRunConfig removes stored rerun config for tasks that no longer need task-level reruns.
func (b *Broker) DropTaskRunConfig(requestID string) {
	if b == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.runConfigs, requestID)
}

// TaskAttempts returns a copy of internal attempt records for requestID's root task.
func (b *Broker) TaskAttempts(requestID string) []TaskAttempt {
	if b == nil {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	root := strings.TrimSpace(b.attemptRoots[requestID])
	if root == "" {
		root = requestID
	}
	attempts := b.attempts[root]
	out := make([]TaskAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, TaskAttempt{
			RequestID: attempt.RequestID,
			RerunOf:   attempt.RerunOf,
			Status:    attempt.Status,
			Error:     attempt.Error,
			StartedAt: attempt.StartedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: attempt.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

// RecordTaskRerunAttempt links a newly queued rerun to the original task attempt chain.
func (b *Broker) RecordTaskRerunAttempt(rerunOf, requestID string) {
	if b == nil {
		return
	}
	rerunOf = strings.TrimSpace(rerunOf)
	requestID = strings.TrimSpace(requestID)
	if rerunOf == "" || requestID == "" {
		return
	}

	now := b.now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()

	root := b.taskAttemptRootLocked(rerunOf)
	if root == "" {
		root = rerunOf
	}
	b.recordTaskAttemptLocked(root, requestID, rerunOf, "queued", "", now)
	b.notifySubscribersLocked()
}

// CloseTask removes a terminal task from the live task list while retaining its rerun config.
func (b *Broker) CloseTask(requestID string) error {
	if b == nil {
		return ErrTaskNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ErrTaskNotFound
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	task, ok := b.tasks[requestID]
	if !ok {
		return ErrTaskNotFound
	}
	if !isCompletedTaskStatus(task.Status) {
		return ErrTaskNotCompleted
	}

	delete(b.tasks, requestID)
	b.closedTasks[requestID] = b.now().UTC()
	b.notifySubscribersLocked()
	return nil
}

func (b *Broker) pruneExpiredTasksLocked(now time.Time) {
	for requestID, closedAt := range b.closedTasks {
		if now.Sub(closedAt) < defaultClosedTaskRetention {
			continue
		}
		delete(b.closedTasks, requestID)
		delete(b.runConfigs, requestID)
	}
}

func (b *Broker) isClosedTaskLocked(requestID string, now time.Time) bool {
	closedAt, ok := b.closedTasks[requestID]
	if !ok {
		return false
	}
	if now.Sub(closedAt) >= defaultClosedTaskRetention {
		delete(b.closedTasks, requestID)
		delete(b.runConfigs, requestID)
		return false
	}
	return true
}

// Subscribe returns a change notification channel and cancel function.
func (b *Broker) Subscribe() (<-chan struct{}, func()) {
	if b == nil {
		ch := make(chan struct{})
		close(ch)
		return ch, func() {}
	}

	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}

	return ch, cancel
}

func (b *Broker) ensureTaskLocked(requestID string, now time.Time) *taskState {
	if existing, ok := b.tasks[requestID]; ok {
		if existing.Prompt == "" {
			existing.Prompt = b.taskPromptLocked(requestID)
			existing.PromptIsUserInput = promptIsUserInputForTask(requestID, existing.Prompt)
		}
		if existing.BaseBranch == "" {
			existing.BaseBranch = b.taskBaseBranchLocked(requestID)
		}
		if existing.Branch == "" {
			existing.Branch = b.taskInitialBranchLocked(requestID)
		}
		existing.UpdatedAt = now
		b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", existing.Status, existing.Error, now)
		return existing
	}

	prompt := b.taskPromptLocked(requestID)
	t := &taskState{
		RequestID:         requestID,
		Prompt:            prompt,
		PromptIsUserInput: promptIsUserInputForTask(requestID, prompt),
		BaseBranch:        b.taskBaseBranchLocked(requestID),
		Branch:            b.taskInitialBranchLocked(requestID),
		Status:            "pending",
		StartedAt:         now,
		UpdatedAt:         now,
	}
	b.tasks[requestID] = t
	b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, now)
	return t
}

func (b *Broker) updateTaskFromLineLocked(t *taskState, line string, fields map[string]string, now time.Time) {
	defer func() {
		if t != nil {
			b.recordTaskAttemptLocked(b.taskAttemptRootLocked(t.RequestID), t.RequestID, "", t.Status, t.Error, now)
		}
	}()

	t.UpdatedAt = now

	if strings.HasPrefix(line, "dispatch status=start") {
		t.Status = "running"
		t.Skill = firstNonEmpty(t.Skill, fields["skill"])
		t.Repos = appendNonEmptyUnique(t.Repos, reposFromFields(fields)...)
		if len(t.Repos) > 0 {
			t.Repo = t.Repos[0]
		} else {
			t.Repo = firstNonEmpty(t.Repo, fields["repo"])
		}
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=completed") {
		t.Status = "completed"
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=ok") {
		t.Status = "completed"
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=no_changes") {
		t.Status = "no_changes"
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
	}

	if strings.HasPrefix(line, "dispatch status=paused") {
		t.Status = "paused"
		t.Stage = firstNonEmpty(t.Stage, "dispatch")
		t.StageStatus = firstNonEmpty(fields["status"], "paused")
	}

	if strings.HasPrefix(line, "dispatch status=resumed") {
		if t.Status == "" || t.Status == "paused" || t.Status == "pending" {
			t.Status = "pending"
		}
		t.Stage = firstNonEmpty(t.Stage, "dispatch")
		t.StageStatus = firstNonEmpty(fields["status"], "resumed")
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=stopped") {
		t.Status = "stopped"
		if code, ok := parseIntField(fields["exit_code"]); ok {
			t.ExitCode = code
		}
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
		if strings.TrimSpace(t.Error) == "" {
			t.Error = "task stopped by operator"
		}
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=error") {
		t.Status = "error"
		if code, ok := parseIntField(fields["exit_code"]); ok {
			t.ExitCode = code
		}
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=invalid") {
		t.Status = "invalid"
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
	}

	if isFinalTaskDispatchLine(fields) && strings.HasPrefix(line, "dispatch status=duplicate") {
		if t.Status == "" || t.Status == "pending" || t.Status == "duplicate" {
			t.Status = "duplicate"
			t.Stage = firstNonEmpty(t.Stage, "dispatch")
			t.StageStatus = firstNonEmpty(fields["state"], t.StageStatus)
			t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
			if t.Error == "" {
				var details []string
				if state := strings.TrimSpace(fields["state"]); state != "" {
					details = append(details, "state="+state)
				}
				if duplicateOf := strings.TrimSpace(fields["duplicate_of"]); duplicateOf != "" {
					details = append(details, "duplicate_of="+duplicateOf)
				}
				if len(details) == 0 {
					t.Error = "duplicate submission ignored"
				} else {
					t.Error = "duplicate submission ignored (" + strings.Join(details, ", ") + ")"
				}
			}
		}
	}

	if strings.HasPrefix(line, "dispatch request_id=") {
		if stage := fields["stage"]; stage != "" {
			t.Stage = stage
		}
		if stageStatus := fields["status"]; stageStatus != "" {
			t.StageStatus = stageStatus
			if stageStatus == "error" && t.Status == "running" {
				t.Status = "error"
			}
		}
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
	}

	if strings.Contains(line, " cmd ") {
		text := strings.TrimSpace(fields["text"])
		if text == "" {
			encoded := strings.TrimSpace(fields["b64"])
			if encoded == "" {
				return
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return
			}
			text = strings.TrimSpace(string(decoded))
		}
		if text == "" {
			return
		}
		b.appendTaskLogLocked(t, TaskLog{
			Time:   now.Format(time.RFC3339Nano),
			Stream: firstNonEmpty(fields["stream"], "stdout"),
			Text:   text,
		})
		return
	}

	if strings.HasPrefix(line, "dispatch ") {
		b.appendTaskLogLocked(t, TaskLog{
			Time:   now.Format(time.RFC3339Nano),
			Stream: "meta",
			Text:   line,
		})
	}
}

func isDuplicateTaskDispatchLine(fields map[string]string) bool {
	if fields == nil {
		return false
	}
	return strings.TrimSpace(fields["status"]) == "duplicate"
}

func (b *Broker) appendTaskLogLocked(t *taskState, line TaskLog) {
	t.Logs = appendCappedTaskLog(t.Logs, b.maxTaskLog, line)
}

func (b *Broker) taskAttemptRootLocked(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ""
	}
	if b.attemptRoots == nil {
		b.attemptRoots = map[string]string{}
	}
	if root := strings.TrimSpace(b.attemptRoots[requestID]); root != "" {
		return root
	}
	b.attemptRoots[requestID] = requestID
	return requestID
}

func (b *Broker) recordTaskAttemptLocked(root, requestID, rerunOf, status, errText string, now time.Time) {
	root = strings.TrimSpace(root)
	requestID = strings.TrimSpace(requestID)
	if root == "" {
		root = requestID
	}
	if root == "" || requestID == "" {
		return
	}
	if b.attempts == nil {
		b.attempts = map[string][]taskAttemptState{}
	}
	if b.attemptRoots == nil {
		b.attemptRoots = map[string]string{}
	}
	b.attemptRoots[requestID] = root
	status = normalizeTaskTerminalStatus(status)
	if status == "" {
		status = "pending"
	}
	errText = strings.TrimSpace(errText)
	rerunOf = strings.TrimSpace(rerunOf)

	attempts := b.attempts[root]
	for i := range attempts {
		if attempts[i].RequestID != requestID {
			continue
		}
		if rerunOf != "" {
			attempts[i].RerunOf = rerunOf
		}
		attempts[i].Status = status
		attempts[i].Error = errText
		attempts[i].UpdatedAt = now
		b.attempts[root] = attempts
		return
	}
	b.attempts[root] = append(attempts, taskAttemptState{
		RequestID: requestID,
		RerunOf:   rerunOf,
		Status:    status,
		Error:     errText,
		StartedAt: now,
		UpdatedAt: now,
	})
}

func shouldDropNoisyCommandLine(line string, fields map[string]string) bool {
	if !strings.Contains(line, " cmd ") {
		return false
	}
	if strings.TrimSpace(fields["text"]) != "" {
		return false
	}
	encoded := strings.TrimSpace(fields["b64"])
	if encoded == "" {
		return true
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(decoded)) == ""
}

func (b *Broker) updateHubConnectionFromLineLocked(line string, fields map[string]string) {
	if baseURL := strings.TrimSpace(fields["base_url"]); baseURL != "" {
		b.hubBaseURL = baseURL
		if domain := hubDomainFromBaseURL(baseURL); domain != "" {
			b.hubDomain = domain
		}
	}
	if domain := strings.TrimSpace(firstNonEmpty(fields["domain"], fields["hub_domain"])); domain != "" {
		b.hubDomain = domain
	}

	switch {
	case strings.HasPrefix(line, "hub.auth status=ok"):
		b.hubConnected = true
		b.hubDetail = ""
		if b.hubTransport == hubTransportDisconnected || b.hubTransport == hubTransportRetrying {
			b.hubTransport = ""
		}
	case strings.HasPrefix(line, "hub.ws status=connected"):
		b.hubConnected = true
		b.hubTransport = hubTransportWS
		b.hubDetail = ""
	case strings.HasPrefix(line, "hub.transport mode=runtime_pull"):
		// Pull mode still means the daemon is connected to MoltenHub transport.
		b.hubConnected = true
		b.hubTransport = hubTransportHTTPLongPoll
		b.hubDetail = ""
	case strings.HasPrefix(line, "hub.transport mode=runtime_ws"):
		b.hubConnected = true
		b.hubTransport = hubTransportWS
		b.hubDetail = ""
	case strings.HasPrefix(line, "hub.connection "):
		switch strings.ToLower(strings.TrimSpace(fields["status"])) {
		case "connected", "online", "ok":
			b.hubConnected = true
			b.hubDetail = strings.TrimSpace(firstNonEmpty(fields["detail"], fields["err"]))
			if b.hubTransport == hubTransportDisconnected || b.hubTransport == hubTransportRetrying {
				b.hubTransport = ""
			}
		case "reachable":
			b.hubConnected = false
			b.hubTransport = hubTransportReachable
			b.hubDetail = strings.TrimSpace(firstNonEmpty(fields["detail"], fields["err"]))
		case "retrying":
			b.hubConnected = false
			b.hubTransport = hubTransportRetrying
			b.hubDetail = strings.TrimSpace(firstNonEmpty(fields["detail"], fields["err"]))
		case "disconnected", "offline", "error":
			b.hubConnected = false
			b.hubTransport = hubTransportDisconnected
			b.hubDetail = strings.TrimSpace(firstNonEmpty(fields["detail"], fields["err"]))
		}
	case strings.HasPrefix(line, "hub.ws status=disabled"),
		strings.HasPrefix(line, "hub.ws status=error"),
		strings.HasPrefix(line, "hub.ws status=disconnected"),
		strings.HasPrefix(line, "hub.pull status=error"),
		strings.HasPrefix(line, "hub.agent status=offline"):
		b.hubConnected = false
		b.hubTransport = hubTransportDisconnected
		b.hubDetail = strings.TrimSpace(firstNonEmpty(fields["detail"], fields["err"]))
	}
}

func (b *Broker) updateResourceMetricsFromLineLocked(line string, fields map[string]string, now time.Time) {
	if !strings.Contains(line, "dispatcher status=window") {
		return
	}

	cpu, okCPU := parseFloatField(fields["cpu"])
	mem, okMem := parseFloatField(fields["memory"])
	disk, okDisk := parseFloatField(fields["disk_io_mb_s"])
	if !okCPU && !okMem && !okDisk {
		return
	}

	if okCPU {
		b.resources.CPUPercent = cpu
	}
	if okMem {
		b.resources.MemoryPercent = mem
	}
	if okDisk {
		b.resources.DiskIOMBs = disk
	}
	b.resources.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
}

func (b *Broker) notifySubscribersLocked() {
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func classifyEventKind(line string) string {
	switch {
	case strings.HasPrefix(line, "dispatch status="):
		return "dispatch_status"
	case strings.Contains(line, " cmd "):
		return "command_output"
	case strings.HasPrefix(line, "hub."):
		return "hub"
	default:
		return "log"
	}
}

func parseKVFields(line string) map[string]string {
	if !strings.Contains(line, "=") {
		return nil
	}
	out := make(map[string]string, 8)
	for idx := 0; idx < len(line); {
		for idx < len(line) && isKVSpace(line[idx]) {
			idx++
		}
		if idx >= len(line) {
			break
		}

		keyStart := idx
		for idx < len(line) && !isKVSpace(line[idx]) && line[idx] != '=' {
			idx++
		}
		if idx >= len(line) || line[idx] != '=' {
			for idx < len(line) && !isKVSpace(line[idx]) {
				idx++
			}
			continue
		}

		key := strings.TrimSpace(line[keyStart:idx])
		idx++
		if key == "" {
			continue
		}

		value, next := parseKVValue(line, idx)
		out[key] = value
		idx = next
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseKVValue(line string, idx int) (string, int) {
	if idx >= len(line) {
		return "", idx
	}

	if line[idx] == '"' {
		if token, ok := parseQuotedToken(line[idx:]); ok {
			if decoded, err := strconv.Unquote(token); err == nil {
				return strings.TrimSpace(decoded), idx + len(token)
			}
			return strings.TrimSpace(strings.Trim(token, `"`)), idx + len(token)
		}
	}

	start := idx
	for idx < len(line) && !isKVSpace(line[idx]) {
		idx++
	}
	return strings.TrimSpace(line[start:idx]), idx
}

func isKVSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func appendCappedEvent(events []Event, max int, entry Event) []Event {
	if max <= 0 {
		return events
	}
	if len(events) < max {
		return append(events, entry)
	}
	copy(events, events[1:])
	events[len(events)-1] = entry
	return events
}

func appendCappedTaskLog(logs []TaskLog, max int, entry TaskLog) []TaskLog {
	if max <= 0 {
		return logs
	}
	if len(logs) < max {
		return append(logs, entry)
	}
	copy(logs, logs[1:])
	logs[len(logs)-1] = entry
	return logs
}

func parseFieldValue(line, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	fields := parseKVFields(line)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(fields[key])
}

func parseQuotedToken(text string) (string, bool) {
	if !strings.HasPrefix(text, "\"") {
		return "", false
	}

	for i := 1; i < len(text); i++ {
		if text[i] != '"' {
			continue
		}

		backslashes := 0
		for j := i - 1; j >= 0 && text[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return text[:i+1], true
		}
	}

	return "", false
}

func parseIntField(v string) (int, bool) {
	if strings.TrimSpace(v) == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseFloatField(v string) (float64, bool) {
	if strings.TrimSpace(v) == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func hubDomainFromBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Hostname())
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func reposFromFields(fields map[string]string) []string {
	primary := strings.TrimSpace(fields["repo"])
	list := splitCommaSeparatedNonEmpty(fields["repos"])
	merged := make([]string, 0, len(list)+1)
	if primary != "" {
		merged = append(merged, primary)
	}
	merged = append(merged, list...)
	return appendNonEmptyUnique(nil, merged...)
}

func taskPRURLFromFields(fields map[string]string) string {
	if fields == nil {
		return ""
	}
	prURL := strings.TrimSpace(fields["pr_url"])
	if prURL != "" {
		return prURL
	}
	prURLs := splitCommaSeparatedNonEmpty(fields["pr_urls"])
	if len(prURLs) > 0 {
		return prURLs[0]
	}
	return ""
}

func splitCommaSeparatedNonEmpty(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func appendNonEmptyUnique(dst []string, values ...string) []string {
	out := make([]string, 0, len(dst)+len(values))
	seen := make(map[string]struct{}, len(dst)+len(values))

	for _, current := range dst {
		trimmed := strings.TrimSpace(current)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}

	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}

	return out
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func promptFromRunConfigJSON(runConfigJSON []byte) string {
	if len(runConfigJSON) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return ""
	}
	stringAt := func(keys ...string) string {
		for _, key := range keys {
			value, ok := raw[key].(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
		return ""
	}
	taskName := stringAt("librarytaskname", "libraryTaskName")
	if taskName != "" {
		catalog, err := library.LoadCatalog(library.DefaultDir)
		if err == nil {
			for _, task := range catalog.Summaries() {
				if strings.TrimSpace(task.Name) != taskName {
					continue
				}
				return libraryTaskDisplayText(task)
			}
		}
	}
	if prompt := stringAt("prompt"); prompt != "" {
		return prompt
	}
	return ""
}

func libraryTaskDisplayText(task library.TaskSummary) string {
	title := strings.TrimSpace(task.DisplayName)
	if title == "" {
		title = strings.TrimSpace(task.Name)
	}
	description := strings.TrimSpace(task.Description)
	switch {
	case title != "" && description != "":
		return title + "\n\n" + description
	case title != "":
		return title
	case description != "":
		return description
	default:
		return strings.TrimSpace(task.Prompt)
	}
}

func promptIsUserInputForTask(requestID, prompt string) bool {
	requestID = strings.TrimSpace(requestID)
	prompt = strings.TrimSpace(prompt)
	if requestID != "" && strings.HasSuffix(requestID, "-failure-review") {
		return false
	}
	if prompt == "" {
		return false
	}
	return prompt != strings.TrimSpace(failurefollowup.RequiredPrompt) &&
		!strings.HasPrefix(prompt, strings.TrimSpace(failurefollowup.RequiredPrompt))
}

func reposFromRunConfigJSON(runConfigJSON []byte) []string {
	if len(runConfigJSON) == 0 {
		return nil
	}
	var raw struct {
		Repo    string   `json:"repo"`
		RepoURL string   `json:"repoUrl"`
		Repos   []string `json:"repos"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return nil
	}
	return appendNonEmptyUnique(nil, append([]string{raw.Repo, raw.RepoURL}, raw.Repos...)...)
}

func branchFromRunConfigJSON(runConfigJSON []byte) string {
	if len(runConfigJSON) == 0 {
		return ""
	}
	var raw struct {
		BaseBranch string `json:"baseBranch"`
		Branch     string `json:"branch"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return ""
	}
	return firstNonEmpty(raw.BaseBranch, raw.Branch)
}

func firstRepo(repos []string) string {
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo != "" {
			return repo
		}
	}
	return ""
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isCompletedTaskStatus(status string) bool {
	switch normalizeTaskTerminalStatus(status) {
	case "completed", "no_changes", "error", "invalid", "duplicate", "stopped":
		return true
	default:
		return false
	}
}

func isFinalTaskDispatchLine(fields map[string]string) bool {
	return strings.TrimSpace(fields["action"]) == ""
}

func normalizeTaskTerminalStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "ok":
		return "completed"
	default:
		return strings.TrimSpace(status)
	}
}
