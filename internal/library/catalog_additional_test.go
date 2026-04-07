package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandRunConfigErrorsForUnknownTaskAndMissingRepo(t *testing.T) {
	t.Parallel()

	catalog := Catalog{
		byName: map[string]TaskDefinition{
			"known": {Name: "known", Prompt: "do work"},
		},
	}

	if _, err := catalog.ExpandRunConfig("missing", "git@github.com:acme/repo.git", "main"); err == nil {
		t.Fatal("ExpandRunConfig(unknown) error = nil, want non-nil")
	}
	if _, err := catalog.ExpandRunConfig("known", "", "main"); err == nil {
		t.Fatal("ExpandRunConfig(empty repo) error = nil, want non-nil")
	}
}

func TestLoadTaskDefinitionsRejectsEmptyFileObject(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := loadTaskDefinitions(path)
	if err == nil {
		t.Fatal("loadTaskDefinitions(empty) error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "at least one task is required") {
		t.Fatalf("loadTaskDefinitions(empty) error = %v", err)
	}
}

func TestResolveCatalogDirAndHelpersEdgeCases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if got := resolveCatalogDir(dir); got != dir {
		t.Fatalf("resolveCatalogDir(abs) = %q, want %q", got, dir)
	}

	nonCatalog := filepath.Join(dir, "not-catalog")
	if err := os.MkdirAll(nonCatalog, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if isCatalogDir(nonCatalog) {
		t.Fatalf("isCatalogDir(%q) = true, want false", nonCatalog)
	}
	if _, ok := findDirUpward("", "library"); ok {
		t.Fatal("findDirUpward(empty startDir) ok = true, want false")
	}
	if _, ok := findDirUpward(nonCatalog, ""); ok {
		t.Fatal("findDirUpward(empty relPath) ok = true, want false")
	}
}

func TestResolveCatalogDirKeepsExistingRelativeCatalogPath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tmp, err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	rel := "catalog-data"
	catalogDir := filepath.Join(tmp, rel)
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(catalog) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "task.json"), []byte(`{"name":"x","prompt":"p"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(task) error = %v", err)
	}

	if got := resolveCatalogDir(rel); got != rel {
		t.Fatalf("resolveCatalogDir(existing relative) = %q, want %q", got, rel)
	}
}
