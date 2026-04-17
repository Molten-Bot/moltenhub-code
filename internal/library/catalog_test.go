package library

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
	t.Parallel()

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
	if got, want := task.PRTitle, "moltenhub-reduce-codebase-centralize-classes"; got != want {
		t.Fatalf("PRTitle = %q, want %q", got, want)
	}
}

func TestDefaultCatalogDoesNotIncludeDeletePromptImagesTask(t *testing.T) {
	t.Parallel()

	catalog, err := LoadCatalog(DefaultDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q) error = %v", DefaultDir, err)
	}

	if _, ok := catalog.byName["delete-prompt-images"]; ok {
		t.Fatalf("default catalog unexpectedly includes %q task", "delete-prompt-images")
	}
}
