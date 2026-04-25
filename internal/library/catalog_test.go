package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDefaultLibraryJSONFilesHaveCanonicalShape(t *testing.T) {
	t.Setenv(catalogDirEnv, "")
	t.Setenv(agentsSeedEnv, "")

	dir := resolveCatalogDir(DefaultDir)
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob library json files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no library json files found in %q", dir)
	}

	wantFields := []string{"commitMessage", "description", "displayName", "prTitle", "prompt", "targetSubdir"}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read file: %v", path, err)
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Errorf("%s: invalid JSON: %v", path, err)
			continue
		}
		if len(raw) != 1 {
			t.Errorf("%s: got %d top-level tasks, want 1", path, len(raw))
			continue
		}

		var taskName string
		var taskData json.RawMessage
		for name, data := range raw {
			taskName = name
			taskData = data
		}
		wantName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if taskName != wantName {
			t.Errorf("%s: top-level task key = %q, want %q", path, taskName, wantName)
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(taskData, &fields); err != nil {
			t.Errorf("%s: task %q must be a JSON object: %v", path, taskName, err)
			continue
		}
		if gotFields := sortedKeys(fields); !reflect.DeepEqual(gotFields, wantFields) {
			t.Errorf("%s: task fields = %v, want %v", path, gotFields, wantFields)
			continue
		}

		for _, field := range wantFields {
			var value string
			if err := json.Unmarshal(fields[field], &value); err != nil {
				t.Errorf("%s: field %q must be a string: %v", path, field, err)
				continue
			}
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: field %q must not be empty", path, field)
			}
		}
	}
}

