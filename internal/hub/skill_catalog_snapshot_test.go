package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jef/moltenhub-code/internal/library"
)

func TestCheckedInSkillCatalogMatchesRuntimeCatalog(t *testing.T) {
	t.Setenv("HARNESS_LIBRARY_DIR", "")
	t.Setenv("HARNESS_AGENTS_SEED_PATH", "")

	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q) error = %v", library.DefaultDir, err)
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	snapshotPath := filepath.Join(repoRoot, "skills", "index.json")
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", snapshotPath, err)
	}

	var got any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", snapshotPath, err)
	}

	want := normalizeJSONForComparison(t, buildRuntimeSkillCatalog(runtimeSkillConfig(), catalog.Summaries()))
	if !jsonEqual(got, want) {
		t.Fatalf("skills/index.json does not match runtime skill catalog\n got: %#v\nwant: %#v", got, want)
	}
}

func normalizeJSONForComparison(t *testing.T, value any) any {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatalf("Unmarshal(normalized) error = %v", err)
	}
	return normalized
}

func jsonEqual(left, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftData) == string(rightData)
}
