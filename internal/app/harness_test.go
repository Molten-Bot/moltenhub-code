package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Molten-Bot/agent_00/internal/agentruntime"
	"github.com/Molten-Bot/agent_00/internal/config"
	"github.com/Molten-Bot/agent_00/internal/execx"
	"github.com/Molten-Bot/agent_00/internal/failurefollowup"
	"github.com/Molten-Bot/agent_00/internal/slug"
	"github.com/Molten-Bot/agent_00/internal/workspace"
)

type expectedRun struct {
	cmd execx.Command
	res execx.Result
	err error
}

type fakeRunner struct {
	t                    *testing.T
	exps                 []expectedRun
	calls                []execx.Command
	allowUnorderedClones bool
	mu                   sync.Mutex
}

type blockingAgentRunner struct {
	started chan struct{}
	once    sync.Once
}

func (r *blockingAgentRunner) Run(ctx context.Context, _ execx.Command) (execx.Result, error) {
	r.once.Do(func() {
		close(r.started)
	})
	<-ctx.Done()
	return execx.Result{}, ctx.Err()
}

func (f *fakeRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.exps) == 0 {
		if isImplicitBranchFreshnessCommand(cmd) {
			f.calls = append(f.calls, cmd)
			return execx.Result{}, nil
		}
		f.t.Fatalf("unexpected command: %+v", cmd)
	}

	matchIndex := -1
	if commandsEqual(f.exps[0].cmd, cmd) {
		matchIndex = 0
	} else if f.allowUnorderedClones && isCloneGitCommand(cmd) {
		for i, exp := range f.exps {
			if !isCloneGitCommand(exp.cmd) {
				continue
			}
			if commandsEqual(exp.cmd, cmd) {
				matchIndex = i
				break
			}
		}
	}

	if matchIndex < 0 {
		if isImplicitBranchFreshnessCommand(cmd) {
			f.calls = append(f.calls, cmd)
			return execx.Result{}, nil
		}
		f.t.Fatalf("command mismatch\n got:  %+v\n want: %+v", cmd, f.exps[0].cmd)
	}

	exp := f.exps[matchIndex]
	f.exps = append(f.exps[:matchIndex], f.exps[matchIndex+1:]...)
	f.calls = append(f.calls, cmd)
	return exp.res, exp.err
}

func commandsEqual(a, b execx.Command) bool {
	if a.Name != b.Name || a.Dir != b.Dir {
		return false
	}
	if isClaudePromptArgCommand(a) && isClaudePromptArgCommand(b) {
		return reflect.DeepEqual(a.Args[:len(a.Args)-1], b.Args[:len(b.Args)-1])
	}
	return reflect.DeepEqual(a.Args, b.Args)
}

func envContainsKey(environ []string, key string) bool {
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if name == key {
			return true
		}
	}
	return false
}

func isCloneGitCommand(cmd execx.Command) bool {
	return cmd.Name == "git" && len(cmd.Args) > 0 && cmd.Args[0] == "clone"
}

func isClaudePromptArgCommand(cmd execx.Command) bool {
	return len(cmd.Args) >= 5 &&
		cmd.Args[0] == "--print" &&
		cmd.Args[1] == "--output-format" &&
		cmd.Args[2] == "text" &&
		cmd.Args[3] == "--dangerously-skip-permissions"
}

func isImplicitBranchFreshnessCommand(cmd execx.Command) bool {
	if cmd.Name != "git" {
		return false
	}
	if len(cmd.Args) >= 3 && cmd.Args[0] == "fetch" && cmd.Args[1] == "origin" {
		return isKnownDefaultBranchName(cmd.Args[2])
	}
	return len(cmd.Args) == 3 && cmd.Args[0] == "merge" && cmd.Args[1] == "--no-edit" && cmd.Args[2] == "FETCH_HEAD"
}

type captureRunner struct {
	cmd execx.Command
}

func (c *captureRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	c.cmd = cmd
	return execx.Result{}, nil
}

type streamLine struct {
	stream string
	line   string
}

type streamCaptureRunner struct {
	res         execx.Result
	err         error
	lines       []streamLine
	capturedCmd execx.Command
}

func (s *streamCaptureRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	s.capturedCmd = cmd
	return s.res, s.err
}

func (s *streamCaptureRunner) RunStream(_ context.Context, cmd execx.Command, handler execx.StreamLineHandler) (execx.Result, error) {
	s.capturedCmd = cmd
	for _, line := range s.lines {
		if handler != nil {
			handler(line.stream, line.line)
		}
	}
	return s.res, s.err
}

type blockingContextRunner struct{}

func (r *blockingContextRunner) Run(ctx context.Context, _ execx.Command) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{}, ctx.Err()
}

type deadlineCaptureRunner struct {
	hadDeadline bool
}

func (r *deadlineCaptureRunner) Run(ctx context.Context, _ execx.Command) (execx.Result, error) {
	_, r.hadDeadline = ctx.Deadline()
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
		PRTitle:       "feat: automate api",
		PRBody:        "Proposed changes from Molten.Bot\n\nThis PR implements the requested changes described below.\nBuilt using AI-assisted engineering and reviewed before submission.\nOnly relevant files were modified.\n\nAutomated by MoltenHub Code\n\nOriginal task prompt:\n```text\nBuild API\n```\n\nCurious how this was built? See how AI agents can help with your own projects: [MoltenBot Code](https://molten.bot/code?source=pr)",
		Labels:        []string{"automation", ""},
		Reviewers:     []string{"octocat", ""},
	}
}

func repoURLFromConfig(cfg config.Config) string {
	return cfg.RepoURL
}

func expectedPreparedReviewContext(repoURL, metadataJSON, commentsText, diffStat, diffPatch string) string {
	var metadata reviewPRMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		panic(err)
	}
	prettyMetadata, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		panic(err)
	}

	var b strings.Builder
	b.WriteString("Prepared pull-request review context (collected before you started):\n")
	b.WriteString(fmt.Sprintf("- Repository remote: %s\n", repoURL))
	b.WriteString(fmt.Sprintf("- Pull request: #%d\n", metadata.Number))
	b.WriteString(fmt.Sprintf("- Pull request URL: %s\n", metadata.URL))
	b.WriteString(fmt.Sprintf("- Base branch: %s\n", metadata.BaseRefName))
	b.WriteString(fmt.Sprintf("- Head branch: %s\n", metadata.HeadRefName))
	b.WriteString("- Existing PR discussion has already been fetched for you below.\n")
	b.WriteString("- The git diff below was generated locally after fetching the PR head and base refs.\n")
	b.WriteString("- Treat this prepared context as a starting point and verify important claims yourself before concluding.\n\n")
	b.WriteString("Pull request metadata:\n```json\n")
	b.WriteString(string(prettyMetadata))
	b.WriteString("\n```\n\n")
	b.WriteString("Existing pull request discussion:\n```text\n")
	b.WriteString(commentsText)
	b.WriteString("\n```\n\n")
	b.WriteString("Local git diff summary:\n```text\n")
	b.WriteString(diffStat)
	b.WriteString("\n```\n\n")
	b.WriteString("Local git diff patch:\n```diff\n")
	b.WriteString(diffPatch)
	b.WriteString("\n```")
	return b.String()
}

func expectedReviewDiscussion(issueComments, reviewComments, reviews string) string {
	return strings.Join([]string{
		"Issue comments:\n" + issueComments,
		"Review comments:\n" + reviewComments,
		"Reviews:\n" + reviews,
	}, "\n\n")
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

func testRunDir(guid string) string {
	return filepath.Join("/tmp", "moltenhub-code", "tasks", guid)
}

func TestRunHappyPathCreatesPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n M file.go\n"}},
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
	if got, want := res.Branch, branch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunPRCreateAlreadyExistsReusesExistingPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	prCreateStderr := fmt.Sprintf(
		"a pull request for branch %q into branch %q already exists:\n%s\n",
		branch,
		cfg.BaseBranch,
		prURL,
	)

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stderr: prCreateStderr}, err: errors.New("pr create failed")},
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

func TestRunPRCreatePermissionDeniedAfterPushCompletesWithBranch(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prCreateStderr := "pull request create failed: GraphQL: moltenbot000 does not have the correct permissions to execute `CreatePullRequest` (createPullRequest)"

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stderr: prCreateStderr}, err: errors.New("pr create failed")},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if res.PRURL != "" {
		t.Fatalf("PRURL = %q, want empty", res.PRURL)
	}
	if res.Branch != branch {
		t.Fatalf("Branch = %q, want %q", res.Branch, branch)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=manual_create_required reason=create_pull_request_permission_denied") {
		t.Fatalf("logs missing manual PR warning:\n%s", strings.Join(logs, "\n"))
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunPRCreateRequiresAuthenticationAfterPushCompletesWithBranch(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prCreateStderr := "HTTP 401: Requires authentication (https://api.github.com/graphql)\nTry authenticating with:  gh auth login"

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stderr: prCreateStderr}, err: errors.New("pr create failed")},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d", res.ExitCode)
	}
	if res.PRURL != "" {
		t.Fatalf("PRURL = %q, want empty", res.PRURL)
	}
	if res.Branch != branch {
		t.Fatalf("Branch = %q, want %q", res.Branch, branch)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=manual_create_required reason=create_pull_request_permission_denied") {
		t.Fatalf("logs missing manual PR warning:\n%s", strings.Join(logs, "\n"))
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesLogsInitialAgentInvocationWorkflowMetadata(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"stage=codex status=start target=services/api agent_run_id=agent-implementation-1 agent_harness=codex mode=implementation attempt=1 repo=repo repo_dir=repo target=services/api",
		"stage=codex status=ok elapsed_s=",
		"agent_run_id=agent-implementation-1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q:\n%s", want, joined)
		}
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunCodexCaptureHeartbeatLogsAgentInvocationWorkflowMetadata(t *testing.T) {
	t.Parallel()

	runner := &blockingAgentRunner{started: make(chan struct{})}
	h := New(runner)
	h.AgentHeartbeatInterval = time.Millisecond
	var (
		mu   sync.Mutex
		logs []string
	)
	h.Logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	runtime := agentruntime.Default()
	targetDir := t.TempDir()
	invocation := newAgentInvocationLogMetadata(runtime, "implementation", 1, "repo", "repo", "services/api")
	go func() {
		_, err := h.runCodexCapture(ctx, runtime, targetDir, "Build API", codexRunOptions{}, "", "", invocation)
		done <- err
	}()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent runner did not start")
	}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		joined := strings.Join(logs, "\n")
		mu.Unlock()
		if strings.Contains(joined, "stage=codex status=running") {
			if !strings.Contains(joined, "agent_run_id=agent-implementation-1 agent_harness=codex mode=implementation attempt=1 repo=repo repo_dir=repo target=services/api") {
				t.Fatalf("running log missing workflow metadata:\n%s", joined)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("runCodexCapture() err = %v, want context.Canceled", err)
			}
			return
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("running heartbeat log missing:\n%s", joined)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRunPRCreateTransientFailureRetriesAndSucceeds(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	transientErr := errors.New("error checking for existing pull request: HTTP 504: We couldn't respond to your request in time. Sorry about that. Please try resubmitting your request and contact us if the problem persists. (https://api.github.com/graphql)")

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stderr: transientErr.Error()}, err: transientErr},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch), res: execx.Result{Stdout: "abc123\trefs/heads/" + branch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, branch), res: execx.Result{Stdout: "[]\n"}},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	var sleeps int
	h.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps++
		if d != prCreateRetryDelay {
			t.Fatalf("sleep delay = %s, want %s", d, prCreateRetryDelay)
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
	if res.PRURL != prURL {
		t.Fatalf("PRURL = %q, want %q", res.PRURL, prURL)
	}
	if sleeps != 1 {
		t.Fatalf("sleep calls = %d, want 1", sleeps)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunPRCreateTransientFailureReusesLookupResult(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	transientErr := errors.New("error checking for existing pull request: HTTP 504: We couldn't respond to your request in time. (https://api.github.com/graphql)")

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stderr: transientErr.Error()}, err: transientErr},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch), res: execx.Result{Stdout: "abc123\trefs/heads/" + branch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, branch), res: execx.Result{Stdout: `[{"url":"` + prURL + `"}]`}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Sleep = func(context.Context, time.Duration) error {
		t.Fatal("Sleep() called; lookup result should avoid retry delay")
		return nil
	}

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

func TestRunCommitNoOpReturnsNoChanges(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{
			cmd: commitCommand(repoDir, cfg.CommitMessage),
			res: execx.Result{Stdout: "On branch moltenhub-build-api\nnothing to commit, working tree clean\n"},
			err: errors.New("exit status 1"),
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n"}},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "0\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
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
	if got, want := res.Branch, branch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
}

func TestRunCommitNoOpWithExistingLocalCommitPushesAndCreatesPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/4242"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n"}},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "1\n"}},
		{cmd: addCommand(repoDir)},
		{
			cmd: commitCommand(repoDir, cfg.CommitMessage),
			res: execx.Result{Stdout: "On branch moltenhub-build-api\nnothing to commit, working tree clean\n"},
			err: errors.New("exit status 1"),
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n"}},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "1\n"}},
		{cmd: fetchBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: mergeFetchedBranchCommand(repoDir)},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "1\n"}},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
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
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunSkipsPRWhenWorkBranchHasNoDeltaFromRemoteBase(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## " + branch + "...origin/" + branch + " [ahead 1]\n"}},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "0\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch), res: execx.Result{Stdout: "[]\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
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

func TestRunSkipsPRWhenPostSyncWorkBranchHasNoDeltaFromRemoteBase(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## " + branch + "\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{
			cmd: commitCommand(repoDir, cfg.CommitMessage),
			res: execx.Result{Stdout: "On branch " + branch + "\nnothing to commit, working tree clean\n"},
			err: errors.New("exit status 1"),
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## " + branch + "\n"}},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "1\n"}},
		{cmd: fetchBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: mergeFetchedBranchCommand(repoDir)},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "0\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch), res: execx.Result{Stdout: "[]\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
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

func TestRunBuildsReviewContextBeforeInvokingCodex(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "Review the pull request"
	cfg.PRBody = "Proposed changes from Molten.Bot\n\nThis PR implements the requested changes described below.\nBuilt using AI-assisted engineering and reviewed before submission.\nOnly relevant files were modified.\n\nAutomated by MoltenHub Code\n\nOriginal task prompt:\n```text\nReview the pull request\n```\n\nCurious how this was built? See how AI agents can help with your own projects: [MoltenBot Code](https://molten.bot/code?source=pr)"
	cfg.Review = &config.ReviewConfig{PRNumber: 42}

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	metadataJSON := `{"number":42,"title":"Improve tests","body":"Adds stronger coverage.","url":"https://github.com/acme/repo/pull/42","state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"feature/improve-tests","author":{"login":"octocat"}}`
	commentsText := expectedReviewDiscussion(
		`[{"user":{"login":"reviewer"},"body":"Please add one more regression test."}]`,
		`[]`,
		`[]`,
	)
	diffStat := " internal/service_test.go | 12 ++++++++++++\n 1 file changed, 12 insertions(+)"
	diffPatch := "diff --git a/internal/service_test.go b/internal/service_test.go\n+func TestServiceRegression(t *testing.T) {}\n"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: prReviewMetadataCommand(repoDir, "42"), res: execx.Result{Stdout: metadataJSON}},
		{cmd: fetchRemoteBranchCommand(repoDir, "main")},
		{cmd: fetchPullRequestHeadCommand(repoDir, 42)},
		{cmd: prReviewIssueCommentsCommand(repoDir, 42), res: execx.Result{Stdout: `[{"user":{"login":"reviewer"},"body":"Please add one more regression test."}]`}},
		{cmd: prReviewReviewCommentsCommand(repoDir, 42), res: execx.Result{Stdout: `[]`}},
		{cmd: prReviewReviewsCommand(repoDir, 42), res: execx.Result{Stdout: `[]`}},
		{cmd: reviewDiffStatCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffStat}},
		{cmd: reviewDiffPatchCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffPatch}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(strings.TrimSpace(cfg.Prompt+"\n\n"+expectedPreparedReviewContext(repoURLFromConfig(cfg), metadataJSON, commentsText, diffStat, diffPatch)), agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## main\n"}},
		{cmd: prReviewSummaryCommand(repoDir, "42", "acme/repo", reviewSummaryBodyPath(runDir, 42))},
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
		t.Fatal("NoChanges = false, want true for review-only run")
	}
	if got, want := res.PRURL, "https://github.com/acme/repo/pull/42"; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunBuildsReviewContextFromHeadBranchSelector(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "Review the pull request"
	cfg.PRBody = "Proposed changes from Molten.Bot\n\nThis PR implements the requested changes described below.\nBuilt using AI-assisted engineering and reviewed before submission.\nOnly relevant files were modified.\n\nAutomated by MoltenHub Code\n\nOriginal task prompt:\n```text\nReview the pull request\n```\n\nCurious how this was built? See how AI agents can help with your own projects: [MoltenBot Code](https://molten.bot/code?source=pr)"
	cfg.Review = &config.ReviewConfig{HeadBranch: "feature/improve-tests", AutoMerge: true, MergeMethod: "squash"}

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	metadataJSON := `{"number":42,"title":"Improve tests","body":"Adds stronger coverage.","url":"https://github.com/acme/repo/pull/42","state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"feature/improve-tests","headRefOid":"abc123","author":{"login":"octocat"}}`
	commentsText := expectedReviewDiscussion(
		`[{"user":{"login":"reviewer"},"body":"Please add one more regression test."}]`,
		`[]`,
		`[]`,
	)
	diffStat := " internal/service_test.go | 12 ++++++++++++\n 1 file changed, 12 insertions(+)"
	diffPatch := "diff --git a/internal/service_test.go b/internal/service_test.go\n+func TestServiceRegression(t *testing.T) {}\n"
	reviewOutput := "No material issues found.\n\n```json\n{\"status\":\"clean\",\"mergeReady\":true,\"summary\":\"No material issues found.\",\"findings\":[]}\n```"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: prReviewMetadataCommand(repoDir, "feature/improve-tests"), res: execx.Result{Stdout: metadataJSON}},
		{cmd: fetchRemoteBranchCommand(repoDir, "main")},
		{cmd: fetchPullRequestHeadCommand(repoDir, 42)},
		{cmd: prReviewIssueCommentsCommand(repoDir, 42), res: execx.Result{Stdout: `[{"user":{"login":"reviewer"},"body":"Please add one more regression test."}]`}},
		{cmd: prReviewReviewCommentsCommand(repoDir, 42), res: execx.Result{Stdout: `[]`}},
		{cmd: prReviewReviewsCommand(repoDir, 42), res: execx.Result{Stdout: `[]`}},
		{cmd: reviewDiffStatCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffStat}},
		{cmd: reviewDiffPatchCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffPatch}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(strings.TrimSpace(cfg.Prompt+"\n\n"+expectedPreparedReviewContext(repoURLFromConfig(cfg), metadataJSON, commentsText, diffStat, diffPatch)), agentsPath)), res: execx.Result{Stdout: reviewOutput}},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## main\n"}},
		{cmd: prReviewSummaryCommand(repoDir, "42", "acme/repo", reviewSummaryBodyPath(runDir, 42))},
		{cmd: prMergeAutoCommand(repoDir, "42", "acme/repo", "squash", "abc123")},
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
	if got, want := res.PRURL, "https://github.com/acme/repo/pull/42"; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestCompleteReviewRunFallsBackToIssueCommentWhenReviewWritebackFails(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	repoDir := t.TempDir()
	bodyPath := reviewSummaryBodyPath(runDir, 42)
	reviewErr := errors.New("run gh [pr review 42 --comment --body-file body --repo acme/repo]: exit status 1 (GraphQL: Could not resolve to a Repository with the name 'acme/repo'. (repository))")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: prReviewSummaryCommand(repoDir, "42", "acme/repo", bodyPath),
			err: reviewErr,
		},
		{cmd: prReviewSummaryFallbackCommand(repoDir, "42", "acme/repo", 42, bodyPath)},
	}}
	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	cfg := config.Config{Review: &config.ReviewConfig{PRNumber: 42}}
	repo := repoWorkspace{Dir: repoDir}
	reviewContext := &preparedReviewContext{
		Selector: "42",
		Metadata: reviewPRMetadata{
			Number:      42,
			URL:         "https://github.com/acme/repo/pull/42",
			HeadRefName: "feature/improve-tests",
		},
	}
	agentRes := execx.Result{Stdout: "```json\n{\"status\":\"findings\",\"mergeReady\":false,\"summary\":\"One issue.\",\"findings\":[{\"severity\":\"Medium\",\"path\":\"src/app.js\",\"line\":9,\"title\":\"Bug\"}]}\n```"}

	if err := h.completeReviewRun(context.Background(), cfg, &repo, reviewContext, agentRes, runDir); err != nil {
		t.Fatalf("completeReviewRun() err = %v, want nil after fallback", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "action=comment_review_failed") || !strings.Contains(got, "action=comment_fallback") {
		t.Fatalf("logs = %q, want review failure and fallback entries", got)
	}
}

func TestCompleteReviewRunDefersTransientGitHubWritebackFailure(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	repoDir := t.TempDir()
	bodyPath := reviewSummaryBodyPath(runDir, 42)
	reviewErr := errors.New("run gh [pr review 42 --comment --body-file body --repo acme/repo]: exit status 1 (error connecting to api.github.com | check your internet connection or https://githubstatus.com)")
	fallbackErr := errors.New("run gh [api repos/acme/repo/issues/42/comments -F body=@body]: exit status 1 (error connecting to api.github.com | check your internet connection or https://githubstatus.com)")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: prReviewSummaryCommand(repoDir, "42", "acme/repo", bodyPath),
			err: reviewErr,
		},
		{
			cmd: prReviewSummaryFallbackCommand(repoDir, "42", "acme/repo", 42, bodyPath),
			err: fallbackErr,
		},
	}}
	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	cfg := config.Config{Review: &config.ReviewConfig{PRNumber: 42}}
	repo := repoWorkspace{Dir: repoDir}
	reviewContext := &preparedReviewContext{
		Selector: "42",
		Metadata: reviewPRMetadata{
			Number:      42,
			URL:         "https://github.com/acme/repo/pull/42",
			HeadRefName: "feature/improve-tests",
		},
	}
	agentRes := execx.Result{Stdout: "```json\n{\"status\":\"clean\",\"mergeReady\":true,\"summary\":\"No material issues.\",\"positives\":[\"Focused tests pass.\"],\"findings\":[]}\n```"}

	if err := h.completeReviewRun(context.Background(), cfg, &repo, reviewContext, agentRes, runDir); err != nil {
		t.Fatalf("completeReviewRun() err = %v, want nil for transient GitHub writeback failure", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "action=comment_writeback_deferred") {
		t.Fatalf("logs = %q, want deferred writeback warning", got)
	}
}

func TestCompleteReviewRunReturnsNonTransientWritebackFailure(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	repoDir := t.TempDir()
	bodyPath := reviewSummaryBodyPath(runDir, 42)
	reviewErr := errors.New("run gh [pr review 42 --comment --body-file body --repo acme/repo]: exit status 1 (HTTP 401: Requires authentication (https://api.github.com/graphql))")
	fallbackErr := errors.New("run gh [api repos/acme/repo/issues/42/comments -F body=@body]: exit status 1 (HTTP 403: Resource not accessible by integration (https://api.github.com/graphql))")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: prReviewSummaryCommand(repoDir, "42", "acme/repo", bodyPath),
			err: reviewErr,
		},
		{
			cmd: prReviewSummaryFallbackCommand(repoDir, "42", "acme/repo", 42, bodyPath),
			err: fallbackErr,
		},
	}}
	h := New(fake)

	cfg := config.Config{Review: &config.ReviewConfig{PRNumber: 42}}
	repo := repoWorkspace{Dir: repoDir}
	reviewContext := &preparedReviewContext{
		Selector: "42",
		Metadata: reviewPRMetadata{
			Number: 42,
			URL:    "https://github.com/acme/repo/pull/42",
		},
	}
	agentRes := execx.Result{Stdout: "```json\n{\"status\":\"clean\",\"mergeReady\":true,\"summary\":\"No material issues.\",\"positives\":[\"Focused tests pass.\"],\"findings\":[]}\n```"}

	err := h.completeReviewRun(context.Background(), cfg, &repo, reviewContext, agentRes, runDir)
	if err == nil {
		t.Fatal("completeReviewRun() err = nil, want non-transient writeback failure")
	}
	if !strings.Contains(err.Error(), "post pull request review summary") {
		t.Fatalf("completeReviewRun() err = %q, want writeback context", err)
	}
}

