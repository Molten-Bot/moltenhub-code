package harness

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

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/workspace"
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

func (f *fakeRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.exps) == 0 {
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
		f.t.Fatalf("command mismatch\n got:  %+v\n want: %+v", cmd, f.exps[0].cmd)
	}

	exp := f.exps[matchIndex]
	f.exps = append(f.exps[:matchIndex], f.exps[matchIndex+1:]...)
	f.calls = append(f.calls, cmd)
	return exp.res, exp.err
}

func commandsEqual(a, b execx.Command) bool {
	return a.Name == b.Name && a.Dir == b.Dir && reflect.DeepEqual(a.Args, b.Args)
}

func isCloneGitCommand(cmd execx.Command) bool {
	return cmd.Name == "git" && len(cmd.Args) > 0 && cmd.Args[0] == "clone"
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
		PRBody:        "Automated by MoltenHub Code\n\nOriginal task prompt:\n```text\nBuild API\n```\n\nIf you would like to connect agents together checkout [Molten Bot Hub](https://molten.bot/hub).",
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

func TestRunBuildsReviewContextBeforeInvokingCodex(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "Review the pull request"
	cfg.PRBody = "Automated by MoltenHub Code\n\nOriginal task prompt:\n```text\nReview the pull request\n```\n\nIf you would like to connect agents together checkout [Molten Bot Hub](https://molten.bot/hub)."
	cfg.Review = &config.ReviewConfig{PRNumber: 42}

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-review-the-pull-request"
	metadataJSON := `{"number":42,"title":"Improve tests","body":"Adds stronger coverage.","url":"https://github.com/acme/repo/pull/42","state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"feature/improve-tests","author":{"login":"octocat"}}`
	commentsText := "reviewer: Please add one more regression test."
	diffStat := " internal/service_test.go | 12 ++++++++++++\n 1 file changed, 12 insertions(+)"
	diffPatch := "diff --git a/internal/service_test.go b/internal/service_test.go\n+func TestServiceRegression(t *testing.T) {}\n"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: prReviewMetadataCommand(repoDir, "42"), res: execx.Result{Stdout: metadataJSON}},
		{cmd: fetchRemoteBranchCommand(repoDir, "main")},
		{cmd: fetchPullRequestHeadCommand(repoDir, 42)},
		{cmd: prReviewCommentsCommand(repoDir, "42"), res: execx.Result{Stdout: commentsText}},
		{cmd: reviewDiffStatCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffStat}},
		{cmd: reviewDiffPatchCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffPatch}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(strings.TrimSpace(cfg.Prompt+"\n\n"+expectedPreparedReviewContext(repoURLFromConfig(cfg), metadataJSON, commentsText, diffStat, diffPatch)), agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M docs/reviews/pr-42.md\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo/pull/43\n"}},
		{cmd: prChecksCommand(repoDir, "https://github.com/acme/repo/pull/43")},
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

func TestRunBuildsReviewContextFromHeadBranchSelector(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	cfg.Prompt = "Review the pull request"
	cfg.PRBody = "Automated by MoltenHub Code\n\nOriginal task prompt:\n```text\nReview the pull request\n```\n\nIf you would like to connect agents together checkout [Molten Bot Hub](https://molten.bot/hub)."
	cfg.Review = &config.ReviewConfig{HeadBranch: "feature/improve-tests"}

	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-review-the-pull-request"
	metadataJSON := `{"number":42,"title":"Improve tests","body":"Adds stronger coverage.","url":"https://github.com/acme/repo/pull/42","state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"feature/improve-tests","author":{"login":"octocat"}}`
	commentsText := "reviewer: Please add one more regression test."
	diffStat := " internal/service_test.go | 12 ++++++++++++\n 1 file changed, 12 insertions(+)"
	diffPatch := "diff --git a/internal/service_test.go b/internal/service_test.go\n+func TestServiceRegression(t *testing.T) {}\n"

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch)},
		{cmd: prReviewMetadataCommand(repoDir, "feature/improve-tests"), res: execx.Result{Stdout: metadataJSON}},
		{cmd: fetchRemoteBranchCommand(repoDir, "main")},
		{cmd: fetchPullRequestHeadCommand(repoDir, 42)},
		{cmd: prReviewCommentsCommand(repoDir, "feature/improve-tests"), res: execx.Result{Stdout: commentsText}},
		{cmd: reviewDiffStatCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffStat}},
		{cmd: reviewDiffPatchCommand(repoDir, remoteTrackingRef("main"), pullRequestTrackingRef(42)), res: execx.Result{Stdout: diffPatch}},
		{cmd: codexCommand(targetDir, withAgentsPrompt(strings.TrimSpace(cfg.Prompt+"\n\n"+expectedPreparedReviewContext(repoURLFromConfig(cfg), metadataJSON, commentsText, diffStat, diffPatch)), agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: " M docs/reviews/pr-42.md\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch)},
		{cmd: prCreateCommand(repoDir, cfg, branch), res: execx.Result{Stdout: "https://github.com/acme/repo/pull/43\n"}},
		{cmd: prChecksCommand(repoDir, "https://github.com/acme/repo/pull/43")},
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

func TestRunWithGitHubTokenRunsAuthSetupGitBeforeCodex(t *testing.T) {
	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"

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
		{cmd: codexCommandWithOptions(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath), codexRunOptions{
			ImagePaths: []string{imageArg},
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
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
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
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch), res: pushRejected, err: errors.New("push rejected")},
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
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
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
		{cmd: prChecksJSONCommand(repoDir, prURL, true), res: execx.Result{Stdout: snapshotJSON + "\n"}},
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
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
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
		{cmd: fetchMainBranchCommand(repoDir)},
		{cmd: pushDryRunCommand(repoDir, cfg.BaseBranch)},
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
	fallbackRepoB := "git@github.com:Molten-Bot/moltenhub-code.git"

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
		{cmd: prCreateCommand(repoDirB, cfg, branch), res: execx.Result{Stdout: "https://github.com/Molten-Bot/moltenhub-code/pull/20\n"}},
		{cmd: prChecksCommand(repoDirB, "https://github.com/Molten-Bot/moltenhub-code/pull/20")},
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
	fetchMain := fetchMainBranchCommand(repoDir)
	if fetchMain.Name != "git" || fetchMain.Dir != repoDir || !reflect.DeepEqual(fetchMain.Args, []string{"fetch", "origin", "main:refs/remotes/origin/main"}) {
		t.Fatalf("fetch main command unexpected: %+v", fetchMain)
	}
	cloneDefault := cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)
	if cloneDefault.Name != "git" || !reflect.DeepEqual(cloneDefault.Args, []string{"clone", "--single-branch", cfg.RepoURL, repoDir}) {
		t.Fatalf("clone default command unexpected: %+v", cloneDefault)
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
	})
	if codexWithImages.Name != "codex" || codexWithImages.Dir != targetDir || !reflect.DeepEqual(codexWithImages.Args, []string{
		"exec",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
		"--image", "/tmp/run/prompt-images/01-shot.png",
		"--image", "/tmp/run/prompt-images/02-shot.png",
	}) {
		t.Fatalf("codex image command unexpected: %+v", codexWithImages)
	}
	if got, want := codexWithImages.Stdin, withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("codex image stdin = %q, want %q", got, want)
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

	cfg.Reviewers = []string{"none"}
	prNoReviewer := prCreateCommand(repoDir, cfg, branch)
	if containsSequence(prNoReviewer.Args, []string{"--reviewer", "none"}) {
		t.Fatalf("pr args should omit none reviewer sentinel: %v", prNoReviewer.Args)
	}
	prNoBaseReviewer := prCreateWithoutBaseCommand(repoDir, cfg, branch)
	if containsSequence(prNoBaseReviewer.Args, []string{"--reviewer", "none"}) {
		t.Fatalf("pr without base args should omit none reviewer sentinel: %v", prNoBaseReviewer.Args)
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
	wantCommitsAhead := []string{"rev-list", "--count", "main..HEAD"}
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
	if shouldCreateWorkBranch("Main") {
		t.Fatal("shouldCreateWorkBranch(Main) = true, want false")
	}
	if shouldCreateWorkBranch("release/fix-ci") {
		t.Fatal("shouldCreateWorkBranch(non-main) = true, want false")
	}

	checks := prChecksCommand(repoDir, "https://github.com/acme/repo/pull/42")
	wantChecks := []string{"pr", "checks", "42", "--watch", "--required", "--interval", "10"}
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
	if ghFork.Name != "gh" || ghFork.Dir != repoDir || !reflect.DeepEqual(ghFork.Args, []string{"repo", "fork", "acme/repo", "--clone=false", "--remote=false"}) {
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

func TestPreflightCommandsWithRuntimeUseVersionForPi(t *testing.T) {
	t.Parallel()

	runtime, err := agentruntime.Resolve(agentruntime.HarnessPi, "pi-custom")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	cmds := preflightCommandsWithRuntime(runtime)
	if got, want := len(cmds), 3; got != want {
		t.Fatalf("len(preflight commands) = %d, want %d", got, want)
	}
	if got := cmds[2]; got.Name != "pi-custom" || !reflect.DeepEqual(got.Args, []string{"--version"}) {
		t.Fatalf("runtime preflight command = %+v", got)
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

	auggieRuntime, err := agentruntime.Resolve(agentruntime.HarnessAuggie, "")
	if err != nil {
		t.Fatalf("Resolve(auggie) error = %v", err)
	}
	auggieCmd, err := agentCommandWithOptions(auggieRuntime, targetDir, prompt, codexRunOptions{})
	if err != nil {
		t.Fatalf("agentCommandWithOptions(auggie) error = %v", err)
	}
	if auggieCmd.Name != "auggie" || auggieCmd.Dir != targetDir {
		t.Fatalf("unexpected auggie command: %+v", auggieCmd)
	}
	if got, want := auggieCmd.Args[len(auggieCmd.Args)-1], withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("auggie prompt arg = %q, want completion-gated prompt", got)
	}
	piRuntime, err := agentruntime.Resolve(agentruntime.HarnessPi, "")
	if err != nil {
		t.Fatalf("Resolve(pi) error = %v", err)
	}
	piCmd, err := agentCommandWithOptions(piRuntime, targetDir, prompt, codexRunOptions{})
	if err != nil {
		t.Fatalf("agentCommandWithOptions(pi) error = %v", err)
	}
	if piCmd.Name != "pi" || piCmd.Dir != targetDir {
		t.Fatalf("unexpected pi command: %+v", piCmd)
	}
	if got, want := piCmd.Args[:len(piCmd.Args)-1], []string{"--print", "--mode", "text", "--no-session"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected pi args prefix = %#v, want %#v", got, want)
	}
	if got, want := piCmd.Args[len(piCmd.Args)-1], withCompletionGatePrompt(prompt); got != want {
		t.Fatalf("pi prompt arg = %q, want completion-gated prompt", got)
	}
	if _, err := agentCommandWithOptions(claudeRuntime, targetDir, prompt, codexRunOptions{ImagePaths: []string{"x.png"}}); err == nil {
		t.Fatal("agentCommandWithOptions(claude with images) error = nil, want non-nil")
	}
	piImageCmd, err := agentCommandWithOptions(piRuntime, targetDir, prompt, codexRunOptions{ImagePaths: []string{"x.png"}})
	if err != nil {
		t.Fatalf("agentCommandWithOptions(pi with images) error = %v", err)
	}
	wantPiImageArgs := []string{"--print", "--mode", "text", "--no-session", "@x.png", withCompletionGatePrompt(prompt)}
	if !reflect.DeepEqual(piImageCmd.Args, wantPiImageArgs) {
		t.Fatalf("pi image args = %#v, want %#v", piImageCmd.Args, wantPiImageArgs)
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
}

func TestRunAppliesResponseModeAcrossNonCodexRuntimes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		harness string
	}{
		{name: "claude", harness: agentruntime.HarnessClaude},
		{name: "auggie", harness: agentruntime.HarnessAuggie},
		{name: "pi", harness: agentruntime.HarnessPi},
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

func TestEnsureTargetAgentsPromptFileCopiesAndCleansUp(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(sourcePath, []byte("seeded instructions"), 0o644); err != nil {
		t.Fatalf("write source agents file: %v", err)
	}

	stagedPath, cleanup, err := ensureTargetAgentsPromptFile(targetDir, sourcePath)
	if err != nil {
		t.Fatalf("ensureTargetAgentsPromptFile() error = %v", err)
	}
	if want := filepath.Join(targetDir, "AGENTS.md"); stagedPath != want {
		t.Fatalf("stagedPath = %q, want %q", stagedPath, want)
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
	if got, want := stagedPath, "./AGENTS.md"; got != want {
		t.Fatalf("staged agents path = %q, want %q", got, want)
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

	h := New(fake)
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "", ""); err != nil {
		t.Fatalf("runCodex() error = %v", err)
	}
	if got := len(fake.exps); got != 0 {
		t.Fatalf("expected all fake runner commands to be consumed, remaining=%d", got)
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
		"When failures occur, send a response back to the calling agent that clearly states failure and includes the error details. Use explicit `Failure:` and `Error details:` fields.",
		"If local test or validation tooling is unavailable in this runtime (for example `command not found`), do not fail solely for that.",
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
