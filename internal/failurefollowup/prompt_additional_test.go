package failurefollowup

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWithExecutionContractUsesContractWhenBaseEmpty(t *testing.T) {
	t.Parallel()

	if got := WithExecutionContract(" \n\t "); got != ExecutionContract {
		t.Fatalf("WithExecutionContract(empty) = %q, want execution contract", got)
	}
}

func TestWithExecutionContractDoesNotDuplicateExistingContract(t *testing.T) {
	t.Parallel()

	base := "do the work\n\n" + ExecutionContract
	if got := WithExecutionContract(base); got != base {
		t.Fatalf("WithExecutionContract(existing contract) = %q, want unchanged", got)
	}
}

func TestComposePromptUsesExplicitNoPathGuidance(t *testing.T) {
	t.Parallel()

	got := ComposePrompt(
		"  "+RequiredPrompt+"  ",
		[]string{"", "   "},
		[]string{"", "\t"},
		"  use fallback log directory  ",
		"",
	)
	if !strings.Contains(got, "\n- use fallback log directory") {
		t.Fatalf("ComposePrompt() = %q, want no-path guidance bullet", got)
	}
	if !strings.Contains(got, ExecutionContract) {
		t.Fatalf("ComposePrompt() missing execution contract")
	}
}

func TestComposePromptPrefersPrimaryLogPaths(t *testing.T) {
	t.Parallel()

	got := ComposePrompt(
		RequiredPrompt,
		[]string{" /tmp/logs/task-1 ", " ", "/tmp/logs/task-1/terminal.log"},
		[]string{"/fallback/should-not-appear"},
		"",
		"  extra context  ",
	)
	if strings.Contains(got, "/fallback/should-not-appear") {
		t.Fatalf("ComposePrompt() included fallback paths despite primary paths: %q", got)
	}
	for _, want := range []string{"/tmp/logs/task-1", "/tmp/logs/task-1/terminal.log", "extra context"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ComposePrompt() missing %q: %q", want, got)
		}
	}
}

func TestTaskLogPathsReturnsNilWhenTaskLogDirInvalid(t *testing.T) {
	t.Parallel()

	if got := TaskLogPaths("", "request-id"); got != nil {
		t.Fatalf("TaskLogPaths(empty root) = %v, want nil", got)
	}
	if got := TaskLogPaths("/tmp/log", ""); got != nil {
		t.Fatalf("TaskLogPaths(empty request ID) = %v, want nil", got)
	}
}

func TestTaskLogPathsExcludesAggregateLogToAvoidSelfReferentialPrompts(t *testing.T) {
	t.Parallel()

	root := "/tmp/logs"
	paths := TaskLogPaths(root, "local-1775613327-000024")
	aggregate := filepath.Join(root, LogFileName)
	for _, path := range paths {
		if path == aggregate {
			t.Fatalf("TaskLogPaths() should not include aggregate log path %q; got=%v", aggregate, paths)
		}
	}
}

func TestTaskLogPathsOmitsFallbackMainLogsForLocalRequests(t *testing.T) {
	t.Parallel()

	root := "/tmp/logs"
	paths := TaskLogPaths(root, "local-1775613327-000024")
	for _, path := range paths {
		if strings.Contains(path, filepath.Join(root, FallbackLogSubdir)+string(filepath.Separator)) {
			t.Fatalf("TaskLogPaths(local request) should exclude fallback main logs: %v", paths)
		}
	}
}

func TestShouldIncludeFallbackTaskLogsByRequestType(t *testing.T) {
	t.Parallel()

	if !shouldIncludeFallbackTaskLogs("") {
		t.Fatal("shouldIncludeFallbackTaskLogs(empty request) = false, want true")
	}
	if shouldIncludeFallbackTaskLogs("local-1775613327-000024") {
		t.Fatal("shouldIncludeFallbackTaskLogs(local request) = true, want false")
	}
	if !shouldIncludeFallbackTaskLogs("req-1775613327-000024") {
		t.Fatal("shouldIncludeFallbackTaskLogs(non-local request) = false, want true")
	}
}

func TestTaskLogDirRejectsBlankInputs(t *testing.T) {
	t.Parallel()

	if got, ok := TaskLogDir(" ", "request-1"); ok || got != "" {
		t.Fatalf("TaskLogDir(blank root) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := TaskLogDir("/tmp/log", " "); ok || got != "" {
		t.Fatalf("TaskLogDir(blank request ID) = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestIdentifierSubdirFallbackAndSanitization(t *testing.T) {
	t.Parallel()

	if got, ok := IdentifierSubdir(" -__- "); !ok || got != FallbackLogSubdir {
		t.Fatalf("IdentifierSubdir(fallback) = (%q, %v), want (%q, true)", got, ok, FallbackLogSubdir)
	}

	got, ok := IdentifierSubdir(" req#1 - part@@2 ")
	if !ok {
		t.Fatal("IdentifierSubdir() ok = false, want true")
	}
	want := filepath.Join("req-1", "part-2")
	if got != want {
		t.Fatalf("IdentifierSubdir() = %q, want %q", got, want)
	}
}

func TestSanitizeLogPathPartCoversPunctuationCollapsing(t *testing.T) {
	t.Parallel()

	if got := SanitizeLogPathPart("___...---"); got != "" {
		t.Fatalf("SanitizeLogPathPart(trimmed empty) = %q, want empty", got)
	}
	if got, want := SanitizeLogPathPart("  A@B@@C__D..E--F  "), "A-B-C__D..E--F"; got != want {
		t.Fatalf("SanitizeLogPathPart() = %q, want %q", got, want)
	}
}
