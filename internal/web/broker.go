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
	defaultMaxReleases         = 100
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
	RequestID         string        `json:"request_id"`
	Source            string        `json:"source,omitempty"`
	Prompt            string        `json:"prompt,omitempty"`
	PromptIsUserInput bool          `json:"prompt_is_user_input"`
	Images            []PromptImage `json:"images,omitempty"`
	Skill             string        `json:"skill,omitempty"`
	Workflow          string        `json:"workflow,omitempty"`
	AgentHarness      string        `json:"agent_harness,omitempty"`
	Repo              string        `json:"repo,omitempty"`
	Repos             []string      `json:"repos,omitempty"`
	BaseBranch        string        `json:"base_branch,omitempty"`
	Status            string        `json:"status"`
	Stage             string        `json:"stage,omitempty"`
	StageStatus       string        `json:"stage_status,omitempty"`
	ExitCode          int           `json:"exit_code,omitempty"`
	WorkspaceDir      string        `json:"workspace_dir,omitempty"`
	Branch            string        `json:"branch,omitempty"`
	PRURL             string        `json:"pr_url,omitempty"`
	Error             string        `json:"error,omitempty"`
	StartedAt         string        `json:"started_at"`
	UpdatedAt         string        `json:"updated_at"`
	DurationSeconds   float64       `json:"duration_seconds,omitempty"`
	CanRerun          bool          `json:"can_rerun,omitempty"`
	Controls          TaskControls  `json:"controls,omitempty"`
	Logs              []TaskLog     `json:"logs"`
}

