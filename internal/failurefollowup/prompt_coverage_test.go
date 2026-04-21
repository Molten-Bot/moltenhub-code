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
	if len(paths) != 5 || paths[0] != filepath.Join("/tmp/logs", "one", "two") {
		t.Fatalf("TaskLogPaths(collapsed separators) = %#v", paths)
	}
	if paths[3] != filepath.Join("/tmp/logs", FallbackLogSubdir, LegacyTaskLogFileName) {
		t.Fatalf("TaskLogPaths(fallback legacy) = %#v, want fallback legacy path", paths)
	}
	if paths[4] != filepath.Join("/tmp/logs", FallbackLogSubdir, LogFileName) {
		t.Fatalf("TaskLogPaths(fallback current) = %#v, want fallback current path", paths)
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

func TestNonRemediableFailureReasonRecognizesQuotaAndAllowsNoDelta(t *testing.T) {
	t.Parallel()

	if got := NonRemediableFailureReason(errors.New("codex: ERROR: Quota exceeded. Check your plan and billing details.")); got != "quota exceeded" {
		t.Fatalf("NonRemediableFailureReason(quota) = %q, want %q", got, "quota exceeded")
	}
	if got := NonRemediableFailureReason(errors.New("Failure: Cannot fix Studio wide-layout issue in current repo. Target UI source missing.")); got != "target ui source missing" {
		t.Fatalf("NonRemediableFailureReason(target missing) = %q, want %q", got, "target ui source missing")
	}
	if got := NonRemediableFailureReason(errors.New("task failed because this branch has no delta from `main`; No commits between main and moltenhub-fix")); got != "" {
		t.Fatalf("NonRemediableFailureReason(no delta) = %q, want empty", got)
	}
}
