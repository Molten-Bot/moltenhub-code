package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/failurefollowup"
)

type transientCloneRunner struct{}

func (transientCloneRunner) Run(context.Context, execx.Command) (execx.Result, error) {
	return execx.Result{Stderr: "connection reset by peer"}, errors.New("clone transient failure")
}

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
	if got := stripSkillFrontMatter("---\nname: caveman\n---\nbody"); got != "body" {
		t.Fatalf("stripSkillFrontMatter() = %q, want body", got)
	}
	if got := cavemanIntensityLabel("caveman-wenyan-ultra"); got != "wenyan-ultra" {
		t.Fatalf("cavemanIntensityLabel() = %q, want wenyan-ultra", got)
	}
	if got, err := withResponseModePrompt("ship fix", "default"); err != nil || got != "ship fix" {
		t.Fatalf("withResponseModePrompt(default) = (%q, %v), want original prompt", got, err)
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

func TestHarnessAdditionalHelperBranches(t *testing.T) {
	t.Parallel()

	if got := cloneRetryBranchLabel(" \n\t "); got != "default" {
		t.Fatalf("cloneRetryBranchLabel(blank) = %q, want default", got)
	}
	if got := cloneRetryBranchLabel("release/2026.04-hotfix"); got != "release/2026.04-hotfix" {
		t.Fatalf("cloneRetryBranchLabel(value) = %q, want branch", got)
	}

	if err := commandErrorWithDetails("prefix", nil, execx.Result{}, 120); err != nil {
		t.Fatalf("commandErrorWithDetails(nil err) = %v, want nil", err)
	}
	withDefaultPrefix := commandErrorWithDetails("", errors.New("boom"), execx.Result{}, 120)
	if withDefaultPrefix == nil || !strings.Contains(withDefaultPrefix.Error(), "command failed: boom") {
		t.Fatalf("commandErrorWithDetails(default prefix) = %v", withDefaultPrefix)
	}
	withDetail := commandErrorWithDetails("probe", errors.New("boom"), execx.Result{Stderr: "remote denied"}, 120)
	if withDetail == nil || !strings.Contains(withDetail.Error(), "probe: boom: remote denied") {
		t.Fatalf("commandErrorWithDetails(detail) = %v", withDetail)
	}

	if isNothingToCommitResult(execx.Result{}, nil) {
		t.Fatal("isNothingToCommitResult(nil err) = true, want false")
	}
	if !isNothingToCommitResult(execx.Result{Stderr: "nothing added to commit but untracked files present"}, errors.New("exit status 1")) {
		t.Fatal("isNothingToCommitResult(nothing added marker) = false, want true")
	}

	if got := nonEmptyOrDefault(" \n ", " fallback "); got != "fallback" {
		t.Fatalf("nonEmptyOrDefault(fallback) = %q, want fallback", got)
	}
	if got := pickFirstNonEmpty(" ", "\n"); got != "" {
		t.Fatalf("pickFirstNonEmpty(all empty) = %q, want empty", got)
	}
	if got := truncateForPrompt("hello", 0); got != "hello" {
		t.Fatalf("truncateForPrompt(no limit) = %q, want hello", got)
	}
	if got := truncateForPrompt(" \n\t ", 10); got != "" {
		t.Fatalf("truncateForPrompt(blank) = %q, want empty", got)
	}

	if isNoChecksReported(execx.Result{Stdout: "no checks reported"}, nil) {
		t.Fatal("isNoChecksReported(nil err) = true, want false")
	}
	if isNoRequiredChecksReported(execx.Result{Stdout: "no required checks"}, nil) {
		t.Fatal("isNoRequiredChecksReported(nil err) = true, want false")
	}
	if shouldReconcileChecksAfterFailure(execx.Result{Stdout: "pass/fail"}, nil) {
		t.Fatal("shouldReconcileChecksAfterFailure(nil err) = true, want false")
	}

	if url, ok := existingPRURLFromCreateFailure(execx.Result{Stderr: "pull request already exists"}, errors.New("already exists")); ok || url != "" {
		t.Fatalf("existingPRURLFromCreateFailure(no url) = (%q, %v), want empty,false", url, ok)
	}

	if got := withCompletionGatePrompt(""); !strings.Contains(got, "Improve this repository in a minimal, production-ready way.") {
		t.Fatalf("withCompletionGatePrompt(empty) missing default prompt: %q", got)
	}
	if got := withCompletionGatePrompt("ship fix"); !strings.Contains(got, failurefollowup.ExecutionContract) {
		t.Fatalf("withCompletionGatePrompt() missing execution contract: %q", got)
	}

	cmd := codexCommandWithOptions("/tmp/repo", "ship fix", codexRunOptions{SkipGitRepoCheck: true})
	if got, want := cmd.Name, "codex"; got != want {
		t.Fatalf("codexCommandWithOptions().Name = %q, want %q", got, want)
	}
	if !slicesContains(cmd.Args, "--skip-git-repo-check") {
		t.Fatalf("codexCommandWithOptions().Args = %v, want --skip-git-repo-check", cmd.Args)
	}
}

func TestHarnessRunCloneWithRetryInterrupted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repoDir) error = %v", err)
	}

	h := Harness{
		Runner: transientCloneRunner{},
		Logf:   func(string, ...any) {},
		Sleep: func(context.Context, time.Duration) error {
			return context.Canceled
		},
	}

	_, err := h.runCloneWithRetry(
		context.Background(),
		"git@github.com:acme/repo.git",
		"main",
		repoDir,
		"repo",
		execx.Command{Name: "git", Args: []string{"clone"}},
	)
	if err == nil || !strings.Contains(err.Error(), "clone retry interrupted") {
		t.Fatalf("runCloneWithRetry() error = %v, want interrupted retry error", err)
	}
}

func TestShouldReplaceCheckSnapshotBranches(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)

	if !shouldReplaceCheckSnapshot(latestCheckState{Time: now, Index: 1}, latestCheckState{Time: later, Index: 0}) {
		t.Fatal("shouldReplaceCheckSnapshot(newer time) = false, want true")
	}
	if shouldReplaceCheckSnapshot(latestCheckState{Time: later, Index: 1}, latestCheckState{Time: now, Index: 2}) {
		t.Fatal("shouldReplaceCheckSnapshot(older time) = true, want false")
	}
	if !shouldReplaceCheckSnapshot(latestCheckState{Time: time.Time{}, Index: 1}, latestCheckState{Time: now, Index: 0}) {
		t.Fatal("shouldReplaceCheckSnapshot(prev zero time, candidate non-zero) = false, want true")
	}
	if shouldReplaceCheckSnapshot(latestCheckState{Time: now, Index: 1}, latestCheckState{Time: time.Time{}, Index: 2}) {
		t.Fatal("shouldReplaceCheckSnapshot(prev non-zero, candidate zero time) = true, want false")
	}
	if shouldReplaceCheckSnapshot(latestCheckState{Time: now, Index: 3}, latestCheckState{Time: now, Index: 2}) {
		t.Fatal("shouldReplaceCheckSnapshot(equal time lower index) = true, want false")
	}
	if !shouldReplaceCheckSnapshot(latestCheckState{Time: now, Index: 1}, latestCheckState{Time: now, Index: 2}) {
		t.Fatal("shouldReplaceCheckSnapshot(equal time higher index) = false, want true")
	}
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
