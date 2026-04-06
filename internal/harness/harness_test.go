package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/workspace"
)

type expectedRun struct {
	cmd execx.Command
	res execx.Result
	err error
}

type fakeRunner struct {
	t     *testing.T
	exps  []expectedRun
	calls []execx.Command
}

func (f *fakeRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	f.t.Helper()
	if len(f.exps) == 0 {
		f.t.Fatalf("unexpected command: %+v", cmd)
	}
	exp := f.exps[0]
	f.exps = f.exps[1:]
	f.calls = append(f.calls, cmd)

	if exp.cmd.Name != cmd.Name || exp.cmd.Dir != cmd.Dir || !reflect.DeepEqual(exp.cmd.Args, cmd.Args) {
		f.t.Fatalf("command mismatch\n got:  %+v\n want: %+v", cmd, exp.cmd)
	}
	return exp.res, exp.err
}

type captureRunner struct {
	cmd execx.Command
}

func (c *captureRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	c.cmd = cmd
	return execx.Result{}, nil
}

func sampleConfig() config.Config {
	return config.Config{
		Version:       "v1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  "services/api",
		Prompt:        "Build API",
		CommitMessage: "feat: automate api",
		PRTitle:       "moltenhub-feat: automate api",
		PRBody:        "Automated by MoltenHub Code\n\nIf you would like to connect agents together checkout [Molten Bot Hub](https://molten.bot/hub).",
		Labels:        []string{"automation", ""},
		Reviewers:     []string{"octocat", ""},
	}
}

func testWorkspaceManager(guid string) workspace.Manager {
	return workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
		ReadFile: func(string) ([]byte, error) {
			return []byte("seeded agents instructions"), nil
		},
		WriteFile: func(string, []byte, os.FileMode) error { return nil },
	}
}

func TestRunHappyPathCreatesPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo/pull/42\n"}},
		{cmd: prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if res.PRURL != "https://github.com/acme/repo/pull/42" {
		t.Fatalf("PRURL = %q", res.PRURL)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunWithGitHubTokenRunsAuthSetupGitBeforeCodex(t *testing.T) {
	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"

	t.Setenv("GITHUB_TOKEN", "ghp_example_token")
	t.Setenv("GH_TOKEN", "")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "setup-git"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo/pull/42\n"}},
		{cmd: prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNonMainBranchReusesExistingBranchAndPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	prURL := "https://github.com/acme/repo/pull/77"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if got, want := res.Branch, cfg.BaseBranch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNonMainBranchPushNonFastForwardRetriesWithRebase(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	prURL := "https://github.com/acme/repo/pull/77"

	pushRejected := execx.Result{
		Stderr: "! [rejected]        release/2026.04-hotfix -> release/2026.04-hotfix (fetch first)\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: pullRebaseCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if got, want := res.Branch, cfg.BaseBranch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunCodexFailureStopsBeforeCommitAndPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)), err: errors.New("codex failed")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("expected error, got nil")
	}
	if res.ExitCode != ExitCodex {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitCodex)
	}
	if !strings.Contains(res.Err.Error(), "codex") {
		t.Fatalf("error = %v", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesSkipsPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if !res.NoChanges {
		t.Fatal("NoChanges = false, want true")
	}
	if res.PRURL != "" {
		t.Fatalf("PRURL = %q, want empty", res.PRURL)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunFailedChecksTriggersCodexRemediation(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"

	checkSummary := "X unit-tests failing"
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stdout: checkSummary + "\n"}, err: errors.New("checks failed")},
		{cmd: codexCommand(targetDir, remediationPrompt(withAgentsPrompt(cfg.Prompt, agentsPath), prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, remediationCommitMessage(cfg.CommitMessage, 1))},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if res.PRURL != prURL {
		t.Fatalf("PRURL = %q, want %q", res.PRURL, prURL)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunFailedChecksWithNoRemediationChangesFails(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"

	checkSummary := "X unit-tests failing"
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stdout: checkSummary + "\n"}, err: errors.New("checks failed")},
		{cmd: codexCommand(targetDir, remediationPrompt(withAgentsPrompt(cfg.Prompt, agentsPath), prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("expected error, got nil")
	}
	if res.ExitCode != ExitPR {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitPR)
	}
	if !strings.Contains(res.Err.Error(), "no remediation changes") {
		t.Fatalf("error = %v", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChecksReportedRetriesBeforePassing(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'moltenhub-build-api-20260402-150405-abcdef12' branch"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: workflowDispatchCommand(repoDir, branch)},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	sleepCalls := 0
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Sleep = func(_ context.Context, d time.Duration) error {
		sleepCalls++
		if d != prChecksNoReportRetryDelay {
			t.Fatalf("sleep delay = %s, want %s", d, prChecksNoReportRetryDelay)
		}
		return nil
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if sleepCalls != 2 {
		t.Fatalf("sleepCalls = %d, want 2", sleepCalls)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChecksReportedAfterRetryWindowTriggersRemediation(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'moltenhub-build-api-20260402-150405-abcdef12' branch"

	exps := []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: workflowDispatchCommand(repoDir, branch)},
	}
	for i := 1; i <= maxPRChecksNoReportRetries; i++ {
		exps = append(exps, expectedRun{
			cmd: prChecksCommand(repoDir, prURL),
			res: execx.Result{Stderr: noChecks + "\n"},
			err: errors.New("checks unavailable"),
		})
	}
	exps = append(exps,
		expectedRun{cmd: codexCommand(targetDir, remediationPrompt(withAgentsPrompt(cfg.Prompt, agentsPath), prURL, noChecks, 1))},
		expectedRun{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		expectedRun{cmd: addCommand(repoDir)},
		expectedRun{cmd: commitCommand(repoDir, remediationCommitMessage(cfg.CommitMessage, 1))},
		expectedRun{cmd: pushCommand(repoDir, branch)},
		expectedRun{cmd: prChecksCommand(repoDir, prURL)},
	)

	fake := &fakeRunner{t: t, exps: exps}
	sleepCalls := 0

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Sleep = func(_ context.Context, d time.Duration) error {
		sleepCalls++
		if d != prChecksNoReportRetryDelay {
			t.Fatalf("sleep delay = %s, want %s", d, prChecksNoReportRetryDelay)
		}
		return nil
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if sleepCalls != maxPRChecksNoReportRetries {
		t.Fatalf("sleepCalls = %d, want %d", sleepCalls, maxPRChecksNoReportRetries)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoRequiredChecksFallsBackToAllChecks(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	noRequired := "no required checks reported on the 'moltenhub-build-api-20260402-150405-abcdef12' branch"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noRequired + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prChecksAnyCommand(repoDir, prURL)},
	}}

	sleepCalls := 0
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Sleep = func(_ context.Context, _ time.Duration) error {
		sleepCalls++
		return nil
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if sleepCalls != 0 {
		t.Fatalf("sleepCalls = %d, want 0", sleepCalls)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunMultiRepoCreatesPRsForEachChangedRepo(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.RepoURL = ""
	cfg.Repo = ""
	cfg.Repos = []string{
		"git@github.com:acme/repo-a.git",
		"git@github.com:acme/repo-b.git",
	}
	cfg.TargetSubdir = "."

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	branch := "moltenhub-build-api-20260402-150405-abcdef12"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	codexPrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, []repoWorkspace{
		{URL: cfg.Repos[0], RelDir: repoRelA},
		{URL: cfg.Repos[1], RelDir: repoRelB},
	})
	codexPrompt = withAgentsPrompt(codexPrompt, agentsPath)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: codexCommandWithOptions(runDir, codexPrompt, codexRunOptions{SkipGitRepoCheck: true})},
		{cmd: statusCommand(repoDirA), res: execx.Result{Stdout: " M file-a.go\n"}},
		{cmd: statusCommand(repoDirB), res: execx.Result{Stdout: " M file-b.go\n"}},
		{cmd: addCommand(repoDirA)},
		{cmd: commitCommand(repoDirA, cfg.CommitMessage)},
		{cmd: pushCommand(repoDirA, branch)},
		{cmd: prCreateCommand(repoDirA, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo-a/pull/10\n"}},
		{cmd: prChecksCommand(repoDirA, "https://github.com/acme/repo-a/pull/10")},
		{cmd: addCommand(repoDirB)},
		{cmd: commitCommand(repoDirB, cfg.CommitMessage)},
		{cmd: pushCommand(repoDirB, branch)},
		{cmd: prCreateCommand(repoDirB, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo-b/pull/20\n"}},
		{cmd: prChecksCommand(repoDirB, "https://github.com/acme/repo-b/pull/20")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == repoDirA }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if got, want := len(res.RepoResults), 2; got != want {
		t.Fatalf("len(RepoResults) = %d, want %d", got, want)
	}
	if res.RepoResults[0].PRURL == "" || res.RepoResults[1].PRURL == "" {
		t.Fatalf("RepoResults PRs = %#v", res.RepoResults)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunMultiRepoRemediationUsesWorkspaceCodexOptions(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.RepoURL = ""
	cfg.Repo = ""
	cfg.Repos = []string{
		"git@github.com:acme/repo-a.git",
		"git@github.com:acme/repo-b.git",
	}
	cfg.TargetSubdir = "."

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	branch := "moltenhub-build-api-20260402-150405-abcdef12"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	codexPrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, []repoWorkspace{
		{URL: cfg.Repos[0], RelDir: repoRelA},
		{URL: cfg.Repos[1], RelDir: repoRelB},
	})
	codexPrompt = withAgentsPrompt(codexPrompt, agentsPath)
	prURL := "https://github.com/acme/repo-a/pull/99"
	checkSummary := "X integration-tests failing"
	repairPrompt := remediationPromptForRepo(codexPrompt, repoRelA, cfg.Repos[0], prURL, checkSummary, 1, true)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: codexCommandWithOptions(runDir, codexPrompt, codexRunOptions{SkipGitRepoCheck: true})},
		{cmd: statusCommand(repoDirA), res: execx.Result{Stdout: " M file-a.go\n"}},
		{cmd: statusCommand(repoDirB), res: execx.Result{Stdout: "\n"}},
		{cmd: addCommand(repoDirA)},
		{cmd: commitCommand(repoDirA, cfg.CommitMessage)},
		{cmd: pushCommand(repoDirA, branch)},
		{cmd: prCreateCommand(repoDirA, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDirA, prURL), res: execx.Result{Stdout: checkSummary + "\n"}, err: errors.New("checks failed")},
		{cmd: codexCommandWithOptions(runDir, repairPrompt, codexRunOptions{SkipGitRepoCheck: true})},
		{cmd: statusCommand(repoDirA), res: execx.Result{Stdout: " M file-a.go\n"}},
		{cmd: addCommand(repoDirA)},
		{cmd: commitCommand(repoDirA, remediationCommitMessage(cfg.CommitMessage, 1))},
		{cmd: pushCommand(repoDirA, branch)},
		{cmd: prChecksCommand(repoDirA, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == repoDirA }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNonMainBranchReusesExistingPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/fix-ci"

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	prURL := "https://github.com/acme/repo/pull/77"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "[{\"url\":\"" + prURL + "\"}]\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if got, want := res.Branch, cfg.BaseBranch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNonMainBranchCreatesPRWithoutExplicitBaseWhenNoOpenPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/fix-ci"

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	prURL := "https://github.com/acme/repo/pull/88"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "[]\n"}},
		{cmd: prCreateWithoutBaseCommand(repoDir, cfg, cfg.BaseBranch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunMissingMoltenhubBaseBranchFallsBackToDefaultAndCreatesNewBranch(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "moltenhub-the-top-left-should-show-our-logo-https-20260406-192020-bf8c1ade"

	now := time.Date(2026, 4, 6, 19, 53, 52, 0, time.UTC)
	guid := "9ded650b29c70708825082be50fbf433"
	runDir := filepath.Join("/tmp", "temp", guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api-20260406-195352-9ded650b"
	prURL := "https://github.com/acme/repo/pull/112"

	cloneMissingBranch := execx.Result{
		Stderr: "warning: Could not find remote branch moltenhub-the-top-left-should-show-our-logo-https-20260406-192020-bf8c1ade to clone.\n" +
			"fatal: Remote branch moltenhub-the-top-left-should-show-our-logo-https-20260406-192020-bf8c1ade not found in upstream origin\n",
	}
	cfgMain := cfg
	cfgMain.BaseBranch = "main"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir), res: cloneMissingBranch, err: errors.New("clone failed")},
		{cmd: cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfgMain, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if got, want := res.Branch, branch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunMissingNonMoltenhubBaseBranchFailsClone(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", "temp", guid)
	repoDir := filepath.Join(runDir, "repo")

	cloneMissingBranch := execx.Result{
		Stderr: "warning: Could not find remote branch release/2026.04-hotfix to clone.\n" +
			"fatal: Remote branch release/2026.04-hotfix not found in upstream origin\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir), res: cloneMissingBranch, err: errors.New("clone failed")},
	}}

	h := New(fake)
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == filepath.Join(repoDir, cfg.TargetSubdir) }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want clone failure")
	}
	if res.ExitCode != ExitClone {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitClone)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestCommandBuilders(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	repoDir := "/tmp/run/repo"
	branch := "moltenhub-build-api-20260402-150405-abcdef12"
	prompt := "fix tests"
	targetDir := filepath.Join(repoDir, "services/api")

	clone := cloneCommand(cfg, repoDir)
	if clone.Name != "git" || !reflect.DeepEqual(clone.Args, []string{"clone", "--branch", "main", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone command unexpected: %+v", clone)
	}
	fetchMain := fetchMainBranchCommand(repoDir)
	if fetchMain.Name != "git" || fetchMain.Dir != repoDir || !reflect.DeepEqual(fetchMain.Args, []string{"fetch", "origin", "main:refs/remotes/origin/main"}) {
		t.Fatalf("fetch main command unexpected: %+v", fetchMain)
	}
	cloneDefault := cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)
	if cloneDefault.Name != "git" || !reflect.DeepEqual(cloneDefault.Args, []string{"clone", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone default command unexpected: %+v", cloneDefault)
	}
	authStatus := authCommand()
	if authStatus.Name != "gh" || !reflect.DeepEqual(authStatus.Args, []string{"auth", "status"}) {
		t.Fatalf("auth status command unexpected: %+v", authStatus)
	}
	authSetup := authSetupGitCommand()
	if authSetup.Name != "gh" || !reflect.DeepEqual(authSetup.Args, []string{"auth", "setup-git"}) {
		t.Fatalf("auth setup-git command unexpected: %+v", authSetup)
	}

	codex := codexCommand(targetDir, prompt)
	if codex.Name != "codex" || codex.Dir != targetDir || !reflect.DeepEqual(codex.Args, []string{"exec", "--sandbox", "workspace-write", withCompletionGatePrompt(prompt)}) {
		t.Fatalf("codex command unexpected: %+v", codex)
	}
	codexWorkspace := codexCommandWithOptions(targetDir, prompt, codexRunOptions{SkipGitRepoCheck: true})
	if codexWorkspace.Name != "codex" || codexWorkspace.Dir != targetDir || !reflect.DeepEqual(codexWorkspace.Args, []string{"exec", "--sandbox", "workspace-write", "--skip-git-repo-check", withCompletionGatePrompt(prompt)}) {
		t.Fatalf("codex workspace command unexpected: %+v", codexWorkspace)
	}
	codexWithImages := codexCommandWithOptions(targetDir, prompt, codexRunOptions{
		SkipGitRepoCheck: true,
		ImagePaths:       []string{"/tmp/run/prompt-images/01-shot.png", "/tmp/run/prompt-images/02-shot.png"},
	})
	if codexWithImages.Name != "codex" || codexWithImages.Dir != targetDir || !reflect.DeepEqual(codexWithImages.Args, []string{
		"exec",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
		"--image", "/tmp/run/prompt-images/01-shot.png",
		"--image", "/tmp/run/prompt-images/02-shot.png",
		withCompletionGatePrompt(prompt),
	}) {
		t.Fatalf("codex image command unexpected: %+v", codexWithImages)
	}

	pr := prCreateCommand(repoDir, cfg, branch)
	wantPrefix := []string{"pr", "create", "--base", "main", "--head", branch, "--title", cfg.PRTitle, "--body", cfg.PRBody}
	if pr.Name != "gh" || pr.Dir != repoDir {
		t.Fatalf("pr command unexpected: %+v", pr)
	}
	if !reflect.DeepEqual(pr.Args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("pr command prefix unexpected: %v", pr.Args)
	}
	if !containsSequence(pr.Args, []string{"--label", "automation"}) {
		t.Fatalf("pr args missing label: %v", pr.Args)
	}
	if !containsSequence(pr.Args, []string{"--reviewer", "octocat"}) {
		t.Fatalf("pr args missing reviewer: %v", pr.Args)
	}

	prNoBase := prCreateWithoutBaseCommand(repoDir, cfg, branch)
	wantNoBasePrefix := []string{"pr", "create", "--head", branch, "--title", cfg.PRTitle, "--body", cfg.PRBody}
	if prNoBase.Name != "gh" || prNoBase.Dir != repoDir {
		t.Fatalf("pr without base command unexpected: %+v", prNoBase)
	}
	if !reflect.DeepEqual(prNoBase.Args[:len(wantNoBasePrefix)], wantNoBasePrefix) {
		t.Fatalf("pr without base command prefix unexpected: %v", prNoBase.Args)
	}
	if containsSequence(prNoBase.Args, []string{"--base", "main"}) {
		t.Fatalf("pr without base should not include --base: %v", prNoBase.Args)
	}
	if !containsSequence(prNoBase.Args, []string{"--label", "automation"}) {
		t.Fatalf("pr without base args missing label: %v", prNoBase.Args)
	}
	if !containsSequence(prNoBase.Args, []string{"--reviewer", "octocat"}) {
		t.Fatalf("pr without base args missing reviewer: %v", prNoBase.Args)
	}

	prLookup := prLookupByHeadCommand(repoDir, branch)
	wantLookup := []string{"pr", "list", "--state", "open", "--head", branch, "--json", "url", "--limit", "1"}
	if prLookup.Name != "gh" || prLookup.Dir != repoDir || !reflect.DeepEqual(prLookup.Args, wantLookup) {
		t.Fatalf("pr lookup command unexpected: %+v", prLookup)
	}

	if !shouldCreateWorkBranch("main") {
		t.Fatal("shouldCreateWorkBranch(main) = false, want true")
	}
	if !shouldCreateWorkBranch(" refs/heads/main ") {
		t.Fatal("shouldCreateWorkBranch(\" refs/heads/main \") = false, want true")
	}
	if !shouldCreateWorkBranch("origin/main") {
		t.Fatal("shouldCreateWorkBranch(origin/main) = false, want true")
	}
	if shouldCreateWorkBranch("Main") {
		t.Fatal("shouldCreateWorkBranch(Main) = true, want false")
	}
	if shouldCreateWorkBranch("release/fix-ci") {
		t.Fatal("shouldCreateWorkBranch(non-main) = true, want false")
	}

	checks := prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")
	wantChecks := []string{"pr", "checks", "https://github.com/acme/repo/pull/42", "--watch", "--required", "--interval", "10"}
	if checks.Name != "gh" || checks.Dir != repoDir || !reflect.DeepEqual(checks.Args, wantChecks) {
		t.Fatalf("pr checks command unexpected: %+v", checks)
	}

	allChecks := prChecksAnyCommand(repoDir, "https://github.com/acme/repo/pull/42")
	wantAllChecks := []string{"pr", "checks", "https://github.com/acme/repo/pull/42", "--watch", "--interval", "10"}
	if allChecks.Name != "gh" || allChecks.Dir != repoDir || !reflect.DeepEqual(allChecks.Args, wantAllChecks) {
		t.Fatalf("pr checks any command unexpected: %+v", allChecks)
	}

	workflowDispatch := workflowDispatchCommand(repoDir, branch)
	wantWorkflowDispatch := []string{"workflow", "run", defaultCIWorkflowPath, "--ref", branch}
	if workflowDispatch.Name != "gh" || workflowDispatch.Dir != repoDir || !reflect.DeepEqual(workflowDispatch.Args, wantWorkflowDispatch) {
		t.Fatalf("workflow dispatch command unexpected: %+v", workflowDispatch)
	}

	pullRebase := pullRebaseCommand(repoDir, branch)
	wantPullRebase := []string{"pull", "--rebase", "origin", branch}
	if pullRebase.Name != "git" || pullRebase.Dir != repoDir || !reflect.DeepEqual(pullRebase.Args, wantPullRebase) {
		t.Fatalf("pull rebase command unexpected: %+v", pullRebase)
	}
}

func TestMaterializePromptImages(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	paths, err := materializePromptImages(runDir, []config.PromptImage{
		{Name: "Clipboard Shot.PNG", MediaType: "image/png", DataBase64: "aGVsbG8="},
	})
	if err != nil {
		t.Fatalf("materializePromptImages() error = %v", err)
	}
	if got, want := len(paths), 1; got != want {
		t.Fatalf("len(paths) = %d, want %d", got, want)
	}
	if want := filepath.Join(runDir, "prompt-images", "01-clipboard-shot.png"); paths[0] != want {
		t.Fatalf("paths[0] = %q, want %q", paths[0], want)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", paths[0], err)
	}
	if got, want := string(data), "hello"; got != want {
		t.Fatalf("image content = %q, want %q", got, want)
	}
}

func TestStageAgentsPromptFileCopiesAndCleansUpStagedFile(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(sourcePath, []byte("seeded instructions"), 0o644); err != nil {
		t.Fatalf("write source agents file: %v", err)
	}

	stagedPath, cleanup, err := stageAgentsPromptFile(targetDir, sourcePath)
	if err != nil {
		t.Fatalf("stageAgentsPromptFile() error = %v", err)
	}
	if stagedPath == sourcePath {
		t.Fatalf("stagedPath = %q, want a staged file under %q", stagedPath, targetDir)
	}
	if !strings.HasPrefix(stagedPath, targetDir+string(filepath.Separator)) {
		t.Fatalf("stagedPath = %q, want under %q", stagedPath, targetDir)
	}
	data, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if got, want := string(data), "seeded instructions"; got != want {
		t.Fatalf("staged file content = %q, want %q", got, want)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file still exists after cleanup: err=%v", err)
	}
}

func TestRunCodexStagesAgentsPromptWithinTargetDir(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(sourcePath, []byte("seeded instructions"), 0o644); err != nil {
		t.Fatalf("write source agents file: %v", err)
	}

	runner := &captureRunner{}

	h := New(runner)
	if err := h.runCodex(context.Background(), targetDir, "", codexRunOptions{}, sourcePath); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}

	if runner.cmd.Name != "codex" || runner.cmd.Dir != targetDir {
		t.Fatalf("unexpected codex command: %+v", runner.cmd)
	}
	if got, want := len(runner.cmd.Args), 4; got != want {
		t.Fatalf("len(captured.Args) = %d, want %d", got, want)
	}
	prompt := runner.cmd.Args[len(runner.cmd.Args)-1]
	re := regexp.MustCompile(`Use (.+) as your primary implementation instructions`)
	matches := re.FindStringSubmatch(prompt)
	if len(matches) != 2 {
		t.Fatalf("staged agents prompt path missing from prompt: %q", prompt)
	}
	stagedPath := strings.TrimSpace(matches[1])
	if !strings.HasPrefix(stagedPath, targetDir+string(filepath.Separator)+".moltenhub-agents-") {
		t.Fatalf("staged agents path = %q, want under %q with .moltenhub-agents-*", stagedPath, targetDir)
	}
	if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged agents file still exists after codex run: err=%v", err)
	}
}

func TestWithCompletionGatePromptIncludesFailureQueueContract(t *testing.T) {
	t.Parallel()

	got := withCompletionGatePrompt("Build API")
	wantSnippets := []string{
		"When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.",
		"When a task fails:",
		"Queue a follow-up task dedicated to reviewing the logs and fixing all underlying issues in this codebase.",
		"Pass the relevant failing file/folder log path(s) into that follow-up task context.",
		`{"repos":["<same_repo_as_failed_task>"],"base_branch":"main","target_subdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}`,
		"Completion requirements:",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("withCompletionGatePrompt() missing snippet %q", snippet)
		}
	}
}

func TestHasGitHubAuthToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	if hasGitHubAuthToken() {
		t.Fatal("hasGitHubAuthToken() = true, want false")
	}

	t.Setenv("GITHUB_TOKEN", "ghp_example")
	if !hasGitHubAuthToken() {
		t.Fatal("hasGitHubAuthToken() = false with GITHUB_TOKEN set, want true")
	}

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "ghp_example_from_gh_token")
	if !hasGitHubAuthToken() {
		t.Fatal("hasGitHubAuthToken() = false with GH_TOKEN set, want true")
	}
}

func TestShouldCreateWorkBranch(t *testing.T) {
	t.Parallel()

	if !shouldCreateWorkBranch("main") {
		t.Fatal("shouldCreateWorkBranch(main) = false, want true")
	}
	if !shouldCreateWorkBranch(" refs/heads/main ") {
		t.Fatal("shouldCreateWorkBranch(\" refs/heads/main \") = false, want true")
	}
	if !shouldCreateWorkBranch("origin/main") {
		t.Fatal("shouldCreateWorkBranch(origin/main) = false, want true")
	}
	if shouldCreateWorkBranch("Main") {
		t.Fatal("shouldCreateWorkBranch(Main) = true, want false")
	}
	if shouldCreateWorkBranch("release/hotfix") {
		t.Fatal("shouldCreateWorkBranch(non-main) = true, want false")
	}
}

func TestNormalizeBranchRef(t *testing.T) {
	t.Parallel()

	if got := normalizeBranchRef("refs/heads/release/2026.04-hotfix"); got != "release/2026.04-hotfix" {
		t.Fatalf("normalizeBranchRef(refs/heads/*) = %q, want %q", got, "release/2026.04-hotfix")
	}
	if got := normalizeBranchRef("origin/release/2026.04-hotfix"); got != "release/2026.04-hotfix" {
		t.Fatalf("normalizeBranchRef(origin/*) = %q, want %q", got, "release/2026.04-hotfix")
	}
	if normalizeBranchRef("Main") == normalizeBranchRef("main") {
		t.Fatal("normalizeBranchRef(Main) equals normalizeBranchRef(main), want different")
	}
}

func TestIsNonFastForwardPush(t *testing.T) {
	t.Parallel()

	if !isNonFastForwardPush(execx.Result{Stderr: "! [rejected] branch -> branch (fetch first)"}, errors.New("push failed")) {
		t.Fatal("isNonFastForwardPush(fetch first) = false, want true")
	}
	if !isNonFastForwardPush(execx.Result{Stderr: "non-fast-forward"}, errors.New("push failed")) {
		t.Fatal("isNonFastForwardPush(non-fast-forward) = false, want true")
	}
	if isNonFastForwardPush(execx.Result{Stderr: "permission denied"}, errors.New("push failed")) {
		t.Fatal("isNonFastForwardPush(permission denied) = true, want false")
	}
	if isNonFastForwardPush(execx.Result{}, nil) {
		t.Fatal("isNonFastForwardPush(nil err) = true, want false")
	}
}
func containsSequence(args, seq []string) bool {
	if len(seq) == 0 || len(seq) > len(args) {
		return false
	}
	for i := 0; i <= len(args)-len(seq); i++ {
		if reflect.DeepEqual(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}
