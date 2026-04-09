package failurefollowup

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTaskLogDirAndIdentifierValidationBranches(t *testing.T) {
	t.Parallel()

	if got, ok := TaskLogDir("/tmp/logs", " - "); !ok || got != filepath.Join("/tmp/logs", "main") {
		t.Fatalf("TaskLogDir(fallback) = (%q, %v), want fallback path", got, ok)
	}
	if got, ok := TaskLogDir("/tmp/logs", "."); !ok || got != filepath.Join("/tmp/logs", "main") {
		t.Fatalf("TaskLogDir(dot fallback) = (%q, %v), want fallback path", got, ok)
	}

	paths := TaskLogPaths("/tmp/logs", "one---two")
	if len(paths) != 3 || paths[0] != filepath.Join("/tmp/logs", "one", "two") {
		t.Fatalf("TaskLogPaths(collapsed separators) = %#v", paths)
	}
}

func TestNonRemediableRepoAccessReasonFallsBackToEmpty(t *testing.T) {
	t.Parallel()

	if got := NonRemediableRepoAccessReason(nil); got != "" {
		t.Fatalf("NonRemediableRepoAccessReason(nil) = %q, want empty", got)
	}
	if got := NonRemediableRepoAccessReason(errors.New("unrelated failure")); got != "" {
		t.Fatalf("NonRemediableRepoAccessReason(unrelated) = %q, want empty", got)
	}
}
