package hubui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxEvents   = 600
	defaultMaxTaskLogs = 2000
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

// Snapshot is the complete monitor payload for the web UI.
type Snapshot struct {
	GeneratedAt string  `json:"generated_at"`
	Events      []Event `json:"events"`
	Tasks       []Task  `json:"tasks"`
}

// Broker collects daemon logs and exposes monitor state snapshots.
type Broker struct {
	mu sync.RWMutex

	now        func() time.Time
	maxEvents  int
	maxTaskLog int

	nextEventID int64
	events      []Event
	tasks       map[string]*taskState
	runConfigs  map[string][]byte
	subs        map[chan struct{}]struct{}
}

type taskState struct {
	RequestID    string
	Prompt       string
	Skill        string
	Repo         string
	Repos        []string
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
		now:        time.Now,
		maxEvents:  defaultMaxEvents,
		maxTaskLog: defaultMaxTaskLogs,
		tasks:      map[string]*taskState{},
		runConfigs: map[string][]byte{},
		subs:       map[chan struct{}]struct{}{},
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

	b.nextEventID++
	b.events = append(b.events, Event{
		ID:        b.nextEventID,
		Time:      now.Format(time.RFC3339Nano),
		Kind:      classifyEventKind(line),
		RequestID: requestID,
		Line:      line,
	})
	if len(b.events) > b.maxEvents {
		b.events = append([]Event(nil), b.events[len(b.events)-b.maxEvents:]...)
	}

	if requestID != "" {
		t := b.ensureTaskLocked(requestID, now)
		b.updateTaskFromLineLocked(t, line, fields, now)
	}

	b.notifySubscribersLocked()
}

// Snapshot returns a deep copy of current monitor state.
func (b *Broker) Snapshot() Snapshot {
	if b == nil {
		return Snapshot{}
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	snapshot := Snapshot{
		GeneratedAt: b.now().UTC().Format(time.RFC3339Nano),
		Events:      append([]Event(nil), b.events...),
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
	if changed {
		b.notifySubscribersLocked()
	}
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
			existing.Prompt = promptFromRunConfigJSON(b.runConfigs[requestID])
		}
		existing.UpdatedAt = now
		return existing
	}

	t := &taskState{
		RequestID: requestID,
		Prompt:    promptFromRunConfigJSON(b.runConfigs[requestID]),
		Status:    "pending",
		StartedAt: now,
		UpdatedAt: now,
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

	if strings.HasPrefix(line, "dispatch status=error") {
		t.Status = "error"
		if code, ok := parseIntField(fields["exit_code"]); ok {
			t.ExitCode = code
		}
		t.WorkspaceDir = firstNonEmpty(fields["workspace"], fields["workspace_dir"], t.WorkspaceDir)
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(fields["pr_url"], t.PRURL)
		t.Error = firstNonEmpty(parseFieldValue(line, "err"), parseFieldValue(line, "error"), t.Error)
	}

	if strings.HasPrefix(line, "dispatch status=invalid") {
		t.Status = "invalid"
		t.Error = firstNonEmpty(parseFieldValue(line, "err"), parseFieldValue(line, "error"), t.Error)
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
		t.PRURL = firstNonEmpty(fields["pr_url"], t.PRURL)
		t.Error = firstNonEmpty(parseFieldValue(line, "err"), parseFieldValue(line, "error"), t.Error)
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
	t.Logs = append(t.Logs, line)
	if len(t.Logs) > b.maxTaskLog {
		t.Logs = append([]TaskLog(nil), t.Logs[len(t.Logs)-b.maxTaskLog:]...)
	}
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
	out := map[string]string{}
	for _, field := range strings.Fields(line) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		out[k] = strings.Trim(v, "\"")
	}
	return out
}

func parseFieldValue(line, key string) string {
	needle := key + "="
	idx := strings.Index(line, needle)
	if idx < 0 {
		return ""
	}

	rest := line[idx+len(needle):]
	if rest == "" {
		return ""
	}

	if strings.HasPrefix(rest, "\"") {
		if token, ok := parseQuotedToken(rest); ok {
			decoded, err := strconv.Unquote(token)
			if err == nil {
				return strings.TrimSpace(decoded)
			}
			return strings.TrimSpace(strings.Trim(token, "\""))
		}
		return strings.TrimSpace(strings.Trim(rest, "\""))
	}

	end := strings.IndexAny(rest, " \t")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
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
