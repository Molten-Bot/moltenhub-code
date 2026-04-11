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
	"testing"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
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

func sampleConfig() config.Config {
	return config.Config{
		Version:       "v1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  "services/api",
		Prompt:        "Build API",
		CommitMessage: "feat: automate api",
		PRTitle:       "moltenhub-feat: automate api",
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
		{cmd: remoteBranchExistsOnOriginCommand(repoDir, branch)},
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

func TestRunNonMainBranchPushNonFastForwardRetriesWithRebase(t *testing.T) {
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
		{cmd: pullRebaseCommand(repoDir, cfg.BaseBranch)},
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
	if !strings.Contains(res.Err.Error(), "verify remote write access") {
		t.Fatalf("error = %v, want write-access context", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "Write access to repository not granted") {
		t.Fatalf("error = %v, want remote error detail", res.Err)
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

	fake := &fakeRunner{t: t, exps: []expectedRun{
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

	fake := &fakeRunner{t: t, exps: []expectedRun{
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

	remoteHead := remoteBranchExistsOnOriginCommand(repoDir, branch)
	wantRemoteHead := []string{"ls-remote", "--heads", "origin", branch}
	if remoteHead.Name != "git" || remoteHead.Dir != repoDir || !reflect.DeepEqual(remoteHead.Args, wantRemoteHead) {
		t.Fatalf("remote head command unexpected: %+v", remoteHead)
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

	jsonChecks := prChecksJSONCommand(repoDir, "https://github.com/acme/repo/pull/42", true)
	wantJSONChecks := []string{"pr", "checks", "https://github.com/acme/repo/pull/42", "--json", "name,bucket,completedAt,startedAt", "--required"}
	if jsonChecks.Name != "gh" || jsonChecks.Dir != repoDir || !reflect.DeepEqual(jsonChecks.Args, wantJSONChecks) {
		t.Fatalf("pr checks json command unexpected: %+v", jsonChecks)
	}

	jsonAnyChecks := prChecksJSONCommand(repoDir, "https://github.com/acme/repo/pull/42", false)
	wantJSONAnyChecks := []string{"pr", "checks", "https://github.com/acme/repo/pull/42", "--json", "name,bucket,completedAt,startedAt"}
	if jsonAnyChecks.Name != "gh" || jsonAnyChecks.Dir != repoDir || !reflect.DeepEqual(jsonAnyChecks.Args, wantJSONAnyChecks) {
		t.Fatalf("pr checks any json command unexpected: %+v", jsonAnyChecks)
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

	pushDryRun := pushDryRunCommand(repoDir, branch)
	wantPushDryRun := []string{"push", "--dry-run", "origin", "HEAD:refs/heads/" + branch}
	if pushDryRun.Name != "git" || pushDryRun.Dir != repoDir || !reflect.DeepEqual(pushDryRun.Args, wantPushDryRun) {
		t.Fatalf("push dry-run command unexpected: %+v", pushDryRun)
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
	if _, err := agentCommandWithOptions(piRuntime, targetDir, prompt, codexRunOptions{ImagePaths: []string{"x.png"}}); err == nil {
		t.Fatal("agentCommandWithOptions(pi with images) error = nil, want non-nil")
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
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, "", codexRunOptions{}, sourcePath); err != nil {
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
	if err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, ""); err != nil {
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
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "")
	if err == nil {
		t.Fatal("runCodex() error = nil, want codex reported failure error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "codex reported failure") {
		t.Fatalf("runCodex() error = %v, want codex reported failure marker", err)
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
	err := h.runCodex(context.Background(), agentruntime.Default(), targetDir, prompt, codexRunOptions{}, "")
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

func TestWithCompletionGatePromptIncludesFailureQueueContract(t *testing.T) {
	t.Parallel()

	got := withCompletionGatePrompt("Build API")
	wantSnippets := []string{
		"Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.",
		`When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.`,
		"When a task fails:",
		"Queue a follow-up task dedicated to reviewing the logs and fixing all underlying issues in this codebase.",
		"Pass the relevant failing file/folder log path(s) into that follow-up task context.",
		`{"repos":["git@github.com:Molten-Bot/moltenhub-code.git"],"baseBranch":"main","targetSubdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}`,
		"Completion requirements:",
		"If no file changes are required, return a clear no-op result with concrete evidence instead of forcing an empty PR.",
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
