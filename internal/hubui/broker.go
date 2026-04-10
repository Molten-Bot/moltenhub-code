package hubui

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
)

const (
	defaultMaxEvents             = 600
	defaultMaxTaskLogs           = 2000
	defaultOKTaskRetentionWindow = 5 * time.Minute
	defaultClosedTaskRetention   = 24 * time.Hour
)

var (
	ErrTaskNotFound     = errors.New("task not found")
	ErrTaskNotCompleted = errors.New("task is not completed")
)

const (
	hubTransportWS           = "ws"
	hubTransportHTTPLongPoll = "http_long_poll"
	hubTransportDisconnected = "disconnected"
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

// Task represents one hub dispatch execution state.
type Task struct {
	RequestID    string    `json:"request_id"`
	Prompt       string    `json:"prompt,omitempty"`
	Skill        string    `json:"skill,omitempty"`
	Repo         string    `json:"repo,omitempty"`
	Repos        []string  `json:"repos,omitempty"`
	BaseBranch   string    `json:"base_branch,omitempty"`
	Status       string    `json:"status"`
	Stage        string    `json:"stage,omitempty"`
	StageStatus  string    `json:"stage_status,omitempty"`
	ExitCode     int       `json:"exit_code,omitempty"`
	WorkspaceDir string    `json:"workspace_dir,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	PRURL        string    `json:"pr_url,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    string    `json:"started_at"`
	UpdatedAt    string    `json:"updated_at"`
	CanRerun     bool      `json:"can_rerun,omitempty"`
	Logs         []TaskLog `json:"logs"`
}

// Connection captures current monitor connectivity state.
type Connection struct {
	HubConnected bool   `json:"hub_connected"`
	HubTransport string `json:"hub_transport,omitempty"`
	HubDomain    string `json:"hub_domain,omitempty"`
	HubBaseURL   string `json:"hub_base_url,omitempty"`
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
	okTaskTTL  time.Duration

	nextEventID int64
	events      []Event
	tasks       map[string]*taskState
	closedTasks map[string]time.Time
	runConfigs  map[string][]byte
	rejectedSeq uint64
	subs        map[chan struct{}]struct{}

	hubConnected bool
	hubTransport string
	hubBaseURL   string
	hubDomain    string
	resources    ResourceMetrics
}

type taskState struct {
	RequestID    string
	Prompt       string
	Skill        string
	Repo         string
	Repos        []string
	BaseBranch   string
	Status       string
	Stage        string
	StageStatus  string
	ExitCode     int
	WorkspaceDir string
	Branch       string
	PRURL        string
	Error        string
	StartedAt    time.Time
	UpdatedAt    time.Time
	Logs         []TaskLog
}

// NewBroker returns a monitor state broker with safe defaults.
func NewBroker() *Broker {
	return &Broker{
		now:         time.Now,
		maxEvents:   defaultMaxEvents,
		maxTaskLog:  defaultMaxTaskLogs,
		okTaskTTL:   defaultOKTaskRetentionWindow,
		tasks:       map[string]*taskState{},
		closedTasks: map[string]time.Time{},
		runConfigs:  map[string][]byte{},
		subs:        map[chan struct{}]struct{}{},
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

	if requestID != "" && !b.isClosedTaskLocked(requestID, now) {
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
			RequestID:    t.RequestID,
			Prompt:       t.Prompt,
			Skill:        t.Skill,
			Repo:         t.Repo,
			Repos:        append([]string(nil), t.Repos...),
			BaseBranch:   t.BaseBranch,
			Status:       t.Status,
			Stage:        t.Stage,
			StageStatus:  t.StageStatus,
			ExitCode:     t.ExitCode,
			WorkspaceDir: t.WorkspaceDir,
			Branch:       t.Branch,
			PRURL:        t.PRURL,
			Error:        t.Error,
			StartedAt:    t.StartedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:    t.UpdatedAt.UTC().Format(time.RFC3339Nano),
			CanRerun:     canRerun,
			Logs:         append([]TaskLog(nil), t.Logs...),
		})
	}

	return snapshot
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

	b.mu.Lock()
	defer b.mu.Unlock()

	changed := false
	if existing, ok := b.runConfigs[requestID]; !ok || !bytes.Equal(existing, cfgCopy) {
		b.runConfigs[requestID] = cfgCopy
		changed = true
	}
	if prompt != "" {
		if t, ok := b.tasks[requestID]; ok && t.Prompt != prompt {
			t.Prompt = prompt
			changed = true
		}
	}
	if baseBranch != "" {
		if t, ok := b.tasks[requestID]; ok {
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
		RequestID:   requestID,
		Prompt:      prompt,
		Repo:        firstRepo(repos),
		Repos:       append([]string(nil), repos...),
		BaseBranch:  baseBranch,
		Status:      status,
		Branch:      baseBranch,
		Error:       errText,
		StartedAt:   now,
		UpdatedAt:   now,
		Stage:       "submit",
		StageStatus: status,
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

// CloseTask removes a completed task and its stored rerun config.
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
	delete(b.runConfigs, requestID)
	b.closedTasks[requestID] = b.now().UTC()
	b.notifySubscribersLocked()
	return nil
}

func (b *Broker) pruneExpiredTasksLocked(now time.Time) {
	if b.okTaskTTL > 0 {
		for requestID, task := range b.tasks {
			if !b.shouldHideTaskLocked(task, now) {
				continue
			}
			delete(b.tasks, requestID)
			delete(b.runConfigs, requestID)
		}
	}
	for requestID, closedAt := range b.closedTasks {
		if now.Sub(closedAt) < defaultClosedTaskRetention {
			continue
		}
		delete(b.closedTasks, requestID)
	}
}

func (b *Broker) isClosedTaskLocked(requestID string, now time.Time) bool {
	closedAt, ok := b.closedTasks[requestID]
	if !ok {
		return false
	}
	if now.Sub(closedAt) >= defaultClosedTaskRetention {
		delete(b.closedTasks, requestID)
		return false
	}
	return true
}

func (b *Broker) shouldHideTaskLocked(task *taskState, now time.Time) bool {
	if task == nil || task.Status != "ok" || b.okTaskTTL <= 0 {
		return false
	}
	return !task.UpdatedAt.IsZero() && now.Sub(task.UpdatedAt) >= b.okTaskTTL
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
		}
		if existing.BaseBranch == "" {
			existing.BaseBranch = b.taskBaseBranchLocked(requestID)
		}
		if existing.Branch == "" {
			existing.Branch = b.taskInitialBranchLocked(requestID)
		}
		existing.UpdatedAt = now
		return existing
	}

	t := &taskState{
		RequestID:  requestID,
		Prompt:     b.taskPromptLocked(requestID),
		BaseBranch: b.taskBaseBranchLocked(requestID),
		Branch:     b.taskInitialBranchLocked(requestID),
		Status:     "pending",
		StartedAt:  now,
		UpdatedAt:  now,
	}
	b.tasks[requestID] = t
	return t
}