func TestCompleteReviewRunDoesNotDeferWhenFallbackWritebackFailureIsPermanent(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	repoDir := t.TempDir()
	bodyPath := reviewSummaryBodyPath(runDir, 42)
	reviewErr := errors.New("run gh [pr review 42 --comment --body-file body --repo acme/repo]: exit status 1 (error connecting to api.github.com | check your internet connection or https://githubstatus.com)")
	fallbackErr := errors.New("run gh [api repos/acme/repo/issues/42/comments -F body=@body]: exit status 1 (HTTP 403: Resource not accessible by integration (https://api.github.com/graphql))")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: prReviewSummaryCommand(repoDir, "42", "acme/repo", bodyPath),
			err: reviewErr,
		},
		{
			cmd: prReviewSummaryFallbackCommand(repoDir, "42", "acme/repo", 42, bodyPath),
			err: fallbackErr,
		},
	}}
	h := New(fake)

	cfg := config.Config{Review: &config.ReviewConfig{PRNumber: 42}}
	repo := repoWorkspace{Dir: repoDir}
	reviewContext := &preparedReviewContext{
		Selector: "42",
		Metadata: reviewPRMetadata{
			Number: 42,
			URL:    "https://github.com/acme/repo/pull/42",
		},
	}
	agentRes := execx.Result{Stdout: "```json\n{\"status\":\"clean\",\"mergeReady\":true,\"summary\":\"No material issues.\",\"positives\":[\"Focused tests pass.\"],\"findings\":[]}\n```"}

	err := h.completeReviewRun(context.Background(), cfg, &repo, reviewContext, agentRes, runDir)
	if err == nil {
		t.Fatal("completeReviewRun() err = nil, want permanent fallback writeback failure")
	}
	if !strings.Contains(err.Error(), "post pull request review summary") {
		t.Fatalf("completeReviewRun() err = %q, want writeback context", err)
	}
}

func TestBuildReviewPromptContextRetriesMetadataWithoutInvalidGitHubTokenEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "stale-token")
	t.Setenv("GITHUB_TOKEN", "stale-token")

	repoDir := t.TempDir()
	metadataJSON := `{"number":2,"title":"Fix bug","body":"Body","url":"https://github.com/acme/repo/pull/2","state":"OPEN","baseRefName":"main","headRefName":"fix-bug","headRefOid":"abc123","author":{"login":"octocat"}}`
	authErr := errors.New("run gh [pr view 2 --json number,title,body,url,state,isDraft,baseRefName,headRefName,headRefOid,author]: exit status 1 (HTTP 401: Requires authentication (https://api.github.com/graphql))")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: prReviewMetadataCommand(repoDir, "2"), err: authErr},
		{cmd: prReviewMetadataCommand(repoDir, "2"), res: execx.Result{Stdout: metadataJSON}},
		{cmd: fetchRemoteBranchCommand(repoDir, "main")},
		{cmd: fetchPullRequestHeadCommand(repoDir, 2)},
		{cmd: prReviewIssueCommentsCommand(repoDir, 2), res: execx.Result{Stdout: "[]"}},
		{cmd: prReviewReviewCommentsCommand(repoDir, 2), res: execx.Result{Stdout: "[]"}},
		{cmd: prReviewReviewsCommand(repoDir, 2), res: execx.Result{Stdout: "[]"}},
		{cmd: reviewDiffStatCommand(repoDir, "refs/remotes/origin/main", "refs/remotes/origin/moltenhub-pr-2"), res: execx.Result{Stdout: " file.go | 1 +"}},
		{cmd: reviewDiffPatchCommand(repoDir, "refs/remotes/origin/main", "refs/remotes/origin/moltenhub-pr-2"), res: execx.Result{Stdout: "diff --git a/file.go b/file.go"}},
	}}
	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	ctx, err := h.buildReviewPromptContext(context.Background(), repoWorkspace{URL: "https://github.com/acme/repo.git", Dir: repoDir, RelDir: "repo"}, config.ReviewConfig{PRNumber: 2})
	if err != nil {
		t.Fatalf("buildReviewPromptContext() err = %v", err)
	}
	if !ctx.GitHubTokenEnvSanitized {
		t.Fatal("GitHubTokenEnvSanitized = false, want true")
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "action=retry_without_env_github_token") {
		t.Fatalf("logs = %q, want sanitized retry warning", got)
	}
	if len(fake.calls) < 7 {
		t.Fatalf("calls = %d, want at least 7", len(fake.calls))
	}
	for _, idx := range []int{1, 4, 5, 6} {
		if envContainsKey(fake.calls[idx].Env, "GH_TOKEN") || envContainsKey(fake.calls[idx].Env, "GITHUB_TOKEN") {
			t.Fatalf("call %d env contains GitHub token variables: %#v", idx, fake.calls[idx].Env)
		}
	}
}

func TestAutoMergeCleanReviewSkipsUnsupportedAutoMergeConfiguration(t *testing.T) {
	t.Parallel()

	repo := repoWorkspace{Dir: "/repo"}
	metadata := reviewPRMetadata{
		URL:        "https://github.com/acme/repo/pull/42",
		State:      "OPEN",
		HeadRefOID: "abc123",
	}
	outcome := reviewOutcome{Status: "clean", MergeReady: true}
	autoMergeErr := errors.New("run gh [pr merge 42 --auto --match-head-commit abc123 --squash]: exit status 1 (GraphQL: Pull request Protected branch rules not configured for this branch (enablePullRequestAutoMerge))")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: prMergeAutoCommand(repo.Dir, "42", "", "squash", "abc123"),
			err: autoMergeErr,
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if err := h.autoMergeCleanReview(context.Background(), repo, "42", "", "squash", metadata, outcome, false); err != nil {
		t.Fatalf("autoMergeCleanReview() err = %v, want nil for unsupported auto-merge config", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "reason=unsupported_or_unconfigured") {
		t.Fatalf("logs = %q, want unsupported auto-merge skip reason", got)
	}
}

func TestAutoMergeCleanReviewSkipsPermissionDeniedAutoMerge(t *testing.T) {
	t.Parallel()

	repo := repoWorkspace{Dir: "/repo"}
	metadata := reviewPRMetadata{
		URL:        "https://github.com/acme/repo/pull/42",
		State:      "OPEN",
		HeadRefOID: "abc123",
	}
	outcome := reviewOutcome{Status: "clean", MergeReady: true}
	autoMergeErr := errors.New("run gh [pr merge 42 --auto --match-head-commit abc123 --squash --repo acme/repo]: exit status 1 (GraphQL: moltenbot000 does not have the correct permissions to execute `MergePullRequest` (mergePullRequest))")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: prMergeAutoCommand(repo.Dir, "42", "acme/repo", "squash", "abc123"),
			err: autoMergeErr,
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if err := h.autoMergeCleanReview(context.Background(), repo, "42", "acme/repo", "squash", metadata, outcome, false); err != nil {
		t.Fatalf("autoMergeCleanReview() err = %v, want nil for permission-denied auto-merge", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "reason=unsupported_or_unconfigured") {
		t.Fatalf("logs = %q, want permission-denied auto-merge skip reason", got)
	}
}

func TestReviewCommentBodyUsesStructuredPositiveNegativePoints(t *testing.T) {
	t.Parallel()

	output := `Here is extra analysis that should not be posted.

` + "```json" + `
{
  "status": "findings",
  "mergeReady": false,
  "summary": "Review found one issue.",
  "positives": [
    "Adds focused regression coverage.",
    "Keeps the existing public API unchanged."
  ],
  "findings": [
    {
      "severity": "Medium",
      "path": "src/worker.js",
      "line": 42,
      "title": "Redaction misses query strings"
    }
  ]
}
` + "```"

	outcome, ok := parseReviewOutcome(output)
	if !ok {
		t.Fatal("parseReviewOutcome() ok = false, want true")
	}
	got := reviewCommentBody(execx.Result{Stdout: output}, outcome, ok)
	want := "**Positive**\n" +
		"- Adds focused regression coverage.\n" +
		"- Keeps the existing public API unchanged.\n" +
		"\n" +
		"**Negative**\n" +
		"- [Medium] src/worker.js:42 - Redaction misses query strings"
	if got != want {
		t.Fatalf("reviewCommentBody() = %q, want %q", got, want)
	}
}

func TestReviewCommentBodyCleanOutcomeKeepsPositiveNegativeShape(t *testing.T) {
	t.Parallel()

	output := "Verbose text that should be ignored.\n\n```json\n{\"status\":\"clean\",\"mergeReady\":true,\"summary\":\"No material issues found.\",\"findings\":[]}\n```"
	outcome, ok := parseReviewOutcome(output)
	if !ok {
		t.Fatal("parseReviewOutcome() ok = false, want true")
	}
	got := reviewCommentBody(execx.Result{Stdout: output}, outcome, ok)
	want := "**Positive**\n" +
		"- No material issues found.\n" +
		"\n" +
		"**Negative**\n" +
		"- No material issues found."
	if got != want {
		t.Fatalf("reviewCommentBody(clean) = %q, want %q", got, want)
	}
}

func TestRunWithGitHubTokenRunsAuthSetupGitBeforeCodex(t *testing.T) {
	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	t.Setenv("GITHUB_TOKEN", "github_token_example_token")
	t.Setenv("GH_TOKEN", "")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "setup-git"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
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

func TestRunAuthSetupGitRetriesGitConfigLockContention(t *testing.T) {
	lockErr := errors.New("failed to set up git credential helper: failed to run git: error: could not lock config file /home/jef/.gitconfig: File exists")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: authSetupGitCommand(), err: lockErr},
		{cmd: authSetupGitCommand()},
	}}

	h := New(fake)
	sleepCalls := 0
	h.Sleep = func(context.Context, time.Duration) error {
		sleepCalls++
		return nil
	}

	if err := h.runAuthSetupGit(context.Background()); err != nil {
		t.Fatalf("runAuthSetupGit() err = %v, want nil", err)
	}
	if sleepCalls != 1 {
		t.Fatalf("runAuthSetupGit() sleep calls = %d, want 1", sleepCalls)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunAuthSetupGitSkipsFatalOnPersistentGitConfigLockContention(t *testing.T) {
	lockErr := errors.New("failed to set up git credential helper: failed to run git: error: could not lock config file /home/jef/.gitconfig: File exists")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: authSetupGitCommand(), err: lockErr},
		{cmd: authSetupGitCommand(), err: lockErr},
		{cmd: authSetupGitCommand(), err: lockErr},
	}}

	h := New(fake)
	if err := h.runAuthSetupGit(context.Background()); err != nil {
		t.Fatalf("runAuthSetupGit() err = %v, want nil", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunAuthSetupGitReturnsErrorForNonLockFailure(t *testing.T) {
	setupErr := errors.New("failed to set up git credential helper: command failed")
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: authSetupGitCommand(), err: setupErr},
	}}

	h := New(fake)
	if err := h.runAuthSetupGit(context.Background()); !errors.Is(err, setupErr) {
		t.Fatalf("runAuthSetupGit() err = %v, want %v", err, setupErr)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunWithPromptImagesKeepsArtifactsOutOfRepo(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Images = []config.PromptImage{
		{Name: "Clipboard Shot.PNG", MediaType: "image/png", DataBase64: "aGVsbG8="},
	}
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "fedcba987654"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	imagePath := filepath.Join(runDir, "prompt-images", "01-clipboard-shot.png")
	imageArg := imagePath

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommandWithOptions(targetDir, withAgentsPrompt(
			withPromptImagePaths(cfg.Prompt, []string{imageArg}),
			agentsPath,
		), codexRunOptions{
			ImagePaths:   []string{imageArg},
			WritableDirs: []string{runDir},
		})},
		{cmd: statusCommand(repoDir)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
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
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}

	data, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", imagePath, err)
	}
	if got, want := string(data), "hello"; got != want {
		t.Fatalf("image content = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "prompt-images")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target repo prompt-images dir should be absent, stat err = %v", err)
	}
}

func TestRunRequiredNonDefaultBranchRejectsUnsafeConfigBeforeCommands(t *testing.T) {
	for _, tt := range []struct {
		name       string
		baseBranch string
		wantError  string
	}{
		{name: "missing branch", wantError: "baseBranch is required"},
		{name: "main", baseBranch: "main", wantError: "protected default branch name"},
		{name: "master ref", baseBranch: "refs/heads/master", wantError: "protected default branch name"},
		{name: "trunk remote", baseBranch: "origin/trunk", wantError: "protected default branch name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sampleConfig()
			cfg.LibraryTaskName = mergeMainLibraryTaskName
			cfg.BaseBranch = tt.baseBranch
			fake := &fakeRunner{t: t}

			res := New(fake).Run(context.Background(), cfg)
			if res.ExitCode != ExitConfig {
				t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitConfig)
			}
			if res.Err == nil || !strings.Contains(res.Err.Error(), tt.wantError) {
				t.Fatalf("Run() error = %v, want %q", res.Err, tt.wantError)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("commands = %#v, want none before config rejection", fake.calls)
			}
		})
	}
}

