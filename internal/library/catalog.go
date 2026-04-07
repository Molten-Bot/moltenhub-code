package library

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/jef/moltenhub-code/internal/config"
)

const DefaultDir = "library"

// TaskDefinition is one callable library entry loaded from ./library/*.json.
type TaskDefinition struct {
	Name          string   `json:"name"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	TargetSubdir  string   `json:"targetSubdir"`
	Prompt        string   `json:"prompt"`
	CommitMessage string   `json:"commitMessage"`
	PRTitle       string   `json:"prTitle"`
	PRBody        string   `json:"prBody"`
	Labels        []string `json:"labels"`
	GitHubHandle  string   `json:"githubHandle"`
	Reviewers     []string `json:"reviewers"`
}

// UnmarshalJSON supports canonical camelCase keys.
func (t *TaskDefinition) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range raw {
		if _, ok := taskDefinitionFieldNames[key]; ok {
			continue
		}
		return fmt.Errorf("json: unknown field %q", key)
	}

	type taskAlias TaskDefinition
	var parsed taskAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*t = TaskDefinition(parsed)
	return nil
}

// TaskSummary is the public UI/runtime registration view of one library task.
type TaskSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

// Catalog contains all loaded library task definitions.
type Catalog struct {
	Tasks  []TaskDefinition
	byName map[string]TaskDefinition
}

var taskDefinitionFieldNames = map[string]struct{}{
	"name":          {},
	"displayName":   {},
	"description":   {},
	"targetSubdir":  {},
	"prompt":        {},
	"commitMessage": {},
	"prTitle":       {},
	"prBody":        {},
	"labels":        {},
	"githubHandle":  {},
	"reviewers":     {},
}

// LoadCatalog loads and validates library tasks from ./library/*.json.
func LoadCatalog(dir string) (Catalog, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = DefaultDir
	}
	dir = resolveCatalogDir(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return Catalog{}, fmt.Errorf("read library dir: %w", err)
	}

	var jsonFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		jsonFiles = append(jsonFiles, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(jsonFiles)

	catalog := Catalog{byName: map[string]TaskDefinition{}}
	for _, path := range jsonFiles {
		tasks, err := loadTaskDefinitions(path)
		if err != nil {
			return Catalog{}, err
		}
		for _, task := range tasks {
			if _, exists := catalog.byName[task.Name]; exists {
				return Catalog{}, fmt.Errorf("duplicate library task name %q", task.Name)
			}
			catalog.byName[task.Name] = task
			catalog.Tasks = append(catalog.Tasks, task)
		}
	}

	return catalog, nil
}

// Summaries returns stable task metadata for UI and runtime registration.
func (c Catalog) Summaries() []TaskSummary {
	if len(c.Tasks) == 0 {
		return nil
	}
	out := make([]TaskSummary, 0, len(c.Tasks))
	for _, task := range c.Tasks {
		out = append(out, TaskSummary{
			Name:        task.Name,
			DisplayName: task.DisplayName,
			Description: task.Description,
		})
	}
	return out
}

// Names returns the ordered list of callable library task names.
func (c Catalog) Names() []string {
	summaries := c.Summaries()
	if len(summaries) == 0 {
		return nil
	}
	out := make([]string, 0, len(summaries))
	for _, task := range summaries {
		out = append(out, task.Name)
	}
	return out
}

// ExpandRunConfig resolves one library task name into a standard harness run config.
func (c Catalog) ExpandRunConfig(taskName, repo, branch string) (config.Config, error) {
	taskName = strings.TrimSpace(taskName)
	task, ok := c.byName[taskName]
	if !ok {
		return config.Config{}, fmt.Errorf("unknown libraryTaskName %q", taskName)
	}

	repo = strings.TrimSpace(repo)
	if repo == "" {
		return config.Config{}, fmt.Errorf("repo is required for library tasks")
	}

	cfg := config.Config{
		RepoURL:       repo,
		BaseBranch:    strings.TrimSpace(branch),
		TargetSubdir:  task.TargetSubdir,
		Prompt:        task.Prompt,
		CommitMessage: task.CommitMessage,
		PRTitle:       task.PRTitle,
		PRBody:        task.PRBody,
		Labels:        append([]string(nil), task.Labels...),
		GitHubHandle:  task.GitHubHandle,
		Reviewers:     append([]string(nil), task.Reviewers...),
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("library task %q: %w", taskName, err)
	}
	return cfg, nil
}

func loadTaskDefinitions(path string) ([]TaskDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read library task %s: %w", path, err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse library task %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("library task %s: at least one task is required", path)
	}

	if looksLikeSingleTaskDefinition(raw) {
		task, err := decodeTaskDefinition(path, "", data)
		if err != nil {
			return nil, err
		}
		return []TaskDefinition{task}, nil
	}

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	tasks := make([]TaskDefinition, 0, len(keys))
	for _, key := range keys {
		task, err := decodeTaskDefinition(path, key, raw[key])
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func looksLikeSingleTaskDefinition(raw map[string]json.RawMessage) bool {
	for key := range raw {
		if _, ok := taskDefinitionFieldNames[key]; ok {
			return true
		}
	}
	return false
}

func decodeTaskDefinition(path, key string, data []byte) (TaskDefinition, error) {
	var task TaskDefinition
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&task); err != nil {
		if key != "" {
			return TaskDefinition{}, fmt.Errorf("parse library task %s key %q: %w", path, key, err)
		}
		return TaskDefinition{}, fmt.Errorf("parse library task %s: %w", path, err)
	}

	task.Name = strings.TrimSpace(task.Name)
	task.DisplayName = strings.TrimSpace(task.DisplayName)
	task.Description = strings.TrimSpace(task.Description)
	task.TargetSubdir = strings.TrimSpace(task.TargetSubdir)
	task.Prompt = strings.TrimSpace(task.Prompt)
	task.CommitMessage = strings.TrimSpace(task.CommitMessage)
	task.PRTitle = strings.TrimSpace(task.PRTitle)
	task.PRBody = strings.TrimSpace(task.PRBody)
	task.GitHubHandle = strings.TrimSpace(task.GitHubHandle)

	if key != "" {
		key = strings.TrimSpace(key)
		if key == "" {
			return TaskDefinition{}, fmt.Errorf("library task %s: task key is required", path)
		}
		if task.Name == "" {
			task.Name = key
		} else if task.Name != key {
			return TaskDefinition{}, fmt.Errorf("library task %s key %q: name must match key when provided", path, key)
		}
	}

	if task.Name == "" {
		return TaskDefinition{}, fmt.Errorf("library task %s: name is required", path)
	}
	if task.Prompt == "" {
		return TaskDefinition{}, fmt.Errorf("library task %s: prompt is required", path)
	}
	if task.TargetSubdir == "" {
		task.TargetSubdir = "."
	}

	return task, nil
}

func resolveCatalogDir(dir string) string {
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	if isCatalogDir(dir) {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		if path, ok := findDirUpward(wd, dir); ok {
			return path
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		if path, ok := findDirUpward(filepath.Dir(file), dir); ok {
			return path
		}
	}
	if exePath, err := os.Executable(); err == nil {
		if path, ok := findDirUpward(filepath.Dir(exePath), dir); ok {
			return path
		}
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		if path, ok := findDirUpward(filepath.Dir(sourceFile), dir); ok {
			return path
		}
	}
	return dir
}

func findDirUpward(startDir, relPath string) (string, bool) {
	startDir = strings.TrimSpace(startDir)
	relPath = strings.TrimSpace(relPath)
	if startDir == "" || relPath == "" {
		return "", false
	}

	current := filepath.Clean(startDir)
	for {
		candidate := filepath.Join(current, relPath)
		if isCatalogDir(candidate) {
			return candidate, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func isCatalogDir(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			return true
		}
	}
	return false
}