func TestLoadCatalogReadsJSONTasks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := `{
  "security-review": {
    "displayName": "Security Review",
    "description": "Audit security boundaries.",
    "targetSubdir": ".",
    "prompt": "Review the repository."
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "security-review.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	catalog, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if got, want := len(catalog.Tasks), 1; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if got, want := catalog.Tasks[0].Name, "security-review"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := catalog.Tasks[0].DisplayName, "Security Review"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := catalog.Tasks[0].TargetSubdir, "."; got != want {
		t.Fatalf("TargetSubdir = %q, want %q", got, want)
	}
	if got, want := catalog.Names(), []string{"security-review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got, want := catalog.Tasks[0].DisplayName, "Security Review"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	summaries := catalog.Summaries()
	if got, want := len(summaries), 1; got != want {
		t.Fatalf("len(Summaries()) = %d, want %d", got, want)
	}
	if got, want := summaries[0].Prompt, "Review the repository."; got != want {
		t.Fatalf("Summaries()[0].Prompt = %q, want %q", got, want)
	}
}

func sortedKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestLoadCatalogSupportsMultipleKeyedTasksInOneFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := `{
  "security-review": {
    "description": "Audit security boundaries.",
    "prompt": "Review the repository."
  },
  "unit-test-coverage": {
    "targetSubdir": ".",
    "prompt": "Raise coverage."
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	catalog, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if got, want := catalog.Names(), []string{"security-review", "unit-test-coverage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got, want := catalog.Tasks[0].TargetSubdir, "."; got != want {
		t.Fatalf("TargetSubdir = %q, want %q", got, want)
	}
}

func TestLoadCatalogSupportsSingleTaskShape(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := `{
  "name": "security-review",
  "displayName": "Security Review",
  "description": "Audit security boundaries.",
  "targetSubdir": ".",
  "prompt": "Review the repository."
}`
	if err := os.WriteFile(filepath.Join(dir, "security-review.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	catalog, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if got, want := catalog.Names(), []string{"security-review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestLoadCatalogRejectsSnakeCaseTaskFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := `{
  "name": "security-review",
  "target_subdir": ".",
  "prompt": "Review the repository."
}`
	if err := os.WriteFile(filepath.Join(dir, "security-review.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	_, err := LoadCatalog(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCatalogRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := `{
  "broken-task": {
    "repos": ["git@github.com:acme/repo.git"],
    "prompt": "x"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	_, err := LoadCatalog(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCatalogRejectsMismatchedInlineNameForKeyedTask(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := `{
  "security-review": {
    "name": "wrong-name",
    "prompt": "Review the repository."
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	_, err := LoadCatalog(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "name must match key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExpandRunConfigUsesRepoAndBranchInputs(t *testing.T) {
	t.Parallel()

	catalog := Catalog{
		Tasks: []TaskDefinition{{
			Name:         "unit-test-coverage",
			TargetSubdir: ".",
			Prompt:       "Raise coverage.",
		}},
		byName: map[string]TaskDefinition{
			"unit-test-coverage": {
				Name:         "unit-test-coverage",
				TargetSubdir: ".",
				Prompt:       "Raise coverage.",
			},
		},
	}

	cfg, err := catalog.ExpandRunConfig("unit-test-coverage", "git@github.com:acme/repo.git", "release")
	if err != nil {
		t.Fatalf("ExpandRunConfig() error = %v", err)
	}
	if got, want := cfg.RepoURL, "git@github.com:acme/repo.git"; got != want {
		t.Fatalf("RepoURL = %q, want %q", got, want)
	}
	if got, want := cfg.LibraryTaskName, "unit-test-coverage"; got != want {
		t.Fatalf("LibraryTaskName = %q, want %q", got, want)
	}
	if got, want := cfg.BaseBranch, "release"; got != want {
		t.Fatalf("BaseBranch = %q, want %q", got, want)
	}
	if got, want := cfg.Prompt, "Raise coverage."; got != want {
		t.Fatalf("Prompt = %q, want %q", got, want)
	}
}

func TestOrderSummariesByUsageSortsDescendingAndPreservesTies(t *testing.T) {
	t.Parallel()

	summaries := []TaskSummary{
		{Name: "alpha", DisplayName: "Alpha"},
		{Name: "beta", DisplayName: "Beta"},
		{Name: "gamma", DisplayName: "Gamma"},
		{Name: "delta", DisplayName: "Delta"},
	}

	got := OrderSummariesByUsage(summaries, map[string]int{
		"gamma": 4,
		"alpha": 2,
		"beta":  2,
	})

	want := []string{"gamma", "alpha", "beta", "delta"}
	gotNames := make([]string, 0, len(got))
	for _, summary := range got {
		gotNames = append(gotNames, summary.Name)
	}
	if !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("OrderSummariesByUsage() = %v, want %v", gotNames, want)
	}
}

func TestResolveCatalogDirFallsBackToSourceTreeWhenWorkingDirChanges(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tmpDir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	resolved := resolveCatalogDir(DefaultDir)
	if !isCatalogDir(resolved) {
		t.Fatalf("resolveCatalogDir(%q) = %q, want existing catalog dir", DefaultDir, resolved)
	}
}

func TestDefaultCatalogIncludesReduceCodebaseCentralizeClassesTask(t *testing.T) {
	t.Setenv(catalogDirEnv, "")
	t.Setenv(agentsSeedEnv, "")

	catalog, err := LoadCatalog(DefaultDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q) error = %v", DefaultDir, err)
	}

	task, ok := catalog.byName["reduce-codebase-centralize-classes"]
	if !ok {
		t.Fatalf("default catalog missing %q task", "reduce-codebase-centralize-classes")
	}
	if !strings.Contains(strings.ToLower(task.Prompt), "reduce the codebase") {
		t.Fatalf("prompt = %q, want reduce-codebase guidance", task.Prompt)
	}
	if !strings.Contains(strings.ToLower(task.Prompt), "centralize duplicated classes") {
		t.Fatalf("prompt = %q, want class centralization guidance", task.Prompt)
	}
	if !strings.Contains(strings.ToLower(task.Prompt), "avoid regressions") {
		t.Fatalf("prompt = %q, want regression-prevention guidance", task.Prompt)
	}
	if got, want := task.PRTitle, "Molten Hub Code: reduce-codebase-centralize-classes"; got != want {
		t.Fatalf("PRTitle = %q, want %q", got, want)
	}
}

func TestDefaultCatalogIncludesFixPRCITestsTask(t *testing.T) {
	t.Setenv(catalogDirEnv, "")
	t.Setenv(agentsSeedEnv, "")

	catalog, err := LoadCatalog(DefaultDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q) error = %v", DefaultDir, err)
	}

	task, ok := catalog.byName["fix-pr-ci-tests"]
	if !ok {
		t.Fatalf("default catalog missing %q task", "fix-pr-ci-tests")
	}
	prompt := strings.ToLower(task.Prompt)
	for _, want := range []string{
		"check pr ci status",
		"do not remove, skip, weaken, or rename test cases unless the tested functionality has been intentionally removed",
		"test coverage must go up when practical and must never go down",
		"`failure:`",
		"`error details:`",
		"do not fail solely for that",
		"repository is not initialized after clone",
		"do not stop work just because you cannot create a pull request or watch remote ci/cd",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want %q guidance", task.Prompt, want)
		}
	}
	if got, want := task.PRTitle, "Molten Hub Code: fix-pr-ci-tests"; got != want {
		t.Fatalf("PRTitle = %q, want %q", got, want)
	}
}

func TestDefaultCatalogDoesNotIncludeDeletePromptImagesTask(t *testing.T) {
	t.Setenv(catalogDirEnv, "")
	t.Setenv(agentsSeedEnv, "")

	catalog, err := LoadCatalog(DefaultDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q) error = %v", DefaultDir, err)
	}

	if _, ok := catalog.byName["delete-prompt-images"]; ok {
		t.Fatalf("default catalog unexpectedly includes %q task", "delete-prompt-images")
	}
}