func TestRunRequiredNonDefaultBranchRejectsAgentBranchSwitchBeforeGitWrites(t *testing.T) {
	cfg := sampleConfig()
	cfg.LibraryTaskName = mergeMainLibraryTaskName
	cfg.BaseBranch = "feature/conflicted"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "requiredbranchswitch"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	defaultBranch := execx.Result{Stdout: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n"}
	featureHead := execx.Result{Stdout: "def456\trefs/heads/" + cfg.BaseBranch + "\n"}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: cfg.BaseBranch + "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: "main\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.ExitCode != ExitGit {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitGit)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), `current branch is "main"`) {
		t.Fatalf("Run() error = %v, want branch switch rejection", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	for _, call := range fake.calls {
		if commandsEqual(call, addCommand(repoDir)) || commandsEqual(call, commitCommand(repoDir, cfg.CommitMessage)) || commandsEqual(call, pushCommand(repoDir, "main")) {
			t.Fatalf("unsafe git write command executed after branch switch: %+v", call)
		}
	}
}

func TestRunRequiredNonDefaultBranchKeepsFeaturePublishPinned(t *testing.T) {
	cfg := sampleConfig()
	cfg.LibraryTaskName = mergeMainLibraryTaskName
	cfg.BaseBranch = "feature/conflicted"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "requiredbranchsuccess"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	prURL := "https://github.com/acme/repo/pull/88"
	defaultBranch := execx.Result{Stdout: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n"}
	featureHead := execx.Result{Stdout: "def456\trefs/heads/" + cfg.BaseBranch + "\n"}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: cfg.BaseBranch + "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: cfg.BaseBranch + "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## " + cfg.BaseBranch + "...origin/" + cfg.BaseBranch + "\n M file.go\n"}},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: cfg.BaseBranch + "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: cfg.BaseBranch + "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() error = %v", res.Err)
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
	for _, call := range fake.calls {
		if commandsEqual(call, pushCommand(repoDir, "main")) {
			t.Fatalf("default branch push executed: %+v", call)
		}
	}
}

func TestRunRequiredNonDefaultBranchChecksRemoteDefaultBeforeSandboxRetry(t *testing.T) {
	cfg := sampleConfig()
	cfg.LibraryTaskName = mergeMainLibraryTaskName
	cfg.BaseBranch = "feature/conflicted"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "requiredbranchretry"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	defaultBranch := execx.Result{Stdout: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n"}
	changedDefaultBranch := execx.Result{Stdout: "ref: refs/heads/" + cfg.BaseBranch + "\tHEAD\ndef456\tHEAD\n"}
	featureHead := execx.Result{Stdout: "def456\trefs/heads/" + cfg.BaseBranch + "\n"}
	bwrapFailure := execx.Result{
		Stdout: "Failure: I could not start any local repository command.",
		Stderr: "bwrap: namespace error: Operation not permitted",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteDefaultBranchCommand(repoDir), res: defaultBranch},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: cfg.BaseBranch + "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: featureHead},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)), res: bwrapFailure},
		{cmd: remoteDefaultBranchCommand(repoDir), res: changedDefaultBranch},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.ExitCode != ExitCodex {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitCodex)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "verify required branch before agent sandbox retry") {
		t.Fatalf("Run() error = %v, want pre-retry branch rejection", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	for _, call := range fake.calls {
		if call.Name == "codex" && slicesContains(call.Args, "danger-full-access") {
			t.Fatalf("danger-full-access retry executed after branch drift: %+v", call)
		}
	}
}

func TestRunNonMainBranchReusesExistingBranchAndPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
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
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## release/2026.04-hotfix...origin/release/2026.04-hotfix\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
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

func TestRunNonMainBranchDetectsAgentCommittedAndPushedChange(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	prURL := "https://github.com/acme/repo/pull/77"
	oldHead := "1111111111111111111111111111111111111111"
	newHead := "2222222222222222222222222222222222222222"
	agentOutput := "Commit pushed: `2222222 fix: update branch`"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: oldHead + "\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)), res: execx.Result{Stdout: agentOutput}},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## release/2026.04-hotfix...origin/release/2026.04-hotfix\n"}},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: newHead + "\n"}},
		{cmd: addCommand(repoDir)},
		{
			cmd: commitCommand(repoDir, cfg.CommitMessage),
			res: execx.Result{Stdout: "On branch release/2026.04-hotfix\nnothing to commit, working tree clean\n"},
			err: errors.New("exit status 1"),
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## release/2026.04-hotfix...origin/release/2026.04-hotfix\n"}},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: newHead + "\n"}},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
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
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNonMainBranchTreatsTransientPRLookupAfterPushAsSuccess(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	lookupErr := errors.New("exit status 128")
	lookupRes := execx.Result{
		Stderr: "fatal: unable to access 'https://github.com/acme/repo.git/': Failed to connect to github.com port 443 after 4518 ms: Could not connect to server",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: lookupRes, err: lookupErr},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	if res.PRURL != "" {
		t.Fatalf("PRURL = %q, want empty after transient lookup failure", res.PRURL)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=lookup_existing reason=transient_failed_after_push") {
		t.Fatalf("logs missing transient lookup warning:\n%s", strings.Join(logs, "\n"))
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunTracksCurrentBranchFromLocalGitStatus(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	createdBranch := "moltenhub-build-api"
	activeBranch := "moltenhub-build-api-refined"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, createdBranch)},
		{cmd: pushDryRunCommand(repoDir, createdBranch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api-refined...origin/moltenhub-build-api-refined\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, activeBranch)},
		{cmd: prCreateCommand(repoDir, cfg, activeBranch), res: execx.Result{Stdout: "https://github.com/acme/repo/pull/42\n"}},
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
	if got, want := res.Branch, activeBranch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.RepoResults[0].Branch, activeBranch; got != want {
		t.Fatalf("RepoResults[0].Branch = %q, want %q", got, want)
	}
}

func TestRunNonMainBranchPushNonFastForwardRetriesWithMergeSync(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
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
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: fetchBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: mergeFetchedBranchCommand(repoDir)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
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

func TestRunNonMainBranchPushSyncMergeConflictInvokesAgentAndRetries(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)

	pushRejected := execx.Result{
		Stderr: "! [rejected]        release/2026.04-hotfix -> release/2026.04-hotfix (fetch first)\n",
	}
	mergeConflict := execx.Result{
		Stdout: "Auto-merging test/worker.test.js\nCONFLICT (content): Merge conflict in test/worker.test.js\n",
		Stderr: "Automatic merge failed; fix conflicts and then commit the result.\n",
	}
	remoteSyncPrompt := remoteBranchSyncConflictPrompt(
		withAgentsPrompt(cfg.Prompt, agentsPath),
		"repo",
		cfg.RepoURL,
		cfg.BaseBranch,
		publishRemoteOrigin,
		mergeConflict,
	)
	syncCommitMessage := remoteBranchSyncCommitMessage(cfg.BaseBranch)
	prURL := "https://github.com/acme/repo/pull/77"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: fetchBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: mergeFetchedBranchCommand(repoDir), res: mergeConflict, err: errors.New("exit status 1")},
		{cmd: codexCommand(targetDir, remoteSyncPrompt)},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M test/worker.test.js\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, syncCommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
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
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitSuccess)
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
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

func TestRunRemoteWriteAccessFailureStopsBeforeCodex(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.RepoURL = "git@gitlab.com:acme/repo.git"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\nfatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch), res: push403, err: errors.New("exit status 128")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("expected error, got nil")
	}
	if res.ExitCode != ExitGit {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitGit)
	}
	if !strings.Contains(res.Err.Error(), "prepare fork fallback for repo") {
		t.Fatalf("error = %v, want fork-fallback context", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "non-GitHub repo") {
		t.Fatalf("error = %v, want non-GitHub context", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "Write access to repository not granted") {
		t.Fatalf("error = %v, want remote error detail", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestPreparePublishWorkflowDirectWriteReady(t *testing.T) {
	t.Parallel()

	repoDir := "/tmp/repo"
	branch := "moltenhub-build-api"
	repo := repoWorkspace{
		URL:    "git@github.com:acme/repo.git",
		Dir:    repoDir,
		RelDir: "repo",
		Branch: branch,
	}
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: pushDryRunCommand(repoDir, branch)},
	}}

	h := New(fake)
	if err := h.preparePublishWorkflow(context.Background(), &repo); err != nil {
		t.Fatalf("preparePublishWorkflow() err = %v", err)
	}
	if !repo.WriteAccessChecked || !repo.WriteAccessAllowed {
		t.Fatalf("write access flags = checked:%t allowed:%t, want checked+allowed", repo.WriteAccessChecked, repo.WriteAccessAllowed)
	}
	if repo.WriteAccessErr != nil {
		t.Fatalf("WriteAccessErr = %v, want nil", repo.WriteAccessErr)
	}
	if got, want := repo.PushRemote, publishRemoteOrigin; got != want {
		t.Fatalf("PushRemote = %q, want %q", got, want)
	}
	if got, want := repo.PublishStrategy, publishStrategyDirect; got != want {
		t.Fatalf("PublishStrategy = %q, want %q", got, want)
	}
	if got, want := repo.PRHeadRef, branch; got != want {
		t.Fatalf("PRHeadRef = %q, want %q", got, want)
	}
	if got := repo.PRTargetRepo; got != "" {
		t.Fatalf("PRTargetRepo = %q, want empty", got)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunPublicGitHubDeniedDirectAccessUsesForkFallback(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\n" +
			"fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403\n",
	}
	prURL := "https://github.com/acme/repo/pull/42"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch), res: push403, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDir, "acme/repo"), res: execx.Result{Stdout: `{"isPrivate":false,"nameWithOwner":"acme/repo"}`}},
		{cmd: ghViewerLoginCommand(repoDir), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: ghRepoForkCommand(repoDir, "acme/repo")},
		{cmd: gitRemoteSetURLCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git"), res: execx.Result{Stderr: "error: No such remote 'fork'\n"}, err: errors.New("exit status 2")},
		{cmd: gitRemoteAddCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git")},
		{cmd: pushDryRunToRemoteCommand(repoDir, publishRemoteFork, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushToRemoteCommand(repoDir, publishRemoteFork, branch)},
		{cmd: prCreateWithOptionsCommand(repoDir, cfg, cfg.BaseBranch, "octocat:"+branch, "acme/repo"), res: execx.Result{Stdout: prURL + "\n"}},
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
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitSuccess)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if got, want := res.RepoResults[0].Branch, branch; got != want {
		t.Fatalf("RepoResults[0].Branch = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestPreparePublishWorkflowForkAlreadyExistsIsIdempotent(t *testing.T) {
	t.Parallel()

	repoDir := "/tmp/repo"
	branch := "moltenhub-build-api"
	repo := repoWorkspace{
		URL:    "git@github.com:acme/repo.git",
		Dir:    repoDir,
		RelDir: "repo",
		Branch: branch,
	}
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\n" +
			"fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: pushDryRunCommand(repoDir, branch), res: push403, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDir, "acme/repo"), res: execx.Result{Stdout: `{"isPrivate":false,"nameWithOwner":"acme/repo"}`}},
		{cmd: ghViewerLoginCommand(repoDir), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: ghRepoForkCommand(repoDir, "acme/repo"), res: execx.Result{Stderr: "a fork already exists"}, err: errors.New("exit status 1")},
		{cmd: gitRemoteSetURLCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git")},
		{cmd: pushDryRunToRemoteCommand(repoDir, publishRemoteFork, branch)},
	}}

	h := New(fake)
	if err := h.preparePublishWorkflow(context.Background(), &repo); err != nil {
		t.Fatalf("preparePublishWorkflow() err = %v", err)
	}
	if got, want := repo.PushRemote, publishRemoteFork; got != want {
		t.Fatalf("PushRemote = %q, want %q", got, want)
	}
	if got, want := repo.PublishStrategy, publishStrategyForkFallback; got != want {
		t.Fatalf("PublishStrategy = %q, want %q", got, want)
	}
	if got, want := repo.PRHeadRef, "octocat:"+branch; got != want {
		t.Fatalf("PRHeadRef = %q, want %q", got, want)
	}
	if got, want := repo.PRTargetRepo, "acme/repo"; got != want {
		t.Fatalf("PRTargetRepo = %q, want %q", got, want)
	}
	if !repo.WriteAccessChecked || !repo.WriteAccessAllowed {
		t.Fatalf("write access flags = checked:%t allowed:%t, want checked+allowed", repo.WriteAccessChecked, repo.WriteAccessAllowed)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestPreparePublishWorkflowGitHubSSHPermissionDeniedUsesForkFallback(t *testing.T) {
	t.Parallel()

	repoDir := "/tmp/repo"
	branch := "moltenhub-build-api"
	repo := repoWorkspace{
		URL:    "git@github.com:acme/repo.git",
		Dir:    repoDir,
		RelDir: "repo",
		Branch: branch,
	}
	sshDenied := execx.Result{
		Stderr: "ERROR: Permission to acme/repo.git denied to octocat.\n" +
			"fatal: Could not read from remote repository.\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: pushDryRunCommand(repoDir, branch), res: sshDenied, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDir, "acme/repo"), res: execx.Result{Stdout: `{"isPrivate":false,"nameWithOwner":"acme/repo"}`}},
		{cmd: ghViewerLoginCommand(repoDir), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: ghRepoForkCommand(repoDir, "acme/repo")},
		{cmd: gitRemoteSetURLCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git"), res: execx.Result{Stderr: "error: No such remote 'fork'\n"}, err: errors.New("exit status 2")},
		{cmd: gitRemoteAddCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git")},
		{cmd: pushDryRunToRemoteCommand(repoDir, publishRemoteFork, branch)},
	}}

	h := New(fake)
	if err := h.preparePublishWorkflow(context.Background(), &repo); err != nil {
		t.Fatalf("preparePublishWorkflow() err = %v", err)
	}
	if got, want := repo.PushRemote, publishRemoteFork; got != want {
		t.Fatalf("PushRemote = %q, want %q", got, want)
	}
	if got, want := repo.PublishStrategy, publishStrategyForkFallback; got != want {
		t.Fatalf("PublishStrategy = %q, want %q", got, want)
	}
	if got, want := repo.PRHeadRef, "octocat:"+branch; got != want {
		t.Fatalf("PRHeadRef = %q, want %q", got, want)
	}
	if got, want := repo.PRTargetRepo, "acme/repo"; got != want {
		t.Fatalf("PRTargetRepo = %q, want %q", got, want)
	}
	if !repo.WriteAccessChecked || !repo.WriteAccessAllowed {
		t.Fatalf("write access flags = checked:%t allowed:%t, want checked+allowed", repo.WriteAccessChecked, repo.WriteAccessAllowed)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestPreparePublishWorkflowForkFallbackSwitchesToHTTPSWhenSSHForkProbeFailsWithToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "github_token_example")
	t.Setenv("GITHUB_TOKEN", "")

	repoDir := "/tmp/repo"
	branch := "moltenhub-build-api"
	repo := repoWorkspace{
		URL:    "git@github.com:acme/repo.git",
		Dir:    repoDir,
		RelDir: "repo",
		Branch: branch,
	}
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\n" +
			"fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403\n",
	}
	sshAuthFailure := execx.Result{
		Stderr: "git@github.com: Permission denied (publickey).\n" +
			"fatal: Could not read from remote repository.\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: pushDryRunCommand(repoDir, branch), res: push403, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDir, "acme/repo"), res: execx.Result{Stdout: `{"isPrivate":false,"nameWithOwner":"acme/repo"}`}},
		{cmd: ghViewerLoginCommand(repoDir), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: ghRepoForkCommand(repoDir, "acme/repo")},
		{cmd: gitRemoteSetURLCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git"), res: execx.Result{Stderr: "error: No such remote 'fork'\n"}, err: errors.New("exit status 2")},
		{cmd: gitRemoteAddCommand(repoDir, publishRemoteFork, "git@github.com:octocat/repo.git")},
		{cmd: pushDryRunToRemoteCommand(repoDir, publishRemoteFork, branch), res: sshAuthFailure, err: errors.New("exit status 128")},
		{cmd: gitRemoteSetURLCommand(repoDir, publishRemoteFork, "https://github.com/octocat/repo.git")},
		{cmd: pushDryRunToRemoteCommand(repoDir, publishRemoteFork, branch)},
	}}

	h := New(fake)
	if err := h.preparePublishWorkflow(context.Background(), &repo); err != nil {
		t.Fatalf("preparePublishWorkflow() err = %v", err)
	}
	if got, want := repo.PushRemote, publishRemoteFork; got != want {
		t.Fatalf("PushRemote = %q, want %q", got, want)
	}
	if got, want := repo.PublishStrategy, publishStrategyForkFallback; got != want {
		t.Fatalf("PublishStrategy = %q, want %q", got, want)
	}
	if got, want := repo.PRHeadRef, "octocat:"+branch; got != want {
		t.Fatalf("PRHeadRef = %q, want %q", got, want)
	}
	if got, want := repo.PRTargetRepo, "acme/repo"; got != want {
		t.Fatalf("PRTargetRepo = %q, want %q", got, want)
	}
	if !repo.WriteAccessChecked || !repo.WriteAccessAllowed {
		t.Fatalf("write access flags = checked:%t allowed:%t, want checked+allowed", repo.WriteAccessChecked, repo.WriteAccessAllowed)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunPrivateGitHubDeniedDirectAccessFailsBeforeCodex(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\n" +
			"fatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch), res: push403, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDir, "acme/repo"), res: execx.Result{Stdout: `{"isPrivate":true,"nameWithOwner":"acme/repo"}`}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("expected error, got nil")
	}
	if res.ExitCode != ExitGit {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitGit)
	}
	if !strings.Contains(res.Err.Error(), "private") {
		t.Fatalf("error = %v, want private repo context", res.Err)
	}
	if strings.Contains(res.Err.Error(), "codex") {
		t.Fatalf("error = %v, want failure before agent stage", res.Err)
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
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

func TestRunNoChangesWithConcreteTaskRepoEvidenceRecordsEvidence(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)),
			res: execx.Result{Stdout: strings.Join([]string{
				"No repo diff. Evidence says requested page styling move is already satisfied.",
				"- Global CSS already lives in [src/style.css](" + filepath.ToSlash(filepath.Join(repoDir, "src/style.css")) + ":1).",
				"- Loaded once from [src/main.ts](" + filepath.ToSlash(filepath.Join(repoDir, "src/main.ts")) + ":3).",
				"- [src/App.vue](" + filepath.ToSlash(filepath.Join(repoDir, "src/App.vue")) + ":1) has no page style block.",
			}, "\n")},
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if !res.NoChanges {
		t.Fatal("NoChanges = false, want true")
	}
	if !res.NoChangeEvidence {
		t.Fatal("NoChangeEvidence = false, want true")
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesFollowUpWithoutConcreteEvidenceFails(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = strings.Join([]string{
		"Review the previous local task logs first.",
		"Identify every root cause behind the no-change result, fix the underlying MoltenHub Code application issue in this repository, validate locally where possible, and summarize the verified results.",
		"Only return a no-op if you can cite concrete repository evidence that no MoltenHub Code change is required; otherwise produce the smallest correct diff or return an explicit failure with blocker details.",
	}, " ")
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := slug.BranchName(cfg.Prompt, now, guid)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)),
			res: execx.Result{Stdout: strings.Join([]string{
				"No repo diff. Existing code already covers reported failure path.",
				"Validated: `go test ./...` passed.",
			}, "\n")},
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want no-change follow-up failure")
	}
	if res.ExitCode != ExitCodex {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitCodex)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if !strings.Contains(res.Err.Error(), "no concrete MoltenHub Code evidence") {
		t.Fatalf("Run() err = %v, want concrete evidence detail", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesFollowUpWithConcreteEvidenceAllowsNoChanges(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = strings.Join([]string{
		"Review the previous local task logs first.",
		"Identify every root cause behind the no-change result, fix the underlying MoltenHub Code application issue in this repository, validate locally where possible, and summarize the verified results.",
		"Only return a no-op if you can cite concrete repository evidence that no MoltenHub Code change is required; otherwise produce the smallest correct diff or return an explicit failure with blocker details.",
	}, " ")
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := slug.BranchName(cfg.Prompt, now, guid)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)),
			res: execx.Result{Stdout: strings.Join([]string{
				"No repository changes required.",
				"Concrete evidence: [internal/hub/daemon.go](" + filepath.ToSlash(filepath.Join(repoDir, "internal/hub/daemon.go")) + ":2210) already skips failure follow-up loops.",
			}, "\n")},
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
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
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitSuccess)
	}
	if !res.NoChanges {
		t.Fatal("NoChanges = false, want true")
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesWithDeletionNoOpEvidenceAllowsNoChanges(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "remove image folders and files from public/knowledge-base where the knowledge base article does not exist in the json file (any more)"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := slug.BranchName(cfg.Prompt, now, guid)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)),
			res: execx.Result{Stdout: strings.Join([]string{
				"No deletion needed. `public/knowledge-base` folders all match `num` entries in [articles.json](" + filepath.ToSlash(filepath.Join(repoDir, "src/data/knowledge-base/articles.json")) + ").",
				"Verification:",
				"- Orphan check: `orphan folders: (none)`",
				"- `npm run validate`: passed",
				"- Git diff: none",
			}, "\n")},
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
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
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitSuccess)
	}
	if !res.NoChanges {
		t.Fatal("NoChanges = false, want true")
	}
	if !res.NoChangeEvidence {
		t.Fatal("NoChangeEvidence = false, want true")
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesFollowUpWithOnlyOriginalRepoEvidenceFails(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = strings.Join([]string{
		"Review the previous local task logs first.",
		"Identify every root cause behind the no-change result, fix the underlying MoltenHub Code application issue in this repository, validate locally where possible, and summarize the verified results.",
		"Only return a no-op if you can cite concrete repository evidence that no MoltenHub Code change is required; otherwise produce the smallest correct diff or return an explicit failure with blocker details.",
	}, " ")
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := slug.BranchName(cfg.Prompt, now, guid)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)),
			res: execx.Result{Stdout: strings.Join([]string{
				"No code change. Request already satisfied.",
				"Evidence:",
				"- [index.html](/workspace/moltenhub-code/tasks/original/repo/index.html:9): SEO description set.",
				"- [src/pageTitle.js](/workspace/moltenhub-code/tasks/original/repo/src/pageTitle.js:1): shared runtime metadata constants set.",
				"Git diff empty. Only untracked `.moltenhub-agents-3149905286.md` exists from harness instructions.",
			}, "\n")},
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want no-change follow-up failure")
	}
	if res.ExitCode != ExitCodex {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitCodex)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if !strings.Contains(res.Err.Error(), "no concrete MoltenHub Code evidence") {
		t.Fatalf("Run() err = %v, want concrete MoltenHub Code evidence detail", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunAgentClaimsChangesButGitIsCleanFails(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	agentOutput := strings.Join([]string{
		"Changed [AGENTS.md](" + filepath.ToSlash(filepath.Join(repoDir, "AGENTS.md")) + ":1).",
		"diff --git a/AGENTS.md b/AGENTS.md",
		"--- a/AGENTS.md",
		"+++ b/AGENTS.md",
		"+# Project Agent Guide",
	}, "\n")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)), res: execx.Result{Stdout: agentOutput}},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## " + branch + "\n"}},
		{cmd: commitsAheadOfBaseCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "0\n"}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want codex mismatch failure")
	}
	if res.ExitCode != ExitCodex {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitCodex)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if !strings.Contains(res.Err.Error(), "reported file changes") {
		t.Fatalf("Run() err = %v, want reported file changes detail", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunAgentAwaitingTaskReturnsCodexFailure(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath)),
			res: execx.Result{Stdout: "AGENTS.md rules active. Caveman full active. Send task."},
		},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want codex failure")
	}
	if res.ExitCode != ExitCodex {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitCodex)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if !strings.Contains(res.Err.Error(), "agent did not identify an implementation target") {
		t.Fatalf("Run() err = %v, want missing implementation target detail", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesOnMainReportsExistingPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/123"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch), res: execx.Result{Stdout: "abc123\trefs/heads/" + branch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, branch), res: execx.Result{Stdout: "[{\"url\":\"" + prURL + "\"}]\n"}},
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
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(res.RepoResults) != 1 {
		t.Fatalf("RepoResults length = %d, want 1", len(res.RepoResults))
	}
	if got, want := res.RepoResults[0].PRURL, prURL; got != want {
		t.Fatalf("RepoResults[0].PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNoChangesReportsMergedPRWhenBranchNoLongerExists(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/123"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch), res: execx.Result{Stdout: ""}},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch), res: execx.Result{Stdout: "[{\"url\":\"" + prURL + "\"}]\n"}},
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
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(res.RepoResults) != 1 {
		t.Fatalf("RepoResults length = %d, want 1", len(res.RepoResults))
	}
	if got, want := res.RepoResults[0].PRURL, prURL; got != want {
		t.Fatalf("RepoResults[0].PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunNonMainBranchNoChangesReportsExistingPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/2026.04-hotfix"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
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
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: prURL + "\n"}},
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
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(res.RepoResults) != 1 {
		t.Fatalf("RepoResults length = %d, want 1", len(res.RepoResults))
	}
	if got, want := res.RepoResults[0].PRURL, prURL; got != want {
		t.Fatalf("RepoResults[0].PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunExistingPRNoChangesUsesPromptPRURLWhenHeadLookupIsEmpty(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "moltenhub-existing-pr"
	prURL := "https://github.com/acme/repo/pull/117"
	cfg.Prompt = strings.Join([]string{
		"Update the existing pull request to address review feedback.",
		"Pull request:",
		"- URL: " + prURL,
		"- Head branch to update: moltenhub-existing-pr",
		"- Base branch: main",
	}, "\n")
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "[]\n"}},
		{cmd: prLookupAnyByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "[]\n"}},
		{cmd: prStateViewCommand(repoDir, prURL), res: execx.Result{Stdout: `{"url":"` + prURL + `","state":"MERGED","mergedAt":"2026-04-02T15:00:00Z","headRefName":"` + cfg.BaseBranch + `"}`}},
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
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if got, want := res.RepoResults[0].PRURL, prURL; got != want {
		t.Fatalf("RepoResults[0].PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunExistingPRNoChangesRejectsPromptPRURLWhenHeadBranchDiffers(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "moltenhub-existing-pr"
	prURL := "https://github.com/acme/repo/pull/117"
	cfg.Prompt = strings.Join([]string{
		"Update the existing pull request to address review feedback.",
		"Pull request:",
		"- URL: " + prURL,
		"- Head branch to update: moltenhub-existing-pr",
		"- Base branch: main",
	}, "\n")
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "[]\n"}},
		{cmd: prLookupAnyByHeadCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "[]\n"}},
		{cmd: prStateViewCommand(repoDir, prURL), res: execx.Result{Stdout: `{"url":"` + prURL + `","state":"OPEN","headRefName":"moltenhub-other-pr"}`}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want prompt PR branch mismatch")
	}
	if res.ExitCode != ExitPR {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitPR)
	}
	if res.PRURL != "" {
		t.Fatalf("PRURL = %q, want empty", res.PRURL)
	}
	if !strings.Contains(res.Err.Error(), `prompt PR head branch "moltenhub-other-pr" did not match`) {
		t.Fatalf("Run() err = %v, want prompt PR branch mismatch", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunExistingPRNoChangesFailsWhenPRLookupFails(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "moltenhub-existing-pr"
	cfg.Prompt = strings.Join([]string{
		"Update the existing pull request to address review feedback.",
		"Pull request:",
		"- Head branch to update: moltenhub-existing-pr",
		"- Base branch: main",
	}, "\n")
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	lookupRes := execx.Result{Stderr: "HTTP 401: Bad credentials (https://api.github.com/graphql)\nTry authenticating with:  gh auth login\n"}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
		{cmd: prLookupByHeadCommand(repoDir, cfg.BaseBranch), res: lookupRes, err: errors.New("exit status 1")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want PR lookup failure")
	}
	if res.ExitCode != ExitPR {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitPR)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false")
	}
	if !strings.Contains(res.Err.Error(), "verify existing pull request") ||
		!strings.Contains(res.Err.Error(), "Bad credentials") {
		t.Fatalf("Run() err = %v, want PR verification auth detail", res.Err)
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"

	checkSummary := "X unit-tests failing"
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
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

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	joinedLogs := strings.Join(logs, "\n")
	for _, want := range []string{
		"mode=remediation",
		"agent_run_id=agent-remediation-repo-1",
		"repo=repo",
		"repo_dir=repo",
	} {
		if !strings.Contains(joinedLogs, want) {
			t.Fatalf("logs missing remediation workflow metadata %q:\n%s", want, joinedLogs)
		}
	}
}

func TestRunTreatsChecksWatchTimeoutWithAllChecksPassingAsSuccess(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	checkSnapshot := `[
		{"name":"build","bucket":"pass","completedAt":"2026-04-02T15:05:00Z"},
		{"name":"deploy","bucket":"pass","completedAt":"2026-04-02T15:05:01Z"}
	]`

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), err: errPRChecksWatchTimeout},
		{cmd: prChecksJSONCommand(repoDir, prURL, true), res: execx.Result{Stdout: checkSnapshot + "\n"}},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "stage=checks status=warn action=watch_timeout") {
		t.Fatalf("logs missing watch timeout warning:\n%s", joinedLogs)
	}
	if !strings.Contains(joinedLogs, "stage=checks status=ok reason=watch_timeout") {
		t.Fatalf("logs missing watch timeout completion:\n%s", joinedLogs)
	}
}

func TestRunTreatsChecksAuthFailureAfterPRCreateAsSuccess(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	checksStderr := "HTTP 401: Requires authentication (https://api.github.com/graphql)\nTry authenticating with:  gh auth login"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: checksStderr}, err: errors.New("checks unavailable")},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "action=watch_skipped reason=github_auth_unavailable") {
		t.Fatalf("logs missing checks auth warning:\n%s", joinedLogs)
	}
}

func TestRunTreatsChecksWatchTimeoutWithPendingChecksAsFailure(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	checkSnapshot := `[
		{"name":"build","bucket":"pass","completedAt":"2026-04-02T15:05:00Z"},
		{"name":"deploy","bucket":"pending","startedAt":"2026-04-02T15:05:01Z"}
	]`
	checkSummary := "build\tpass\ndeploy\tpending"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), err: errPRChecksWatchTimeout},
		{cmd: prChecksJSONCommand(repoDir, prURL, true), res: execx.Result{Stdout: checkSnapshot + "\n"}},
		{cmd: codexCommand(targetDir, remediationPrompt(withAgentsPrompt(cfg.Prompt, agentsPath), prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	if res.PRURL != prURL {
		t.Fatalf("PRURL = %q, want %q", res.PRURL, prURL)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "stage=checks status=warn action=watch_timeout") {
		t.Fatalf("logs missing watch timeout warning:\n%s", joinedLogs)
	}
	if strings.Contains(joinedLogs, "stage=checks status=ok reason=watch_timeout") {
		t.Fatalf("logs unexpectedly marked watch timeout ok:\n%s", joinedLogs)
	}
}

func TestRunTreatsChecksWatchTimeoutSnapshotQueryFailureAsFailure(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	checkSummary := "No check output was provided by gh."

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), err: errPRChecksWatchTimeout},
		{cmd: prChecksJSONCommand(repoDir, prURL, true), err: errors.New("checks snapshot unavailable")},
		{cmd: codexCommand(targetDir, remediationPrompt(withAgentsPrompt(cfg.Prompt, agentsPath), prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "stage=checks status=warn action=watch_timeout_snapshot reason=query_failed") {
		t.Fatalf("logs missing snapshot query failure:\n%s", joinedLogs)
	}
	if strings.Contains(joinedLogs, "stage=checks status=ok reason=watch_timeout") {
		t.Fatalf("logs unexpectedly marked watch timeout ok:\n%s", joinedLogs)
	}
}

func TestRunTreatsAnyChecksWatchTimeoutSnapshotQueryFailureAsFailure(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noRequired := "no required checks reported on the 'moltenhub-build-api' branch"
	checkSummary := "No check output was provided by gh."

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noRequired + "\n"}, err: errors.New("checks unavailable")},
		{cmd: requiredStatusChecksCommand(repoDir, "acme/repo", "main"), res: execx.Result{Stdout: `{"contexts":["test"],"checks":[]}`}},
		{cmd: prChecksAnyCommand(repoDir, prURL), err: errPRChecksWatchTimeout},
		{cmd: prChecksJSONCommand(repoDir, prURL, false), err: errors.New("checks snapshot unavailable")},
		{cmd: codexCommand(targetDir, remediationPrompt(withAgentsPrompt(cfg.Prompt, agentsPath), prURL, checkSummary, 1))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "\n"}},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

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
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "stage=checks status=warn action=watch_timeout_snapshot reason=query_failed") {
		t.Fatalf("logs missing snapshot query failure:\n%s", joinedLogs)
	}
	if strings.Contains(joinedLogs, "stage=checks status=ok reason=watch_timeout") {
		t.Fatalf("logs unexpectedly marked watch timeout ok:\n%s", joinedLogs)
	}
}

func TestRemediationPromptUsesCIFixLibraryTask(t *testing.T) {
	t.Parallel()

	prompt := remediationPrompt("Original task prompt", "https://github.com/acme/repo/pull/42", "unit tests failed", 1)

	for _, want := range []string{
		"You are a senior software engineer fixing pull-request CI failures.",
		"Check PR CI status with gh.",
		"Remediation round 1/3.",
		"PR CI/CD checks are failing right now.",
		"unit tests failed",
		"Original task context:\nOriginal task prompt",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("remediationPrompt() missing %q in:\n%s", want, prompt)
		}
	}
}

func TestRunFailedChecksWithStaleFailureSnapshotPasses(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"

	checkOutput := strings.Join([]string{
		"Build and test\tfail\t23s\thttps://github.com/acme/repo/actions/runs/1/job/11",
		"Build and test\tpass\t22s\thttps://github.com/acme/repo/actions/runs/2/job/22",
	}, "\n")
	snapshotJSON := `[
		{"name":"Build and test","bucket":"fail","completedAt":"2026-04-02T15:00:00Z","startedAt":"2026-04-02T14:59:00Z"},
		{"name":"Build and test","bucket":"pass","completedAt":"2026-04-02T15:01:00Z","startedAt":"2026-04-02T15:00:15Z"}
	]`

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stdout: checkOutput + "\n"}, err: errors.New("checks failed")},
		{cmd: prChecksJSONCommand(repoDir, prURL, false), res: execx.Result{Stdout: snapshotJSON + "\n"}},
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