func (b *Broker) updateTaskFromLineLocked(t *taskState, line string, fields map[string]string, now time.Time) {
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

	if strings.HasPrefix(line, "dispatch status=ok") {
		t.Status = "ok"
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(fields["pr_url"], t.PRURL)
	}

	if strings.HasPrefix(line, "dispatch status=no_changes") {
		t.Status = "no_changes"
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
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

	if strings.HasPrefix(line, "dispatch status=stopped") {
		t.Status = "stopped"
		if code, ok := parseIntField(fields["exit_code"]); ok {
			t.ExitCode = code
		}
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(fields["pr_url"], t.PRURL)
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
		if strings.TrimSpace(t.Error) == "" {
			t.Error = "task stopped by operator"
		}
	}

	if strings.HasPrefix(line, "dispatch status=error") {
		t.Status = "error"
		if code, ok := parseIntField(fields["exit_code"]); ok {
			t.ExitCode = code
		}
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(fields["pr_url"], t.PRURL)
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
	}

	if strings.HasPrefix(line, "dispatch status=invalid") {
		t.Status = "invalid"
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
	}

	if strings.HasPrefix(line, "dispatch status=duplicate") {
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
		t.PRURL = firstNonEmpty(fields["pr_url"], t.PRURL)
		t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
	}

	if strings.Contains(line, " cmd ") && fields["b64"] != "" {
		decoded, err := base64.StdEncoding.DecodeString(fields["b64"])
		if err == nil {
			b.appendTaskLogLocked(t, TaskLog{
				Time:   now.Format(time.RFC3339Nano),
				Stream: firstNonEmpty(fields["stream"], "stdout"),
				Text:   string(decoded),
			})
			return
		}
	}

	if strings.HasPrefix(line, "dispatch ") {
		b.appendTaskLogLocked(t, TaskLog{
			Time:   now.Format(time.RFC3339Nano),
			Stream: "meta",
			Text:   line,
		})
	}
}

func (b *Broker) appendTaskLogLocked(t *taskState, line TaskLog) {
	t.Logs = appendCappedTaskLog(t.Logs, b.maxTaskLog, line)
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
		if b.hubTransport == hubTransportDisconnected {
			b.hubTransport = ""
		}
	case strings.HasPrefix(line, "hub.ws status=connected"):
		b.hubConnected = true
		b.hubTransport = hubTransportWS
	case strings.HasPrefix(line, "hub.transport mode=openclaw_pull"):
		// Pull mode still means the daemon is connected to MoltenHub transport.
		b.hubConnected = true
		b.hubTransport = hubTransportHTTPLongPoll
	case strings.HasPrefix(line, "hub.transport mode=openclaw_ws"):
		b.hubConnected = true
		b.hubTransport = hubTransportWS
	case strings.HasPrefix(line, "hub.connection "):
		switch strings.ToLower(strings.TrimSpace(fields["status"])) {
		case "connected", "online", "ok":
			b.hubConnected = true
			if b.hubTransport == hubTransportDisconnected {
				b.hubTransport = ""
			}
		case "disconnected", "offline", "error":
			b.hubConnected = false
			b.hubTransport = hubTransportDisconnected
		}
	case strings.HasPrefix(line, "hub.ws status=disabled"),
		strings.HasPrefix(line, "hub.ws status=error"),
		strings.HasPrefix(line, "hub.ws status=disconnected"),
		strings.HasPrefix(line, "hub.pull status=error"),
		strings.HasPrefix(line, "hub.agent status=offline"):
		b.hubConnected = false
		b.hubTransport = hubTransportDisconnected
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

func promptFromRunConfigJSON(runConfigJSON []byte) string {
	if len(runConfigJSON) == 0 {
		return ""
	}
	var raw struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return ""
	}
	return strings.TrimSpace(raw.Prompt)
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
	switch strings.TrimSpace(status) {
	case "ok", "no_changes", "error", "invalid", "duplicate", "stopped":
		return true
	default:
		return false
	}
}
