package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jef/how-i-want-to-code/internal/config"
	"github.com/jef/how-i-want-to-code/internal/execx"
	"github.com/jef/how-i-want-to-code/internal/workspace"
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

func sampleConfig() config.Config {
	return config.Config{
		Version:       "v1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  "services/api",
		Prompt:        "Build API",
		CommitMessage: "feat: automate api",
		PRTitle:       "feat: automate api",
		PRBody:        "Automated by codex harness",
		Labels:        []string{"automation", ""},
		Reviewers:     []string{"octocat", ""},
	}
}

func TestRunHappyPathCreatesPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo/pull/42\n"}},
		{cmd: prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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

func TestRunCodexFailureStopsBeforeCommitAndPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt), err: errors.New("codex failed")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"

	checkSummary := "X unit-tests failing"
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stdout: checkSummary + "\n"}, err: errors.New("checks failed")},
		{cmd: codexCommand(targetDir, remediationPrompt(cfg.Prompt, prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, remediationCommitMessage(cfg.CommitMessage, 1))},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"

	checkSummary := "X unit-tests failing"
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stdout: checkSummary + "\n"}, err: errors.New("checks failed")},
		{cmd: codexCommand(targetDir, remediationPrompt(cfg.Prompt, prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'codex/build-api-20260402-150405-abcdef12' branch"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	sleepCalls := 0
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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
	runDir := filepath.Join("/tmp", guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "codex/build-api-20260402-150405-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'codex/build-api-20260402-150405-abcdef12' branch"

	exps := []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, cfg.Prompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
	}
	for i := 0; i <= maxPRChecksNoReportRetries; i++ {
		exps = append(exps, expectedRun{
			cmd: prChecksCommand(repoDir, prURL),
			res: execx.Result{Stderr: noChecks + "\n"},
			err: errors.New("checks unavailable"),
		})
	}
	exps = append(exps,
		expectedRun{cmd: codexCommand(targetDir, remediationPrompt(cfg.Prompt, prURL, noChecks, 1))},
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
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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
	runDir := filepath.Join("/tmp", guid)
	branch := "codex/build-api-20260402-150405-abcdef12"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	codexPrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, []repoWorkspace{
		{URL: cfg.Repos[0], RelDir: repoRelA},
		{URL: cfg.Repos[1], RelDir: repoRelB},
	})

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: codexCommand(runDir, codexPrompt)},
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
	h.Workspace = workspace.Manager{
		PathExists: func(string) bool { return false },
		NewGUID:    func() string { return guid },
		MkdirAll:   func(string, os.FileMode) error { return nil },
	}
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

func TestCommandBuilders(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	repoDir := "/tmp/run/repo"
	branch := "codex/build-api-20260402-150405-abcdef12"
	prompt := "fix tests"
	targetDir := filepath.Join(repoDir, "services/api")

	clone := cloneCommand(cfg, repoDir)
	if clone.Name != "git" || !reflect.DeepEqual(clone.Args, []string{"clone", "--branch", "main", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone command unexpected: %+v", clone)
	}

	codex := codexCommand(targetDir, prompt)
	if codex.Name != "codex" || codex.Dir != targetDir || !reflect.DeepEqual(codex.Args, []string{"exec", "--sandbox", "workspace-write", withCompletionGatePrompt(prompt)}) {
		t.Fatalf("codex command unexpected: %+v", codex)
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

	checks := prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")
	wantChecks := []string{"pr", "checks", "https://github.com/acme/repo/pull/42", "--watch", "--required", "--interval", "10"}
	if checks.Name != "gh" || checks.Dir != repoDir || !reflect.DeepEqual(checks.Args, wantChecks) {
		t.Fatalf("pr checks command unexpected: %+v", checks)
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