func TestRunFailedChecksFallsBackToTextSnapshotWhenGHJSONUnsupported(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"

	checkOutput := strings.Join([]string{
		"Build and test\tfail\t23s\thttps://github.com/acme/repo/actions/runs/1/job/11",
		"Build and test\tpass\t22s\thttps://github.com/acme/repo/actions/runs/2/job/22",
	}, "\n")
	unsupportedJSON := strings.Join([]string{
		"unknown flag: --json",
		"Usage:  gh pr checks [<number> | <url> | <branch>] [flags]",
	}, "\n")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stdout: checkOutput + "\n"}, err: errors.New("checks failed")},
		{cmd: prChecksJSONCommand(repoDir, prURL, false), res: execx.Result{Stdout: checkOutput + "\n", Stderr: unsupportedJSON}, err: errors.New("exit status 1")},
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

func TestLatestCheckSnapshotFromTextUsesLastReportedState(t *testing.T) {
	t.Parallel()

	raw := strings.Join([]string{
		"Refreshing checks status every 10 seconds. Press Ctrl+C to quit.",
		"",
		"Build and validate\tpending\t0\thttps://github.com/acme/repo/actions/runs/1/job/1",
		"Workers Builds: design\tpending\t0\thttps://dash.cloudflare.com/account/workers/services/view/design/production/builds/1",
		"Refreshing checks status every 10 seconds. Press Ctrl+C to quit.",
		"",
		"Workers Builds: design\tfail\t0\thttps://dash.cloudflare.com/account/workers/services/view/design/production/builds/1",
		"Release to Cloudflare\tskipping\t0\thttps://github.com/acme/repo/actions/runs/1/job/2",
		"Build and validate\tpass\t51s\thttps://github.com/acme/repo/actions/runs/1/job/1",
	}, "\n")

	snapshot, err := latestCheckSnapshotFromText(raw)
	if err != nil {
		t.Fatalf("latestCheckSnapshotFromText() err = %v", err)
	}
	if snapshot.AllPassing {
		t.Fatal("AllPassing = true, want false")
	}
	if !snapshot.HasFailures {
		t.Fatal("HasFailures = false, want true")
	}
	wantSummary := strings.Join([]string{
		"Build and validate\tpass",
		"Release to Cloudflare\tskipping",
		"Workers Builds: design\tfail",
	}, "\n")
	if snapshot.Summary != wantSummary {
		t.Fatalf("Summary = %q, want %q", snapshot.Summary, wantSummary)
	}
}

func TestRunFailedChecksWithNoRemediationChangesFails(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"

	checkSummary := "X unit-tests failing"
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
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
	if res.PRURL != prURL {
		t.Fatalf("PRURL = %q, want %q", res.PRURL, prURL)
	}
	if len(res.RepoResults) != 1 {
		t.Fatalf("RepoResults len = %d, want 1", len(res.RepoResults))
	}
	if got := res.RepoResults[0].PRURL; got != prURL {
		t.Fatalf("RepoResults[0].PRURL = %q, want %q", got, prURL)
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'moltenhub-build-api' branch"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prStateViewCommand(repoDir, prURL), res: execx.Result{Stdout: `{"url":"` + prURL + `","state":"OPEN"}`}},
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'moltenhub-build-api' branch"

	exps := []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prStateViewCommand(repoDir, prURL), res: execx.Result{Stdout: `{"url":"` + prURL + `","state":"OPEN"}`}},
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
		expectedRun{cmd: requiredStatusChecksCommand(repoDir, "acme/repo", "main"), res: execx.Result{Stdout: `{"contexts":["ci/test"],"checks":[]}`}},
		expectedRun{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "abcdef123456\n"}},
		expectedRun{cmd: workflowDispatchRunsCommand(repoDir, branch), res: execx.Result{Stdout: "[]\n"}},
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

func TestRunNoChecksReportedResolvesFromWorkflowDispatchSnapshot(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'moltenhub-build-api' branch"

	exps := []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prStateViewCommand(repoDir, prURL), res: execx.Result{Stdout: `{"url":"` + prURL + `","state":"OPEN"}`}},
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
		expectedRun{cmd: requiredStatusChecksCommand(repoDir, "acme/repo", "main"), res: execx.Result{Stdout: `{"contexts":["ci/test"],"checks":[]}`}},
		expectedRun{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "deadbeef\n"}},
		expectedRun{
			cmd: workflowDispatchRunsCommand(repoDir, branch),
			res: execx.Result{Stdout: `[{"status":"completed","conclusion":"success","workflowName":"CI","displayTitle":"CI","headSha":"deadbeef"}]`},
		},
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

func TestRunNoChecksReportedWithNoRequiredStatusChecksPasses(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noChecks := "no checks reported on the 'moltenhub-build-api' branch"

	exps := []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noChecks + "\n"}, err: errors.New("checks unavailable")},
		{cmd: prStateViewCommand(repoDir, prURL), res: execx.Result{Stdout: `{"url":"` + prURL + `","state":"OPEN"}`}},
		{cmd: workflowDispatchCommand(repoDir, branch), res: execx.Result{Stderr: "HTTP 422: Workflow does not have 'workflow_dispatch' trigger"}, err: errors.New("workflow dispatch failed")},
	}
	for i := 1; i <= maxPRChecksNoReportRetries; i++ {
		exps = append(exps, expectedRun{
			cmd: prChecksCommand(repoDir, prURL),
			res: execx.Result{Stderr: noChecks + "\n"},
			err: errors.New("checks unavailable"),
		})
	}
	exps = append(exps, expectedRun{
		cmd: requiredStatusChecksCommand(repoDir, "acme/repo", "main"),
		res: execx.Result{Stderr: "HTTP 404: Branch not protected"},
		err: errors.New("branch protection unavailable"),
	})

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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noRequired := "no required checks reported on the 'moltenhub-build-api' branch"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noRequired + "\n"}, err: errors.New("checks unavailable")},
		{cmd: requiredStatusChecksCommand(repoDir, "acme/repo", "main"), res: execx.Result{Stdout: `{"contexts":["test"],"checks":[]}`}},
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

func TestRunNoRequiredChecksWithNoConfiguredRequiredChecksPasses(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/42"
	noRequired := "no required checks reported on the 'moltenhub-build-api' branch"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL), res: execx.Result{Stderr: noRequired + "\n"}, err: errors.New("checks unavailable")},
		{
			cmd: requiredStatusChecksCommand(repoDir, "acme/repo", "main"),
			res: execx.Result{Stderr: "HTTP 404: Branch not protected"},
			err: errors.New("branch protection unavailable"),
		},
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	branch := "moltenhub-build-api"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	codexPrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, []repoWorkspace{
		{URL: cfg.Repos[0], RelDir: repoRelA},
		{URL: cfg.Repos[1], RelDir: repoRelB},
	})
	codexPrompt = withAgentsPrompt(codexPrompt, agentsPath)

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: pushDryRunCommand(repoDirA, branch)},
		{cmd: pushDryRunCommand(repoDirB, branch)},
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

func TestRunMultiRepoMixedDirectAndForkFallbackResolvesBeforeAgent(t *testing.T) {
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	branch := "moltenhub-build-api"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	codexPrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, []repoWorkspace{
		{URL: cfg.Repos[0], RelDir: repoRelA},
		{URL: cfg.Repos[1], RelDir: repoRelB},
	})
	codexPrompt = withAgentsPrompt(codexPrompt, agentsPath)
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\n" +
			"fatal: unable to access 'https://github.com/acme/repo-b.git/': The requested URL returned error: 403\n",
	}
	prURLA := "https://github.com/acme/repo-a/pull/10"
	prURLB := "https://github.com/acme/repo-b/pull/20"

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: pushDryRunCommand(repoDirA, branch)},
		{cmd: pushDryRunCommand(repoDirB, branch), res: push403, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDirB, "acme/repo-b"), res: execx.Result{Stdout: `{"isPrivate":false,"nameWithOwner":"acme/repo-b"}`}},
		{cmd: ghViewerLoginCommand(repoDirB), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: ghRepoForkCommand(repoDirB, "acme/repo-b")},
		{cmd: gitRemoteSetURLCommand(repoDirB, publishRemoteFork, "git@github.com:octocat/repo-b.git"), res: execx.Result{Stderr: "error: No such remote 'fork'\n"}, err: errors.New("exit status 2")},
		{cmd: gitRemoteAddCommand(repoDirB, publishRemoteFork, "git@github.com:octocat/repo-b.git")},
		{cmd: pushDryRunToRemoteCommand(repoDirB, publishRemoteFork, branch)},
		{cmd: codexCommandWithOptions(runDir, codexPrompt, codexRunOptions{SkipGitRepoCheck: true})},
		{cmd: statusCommand(repoDirA), res: execx.Result{Stdout: " M file-a.go\n"}},
		{cmd: statusCommand(repoDirB), res: execx.Result{Stdout: " M file-b.go\n"}},
		{cmd: addCommand(repoDirA)},
		{cmd: commitCommand(repoDirA, cfg.CommitMessage)},
		{cmd: pushCommand(repoDirA, branch)},
		{cmd: prCreateCommand(repoDirA, cfg, branch), res: execx.Result{Stdout: prURLA + "\n"}},
		{cmd: prChecksCommand(repoDirA, prURLA)},
		{cmd: addCommand(repoDirB)},
		{cmd: commitCommand(repoDirB, cfg.CommitMessage)},
		{cmd: pushToRemoteCommand(repoDirB, publishRemoteFork, branch)},
		{cmd: prCreateWithOptionsCommand(repoDirB, cfg, cfg.BaseBranch, "octocat:"+branch, "acme/repo-b"), res: execx.Result{Stdout: prURLB + "\n"}},
		{cmd: prChecksCommand(repoDirB, prURLB)},
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
	if got, want := res.RepoResults[0].PRURL, prURLA; got != want {
		t.Fatalf("RepoResults[0].PRURL = %q, want %q", got, want)
	}
	if got, want := res.RepoResults[1].PRURL, prURLB; got != want {
		t.Fatalf("RepoResults[1].PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunMultiRepoUnresolvedWorkflowFailsBeforeAgent(t *testing.T) {
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
	runDir := testRunDir(guid)
	branch := "moltenhub-build-api"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	push403 := execx.Result{
		Stderr: "remote: Write access to repository not granted.\n" +
			"fatal: unable to access 'https://github.com/acme/repo-b.git/': The requested URL returned error: 403\n",
	}

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: pushDryRunCommand(repoDirA, branch)},
		{cmd: pushDryRunCommand(repoDirB, branch), res: push403, err: errors.New("exit status 128")},
		{cmd: ghRepoViewVisibilityCommand(repoDirB, "acme/repo-b"), res: execx.Result{Stdout: `{"isPrivate":true,"nameWithOwner":"acme/repo-b"}`}},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == repoDirA }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want pre-agent workflow failure")
	}
	if res.ExitCode != ExitGit {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitGit)
	}
	if !strings.Contains(res.Err.Error(), "workflow:") {
		t.Fatalf("error = %v, want workflow-stage failure", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "private") {
		t.Fatalf("error = %v, want private repo context", res.Err)
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	branch := "moltenhub-build-api"

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

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: pushDryRunCommand(repoDirA, branch)},
		{cmd: pushDryRunCommand(repoDirB, branch)},
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

type cloneBarrierRunner struct {
	mu        sync.Mutex
	cloneSeen int
	cloneGate chan struct{}
}

func newCloneBarrierRunner() *cloneBarrierRunner {
	return &cloneBarrierRunner{
		cloneGate: make(chan struct{}),
	}
}

func (r *cloneBarrierRunner) Run(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	if isCloneGitCommand(cmd) {
		r.mu.Lock()
		r.cloneSeen++
		if r.cloneSeen == 2 {
			close(r.cloneGate)
		}
		r.mu.Unlock()

		select {
		case <-r.cloneGate:
			return execx.Result{}, nil
		case <-ctx.Done():
			return execx.Result{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return execx.Result{}, errors.New("clone concurrency barrier timed out")
		}
	}

	if cmd.Name == "git" && len(cmd.Args) >= 2 && cmd.Args[0] == "switch" && cmd.Args[1] == "-c" {
		return execx.Result{}, errors.New("stop after clone stage")
	}

	return execx.Result{}, nil
}

func (r *cloneBarrierRunner) CloneSeen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cloneSeen
}

func TestRunMultiRepoClonesConcurrently(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.RepoURL = ""
	cfg.Repo = ""
	cfg.Repos = []string{
		"git@github.com:acme/repo-a.git",
		"git@github.com:acme/repo-b.git",
	}
	cfg.TargetSubdir = "."

	guid := "cloneconcurrency123"
	runDir := testRunDir(guid)
	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)

	runner := newCloneBarrierRunner()
	h := New(runner)
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == repoDirA }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res := h.Run(ctx, cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want branch-stage stop error")
	}
	if res.ExitCode != ExitGit {
		t.Fatalf("ExitCode = %d, want %d (clone stage should have succeeded)", res.ExitCode, ExitGit)
	}
	if strings.Contains(strings.ToLower(res.Err.Error()), "clone concurrency barrier timed out") {
		t.Fatalf("Run() err = %v, want clone stage to proceed concurrently", res.Err)
	}
	if got, want := runner.CloneSeen(), len(cfg.Repos); got != want {
		t.Fatalf("clone calls observed = %d, want %d", got, want)
	}
}

