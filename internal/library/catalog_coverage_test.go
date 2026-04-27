package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTaskDefinitionUnmarshalJSONRejectsInvalidAndUnknownFields(t *testing.T) {
	t.Parallel()

	var task TaskDefinition
	if err := task.UnmarshalJSON([]byte(`{`)); err == nil {
		t.Fatal("TaskDefinition.UnmarshalJSON(invalid JSON) error = nil, want non-nil")
	}
	if err := json.Unmarshal([]byte(`{"name":"x","unknown":true}`), &task); err == nil {
		t.Fatal("UnmarshalJSON(unknown field) error = nil, want non-nil")
	}
	if err := json.Unmarshal([]byte(`{"name":{}}`), &task); err == nil {
		t.Fatal("UnmarshalJSON(invalid field type) error = nil, want non-nil")
	}
	if err := json.Unmarshal([]byte(`{`), &task); err == nil {
		t.Fatal("UnmarshalJSON(invalid JSON) error = nil, want non-nil")
	}
}

func TestCatalogSummariesAndNamesReturnNilWhenEmpty(t *testing.T) {
	t.Parallel()

	var catalog Catalog
	if got := catalog.Summaries(); got != nil {
		t.Fatalf("Summaries() = %v, want nil", got)
	}
	if got := catalog.Names(); got != nil {
		t.Fatalf("Names() = %v, want nil", got)
	}
}

func TestLoadCatalogSkipsNonJSONAndRejectsDuplicates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("Mkdir(nested) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatalf("WriteFile(notes) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one.json"), []byte(`{"name":"dup","prompt":"a"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(one) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.json"), []byte(`{"name":"dup","prompt":"b"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(two) error = %v", err)
	}

	_, err := LoadCatalog(dir)
	if err == nil || !strings.Contains(err.Error(), `duplicate library task name "dup"`) {
		t.Fatalf("LoadCatalog() error = %v, want duplicate name failure", err)
	}
}

func TestLoadCatalogDefaultAndReadDirErrorPaths(t *testing.T) {
	t.Setenv(catalogDirEnv, "")
	t.Setenv(agentsSeedEnv, "")

	if _, err := LoadCatalog(" "); err != nil {
		t.Fatalf("LoadCatalog(default dir) error = %v", err)
	}
	if _, err := LoadCatalog(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "read library dir") {
		t.Fatalf("LoadCatalog(missing dir) error = %v, want read failure", err)
	}
}

func TestLoadTaskDefinitionsAndDecodeTaskDefinitionValidationPaths(t *testing.T) {
	t.Parallel()

	if _, err := loadTaskDefinitions(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("loadTaskDefinitions(missing file) error = nil, want non-nil")
	}

	dir := t.TempDir()
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte(`{"task":`), 0o644); err != nil {
		t.Fatalf("WriteFile(badJSON) error = %v", err)
	}
	if _, err := loadTaskDefinitions(badJSON); err == nil {
		t.Fatal("loadTaskDefinitions(bad JSON) error = nil, want non-nil")
	}

	if _, err := decodeTaskDefinition("tasks.json", " \t ", []byte(`{"prompt":"x"}`)); err == nil {
		t.Fatal("decodeTaskDefinition(blank key) error = nil, want non-nil")
	}
	if _, err := decodeTaskDefinition("tasks.json", "", []byte(`{"name":"x"}`)); err == nil {
		t.Fatal("decodeTaskDefinition(missing prompt) error = nil, want non-nil")
	}
	if _, err := decodeTaskDefinition("tasks.json", "", []byte(`{"prompt":"x"}`)); err == nil {
		t.Fatal("decodeTaskDefinition(missing name) error = nil, want non-nil")
	}

	task, err := decodeTaskDefinition("tasks.json", "task-name", []byte(`{
		"displayName":" Display ",
		"description":" Desc ",
		"targetSubdir":"  ",
		"prompt":" Prompt ",
		"commitMessage":" Commit ",
		"prTitle":" Title ",
		"prBody":" Body ",
		"labels":["x"],
		"githubHandle":" @octocat ",
		"reviewers":["octocat"]
	}`))
	if err != nil {
		t.Fatalf("decodeTaskDefinition(valid) error = %v", err)
	}
	if got, want := task.Name, "task-name"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := task.TargetSubdir, "."; got != want {
		t.Fatalf("TargetSubdir = %q, want %q", got, want)
	}
	if got, want := task.GitHubHandle, "@octocat"; got != want {
		t.Fatalf("GitHubHandle = %q, want %q", got, want)
	}
}

func TestResolveCatalogHelpersCoverFallbackBranches(t *testing.T) {
	if got := resolveCatalogDir(""); got != "" {
		t.Fatalf("resolveCatalogDir(\"\") = %q, want empty", got)
	}

	root := t.TempDir()
	catalogDir := filepath.Join(root, "library")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(catalogDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "task.json"), []byte(`{"name":"x","prompt":"p"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(task) error = %v", err)
	}

	if got := catalogDirFromHint(filepath.Join(catalogDir, "missing.json")); got != catalogDir {
		t.Fatalf("catalogDirFromHint(missing file path) = %q, want %q", got, catalogDir)
	}
	if got := catalogDirFromHint(" "); got != "" {
		t.Fatalf("catalogDirFromHint(empty) = %q, want empty", got)
	}
	if got := catalogDirFromHint(root); got != "" {
		t.Fatalf("catalogDirFromHint(non-catalog dir) = %q, want empty", got)
	}
	plainFile := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(plainFile, []byte("plain"), 0o644); err != nil {
		t.Fatalf("WriteFile(plain) error = %v", err)
	}
	if got := catalogDirFromHint(plainFile); got != "" {
		t.Fatalf("catalogDirFromHint(non-catalog file) = %q, want empty", got)
	}
	if got := catalogDirFromHint(filepath.Join(root, "missing")); got != "" {
		t.Fatalf("catalogDirFromHint(non-catalog missing path) = %q, want empty", got)
	}
	if got := resolveCatalogDir("definitely-not-a-library-catalog-for-test"); got != "definitely-not-a-library-catalog-for-test" {
		t.Fatalf("resolveCatalogDir(missing rel) = %q, want original rel", got)
	}

	t.Setenv(catalogDirEnv, filepath.Join(root, "missing"))
	t.Setenv(agentsSeedEnv, filepath.Join(catalogDir, "AGENTS.md"))
	if err := os.WriteFile(filepath.Join(catalogDir, "AGENTS.md"), []byte("# seed"), 0o644); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
	}
	if got := catalogDirFromEnv(); got != catalogDir {
		t.Fatalf("catalogDirFromEnv() = %q, want %q", got, catalogDir)
	}

	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}
	if got, ok := findDirUpward(nested, "library"); !ok || got != catalogDir {
		t.Fatalf("findDirUpward() = (%q, %v), want (%q, true)", got, ok, catalogDir)
	}
}

