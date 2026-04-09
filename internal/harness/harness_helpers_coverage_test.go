package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
)

func TestHarnessStringAndCheckHelpers(t *testing.T) {
	t.Parallel()

	if got := mergeStreamedOutput("existing\n", ""); got != "existing" {
		t.Fatalf("mergeStreamedOutput(blank captured) = %q", got)
	}
	if got := mergeStreamedOutput("", "captured\n"); got != "captured" {
		t.Fatalf("mergeStreamedOutput(blank existing) = %q", got)
	}
	if got := mergeStreamedOutput("alpha\nbeta", "beta"); got != "alpha\nbeta" {
		t.Fatalf("mergeStreamedOutput(duplicate) = %q", got)
	}
	if got := truncateForPrompt("abcdef", 3); !strings.Contains(got, "...(truncated)") {
		t.Fatalf("truncateForPrompt() = %q, want truncation marker", got)
	}
	if got := nonEmptyOrDefault(" value ", "fallback"); got != "value" {
		t.Fatalf("nonEmptyOrDefault() = %q, want value", got)
	}
	if got := pickFirstNonEmpty(" ", "\n", " value "); got != "value" {
		t.Fatalf("pickFirstNonEmpty() = %q, want value", got)
	}
	if !isNoChecksReported(execx.Result{Stdout: "no checks reported"}, errors.New("boom")) {
		t.Fatal("isNoChecksReported() = false, want true")
	}
	if !isNoRequiredChecksReported(execx.Result{Stderr: "NO REQUIRED CHECKS"}, errors.New("boom")) {
		t.Fatal("isNoRequiredChecksReported() = false, want true")
	}
	if !shouldReconcileChecksAfterFailure(execx.Result{Stdout: "job\tpass\tone\njob\tfail\ttwo"}, errors.New("boom")) {
		t.Fatal("shouldReconcileChecksAfterFailure() = false, want true")
	}
}

func TestHarnessURLCloneAndSandboxHelpers(t *testing.T) {
	t.Parallel()

	url, ok := existingPRURLFromCreateFailure(execx.Result{Stderr: "pull request already exists https://github.com/acme/repo/pull/1"}, errors.New("create failed"))
	if !ok || url != "https://github.com/acme/repo/pull/1" {
		t.Fatalf("existingPRURLFromCreateFailure() = (%q, %v)", url, ok)
	}
	if shouldRetryClone(nil, execx.Result{}) {
		t.Fatal("shouldRetryClone(nil) = true, want false")
	}
	if shouldRetryClone(errors.New("repo not found"), execx.Result{Stderr: "repository not found"}) {
		t.Fatal("shouldRetryClone(repo not found) = true, want false")
	}
	if shouldRetryClone(errors.New("missing branch"), execx.Result{Stderr: "could not find remote branch"}) {
		t.Fatal("shouldRetryClone(missing branch) = true, want false")
	}
	if !shouldRetryClone(errors.New("transient"), execx.Result{Stderr: "connection reset"}) {
		t.Fatal("shouldRetryClone(transient) = false, want true")
	}
	if !shouldFallbackCloneToDefaultBranch("moltenhub-topic", execx.Result{Stderr: "remote branch moltenhub-topic not found"}, errors.New("missing")) {
		t.Fatal("shouldFallbackCloneToDefaultBranch() = false, want true")
	}
	if shouldFallbackCloneToDefaultBranch("release", execx.Result{Stderr: "remote branch release not found"}, errors.New("missing")) {
		t.Fatal("shouldFallbackCloneToDefaultBranch(non-moltenhub) = true, want false")
	}
	if got := overrideCodexSandbox([]string{"exec", "--sandbox", "workspace-write"}, "danger-full-access"); got[2] != "danger-full-access" {
		t.Fatalf("overrideCodexSandbox() = %#v", got)
	}
	if got := overrideCodexSandbox([]string{"exec"}, "danger-full-access"); len(got) != 1 {
		t.Fatalf("overrideCodexSandbox(no flag) = %#v", got)
	}
}

func TestHarnessFilesystemAndPromptHelpers(t *testing.T) {
	t.Parallel()

	cleanup := combineCleanupFns(func() error { return errors.New("one") }, nil, func() error { return errors.New("two") })
	if err := cleanup(); err == nil || !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("combineCleanupFns() error = %v, want joined errors", err)
	}
	if got := promptPathForCodex("/tmp/repo", "/tmp/repo/AGENTS.md"); got != "./AGENTS.md" {
		t.Fatalf("promptPathForCodex(within target) = %q", got)
	}
	if got := promptPathForCodex("/tmp/repo", "/outside/AGENTS.md"); got != "/outside/AGENTS.md" {
		t.Fatalf("promptPathForCodex(outside target) = %q", got)
	}
	if _, err := resolveTargetDir("/tmp/repo", "../escape"); err == nil {
		t.Fatal("resolveTargetDir(escape) error = nil, want non-nil")
	}
	if got, err := resolveTargetDir("/tmp/repo", "./subdir"); err != nil || !strings.HasSuffix(got, "/tmp/repo/subdir") {
		t.Fatalf("resolveTargetDir(valid) = (%q, %v)", got, err)
	}
}

func TestHarnessRuntimeAndCheckSnapshotHelpers(t *testing.T) {
	t.Parallel()

	if len(preflightCommands()) != 3 {
		t.Fatalf("len(preflightCommands()) = %d, want 3", len(preflightCommands()))
	}
	if got := runtimeLogStage(agentruntime.Runtime{Harness: "Claude Code!"}); got != "agent" {
		t.Fatalf("runtimeLogStage(invalid) = %q, want agent", got)
	}
	if got := summarizeCheckOutput(execx.Result{}); got != "No check output was provided by gh." {
		t.Fatalf("summarizeCheckOutput(empty) = %q", got)
	}
	long := strings.Repeat("x", maxCheckSummaryChars+5)
	if got := summarizeCheckOutput(execx.Result{Stdout: long}); !strings.Contains(got, "...(truncated)") {
		t.Fatalf("summarizeCheckOutput(long) = %q", got)
	}
	if ts := checkSnapshotTime(ghPRCheck{StartedAt: "2026-04-09T10:11:12Z"}); ts.IsZero() {
		t.Fatal("checkSnapshotTime(valid startedAt) = zero, want parsed time")
	}
	if !shouldReplaceCheckSnapshot(latestCheckState{Index: 1}, latestCheckState{Index: 2}) {
		t.Fatal("shouldReplaceCheckSnapshot(later index) = false, want true")
	}
	if got := remediationCommitMessage("  chore: test  ", 2); got != "chore: test (ci remediation 2)" {
		t.Fatalf("remediationCommitMessage() = %q", got)
	}
	if got := remediationCommitMessage("", 1); got != "chore: automated update (ci remediation 1)" {
		t.Fatalf("remediationCommitMessage(empty) = %q", got)
	}
}

func TestHarnessContextSleepHelper(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepWithContext(canceled) error = %v, want context.Canceled", err)
	}
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepWithContext(zero) error = %v", err)
	}
}