func TestRunNonMainBranchReusesExistingPR(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = "release/fix-ci"

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
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
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
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
	runDir := testRunDir(guid)
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
		{cmd: fetchBaseBranchCommand(repoDir, cfg.BaseBranch)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
		{cmd: headCommitSHACommand(repoDir), res: execx.Result{Stdout: "1111111111111111111111111111111111111111\n"}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, cfg.BaseBranch)},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, cfg.BaseBranch), res: execx.Result{Stdout: "abc123\trefs/heads/" + cfg.BaseBranch + "\n"}},
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
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
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
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: "main\n"}},
		{cmd: headCommitSHACommand(repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
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

func TestRunEmptyBaseBranchClonesRemoteDefaultAndCreatesNewBranch(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.BaseBranch = ""

	now := time.Date(2026, 4, 6, 19, 53, 52, 0, time.UTC)
	guid := "9ded650b29c70708825082be50fbf433"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/114"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: "master\n"}},
		{cmd: headCommitSHACommand(repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateWithOptionsCommand(repoDir, cfg, "master", branch, ""), res: execx.Result{Stdout: prURL + "\n"}},
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

func TestRunFailureFollowUpIgnoresStaleBranchAndTargetsDefaultRoot(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.RepoURL = "https://github.com/Molten-Bot/agent_00.git"
	cfg.BaseBranch = "client-logo-theme"
	cfg.TargetSubdir = "internal/web"
	cfg.Prompt = failurefollowup.RequiredPrompt

	now := time.Date(2026, 6, 10, 19, 53, 52, 0, time.UTC)
	guid := "3fcc0c7fb94ab58bf6c1fe88ef400f67"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := repoDir
	branch := slug.BranchName(cfg.Prompt, now, guid)
	prURL := "https://github.com/Molten-Bot/agent_00/pull/116"
	sanitizedCfg := cfg
	sanitizedCfg.BaseBranch = "main"
	sanitizedCfg.TargetSubdir = "."

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "setup-git"}}},
		{cmd: cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: "main\n"}},
		{cmd: headCommitSHACommand(repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, sanitizedCfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
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

func TestRunMissingMainBaseBranchFallsBackToRemoteDefaultAndCreatesNewBranch(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 6, 19, 53, 52, 0, time.UTC)
	guid := "9ded650b29c70708825082be50fbf433"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/115"

	cloneMissingMain := execx.Result{
		Stderr: "warning: Could not find remote branch main to clone.\n" +
			"fatal: Remote branch main not found in upstream origin\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir), res: cloneMissingMain, err: errors.New("clone failed")},
		{cmd: remoteRefsCommand(cfg.RepoURL), res: execx.Result{Stdout: "abc123\trefs/heads/master\n"}},
		{cmd: cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)},
		{cmd: currentBranchCommand(repoDir), res: execx.Result{Stdout: "master\n"}},
		{cmd: headCommitSHACommand(repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateWithOptionsCommand(repoDir, cfg, "master", branch, ""), res: execx.Result{Stdout: prURL + "\n"}},
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

func TestCloneRequiredNonDefaultBranchRefusesDefaultFallback(t *testing.T) {
	branch := "moltenhub-missing-feature"
	repo := repoWorkspace{
		URL:                      "git@github.com:acme/repo.git",
		Dir:                      "/tmp/required-feature-repo",
		RelDir:                   "repo",
		RequiresNonDefaultBranch: true,
		RequiredBranch:           branch,
		RequiredBranchTask:       mergeMainLibraryTaskName,
	}
	cloneMissingBranch := execx.Result{
		Stderr: "warning: Could not find remote branch " + branch + " to clone.\n" +
			"fatal: Remote branch " + branch + " not found in upstream origin\n",
	}
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: cloneRepoCommand(repo.URL, branch, repo.Dir), res: cloneMissingBranch, err: errors.New("clone failed")},
	}}

	err := New(fake).cloneRepository(context.Background(), &repo, branch, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing default-branch fallback") {
		t.Fatalf("cloneRepository() error = %v, want fail-closed branch error", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if len(fake.calls) != 1 || !commandsEqual(fake.calls[0], cloneRepoCommand(repo.URL, branch, repo.Dir)) {
		t.Fatalf("commands = %#v, want only exact feature-branch clone", fake.calls)
	}
}

func TestRunMissingMainBaseBranchBootstrapsUninitializedRepository(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 6, 19, 53, 52, 0, time.UTC)
	guid := "9ded650b29c70708825082be50fbf433"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/113"

	cloneMissingMain := execx.Result{
		Stderr: "warning: Could not find remote branch main to clone.\n" +
			"fatal: Remote branch main not found in upstream origin\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir), res: cloneMissingMain, err: errors.New("clone failed")},
		{cmd: remoteRefsCommand(cfg.RepoURL), res: execx.Result{Stdout: ""}},
		{cmd: cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)},
		{cmd: switchMainBranchCommand(repoDir)},
		{cmd: initializeMainBranchCommitCommand(repoDir)},
		{cmd: pushCommand(repoDir, "main")},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
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

func TestRunCloneRetriesTransientFailureThenSucceeds(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 6, 19, 53, 52, 0, time.UTC)
	guid := "9ded650b29c70708825082be50fbf433"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	prURL := "https://github.com/acme/repo/pull/112"

	cloneTransientFailure := execx.Result{
		Stderr: "fatal: unable to access 'https://github.com/acme/repo.git/': Failed to connect to github.com port 443: Connection timed out\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir), res: cloneTransientFailure, err: errors.New("clone failed")},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: prURL + "\n"}},
		{cmd: prChecksCommand(repoDir, prURL)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Sleep = func(context.Context, time.Duration) error { return nil }

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

func TestRunRepoNotFoundCloneFailsWithoutRetry(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	repoDir := filepath.Join(runDir, "repo")
	cloneRepoNotFound := execx.Result{
		Stderr: "remote: Repository not found.\n" +
			"fatal: repository 'git@github.com:acme/repo.git/' not found\n",
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir), res: cloneRepoNotFound, err: errors.New("clone failed")},
	}}

	h := New(fake)
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == filepath.Join(repoDir, cfg.TargetSubdir) }
	h.Sleep = func(context.Context, time.Duration) error { return nil }

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want clone failure")
	}
	if res.ExitCode != ExitClone {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitClone)
	}
	if !strings.Contains(strings.ToLower(res.Err.Error()), "repository") {
		t.Fatalf("error = %v, want repository detail", res.Err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunRepoNotFoundCloneFallsBackToKnownOwner(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.RepoURL = ""
	cfg.Repo = ""
	cfg.Repos = []string{
		"git@github.com:Molten-Bot/user-portal.git",
		"git@github.com:moltenbot000/moltenhub-code.git",
	}
	cfg.TargetSubdir = "."

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	branch := "moltenhub-build-api"

	repoRelA := repoWorkspaceDirName(cfg.Repos[0], 0, len(cfg.Repos))
	repoRelB := repoWorkspaceDirName(cfg.Repos[1], 1, len(cfg.Repos))
	repoDirA := filepath.Join(runDir, repoRelA)
	repoDirB := filepath.Join(runDir, repoRelB)
	fallbackRepoB := "git@github.com:Molten-Bot/agent_00.git"

	codexPrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, []repoWorkspace{
		{URL: cfg.Repos[0], RelDir: repoRelA},
		{URL: fallbackRepoB, RelDir: repoRelB},
	})
	codexPrompt = withAgentsPrompt(codexPrompt, agentsPath)

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneRepoCommand(cfg.Repos[0], cfg.BaseBranch, repoDirA)},
		{
			cmd: cloneRepoCommand(cfg.Repos[1], cfg.BaseBranch, repoDirB),
			res: execx.Result{Stderr: "remote: Repository not found.\nfatal: repository not found\n"},
			err: errors.New("clone failed"),
		},
		{cmd: cloneRepoCommand(fallbackRepoB, cfg.BaseBranch, repoDirB)},
		{cmd: branchCommand(repoDirA, branch)},
		{cmd: branchCommand(repoDirB, branch)},
		{cmd: pushDryRunCommand(repoDirA, branch)},
		{cmd: pushDryRunCommand(repoDirB, branch)},
		{cmd: codexCommandWithOptions(runDir, codexPrompt, codexRunOptions{SkipGitRepoCheck: true})},
		{cmd: statusCommand(repoDirA), res: execx.Result{Stdout: " M file-a.go\n"}},
		{cmd: statusCommand(repoDirB), res: execx.Result{Stdout: " M file-b.go\n"}},
		{cmd: addCommand(repoDirA)},
		{cmd: commitCommand(repoDirA, cfg.CommitMessage)},
		{cmd: pushCommand(repoDirA, branch)},
		{cmd: prCreateCommand(repoDirA, cfg, branch), res: execx.Result{Stdout: "https://github.com/Molten-Bot/user-portal/pull/10\n"}},
		{cmd: prChecksCommand(repoDirA, "https://github.com/Molten-Bot/user-portal/pull/10")},
		{cmd: addCommand(repoDirB)},
		{cmd: commitCommand(repoDirB, cfg.CommitMessage)},
		{cmd: pushCommand(repoDirB, branch)},
		{cmd: prCreateCommand(repoDirB, cfg, branch), res: execx.Result{Stdout: "https://github.com/Molten-Bot/agent_00/pull/20\n"}},
		{cmd: prChecksCommand(repoDirB, "https://github.com/Molten-Bot/agent_00/pull/20")},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == repoDirA }
	h.Sleep = func(context.Context, time.Duration) error { return nil }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if res.ExitCode != ExitSuccess {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, ExitSuccess)
	}
	if got, want := len(res.RepoResults), 2; got != want {
		t.Fatalf("len(RepoResults) = %d, want %d", got, want)
	}
	if got, want := res.RepoResults[1].RepoURL, fallbackRepoB; got != want {
		t.Fatalf("RepoResults[1].RepoURL = %q, want %q", got, want)
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
	runDir := testRunDir(guid)
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
	branch := "moltenhub-build-api"
	prompt := "fix tests"
	targetDir := filepath.Join(repoDir, "services/api")

	clone := cloneCommand(cfg, repoDir)
	if clone.Name != "git" || !reflect.DeepEqual(clone.Args, []string{"clone", "--branch", "main", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone command unexpected: %+v", clone)
	}
	cfgDefaultBranch := cfg
	cfgDefaultBranch.BaseBranch = ""
	cloneDefaultBranch := cloneCommand(cfgDefaultBranch, repoDir)
	if cloneDefaultBranch.Name != "git" || !reflect.DeepEqual(cloneDefaultBranch.Args, []string{"clone", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone command for default branch unexpected: %+v", cloneDefaultBranch)
	}
	fetchBase := fetchBaseBranchCommand(repoDir, "main")
	if fetchBase.Name != "git" || fetchBase.Dir != repoDir || !reflect.DeepEqual(fetchBase.Args, []string{"fetch", "origin", "main:refs/remotes/origin/main"}) {
		t.Fatalf("fetch base command unexpected: %+v", fetchBase)
	}
	cloneDefault := cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)
	if cloneDefault.Name != "git" || !reflect.DeepEqual(cloneDefault.Args, []string{"clone", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone default command unexpected: %+v", cloneDefault)
	}
	currentBranch := currentBranchCommand(repoDir)
	if currentBranch.Name != "git" || currentBranch.Dir != repoDir || !reflect.DeepEqual(currentBranch.Args, []string{"branch", "--show-current"}) {
		t.Fatalf("current branch command unexpected: %+v", currentBranch)
	}
	remoteRefs := remoteRefsCommand(cfg.RepoURL)
	if remoteRefs.Name != "git" || !reflect.DeepEqual(remoteRefs.Args, []string{"ls-remote", "--heads", "--tags", cfg.RepoURL}) {
		t.Fatalf("remote refs command unexpected: %+v", remoteRefs)
	}
	switchMain := switchMainBranchCommand(repoDir)
	if switchMain.Name != "git" || switchMain.Dir != repoDir || !reflect.DeepEqual(switchMain.Args, []string{"switch", "-C", "main"}) {
		t.Fatalf("switch main command unexpected: %+v", switchMain)
	}
	initMain := initializeMainBranchCommitCommand(repoDir)
	wantInitMain := []string{
		"-c",
		"user.name=MoltenHub Code",
		"-c",
		"user.email=bot@molten.bot",
		"commit",
		"--allow-empty",
		"-m",
		"chore: initialize main branch",
		"-m",
		moltenbotCoAuthorTrailer,
	}
	if initMain.Name != "git" || initMain.Dir != repoDir || !reflect.DeepEqual(initMain.Args, wantInitMain) {
		t.Fatalf("initialize main branch command unexpected: %+v", initMain)
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
	if codex.Name != "codex" || codex.Dir != targetDir || !reflect.DeepEqual(codex.Args, []string{"exec", "--sandbox", "workspace-write"}) {
		t.Fatalf("codex command unexpected: %+v", codex)
	}
	if got, want := codex.Stdin, withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("codex stdin = %q, want %q", got, want)
	}
	codexWorkspace := codexCommandWithOptions(targetDir, prompt, codexRunOptions{SkipGitRepoCheck: true})
	if codexWorkspace.Name != "codex" || codexWorkspace.Dir != targetDir || !reflect.DeepEqual(codexWorkspace.Args, []string{"exec", "--sandbox", "workspace-write", "--skip-git-repo-check"}) {
		t.Fatalf("codex workspace command unexpected: %+v", codexWorkspace)
	}
	if got, want := codexWorkspace.Stdin, withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("codex workspace stdin = %q, want %q", got, want)
	}
	codexWithImages := codexCommandWithOptions(targetDir, prompt, codexRunOptions{
		SkipGitRepoCheck: true,
		ImagePaths:       []string{"/tmp/run/prompt-images/01-shot.png", "/tmp/run/prompt-images/02-shot.png"},
		WritableDirs:     []string{"/tmp/run"},
	})
	if codexWithImages.Name != "codex" || codexWithImages.Dir != targetDir || !reflect.DeepEqual(codexWithImages.Args, []string{
		"exec",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
		"--add-dir", "/tmp/run",
		"--image", "/tmp/run/prompt-images/01-shot.png",
		"--image", "/tmp/run/prompt-images/02-shot.png",
	}) {
		t.Fatalf("codex image command unexpected: %+v", codexWithImages)
	}
	if got, want := codexWithImages.Stdin, withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("codex image stdin = %q, want %q", got, want)
	}

	pr := prCreateCommand(repoDir, cfg, branch)
	normalizedPRBody := config.NormalizePRBody(cfg.PRBody, cfg.Prompt)
	wantPrefix := []string{"pr", "create", "--base", "main", "--head", branch, "--title", cfg.PRTitle, "--body", normalizedPRBody}
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
	wantNoBasePrefix := []string{"pr", "create", "--head", branch, "--title", cfg.PRTitle, "--body", normalizedPRBody}
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

	cfg.Reviewers = []string{"none"}
	prNoReviewer := prCreateCommand(repoDir, cfg, branch)
	if containsSequence(prNoReviewer.Args, []string{"--reviewer", "none"}) {
		t.Fatalf("pr args should omit none reviewer sentinel: %v", prNoReviewer.Args)
	}
	prNoBaseReviewer := prCreateWithoutBaseCommand(repoDir, cfg, branch)
	if containsSequence(prNoBaseReviewer.Args, []string{"--reviewer", "none"}) {
		t.Fatalf("pr without base args should omit none reviewer sentinel: %v", prNoBaseReviewer.Args)
	}

	prComment := prCommentCommand(repoDir, "https://github.com/acme/repo/pull/42", "body")
	wantComment := []string{"pr", "comment", "https://github.com/acme/repo/pull/42", "--body", "body"}
	if prComment.Name != "gh" || prComment.Dir != repoDir || !reflect.DeepEqual(prComment.Args, wantComment) {
		t.Fatalf("pr comment command unexpected: %+v", prComment)
	}
	addScreenshots := addPRCommentScreenshotsCommand(repoDir, []string{".moltenhub/pr-comment-screenshots/after.png"})
	wantAddScreenshots := []string{"add", "-f", "--", ".moltenhub/pr-comment-screenshots/after.png"}
	if addScreenshots.Name != "git" || addScreenshots.Dir != repoDir || !reflect.DeepEqual(addScreenshots.Args, wantAddScreenshots) {
		t.Fatalf("add PR comment screenshots command unexpected: %+v", addScreenshots)
	}

	prLookup := prLookupByHeadCommand(repoDir, branch)
	wantLookup := []string{"pr", "list", "--state", "open", "--head", branch, "--json", "url", "--limit", "1"}
	if prLookup.Name != "gh" || prLookup.Dir != repoDir || !reflect.DeepEqual(prLookup.Args, wantLookup) {
		t.Fatalf("pr lookup command unexpected: %+v", prLookup)
	}
	prLookupWithRepo := prLookupByHeadWithRepoCommand(repoDir, "octocat:"+branch, "acme/repo")
	wantLookupWithRepo := []string{"pr", "list", "--state", "open", "--head", "octocat:" + branch, "--json", "url", "--limit", "1", "--repo", "acme/repo"}
	if prLookupWithRepo.Name != "gh" || prLookupWithRepo.Dir != repoDir || !reflect.DeepEqual(prLookupWithRepo.Args, wantLookupWithRepo) {
		t.Fatalf("pr lookup with repo command unexpected: %+v", prLookupWithRepo)
	}

	remoteHead := remoteBranchExistsOnOriginCommand(repoDir, branch)
	wantRemoteHead := []string{"ls-remote", "--heads", "origin", branch}
	if remoteHead.Name != "git" || remoteHead.Dir != repoDir || !reflect.DeepEqual(remoteHead.Args, wantRemoteHead) {
		t.Fatalf("remote head command unexpected: %+v", remoteHead)
	}
	remoteForkHead := remoteBranchExistsOnRemoteCommand(repoDir, "fork", branch)
	wantRemoteForkHead := []string{"ls-remote", "--heads", "fork", branch}
	if remoteForkHead.Name != "git" || remoteForkHead.Dir != repoDir || !reflect.DeepEqual(remoteForkHead.Args, wantRemoteForkHead) {
		t.Fatalf("remote fork head command unexpected: %+v", remoteForkHead)
	}

	commitsAhead := commitsAheadOfBaseCommand(repoDir, "refs/heads/main")
	wantCommitsAhead := []string{"rev-list", "--count", "refs/remotes/origin/main..HEAD"}
	if commitsAhead.Name != "git" || commitsAhead.Dir != repoDir || !reflect.DeepEqual(commitsAhead.Args, wantCommitsAhead) {
		t.Fatalf("commits ahead command unexpected: %+v", commitsAhead)
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
	if !shouldCreateWorkBranch("master") {
		t.Fatal("shouldCreateWorkBranch(master) = false, want true")
	}
	if shouldCreateWorkBranch("Main") {
		t.Fatal("shouldCreateWorkBranch(Main) = true, want false")
	}
	if shouldCreateWorkBranch("release/fix-ci") {
		t.Fatal("shouldCreateWorkBranch(non-main) = true, want false")
	}

	checks := prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")
	wantChecks := []string{"pr", "checks", "42", "--watch", "--interval", "10"}
	if checks.Name != "gh" || checks.Dir != repoDir || !reflect.DeepEqual(checks.Args, wantChecks) {
		t.Fatalf("pr checks command unexpected: %+v", checks)
	}

	allChecks := prChecksAnyCommand(repoDir, "https://github.com/acme/repo/pull/42")
	wantAllChecks := []string{"pr", "checks", "42", "--watch", "--interval", "10"}
	if allChecks.Name != "gh" || allChecks.Dir != repoDir || !reflect.DeepEqual(allChecks.Args, wantAllChecks) {
		t.Fatalf("pr checks any command unexpected: %+v", allChecks)
	}

	jsonChecks := prChecksJSONCommand(repoDir, "https://github.com/acme/repo/pull/42", true)
	wantJSONChecks := []string{"pr", "checks", "42", "--json", "name,bucket,completedAt,startedAt", "--required"}
	if jsonChecks.Name != "gh" || jsonChecks.Dir != repoDir || !reflect.DeepEqual(jsonChecks.Args, wantJSONChecks) {
		t.Fatalf("pr checks json command unexpected: %+v", jsonChecks)
	}

	jsonAnyChecks := prChecksJSONCommand(repoDir, "https://github.com/acme/repo/pull/42", false)
	wantJSONAnyChecks := []string{"pr", "checks", "42", "--json", "name,bucket,completedAt,startedAt"}
	if jsonAnyChecks.Name != "gh" || jsonAnyChecks.Dir != repoDir || !reflect.DeepEqual(jsonAnyChecks.Args, wantJSONAnyChecks) {
		t.Fatalf("pr checks any json command unexpected: %+v", jsonAnyChecks)
	}

	workflowDispatch := workflowDispatchCommand(repoDir, branch)
	wantWorkflowDispatch := []string{"workflow", "run", defaultCIWorkflowPath, "--ref", branch}
	if workflowDispatch.Name != "gh" || workflowDispatch.Dir != repoDir || !reflect.DeepEqual(workflowDispatch.Args, wantWorkflowDispatch) {
		t.Fatalf("workflow dispatch command unexpected: %+v", workflowDispatch)
	}
	headSHA := headCommitSHACommand(repoDir)
	if headSHA.Name != "git" || headSHA.Dir != repoDir || !reflect.DeepEqual(headSHA.Args, []string{"rev-parse", "HEAD"}) {
		t.Fatalf("head commit command unexpected: %+v", headSHA)
	}
	workflowRuns := workflowDispatchRunsCommand(repoDir, branch)
	wantWorkflowRuns := []string{
		"run", "list",
		"--workflow", defaultCIWorkflowPath,
		"--branch", branch,
		"--event", "workflow_dispatch",
		"--json", "status,conclusion,workflowName,displayTitle,headSha",
		"--limit", "1",
	}
	if workflowRuns.Name != "gh" || workflowRuns.Dir != repoDir || !reflect.DeepEqual(workflowRuns.Args, wantWorkflowRuns) {
		t.Fatalf("workflow runs command unexpected: %+v", workflowRuns)
	}
	requiredChecks := requiredStatusChecksCommand(repoDir, "acme/repo", "release/fix-ci")
	wantRequiredChecks := []string{"api", "repos/acme/repo/branches/release%2Ffix-ci/protection/required_status_checks"}
	if requiredChecks.Name != "gh" || requiredChecks.Dir != repoDir || !reflect.DeepEqual(requiredChecks.Args, wantRequiredChecks) {
		t.Fatalf("required status checks command unexpected: %+v", requiredChecks)
	}

	fetchBranch := fetchBranchCommand(repoDir, branch)
	wantFetchBranch := []string{"fetch", "origin", branch}
	if fetchBranch.Name != "git" || fetchBranch.Dir != repoDir || !reflect.DeepEqual(fetchBranch.Args, wantFetchBranch) {
		t.Fatalf("fetch branch command unexpected: %+v", fetchBranch)
	}
	fetchForkBranch := fetchBranchFromRemoteCommand(repoDir, "fork", branch)
	wantFetchForkBranch := []string{"fetch", "fork", branch}
	if fetchForkBranch.Name != "git" || fetchForkBranch.Dir != repoDir || !reflect.DeepEqual(fetchForkBranch.Args, wantFetchForkBranch) {
		t.Fatalf("fetch fork branch command unexpected: %+v", fetchForkBranch)
	}

	mergeFetchedBranch := mergeFetchedBranchCommand(repoDir)
	wantMergeFetchedBranch := []string{"merge", "--no-edit", "FETCH_HEAD"}
	if mergeFetchedBranch.Name != "git" || mergeFetchedBranch.Dir != repoDir || !reflect.DeepEqual(mergeFetchedBranch.Args, wantMergeFetchedBranch) {
		t.Fatalf("merge fetched branch command unexpected: %+v", mergeFetchedBranch)
	}

	pushDryRun := pushDryRunCommand(repoDir, branch)
	wantPushDryRun := []string{"push", "--dry-run", "origin", "HEAD:refs/heads/" + branch}
	if pushDryRun.Name != "git" || pushDryRun.Dir != repoDir || !reflect.DeepEqual(pushDryRun.Args, wantPushDryRun) {
		t.Fatalf("push dry-run command unexpected: %+v", pushDryRun)
	}
	pushForkDryRun := pushDryRunToRemoteCommand(repoDir, "fork", branch)
	wantPushForkDryRun := []string{"push", "--dry-run", "fork", "HEAD:refs/heads/" + branch}
	if pushForkDryRun.Name != "git" || pushForkDryRun.Dir != repoDir || !reflect.DeepEqual(pushForkDryRun.Args, wantPushForkDryRun) {
		t.Fatalf("push fork dry-run command unexpected: %+v", pushForkDryRun)
	}
	pushFork := pushToRemoteCommand(repoDir, "fork", branch)
	wantPushFork := []string{"push", "-u", "fork", branch}
	if pushFork.Name != "git" || pushFork.Dir != repoDir || !reflect.DeepEqual(pushFork.Args, wantPushFork) {
		t.Fatalf("push fork command unexpected: %+v", pushFork)
	}
	prWithRepo := prCreateWithOptionsCommand(repoDir, cfg, "main", "octocat:"+branch, "acme/repo")
	if !containsSequence(prWithRepo.Args, []string{"--repo", "acme/repo"}) {
		t.Fatalf("pr create with repo args missing --repo: %v", prWithRepo.Args)
	}
	ghViewer := ghViewerLoginCommand(repoDir)
	if ghViewer.Name != "gh" || ghViewer.Dir != repoDir || !reflect.DeepEqual(ghViewer.Args, []string{"api", "user"}) {
		t.Fatalf("gh viewer command unexpected: %+v", ghViewer)
	}
	ghRepoView := ghRepoViewVisibilityCommand(repoDir, "acme/repo")
	if ghRepoView.Name != "gh" || ghRepoView.Dir != repoDir || !reflect.DeepEqual(ghRepoView.Args, []string{"repo", "view", "acme/repo", "--json", "isPrivate,nameWithOwner"}) {
		t.Fatalf("gh repo view command unexpected: %+v", ghRepoView)
	}
	ghFork := ghRepoForkCommand(repoDir, "acme/repo")
	if ghFork.Name != "gh" || ghFork.Dir != repoDir || !reflect.DeepEqual(ghFork.Args, []string{"repo", "fork", "acme/repo"}) {
		t.Fatalf("gh repo fork command unexpected: %+v", ghFork)
	}
	remoteAdd := gitRemoteAddCommand(repoDir, "fork", "git@github.com:octocat/repo.git")
	if remoteAdd.Name != "git" || remoteAdd.Dir != repoDir || !reflect.DeepEqual(remoteAdd.Args, []string{"remote", "add", "fork", "git@github.com:octocat/repo.git"}) {
		t.Fatalf("git remote add command unexpected: %+v", remoteAdd)
	}
	remoteSetURL := gitRemoteSetURLCommand(repoDir, "fork", "git@github.com:octocat/repo.git")
	if remoteSetURL.Name != "git" || remoteSetURL.Dir != repoDir || !reflect.DeepEqual(remoteSetURL.Args, []string{"remote", "set-url", "fork", "git@github.com:octocat/repo.git"}) {
		t.Fatalf("git remote set-url command unexpected: %+v", remoteSetURL)
	}
}

func TestCommitCommandAddsMoltenbotCoAuthorTrailer(t *testing.T) {
	t.Parallel()

	const wantTrailer = "Co-authored-by: Molten Bot 000 <260473928+moltenbot000@users.noreply.github.com>"
	if moltenbotCoAuthorTrailer != wantTrailer {
		t.Fatalf("moltenbotCoAuthorTrailer = %q, want %q", moltenbotCoAuthorTrailer, wantTrailer)
	}

	repoDir := "/tmp/run/repo"
	commit := commitCommand(repoDir, "feat: automate api")
	want := []string{"commit", "-m", "feat: automate api", "-m", wantTrailer}
	if commit.Name != "git" || commit.Dir != repoDir || !reflect.DeepEqual(commit.Args, want) {
		t.Fatalf("commit command unexpected: %+v", commit)
	}

	remediationMessage := remediationCommitMessage("fix: tests", 2)
	remediation := commitCommand(repoDir, remediationMessage)
	wantRemediation := []string{"commit", "-m", "fix: tests (ci remediation 2)", "-m", wantTrailer}
	if remediation.Name != "git" || remediation.Dir != repoDir || !reflect.DeepEqual(remediation.Args, wantRemediation) {
		t.Fatalf("remediation commit command unexpected: %+v", remediation)
	}

	messageWithTrailer := "feat: automate api\n\n" + wantTrailer
	deduped := commitCommand(repoDir, messageWithTrailer)
	wantDeduped := []string{"commit", "-m", messageWithTrailer}
	if deduped.Name != "git" || deduped.Dir != repoDir || !reflect.DeepEqual(deduped.Args, wantDeduped) {
		t.Fatalf("deduped commit command unexpected: %+v", deduped)
	}
}

func TestPRCommentScreenshotFilesFindsImagesInHandoffDirectory(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	root := filepath.Join(repoDir, prCommentScreenshotsRelDir)
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, rel := range []string{
		"after.png",
		"before.JPG",
		filepath.Join("nested", "app.jpeg"),
		"notes.txt",
	} {
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", rel, err)
		}
	}

	files, err := prCommentScreenshotFiles(repoDir)
	if err != nil {
		t.Fatalf("prCommentScreenshotFiles() error = %v", err)
	}
	want := []string{
		".moltenhub/pr-comment-screenshots/after.png",
		".moltenhub/pr-comment-screenshots/before.JPG",
		".moltenhub/pr-comment-screenshots/nested/app.jpeg",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
}

func TestRepoHasPendingChangesTreatsScreenshotHandoffAsChange(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	root := filepath.Join(repoDir, prCommentScreenshotsRelDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "after.png"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	changed, err := New(nil).repoHasPendingChanges(context.Background(), repoWorkspace{
		Dir:              repoDir,
		CreateWorkBranch: true,
		BaseBranch:       "main",
	}, "## moltenhub-validation\n")
	if err != nil {
		t.Fatalf("repoHasPendingChanges() error = %v", err)
	}
	if !changed {
		t.Fatal("repoHasPendingChanges() = false, want true")
	}
}

func TestRepoHasPendingChangesIgnoresUnchangedScreenshotHandoffBaseline(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	root := filepath.Join(repoDir, prCommentScreenshotsRelDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "after.png"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	baseline, err := prCommentScreenshotSnapshot(repoDir)
	if err != nil {
		t.Fatalf("prCommentScreenshotSnapshot() error = %v", err)
	}

	changed, err := New(nil).repoHasPendingChanges(context.Background(), repoWorkspace{
		Dir:                         repoDir,
		BaseBranch:                  "main",
		PRCommentScreenshotBaseline: baseline,
	}, "## moltenhub-validation\n")
	if err != nil {
		t.Fatalf("repoHasPendingChanges() error = %v", err)
	}
	if changed {
		t.Fatal("repoHasPendingChanges() = true, want false")
	}
}

func TestChangedPRCommentScreenshotFilesFindsOnlyNewOrModifiedImages(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	root := filepath.Join(repoDir, prCommentScreenshotsRelDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "before.png"), []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile(before) error = %v", err)
	}
	baseline, err := prCommentScreenshotSnapshot(repoDir)
	if err != nil {
		t.Fatalf("prCommentScreenshotSnapshot() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "before.png"), []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile(before modified) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "after.jpg"), []byte("after"), 0o644); err != nil {
		t.Fatalf("WriteFile(after) error = %v", err)
	}

	files, err := changedPRCommentScreenshotFiles(repoDir, baseline)
	if err != nil {
		t.Fatalf("changedPRCommentScreenshotFiles() error = %v", err)
	}
	want := []string{
		".moltenhub/pr-comment-screenshots/after.jpg",
		".moltenhub/pr-comment-screenshots/before.png",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
}

func TestPRCommentScreenshotsBodyBuildsGitHubImageLinks(t *testing.T) {
	t.Parallel()

	body, err := prCommentScreenshotsBody(
		"https://github.com/acme/repo/pull/42",
		"",
		"moltenhub/pink",
		[]string{".moltenhub/pr-comment-screenshots/before shot.png"},
	)
	if err != nil {
		t.Fatalf("prCommentScreenshotsBody() error = %v", err)
	}
	for _, want := range []string{
		"Automated screenshots captured during the run.",
		"### .moltenhub/pr-comment-screenshots/before shot.png",
		"![.moltenhub/pr-comment-screenshots/before shot.png]",
		"https://github.com/acme/repo/blob/moltenhub/pink/.moltenhub/pr-comment-screenshots/before%20shot.png?raw=1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestPRCommentScreenshotsBodyUsesForkHeadOwner(t *testing.T) {
	t.Parallel()

	body, err := prCommentScreenshotsBody(
		"https://github.com/acme/repo/pull/42",
		"octocat",
		"moltenhub-pink",
		[]string{".moltenhub/pr-comment-screenshots/after.png"},
	)
	if err != nil {
		t.Fatalf("prCommentScreenshotsBody() error = %v", err)
	}
	want := "https://github.com/octocat/repo/blob/moltenhub-pink/.moltenhub/pr-comment-screenshots/after.png?raw=1"
	if !strings.Contains(body, want) {
		t.Fatalf("body missing fork image URL %q:\n%s", want, body)
	}
}

func TestPreflightCommandsWithRuntimeUsesConfiguredCLI(t *testing.T) {
	t.Parallel()

	runtime, err := agentruntime.Resolve(agentruntime.HarnessClaude, "claude-custom")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	cmds := preflightCommandsWithRuntime(runtime)
	if got, want := len(cmds), 3; got != want {
		t.Fatalf("len(preflight commands) = %d, want %d", got, want)
	}
	if got := cmds[2]; got.Name != "claude-custom" || !reflect.DeepEqual(got.Args, []string{"--help"}) {
		t.Fatalf("runtime preflight command = %+v", got)
	}
}

func TestPRCreateCommandsEnforceStandardBodyFormat(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "Investigate failing checks"
	cfg.PRBody = "Hardened auth retry flow and fixed flaky assertions."

	pr := prCreateCommand("/tmp/repo", cfg, "topic-branch")
	body, ok := flagValue(pr.Args, "--body")
	if !ok {
		t.Fatalf("pr create args missing --body: %v", pr.Args)
	}

	required := []string{
		"Proposed changes from Molten.Bot",
		"This PR implements the requested changes described below.\nBuilt using AI-assisted engineering and reviewed before submission.\nOnly relevant files were modified.",
		"Hardened auth retry flow and fixed flaky assertions.",
		"Original task prompt:\n```text\nInvestigate failing checks\n```",
		"Curious how this was built? See how AI agents can help with your own projects: [MoltenBot Code](https://molten.bot/code?source=pr)",
	}
	for _, item := range required {
		if !strings.Contains(body, item) {
			t.Fatalf("normalized PR body missing %q: %q", item, body)
		}
	}
	if strings.Contains(body, "Standard PR format reference: https://github.com/Molten-Bot/agent_00/pull/580") {
		t.Fatalf("normalized PR body retained deprecated format reference line: %q", body)
	}
	if strings.Contains(body, "Agent summary of issue resolution:") {
		t.Fatalf("normalized PR body retained deprecated summary heading: %q", body)
	}

	prNoBase := prCreateWithoutBaseCommand("/tmp/repo", cfg, "topic-branch")
	noBaseBody, ok := flagValue(prNoBase.Args, "--body")
	if !ok {
		t.Fatalf("pr create without base args missing --body: %v", prNoBase.Args)
	}
	if noBaseBody != body {
		t.Fatalf("normalized PR body mismatch between command variants: with-base=%q without-base=%q", body, noBaseBody)
	}
}

func TestAgentCommandWithOptionsUsesConfiguredRuntime(t *testing.T) {
	t.Parallel()

	targetDir := "/tmp/repo"
	prompt := "Fix the failing tests."

	claudeRuntime, err := agentruntime.Resolve(agentruntime.HarnessClaude, "")
	if err != nil {
		t.Fatalf("Resolve(claude) error = %v", err)
	}
	claudeCmd, err := agentCommandWithOptions(claudeRuntime, targetDir, prompt, codexRunOptions{})
	if err != nil {
		t.Fatalf("agentCommandWithOptions(claude) error = %v", err)
	}
	if claudeCmd.Name != "claude" || claudeCmd.Dir != targetDir {
		t.Fatalf("unexpected claude command: %+v", claudeCmd)
	}
	if got, want := claudeCmd.Args[len(claudeCmd.Args)-1], withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("claude prompt arg = %q, want completion-gated prompt", got)
	}
	if claudeCmd.Stdin != "" {
		t.Fatalf("claude stdin = %q, want empty", claudeCmd.Stdin)
	}

	if _, err := agentCommandWithOptions(claudeRuntime, targetDir, prompt, codexRunOptions{ImagePaths: []string{"x.png"}}); err == nil {
		t.Fatal("agentCommandWithOptions(claude with images) error = nil, want non-nil")
	}
}

func TestRunRejectsUnsupportedPromptImagesBeforePreflight(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.AgentHarness = agentruntime.HarnessClaude
	cfg.Images = []config.PromptImage{{Name: "shot.png", MediaType: "image/png", DataBase64: "aGVsbG8="}}

	fake := &fakeRunner{t: t}
	h := New(fake)

	res := h.Run(context.Background(), cfg)
	if res.Err == nil {
		t.Fatal("Run() err = nil, want prompt image support error")
	}
	if !errors.Is(res.Err, agentruntime.ErrPromptImagesUnsupported) {
		t.Fatalf("Run() err = %v, want ErrPromptImagesUnsupported", res.Err)
	}
	if got, want := res.ExitCode, ExitConfig; got != want {
		t.Fatalf("ExitCode = %d, want %d", got, want)
	}
}

func TestRunUsesConfiguredRuntimeCommand(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.AgentHarness = "claude"
	cfg.AgentCommand = "claude-custom"
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "runtimecmd123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	runtime, err := agentruntime.Resolve(cfg.AgentHarness, cfg.AgentCommand)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	runtimePrompt := withAgentsPrompt(cfg.Prompt, agentsPath)
	runtimePrompt, err = withResponseModePrompt(runtimePrompt, cfg.ResponseMode)
	if err != nil {
		t.Fatalf("withResponseModePrompt() error = %v", err)
	}
	runtimeCmd, err := agentCommandWithOptions(runtime, targetDir, runtimePrompt, codexRunOptions{})
	if err != nil {
		t.Fatalf("agentCommandWithOptions() error = %v", err)
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "claude-custom", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: runtimeCmd},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: ""}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
	}}

	var logs []string
	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if !res.NoChanges {
		t.Fatal("NoChanges = false, want true")
	}
	joinedLogs := strings.Join(logs, "\n")
	for _, want := range []string{
		"stage=claude status=start target=services/api agent_run_id=agent-implementation-1 agent_harness=claude mode=implementation attempt=1 repo=repo repo_dir=repo target=services/api",
		"stage=claude status=ok elapsed_s=",
	} {
		if !strings.Contains(joinedLogs, want) {
			t.Fatalf("logs missing claude workflow metadata %q:\n%s", want, joinedLogs)
		}
	}
}

func TestRunNoChangesRecordsConcreteNoChangeEvidence(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "FIX THIS: move openapi.yml under public/ or add serving."
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "nochangeevidence123"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-fix-this-move-openapi-yml-under-public-o"

	runtime := agentruntime.Default()
	runtimePrompt := withAgentsPrompt(cfg.Prompt, agentsPath)
	runtimePrompt, err := withResponseModePrompt(runtimePrompt, cfg.ResponseMode)
	if err != nil {
		t.Fatalf("withResponseModePrompt() error = %v", err)
	}
	runtimeCmd, err := agentCommandWithOptions(runtime, targetDir, runtimePrompt, codexRunOptions{})
	if err != nil {
		t.Fatalf("agentCommandWithOptions() error = %v", err)
	}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: runtime.PreflightCommand()},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{
			cmd: runtimeCmd,
			res: execx.Result{Stdout: strings.Join([]string{
				"No repository changes required.",
				"MoltenHub Code evidence: internal/app/harness.go already rejects failure/no-changes follow-up no-ops without concrete evidence.",
			}, "\n")},
		},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: ""}},
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
		{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
	}}

	h := New(fake)
	h.Now = func() time.Time { return now }
	h.Workspace = testWorkspaceManager(guid)
	h.TargetDirOK = func(path string) bool { return path == targetDir }

	res := h.Run(context.Background(), cfg)
	if res.Err != nil {
		t.Fatalf("Run() err = %v", res.Err)
	}
	if !res.NoChanges {
		t.Fatal("NoChanges = false, want true")
	}
	if !res.NoChangeEvidence {
		t.Fatal("NoChangeEvidence = false, want true")
	}
}