// PromptImage captures one prompt image attachment shown in task views.
type PromptImage struct {
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"mediaType,omitempty"`
	DataBase64 string `json:"dataBase64,omitempty"`
}

// Release represents a merged pull request that shipped from one originating prompt.
type Release struct {
	RequestID         string   `json:"request_id"`
	Prompt            string   `json:"prompt,omitempty"`
	PromptIsUserInput bool     `json:"prompt_is_user_input"`
	Skill             string   `json:"skill,omitempty"`
	Workflow          string   `json:"workflow,omitempty"`
	AgentHarness      string   `json:"agent_harness,omitempty"`
	Repo              string   `json:"repo,omitempty"`
	Repos             []string `json:"repos,omitempty"`
	BaseBranch        string   `json:"base_branch,omitempty"`
	Branch            string   `json:"branch,omitempty"`
	PRURL             string   `json:"pr_url,omitempty"`
	StartedAt         string   `json:"started_at,omitempty"`
	CompletedAt       string   `json:"completed_at,omitempty"`
	MergedAt          string   `json:"merged_at,omitempty"`
	ReleasedAt        string   `json:"released_at"`
	DurationSeconds   float64  `json:"duration_seconds,omitempty"`
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

// DashboardStats captures in-memory run stats for the monitor dashboard.
type DashboardStats struct {
	TotalTasks             int               `json:"total_tasks"`
	ActiveTasks            int               `json:"active_tasks"`
	CompletedTasks         int               `json:"completed_tasks"`
	FailedTasks            int               `json:"failed_tasks"`
	MaxConcurrentTasks     int               `json:"max_concurrent_tasks"`
	SessionRuntimeSeconds  float64           `json:"session_runtime_seconds"`
	TotalSavedSeconds      float64           `json:"total_saved_seconds"`
	ReviewTasks            int               `json:"review_tasks"`
	ReviewActiveTasks      int               `json:"review_active_tasks"`
	ReviewCompletedTasks   int               `json:"review_completed_tasks"`
	ReviewFailedTasks      int               `json:"review_failed_tasks"`
	ReviewSavedSeconds     float64           `json:"review_saved_seconds"`
	SuccessRate            float64           `json:"success_rate"`
	AverageDurationSeconds float64           `json:"average_duration_seconds"`
	VelocityPerHour        float64           `json:"velocity_per_hour"`
	ThroughputPerHour      float64           `json:"throughput_per_hour"`
	UpdatedAt              string            `json:"updated_at,omitempty"`
	WorkflowTimes          []TimeStatsGroup  `json:"workflow_times,omitempty"`
	AgentTimes             []TimeStatsGroup  `json:"agent_times,omitempty"`
	SourceMix              []CountStatsGroup `json:"source_mix,omitempty"`
}

type RepositoryVisibility string

const (
	RepositoryVisibilityUnknown RepositoryVisibility = "unknown"
	RepositoryVisibilityPublic  RepositoryVisibility = "public"
	RepositoryVisibilityPrivate RepositoryVisibility = "private"
)

type RepositoryOwnerKind string

const (
	RepositoryOwnerUnknown      RepositoryOwnerKind = "unknown"
	RepositoryOwnerPersonal     RepositoryOwnerKind = "personal"
	RepositoryOwnerOrganization RepositoryOwnerKind = "organization"
)

// Repository is the in-memory view of one repository and the work observed for it.
type Repository struct {
	Key            string               `json:"key"`
	Name           string               `json:"name,omitempty"`
	FullName       string               `json:"full_name,omitempty"`
	Description    string               `json:"description,omitempty"`
	HTMLURL        string               `json:"html_url,omitempty"`
	OwnerAvatarURL string               `json:"owner_avatar_url,omitempty"`
	DefaultBranch  string               `json:"default_branch,omitempty"`
	Language       string               `json:"language,omitempty"`
	UpdatedAt      string               `json:"updated_at,omitempty"`
	PushedAt       string               `json:"pushed_at,omitempty"`
	Visibility     RepositoryVisibility `json:"visibility"`
	OwnerKind      RepositoryOwnerKind  `json:"owner_kind"`
	Private        bool                 `json:"private"`
	Public         bool                 `json:"public"`
	Personal       bool                 `json:"personal"`
	Organization   bool                 `json:"organization"`
	Stats          RepositoryStats      `json:"stats"`
	PullRequests   []string             `json:"pull_requests,omitempty"`
}

type RepositoryStats struct {
	TotalTasks             int     `json:"total_tasks"`
	ActiveTasks            int     `json:"active_tasks"`
	CompletedTasks         int     `json:"completed_tasks"`
	FailedTasks            int     `json:"failed_tasks"`
	TotalDurationSeconds   float64 `json:"total_duration_seconds"`
	TotalSavedSeconds      float64 `json:"total_saved_seconds"`
	AverageDurationSeconds float64 `json:"average_duration_seconds"`
}

// TimeStatsGroup captures observed runtime and saved time for one workflow or agent.
type TimeStatsGroup struct {
	Name                   string  `json:"name"`
	Tasks                  int     `json:"tasks"`
	ActiveTasks            int     `json:"active_tasks"`
	CompletedTasks         int     `json:"completed_tasks"`
	TotalDurationSeconds   float64 `json:"total_duration_seconds"`
	TotalSavedSeconds      float64 `json:"total_saved_seconds"`
	AverageDurationSeconds float64 `json:"average_duration_seconds"`
}

// CountStatsGroup captures task counts for dashboard mix charts.
type CountStatsGroup struct {
	Name           string `json:"name"`
	Tasks          int    `json:"tasks"`
	ActiveTasks    int    `json:"active_tasks"`
	CompletedTasks int    `json:"completed_tasks"`
	FailedTasks    int    `json:"failed_tasks"`
}

// Snapshot is the complete monitor payload for the web UI.
type Snapshot struct {
	GeneratedAt   string          `json:"generated_at"`
	Connection    Connection      `json:"connection"`
	Resources     ResourceMetrics `json:"resources"`
	Stats         DashboardStats  `json:"stats"`
	Repositories  []Repository    `json:"repositories,omitempty"`
	Releases      []Release       `json:"releases"`
	Events        []Event         `json:"events"`
	Tasks         []Task          `json:"tasks"`
	PromptedRepos []string        `json:"prompted_repos,omitempty"`
}

// Broker collects daemon logs and exposes monitor state snapshots.
type Broker struct {
	mu sync.RWMutex

	now        func() time.Time
	maxEvents  int
	maxTaskLog int

	nextEventID   int64
	events        []Event
	releases      []releaseState
	tasks         map[string]*taskState
	closedTasks   map[string]time.Time
	runConfigs    map[string][]byte
	promptedRepos []string
	repositories  []Repository
	repoIndex     map[string]int
	attempts      map[string][]taskAttemptState
	attemptRoots  map[string]string
	rejectedSeq   uint64
	subs          map[chan struct{}]struct{}

	hubConnected       bool
	hubTransport       string
	hubBaseURL         string
	hubDomain          string
	hubDetail          string
	resources          ResourceMetrics
	maxConcurrentTasks int
	sessionStartedAt   time.Time
}

type taskState struct {
	RequestID         string
	Source            string
	Prompt            string
	PromptIsUserInput bool
	Images            []PromptImage
	Skill             string
	Workflow          string
	AgentHarness      string
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

type releaseState struct {
	RequestID         string
	Prompt            string
	PromptIsUserInput bool
	Skill             string
	Workflow          string
	AgentHarness      string
	Repo              string
	Repos             []string
	BaseBranch        string
	Branch            string
	PRURL             string
	StartedAt         time.Time
	CompletedAt       time.Time
	MergedAt          time.Time
	ReleasedAt        time.Time
	Duration          time.Duration
}

type taskAttemptState struct {
	RequestID string
	RerunOf   string
	Workflow  string
	Agent     string
	Source    string
	Repos     []string
	PRURL     string
	Status    string
	Error     string
	StartedAt time.Time
	UpdatedAt time.Time
}

// NewBroker returns a monitor state broker with safe defaults.
func NewBroker() *Broker {
	now := time.Now
	return &Broker{
		now:              now,
		maxEvents:        defaultMaxEvents,
		maxTaskLog:       defaultMaxTaskLogs,
		tasks:            map[string]*taskState{},
		closedTasks:      map[string]time.Time{},
		runConfigs:       map[string][]byte{},
		repoIndex:        map[string]int{},
		attempts:         map[string][]taskAttemptState{},
		attemptRoots:     map[string]string{},
		subs:             map[chan struct{}]struct{}{},
		sessionStartedAt: now().UTC(),
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
		Resources:     b.resources,
		Events:        append([]Event(nil), b.events...),
		PromptedRepos: append([]string(nil), b.promptedRepos...),
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
		status := normalizeTaskTerminalStatus(t.Status)
		snapshot.Tasks = append(snapshot.Tasks, Task{
			RequestID:         t.RequestID,
			Source:            t.Source,
			Prompt:            t.Prompt,
			PromptIsUserInput: t.PromptIsUserInput,
			Images:            append([]PromptImage(nil), t.Images...),
			Skill:             t.Skill,
			Workflow:          taskWorkflow(t),
			AgentHarness:      taskAgentHarness(t),
			Repo:              t.Repo,
			Repos:             append([]string(nil), t.Repos...),
			BaseBranch:        t.BaseBranch,
			Status:            status,
			Stage:             t.Stage,
			StageStatus:       t.StageStatus,
			ExitCode:          t.ExitCode,
			WorkspaceDir:      t.WorkspaceDir,
			Branch:            t.Branch,
			PRURL:             t.PRURL,
			Error:             t.Error,
			StartedAt:         t.StartedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339Nano),
			DurationSeconds:   taskDuration(t.StartedAt, t.UpdatedAt, now, status).Seconds(),
			CanRerun:          canRerun,
			Logs:              append([]TaskLog(nil), t.Logs...),
		})
	}
	snapshot.Releases = b.releasesSnapshotLocked()
	snapshot.Stats = b.dashboardStatsLocked(now, tasks)
	snapshot.Repositories = b.repositoriesSnapshotLocked()

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
	status := normalizeTaskTerminalStatus(t.Status)
	return Task{
		RequestID:         t.RequestID,
		Source:            t.Source,
		Prompt:            t.Prompt,
		PromptIsUserInput: t.PromptIsUserInput,
		Images:            append([]PromptImage(nil), t.Images...),
		Skill:             t.Skill,
		Workflow:          taskWorkflow(t),
		AgentHarness:      taskAgentHarness(t),
		Repo:              t.Repo,
		Repos:             append([]string(nil), t.Repos...),
		BaseBranch:        t.BaseBranch,
		Status:            status,
		Stage:             t.Stage,
		StageStatus:       t.StageStatus,
		ExitCode:          t.ExitCode,
		WorkspaceDir:      t.WorkspaceDir,
		Branch:            t.Branch,
		PRURL:             t.PRURL,
		Error:             t.Error,
		StartedAt:         t.StartedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		DurationSeconds:   taskDuration(t.StartedAt, t.UpdatedAt, now, status).Seconds(),
		CanRerun:          canRerun,
		Logs:              append([]TaskLog(nil), t.Logs...),
	}, true
}

// RecordTaskRunConfig stores a parsed task run config payload for future reruns.
func (b *Broker) RecordTaskRunConfig(requestID string, runConfigJSON []byte) {
	b.RecordTaskRunConfigWithSource(requestID, runConfigJSON, "")
}

// RecordTaskRunConfigWithSource stores a parsed task run config payload and task start source.
func (b *Broker) RecordTaskRunConfigWithSource(requestID string, runConfigJSON []byte, source string) {
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
	images := imagesFromRunConfigJSON(cfgCopy)
	baseBranch := branchFromRunConfigJSON(cfgCopy)
	repos := reposFromRunConfigJSON(cfgCopy)
	workflow := workflowFromRunConfigJSON(cfgCopy)
	agentHarness := agentHarnessFromRunConfigJSON(cfgCopy)
	source = firstNonEmpty(
		normalizeTaskSource(source),
		sourceFromRunConfigJSON(cfgCopy),
		defaultTaskSourceForRequestID(requestID),
	)
	now := b.now().UTC()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)

	changed := false
	if existing, ok := b.runConfigs[requestID]; !ok || !bytes.Equal(existing, cfgCopy) {
		b.runConfigs[requestID] = cfgCopy
		changed = true
	}
	if next := appendNonEmptyUnique(b.promptedRepos, repos...); !sameStringSlice(b.promptedRepos, next) {
		b.promptedRepos = next
		changed = true
	}
	if b.registerRepositoriesLocked(repos, nil) {
		changed = true
	}
	t, taskExists := b.tasks[requestID]
	if !taskExists && !b.isClosedTaskLocked(requestID, now) {
		t = &taskState{
			RequestID:         requestID,
			Source:            source,
			Prompt:            prompt,
			PromptIsUserInput: promptIsUserInputForTask(requestID, prompt),
			Images:            append([]PromptImage(nil), images...),
			Workflow:          workflow,
			AgentHarness:      agentHarness,
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
		b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, t.Workflow, t.AgentHarness, t.Source, t.Repos, t.PRURL, now)
		changed = true
	}
	if source != "" && t != nil && t.Source != source {
		t.Source = source
		b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, t.Workflow, t.AgentHarness, t.Source, t.Repos, t.PRURL, now)
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
	if t != nil && !samePromptImages(t.Images, images) {
		t.Images = append([]PromptImage(nil), images...)
		changed = true
	}
	if workflow != "" && t != nil && t.Workflow != workflow {
		t.Workflow = workflow
		changed = true
	}
	if agentHarness != "" && t != nil && t.AgentHarness != agentHarness {
		t.AgentHarness = agentHarness
		changed = true
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

// RecordTaskSource updates the start source for an already visible task.
func (b *Broker) RecordTaskSource(requestID string, source string) {
	if b == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	source = normalizeTaskSource(source)
	if requestID == "" || source == "" {
		return
	}

	now := b.now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTasksLocked(now)
	t, ok := b.tasks[requestID]
	if !ok || t == nil || t.Source == source {
		return
	}
	t.Source = source
	t.UpdatedAt = now
	b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, t.Workflow, t.AgentHarness, t.Source, t.Repos, t.PRURL, now)
	b.notifySubscribersLocked()
}

// RecordRejectedPromptSubmission stores a failed prompt submission so it remains visible in the task list.
func (b *Broker) RecordRejectedPromptSubmission(runConfigJSON []byte, status string, err error) string {
	return b.RecordRejectedPromptSubmissionWithSource(runConfigJSON, status, err, "")
}

// RecordRejectedPromptSubmissionWithSource stores a failed prompt submission with its start source.
func (b *Broker) RecordRejectedPromptSubmissionWithSource(runConfigJSON []byte, status string, err error, source string) string {
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
	images := imagesFromRunConfigJSON(runConfigJSON)
	workflow := workflowFromRunConfigJSON(runConfigJSON)
	agentHarness := agentHarnessFromRunConfigJSON(runConfigJSON)
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
		Source:            firstNonEmpty(normalizeTaskSource(source), sourceFromRunConfigJSON(runConfigJSON), defaultTaskSourceForRequestID(requestID)),
		Prompt:            prompt,
		PromptIsUserInput: promptIsUserInputForTask(requestID, prompt),
		Images:            append([]PromptImage(nil), images...),
		Workflow:          workflow,
		AgentHarness:      agentHarness,
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
	b.registerRepositoriesLocked(t.Repos, nil)
	b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, t.Workflow, t.AgentHarness, t.Source, t.Repos, t.PRURL, now)
	b.notifySubscribersLocked()
	return requestID
}

func (b *Broker) dashboardStatsLocked(now time.Time, tasks []*taskState) DashboardStats {
	active := 0
	seen := map[string]taskAttemptState{}
	for _, t := range tasks {
		if t == nil {
			continue
		}
		if isActiveTaskStatus(t.Status) {
			active++
		}
		if strings.TrimSpace(t.RequestID) == "" {
			continue
		}
		seen[t.RequestID] = taskAttemptState{
			RequestID: t.RequestID,
			Workflow:  taskWorkflow(t),
			Agent:     taskAgentHarness(t),
			Source:    normalizeTaskSource(t.Source),
			Repos:     append([]string(nil), t.Repos...),
			PRURL:     strings.TrimSpace(t.PRURL),
			Status:    normalizeTaskTerminalStatus(t.Status),
			Error:     strings.TrimSpace(t.Error),
			StartedAt: t.StartedAt,
			UpdatedAt: t.UpdatedAt,
		}
	}
	for _, attempts := range b.attempts {
		for _, attempt := range attempts {
			requestID := strings.TrimSpace(attempt.RequestID)
			if requestID == "" {
				continue
			}
			seen[requestID] = attempt
		}
	}
	if active > b.maxConcurrentTasks {
		b.maxConcurrentTasks = active
	}

	stats := DashboardStats{
		TotalTasks:            len(seen),
		ActiveTasks:           active,
		MaxConcurrentTasks:    b.maxConcurrentTasks,
		SessionRuntimeSeconds: taskDuration(b.sessionStartedAt, time.Time{}, now, "").Seconds(),
		UpdatedAt:             now.UTC().Format(time.RFC3339Nano),
	}
	var earliest time.Time
	var totalDuration time.Duration
	durationCount := 0
	successfulTerminals := 0
	workflowGroups := map[string]*TimeStatsGroup{}
	agentGroups := map[string]*TimeStatsGroup{}
	sourceGroups := map[string]*CountStatsGroup{}
	repoAttempts := b.updateRepositoryStatsLocked(now, seen)
	counted := map[string]struct{}{}
	recordAttempt := func(attempt taskAttemptState) {
		requestID := strings.TrimSpace(attempt.RequestID)
		if requestID != "" {
			if _, ok := counted[requestID]; ok {
				return
			}
			counted[requestID] = struct{}{}
		}
		startedAt := attempt.StartedAt
		updatedAt := attempt.UpdatedAt
		if startedAt.IsZero() {
			startedAt = updatedAt
		}
		if updatedAt.IsZero() {
			updatedAt = startedAt
		}
		if !startedAt.IsZero() && (earliest.IsZero() || startedAt.Before(earliest)) {
			earliest = startedAt
		}
		status := normalizeTaskTerminalStatus(attempt.Status)
		if isSuccessfulTaskStatus(status) {
			stats.CompletedTasks++
			successfulTerminals++
		} else if isFailedTaskStatus(status) {
			stats.FailedTasks++
		}
		reviewAttempt := isReviewTaskAttempt(attempt)
		if reviewAttempt {
			stats.ReviewTasks++
			if isActiveTaskStatus(status) {
				stats.ReviewActiveTasks++
			}
			if isSuccessfulTaskStatus(status) {
				stats.ReviewCompletedTasks++
			}
			if isFailedTaskStatus(status) {
				stats.ReviewFailedTasks++
			}
		}
		if isCompletedTaskStatus(status) && !startedAt.IsZero() && !updatedAt.IsZero() && !updatedAt.Before(startedAt) {
			duration := updatedAt.Sub(startedAt)
			totalDuration += duration
			durationCount++
		}
		duration := taskDuration(startedAt, updatedAt, now, status)
		savedDuration := savedDurationForAttempt(attempt, status, duration)
		stats.TotalSavedSeconds += savedDuration.Seconds()
		if reviewAttempt {
			stats.ReviewSavedSeconds += savedDuration.Seconds()
		}
		addTimeStatsGroupWithSavedDuration(workflowGroups, workflowStatsGroupName(attempt.Workflow), status, duration, savedDuration)
		addTimeStatsGroupWithSavedDuration(agentGroups, normalizedGroupName(attempt.Agent, "Unknown Agent"), status, duration, savedDuration)
		addCountStatsGroup(sourceGroups, sourceGroupName(attempt.Source), status)
	}
	for _, repo := range b.repositories {
		for _, attempt := range repoAttempts[repo.Key] {
			recordAttempt(attempt)
		}
	}
	for _, attempt := range seen {
		recordAttempt(attempt)
	}
	terminalTasks := stats.CompletedTasks + stats.FailedTasks
	if terminalTasks > 0 {
		stats.SuccessRate = float64(successfulTerminals) / float64(terminalTasks)
	}
	if durationCount > 0 {
		stats.AverageDurationSeconds = totalDuration.Seconds() / float64(durationCount)
	}
	if !earliest.IsZero() {
		elapsedHours := now.Sub(earliest).Hours()
		if elapsedHours <= 0 {
			elapsedHours = 1.0 / 3600.0
		}
		stats.VelocityPerHour = float64(stats.TotalTasks) / elapsedHours
		stats.ThroughputPerHour = float64(stats.CompletedTasks) / elapsedHours
	}
	stats.WorkflowTimes = sortedTimeStatsGroups(workflowGroups)
	stats.AgentTimes = sortedTimeStatsGroups(agentGroups)
	stats.SourceMix = sortedCountStatsGroups(sourceGroups)
	return stats
}

func isActiveTaskStatus(status string) bool {
	switch normalizeTaskTerminalStatus(status) {
	case "completed", "no_changes", "error", "invalid", "duplicate", "stopped":
		return false
	default:
		return true
	}
}

func isSuccessfulTaskStatus(status string) bool {
	switch normalizeTaskTerminalStatus(status) {
	case "completed", "no_changes":
		return true
	default:
		return false
	}
}

func isFailedTaskStatus(status string) bool {
	switch normalizeTaskTerminalStatus(status) {
	case "error", "invalid", "duplicate":
		return true
	default:
		return false
	}
}

func isReviewTaskAttempt(attempt taskAttemptState) bool {
	if normalizeTaskSource(attempt.Source) == "review" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(attempt.Workflow)) {
	case "code-review", "code_review", "pull request code review":
		return true
	default:
		return false
	}
}

func savedDurationForAttempt(attempt taskAttemptState, status string, duration time.Duration) time.Duration {
	if duration <= 0 {
		return 0
	}
	if isReviewTaskAttempt(attempt) {
		return duration
	}
	if isSuccessfulTaskStatus(status) {
		return duration
	}
	return 0
}

func taskDuration(startedAt, updatedAt, now time.Time, status string) time.Duration {
	if startedAt.IsZero() {
		return 0
	}
	end := updatedAt
	if !isCompletedTaskStatus(status) {
		end = now
	}
	if end.IsZero() || end.Before(startedAt) {
		return 0
	}
	return end.Sub(startedAt)
}

func parseTimeOrZero(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (b *Broker) releasesSnapshotLocked() []Release {
	if len(b.releases) == 0 {
		return nil
	}
	releases := append([]releaseState(nil), b.releases...)
	sort.Slice(releases, func(i, j int) bool {
		if releases[i].ReleasedAt.Equal(releases[j].ReleasedAt) {
			return releases[i].RequestID > releases[j].RequestID
		}
		return releases[i].ReleasedAt.After(releases[j].ReleasedAt)
	})
	out := make([]Release, 0, len(releases))
	for _, release := range releases {
		out = append(out, Release{
			RequestID:         release.RequestID,
			Prompt:            release.Prompt,
			PromptIsUserInput: release.PromptIsUserInput,
			Skill:             release.Skill,
			Workflow:          release.Workflow,
			AgentHarness:      release.AgentHarness,
			Repo:              release.Repo,
			Repos:             append([]string(nil), release.Repos...),
			BaseBranch:        release.BaseBranch,
			Branch:            release.Branch,
			PRURL:             release.PRURL,
			StartedAt:         formatTimeOrEmpty(release.StartedAt),
			CompletedAt:       formatTimeOrEmpty(release.CompletedAt),
			MergedAt:          formatTimeOrEmpty(release.MergedAt),
			ReleasedAt:        formatTimeOrEmpty(release.ReleasedAt),
			DurationSeconds:   release.Duration.Seconds(),
		})
	}
	return out
}

func (b *Broker) repositoriesSnapshotLocked() []Repository {
	if len(b.repositories) == 0 {
		return nil
	}
	repos := append([]Repository(nil), b.repositories...)
	sort.SliceStable(repos, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(repos[i].FullName, repos[i].HTMLURL, repos[i].Key))
		right := strings.ToLower(firstNonEmpty(repos[j].FullName, repos[j].HTMLURL, repos[j].Key))
		return left < right
	})
	for i := range repos {
		repos[i].PullRequests = append([]string(nil), repos[i].PullRequests...)
	}
	return repos
}

func (b *Broker) SetGitHubRepositories(repos []GitHubRepo) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.registerRepositoriesLocked(nil, repos) {
		b.notifySubscribersLocked()
	}
}

func (b *Broker) registerRepositoriesLocked(values []string, githubRepos []GitHubRepo) bool {
	if b.repoIndex == nil {
		b.repoIndex = map[string]int{}
	}
	changed := false
	for _, value := range values {
		key := repositoryKey(value)
		if key == "" {
			continue
		}
		if _, ok := b.repoIndex[key]; ok {
			continue
		}
		b.repoIndex[key] = len(b.repositories)
		b.repositories = append(b.repositories, Repository{
			Key:        key,
			Name:       repositoryNameFromValue(value),
			FullName:   repositoryFullNameFromValue(value),
			HTMLURL:    repositoryHTMLURLFromKey(key),
			Visibility: RepositoryVisibilityUnknown,
			OwnerKind:  RepositoryOwnerUnknown,
		})
		changed = true
	}
	for _, repo := range githubRepos {
		key := repositoryKey(firstNonEmpty(repo.FullName, repo.HTMLURL, repo.Name))
		if key == "" {
			continue
		}
		next := repositoryFromGitHubRepo(key, repo)
		if idx, ok := b.repoIndex[key]; ok {
			current := b.repositories[idx]
			next.Stats = current.Stats
			next.PullRequests = current.PullRequests
			b.repositories[idx] = mergeRepository(current, next)
			changed = true
			continue
		}
		b.repoIndex[key] = len(b.repositories)
		b.repositories = append(b.repositories, next)
		changed = true
	}
	return changed
}

func (b *Broker) updateRepositoryStatsLocked(now time.Time, attempts map[string]taskAttemptState) map[string][]taskAttemptState {
	for i := range b.repositories {
		b.repositories[i].Stats = RepositoryStats{}
		b.repositories[i].PullRequests = nil
	}
	repoAttempts := map[string][]taskAttemptState{}
	for _, attempt := range attempts {
		repos := appendNonEmptyUnique(nil, attempt.Repos...)
		b.registerRepositoriesLocked(repos, nil)
		for _, repoValue := range repos {
			key := repositoryKey(repoValue)
			idx, ok := b.repoIndex[key]
			if !ok {
				continue
			}
			repoAttempts[key] = append(repoAttempts[key], attempt)
			addRepositoryAttemptStats(&b.repositories[idx].Stats, attempt, now)
			if prURL := strings.TrimSpace(attempt.PRURL); prURL != "" {
				b.repositories[idx].PullRequests = appendNonEmptyUnique(b.repositories[idx].PullRequests, prURL)
			}
		}
	}
	return repoAttempts
}

func addRepositoryAttemptStats(stats *RepositoryStats, attempt taskAttemptState, now time.Time) {
	if stats == nil {
		return
	}
	status := normalizeTaskTerminalStatus(attempt.Status)
	duration := taskDuration(attempt.StartedAt, attempt.UpdatedAt, now, status)
	stats.TotalTasks++
	if isActiveTaskStatus(status) {
		stats.ActiveTasks++
	}
	if isSuccessfulTaskStatus(status) {
		stats.CompletedTasks++
	}
	if isFailedTaskStatus(status) {
		stats.FailedTasks++
	}
	stats.TotalSavedSeconds += savedDurationForAttempt(attempt, status, duration).Seconds()
	stats.TotalDurationSeconds += duration.Seconds()
	completed := stats.TotalTasks - stats.ActiveTasks
	if completed > 0 {
		stats.AverageDurationSeconds = stats.TotalDurationSeconds / float64(stats.TotalTasks)
	}
}

func taskWorkflow(t *taskState) string {
	if t == nil {
		return ""
	}
	return firstNonEmpty(t.Workflow, t.Skill)
}

func taskAgentHarness(t *taskState) string {
	if t == nil {
		return ""
	}
	return firstNonEmpty(t.AgentHarness, agentHarnessFromStage(t.Stage))
}

func workflowStatsGroupName(name string) string {
	name = normalizedGroupName(name, "Ad Hoc Prompt")
	if displayName := libraryTaskDisplayNameByName(name); displayName != "" {
		return displayName
	}
	return name
}

func libraryTaskDisplayNameByName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err != nil {
		return ""
	}
	for _, task := range catalog.Summaries() {
		if strings.TrimSpace(task.Name) != name {
			continue
		}
		return strings.TrimSpace(task.DisplayName)
	}
	return ""
}

func addTimeStatsGroup(groups map[string]*TimeStatsGroup, name, status string, duration time.Duration) {
	savedDuration := time.Duration(0)
	if isSuccessfulTaskStatus(status) {
		savedDuration = duration
	}
	addTimeStatsGroupWithSavedDuration(groups, name, status, duration, savedDuration)
}

func addTimeStatsGroupWithSavedDuration(groups map[string]*TimeStatsGroup, name, status string, duration, savedDuration time.Duration) {
	if groups == nil {
		return
	}
	name = normalizedGroupName(name, "Unknown")
	group := groups[name]
	if group == nil {
		group = &TimeStatsGroup{Name: name}
		groups[name] = group
	}
	group.Tasks++
	if isActiveTaskStatus(status) {
		group.ActiveTasks++
	}
	if isSuccessfulTaskStatus(status) {
		group.CompletedTasks++
	}
	group.TotalSavedSeconds += savedDuration.Seconds()
	group.TotalDurationSeconds += duration.Seconds()
	completed := group.Tasks - group.ActiveTasks
	if completed > 0 {
		group.AverageDurationSeconds = group.TotalDurationSeconds / float64(group.Tasks)
	}
}

func sortedTimeStatsGroups(groups map[string]*TimeStatsGroup) []TimeStatsGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]TimeStatsGroup, 0, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalSavedSeconds == out[j].TotalSavedSeconds {
			return out[i].Name < out[j].Name
		}
		return out[i].TotalSavedSeconds > out[j].TotalSavedSeconds
	})
	return out
}

func addCountStatsGroup(groups map[string]*CountStatsGroup, name, status string) {
	if groups == nil {
		return
	}
	name = normalizedGroupName(name, "Other")
	group := groups[name]
	if group == nil {
		group = &CountStatsGroup{Name: name}
		groups[name] = group
	}
	group.Tasks++
	if isActiveTaskStatus(status) {
		group.ActiveTasks++
	}
	if isSuccessfulTaskStatus(status) {
		group.CompletedTasks++
	}
	if isFailedTaskStatus(status) {
		group.FailedTasks++
	}
}

func sortedCountStatsGroups(groups map[string]*CountStatsGroup) []CountStatsGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]CountStatsGroup, 0, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tasks == out[j].Tasks {
			return out[i].Name < out[j].Name
		}
		return out[i].Tasks > out[j].Tasks
	})
	return out
}

func sourceGroupName(source string) string {
	switch normalizeTaskSource(source) {
	case "chat":
		return "Chat"
	case "hub":
		return "Hub"
	case "json":
		return "JSON"
	case "library":
		return "Library"
	case "review":
		return "Review"
	case "prompt":
		return "Prompt"
	default:
		return "Other"
	}
}

func normalizedGroupName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return fallback
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

// RecordReleaseFromTask stores a release entry for a merged pull request.
func (b *Broker) RecordReleaseFromTask(task Task, mergedAt string) {
	if b == nil {
		return
	}
	requestID := strings.TrimSpace(task.RequestID)
	prURL := strings.TrimSpace(task.PRURL)
	if requestID == "" || prURL == "" {
		return
	}

	now := b.now().UTC()
	startedAt := parseTimeOrZero(task.StartedAt)
	completedAt := parseTimeOrZero(task.UpdatedAt)
	mergedTime := parseTimeOrZero(mergedAt)
	if mergedTime.IsZero() {
		mergedTime = now
	}
	duration := time.Duration(0)
	if task.DurationSeconds > 0 {
		duration = time.Duration(task.DurationSeconds * float64(time.Second))
	} else if !startedAt.IsZero() && !completedAt.IsZero() && completedAt.After(startedAt) {
		duration = completedAt.Sub(startedAt)
	}

	release := releaseState{
		RequestID:         requestID,
		Prompt:            strings.TrimSpace(task.Prompt),
		PromptIsUserInput: task.PromptIsUserInput,
		Skill:             strings.TrimSpace(task.Skill),
		Workflow:          strings.TrimSpace(task.Workflow),
		AgentHarness:      strings.TrimSpace(task.AgentHarness),
		Repo:              strings.TrimSpace(task.Repo),
		Repos:             append([]string(nil), task.Repos...),
		BaseBranch:        strings.TrimSpace(task.BaseBranch),
		Branch:            strings.TrimSpace(task.Branch),
		PRURL:             prURL,
		StartedAt:         startedAt,
		CompletedAt:       completedAt,
		MergedAt:          mergedTime,
		ReleasedAt:        now,
		Duration:          duration,
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for i := range b.releases {
		if b.releases[i].RequestID == requestID {
			b.releases[i] = release
			b.notifySubscribersLocked()
			return
		}
	}
	b.releases = append([]releaseState{release}, b.releases...)
	if len(b.releases) > defaultMaxReleases {
		b.releases = b.releases[:defaultMaxReleases]
	}
	b.notifySubscribersLocked()
}

// MarkTaskPRMerged marks the originating task as done after its pull request merges.
func (b *Broker) MarkTaskPRMerged(requestID string, mergedAt string) error {
	if b == nil {
		return ErrTaskNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ErrTaskNotFound
	}

	now := b.now().UTC()
	mergedTime := parseTimeOrZero(mergedAt)
	if mergedTime.IsZero() {
		mergedTime = now
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	task, ok := b.tasks[requestID]
	if !ok || task == nil {
		return ErrTaskNotFound
	}
	if !isCompletedTaskStatus(task.Status) {
		return ErrTaskNotCompleted
	}

	task.Status = "merged"
	task.Stage = "finalize"
	task.StageStatus = "merged"
	task.UpdatedAt = mergedTime
	b.recordTaskAttemptLocked(
		b.taskAttemptRootLocked(requestID),
		requestID,
		"",
		task.Status,
		task.Error,
		task.Workflow,
		task.AgentHarness,
		task.Source,
		task.Repos,
		task.PRURL,
		mergedTime,
	)
	b.notifySubscribersLocked()
	return nil
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
	source := ""
	var repos []string
	prURL := ""
	if t, ok := b.tasks[rerunOf]; ok && t != nil {
		source = t.Source
		repos = append([]string(nil), t.Repos...)
		prURL = t.PRURL
	}
	b.recordTaskAttemptLocked(root, requestID, rerunOf, "queued", "", "", "", source, repos, prURL, now)
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
		if len(existing.Images) == 0 {
			existing.Images = imagesFromRunConfigJSON(b.runConfigs[requestID])
		}
		if existing.Workflow == "" {
			existing.Workflow = workflowFromRunConfigJSON(b.runConfigs[requestID])
		}
		if existing.AgentHarness == "" {
			existing.AgentHarness = agentHarnessFromRunConfigJSON(b.runConfigs[requestID])
		}
		if existing.Source == "" {
			existing.Source = firstNonEmpty(sourceFromRunConfigJSON(b.runConfigs[requestID]), defaultTaskSourceForRequestID(requestID))
		}
		if existing.BaseBranch == "" {
			existing.BaseBranch = b.taskBaseBranchLocked(requestID)
		}
		if existing.Branch == "" {
			existing.Branch = b.taskInitialBranchLocked(requestID)
		}
		existing.UpdatedAt = now
		b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", existing.Status, existing.Error, existing.Workflow, existing.AgentHarness, existing.Source, existing.Repos, existing.PRURL, now)
		return existing
	}

	prompt := b.taskPromptLocked(requestID)
	images := imagesFromRunConfigJSON(b.runConfigs[requestID])
	t := &taskState{
		RequestID:         requestID,
		Source:            firstNonEmpty(sourceFromRunConfigJSON(b.runConfigs[requestID]), defaultTaskSourceForRequestID(requestID)),
		Prompt:            prompt,
		PromptIsUserInput: promptIsUserInputForTask(requestID, prompt),
		Images:            images,
		Workflow:          workflowFromRunConfigJSON(b.runConfigs[requestID]),
		AgentHarness:      agentHarnessFromRunConfigJSON(b.runConfigs[requestID]),
		BaseBranch:        b.taskBaseBranchLocked(requestID),
		Branch:            b.taskInitialBranchLocked(requestID),
		Status:            "pending",
		StartedAt:         now,
		UpdatedAt:         now,
	}
	b.tasks[requestID] = t
	b.recordTaskAttemptLocked(b.taskAttemptRootLocked(requestID), requestID, "", t.Status, t.Error, t.Workflow, t.AgentHarness, t.Source, t.Repos, t.PRURL, now)
	return t
}

func (b *Broker) updateTaskFromLineLocked(t *taskState, line string, fields map[string]string, now time.Time) {
	defer func() {
		if t != nil {
			b.registerRepositoriesLocked(t.Repos, nil)
			b.recordTaskAttemptLocked(b.taskAttemptRootLocked(t.RequestID), t.RequestID, "", t.Status, t.Error, t.Workflow, t.AgentHarness, t.Source, t.Repos, t.PRURL, now)
		}
	}()

	t.UpdatedAt = now

	if strings.HasPrefix(line, "dispatch status=start") {
		t.Status = "running"
		t.Source = firstNonEmpty(t.Source, normalizeTaskSource(fields["source"]), defaultTaskSourceForRequestID(t.RequestID))
		t.Skill = firstNonEmpty(t.Skill, fields["skill"])
		t.Workflow = firstNonEmpty(t.Workflow, workflowFromFields(fields))
		t.AgentHarness = firstNonEmpty(t.AgentHarness, agentHarnessFromFields(fields))
		repos := reposFromFields(fields)
		t.Repos = appendNonEmptyUnique(t.Repos, repos...)
		b.promptedRepos = appendNonEmptyUnique(b.promptedRepos, repos...)
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
		agentInvocationLine := isAgentInvocationWorkflowStatusFields(fields)
		if stage := fields["stage"]; stage != "" {
			t.Stage = stage
			t.AgentHarness = firstNonEmpty(t.AgentHarness, agentHarnessFromStage(stage))
		}
		if stageStatus := fields["status"]; stageStatus != "" {
			t.StageStatus = stageStatus
			if stageStatus == "error" && t.Status == "running" && !agentInvocationLine {
				t.Status = "error"
			}
		}
		t.Branch = firstNonEmpty(fields["branch"], t.Branch)
		t.PRURL = firstNonEmpty(taskPRURLFromFields(fields), t.PRURL)
		if !agentInvocationLine {
			t.Error = firstNonEmpty(fields["err"], fields["error"], t.Error)
		}
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

func (b *Broker) recordTaskAttemptLocked(root, requestID, rerunOf, status, errText, workflow, agent, source string, repos []string, prURL string, now time.Time) {
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
	workflow = strings.TrimSpace(workflow)
	agent = strings.TrimSpace(agent)
	source = normalizeTaskSource(source)
	repos = appendNonEmptyUnique(nil, repos...)
	prURL = strings.TrimSpace(prURL)

	attempts := b.attempts[root]
	for i := range attempts {
		if attempts[i].RequestID != requestID {
			continue
		}
		if rerunOf != "" {
			attempts[i].RerunOf = rerunOf
		}
		if workflow != "" {
			attempts[i].Workflow = workflow
		}
		if agent != "" {
			attempts[i].Agent = agent
		}
		if source != "" {
			attempts[i].Source = source
		}
		if len(repos) > 0 {
			attempts[i].Repos = append([]string(nil), repos...)
		}
		if prURL != "" {
			attempts[i].PRURL = prURL
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
		Workflow:  workflow,
		Agent:     agent,
		Source:    source,
		Repos:     append([]string(nil), repos...),
		PRURL:     prURL,
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
	b.updateMaxConcurrentTasksLocked()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (b *Broker) updateMaxConcurrentTasksLocked() {
	active := 0
	for _, task := range b.tasks {
		if task != nil && isActiveTaskStatus(task.Status) {
			active++
		}
	}
	if active > b.maxConcurrentTasks {
		b.maxConcurrentTasks = active
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

func repositoryFromGitHubRepo(key string, repo GitHubRepo) Repository {
	ownerKind := repositoryOwnerKind(repo.OwnerType)
	visibility := RepositoryVisibilityPublic
	if repo.Private {
		visibility = RepositoryVisibilityPrivate
	}
	fullName := strings.TrimSpace(repo.FullName)
	return Repository{
		Key:            key,
		Name:           strings.TrimSpace(repo.Name),
		FullName:       fullName,
		Description:    strings.TrimSpace(repo.Description),
		HTMLURL:        strings.TrimSpace(repo.HTMLURL),
		OwnerAvatarURL: strings.TrimSpace(repo.OwnerAvatarURL),
		DefaultBranch:  strings.TrimSpace(repo.DefaultBranch),
		Language:       strings.TrimSpace(repo.Language),
		UpdatedAt:      strings.TrimSpace(repo.UpdatedAt),
		PushedAt:       strings.TrimSpace(repo.PushedAt),
		Visibility:     visibility,
		OwnerKind:      ownerKind,
		Private:        repo.Private,
		Public:         !repo.Private,
		Personal:       ownerKind == RepositoryOwnerPersonal,
		Organization:   ownerKind == RepositoryOwnerOrganization,
	}
}

func mergeRepository(current, next Repository) Repository {
	next.Key = firstNonEmpty(next.Key, current.Key)
	next.Name = firstNonEmpty(next.Name, current.Name)
	next.FullName = firstNonEmpty(next.FullName, current.FullName)
	next.Description = firstNonEmpty(next.Description, current.Description)
	next.HTMLURL = firstNonEmpty(next.HTMLURL, current.HTMLURL)
	next.OwnerAvatarURL = firstNonEmpty(next.OwnerAvatarURL, current.OwnerAvatarURL)
	next.DefaultBranch = firstNonEmpty(next.DefaultBranch, current.DefaultBranch)
	next.Language = firstNonEmpty(next.Language, current.Language)
	next.UpdatedAt = firstNonEmpty(next.UpdatedAt, current.UpdatedAt)
	next.PushedAt = firstNonEmpty(next.PushedAt, current.PushedAt)
	if next.Visibility == "" || next.Visibility == RepositoryVisibilityUnknown {
		next.Visibility = current.Visibility
	}
	if next.OwnerKind == "" || next.OwnerKind == RepositoryOwnerUnknown {
		next.OwnerKind = current.OwnerKind
	}
	next.Private = next.Visibility == RepositoryVisibilityPrivate
	next.Public = next.Visibility == RepositoryVisibilityPublic
	next.Personal = next.OwnerKind == RepositoryOwnerPersonal
	next.Organization = next.OwnerKind == RepositoryOwnerOrganization
	return next
}

func repositoryOwnerKind(ownerType string) RepositoryOwnerKind {
	switch strings.ToLower(strings.TrimSpace(ownerType)) {
	case "organization", "org":
		return RepositoryOwnerOrganization
	case "user", "personal":
		return RepositoryOwnerPersonal
	default:
		return RepositoryOwnerUnknown
	}
}

func repositoryKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	normalized := strings.TrimSuffix(value, ".git")
	if strings.HasPrefix(normalized, "git@github.com:") {
		normalized = strings.TrimPrefix(normalized, "git@github.com:")
		return strings.ToLower(strings.Trim(normalized, "/"))
	}
	if strings.HasPrefix(normalized, "https://github.com/") || strings.HasPrefix(normalized, "http://github.com/") {
		if u, err := url.Parse(normalized); err == nil {
			return strings.ToLower(strings.Trim(u.Path, "/"))
		}
	}
	if strings.Count(normalized, "/") == 1 && !strings.Contains(normalized, "://") {
		return strings.ToLower(strings.Trim(normalized, "/"))
	}
	return strings.ToLower(normalized)
}

func repositoryFullNameFromValue(value string) string {
	key := repositoryKey(value)
	if strings.Count(key, "/") == 1 {
		return key
	}
	return ""
}

func repositoryNameFromValue(value string) string {
	fullName := repositoryFullNameFromValue(value)
	if fullName == "" {
		return strings.TrimSpace(value)
	}
	parts := strings.Split(fullName, "/")
	return parts[len(parts)-1]
}

func repositoryHTMLURLFromKey(key string) string {
	key = strings.Trim(strings.TrimSpace(key), "/")
	if key == "" || strings.Count(key, "/") != 1 {
		return ""
	}
	return "https://github.com/" + key
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

func workflowFromFields(fields map[string]string) string {
	if fields == nil {
		return ""
	}
	return firstNonEmpty(fields["workflow"], fields["library_task"], fields["libraryTaskName"], fields["skill"])
}

func agentHarnessFromFields(fields map[string]string) string {
	if fields == nil {
		return ""
	}
	return firstNonEmpty(fields["agent_harness"], fields["agentHarness"], agentHarnessFromStage(fields["stage"]))
}

func agentHarnessFromStage(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "codex", "claude":
		return strings.ToLower(strings.TrimSpace(stage))
	default:
		return ""
	}
}

func isAgentInvocationWorkflowStatusFields(fields map[string]string) bool {
	if strings.TrimSpace(fields["agent_run_id"]) == "" {
		return false
	}
	return agentHarnessFromStage(fields["stage"]) != ""
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

func imagesFromRunConfigJSON(runConfigJSON []byte) []PromptImage {
	if len(runConfigJSON) == 0 {
		return nil
	}
	var raw struct {
		Images []PromptImage `json:"images"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return nil
	}
	out := make([]PromptImage, 0, len(raw.Images))
	for _, image := range raw.Images {
		dataBase64 := strings.TrimSpace(image.DataBase64)
		if dataBase64 == "" {
			continue
		}
		mediaType := strings.TrimSpace(image.MediaType)
		if mediaType == "" {
			mediaType = "image/png"
		}
		out = append(out, PromptImage{
			Name:       strings.TrimSpace(image.Name),
			MediaType:  mediaType,
			DataBase64: dataBase64,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func samePromptImages(a, b []PromptImage) bool {
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

func workflowFromRunConfigJSON(runConfigJSON []byte) string {
	if len(runConfigJSON) == 0 {
		return ""
	}
	var raw struct {
		LibraryTaskName      string `json:"libraryTaskName"`
		LibraryTaskNameLower string `json:"librarytaskname"`
		Skill                string `json:"skill"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return ""
	}
	return firstNonEmpty(raw.LibraryTaskName, raw.LibraryTaskNameLower, raw.Skill)
}

func agentHarnessFromRunConfigJSON(runConfigJSON []byte) string {
	if len(runConfigJSON) == 0 {
		return ""
	}
	var raw struct {
		AgentHarness      string `json:"agentHarness"`
		AgentHarnessSnake string `json:"agent_harness"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return ""
	}
	return firstNonEmpty(raw.AgentHarness, raw.AgentHarnessSnake)
}

func sourceFromRunConfigJSON(runConfigJSON []byte) string {
	if len(runConfigJSON) == 0 {
		return ""
	}
	var raw struct {
		Source             string `json:"source"`
		StartSource        string `json:"startSource"`
		StartSourceSnake   string `json:"start_source"`
		TaskSource         string `json:"taskSource"`
		TaskSourceSnake    string `json:"task_source"`
		RequestSource      string `json:"requestSource"`
		RequestSourceSnake string `json:"request_source"`
	}
	if err := json.Unmarshal(runConfigJSON, &raw); err != nil {
		return ""
	}
	return firstNonEmpty(
		normalizeTaskSource(raw.Source),
		normalizeTaskSource(raw.StartSource),
		normalizeTaskSource(raw.StartSourceSnake),
		normalizeTaskSource(raw.TaskSource),
		normalizeTaskSource(raw.TaskSourceSnake),
		normalizeTaskSource(raw.RequestSource),
		normalizeTaskSource(raw.RequestSourceSnake),
	)
}

func defaultTaskSourceForRequestID(requestID string) string {
	requestID = strings.ToLower(strings.TrimSpace(requestID))
	switch {
	case strings.HasPrefix(requestID, "req-"), strings.HasPrefix(requestID, "hub-"):
		return "hub"
	case strings.HasPrefix(requestID, "local-"):
		return "prompt"
	default:
		return ""
	}
}

func normalizeTaskSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	source = strings.ReplaceAll(source, "-", "_")
	switch source {
	case "chat":
		return "chat"
	case "hub", "hub_dispatch", "moltenhub", "molten_hub":
		return "hub"
	case "prompt", "builder", "studio", "local", "local_submit":
		return "prompt"
	case "library", "library_task", "librarytask":
		return "library"
	case "review", "code_review", "pull_request_review", "pr_review":
		return "review"
	case "json", "raw":
		return "json"
	default:
		return ""
	}
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
	case "completed", "no_changes", "merged", "error", "invalid", "duplicate", "stopped":
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