func TestCatalogExpandAndOrderingErrorBranches(t *testing.T) {
	t.Parallel()

	catalog := Catalog{byName: map[string]TaskDefinition{
		"bad": {Name: "bad", TargetSubdir: "../escape", Prompt: "run task"},
	}}
	if _, err := catalog.ExpandRunConfig("bad", "git@github.com:acme/repo.git", "main"); err == nil || !strings.Contains(err.Error(), `library task "bad"`) {
		t.Fatalf("ExpandRunConfig(invalid task config) error = %v, want wrapped validation error", err)
	}
	if got := OrderSummariesByUsage(nil, map[string]int{"x": 1}); got != nil {
		t.Fatalf("OrderSummariesByUsage(nil) = %#v, want nil", got)
	}
	in := []TaskSummary{{Name: "a"}, {Name: "b"}}
	got := OrderSummariesByUsage(in, nil)
	if len(got) != len(in) || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("OrderSummariesByUsage(no usage) = %#v, want original order", got)
	}
}

func TestIsCatalogDirReturnsFalseWhenReadDirFails(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chmod unreadable directory behavior differs on Windows")
	}

	dir := filepath.Join(t.TempDir(), "unreadable")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir(unreadable) error = %v", err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatalf("Chmod(unreadable) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if isCatalogDir(dir) {
		t.Fatalf("isCatalogDir(%q) = true, want false", dir)
	}
}