func TestAgentOutputCitesGeneralNoChangeEvidenceForZeroCommits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name: "zero commits with file evidence",
			output: strings.Join([]string{
				"No-op. git-changes-by-day ran across all 10 repos. June 30 UTC: zero commits.",
				"No `releases.json` change was produced.",
			}, "\n"),
			want: true,
		},
		{
			name:   "numeric zero commits with file evidence",
			output: "0 commits matched; `releases.json` remains unchanged.",
			want:   true,
		},
		{
			name:   "claim without file evidence",
			output: "No-op. Zero commits matched.",
		},
		{
			name:   "file mention without no-change claim",
			output: "Checked `releases.json`; changes are still required.",
		},
		{
			name:   "explicit failure is not evidence",
			output: "Failure: zero commits because `releases.json` could not be read.",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := agentOutputCitesGeneralNoChangeEvidence(execx.Result{Stdout: tt.output})
			if got != tt.want {
				t.Fatalf("agentOutputCitesGeneralNoChangeEvidence() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestRunAppliesResponseModeAcrossNonCodexRuntimes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		harness string
	}{
		{name: "claude", harness: agentruntime.HarnessClaude},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := sampleConfig()
			cfg.AgentHarness = tt.harness
			cfg.ResponseMode = "caveman-full"

			now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
			guid := "runtimemode123456"
			runDir := testRunDir(guid)
			agentsPath := filepath.Join(runDir, "AGENTS.md")
			repoDir := filepath.Join(runDir, "repo")
			targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
			branch := "moltenhub-build-api"

			runtime, err := agentruntime.Resolve(cfg.AgentHarness, cfg.AgentCommand)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}

			runtimePrompt := withAgentsPrompt(cfg.Prompt, agentsPath)
			runtimePrompt, err = withResponseModePrompt(runtimePrompt, cfg.ResponseMode)
			if err != nil {
				t.Fatalf("withResponseModePrompt() error = %v", err)
			}
			runtimeCmd, err := agentCommandWithOptions(runtime, targetDir, runtimePrompt, codexRunOptions{})
			if err != nil {
				t.Fatalf("agentCommandWithOptions() error = %v", err)
			}

			fake := &fakeRunner{t: t, exps: []expectedRun{
				{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
				{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
				{cmd: runtime.PreflightCommand()},
				{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
				{cmd: cloneCommand(cfg, repoDir)},
				{cmd: branchCommand(repoDir, branch)},
				{cmd: pushDryRunCommand(repoDir, branch)},
				{cmd: runtimeCmd},
				{cmd: statusCommand(repoDir), res: execx.Result{Stdout: ""}},
				{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
				{cmd: prLookupAnyByHeadCommand(repoDir, branch)},
			}}

			h := New(fake)
			h.Now = func() time.Time { return now }
			h.Workspace = testWorkspaceManager(guid)
			h.TargetDirOK = func(path string) bool { return path == targetDir }

			res := h.Run(context.Background(), cfg)
			if res.Err != nil {
				t.Fatalf("Run() err = %v", res.Err)
			}
			if !res.NoChanges {
				t.Fatal("NoChanges = false, want true")
			}
		})
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

func TestMaterializePromptImagesRequiresBaseDir(t *testing.T) {
	t.Parallel()

	if _, err := materializePromptImages(" \t ", []config.PromptImage{
		{Name: "Clipboard Shot.PNG", MediaType: "image/png", DataBase64: "aGVsbG8="},
	}); err == nil {
		t.Fatal("materializePromptImages(blank baseDir) error = nil, want non-nil")
	}
}

func TestWithPromptImagePaths(t *testing.T) {
	t.Parallel()

	got := withPromptImagePaths("inspect screenshot", []string{"prompt-images/01-shot.png", "  ", "/tmp/run/prompt-images/02-shot.png"})
	for _, want := range []string{
		"inspect screenshot",
		"Prompt image files are available at these paths:",
		"- prompt-images/01-shot.png",
		"- /tmp/run/prompt-images/02-shot.png",
		"Use these paths when you need to inspect attached images from the workspace.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("withPromptImagePaths() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-   ") {
		t.Fatalf("withPromptImagePaths() kept blank image path:\n%s", got)
	}
}

func TestCodexImageArgsPrefersRelativePaths(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	imagePath := filepath.Join(targetDir, "prompt-images", "01-shot.png")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(imagePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := codexImageArgs(targetDir, []string{imagePath})
	if err != nil {
		t.Fatalf("codexImageArgs() error = %v", err)
	}
	want := []string{filepath.ToSlash(filepath.Join("prompt-images", "01-shot.png"))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codexImageArgs() = %v, want %v", got, want)
	}
}

func TestCodexImageArgsRejectsMissingPath(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	_, err := codexImageArgs(targetDir, []string{filepath.Join(targetDir, "missing.png")})
	if err == nil {
		t.Fatal("codexImageArgs() error = nil, want missing path error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "resolve image path") {
		t.Fatalf("codexImageArgs() error = %v, want resolve image path context", err)
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

func TestSelectAgentsPromptFileUsesStagedFileWhenTargetAgentsMissing(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(sourcePath, []byte("seeded instructions"), 0o644); err != nil {
		t.Fatalf("write source agents file: %v", err)
	}

	stagedPath, cleanup, err := selectAgentsPromptFile(targetDir, sourcePath)
	if err != nil {
		t.Fatalf("selectAgentsPromptFile() error = %v", err)
	}
	if stagedPath != sourcePath {
		t.Fatalf("stagedPath = %q, want %q", stagedPath, sourcePath)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target AGENTS.md was created: err=%v", err)
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
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "", codexRunOptions{}, sourcePath, ""); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}

	if runner.cmd.Name != "codex" || runner.cmd.Dir != targetDir {
		t.Fatalf("unexpected codex command: %+v", runner.cmd)
	}
	if got, want := len(runner.cmd.Args), 3; got != want {
		t.Fatalf("len(captured.Args) = %d, want %d", got, want)
	}
	prompt := runner.cmd.Stdin
	re := regexp.MustCompile(`Use (.+) as your primary implementation instructions`)
	matches := re.FindStringSubmatch(prompt)
	if len(matches) != 2 {
		t.Fatalf("staged agents prompt path missing from prompt: %q", prompt)
	}
	stagedPath := strings.TrimSpace(matches[1])
	if !strings.HasPrefix(stagedPath, "./.moltenhub-agents-") || !strings.HasSuffix(stagedPath, ".md") {
		t.Fatalf("staged agents path = %q, want hidden moltenhub seed path", stagedPath)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target AGENTS.md still exists after codex run: err=%v", err)
	}
}

func TestRunCodexUsesExistingTargetAgentsPromptFile(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	targetAgentsPath := filepath.Join(targetDir, "AGENTS.md")
	const existingAgentsContent = "repo-local instructions"
	if err := os.WriteFile(targetAgentsPath, []byte(existingAgentsContent), 0o644); err != nil {
		t.Fatalf("write target agents file: %v", err)
	}

	sourcePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(sourcePath, []byte("seeded instructions"), 0o644); err != nil {
		t.Fatalf("write source agents file: %v", err)
	}

	runner := &captureRunner{}
	h := New(runner)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "ship fix", codexRunOptions{}, sourcePath, ""); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}

	if !strings.Contains(runner.cmd.Stdin, "Use ./AGENTS.md as your primary implementation instructions before making any changes.") {
		t.Fatalf("captured prompt missing target AGENTS directive: %q", runner.cmd.Stdin)
	}
	if !strings.Contains(runner.cmd.Stdin, agentsCredentialGuardInstruction) {
		t.Fatalf("captured prompt missing credential guard instruction: %q", runner.cmd.Stdin)
	}

	data, err := os.ReadFile(targetAgentsPath)
	if err != nil {
		t.Fatalf("read target agents file: %v", err)
	}
	if got, want := string(data), existingAgentsContent; got != want {
		t.Fatalf("target AGENTS content = %q, want %q", got, want)
	}
}

func TestRunCodexInjectsResponseModePrompt(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	runner := &captureRunner{}

	h := New(runner)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "ship fix", codexRunOptions{}, "", "caveman-ultra"); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}

	if !strings.Contains(runner.cmd.Stdin, "Caveman response mode is enabled for this run only.") {
		t.Fatalf("captured prompt missing response-mode banner: %q", runner.cmd.Stdin)
	}
	if !strings.Contains(runner.cmd.Stdin, "Selected intensity: ultra.") {
		t.Fatalf("captured prompt missing selected intensity: %q", runner.cmd.Stdin)
	}
	if !strings.Contains(runner.cmd.Stdin, "Respond terse like smart caveman.") {
		t.Fatalf("captured prompt missing caveman skill body: %q", runner.cmd.Stdin)
	}
	if !strings.Contains(runner.cmd.Stdin, "ship fix") {
		t.Fatalf("captured prompt missing task prompt: %q", runner.cmd.Stdin)
	}
}

func TestRunCodexRetriesWithoutSandboxOnBwrapFailure(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "make home page pink"
	firstCmd := codexCommand(targetDir, prompt)
	retryCmd := firstCmd
	retryCmd.Args = overrideCodexSandbox(retryCmd.Args, "danger-full-access")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: I could not start any local repository command.",
				Stderr: "bwrap: namespace error: Operation not permitted",
			},
		},
		{
			cmd: retryCmd,
			res: execx.Result{Stdout: "done"},
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}
	if got := len(fake.exps); got != 0 {
		t.Fatalf("expected all fake runner commands to be consumed, remaining=%d", got)
	}
	joinedLogs := strings.Join(logs, "\n")
	if strings.Contains(joinedLogs, "status=error") {
		t.Fatalf("retryable sandbox failure logged terminal error before retry:\n%s", joinedLogs)
	}
	if !strings.Contains(joinedLogs, "status=warn action=retry_without_sandbox") {
		t.Fatalf("retry log missing retry_without_sandbox warning:\n%s", joinedLogs)
	}
}

func TestRunCodexChecksBranchInvariantBeforeSandboxRetry(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "make home page pink"
	firstCmd := codexCommand(targetDir, prompt)
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: I could not start any local repository command.",
				Stderr: "bwrap: namespace error: Operation not permitted",
			},
		},
	}}

	invariantCalls := 0
	h := New(fake)
	h.agentRetryInvariant = func(context.Context) error {
		invariantCalls++
		return errors.New("required branch changed")
	}
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "verify required branch before agent sandbox retry") {
		t.Fatalf("runCodex() error = %v, want branch invariant failure", err)
	}
	if invariantCalls != 1 {
		t.Fatalf("invariant calls = %d, want 1", invariantCalls)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if len(fake.calls) != 1 {
		t.Fatalf("agent commands = %d, want no danger-full-access retry", len(fake.calls))
	}
}

func TestRunCommandStreamRunnerMergesCapturedOutput(t *testing.T) {
	t.Parallel()

	runner := &streamCaptureRunner{
		res: execx.Result{
			Stdout: "Failure: I could not start any local repository command.",
		},
		lines: []streamLine{
			{stream: "stderr", line: "- Error detail: bwrap: No permissions to create a new namespace..."},
		},
	}

	h := New(runner)
	res, err := h.runCommand(
		context.Background(),
		"codex",
		execx.Command{Name: "codex", Args: []string{"exec", "--sandbox", "workspace-write"}},
	)
	if err != nil {
		t.Fatalf("runCommand() error = %v", err)
	}
	if !strings.Contains(res.Stderr, "No permissions to create a new namespace") {
		t.Fatalf("res.Stderr = %q, want merged streamed stderr detail", res.Stderr)
	}
	if !shouldRetryCodexWithoutSandbox(res, nil) {
		t.Fatal("shouldRetryCodexWithoutSandbox(...) = false, want true")
	}
}

func TestRunCommandSkipsLoggingEmptyStreamLines(t *testing.T) {
	t.Parallel()

	runner := &streamCaptureRunner{
		res: execx.Result{},
		lines: []streamLine{
			{stream: "stderr", line: ""},
			{stream: "stderr", line: "ERROR: failed to apply patch"},
		},
	}

	var logs []string
	h := New(runner)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if _, err := h.runCommand(
		context.Background(),
		"codex",
		execx.Command{Name: "codex", Args: []string{"exec"}},
	); err != nil {
		t.Fatalf("runCommand() error = %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("len(logs) = %d, want 1", len(logs))
	}
	if strings.HasSuffix(logs[0], "b64=") {
		t.Fatalf("log = %q, want non-empty encoded payload", logs[0])
	}
	if !strings.Contains(logs[0], "stream=stderr") {
		t.Fatalf("log = %q, want stderr stream marker", logs[0])
	}
}

func TestRunCodexReturnsErrorWhenCodexReportsFailure(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "make home page pink"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: I could not start any local repository command.",
				Stderr: "Error details:\n- Something went wrong",
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported failure error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
	if !strings.Contains(err.Error(), "Error details: - Something went wrong") {
		t.Fatalf("runCodex() error = %v, want explicit error details from codex output", err)
	}
}

func TestRunCodexReturnsErrorWhenCodexFailureOnlyHasStderrDetail(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix compile error"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: I could not update repository files.",
				Stderr: "permission denied writing /tmp/worktree",
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported failure error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
	if !strings.Contains(err.Error(), "Error details: permission denied writing /tmp/worktree") {
		t.Fatalf("runCodex() error = %v, want fallback error details from stderr", err)
	}
}

func TestRunCodexReturnsErrorWhenAgentReportsNoImplementationTarget(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix failing dispatch follow-up handling"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "`AGENTS.md` loaded. Repo checked. No implementation target given yet.\nSend bug/feature/change.",
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported failure error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
	if !strings.Contains(err.Error(), "Failure: agent did not identify an implementation target.") {
		t.Fatalf("runCodex() error = %v, want no-implementation-target failure", err)
	}
	if !strings.Contains(err.Error(), "Error details: `AGENTS.md` loaded. Repo checked. No implementation target given yet.") {
		t.Fatalf("runCodex() error = %v, want explicit no-implementation-target detail", err)
	}
}

func TestRunCodexDoesNotDowngradeMissingImplementationTargetToValidationGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix failing dispatch follow-up handling"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "`AGENTS.md` read. Repo ready.\nNo implementation target given. Send bug/feature request.",
				Stderr: "Failure: local validation command failed in runtime.\nError details: `npm run test` -> `sh: 1: vitest: not found`",
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want no-implementation-target failure")
	}
	if !strings.Contains(err.Error(), "Failure: agent did not identify an implementation target.") {
		t.Fatalf("runCodex() error = %v, want no-implementation-target failure", err)
	}
	if strings.Contains(err.Error(), "validation_tooling_unavailable") {
		t.Fatalf("runCodex() error = %v, should not downgrade to validation tooling gap", err)
	}
}

func TestRunCodexDoesNotDowngradeProductionHealthFailureToValidationGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "is production working and stable -> https://na.hub.molten.bot/health"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"Failure: Cannot confirm production working/stable.",
					"Error details: runtime cannot resolve `na.hub.molten.bot`. Repo health script failed after 3 attempts:",
					"`curl: (6) Could not resolve host: na.hub.molten.bot`",
					"`HTTP 000`",
					"No repo changes made.",
				}, "\n"),
				Stderr: strings.Join([]string{
					"If local test or validation tooling is unavailable in this runtime (for example `command not found` or missing `node_modules`), do not fail solely for that.",
					"Failure: Cannot confirm production working/stable.",
					"Error details: runtime cannot resolve `na.hub.molten.bot`.",
				}, "\n"),
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported production health failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
	if !strings.Contains(err.Error(), "Cannot confirm production working/stable") {
		t.Fatalf("runCodex() error = %v, want production health failure detail", err)
	}
	if strings.Contains(err.Error(), "validation_tooling_unavailable") {
		t.Fatalf("runCodex() error = %v, should not downgrade to validation tooling gap", err)
	}
}

func TestRunCodexAllowsValidationToolingMissingFailure(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "refresh seo metadata"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Could not run automated test suite in this runtime.",
				Stderr: "Error details: `sh: 1: vitest: not found`",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for validation-tooling gap", err)
	}
}

func TestRunCodexAllowsMissingCurlWhenSmokeFallbackSucceeded(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "rename application title"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{Stdout: strings.Join([]string{
				"Changed application title and related tests.",
				"Validation: `go test ./...` passed. Local server smoke check passed.",
				"Failure: Initial `curl` smoke command unavailable; fallback succeeded.",
				"Error details: `/bin/bash: curl: command not found`",
			}, "\n")},
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for recovered smoke tooling gap", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=validation_tooling_unavailable") {
		t.Fatalf("logs missing validation tooling warning:\n%s", strings.Join(logs, "\n"))
	}
}

func TestRunCodexRejectsMissingCurlWithoutSuccessfulSmokeFallback(t *testing.T) {
	t.Parallel()

	for _, detail := range []string{
		"Failure: Initial `curl` smoke command unavailable.\nError details: `/bin/bash: curl: command not found`",
		"Failure: Initial `curl` smoke command unavailable; fallback failed.\nError details: `/bin/bash: curl: command not found`",
	} {
		detail := detail
		t.Run(detail, func(t *testing.T) {
			t.Parallel()

			targetDir := t.TempDir()
			prompt := "rename application title"
			fake := &fakeRunner{t: t, exps: []expectedRun{
				{
					cmd: codexCommand(targetDir, prompt),
					res: execx.Result{Stdout: detail},
				},
			}}

			h := New(fake)
			err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
			if err == nil {
				t.Fatal("runCodex() error = nil, want unconfirmed smoke fallback failure")
			}
			if !strings.Contains(err.Error(), "codex reported failure") {
				t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
			}
		})
	}
}

func TestRunCodexAllowsLocalAutomatedTestsValidationGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "review hub diagnostics"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Could not run local automated tests in this runtime.",
				Stderr: "Error details: `npm test -- src/features/hub/components/__tests__/HubAgentsSection.test.tsx` -> `sh: 1: vitest: not found`",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local automated tests tooling gap", err)
	}
}

func TestRunCodexAllowsLocalAutomatedTestRunUnavailableValidationGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "adjust layout spacing"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local automated test run unavailable.",
				Stderr: "Error details: `npm run test -- --run src/components/__tests__/Layout.test.tsx` failed with `sh: 1: vitest: not found`.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local automated test run unavailable tooling gap", err)
	}
}

func TestRunCodexAllowsLocalAutomatedTestRunUnavailableWithExitStatus127(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "adjust dock icon stroke"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local automated test run unavailable in this runtime.",
				Stderr: "Error details: `npm run test -- --run src/components/__tests__/Layout.test.tsx` exited with status 127.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local automated test run unavailable exit-status tooling gap", err)
	}
}

func TestRunCodexAllowsBuildValidationToolingGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "adjust icon stroke weight"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Full build validation could not run in this runtime.",
				Stderr: "Error details: `npm run -s build` -> `sh: 1: tsc: not found`",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for build validation tooling gap", err)
	}
}

func TestRunCodexAllowsLocalValidationToolMissingInRuntime(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "check runtime diagnostics"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local validation tool missing in runtime.",
				Stderr: "Error details: `npm run test -- src/features/hub/components/__tests__/HubAgentsSection.test.tsx` failed with `sh: 1: vitest: not found`.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local validation tool missing in runtime", err)
	}
}

func TestRunCodexAllowsLocalTestRunnerUnavailableInRuntime(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "update organization profile tests"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local test runner unavailable in runtime.",
				Stderr: "Error details: `npm test -- --run src/features/hub/components/__tests__/HubOrganizationSection.test.tsx` failed with `sh: 1: vitest: not found`.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local test runner tooling gap", err)
	}
}

func TestRunCodexAllowsLocalLintValidationToolUnavailable(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "polish profile styles"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local lint validation tool unavailable.",
				Stderr: "Error details: `sh: 1: eslint: not found` while running `npm run lint`.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local lint tooling gap", err)
	}
}

func TestRunCodexAllowsValidationToolingGapWhenCommandReturnsError(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "run validation after docs change"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local build validation not runnable in runtime.",
				Stderr: "Error details: `npm run build` -> `sh: 1: tsc: not found`",
			},
			err: errors.New("run codex [exec]: exit status 1"),
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for validation tooling gap on non-zero exit", err)
	}
}

func TestRunCodexDoesNotDowngradeTransportFailureToValidationToolingGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "review pull request"
	firstCmd := codexCommand(targetDir, prompt)
	transportErr := errors.New("ERROR: stream disconnected before completion: error sending request for url (https://chatgpt.com/backend-api/codex/responses)")

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "If local validation tooling is unavailable in this runtime, do not fail solely for that validation gap.",
				Stderr: "source mentions tooling/deps not installed in runtime, then responses_websocket failed to lookup address information",
			},
			err: transportErr,
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want transport failure")
	}
	if !strings.Contains(err.Error(), "stream disconnected before completion") {
		t.Fatalf("runCodex() error = %v, want original transport error", err)
	}
}

func TestRunCodexAllowsLocalValidationCommandFailedInRuntime(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "run local validation"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: local validation command failed in runtime.",
				Stderr: "Error details: `npm run typecheck` -> `sh: 1: tsc: not found` (tooling/deps not installed in runtime).",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local validation command failed in runtime", err)
	}
}

func TestRunCodexAllowsLocalValidationCommandFailedWithToolingGap(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "run local validation"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: local validation command failed.",
				Stderr: "Error details: `npm run typecheck` -> `sh: 1: tsc: not found` (tooling/deps not installed in runtime).",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local validation command tooling gap", err)
	}
}

func TestRunCodexAllowsFullSuiteValidationUnavailableWithMissingDeps(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "improve unit test coverage"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"Failure: full-suite validation unavailable in runtime.",
					"Error details: `uv` missing, `python` shim missing; direct `python3 -m pytest` collection fails on missing deps including `pipecat`, `aiohttp`, `typer`, `loguru`.",
				}, "\n"),
				Stderr: "tokens used",
			},
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for full-suite validation tooling gap", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=validation_tooling_unavailable") {
		t.Fatalf("logs missing validation tooling warning:\n%s", strings.Join(logs, "\n"))
	}
}

func TestRunCodexAllowsNPMCheckMissingNodeModulesPluginResolution(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix ios scroll"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: `npm run check` failed.",
				Stderr: "Error details: `PluginError: Failed to resolve plugin for module \"expo-image-picker\" relative to \"../repo\". Do you have node modules installed?`",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for missing node_modules plugin-resolution tooling gap", err)
	}
}

func TestRunCodexAllowsLocalBuildValidationCommandFailedInRuntime(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "run local build validation"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Local build validation command failed in runtime.",
				Stderr: "Error details: `npm run build` -> `sh: 1: tsc: not found`",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for local build validation command failed in runtime", err)
	}
}

func TestRunCodexAllowsAlternativeValidationWhenLintToolMissing(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "thin icon line weights"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: Error details: - `npm run lint` failed with `sh: 1: eslint: not found`. | Alternative validation: | - `git diff --check` passed.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil when alternative validation is reported", err)
	}
}

func TestRunCodexAllowsFocusedValidationUnavailableFailure(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "make GitHub setup modal narrower"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: focused Go validation unavailable in runtime.",
				Stderr: "Error details: `/bin/sh: go: not found`",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for focused validation-tooling gap", err)
	}
}

func TestRunCodexAllowsRecoveredTransientRegistryLookupFailure(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "update OPENCLAW_VERSION"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: One registry query command failed during extra metadata check.",
				Stderr: "Error details: `npm error code EAI_AGAIN`, `getaddrinfo EAI_AGAIN registry.npmjs.org` (transient DNS/network), then retry succeeded.",
			},
		},
	}}

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for recovered transient registry lookup failure", err)
	}
}

func TestRunCodexReturnsErrorWhenTransientRegistryLookupDidNotRecover(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "update OPENCLAW_VERSION"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: "Failure: One registry query command failed during extra metadata check.",
				Stderr: "Error details: `npm error code EAI_AGAIN`, `getaddrinfo EAI_AGAIN registry.npmjs.org` (transient DNS/network).",
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported failure when transient registry lookup did not recover")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
}

func TestRunCodexReturnsTimeoutWhenAgentStageRunsTooLong(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	runner := &blockingContextRunner{}

	h := New(runner)
	h.AgentStageTimeout = 40 * time.Millisecond

	start := time.Now()
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "investigate timeout", codexRunOptions{}, "", "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runCodex() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "codex timed out after 40ms") {
		t.Fatalf("runCodex() error = %q, want explicit codex timeout detail", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runCodex() elapsed = %s, want fast timeout", elapsed)
	}
}

func TestRunCodexDoesNotApplyDefaultAgentStageTimeout(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	runner := &deadlineCaptureRunner{}

	h := New(runner)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "stay pink as long as needed", codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil", err)
	}
	if runner.hadDeadline {
		t.Fatal("runCodex() applied an unexpected default stage deadline")
	}
}

func TestRunCodexReturnsErrorWhenCodexReportsStructuredTaskFailure(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "add prompt image to /code page"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stderr: strings.Join([]string{
					`"summary": "Task failed.",`,
					`"message": "Task failed. One or more hub snapshot regions failed to refresh.",`,
					`"error": "One or more hub snapshot regions failed to refresh.",`,
					`"stack": "Error: One or more hub snapshot regions failed to refresh."`,
				}, "\n"),
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported failure error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
}

func TestShouldRetryCodexWithoutSandbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		res  execx.Result
		err  error
		want bool
	}{
		{
			name: "bwrap namespace error",
			res: execx.Result{
				Stderr: "bwrap: namespace error: Operation not permitted",
			},
			want: true,
		},
		{
			name: "explicit no-permissions namespace text",
			res: execx.Result{
				Stderr: "bwrap: No permissions to create a new namespace",
			},
			want: true,
		},
		{
			name: "model reports command start failure due sandbox",
			res: execx.Result{
				Stdout: "Failure: I could not start any local repository command.",
				Stderr: "The blocker is the sandbox/runtime environment.",
			},
			want: true,
		},
		{
			name: "generic task failure should not trigger retry",
			res: execx.Result{
				Stderr: "ERROR: failed to apply patch",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldRetryCodexWithoutSandbox(tt.res, tt.err); got != tt.want {
				t.Fatalf("shouldRetryCodexWithoutSandbox(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentOutputClaimsFileChangesUsesStdoutOnly(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Issue already fixed in `HEAD`.",
			"No new tracked diff.",
		}, "\n"),
		Stderr: strings.Join([]string{
			"exec /bin/bash -lc 'git diff -- src/main.jsx'",
			"Changed [AGENTS.md](/workspace/repo/AGENTS.md:1).",
			"diff --git a/generated.js b/generated.js",
		}, "\n"),
	}

	if agentOutputClaimsFileChanges(res) {
		t.Fatal("agentOutputClaimsFileChanges() = true for stderr transcript noise, want false")
	}
}

func TestAgentOutputClaimsFileChangesDetectsStdoutDiff(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Changed [src/main.jsx](/workspace/repo/src/main.jsx:5427).",
			"diff --git a/src/main.jsx b/src/main.jsx",
			"--- a/src/main.jsx",
			"+++ b/src/main.jsx",
		}, "\n"),
	}

	if !agentOutputClaimsFileChanges(res) {
		t.Fatal("agentOutputClaimsFileChanges() = false for stdout diff, want true")
	}
}

func TestCodexReportedFailure(t *testing.T) {
	t.Parallel()

	if failed, detail := codexReportedFailure(execx.Result{
		Stdout: "Failure: I could not start any local repository command.",
	}); !failed || !strings.HasPrefix(detail, "Failure:") {
		t.Fatalf("codexReportedFailure(failure line) = (%v, %q), want (true, 'Failure:...')", failed, detail)
	}

	if failed, detail := codexReportedFailure(execx.Result{
		Stdout: "All good. No changes needed.",
	}); failed || detail != "" {
		t.Fatalf("codexReportedFailure(success text) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureDetectsNoImplementationTaskGiven(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"`AGENTS.md` read. Repo Go harness.",
			"No implementation task given yet. Send target bug/feature/change.",
		}, "\n"),
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(no implementation task) = false, want true")
	}
	if !strings.Contains(detail, "Failure: agent did not identify an implementation target") {
		t.Fatalf("codexReportedFailure(no implementation task) detail = %q", detail)
	}
}

func TestCodexReportedFailureDetectsAwaitingTaskResponse(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: "AGENTS.md rules active. Caveman full active. Send task.",
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(send task) = false, want true")
	}
	if !strings.Contains(detail, "Failure: agent did not identify an implementation target") {
		t.Fatalf("codexReportedFailure(send task) detail = %q", detail)
	}
}

func TestCodexReportedFailureDetectsNoTaskGivenResponse(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"No task given yet.",
			"Repo read. `AGENTS.md` loaded. Go project. Worktree has untracked `AGENTS.md` only.",
			"Send actual change request: bug, feature, test failure, or PR review target.",
		}, "\n"),
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(no task given) = false, want true")
	}
	if !strings.Contains(detail, "Failure: agent did not identify an implementation target") {
		t.Fatalf("codexReportedFailure(no task given) detail = %q", detail)
	}
}

func TestCodexReportedFailureIgnoresSendTaskSummary(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: "Send task button now creates a queued dispatch request.",
	}

	failed, detail := codexReportedFailure(res)
	if failed || detail != "" {
		t.Fatalf("codexReportedFailure(send task summary) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureDetectsCompactStderrFailure(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stderr: strings.Join([]string{
			"Failure: I could not update repository files.",
			"Error details: permission denied writing /tmp/worktree",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); !failed || !strings.HasPrefix(detail, "Failure:") {
		t.Fatalf("codexReportedFailure(compact stderr failure) = (%v, %q), want (true, \"Failure:...\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresFailureMarkerInsideNoisyStderr(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: "Refactor complete. Shared logic centralized. Validation passed.",
		Stderr: strings.Join([]string{
			"OpenAI Codex v0.122.0",
			"workdir: /tmp/repo",
			"model: gpt-5.3-codex",
			"user",
			"Observed failure context:",
			"Failure: focused tests failed on compile due duplicated helper after refactor.",
			"Error details: duplicate helper function in internal/app/service.go",
			"codex",
			"tokens used",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(noisy stderr failure marker) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresInterimFailureMarkerInNoisyStdout(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Scanning repository and preparing focused tests.",
			"Found compile break and started fix.",
			"Failure: focused tests failed on compile due duplicated helper after refactor.",
			"Error details: internal/app/service.go:1905:6: connectedAgentSkills redeclared in this block",
			"apply patch",
			"patch: completed",
			"go test ./internal/app ./internal/web",
			"ok   \tgithub.com/moltenbot000/moltenhub-dispatch/internal/app",
			"ok   \tgithub.com/moltenbot000/moltenhub-dispatch/internal/web",
			"go test ./...",
			"ok   \tgithub.com/moltenbot000/moltenhub-dispatch/internal/hub",
			"Refactor complete. Tests pass.",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(interim stdout failure marker) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresBareFailureHeadingInCompletedSummary(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Done.",
			"Changed:",
			"- Added [GlobalStyles.astro](/tmp/repo/src/components/GlobalStyles.astro)",
			"Verification:",
			"- `npm run typecheck` passed.",
			"- `npm run build` passed.",
			"Failure:",
			"- `npm test` failed pre-existing `tests/unit/login-targets.test.ts` expectation.",
			"- `npm run lint` failed pre-existing `src/pages/code.astro` type errors.",
			"Remaining risk: visual comparison only smoke-level.",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(completed summary failure heading) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureKeepsBareFailureHeadingFatalWithoutCompletion(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Failure:",
			"Error details: could not write repository files.",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); !failed || !strings.HasPrefix(detail, "Failure:") {
		t.Fatalf("codexReportedFailure(bare fatal heading) = (%v, %q), want (true, \"Failure:...\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresNarrativeTaskFailedPhrasing(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Task failed previously because ci tooling was unavailable in runtime.",
			"Applied remediation and added unit tests for library task flow.",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(narrative task failed phrasing) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureDetectsTerminalFailureInNoisyStdout(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Scanning repository and preparing focused tests.",
			"apply patch",
			"patch: completed",
			"go test ./internal/app ./internal/web",
			"internal/app/service.go:1905:6: connectedAgentSkills redeclared in this block",
			"FAIL\tgithub.com/moltenbot000/moltenhub-dispatch/internal/app [build failed]",
			"Failure: focused tests failed on compile due duplicated helper after refactor.",
			"Error details: internal/app/service.go:1905:6: connectedAgentSkills redeclared in this block",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); !failed || !strings.HasPrefix(detail, "Failure:") {
		t.Fatalf("codexReportedFailure(terminal stdout failure marker) = (%v, %q), want (true, \"Failure:...\")", failed, detail)
	}
}

func TestCodexReportedFailureTreatsCompletedHubSnapshotRefreshWarningAsNonFatal(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Done. Blog post added at [src/data/blogposts.json](/tmp/repo/src/data/blogposts.json:2).",
			"Validation:",
			"`npm run generate:content` passed.",
			"`npm run build` passed after `npm ci`.",
			"Failure: prebuild hub snapshot refresh could not fetch live snapshot.",
			"Error details: `MOLTENHUB_ADMIN_SNAPSHOT_KEY is not configured for this build.` Build kept existing `hub-snapshot.json` and completed.",
			"Sources used: npm package facts from `npm view @moltenbot/railsmith`.",
		}, "\n"),
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(hub snapshot warning) = false, want initial terminal failure detection")
	}
	detail = codexFailureDetailWithErrorDetails(res, detail)
	if !isNonFatalHubSnapshotRefreshFailure(detail, res) {
		t.Fatalf("isNonFatalHubSnapshotRefreshFailure(...) = false, detail=%q", detail)
	}
}

func TestRunCodexAllowsBuildPreStepHubSnapshotRefreshWarning(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "remove fake copy from product page"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"Done.",
					"Verified:",
					"- `npm test` passed.",
					"- `npm run build` passed.",
					"Failure: Hub snapshot refresh unavailable during build pre-step, but build continued using existing snapshot.",
					"Error details: `MOLTENHUB_ADMIN_SNAPSHOT_KEY is not configured for this build` for NA and EU snapshot endpoints.",
				}, "\n"),
			},
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for completed hub snapshot warning", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=hub_snapshot_refresh_unavailable") {
		t.Fatalf("logs missing hub snapshot warning:\n%s", strings.Join(logs, "\n"))
	}
}

func TestRunCodexAllowsNonFatalPrebuildSnapshotRefreshWarning(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "display image in social sharing should be the product image"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"Done: src/pages/schweebles.astro now passes `productImage` to `PageLayout`.",
					"Verified:",
					"`npm test` passed: 36 files, 185 tests.",
					"`npm run typecheck` passed.",
					"`npm run build` passed, and `dist/schweebles/index.html` contains product image social tags.",
					"Validation note:",
					"Failure: non-fatal prebuild snapshot refresh warning.",
					"Error details: `MOLTENHUB_ADMIN_SNAPSHOT_KEY is not configured for this build` for NA/EU snapshot refresh. Build still completed.",
				}, "\n"),
			},
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for non-fatal snapshot refresh warning", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=hub_snapshot_refresh_unavailable") {
		t.Fatalf("logs missing hub snapshot warning:\n%s", strings.Join(logs, "\n"))
	}
}

func TestRunCodexAllowsRemoteDeploymentAuthUnavailableAfterLocalPass(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix Cloudflare preview build failure"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"No new repo diff. Existing branch already contains requested change.",
					"Validation passed:",
					"`npm test`",
					"`npm run build`",
					"`npm run cf:preview -- --dry-run`",
					"Failure: Cloudflare Workers Builds check still reported failed remotely.",
					"Error details: GitHub CI build passed. Cloudflare build logs require Cloudflare auth; local `wrangler whoami` says not authenticated, and `wrangler deployments list` needs `CLOUDFLARE_API_TOKEN`. Local Wrangler dry-run deploy passes, so no repo-side failure reproduced.",
				}, "\n"),
			},
		},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v, want nil for remote deployment auth gap after local pass", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "action=remote_deployment_auth_unavailable") {
		t.Fatalf("logs missing remote deployment auth warning:\n%s", strings.Join(logs, "\n"))
	}
}

func TestRunCodexKeepsRemoteDeploymentApiTokenFailureFatal(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix Cloudflare preview build failure"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"Failure: Cloudflare Workers Builds check still reported failed remotely.",
					"Error details: GitHub CI build passed. Remote build failed because deployment API token is rejected by provider. Local Wrangler dry-run deploy passes.",
				}, "\n"),
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want fatal remote deployment failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
}

func TestRunCodexKeepsRemoteDeploymentFailureFatalWhenRepoSideReproduced(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	prompt := "fix Cloudflare preview build failure"
	firstCmd := codexCommand(targetDir, prompt)

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{
			cmd: firstCmd,
			res: execx.Result{
				Stdout: strings.Join([]string{
					"Failure: Cloudflare Workers Builds check still reported failed remotely.",
					"Error details: Local `npm run build` also fails with TypeScript errors, so repo-side failure reproduced.",
				}, "\n"),
			},
		},
	}}

	h := New(fake)
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want fatal repo-side deployment failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
	}
}

func TestCodexReportedFailureDoesNotIgnoreIncompleteHubSnapshotRefreshFailure(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: strings.Join([]string{
			"Failure: prebuild hub snapshot refresh could not fetch live snapshot.",
			"Error details: `MOLTENHUB_ADMIN_SNAPSHOT_KEY is not configured for this build.`",
		}, "\n"),
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(incomplete hub snapshot failure) = false, want failure")
	}
	detail = codexFailureDetailWithErrorDetails(res, detail)
	if isNonFatalHubSnapshotRefreshFailure(detail, res) {
		t.Fatalf("isNonFatalHubSnapshotRefreshFailure(...) = true for incomplete build detail %q", detail)
	}
}

func TestCodexReportedFailureDetectsStructuredTaskFailurePayload(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stderr: strings.Join([]string{
			`"summary": "Task failed.",`,
			`"message": "Task failed. One or more hub snapshot regions failed to refresh.",`,
			`"error": "One or more hub snapshot regions failed to refresh.",`,
			`"stack": "Error: One or more hub snapshot regions failed to refresh."`,
		}, "\n"),
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(structured task failure payload) = false, want true")
	}
	if !strings.Contains(detail, `"summary": "Task failed."`) {
		t.Fatalf("codexReportedFailure(...) detail = %q, want task-failure summary line", detail)
	}
}

func TestCodexReportedFailureIgnoresStructuredTaskFailureInNoisyStderr(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: "- Deferred home hero JS init to load + idle callback.",
		Stderr: strings.Join([]string{
			"setTimeout(loadGA, 2000);",
			`"summary": "Task failed.",`,
			`"message": "Task failed. One or more hub snapshot regions failed to refresh.",`,
			`"error": "One or more hub snapshot regions failed to refresh.",`,
			`"stack": "Error: One or more hub snapshot regions failed to refresh."`,
			"</script> </body> </html>",
			"window.setTimeout(() => {",
			"setTimeout(() => {",
			"const summary = 'Task failed.';",
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(noisy stderr snippet) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresCompactStructuredStderrWhenStdoutHasSuccess(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stdout: "Implemented requested changes.",
		Stderr: strings.Join([]string{
			`"summary": "Task failed.",`,
			`"message": "Task failed. One or more hub snapshot regions failed to refresh.",`,
			`"error": "One or more hub snapshot regions failed to refresh.",`,
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(compact stderr + success stdout) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresGoStructStyleFailureSnippets(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stderr: strings.Join([]string{
			`Message: "Task failed because the downstream agent did not reply before the timeout.",`,
			`Error:   err.Error(),`,
			`Detail:  map[string]any{"timeout": true},`,
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(go struct snippet) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureDetectsLowercaseStructuredKeyValuePayload(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stderr: strings.Join([]string{
			`message: "Task failed because the downstream agent did not reply before the timeout."`,
			`error: "task timed out waiting for code_for_me"`,
		}, "\n"),
	}

	failed, detail := codexReportedFailure(res)
	if !failed {
		t.Fatal("codexReportedFailure(lowercase key-value payload) = false, want true")
	}
	if !strings.Contains(detail, `message: "Task failed because the downstream agent did not reply before the timeout."`) {
		t.Fatalf("codexReportedFailure(...) detail = %q, want message line", detail)
	}
}

func TestCodexReportedFailureIgnoresQuotedDispatchLogEcho(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stderr: strings.Join([]string{
			`dispatch request_id=local-1775867707-000003 cmd phase=codex name=codex stream=stderr text="\"summary\": \"Task failed.\","`,
			`dispatch request_id=local-1775867707-000003 cmd phase=codex name=codex stream=stderr text="\"error\": \"One or more hub snapshot regions failed to refresh.\","`,
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(dispatch log echo) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestCodexReportedFailureIgnoresGoStructSnippet(t *testing.T) {
	t.Parallel()

	res := execx.Result{
		Stderr: strings.Join([]string{
			`Message: "Task failed while dispatching to a connected agent.",`,
			`Error:   strings.TrimSpace(message.Error),`,
		}, "\n"),
	}

	if failed, detail := codexReportedFailure(res); failed || detail != "" {
		t.Fatalf("codexReportedFailure(go struct snippet) = (%v, %q), want (false, \"\")", failed, detail)
	}
}

func TestWithCompletionGatePromptIncludesAgentRuntimeGuidance(t *testing.T) {
	t.Parallel()

	got := withCompletionGatePrompt("Build API")
	wantSnippets := []string{
		"Agent input:\nBuild API",
		"Treat non-empty product, bug, feature, or review text in it as the implementation target.",
		"Do not answer that no implementation task was given when the agent input includes a requested repository change.",
		"When failures occur, send a response back to the calling agent that clearly states failure and includes the error details. Use explicit `Failure:` and `Error details:` fields.",
		"If local test or validation tooling is unavailable in this runtime (for example `command not found` or missing `node_modules`), do not fail solely for that and do not use `Failure:` solely for that validation gap.",
		"Before sharing repository or pull-request links in Hub activity, use `gh repo view OWNER/REPO --json isPrivate,nameWithOwner` during clone or PR tooling.",
		"Share repo and PR links only when GitHub reports `isPrivate:false`; never share private repository links.",
		"If a repository is not initialized after clone, use only gh CLI/git tools to create and push a main branch, then continue once git state is ready for work.",
		"Do not stop work just because you cannot create a pull request or watch remote CI/CD from inside this agent runtime.",
		"For implementation or repository-change requests, do not stop at analysis.",
		"Only return a no-op when the task is genuinely review/investigation-only",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("withCompletionGatePrompt() missing snippet %q", snippet)
		}
	}
}

func TestWithCompletionGatePromptPreservesTerseImplementationTarget(t *testing.T) {
	t.Parallel()

	prompt := strings.Join([]string{
		"in git chat",
		"in the repo listing with long text -> truncate and put an elipsis (...) when the text is over -> 100 chars",
	}, "\n")

	got := withCompletionGatePrompt(withAgentsPrompt(prompt, "./AGENTS.md"))
	wantSnippets := []string{
		"Agent input:",
		"Use ./AGENTS.md as your primary implementation instructions before making any changes.",
		"in git chat",
		"in the repo listing with long text -> truncate and put an elipsis (...) when the text is over -> 100 chars",
		"Task packet handling:",
		"Do not answer that no implementation task was given when the agent input includes a requested repository change.",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("withCompletionGatePrompt() missing snippet %q", snippet)
		}
	}

	if strings.Index(got, "in git chat") > strings.Index(got, "Task packet handling:") {
		t.Fatalf("withCompletionGatePrompt() put task handling before terse task: %q", got)
	}
}

func TestWithBackpressurePromptIncludesAdaptiveCriteria(t *testing.T) {
	t.Parallel()

	got := withBackpressurePrompt("Build API", nil)
	wantSnippets := []string{
		"Build API",
		backpressureHeading,
		"only consider the task done",
		"linting, formatting, type-checking, build, test",
		"curl against a local endpoint",
		"Playwright or an actual browser path",
		"benchmarks or performance measurements only when",
		"functional correctness, tests, types, security/privacy, brevity/scope control, and visual design",
		"Remote pull-request creation, pull-request check monitoring, and CI remediation are managed by the harness",
	}
	for _, snippet := range wantSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("withBackpressurePrompt() missing snippet %q in:\n%s", snippet, got)
		}
	}

	if again := withBackpressurePrompt(got, nil); again != got {
		t.Fatalf("withBackpressurePrompt() duplicated idempotent block:\n%s", again)
	}
}

func TestCollectBackpressureRequirementsReadsRootAndTargetFiles(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	targetDir := filepath.Join(repoDir, "services", "api")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, backpressureFilename), []byte("Run repo-wide audit."), 0o644); err != nil {
		t.Fatalf("write root backpressure: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, backpressureFilename), []byte("Exercise the API health route."), 0o644); err != nil {
		t.Fatalf("write target backpressure: %v", err)
	}

	h := New(&fakeRunner{t: t})
	requirements := h.collectBackpressureRequirements([]repoWorkspace{{Dir: repoDir, RelDir: "repo", URL: "git@github.com:acme/repo.git"}}, "services/api")
	got := withBackpressurePrompt("Build API", requirements)

	for _, snippet := range []string{
		"Project-specific backpressure requirements:",
		"From BACKPRESSURE.md:",
		"Run repo-wide audit.",
		"From services/api/BACKPRESSURE.md:",
		"Exercise the API health route.",
	} {
		if !strings.Contains(got, snippet) {
			t.Fatalf("prompt missing %q in:\n%s", snippet, got)
		}
	}
}

func TestCollectBackpressureRequirementsReadsMultiRepoRootFiles(t *testing.T) {
	t.Parallel()

	repoA := t.TempDir()
	repoB := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoA, backpressureFilename), []byte("Validate service A."), 0o644); err != nil {
		t.Fatalf("write repo A backpressure: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoB, backpressureFilename), []byte("Validate service B."), 0o644); err != nil {
		t.Fatalf("write repo B backpressure: %v", err)
	}

	h := New(&fakeRunner{t: t})
	requirements := h.collectBackpressureRequirements([]repoWorkspace{
		{Dir: repoA, RelDir: "repo-a", URL: "git@github.com:acme/repo-a.git"},
		{Dir: repoB, RelDir: "repo-b", URL: "git@github.com:acme/repo-b.git"},
	}, ".")
	got := withBackpressurePrompt("Build API", requirements)

	for _, snippet := range []string{
		"repo-a/BACKPRESSURE.md (git@github.com:acme/repo-a.git)",
		"Validate service A.",
		"repo-b/BACKPRESSURE.md (git@github.com:acme/repo-b.git)",
		"Validate service B.",
	} {
		if !strings.Contains(got, snippet) {
			t.Fatalf("prompt missing %q in:\n%s", snippet, got)
		}
	}
}

func TestRunCodexInjectsBackpressureBeforeResponseModeWrapping(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	runner := &captureRunner{}
	h := New(runner)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "ship fix", codexRunOptions{}, "", "caveman-full"); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}

	prompt := runner.cmd.Stdin
	if !strings.Contains(prompt, "Caveman response mode is enabled for this run only.") {
		t.Fatalf("prompt missing response mode wrapper:\n%s", prompt)
	}
	if !strings.Contains(prompt, backpressureHeading) {
		t.Fatalf("prompt missing backpressure contract:\n%s", prompt)
	}
	if strings.Index(prompt, backpressureHeading) < strings.Index(prompt, "ship fix") {
		t.Fatalf("backpressure contract should be appended after task context:\n%s", prompt)
	}
}

func TestBackpressurePromptCoversReviewAndRemediationPrompts(t *testing.T) {
	t.Parallel()

	reviewPrompt := withBackpressurePrompt("Review the pull request\n\nPrepared pull-request review context:\n{}", nil)
	for _, snippet := range []string{"Review the pull request", "Prepared pull-request review context:", backpressureHeading} {
		if !strings.Contains(reviewPrompt, snippet) {
			t.Fatalf("review prompt missing %q in:\n%s", snippet, reviewPrompt)
		}
	}

	basePrompt := withBackpressurePrompt("Build API", nil)
	repairPrompt := remediationPrompt(basePrompt, "https://github.com/acme/repo/pull/42", "unit tests failed", 1)
	if !strings.Contains(repairPrompt, backpressureHeading) {
		t.Fatalf("remediation prompt missing backpressure context:\n%s", repairPrompt)
	}
	if count := strings.Count(repairPrompt, backpressureHeading); count != 1 {
		t.Fatalf("remediation prompt has %d backpressure blocks, want 1:\n%s", count, repairPrompt)
	}
}

func TestSyncGeneratedWorkBranchFetchesAndMergesBase(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	repo := &repoWorkspace{
		URL:              "git@github.com:acme/repo.git",
		Dir:              repoDir,
		RelDir:           "repo",
		Branch:           "moltenhub-build-api",
		BaseBranch:       "main",
		CreateWorkBranch: true,
	}
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: fetchBranchCommand(repoDir, "main")},
		{cmd: mergeFetchedBranchCommand(repoDir)},
	}}

	h := New(fake)
	exitCode, stage, err := h.syncGeneratedWorkBranchWithBase(context.Background(), repo, agentruntime.Default(), repoDir, codexRunOptions{}, "Build API", "", "", ".", "codex")
	if err != nil {
		t.Fatalf("syncGeneratedWorkBranchWithBase() err = %v", err)
	}
	if exitCode != ExitSuccess || stage != "" {
		t.Fatalf("syncGeneratedWorkBranchWithBase() = (%d, %q), want success", exitCode, stage)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("remaining expected commands = %d", len(fake.exps))
	}
}

func TestSyncGeneratedWorkBranchSkipsExistingNonDefaultBranch(t *testing.T) {
	t.Parallel()

	repo := &repoWorkspace{
		URL:              "git@github.com:acme/repo.git",
		Dir:              t.TempDir(),
		RelDir:           "repo",
		Branch:           "release/2026.04-hotfix",
		BaseBranch:       "release/2026.04-hotfix",
		CreateWorkBranch: false,
	}
	fake := &fakeRunner{t: t}

	h := New(fake)
	exitCode, stage, err := h.syncGeneratedWorkBranchWithBase(context.Background(), repo, agentruntime.Default(), repo.Dir, codexRunOptions{}, "Build API", "", "", ".", "codex")
	if err != nil {
		t.Fatalf("syncGeneratedWorkBranchWithBase() err = %v", err)
	}
	if exitCode != ExitSuccess || stage != "" {
		t.Fatalf("syncGeneratedWorkBranchWithBase() = (%d, %q), want success", exitCode, stage)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("syncGeneratedWorkBranchWithBase() ran commands for non-default branch: %+v", fake.calls)
	}
}

func TestSyncGeneratedWorkBranchResolvesMergeConflictWithAgent(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	repo := &repoWorkspace{
		URL:              "git@github.com:acme/repo.git",
		Dir:              repoDir,
		RelDir:           "repo",
		Branch:           "moltenhub-build-api",
		BaseBranch:       "main",
		CreateWorkBranch: true,
	}
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: fetchBranchCommand(repoDir, "main")},
		{
			cmd: mergeFetchedBranchCommand(repoDir),
			res: execx.Result{Stdout: "CONFLICT (content): Merge conflict in file.go\nAutomatic merge failed; fix conflicts and then commit the result."},
			err: errors.New("merge failed"),
		},
		{cmd: codexCommand(repoDir, "resolve conflict")},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, baseSyncCommitMessage("main"))},
		{cmd: fetchBranchCommand(repoDir, "main")},
		{cmd: mergeFetchedBranchCommand(repoDir)},
	}}

	var logs []string
	h := New(fake)
	h.Logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	exitCode, stage, err := h.syncGeneratedWorkBranchWithBase(context.Background(), repo, agentruntime.Default(), repoDir, codexRunOptions{}, "Build API", "", "", ".", "codex")
	if err != nil {
		t.Fatalf("syncGeneratedWorkBranchWithBase() err = %v", err)
	}
	if exitCode != ExitSuccess || stage != "" {
		t.Fatalf("syncGeneratedWorkBranchWithBase() = (%d, %q), want success", exitCode, stage)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("remaining expected commands = %d", len(fake.exps))
	}
	joinedLogs := strings.Join(logs, "\n")
	for _, want := range []string{
		"mode=base_sync_conflict",
		"agent_run_id=agent-base_sync_conflict-repo-1",
		"repo=repo",
		"repo_dir=repo",
		"target=.",
	} {
		if !strings.Contains(joinedLogs, want) {
			t.Fatalf("logs missing base-sync workflow metadata %q:\n%s", want, joinedLogs)
		}
	}
}

func TestHasGitHubAuthToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	if hasGitHubAuthToken() {
		t.Fatal("hasGitHubAuthToken() = true, want false")
	}

	t.Setenv("GITHUB_TOKEN", "github_token_example")
	if !hasGitHubAuthToken() {
		t.Fatal("hasGitHubAuthToken() = false with GITHUB_TOKEN set, want true")
	}

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "github_token_from_gh_token")
	if !hasGitHubAuthToken() {
		t.Fatal("hasGitHubAuthToken() = false with GH_TOKEN set, want true")
	}
}

func TestShouldSetupGitHubAuthForRepos(t *testing.T) {
	t.Parallel()

	if shouldSetupGitHubAuthForRepos([]string{"git@github.com:acme/repo.git"}) {
		t.Fatal("shouldSetupGitHubAuthForRepos(ssh github) = true, want false")
	}
	if !shouldSetupGitHubAuthForRepos([]string{"https://github.com/acme/repo.git"}) {
		t.Fatal("shouldSetupGitHubAuthForRepos(https github) = false, want true")
	}
	if !shouldSetupGitHubAuthForRepos([]string{" http://github.com/acme/repo.git "}) {
		t.Fatal("shouldSetupGitHubAuthForRepos(http github) = false, want true")
	}
	if shouldSetupGitHubAuthForRepos([]string{"https://gitlab.com/acme/repo.git"}) {
		t.Fatal("shouldSetupGitHubAuthForRepos(non-github https) = true, want false")
	}
}

func TestRunHTTPSGitHubRepoConfiguresGitAuthWithoutEnvToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	cfg := sampleConfig()
	cfg.RepoURL = "https://github.com/acme/repo.git"
	cfg.Repo = cfg.RepoURL
	cfg.Repos = []string{cfg.RepoURL}

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "httpsauth123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

	fake := &fakeRunner{t: t, allowUnorderedClones: true, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "setup-git"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## moltenhub-build-api\n M file.go\n"}},
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
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
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
	if !shouldCreateWorkBranch("master") {
		t.Fatal("shouldCreateWorkBranch(master) = false, want true")
	}
	if shouldCreateWorkBranch("Main") {
		t.Fatal("shouldCreateWorkBranch(Main) = true, want false")
	}
	if shouldCreateWorkBranch("release/hotfix") {
		t.Fatal("shouldCreateWorkBranch(non-main) = true, want false")
	}
}

func TestLocalBranchFromStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name:   "branch only",
			stdout: "## moltenhub-branch\n",
			want:   "moltenhub-branch",
		},
		{
			name:   "branch with upstream",
			stdout: "## release/2026.04...origin/release/2026.04 [ahead 1]\n M file.go\n",
			want:   "release/2026.04",
		},
		{
			name:   "missing header",
			stdout: " M file.go\n",
			want:   "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := localBranchFromStatus(tt.stdout); got != tt.want {
				t.Fatalf("localBranchFromStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasTrackedWorktreeChanges(t *testing.T) {
	t.Parallel()

	if hasTrackedWorktreeChanges("## moltenhub-branch\n") {
		t.Fatal("hasTrackedWorktreeChanges(branch-only) = true, want false")
	}
	if !hasTrackedWorktreeChanges("## moltenhub-branch\n M file.go\n") {
		t.Fatal("hasTrackedWorktreeChanges(with diff) = false, want true")
	}
	if hasTrackedWorktreeChanges("\n") {
		t.Fatal("hasTrackedWorktreeChanges(empty) = true, want false")
	}
}

func TestHasAheadCommitsInStatus(t *testing.T) {
	t.Parallel()

	if !hasAheadCommitsInStatus("## moltenhub-branch...origin/moltenhub-branch [ahead 1]\n") {
		t.Fatal("hasAheadCommitsInStatus(ahead) = false, want true")
	}
	if hasAheadCommitsInStatus("## moltenhub-branch...origin/moltenhub-branch [behind 2]\n") {
		t.Fatal("hasAheadCommitsInStatus(behind) = true, want false")
	}
	if hasAheadCommitsInStatus(" M file.go\n") {
		t.Fatal("hasAheadCommitsInStatus(no-header) = true, want false")
	}
}

func TestValidateRequiredNonDefaultBranches(t *testing.T) {
	tests := []struct {
		name                string
		baseBranch          string
		defaultBranchOutput string
		currentBranchOutput string
		remoteHeadOutput    string
		wantErrMarker       string
	}{
		{
			name:          "rejects main even when custom default",
			baseBranch:    "main",
			wantErrMarker: `configured base branch "main" is a protected default branch name`,
		},
		{
			name:          "rejects master even when custom default",
			baseBranch:    "master",
			wantErrMarker: `configured base branch "master" is a protected default branch name`,
		},
		{
			name:          "rejects trunk even when custom default",
			baseBranch:    "trunk",
			wantErrMarker: `configured base branch "trunk" is a protected default branch name`,
		},
		{
			name:                "rejects repository custom default",
			baseBranch:          "release",
			defaultBranchOutput: "ref: refs/heads/release\tHEAD\nabc123\tHEAD\n",
			wantErrMarker:       `configured base branch "release" is repository default "release"`,
		},
		{
			name:                "accepts checked out remote feature branch",
			baseBranch:          "feature/conflicted",
			defaultBranchOutput: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n",
			currentBranchOutput: "feature/conflicted\n",
			remoteHeadOutput:    "def456\trefs/heads/feature/conflicted\n",
		},
		{
			name:                "fails closed when default missing",
			baseBranch:          "feature/conflicted",
			defaultBranchOutput: "abc123\tHEAD\n",
			wantErrMarker:       "returned no branch",
		},
		{
			name:                "rejects detached tag checkout",
			baseBranch:          "v1.2.3",
			defaultBranchOutput: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n",
			wantErrMarker:       `configured base branch "v1.2.3" left HEAD detached`,
		},
		{
			name:                "rejects mismatched checkout",
			baseBranch:          "feature/conflicted",
			defaultBranchOutput: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n",
			currentBranchOutput: "other-feature\n",
			wantErrMarker:       `current branch is "other-feature"`,
		},
		{
			name:                "rejects origin-prefixed local branch",
			baseBranch:          "feature/conflicted",
			defaultBranchOutput: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n",
			currentBranchOutput: "origin/feature/conflicted\n",
			wantErrMarker:       `current branch is "origin/feature/conflicted"`,
		},
		{
			name:                "rejects missing remote head",
			baseBranch:          "v1.2.3",
			defaultBranchOutput: "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n",
			currentBranchOutput: "v1.2.3\n",
			wantErrMarker:       `does not resolve to refs/heads/v1.2.3`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			repo := repoWorkspace{
				URL:        "git@github.com:acme/repo.git",
				Dir:        "/tmp/repo",
				BaseBranch: tt.baseBranch,
			}
			var exps []expectedRun
			if !isProtectedDefaultBranchName(tt.baseBranch) {
				exps = append(exps, expectedRun{
					cmd: remoteDefaultBranchCommand(repo.Dir),
					res: execx.Result{Stdout: tt.defaultBranchOutput},
				})
				defaultBranch := remoteDefaultBranchFromLSRemote(tt.defaultBranchOutput)
				if defaultBranch != "" && defaultBranch != normalizeBranchRef(tt.baseBranch) {
					exps = append(exps, expectedRun{
						cmd: currentBranchCommand(repo.Dir),
						res: execx.Result{Stdout: tt.currentBranchOutput},
					})
					if strings.TrimSpace(tt.currentBranchOutput) == normalizeBranchRef(tt.baseBranch) {
						exps = append(exps, expectedRun{
							cmd: remoteBranchExistsOnOriginCommand(repo.Dir, tt.baseBranch),
							res: execx.Result{Stdout: tt.remoteHeadOutput},
						})
					}
				}
			}
			fake := &fakeRunner{t: t, exps: exps}
			h := New(fake)
			err := h.validateRequiredNonDefaultBranches(context.Background(), config.Config{
				LibraryTaskName:          mergeMainLibraryTaskName,
				RequiresNonDefaultBranch: true,
			}, []repoWorkspace{repo})
			if tt.wantErrMarker == "" {
				if err != nil {
					t.Fatalf("validateRequiredNonDefaultBranches() error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErrMarker) {
				t.Fatalf("validateRequiredNonDefaultBranches() error = %v, want %q", err, tt.wantErrMarker)
			}
			if len(fake.exps) != 0 {
				t.Fatalf("unconsumed expectations: %d", len(fake.exps))
			}
		})
	}
}

func TestLibraryTaskRequiresNonDefaultBranchUsesCatalogMetadata(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{
			name: "metadata enables gate when caller omits bool",
			cfg:  config.Config{LibraryTaskName: mergeMainLibraryTaskName},
			want: true,
		},
		{
			name: "explicit requirement remains enabled for other task",
			cfg:  config.Config{LibraryTaskName: "unit-test-coverage", RequiresNonDefaultBranch: true},
			want: true,
		},
		{
			name: "unnamed direct config preserves explicit requirement",
			cfg:  config.Config{RequiresNonDefaultBranch: true},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := libraryTaskRequiresNonDefaultBranch(tt.cfg)
			if err != nil {
				t.Fatalf("libraryTaskRequiresNonDefaultBranch() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("libraryTaskRequiresNonDefaultBranch() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestValidateEnforcedNonDefaultBranchCheckoutsRejectsAnyRepoDrift(t *testing.T) {
	repos := []repoWorkspace{
		{
			URL:                      "git@github.com:acme/repo-a.git",
			Dir:                      "/tmp/repo-a",
			Branch:                   "feature/conflicted",
			RequiresNonDefaultBranch: true,
			RequiredBranch:           "feature/conflicted",
			RequiredBranchTask:       mergeMainLibraryTaskName,
		},
		{
			URL:                      "git@github.com:acme/repo-b.git",
			Dir:                      "/tmp/repo-b",
			Branch:                   "feature/conflicted",
			RequiresNonDefaultBranch: true,
			RequiredBranch:           "feature/conflicted",
			RequiredBranchTask:       mergeMainLibraryTaskName,
		},
	}
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: currentBranchCommand(repos[0].Dir), res: execx.Result{Stdout: "feature/conflicted\n"}},
		{cmd: currentBranchCommand(repos[1].Dir), res: execx.Result{Stdout: "main\n"}},
	}}

	err := New(fake).validateEnforcedNonDefaultBranchCheckouts(context.Background(), repos)
	if err == nil || !strings.Contains(err.Error(), `current branch is "main"`) || !strings.Contains(err.Error(), repos[1].URL) {
		t.Fatalf("validateEnforcedNonDefaultBranchCheckouts() error = %v, want second-repo drift", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestValidateEnforcedNonDefaultBranchCheckoutRejectsPublishRefDriftWithoutGit(t *testing.T) {
	repo := repoWorkspace{
		URL:                      "git@github.com:acme/repo.git",
		Dir:                      "/tmp/repo",
		Branch:                   "main",
		RequiresNonDefaultBranch: true,
		RequiredBranch:           "feature/conflicted",
		RequiredBranchTask:       mergeMainLibraryTaskName,
	}
	fake := &fakeRunner{t: t}

	err := New(fake).validateEnforcedNonDefaultBranchCheckout(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), `harness publish branch is "main"`) {
		t.Fatalf("validateEnforcedNonDefaultBranchCheckout() error = %v, want publish-ref drift", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("git calls = %#v, want rejection before git command", fake.calls)
	}
}

func TestRemoteBranchHeadExistsRequiresExactHeadsRef(t *testing.T) {
	t.Parallel()

	output := "abc123\trefs/heads/release/feature\ndef456\trefs/tags/feature\n"
	if remoteBranchHeadExists(output, "feature") {
		t.Fatal("remoteBranchHeadExists(feature) = true for suffix branch or tag, want false")
	}
	if !remoteBranchHeadExists(output, "release/feature") {
		t.Fatal("remoteBranchHeadExists(release/feature) = false, want true")
	}
}

func TestRemoteDefaultBranchFromLSRemote(t *testing.T) {
	t.Parallel()

	if got, want := remoteDefaultBranchFromLSRemote("ref: refs/heads/release/v2\tHEAD\nabc123\tHEAD\n"), "release/v2"; got != want {
		t.Fatalf("remoteDefaultBranchFromLSRemote() = %q, want %q", got, want)
	}
	if got, want := remoteDefaultBranchFromLSRemote("ref: refs/heads/origin/release\tHEAD\nabc123\tHEAD\n"), "origin/release"; got != want {
		t.Fatalf("remoteDefaultBranchFromLSRemote(origin branch) = %q, want %q", got, want)
	}
	if got := remoteDefaultBranchFromLSRemote("abc123\tHEAD\n"); got != "" {
		t.Fatalf("remoteDefaultBranchFromLSRemote(no symref) = %q, want empty", got)
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

func flagValue(args []string, flag string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}
