package harness

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/failurefollowup"
	"github.com/Molten-Bot/moltenhub-code/internal/githubutil"
	"github.com/Molten-Bot/moltenhub-code/internal/slug"
	"github.com/Molten-Bot/moltenhub-code/internal/workspace"
)

const (
	ExitSuccess   = 0
	ExitUsage     = 2
	ExitConfig    = 10
	ExitPreflight = 20
	ExitAuth      = 21
	ExitWorkspace = 30
	ExitClone     = 40
	ExitCodex     = 50
	ExitGit       = 60
	ExitPR        = 70

	maxPRCheckRemediationAttempts = 3
	prChecksWatchIntervalSeconds  = 10
	// Allow up to ~3 minutes for newly-created PR checks to appear before remediation.
	maxPRChecksNoReportRetries       = 18
	prChecksNoReportRetryDelay       = 10 * time.Second
	maxCheckSummaryChars             = 4000
	defaultCIWorkflowPath            = ".github/workflows/ci.yml"
	maxPushSyncAttempts              = 3
	maxCloneAttempts                 = 3
	cloneRetryDelay                  = 2 * time.Second
	maxAuthSetupGitAttempts          = 3
	authSetupGitRetryDelay           = 350 * time.Millisecond
	maxCloneErrorDetailChars         = 500
	maxGitErrorDetailChars           = 500
	maxReviewMetadataChars           = 12000
	maxReviewCommentsChars           = 16000
	maxReviewDiffStatChars           = 12000
	maxReviewDiffPatchChars          = 30000
	bootstrapGitUserName             = "MoltenHub Code"
	bootstrapGitUserEmail            = "bot@molten.bot"
	bootstrapMainCommitMessage       = "chore: initialize main branch"
	agentsCredentialGuardInstruction = "YOU ARE NOT ALLOWED TO SHARE: GITHUB PAT and YOUR (AGENTS) AUTH CREDENTIALS"
	prCommentScreenshotsRelDir       = ".moltenhub/pr-comment-screenshots"
	publishRemoteOrigin              = "origin"
	publishRemoteFork                = "fork"
	publishStrategyDirect            = "direct"
	publishStrategyForkFallback      = "fork-fallback"
)

type logFn func(string, ...any)

var authSetupGitMu sync.Mutex

// Result captures run output and status.
type Result struct {
	ExitCode     int
	Err          error
	WorkspaceDir string
	Branch       string
	PRURL        string
	NoChanges    bool
	RepoResults  []RepoResult
}

// RepoResult captures outcome details for one repository in a run.
type RepoResult struct {
	RepoURL string
	RepoDir string
	Branch  string
	PRURL   string
	Changed bool
}

type repoWorkspace struct {
	URL                         string
	Dir                         string
	RelDir                      string
	Branch                      string
	PRURL                       string
	PRHeadRef                   string
	PRHeadOwner                 string
	PRTargetRepo                string
	BaseBranch                  string
	CreateWorkBranch            bool
	Changed                     bool
	WriteAccessChecked          bool
	WriteAccessAllowed          bool
	WriteAccessErr              error
	PushRemote                  string
	PublishStrategy             string
	PRCommentScreenshotBaseline map[string]string
	PRCommentScreenshotFiles    []string
}

type codexRunOptions = agentruntime.RunOptions

// Harness executes the clone -> codex -> PR workflow.
type Harness struct {
	Runner      execx.Runner
	Workspace   workspace.Manager
	Now         func() time.Time
	Logf        logFn
	TargetDirOK func(string) bool
	Sleep       func(context.Context, time.Duration) error
	// AgentStageTimeout bounds each agent execution attempt when positive;
	// zero or negative disables the timeout.
	AgentStageTimeout time.Duration
}

// New returns a harness configured with defaults.
func New(runner execx.Runner) Harness {
	return Harness{
		Runner:      runner,
		Workspace:   workspace.NewManager(),
		Now:         time.Now,
		Logf:        func(string, ...any) {},
		TargetDirOK: pathIsDir,
		Sleep:       sleepWithContext,
	}
}

// Run executes a full automation attempt.
func (h Harness) Run(ctx context.Context, cfg config.Config) Result {
	if h.Runner == nil {
		return h.fail(ExitUsage, "usage", fmt.Errorf("runner is required"), "")
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return h.fail(ExitConfig, "config", err, "")
	}
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.Logf == nil {
		h.Logf = func(string, ...any) {}
	}
	if h.TargetDirOK == nil {
		h.TargetDirOK = pathIsDir
	}
	if h.Sleep == nil {
		h.Sleep = sleepWithContext
	}
	runtime, err := agentruntime.Resolve(cfg.AgentHarness, cfg.AgentCommand)
	if err != nil {
		return h.fail(ExitConfig, "config", err, "")
	}
	if err := validateRuntimePromptImages(runtime, cfg.Images); err != nil {
		return h.fail(ExitConfig, "config", err, "")
	}
	agentStage := runtimeLogStage(runtime)

	h.logf("stage=preflight status=start")
	for _, cmd := range preflightCommandsWithRuntime(runtime) {
		if _, err := h.runCommand(ctx, "preflight", cmd); err != nil {
			return h.fail(ExitPreflight, "preflight", err, "")
		}
	}
	h.logf("stage=preflight status=ok")

	h.logf("stage=auth status=start")
	if _, err := h.runCommand(ctx, "auth", authCommand()); err != nil {
		return h.fail(ExitAuth, "auth", err, "")
	}
	if shouldSetupGitHubAuthForRepos(cfg.RepoList()) || hasGitHubAuthToken() {
		if err := h.runAuthSetupGit(ctx); err != nil {
			return h.fail(ExitAuth, "auth", err, "")
		}
	}
	h.logf("stage=auth status=ok")

	h.logf("stage=workspace status=start")
	runDir, guid, err := h.Workspace.CreateRunDir()
	if err != nil {
		return h.fail(ExitWorkspace, "workspace", err, "")
	}
	agentsPath, err := h.Workspace.SeedAgentsFile(runDir)
	if err != nil {
		h.logf("stage=workspace status=warn action=seed_agents err=%q", err)
		agentsPath = ""
	}
	h.logf("stage=workspace status=ok run_dir=%s guid=%s agents=%s", runDir, guid, agentsPath)

	repoURLs := cfg.RepoList()
	if len(repoURLs) == 0 {
		return h.fail(ExitConfig, "config", fmt.Errorf("one of repo, repoUrl, or repos[] is required"), runDir)
	}
	runCfg := cfg
	cloneBaseBranch := strings.TrimSpace(runCfg.BaseBranch)

	repos := make([]repoWorkspace, 0, len(repoURLs))
	for i, repoURL := range repoURLs {
		relDir := repoWorkspaceDirName(repoURL, i, len(repoURLs))
		repoDir := filepath.Join(runDir, relDir)
		repos = append(repos, repoWorkspace{
			URL:             repoURL,
			Dir:             repoDir,
			RelDir:          relDir,
			PushRemote:      publishRemoteOrigin,
			PublishStrategy: publishStrategyDirect,
		})
	}
	if err := h.cloneRepositories(ctx, repos, cloneBaseBranch); err != nil {
		return h.fail(ExitClone, "clone", err, runDir)
	}
	if len(repos) > 0 {
		runCfg.BaseBranch = repos[0].BaseBranch
	}

	targetDir, err := resolveTargetDir(repos[0].Dir, cfg.TargetSubdir)
	if err != nil {
		return h.fail(ExitConfig, "config", err, runDir)
	}
	if !h.TargetDirOK(targetDir) {
		return h.fail(ExitConfig, "config", fmt.Errorf("targetSubdir does not exist or is not a directory: %s", cfg.TargetSubdir), runDir)
	}

	generatedBranch := slug.BranchName(cfg.Prompt, h.Now(), guid)
	for i := range repos {
		branch := strings.TrimSpace(repos[i].BaseBranch)
		if repos[i].CreateWorkBranch {
			branch = generatedBranch
		}
		repos[i].Branch = branch
		if !repos[i].CreateWorkBranch {
			h.logf(
				"stage=git status=ok action=branch_reuse branch=%s baseBranch=%s repo=%s repo_dir=%s",
				branch,
				repos[i].BaseBranch,
				repos[i].URL,
				repos[i].RelDir,
			)
			continue
		}
		h.logf("stage=git status=start action=branch branch=%s repo=%s repo_dir=%s", branch, repos[i].URL, repos[i].RelDir)
		if _, err := h.runCommand(ctx, "git", branchCommand(repos[i].Dir, branch)); err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		h.logf("stage=git status=ok action=branch branch=%s repo=%s repo_dir=%s", branch, repos[i].URL, repos[i].RelDir)
	}
	for i := range repos {
		if err := h.preparePublishWorkflow(ctx, &repos[i]); err != nil {
			return h.fail(ExitGit, "workflow", err, runDir)
		}
	}
	for i := range repos {
		screenshotSnapshot, err := prCommentScreenshotSnapshot(repos[i].Dir)
		if err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		repos[i].PRCommentScreenshotBaseline = screenshotSnapshot
	}

	codexDir := targetDir
	if len(repos) > 1 {
		codexDir = runDir
	}
	imagePaths, err := materializePromptImages(runDir, cfg.Images)
	if err != nil {
		return h.fail(ExitConfig, "config", err, runDir)
	}
	imageArgs, err := codexImageArgs(codexDir, imagePaths)
	if err != nil {
		return h.fail(ExitConfig, "config", err, runDir)
	}
	codexOpts := codexRunOptions{
		SkipGitRepoCheck: len(repos) > 1,
		ImagePaths:       imageArgs,
	}
	if len(imageArgs) > 0 {
		codexOpts.WritableDirs = []string{runDir}
	}
	codexBasePrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, repos)
	codexBasePrompt = withPromptImagePaths(codexBasePrompt, imageArgs)
	if reviewPrompt, err := h.prepareReviewPrompt(ctx, runCfg, repos, codexBasePrompt); err != nil {
		return h.fail(ExitPR, "review", err, runDir)
	} else {
		codexBasePrompt = reviewPrompt
	}
	codexTargetLabel := codexTargetLabel(cfg.TargetSubdir, len(repos) > 1)

	h.logf("stage=%s status=start target=%s", agentStage, codexTargetLabel)
	codexStart := time.Now()
	if err := h.runCodex(ctx, runtime, codexDir, codexBasePrompt, codexOpts, agentsPath, cfg.ResponseMode); err != nil {
		return h.fail(ExitCodex, agentStage, err, runDir)
	}
	h.logf("stage=%s status=ok elapsed_s=%d", agentStage, int(time.Since(codexStart).Seconds()))

	for i := range repos {
		statusRes, err := h.runCommand(ctx, "git", statusCommand(repos[i].Dir))
		if err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		repos[i].Branch = pickFirstNonEmpty(localBranchFromStatus(statusRes.Stdout), repos[i].Branch)
		syncRepoPublishHeadRef(&repos[i])
		changed, detectErr := h.repoHasPendingChanges(ctx, repos[i], statusRes.Stdout)
		if detectErr != nil {
			return h.fail(ExitGit, "git", detectErr, runDir)
		}
		repos[i].Changed = changed
		h.logf("stage=git status=scan repo=%s repo_dir=%s changed=%t", repos[i].URL, repos[i].RelDir, repos[i].Changed)
	}

	changedCount := 0
	for _, repo := range repos {
		if repo.Changed {
			changedCount++
		}
	}
	if changedCount == 0 {
		h.populateNoChangePRURLs(ctx, repos)
		h.logf("stage=git status=no_changes")
		res := buildResult(runDir, repos, true)
		res.ExitCode = ExitSuccess
		return res
	}

	for i := range repos {
		if !repos[i].Changed {
			continue
		}
		if exitCode, stage, err := h.processChangedRepo(
			ctx,
			runCfg,
			&repos[i],
			runtime,
			codexDir,
			codexOpts,
			codexBasePrompt,
			agentsPath,
			codexTargetLabel,
			agentStage,
			len(repos) > 1,
		); err != nil {
			return h.failWithRepos(exitCode, stage, err, runDir, repos)
		}
	}

	changedCount = 0
	for _, repo := range repos {
		if repo.Changed {
			changedCount++
		}
	}
	if changedCount == 0 {
		h.populateNoChangePRURLs(ctx, repos)
		h.logf("stage=git status=no_changes")
		res := buildResult(runDir, repos, true)
		res.ExitCode = ExitSuccess
		return res
	}

	res := buildResult(runDir, repos, false)
	res.ExitCode = ExitSuccess
	return res
}

func (h Harness) processChangedRepo(
	ctx context.Context,
	cfg config.Config,
	repo *repoWorkspace,
	runtime agentruntime.Runtime,
	codexDir string,
	codexOpts codexRunOptions,
	codexBasePrompt string,
	agentsPath string,
	codexTargetLabel string,
	agentStage string,
	multiRepo bool,
) (int, string, error) {
	if repo == nil {
		return ExitConfig, "config", fmt.Errorf("repo workspace is required")
	}
	if repo.WriteAccessChecked && !repo.WriteAccessAllowed {
		if repo.WriteAccessErr != nil {
			return ExitGit, "git", fmt.Errorf("cannot publish changes for repo %s branch %q: %w", repo.URL, repo.Branch, repo.WriteAccessErr)
		}
		return ExitGit, "git", fmt.Errorf("cannot publish changes for repo %s branch %q: remote write access unavailable", repo.URL, repo.Branch)
	}
	if !repo.WriteAccessChecked {
		if err := h.preparePublishWorkflow(ctx, repo); err != nil {
			return ExitGit, "workflow", err
		}
	}

	h.logf("stage=git status=start action=commit repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	if _, err := h.runCommand(ctx, "git", addCommand(repo.Dir)); err != nil {
		return ExitGit, "git", err
	}
	if screenshotFiles, err := changedPRCommentScreenshotFiles(repo.Dir, repo.PRCommentScreenshotBaseline); err != nil {
		return ExitGit, "git", err
	} else if len(screenshotFiles) > 0 {
		repo.PRCommentScreenshotFiles = screenshotFiles
		if _, err := h.runCommand(ctx, "git", addPRCommentScreenshotsCommand(repo.Dir, screenshotFiles)); err != nil {
			return ExitGit, "git", err
		}
	}
	commitRes, commitErr := h.runCommand(ctx, "git", commitCommand(repo.Dir, cfg.CommitMessage))
	if commitErr != nil {
		noChanges, statusErr := h.refreshRepoChangeStateAfterNoOpCommit(ctx, repo, commitRes, commitErr)
		if statusErr != nil {
			return ExitGit, "git", statusErr
		}
		if noChanges {
			h.logf("stage=git status=ok action=commit repo=%s repo_dir=%s reason=no_changes_after_add", repo.URL, repo.RelDir)
			return ExitSuccess, "git", nil
		}
		if !isNothingToCommitResult(commitRes, commitErr) {
			return ExitGit, "git", commitErr
		}
		h.logf("stage=git status=ok action=commit repo=%s repo_dir=%s reason=already_committed", repo.URL, repo.RelDir)
	}
	if err := h.pushWithSync(ctx, *repo, 0); err != nil {
		return ExitGit, "git", err
	}
	h.logf("stage=git status=ok action=commit repo=%s repo_dir=%s", repo.URL, repo.RelDir)

	h.logf("stage=pr status=start repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	createWorkBranch := repo.CreateWorkBranch
	headRef := repoPRHeadRef(*repo)
	targetRepo := repoPRTargetRepo(*repo)
	if !createWorkBranch {
		prURL, err := h.lookupOpenPRURLByHead(ctx, *repo)
		if err != nil {
			return ExitPR, "pr", err
		}
		repo.PRURL = prURL
	}

	if repo.PRURL == "" {
		var (
			prRes execx.Result
			err   error
		)
		if createWorkBranch {
			prRes, err = h.runCommand(ctx, "pr", prCreateWithOptionsCommand(repo.Dir, cfg, repo.BaseBranch, headRef, targetRepo))
		} else {
			prRes, err = h.runCommand(ctx, "pr", prCreateWithoutBaseWithOptionsCommand(repo.Dir, cfg, headRef, targetRepo))
		}
		if err != nil {
			if existingPRURL, ok := existingPRURLFromCreateFailure(prRes, err); ok {
				repo.PRURL = existingPRURL
				h.logf(
					"stage=pr status=warn action=reuse_existing reason=already_exists repo=%s repo_dir=%s branch=%s pr_url=%s",
					repo.URL,
					repo.RelDir,
					repo.Branch,
					repo.PRURL,
				)
			} else {
				return ExitPR, "pr", err
			}
		}
		if repo.PRURL == "" {
			repo.PRURL = extractFirstURL(prRes.Stdout)
		}
		if repo.PRURL == "" {
			repo.PRURL = extractFirstURL(prRes.Stderr)
		}
		if repo.PRURL == "" {
			prURL, verifyErr := h.lookupOpenPRURLByHead(ctx, *repo)
			if verifyErr != nil {
				return ExitPR, "pr", fmt.Errorf("verify open pull request for repo %s: %w", repo.URL, verifyErr)
			}
			repo.PRURL = prURL
		}
		if repo.PRURL == "" {
			return ExitPR, "pr", fmt.Errorf("gh pr create did not return a PR URL for repo %s", repo.URL)
		}
	}
	h.logf("stage=pr status=ok repo=%s repo_dir=%s pr_url=%s", repo.URL, repo.RelDir, repo.PRURL)
	if err := h.commentPRScreenshots(ctx, *repo, repo.PRCommentScreenshotFiles); err != nil {
		h.logf(
			"stage=pr status=warn action=comment_screenshots repo=%s repo_dir=%s pr_url=%s err=%q",
			repo.URL,
			repo.RelDir,
			repo.PRURL,
			err,
		)
	}

	for attempt := 0; ; attempt++ {
		var (
			checkRes     execx.Result
			checkErr     error
			checkSummary string
		)
		for noReportRetry := 0; ; noReportRetry++ {
			requiredChecksOnly := true
			h.logf("stage=checks status=start repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
			checkRes, checkErr = h.runCommand(ctx, "checks", prChecksCommand(repo.Dir, repo.PRURL))
			if checkErr != nil && isNoRequiredChecksReported(checkRes, checkErr) {
				h.logf(
					"stage=checks status=fallback reason=no_required_checks repo=%s repo_dir=%s pr_url=%s attempt=%d",
					repo.URL,
					repo.RelDir,
					repo.PRURL,
					attempt+1,
				)
				requiredChecksOnly = false
				checkRes, checkErr = h.runCommand(ctx, "checks", prChecksAnyCommand(repo.Dir, repo.PRURL))
			}
			if checkErr == nil {
				h.logf("stage=checks status=ok repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
				return ExitSuccess, "", nil
			}

			checkSummary = summarizeCheckOutput(checkRes)
			if shouldReconcileChecksAfterFailure(checkRes, checkErr) {
				if reconciled, latestSummary, reconcileErr := h.reconcileChecksAfterFailure(ctx, *repo, requiredChecksOnly); reconcileErr == nil {
					if latestSummary != "" {
						checkSummary = latestSummary
					}
					if reconciled {
						h.logf(
							"stage=checks status=ok reason=latest_snapshot repo=%s repo_dir=%s pr_url=%s attempt=%d",
							repo.URL,
							repo.RelDir,
							repo.PRURL,
							attempt+1,
						)
						return ExitSuccess, "", nil
					}
				} else {
					h.logf(
						"stage=checks status=warn action=latest_snapshot reason=query_failed repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.PRURL,
						attempt+1,
						reconcileErr,
					)
				}
			}
			noChecksReported := isNoChecksReported(checkRes, checkErr)
			if noChecksReported && noReportRetry == 0 {
				h.logf(
					"stage=checks status=start action=workflow_dispatch reason=no_checks_reported repo=%s repo_dir=%s branch=%s workflow=%s attempt=%d",
					repo.URL,
					repo.RelDir,
					repo.Branch,
					defaultCIWorkflowPath,
					attempt+1,
				)
				if _, dispatchErr := h.runCommand(ctx, "checks", workflowDispatchCommand(repo.Dir, repo.Branch)); dispatchErr != nil {
					h.logf(
						"stage=checks status=warn action=workflow_dispatch reason=failed repo=%s repo_dir=%s branch=%s workflow=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.Branch,
						defaultCIWorkflowPath,
						attempt+1,
						dispatchErr,
					)
				} else {
					h.logf(
						"stage=checks status=ok action=workflow_dispatch repo=%s repo_dir=%s branch=%s workflow=%s attempt=%d",
						repo.URL,
						repo.RelDir,
						repo.Branch,
						defaultCIWorkflowPath,
						attempt+1,
					)
				}
			}
			if noChecksReported && noReportRetry >= maxPRChecksNoReportRetries {
				if noRequired, noRequiredSummary, requiredErr := h.reconcileNoChecksWithRequiredStatusChecks(ctx, *repo); requiredErr == nil {
					if noRequired {
						if noRequiredSummary != "" {
							checkSummary = noRequiredSummary
						}
						h.logf(
							"stage=checks status=ok reason=no_required_status_checks repo=%s repo_dir=%s pr_url=%s attempt=%d",
							repo.URL,
							repo.RelDir,
							repo.PRURL,
							attempt+1,
						)
						return ExitSuccess, "", nil
					}
				} else {
					h.logf(
						"stage=checks status=warn action=required_status_checks reason=query_failed repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.PRURL,
						attempt+1,
						requiredErr,
					)
				}
				if reconciled, workflowSummary, reconcileErr := h.reconcileNoChecksWithWorkflowDispatch(ctx, *repo); reconcileErr == nil {
					if workflowSummary != "" {
						checkSummary = workflowSummary
					}
					if reconciled {
						h.logf(
							"stage=checks status=ok reason=workflow_dispatch_snapshot repo=%s repo_dir=%s pr_url=%s attempt=%d",
							repo.URL,
							repo.RelDir,
							repo.PRURL,
							attempt+1,
						)
						return ExitSuccess, "", nil
					}
				} else {
					h.logf(
						"stage=checks status=warn action=workflow_dispatch_snapshot reason=query_failed repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.PRURL,
						attempt+1,
						reconcileErr,
					)
				}
			}
			if noReportRetry >= maxPRChecksNoReportRetries || !noChecksReported {
				break
			}

			h.logf(
				"stage=checks status=waiting reason=no_checks_reported repo=%s repo_dir=%s pr_url=%s attempt=%d retry=%d/%d",
				repo.URL,
				repo.RelDir,
				repo.PRURL,
				attempt+1,
				noReportRetry+1,
				maxPRChecksNoReportRetries,
			)
			if err := h.Sleep(ctx, prChecksNoReportRetryDelay); err != nil {
				return ExitPR, "checks", err
			}
		}

		h.logf("stage=checks status=failed repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
		if attempt >= maxPRCheckRemediationAttempts {
			return ExitPR, "checks", fmt.Errorf(
				"required PR checks failed for repo %s after %d remediation attempt(s): %s",
				repo.URL,
				maxPRCheckRemediationAttempts,
				checkSummary,
			)
		}

		repairPrompt := remediationPromptForRepo(
			codexBasePrompt,
			repo.RelDir,
			repo.URL,
			repo.PRURL,
			checkSummary,
			attempt+1,
			multiRepo,
		)
		h.logf(
			"stage=%s status=start target=%s mode=remediation attempt=%d repo=%s repo_dir=%s",
			agentStage,
			codexTargetLabel,
			attempt+1,
			repo.URL,
			repo.RelDir,
		)
		codexStart := time.Now()
		if err := h.runCodex(ctx, runtime, codexDir, repairPrompt, codexOpts, agentsPath, cfg.ResponseMode); err != nil {
			return ExitCodex, agentStage, err
		}
		h.logf(
			"stage=%s status=ok elapsed_s=%d mode=remediation attempt=%d repo=%s repo_dir=%s",
			agentStage,
			int(time.Since(codexStart).Seconds()),
			attempt+1,
			repo.URL,
			repo.RelDir,
		)

		statusRes, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
		if err != nil {
			return ExitGit, "git", err
		}
		if strings.TrimSpace(statusRes.Stdout) == "" {
			return ExitPR, "checks", fmt.Errorf("required PR checks failed and agent produced no remediation changes for repo %s", repo.URL)
		}

		h.logf("stage=git status=start action=repair_commit attempt=%d repo=%s repo_dir=%s", attempt+1, repo.URL, repo.RelDir)
		if _, err := h.runCommand(ctx, "git", addCommand(repo.Dir)); err != nil {
			return ExitGit, "git", err
		}
		commitRes, commitErr := h.runCommand(ctx, "git", commitCommand(repo.Dir, remediationCommitMessage(cfg.CommitMessage, attempt+1)))
		if commitErr != nil {
			noChanges, statusErr := h.refreshRepoChangeStateAfterNoOpCommit(ctx, repo, commitRes, commitErr)
			if statusErr != nil {
				return ExitGit, "git", statusErr
			}
			if noChanges {
				h.logf(
					"stage=git status=ok action=repair_commit attempt=%d repo=%s repo_dir=%s reason=no_changes_after_add",
					attempt+1,
					repo.URL,
					repo.RelDir,
				)
				continue
			}
			if !isNothingToCommitResult(commitRes, commitErr) {
				return ExitGit, "git", commitErr
			}
			h.logf(
				"stage=git status=ok action=repair_commit attempt=%d repo=%s repo_dir=%s reason=already_committed",
				attempt+1,
				repo.URL,
				repo.RelDir,
			)
		}
		if err := h.pushWithSync(ctx, *repo, attempt+1); err != nil {
			return ExitGit, "git", err
		}
		h.logf("stage=git status=ok action=repair_commit attempt=%d repo=%s repo_dir=%s", attempt+1, repo.URL, repo.RelDir)
	}
}

func (h Harness) pushWithSync(ctx context.Context, repo repoWorkspace, remediationAttempt int) error {
	pushRemote := repoPushRemote(repo)
	for pushAttempt := 1; pushAttempt <= maxPushSyncAttempts; pushAttempt++ {
		res, err := h.runCommand(ctx, "git", pushToRemoteCommand(repo.Dir, pushRemote, repo.Branch))
		if err == nil {
			return nil
		}
		if !isNonFastForwardPush(res, err) || pushAttempt >= maxPushSyncAttempts {
			return err
		}
		if remediationAttempt > 0 {
			h.logf(
				"stage=git status=retry action=push_sync reason=non_fast_forward repo=%s repo_dir=%s branch=%s remediation_attempt=%d retry=%d/%d",
				repo.URL,
				repo.RelDir,
				repo.Branch,
				remediationAttempt,
				pushAttempt,
				maxPushSyncAttempts-1,
			)
		} else {
			h.logf(
				"stage=git status=retry action=push_sync reason=non_fast_forward repo=%s repo_dir=%s branch=%s retry=%d/%d",
				repo.URL,
				repo.RelDir,
				repo.Branch,
				pushAttempt,
				maxPushSyncAttempts-1,
			)
		}
		if _, syncErr := h.runCommand(ctx, "git", fetchBranchFromRemoteCommand(repo.Dir, pushRemote, repo.Branch)); syncErr != nil {
			return fmt.Errorf("sync branch %q on remote %q before push retry: %w", repo.Branch, pushRemote, syncErr)
		}
		if _, syncErr := h.runCommand(ctx, "git", mergeFetchedBranchCommand(repo.Dir)); syncErr != nil {
			return fmt.Errorf("sync branch %q on remote %q before push retry: %w", repo.Branch, pushRemote, syncErr)
		}
	}
	return fmt.Errorf("push retries exhausted for branch %q on remote %q", repo.Branch, pushRemote)
}

func (h Harness) verifyRemoteWriteAccess(ctx context.Context, repo repoWorkspace) error {
	return h.verifyRemoteWriteAccessOnRemote(ctx, repo, publishRemoteOrigin, "git")
}

func (h Harness) verifyRemoteWriteAccessOnRemote(ctx context.Context, repo repoWorkspace, remote, stage string) error {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = publishRemoteOrigin
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "git"
	}
	h.logf(
		"stage=%s status=start action=probe_write_access repo=%s repo_dir=%s branch=%s remote=%s",
		stage,
		repo.URL,
		repo.RelDir,
		repo.Branch,
		remote,
	)
	res, err := h.runCommand(ctx, "git", pushDryRunToRemoteCommand(repo.Dir, remote, repo.Branch))
	if err != nil {
		if isNonFastForwardPush(res, err) {
			h.logf(
				"stage=%s status=ok action=probe_write_access reason=non_fast_forward repo=%s repo_dir=%s branch=%s remote=%s",
				stage,
				repo.URL,
				repo.RelDir,
				repo.Branch,
				remote,
			)
			return nil
		}
		return commandErrorWithDetails(
			fmt.Sprintf("verify remote write access for repo %s branch %q on remote %q", repo.URL, repo.Branch, remote),
			err,
			res,
			maxGitErrorDetailChars,
		)
	}
	h.logf(
		"stage=%s status=ok action=probe_write_access repo=%s repo_dir=%s branch=%s remote=%s",
		stage,
		repo.URL,
		repo.RelDir,
		repo.Branch,
		remote,
	)
	return nil
}

func (h Harness) preparePublishWorkflow(ctx context.Context, repo *repoWorkspace) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}

	branch := normalizeBranchRef(repo.Branch)
	if branch == "" {
		return fmt.Errorf("prepare publish workflow for repo %s: branch is required", repo.URL)
	}

	repo.PushRemote = publishRemoteOrigin
	repo.PublishStrategy = publishStrategyDirect
	repo.PRHeadRef = branch
	repo.PRHeadOwner = ""
	repo.PRTargetRepo = ""

	h.logf(
		"stage=workflow status=start action=prepare_publish repo=%s repo_dir=%s branch=%s",
		repo.URL,
		repo.RelDir,
		repo.Branch,
	)

	if err := h.verifyRemoteWriteAccessOnRemote(ctx, *repo, publishRemoteOrigin, "workflow"); err == nil {
		repo.WriteAccessChecked = true
		repo.WriteAccessAllowed = true
		repo.WriteAccessErr = nil
		h.logf(
			"stage=workflow status=ok action=prepare_publish repo=%s repo_dir=%s branch=%s strategy=%s remote=%s",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			repo.PublishStrategy,
			repo.PushRemote,
		)
		return nil
	} else {
		repo.WriteAccessChecked = true
		repo.WriteAccessAllowed = false
		repo.WriteAccessErr = err
		if failurefollowup.NonRemediableRepoAccessReason(err) == "" {
			return err
		}
		h.logf(
			"stage=workflow status=warn action=prepare_publish reason=direct_write_unavailable repo=%s repo_dir=%s branch=%s err=%q",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			err,
		)
		if fallbackErr := h.prepareForkFallbackPublishWorkflow(ctx, repo); fallbackErr != nil {
			return fmt.Errorf(
				"prepare fork fallback for repo %s after direct write denial: %w",
				repo.URL,
				errors.Join(err, fallbackErr),
			)
		}
		repo.WriteAccessChecked = true
		repo.WriteAccessAllowed = true
		repo.WriteAccessErr = nil
		h.logf(
			"stage=workflow status=ok action=prepare_publish repo=%s repo_dir=%s branch=%s strategy=%s remote=%s head=%s target_repo=%s",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			repo.PublishStrategy,
			repo.PushRemote,
			repo.PRHeadRef,
			repo.PRTargetRepo,
		)
		return nil
	}
}

type ghViewerProfile struct {
	Login string `json:"login"`
}

type ghRepoVisibility struct {
	IsPrivate     bool   `json:"isPrivate"`
	NameWithOwner string `json:"nameWithOwner"`
}

func (h Harness) prepareForkFallbackPublishWorkflow(ctx context.Context, repo *repoWorkspace) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}
	ref, ok := parseGitHubRepoRef(repo.URL)
	if !ok {
		return fmt.Errorf("cannot prepare fork fallback for non-GitHub repo %s", repo.URL)
	}
	upstreamRepo := fmt.Sprintf("%s/%s", ref.owner, ref.name)

	repoViewRes, repoViewErr := h.runCommand(ctx, "workflow", ghRepoViewVisibilityCommand(repo.Dir, upstreamRepo))
	if repoViewErr != nil {
		return commandErrorWithDetails(
			fmt.Sprintf("inspect upstream repository visibility for %s", upstreamRepo),
			repoViewErr,
			repoViewRes,
			maxGitErrorDetailChars,
		)
	}
	repoView, parseViewErr := parseGitHubRepoVisibility(repoViewRes.Stdout)
	if parseViewErr != nil {
		return fmt.Errorf("decode github repository visibility for %s: %w", upstreamRepo, parseViewErr)
	}
	resolvedUpstreamRepo := strings.TrimSpace(repoView.NameWithOwner)
	if resolvedUpstreamRepo == "" {
		resolvedUpstreamRepo = upstreamRepo
	}
	if repoView.IsPrivate {
		return fmt.Errorf("repository %s is private; automatic fork fallback supports only public repositories", resolvedUpstreamRepo)
	}

	viewerRes, viewerErr := h.runCommand(ctx, "workflow", ghViewerLoginCommand(repo.Dir))
	if viewerErr != nil {
		return commandErrorWithDetails(
			"resolve authenticated github login",
			viewerErr,
			viewerRes,
			maxGitErrorDetailChars,
		)
	}
	viewerLogin, parseViewerErr := parseGitHubViewerLogin(viewerRes.Stdout)
	if parseViewerErr != nil {
		return fmt.Errorf("decode authenticated github login: %w", parseViewerErr)
	}
	if viewerLogin == "" {
		return fmt.Errorf("decode authenticated github login: missing login")
	}

	forkRes, forkErr := h.runCommand(ctx, "workflow", ghRepoForkCommand(repo.Dir, resolvedUpstreamRepo))
	if forkErr != nil && !isForkAlreadyExistsError(forkRes, forkErr) {
		return commandErrorWithDetails(
			fmt.Sprintf("create or reuse fork for %s", resolvedUpstreamRepo),
			forkErr,
			forkRes,
			maxGitErrorDetailChars,
		)
	}

	forkURL, forkURLOk := ref.withOwner(viewerLogin)
	if !forkURLOk {
		return fmt.Errorf("compute fork remote url for repo %s and viewer %s", repo.URL, viewerLogin)
	}
	if err := h.ensureForkRemoteConfigured(ctx, *repo, forkURL); err != nil {
		return err
	}
	if err := h.verifyRemoteWriteAccessOnRemote(ctx, *repo, publishRemoteFork, "workflow"); err != nil {
		if !hasGitHubAuthToken() || !isGitHubSSHRemoteURL(forkURL) {
			return err
		}
		httpsForkURL, httpsForkURLOk := ref.withHTTPSOwner(viewerLogin)
		if !httpsForkURLOk {
			return err
		}
		if setErr := h.ensureForkRemoteConfigured(ctx, *repo, httpsForkURL); setErr != nil {
			return setErr
		}
		if verifyErr := h.verifyRemoteWriteAccessOnRemote(ctx, *repo, publishRemoteFork, "workflow"); verifyErr != nil {
			return verifyErr
		}
	}

	repo.PushRemote = publishRemoteFork
	repo.PublishStrategy = publishStrategyForkFallback
	repo.PRHeadOwner = viewerLogin
	repo.PRHeadRef = fmt.Sprintf("%s:%s", viewerLogin, normalizeBranchRef(repo.Branch))
	repo.PRTargetRepo = resolvedUpstreamRepo
	return nil
}

func (h Harness) ensureForkRemoteConfigured(ctx context.Context, repo repoWorkspace, forkURL string) error {
	setRes, setErr := h.runCommand(ctx, "workflow", gitRemoteSetURLCommand(repo.Dir, publishRemoteFork, forkURL))
	if setErr == nil {
		return nil
	}
	if !isNoSuchRemoteError(setRes, setErr) {
		return commandErrorWithDetails(
			fmt.Sprintf("set remote %q url for repo %s", publishRemoteFork, repo.URL),
			setErr,
			setRes,
			maxGitErrorDetailChars,
		)
	}
	addRes, addErr := h.runCommand(ctx, "workflow", gitRemoteAddCommand(repo.Dir, publishRemoteFork, forkURL))
	if addErr != nil {
		return commandErrorWithDetails(
			fmt.Sprintf("add remote %q url for repo %s", publishRemoteFork, repo.URL),
			addErr,
			addRes,
			maxGitErrorDetailChars,
		)
	}
	return nil
}

func parseGitHubViewerLogin(raw string) (string, error) {
	var profile ghViewerProfile
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &profile); err != nil {
		return "", err
	}
	return strings.TrimSpace(profile.Login), nil
}

func parseGitHubRepoVisibility(raw string) (ghRepoVisibility, error) {
	var repo ghRepoVisibility
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &repo); err != nil {
		return ghRepoVisibility{}, err
	}
	return repo, nil
}

func isForkAlreadyExistsError(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	markers := []string{
		"already exists",
		"already have a fork",
		"already has a fork",
		"already forking",
		"already forked",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isNoSuchRemoteError(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "no such remote")
}

func repoPushRemote(repo repoWorkspace) string {
	if remote := strings.TrimSpace(repo.PushRemote); remote != "" {
		return remote
	}
	return publishRemoteOrigin
}

func repoPRHeadRef(repo repoWorkspace) string {
	if owner := strings.TrimSpace(repo.PRHeadOwner); owner != "" {
		branch := normalizeBranchRef(repo.Branch)
		if branch != "" {
			return fmt.Sprintf("%s:%s", owner, branch)
		}
	}
	if head := strings.TrimSpace(repo.PRHeadRef); head != "" {
		if idx := strings.Index(head, ":"); idx > 0 {
			owner := strings.TrimSpace(head[:idx])
			branch := normalizeBranchRef(repo.Branch)
			if owner != "" && branch != "" {
				return fmt.Sprintf("%s:%s", owner, branch)
			}
		}
		return head
	}
	return normalizeBranchRef(repo.Branch)
}

func repoPRTargetRepo(repo repoWorkspace) string {
	return strings.TrimSpace(repo.PRTargetRepo)
}

func (h Harness) populateNoChangePRURLs(ctx context.Context, repos []repoWorkspace) {
	for i := range repos {
		prURL, err := h.lookupOpenPRURLByHead(ctx, repos[i])
		if err != nil {
			h.logf(
				"stage=pr status=warn action=lookup_existing reason=failed repo=%s repo_dir=%s branch=%s err=%q",
				repos[i].URL,
				repos[i].RelDir,
				repos[i].Branch,
				err,
			)
			continue
		}
		if prURL == "" {
			prURL, err = h.lookupAnyPRURLByHead(ctx, repos[i])
			if err != nil {
				h.logf(
					"stage=pr status=warn action=lookup_existing reason=fallback_failed repo=%s repo_dir=%s branch=%s err=%q",
					repos[i].URL,
					repos[i].RelDir,
					repos[i].Branch,
					err,
				)
				continue
			}
		}
		if prURL == "" {
			continue
		}
		repos[i].PRURL = prURL
		h.logf(
			"stage=pr status=ok action=lookup_existing repo=%s repo_dir=%s branch=%s pr_url=%s",
			repos[i].URL,
			repos[i].RelDir,
			repos[i].Branch,
			repos[i].PRURL,
		)
	}
}

func (h Harness) lookupOpenPRURLByHead(ctx context.Context, repo repoWorkspace) (string, error) {
	headRef := repoPRHeadRef(repo)
	if headRef == "" {
		return "", nil
	}
	branch := normalizeBranchRef(repo.Branch)
	if branch == "" {
		return "", nil
	}

	pushRemote := repoPushRemote(repo)
	remoteRes, remoteErr := h.runCommand(ctx, "git", remoteBranchExistsOnRemoteCommand(repo.Dir, pushRemote, branch))
	if remoteErr != nil {
		return "", fmt.Errorf("verify remote branch %q for repo %s on remote %q: %w", branch, repo.URL, pushRemote, remoteErr)
	}
	if !hasRemoteBranch(remoteRes) {
		return "", nil
	}

	lookupRes, err := h.runCommand(ctx, "pr", prLookupByHeadWithRepoCommand(repo.Dir, headRef, repoPRTargetRepo(repo)))
	if err != nil {
		return "", err
	}
	if prURL := parsePRURLFromLookupOutput(lookupRes.Stdout); prURL != "" {
		return prURL, nil
	}
	return parsePRURLFromLookupOutput(lookupRes.Stderr), nil
}

func (h Harness) lookupAnyPRURLByHead(ctx context.Context, repo repoWorkspace) (string, error) {
	headRef := repoPRHeadRef(repo)
	if headRef == "" {
		return "", nil
	}

	lookupRes, err := h.runCommand(ctx, "pr", prLookupAnyByHeadWithRepoCommand(repo.Dir, headRef, repoPRTargetRepo(repo)))
	if err != nil {
		return "", err
	}
	if prURL := parsePRURLFromLookupOutput(lookupRes.Stdout); prURL != "" {
		return prURL, nil
	}
	return parsePRURLFromLookupOutput(lookupRes.Stderr), nil
}

func (h Harness) runCloneWithRetry(
	ctx context.Context,
	repoURL, branch, repoDir, relDir string,
	cmd execx.Command,
) (execx.Result, error) {
	for attempt := 1; ; attempt++ {
		res, err := h.runCommand(ctx, "clone", cmd)
		if err == nil {
			return res, nil
		}
		if !shouldRetryClone(err, res) || attempt >= maxCloneAttempts {
			return res, cloneErrorWithDetails(err, res)
		}

		h.logf(
			"stage=clone status=retry reason=transient_error repo=%s branch=%s repo_dir=%s retry=%d/%d err=%q",
			repoURL,
			cloneRetryBranchLabel(branch),
			relDir,
			attempt,
			maxCloneAttempts-1,
			err,
		)
		if cleanupErr := os.RemoveAll(repoDir); cleanupErr != nil {
			return res, fmt.Errorf("cleanup failed clone dir %s before retry: %w", repoDir, cleanupErr)
		}
		if sleepErr := h.Sleep(ctx, cloneRetryDelay); sleepErr != nil {
			return res, fmt.Errorf("clone retry interrupted: %w", sleepErr)
		}
	}
}

func (h Harness) cloneRepositories(ctx context.Context, repos []repoWorkspace, baseBranch string) error {
	baseBranch = strings.TrimSpace(baseBranch)
	if len(repos) == 0 {
		return nil
	}
	repoURLs := make([]string, 0, len(repos))
	for _, repo := range repos {
		repoURLs = append(repoURLs, repo.URL)
	}
	repoOwnerHints := repoOwnerFallbackCandidates(repoURLs)

	cloneErrors := make([]error, len(repos))

	cloneOne := func(index int) {
		err := h.cloneRepository(ctx, &repos[index], baseBranch, repoOwnerHints)
		cloneErrors[index] = err
	}

	if len(repos) == 1 {
		cloneOne(0)
	} else {
		var wg sync.WaitGroup
		wg.Add(len(repos))
		for i := range repos {
			i := i
			go func() {
				defer wg.Done()
				cloneOne(i)
			}()
		}
		wg.Wait()
	}

	for _, err := range cloneErrors {
		if err != nil {
			return err
		}
	}

	for _, repo := range repos {
		if repo.CreateWorkBranch {
			continue
		}
		h.logf("stage=clone status=start action=fetch_base branch=%s repo=%s repo_dir=%s", repo.BaseBranch, repo.URL, repo.RelDir)
		if _, err := h.runCommand(ctx, "clone", fetchBaseBranchCommand(repo.Dir, repo.BaseBranch)); err != nil {
			return err
		}
		h.logf("stage=clone status=ok action=fetch_base branch=%s repo=%s repo_dir=%s", repo.BaseBranch, repo.URL, repo.RelDir)
	}

	return nil
}

func (h Harness) cloneRepository(ctx context.Context, repo *repoWorkspace, branch string, repoOwnerHints []string) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}

	branch = strings.TrimSpace(branch)

	repoURL := repo.URL
	requestedRepoURL := repoURL
	h.logf("stage=clone status=start repo=%s branch=%s repo_dir=%s", repoURL, cloneRetryBranchLabel(branch), repo.RelDir)

	if branch == "" {
		cloneRes, cloneErr := h.runCloneWithRetry(
			ctx,
			repoURL,
			branch,
			repo.Dir,
			repo.RelDir,
			cloneRepoDefaultBranchCommand(repoURL, repo.Dir),
		)
		if cloneErr != nil && isRepoNotFoundCloneError(cloneErr, cloneRes) {
			if fallbackRepoURL, ok := repoOwnerFallbackURL(repoURL, repoOwnerHints); ok {
				h.logf(
					"stage=clone status=warn action=fallback_repo_owner reason=repository_not_found repo=%s fallback_repo=%s branch=%s repo_dir=%s",
					repoURL,
					fallbackRepoURL,
					cloneRetryBranchLabel(branch),
					repo.RelDir,
				)
				if err := os.RemoveAll(repo.Dir); err != nil {
					return fmt.Errorf("cleanup failed clone dir %s: %w", repo.Dir, err)
				}
				fallbackRes, fallbackErr := h.runCloneWithRetry(
					ctx,
					fallbackRepoURL,
					branch,
					repo.Dir,
					repo.RelDir,
					cloneRepoDefaultBranchCommand(fallbackRepoURL, repo.Dir),
				)
				if fallbackErr == nil {
					repo.URL = fallbackRepoURL
					repoURL = fallbackRepoURL
					cloneErr = nil
					h.logf(
						"stage=clone status=ok action=fallback_repo_owner repo=%s fallback_repo=%s branch=%s repo_dir=%s",
						requestedRepoURL,
						fallbackRepoURL,
						cloneRetryBranchLabel(branch),
						repo.RelDir,
					)
				} else {
					repo.URL = fallbackRepoURL
					repoURL = fallbackRepoURL
					cloneRes = fallbackRes
					cloneErr = fallbackErr
					h.logf(
						"stage=clone status=warn action=fallback_repo_owner reason=fallback_failed repo=%s fallback_repo=%s branch=%s repo_dir=%s err=%q",
						requestedRepoURL,
						fallbackRepoURL,
						cloneRetryBranchLabel(branch),
						repo.RelDir,
						fallbackErr,
					)
				}
			}
		}
		if cloneErr != nil {
			return cloneErr
		}
		resolvedBranch, err := h.resolveClonedDefaultBranch(ctx, *repo, repoURL)
		if err != nil {
			return err
		}
		repo.BaseBranch = resolvedBranch
		repo.CreateWorkBranch = true
		h.logf("stage=clone status=ok repo=%s repo_dir=%s resolved_branch=%s", repoURL, repo.RelDir, resolvedBranch)
		return nil
	}

	cloneRes, cloneErr := h.runCloneWithRetry(
		ctx,
		repoURL,
		branch,
		repo.Dir,
		repo.RelDir,
		cloneRepoCommand(repoURL, branch, repo.Dir),
	)
	if cloneErr != nil && isRepoNotFoundCloneError(cloneErr, cloneRes) {
		if fallbackRepoURL, ok := repoOwnerFallbackURL(repoURL, repoOwnerHints); ok {
			h.logf(
				"stage=clone status=warn action=fallback_repo_owner reason=repository_not_found repo=%s fallback_repo=%s branch=%s repo_dir=%s",
				repoURL,
				fallbackRepoURL,
				branch,
				repo.RelDir,
			)
			if err := os.RemoveAll(repo.Dir); err != nil {
				return fmt.Errorf("cleanup failed clone dir %s: %w", repo.Dir, err)
			}
			fallbackRes, fallbackErr := h.runCloneWithRetry(
				ctx,
				fallbackRepoURL,
				branch,
				repo.Dir,
				repo.RelDir,
				cloneRepoCommand(fallbackRepoURL, branch, repo.Dir),
			)
			if fallbackErr == nil {
				repo.URL = fallbackRepoURL
				repoURL = fallbackRepoURL
				cloneRes = fallbackRes
				cloneErr = nil
				h.logf(
					"stage=clone status=ok action=fallback_repo_owner repo=%s fallback_repo=%s branch=%s repo_dir=%s",
					requestedRepoURL,
					fallbackRepoURL,
					branch,
					repo.RelDir,
				)
			} else {
				repo.URL = fallbackRepoURL
				repoURL = fallbackRepoURL
				cloneRes = fallbackRes
				cloneErr = fallbackErr
				h.logf(
					"stage=clone status=warn action=fallback_repo_owner reason=fallback_failed repo=%s fallback_repo=%s branch=%s repo_dir=%s err=%q",
					requestedRepoURL,
					fallbackRepoURL,
					branch,
					repo.RelDir,
					fallbackErr,
				)
			}
		}
	}
	if cloneErr == nil {
		repo.BaseBranch = normalizeBranchRef(branch)
		repo.CreateWorkBranch = shouldCreateWorkBranch(branch)
		h.logf("stage=clone status=ok repo=%s repo_dir=%s", repoURL, repo.RelDir)
		return nil
	}
	if shouldBootstrapUninitializedMainBranch(branch, cloneRes, cloneErr) {
		hasRefs, refsErr := h.remoteRepositoryHasRefs(ctx, repoURL)
		if refsErr != nil {
			return refsErr
		}
		if !hasRefs {
			if err := h.bootstrapUninitializedMainBranch(ctx, *repo, repoURL); err != nil {
				return err
			}
			repo.BaseBranch = "main"
			repo.CreateWorkBranch = true
			return nil
		}
	}
	if !shouldFallbackCloneToDefaultBranch(branch, cloneRes, cloneErr) {
		return cloneErr
	}

	h.logf(
		"stage=clone status=warn action=fallback_default_branch reason=missing_remote_branch repo=%s branch=%s repo_dir=%s",
		repoURL,
		branch,
		repo.RelDir,
	)
	if err := os.RemoveAll(repo.Dir); err != nil {
		return fmt.Errorf("cleanup failed clone dir %s: %w", repo.Dir, err)
	}
	if _, err := h.runCloneWithRetry(
		ctx,
		repoURL,
		"",
		repo.Dir,
		repo.RelDir,
		cloneRepoDefaultBranchCommand(repoURL, repo.Dir),
	); err != nil {
		return err
	}
	resolvedBranch, err := h.resolveClonedDefaultBranch(ctx, *repo, repoURL)
	if err != nil {
		return err
	}
	repo.BaseBranch = resolvedBranch
	repo.CreateWorkBranch = true

	h.logf(
		"stage=clone status=ok action=fallback_default_branch repo=%s repo_dir=%s resolved_branch=%s",
		repoURL,
		repo.RelDir,
		resolvedBranch,
	)
	return nil
}

func (h Harness) resolveClonedDefaultBranch(ctx context.Context, repo repoWorkspace, repoURL string) (string, error) {
	res, err := h.runCommand(ctx, "clone", currentBranchCommand(repo.Dir))
	if err != nil {
		return "", commandErrorWithDetails(
			fmt.Sprintf("resolve cloned default branch for repo %s", repoURL),
			err,
			res,
			maxCloneErrorDetailChars,
		)
	}
	branch := normalizeBranchRef(res.Stdout)
	headRes, headErr := h.runCommand(ctx, "clone", headCommitSHACommand(repo.Dir))
	if headErr == nil && branch != "" {
		return branch, nil
	}
	hasRefs, refsErr := h.remoteRepositoryHasRefs(ctx, repoURL)
	if refsErr != nil {
		return "", refsErr
	}
	if !hasRefs {
		if err := h.bootstrapUninitializedMainBranch(ctx, repo, repoURL); err != nil {
			return "", err
		}
		return "main", nil
	}
	if headErr != nil {
		return "", commandErrorWithDetails(
			fmt.Sprintf("verify cloned default branch HEAD for repo %s", repoURL),
			headErr,
			headRes,
			maxCloneErrorDetailChars,
		)
	}
	return "", fmt.Errorf("resolve cloned default branch for repo %s: git branch --show-current returned empty branch", repoURL)
}

func (h Harness) remoteRepositoryHasRefs(ctx context.Context, repoURL string) (bool, error) {
	res, err := h.runCommand(ctx, "clone", remoteRefsCommand(repoURL))
	if err != nil {
		return false, commandErrorWithDetails(
			fmt.Sprintf("inspect remote refs for repo %s", repoURL),
			err,
			res,
			maxCloneErrorDetailChars,
		)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

func (h Harness) bootstrapUninitializedMainBranch(ctx context.Context, repo repoWorkspace, repoURL string) error {
	h.logf(
		"stage=clone status=warn action=bootstrap_main reason=uninitialized_remote repo=%s repo_dir=%s",
		repoURL,
		repo.RelDir,
	)
	if err := os.RemoveAll(repo.Dir); err != nil {
		return fmt.Errorf("cleanup failed clone dir %s: %w", repo.Dir, err)
	}
	if _, err := h.runCloneWithRetry(
		ctx,
		repoURL,
		"",
		repo.Dir,
		repo.RelDir,
		cloneRepoDefaultBranchCommand(repoURL, repo.Dir),
	); err != nil {
		return err
	}
	if _, err := h.runCommand(ctx, "clone", switchMainBranchCommand(repo.Dir)); err != nil {
		return err
	}
	if _, err := h.runCommand(ctx, "clone", initializeMainBranchCommitCommand(repo.Dir)); err != nil {
		return err
	}
	if _, err := h.runCommand(ctx, "clone", pushCommand(repo.Dir, "main")); err != nil {
		return err
	}
	h.logf(
		"stage=clone status=ok action=bootstrap_main repo=%s repo_dir=%s resolved_branch=main",
		repoURL,
		repo.RelDir,
	)
	return nil
}

func shouldBootstrapUninitializedMainBranch(baseBranch string, res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	if normalizeBranchRef(baseBranch) != "main" {
		return false
	}
	return isMissingRemoteBranchCloneError(err, res)
}

func cloneRetryBranchLabel(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "default"
	}
	return branch
}

func cloneErrorWithDetails(err error, res execx.Result) error {
	if err == nil {
		return nil
	}
	detail := summarizeCommandErrorDetail(res, maxCloneErrorDetailChars)
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func summarizeCommandErrorDetail(res execx.Result, maxChars int) string {
	detail := strings.TrimSpace(strings.Join([]string{res.Stderr, res.Stdout}, "\n"))
	if detail == "" {
		return ""
	}
	detail = strings.ReplaceAll(detail, "\r\n", "\n")
	detail = strings.ReplaceAll(detail, "\r", "\n")
	detail = strings.Join(strings.Fields(detail), " ")
	if maxChars <= 0 || len(detail) <= maxChars {
		return detail
	}
	return strings.TrimSpace(detail[:maxChars]) + "...(truncated)"
}

func commandErrorWithDetails(prefix string, err error, res execx.Result, maxChars int) error {
	if err == nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "command failed"
	}
	detail := summarizeCommandErrorDetail(res, maxChars)
	if detail == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, detail)
}

func (h Harness) refreshRepoChangeStateAfterNoOpCommit(
	ctx context.Context,
	repo *repoWorkspace,
	commitRes execx.Result,
	commitErr error,
) (bool, error) {
	if repo == nil || !isNothingToCommitResult(commitRes, commitErr) {
		return false, nil
	}

	statusRes, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
	if err != nil {
		return false, err
	}
	repo.Branch = pickFirstNonEmpty(localBranchFromStatus(statusRes.Stdout), repo.Branch)
	syncRepoPublishHeadRef(repo)
	changed, detectErr := h.repoHasPendingChanges(ctx, *repo, statusRes.Stdout)
	if detectErr != nil {
		return false, detectErr
	}
	repo.Changed = changed
	return !repo.Changed, nil
}

func syncRepoPublishHeadRef(repo *repoWorkspace) {
	if repo == nil {
		return
	}
	branch := normalizeBranchRef(repo.Branch)
	if branch == "" {
		return
	}
	if owner := strings.TrimSpace(repo.PRHeadOwner); owner != "" {
		repo.PRHeadRef = fmt.Sprintf("%s:%s", owner, branch)
		return
	}
	if strings.EqualFold(strings.TrimSpace(repo.PublishStrategy), publishStrategyForkFallback) {
		head := strings.TrimSpace(repo.PRHeadRef)
		if idx := strings.Index(head, ":"); idx > 0 {
			owner := strings.TrimSpace(head[:idx])
			if owner != "" {
				repo.PRHeadOwner = owner
				repo.PRHeadRef = fmt.Sprintf("%s:%s", owner, branch)
				return
			}
		}
	}
	repo.PRHeadRef = branch
}

func (h Harness) repoHasPendingChanges(
	ctx context.Context,
	repo repoWorkspace,
	statusStdout string,
) (bool, error) {
	if hasTrackedWorktreeChanges(statusStdout) || hasAheadCommitsInStatus(statusStdout) {
		return true, nil
	}
	screenshotFiles, err := changedPRCommentScreenshotFiles(repo.Dir, repo.PRCommentScreenshotBaseline)
	if err != nil {
		return false, err
	}
	if len(screenshotFiles) > 0 {
		return true, nil
	}
	// `git status --porcelain --branch` should include a branch header.
	// If it does not, keep legacy behavior and treat this as no changes.
	if strings.TrimSpace(localBranchFromStatus(statusStdout)) == "" {
		return false, nil
	}
	if !repo.CreateWorkBranch {
		return false, nil
	}
	commitsAhead, err := h.countCommitsAheadOfBase(ctx, repo, normalizeBranchRef(repo.BaseBranch))
	if err != nil {
		return false, err
	}
	return commitsAhead > 0, nil
}

func (h Harness) countCommitsAheadOfBase(ctx context.Context, repo repoWorkspace, baseBranch string) (int, error) {
	baseBranch = normalizeBranchRef(baseBranch)
	if baseBranch == "" {
		return 0, fmt.Errorf("base branch is required to count commits ahead for repo %s", repo.URL)
	}
	res, err := h.runCommand(ctx, "git", commitsAheadOfBaseCommand(repo.Dir, baseBranch))
	if err != nil {
		return 0, commandErrorWithDetails(
			fmt.Sprintf("count commits ahead of base branch %q for repo %s", baseBranch, repo.URL),
			err,
			res,
			maxGitErrorDetailChars,
		)
	}
	countText := strings.TrimSpace(res.Stdout)
	if countText == "" {
		return 0, nil
	}
	count, parseErr := strconv.Atoi(countText)
	if parseErr != nil {
		return 0, fmt.Errorf(
			"parse commits-ahead count for repo %s branch %q: %w",
			repo.URL,
			repo.Branch,
			parseErr,
		)
	}
	return count, nil
}

func isNothingToCommitResult(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		err.Error(),
		res.Stdout,
		res.Stderr,
	}, "\n")))
	return strings.Contains(text, "nothing to commit") ||
		strings.Contains(text, "working tree clean") ||
		strings.Contains(text, "nothing added to commit")
}

func buildResult(runDir string, repos []repoWorkspace, noChanges bool) Result {
	res := Result{
		WorkspaceDir: runDir,
		NoChanges:    noChanges,
		RepoResults:  make([]RepoResult, 0, len(repos)),
	}

	for _, repo := range repos {
		res.RepoResults = append(res.RepoResults, RepoResult{
			RepoURL: repo.URL,
			RepoDir: repo.Dir,
			Branch:  repo.Branch,
			PRURL:   repo.PRURL,
			Changed: repo.Changed,
		})

		if repo.Changed && res.Branch == "" {
			res.Branch = repo.Branch
		}
		if repo.PRURL != "" && res.PRURL == "" {
			res.PRURL = repo.PRURL
		}
	}
	if res.Branch == "" && len(repos) > 0 {
		res.Branch = repos[0].Branch
	}
	return res
}

func buildFailureResult(exitCode int, stage string, err error, runDir string, repos []repoWorkspace) Result {
	res := buildResult(runDir, repos, false)
	res.ExitCode = exitCode
	res.Err = fmt.Errorf("%s: %w", stage, err)
	return res
}

func codexTargetLabel(targetSubdir string, multiRepo bool) string {
	if multiRepo {
		return "workspace"
	}
	targetSubdir = strings.TrimSpace(targetSubdir)
	if targetSubdir == "" {
		return "."
	}
	return targetSubdir
}

func workspaceCodexPrompt(prompt, targetSubdir string, repos []repoWorkspace) string {
	base := strings.TrimSpace(prompt)
	if len(repos) <= 1 {
		return base
	}

	var b strings.Builder
	if base != "" {
		b.WriteString(base)
		b.WriteString("\n\n")
	}
	b.WriteString("Workspace context:\n")
	b.WriteString("- Multiple repositories are already cloned before you begin.\n")
	b.WriteString(fmt.Sprintf("- Primary target subdirectory: %s/%s\n", repos[0].RelDir, strings.TrimSpace(targetSubdir)))
	b.WriteString("- Repository map (workspace path => remote):\n")
	for _, repo := range repos {
		b.WriteString(fmt.Sprintf("- %s => %s\n", repo.RelDir, repo.URL))
	}
	b.WriteString("- If you modify files in any repository, keep each changed repository on its own branch and PR.\n")
	b.WriteString("- Only create a new branch when starting from the repository default branch; if you're fixing an existing non-default branch, stay on it.\n")
	b.WriteString("- Start every new branch name with 'moltenhub-'. Do not prefix PR titles with it.\n")
	return strings.TrimSpace(b.String())
}

func (h Harness) prepareReviewPrompt(
	ctx context.Context,
	cfg config.Config,
	repos []repoWorkspace,
	basePrompt string,
) (string, error) {
	if cfg.Review == nil {
		return basePrompt, nil
	}
	if len(repos) != 1 {
		return "", fmt.Errorf("review tasks support exactly one repository")
	}

	repo := repos[0]
	h.logf("stage=review status=start repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	reviewContext, err := h.buildReviewPromptContext(ctx, repo, *cfg.Review)
	if err != nil {
		return "", err
	}
	h.logf("stage=review status=ok repo=%s repo_dir=%s", repo.URL, repo.RelDir)

	if strings.TrimSpace(reviewContext) == "" {
		return basePrompt, nil
	}
	if strings.TrimSpace(basePrompt) == "" {
		return reviewContext, nil
	}
	return strings.TrimSpace(basePrompt + "\n\n" + reviewContext), nil
}

func withAgentsPrompt(prompt, agentsPath string) string {
	base := strings.TrimSpace(prompt)
	agentsPath = strings.TrimSpace(agentsPath)

	location := "./AGENTS.md"
	if agentsPath != "" {
		location = agentsPath
	}

	directive := fmt.Sprintf(
		"you are ./AGENTS.md\nUse %s as your primary implementation instructions before making any changes.\n%s",
		location,
		agentsCredentialGuardInstruction,
	)
	if base == "" {
		return directive
	}
	return directive + "\n\n" + base
}

type reviewPRMetadata struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (h Harness) buildReviewPromptContext(
	ctx context.Context,
	repo repoWorkspace,
	reviewCfg config.ReviewConfig,
) (string, error) {
	selector := reviewSelector(reviewCfg)
	if selector == "" {
		return "", fmt.Errorf("review selector is required")
	}

	metaRes, err := h.runCommand(ctx, "review", prReviewMetadataCommand(repo.Dir, selector))
	if err != nil {
		return "", fmt.Errorf("load pull request metadata: %w", err)
	}

	var metadata reviewPRMetadata
	if err := json.Unmarshal([]byte(strings.TrimSpace(metaRes.Stdout)), &metadata); err != nil {
		return "", fmt.Errorf("decode pull request metadata: %w", err)
	}
	if metadata.Number <= 0 {
		return "", fmt.Errorf("pull request metadata did not include a valid number")
	}
	if strings.TrimSpace(metadata.BaseRefName) == "" {
		return "", fmt.Errorf("pull request metadata did not include a base branch")
	}

	if _, err := h.runCommand(ctx, "review", fetchRemoteBranchCommand(repo.Dir, metadata.BaseRefName)); err != nil {
		return "", fmt.Errorf("fetch pull request base branch %q: %w", metadata.BaseRefName, err)
	}
	if _, err := h.runCommand(ctx, "review", fetchPullRequestHeadCommand(repo.Dir, metadata.Number)); err != nil {
		return "", fmt.Errorf("fetch pull request head for #%d: %w", metadata.Number, err)
	}

	commentsRes, err := h.runCommand(ctx, "review", prReviewCommentsCommand(repo.Dir, selector))
	if err != nil {
		return "", fmt.Errorf("load pull request comments: %w", err)
	}

	baseRef := remoteTrackingRef(metadata.BaseRefName)
	headRef := pullRequestTrackingRef(metadata.Number)

	diffStatRes, err := h.runCommand(ctx, "review", reviewDiffStatCommand(repo.Dir, baseRef, headRef))
	if err != nil {
		return "", fmt.Errorf("summarize pull request diff: %w", err)
	}
	diffPatchRes, err := h.runCommand(ctx, "review", reviewDiffPatchCommand(repo.Dir, baseRef, headRef))
	if err != nil {
		return "", fmt.Errorf("load pull request diff: %w", err)
	}

	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode pull request metadata: %w", err)
	}

	var b strings.Builder
	b.WriteString("Prepared pull-request review context (collected before you started):\n")
	b.WriteString(fmt.Sprintf("- Repository remote: %s\n", repo.URL))
	b.WriteString(fmt.Sprintf("- Pull request: #%d\n", metadata.Number))
	b.WriteString(fmt.Sprintf("- Pull request URL: %s\n", pickFirstNonEmpty(metadata.URL, reviewCfg.PRURL)))
	b.WriteString(fmt.Sprintf("- Base branch: %s\n", metadata.BaseRefName))
	b.WriteString(fmt.Sprintf("- Head branch: %s\n", pickFirstNonEmpty(metadata.HeadRefName, reviewCfg.HeadBranch)))
	b.WriteString("- Existing PR discussion has already been fetched for you below.\n")
	b.WriteString("- The git diff below was generated locally after fetching the PR head and base refs.\n")
	b.WriteString("- Treat this prepared context as a starting point and verify important claims yourself before concluding.\n\n")
	b.WriteString("Pull request metadata:\n```json\n")
	b.WriteString(truncateForPrompt(string(metadataJSON), maxReviewMetadataChars))
	b.WriteString("\n```\n\n")
	b.WriteString("Existing pull request discussion:\n```text\n")
	b.WriteString(truncateForPrompt(nonEmptyOrDefault(commentsRes.Stdout, "No pull-request comments were returned by gh pr view --comments."), maxReviewCommentsChars))
	b.WriteString("\n```\n\n")
	b.WriteString("Local git diff summary:\n```text\n")
	b.WriteString(truncateForPrompt(nonEmptyOrDefault(joinCommandOutput(diffStatRes), "No diff summary output was returned by git diff --stat --summary."), maxReviewDiffStatChars))
	b.WriteString("\n```\n\n")
	b.WriteString("Local git diff patch:\n```diff\n")
	b.WriteString(truncateForPrompt(nonEmptyOrDefault(diffPatchRes.Stdout, "No diff patch output was returned by git diff."), maxReviewDiffPatchChars))
	b.WriteString("\n```")
	return strings.TrimSpace(b.String()), nil
}

func repoWorkspaceDirName(repoURL string, index, total int) string {
	if total <= 1 {
		return "repo"
	}
	return fmt.Sprintf("repo-%02d-%s", index+1, repoDirSlug(repoURL))
}

func reviewSelector(reviewCfg config.ReviewConfig) string {
	if reviewCfg.PRNumber > 0 {
		return fmt.Sprintf("%d", reviewCfg.PRNumber)
	}
	if prURL := strings.TrimSpace(reviewCfg.PRURL); prURL != "" {
		return githubutil.PullRequestSelector(prURL)
	}
	return strings.TrimSpace(reviewCfg.HeadBranch)
}

func prReviewMetadataCommand(repoDir, selector string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{
			"pr", "view", selector,
			"--json", "number,title,body,url,state,isDraft,baseRefName,headRefName,author",
		},
	}
}

func prReviewCommentsCommand(repoDir, selector string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"pr", "view", selector, "--comments"},
	}
}

func fetchRemoteBranchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"fetch", "origin", fmt.Sprintf("%s:refs/remotes/origin/%s", branch, branch)},
	}
}

func fetchPullRequestHeadCommand(repoDir string, prNumber int) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"fetch", "origin", fmt.Sprintf("pull/%d/head:%s", prNumber, pullRequestTrackingRef(prNumber))},
	}
}

func remoteTrackingRef(branch string) string {
	return fmt.Sprintf("refs/remotes/origin/%s", strings.TrimSpace(branch))
}

func pullRequestTrackingRef(prNumber int) string {
	return fmt.Sprintf("refs/remotes/origin/moltenhub-pr-%d", prNumber)
}

func reviewDiffStatCommand(repoDir, baseRef, headRef string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"diff", "--stat", "--summary", fmt.Sprintf("%s...%s", baseRef, headRef)},
	}
}

func reviewDiffPatchCommand(repoDir, baseRef, headRef string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"diff", "--unified=3", "--no-ext-diff", fmt.Sprintf("%s...%s", baseRef, headRef)},
	}
}

func repoDirSlug(repoURL string) string {
	segment := strings.TrimSpace(repoURL)
	if i := strings.LastIndex(segment, "/"); i >= 0 {
		segment = segment[i+1:]
	}
	if i := strings.LastIndex(segment, ":"); i >= 0 {
		segment = segment[i+1:]
	}
	segment = strings.TrimSuffix(segment, ".git")
	segment = strings.ToLower(segment)

	var b strings.Builder
	lastSep := false
	for _, r := range segment {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSep = false
			continue
		}
		if b.Len() > 0 && !lastSep {
			b.WriteByte('-')
			lastSep = true
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "repo"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
		if out == "" {
			return "repo"
		}
	}
	return out
}

func (h Harness) fail(exitCode int, stage string, err error, runDir string) Result {
	h.logf("stage=%s status=error err=%q", stage, err)
	return Result{ExitCode: exitCode, Err: fmt.Errorf("%s: %w", stage, err), WorkspaceDir: runDir}
}

func (h Harness) failWithRepos(exitCode int, stage string, err error, runDir string, repos []repoWorkspace) Result {
	h.logf("stage=%s status=error err=%q", stage, err)
	return buildFailureResult(exitCode, stage, err, runDir, repos)
}

func (h Harness) logf(format string, args ...any) {
	h.Logf(format, args...)
}

func (h Harness) runCommand(ctx context.Context, phase string, cmd execx.Command) (execx.Result, error) {
	onLine := func(stream, line string) {
		if strings.TrimSpace(line) == "" {
			return
		}
		h.logf("cmd phase=%s name=%s stream=%s b64=%s", phase, cmd.Name, stream, encodeLogLine(line))
	}

	if streamRunner, ok := h.Runner.(execx.StreamRunner); ok {
		var stdoutBuf strings.Builder
		var stderrBuf strings.Builder
		streamOnLine := func(stream, line string) {
			onLine(stream, line)
			switch stream {
			case "stdout":
				stdoutBuf.WriteString(line)
				stdoutBuf.WriteByte('\n')
			case "stderr":
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
			}
		}
		res, err := streamRunner.RunStream(ctx, cmd, streamOnLine)
		res.Stdout = mergeStreamedOutput(res.Stdout, stdoutBuf.String())
		res.Stderr = mergeStreamedOutput(res.Stderr, stderrBuf.String())
		return res, err
	}

	res, err := h.Runner.Run(ctx, cmd)
	emitBufferedOutput(res, onLine)
	return res, err
}

func emitBufferedOutput(res execx.Result, onLine execx.StreamLineHandler) {
	if onLine == nil {
		return
	}
	for _, line := range splitOutputLines(res.Stdout) {
		onLine("stdout", line)
	}
	for _, line := range splitOutputLines(res.Stderr) {
		onLine("stderr", line)
	}
}

func splitOutputLines(text string) []string {
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func mergeStreamedOutput(existing, captured string) string {
	existing = strings.TrimSuffix(existing, "\n")
	captured = strings.TrimSuffix(captured, "\n")
	if strings.TrimSpace(captured) == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return captured
	}
	if strings.Contains(existing, captured) {
		return existing
	}
	return existing + "\n" + captured
}

func joinCommandOutput(res execx.Result) string {
	return strings.TrimSpace(strings.Join([]string{res.Stdout, res.Stderr}, "\n"))
}

func truncateForPrompt(text string, max int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if max <= 0 || len(text) <= max {
		return text
	}
	return strings.TrimSpace(text[:max]) + "\n...(truncated)"
}

func nonEmptyOrDefault(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}

func pickFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func encodeLogLine(line string) string {
	return base64.StdEncoding.EncodeToString([]byte(line))
}

func isNoChecksReported(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "no checks reported")
}

func isNoRequiredChecksReported(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "no required checks")
}

func shouldReconcileChecksAfterFailure(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr}, "\n"))
	return strings.Contains(text, "\tpass\t") && strings.Contains(text, "\tfail\t")
}

func existingPRURLFromCreateFailure(res execx.Result, err error) (string, bool) {
	if err == nil {
		return "", false
	}

	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	if !strings.Contains(text, "pull request") || !strings.Contains(text, "already exists") {
		return "", false
	}

	for _, candidate := range []string{res.Stdout, res.Stderr, err.Error()} {
		if url := extractFirstURL(candidate); url != "" {
			return url, true
		}
	}
	return "", false
}

func isNonFastForwardPush(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "non-fast-forward") || strings.Contains(text, "fetch first")
}

func shouldRetryClone(err error, res execx.Result) bool {
	if err == nil {
		return false
	}
	if isRepoNotFoundCloneError(err, res) {
		return false
	}
	return !isMissingRemoteBranchCloneError(err, res)
}

func isMissingRemoteBranchCloneError(err error, res execx.Result) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "could not find remote branch") ||
		(strings.Contains(text, "remote branch") && strings.Contains(text, "not found"))
}

func isRepoNotFoundCloneError(err error, res execx.Result) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "remote: repository not found") ||
		(strings.Contains(text, "fatal: repository") && strings.Contains(text, "not found")) ||
		strings.Contains(text, "repository not found") ||
		strings.Contains(text, "does not appear to be a git repository") ||
		strings.Contains(text, "repository does not exist")
}

var gitHubSCPLikeRepoPattern = regexp.MustCompile(`(?i)^((?:[^@:\s/]+@)?github\.com:)([^/\s]+)/([^/\s]+?)(\.git)?$`)

type gitHubRepoRef struct {
	owner        string
	name         string
	hasGitSuffix bool
	scpPrefix    string
	urlStyle     bool
	urlValue     url.URL
}

func parseGitHubRepoRef(repoURL string) (gitHubRepoRef, bool) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return gitHubRepoRef{}, false
	}

	if matches := gitHubSCPLikeRepoPattern.FindStringSubmatch(repoURL); len(matches) == 5 {
		owner := strings.TrimSpace(matches[2])
		name := strings.TrimSpace(matches[3])
		if owner == "" || name == "" {
			return gitHubRepoRef{}, false
		}
		return gitHubRepoRef{
			owner:        owner,
			name:         name,
			hasGitSuffix: strings.TrimSpace(matches[4]) != "",
			scpPrefix:    matches[1],
		}, true
	}

	parsed, err := url.Parse(repoURL)
	if err != nil {
		return gitHubRepoRef{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Hostname()), "github.com") {
		return gitHubRepoRef{}, false
	}
	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		return gitHubRepoRef{}, false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return gitHubRepoRef{}, false
	}
	owner := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if owner == "" || name == "" {
		return gitHubRepoRef{}, false
	}
	hasGitSuffix := strings.HasSuffix(strings.ToLower(name), ".git")
	name = strings.TrimSuffix(name, ".git")
	if name == "" {
		return gitHubRepoRef{}, false
	}
	return gitHubRepoRef{
		owner:        owner,
		name:         name,
		hasGitSuffix: hasGitSuffix,
		urlStyle:     true,
		urlValue:     *parsed,
	}, true
}

func isGitHubSSHRemoteURL(rawURL string) bool {
	rawURL = strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(rawURL, "git@github.com:") || strings.HasPrefix(rawURL, "ssh://git@github.com/")
}

func (r gitHubRepoRef) withOwner(owner string) (string, bool) {
	owner = strings.TrimSpace(owner)
	if owner == "" || strings.EqualFold(owner, r.owner) {
		return "", false
	}
	if strings.ContainsAny(owner, " \t\r\n") || strings.Contains(owner, "/") {
		return "", false
	}

	repoName := r.name
	if r.hasGitSuffix {
		repoName += ".git"
	}
	if r.urlStyle {
		updated := r.urlValue
		updated.Path = "/" + owner + "/" + repoName
		updated.RawPath = ""
		return updated.String(), true
	}
	if strings.TrimSpace(r.scpPrefix) == "" {
		return "", false
	}
	return r.scpPrefix + owner + "/" + repoName, true
}

func (r gitHubRepoRef) withHTTPSOwner(owner string) (string, bool) {
	owner = strings.TrimSpace(owner)
	if owner == "" || strings.EqualFold(owner, r.owner) {
		return "", false
	}
	if strings.ContainsAny(owner, " \t\r\n") || strings.Contains(owner, "/") {
		return "", false
	}

	repoName := r.name
	if r.hasGitSuffix {
		repoName += ".git"
	}
	return fmt.Sprintf("https://github.com/%s/%s", owner, repoName), true
}

func repoOwnerFallbackCandidates(repoURLs []string) []string {
	if len(repoURLs) == 0 {
		return nil
	}

	owners := make([]string, 0, len(repoURLs))
	seen := make(map[string]struct{}, len(repoURLs))
	appendOwner := func(owner string) {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			return
		}
		key := strings.ToLower(owner)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		owners = append(owners, owner)
	}

	for _, repoURL := range repoURLs {
		ref, ok := parseGitHubRepoRef(repoURL)
		if !ok {
			continue
		}
		appendOwner(ref.owner)
	}
	return owners
}

func repoOwnerFallbackURL(repoURL string, ownerHints []string) (string, bool) {
	ref, ok := parseGitHubRepoRef(repoURL)
	if !ok {
		return "", false
	}

	candidates := make([]string, 0, len(ownerHints)+1)
	seen := make(map[string]struct{}, len(ownerHints)+2)
	seen[strings.ToLower(strings.TrimSpace(ref.owner))] = struct{}{}
	appendCandidate := func(owner string) {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			return
		}
		key := strings.ToLower(owner)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, owner)
	}

	for _, owner := range ownerHints {
		appendCandidate(owner)
	}
	if defaultRef, ok := parseGitHubRepoRef(config.DefaultRepositoryURL); ok && strings.EqualFold(defaultRef.name, ref.name) {
		appendCandidate(defaultRef.owner)
	}

	for _, owner := range candidates {
		candidateURL, ok := ref.withOwner(owner)
		if !ok {
			continue
		}
		return candidateURL, true
	}
	return "", false
}

func shouldFallbackCloneToDefaultBranch(baseBranch string, res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	normalized := normalizeBranchRef(baseBranch)
	if !strings.HasPrefix(normalized, "moltenhub-") && !isKnownDefaultBranchName(normalized) {
		return false
	}
	return isMissingRemoteBranchCloneError(err, res)
}

func (h Harness) runCodex(
	ctx context.Context,
	runtime agentruntime.Runtime,
	targetDir string,
	prompt string,
	opts codexRunOptions,
	agentsPath string,
	responseMode string,
) error {
	finalPrompt := strings.TrimSpace(prompt)
	cleanup := func() error { return nil }

	if trimmedAgentsPath := strings.TrimSpace(agentsPath); trimmedAgentsPath != "" {
		stagedAgentsPath, stagedCleanup, err := stageAgentsPromptFile(targetDir, trimmedAgentsPath)
		if err != nil {
			h.logf(
				"stage=workspace status=warn action=stage_agents_for_agent target=%s source=%s err=%q",
				targetDir,
				trimmedAgentsPath,
				err,
			)
			stagedAgentsPath = trimmedAgentsPath
			stagedCleanup = func() error { return nil }
		}

		promptAgentsPath := stagedAgentsPath
		targetAgentsPath, targetAgentsCleanup, ensureErr := ensureTargetAgentsPromptFile(targetDir, stagedAgentsPath)
		if ensureErr != nil {
			h.logf(
				"stage=workspace status=warn action=ensure_target_agents_for_agent target=%s source=%s err=%q",
				targetDir,
				stagedAgentsPath,
				ensureErr,
			)
			targetAgentsCleanup = func() error { return nil }
		} else {
			promptAgentsPath = promptPathForCodex(targetDir, targetAgentsPath)
		}

		finalPrompt = withAgentsPrompt(finalPrompt, promptAgentsPath)
		cleanup = combineCleanupFns(stagedCleanup, targetAgentsCleanup)
	}
	if promptWithResponseMode, err := withResponseModePrompt(finalPrompt, responseMode); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			h.logf(
				"stage=workspace status=warn action=cleanup_agents_for_agent target=%s err=%q",
				targetDir,
				cleanupErr,
			)
		}
		return err
	} else {
		finalPrompt = promptWithResponseMode
	}

	res, err := h.runCodexWithHeartbeat(ctx, runtime, targetDir, finalPrompt, opts, "")
	if shouldRetryCodexWithoutSandbox(res, err) {
		agentStage := runtimeLogStage(runtime)
		h.logf(
			"stage=%s status=warn action=retry_without_sandbox reason=%q",
			agentStage,
			"detected bubblewrap namespace sandbox failure; retrying with danger-full-access",
		)
		_, retryErr := h.runCodexWithHeartbeat(ctx, runtime, targetDir, finalPrompt, opts, "danger-full-access")
		err = retryErr
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		h.logf(
			"stage=workspace status=warn action=cleanup_agents_for_agent target=%s err=%q",
			targetDir,
			cleanupErr,
		)
	}
	return err
}

func combineCleanupFns(cleanups ...func() error) func() error {
	return func() error {
		var errs []error
		for _, cleanup := range cleanups {
			if cleanup == nil {
				continue
			}
			if err := cleanup(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

func stageAgentsPromptFile(targetDir, agentsPath string) (string, func() error, error) {
	targetDir = strings.TrimSpace(targetDir)
	agentsPath = strings.TrimSpace(agentsPath)
	if targetDir == "" {
		return "", nil, fmt.Errorf("codex target directory is required")
	}
	if agentsPath == "" {
		return "", nil, fmt.Errorf("agents source path is required")
	}

	relativeToTarget, relErr := filepath.Rel(targetDir, agentsPath)
	if relErr == nil && relativeToTarget != ".." && !strings.HasPrefix(relativeToTarget, ".."+string(filepath.Separator)) {
		return agentsPath, func() error { return nil }, nil
	}

	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return "", nil, fmt.Errorf("read agents source file: %w", err)
	}

	f, err := os.CreateTemp(targetDir, ".moltenhub-agents-*.md")
	if err != nil {
		return "", nil, fmt.Errorf("create staged agents file: %w", err)
	}

	stagedPath := f.Name()
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(stagedPath)
		return "", nil, fmt.Errorf("write staged agents file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(stagedPath)
		return "", nil, fmt.Errorf("close staged agents file: %w", err)
	}

	cleanup := func() error {
		if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove staged agents file %s: %w", stagedPath, err)
		}
		return nil
	}
	return stagedPath, cleanup, nil
}

func ensureTargetAgentsPromptFile(targetDir, agentsPath string) (string, func() error, error) {
	targetDir = strings.TrimSpace(targetDir)
	agentsPath = strings.TrimSpace(agentsPath)
	if targetDir == "" {
		return "", nil, fmt.Errorf("codex target directory is required")
	}
	if agentsPath == "" {
		return "", nil, fmt.Errorf("agents source path is required")
	}

	targetPath := filepath.Join(targetDir, "AGENTS.md")
	if st, err := os.Stat(targetPath); err == nil {
		if st.IsDir() {
			return "", nil, fmt.Errorf("target agents path %s is a directory", targetPath)
		}
		return targetPath, func() error { return nil }, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("stat target agents file %s: %w", targetPath, err)
	}

	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return "", nil, fmt.Errorf("read agents source file %s: %w", agentsPath, err)
	}
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		return "", nil, fmt.Errorf("write target agents file %s: %w", targetPath, err)
	}

	cleanup := func() error {
		if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove target agents file %s: %w", targetPath, err)
		}
		return nil
	}
	return targetPath, cleanup, nil
}

func promptPathForCodex(targetDir, path string) string {
	targetDir = strings.TrimSpace(targetDir)
	path = strings.TrimSpace(path)
	if targetDir == "" || path == "" {
		return path
	}

	rel, err := filepath.Rel(targetDir, path)
	if err != nil {
		return path
	}
	if rel == "." || rel == "" || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "./") && !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	return rel
}

func (h Harness) runCodexWithHeartbeat(
	ctx context.Context,
	runtime agentruntime.Runtime,
	targetDir, prompt string,
	opts codexRunOptions,
	sandboxOverride string,
) (execx.Result, error) {
	cmd, err := agentCommandWithOptions(runtime, targetDir, prompt, opts)
	if err != nil {
		return execx.Result{}, err
	}
	if strings.TrimSpace(sandboxOverride) != "" {
		cmd.Args = overrideCodexSandbox(cmd.Args, sandboxOverride)
	}

	runCtx := ctx
	agentStage := runtimeLogStage(runtime)
	if timeout := h.agentStageTimeout(); timeout > 0 {
		timeoutErr := fmt.Errorf("%s timed out after %s", agentStage, timeout)
		timeoutCtx, cancel := context.WithTimeoutCause(ctx, timeout, timeoutErr)
		defer cancel()
		runCtx = timeoutCtx
	}

	type codexRunResult struct {
		res execx.Result
		err error
	}
	done := make(chan codexRunResult, 1)
	go func() {
		runRes, runErr := h.runCommand(runCtx, agentStage, cmd)
		done <- codexRunResult{res: runRes, err: runErr}
	}()

	start := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case run := <-done:
			if failed, detail := codexReportedFailure(run.res); failed {
				detail = codexFailureDetailWithErrorDetails(run.res, detail)
				if isNonFatalValidationToolingFailure(detail, run.res) {
					h.logf(
						"stage=%s status=warn action=validation_tooling_unavailable detail=%q",
						agentStage,
						detail,
					)
					return run.res, nil
				}
				if isRecoveredTransientRegistryLookupFailure(detail, run.res) {
					h.logf(
						"stage=%s status=warn action=recovered_transient_registry_lookup detail=%q",
						agentStage,
						detail,
					)
					return run.res, nil
				}
				return run.res, fmt.Errorf("%s reported failure: %s", agentStage, detail)
			}
			if run.err != nil {
				if isNonFatalValidationToolingFailure("", run.res) {
					h.logf(
						"stage=%s status=warn action=validation_tooling_unavailable detail=%q",
						agentStage,
						strings.TrimSpace(strings.Join([]string{run.res.Stdout, run.res.Stderr}, "\n")),
					)
					return run.res, nil
				}
				return run.res, run.err
			}
			return run.res, nil
		case <-ticker.C:
			h.logf("stage=%s status=running elapsed_s=%d", agentStage, int(time.Since(start).Seconds()))
		case <-runCtx.Done():
			cause := context.Cause(runCtx)
			if cause != nil {
				return execx.Result{}, cause
			}
			return execx.Result{}, runCtx.Err()
		}
	}
}

func (h Harness) agentStageTimeout() time.Duration {
	if h.AgentStageTimeout <= 0 {
		return 0
	}
	return h.AgentStageTimeout
}

func overrideCodexSandbox(args []string, sandbox string) []string {
	if len(args) == 0 {
		return args
	}
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--sandbox" {
			out[i+1] = strings.TrimSpace(sandbox)
			return out
		}
	}
	return out
}

func shouldRetryCodexWithoutSandbox(res execx.Result, err error) bool {
	if err == nil && strings.TrimSpace(res.Stdout) == "" && strings.TrimSpace(res.Stderr) == "" {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr}, "\n"))
	if err != nil {
		text = strings.TrimSpace(text + "\n" + strings.ToLower(err.Error()))
	}

	if strings.Contains(text, "bubblewrap") || strings.Contains(text, "bwrap") || strings.Contains(text, "unshare failed") {
		return true
	}
	if strings.Contains(text, "no permissions to create a new namespace") {
		return true
	}
	if strings.Contains(text, "namespace error") && strings.Contains(text, "operation not permitted") {
		return true
	}
	if strings.Contains(text, "could not start any local repository command") &&
		(strings.Contains(text, "sandbox/runtime environment") || strings.Contains(text, "namespace")) {
		return true
	}
	return false
}

func codexReportedFailure(res execx.Result) (bool, string) {
	if failed, detail := codexReportedFailureInOutput(res.Stdout, true); failed {
		return true, detail
	}
	if failed, detail := codexReportedCompactPlainFailure(res.Stderr); failed {
		return true, detail
	}

	// Codex occasionally emits rich diagnostics and code snippets on stderr that
	// contain task-failure-like JSON keys. Avoid treating those noisy traces as
	// fatal unless stderr is a compact, failure-shaped payload.
	if strings.TrimSpace(res.Stdout) == "" {
		if failed, detail := codexReportedCompactStructuredFailure(res.Stderr); failed {
			return true, detail
		}
	}
	return false, ""
}

func codexReportedCompactPlainFailure(output string) (bool, string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return false, ""
	}
	lines := splitOutputLines(output)
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}
	if len(nonEmpty) == 0 || len(nonEmpty) > 8 {
		return false, ""
	}
	for _, line := range nonEmpty {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "failure:") || strings.HasPrefix(lower, "task failed") {
			return true, line
		}
		if isNoImplementationTargetLine(lower) {
			return true, "Failure: agent did not identify an implementation target."
		}
	}
	return false, ""
}

func isNoImplementationTargetLine(lowerLine string) bool {
	lowerLine = strings.TrimSpace(lowerLine)
	return strings.Contains(lowerLine, "no implementation target given") ||
		strings.Contains(lowerLine, "no implementation target was given") ||
		strings.Contains(lowerLine, "no implementation target provided") ||
		strings.Contains(lowerLine, "no implementation target was provided")
}

func codexFailureDetailWithErrorDetails(res execx.Result, failureDetail string) string {
	detail := strings.TrimSpace(failureDetail)
	if detail == "" {
		detail = "Failure: task failed."
	}
	errorDetail := codexExtractErrorDetail(res.Stdout)
	if errorDetail == "" {
		errorDetail = codexExtractErrorDetail(res.Stderr)
	}
	if errorDetail == "" {
		return detail
	}
	lowerDetail := strings.ToLower(detail)
	lowerError := strings.ToLower(errorDetail)
	if strings.Contains(lowerDetail, lowerError) {
		return detail
	}
	return strings.TrimSpace(detail + " " + errorDetail)
}

func isNonFatalValidationToolingFailure(detail string, res execx.Result) bool {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		detail,
		res.Stdout,
		res.Stderr,
	}, "\n")))
	if text == "" {
		return false
	}

	validationUnavailableMarkers := []string{
		"could not run automated test suite",
		"could not run automated tests",
		"could not run local automated tests",
		"could not run vitest in current runtime",
		"local automated test run unavailable",
		"local automated validation not runnable",
		"local automated validation not runnable in this runtime",
		"local test runner unavailable",
		"local test runner unavailable in runtime",
		"local test execution unavailable",
		"local test execution unavailable in this runtime",
		"unable to run automated test suite",
		"local validation tool missing in runtime",
		"local validation command failed in runtime",
		"local build validation command failed in runtime",
		"local build validation not runnable",
		"local build validation not runnable in runtime",
		"local lint validation tool unavailable",
		"local validation command failed",
		"local build validation command failed",
		"validation command failed in runtime",
		"build validation command failed in runtime",
		"validation command failed",
		"build validation command failed",
		"validation unavailable in runtime",
		"validation tooling unavailable",
		"local validation tooling unavailable in runtime",
		"full build validation could not run",
		"focused go validation unavailable in runtime",
		"node_modules missing",
	}
	validationUnavailable := containsAny(text, validationUnavailableMarkers)

	missingTooling := strings.Contains(text, "command not found") ||
		strings.Contains(text, ": not found") ||
		strings.Contains(text, "exit status 127") ||
		strings.Contains(text, "exited with status 127") ||
		strings.Contains(text, "exit code 127") ||
		strings.Contains(text, "returned 127") ||
		strings.Contains(text, "enoent") ||
		strings.Contains(text, "cannot find module") ||
		strings.Contains(text, "do you have node modules installed") ||
		strings.Contains(text, "have node modules installed") ||
		strings.Contains(text, "do you have node_modules installed") ||
		strings.Contains(text, "have node_modules installed") ||
		strings.Contains(text, "not installed in runtime") ||
		strings.Contains(text, "tooling/deps not installed") ||
		strings.Contains(text, "node_modules missing")
	if !missingTooling {
		return false
	}
	if validationUnavailable {
		return true
	}

	validationCommandMarkers := []string{
		"npm run lint",
		"npm run build",
		"npm run check",
		"npm run -s build",
		"npm run typecheck",
		"npm test",
		"npm run test",
		"pnpm test",
		"yarn test",
		"vitest",
		"eslint",
		"tsc",
		"go test",
		"go vet",
		"golangci-lint",
	}
	validationContextMarkers := []string{
		"validation",
		"test runner",
		"test execution",
		"automated test",
		"lint",
		"typecheck",
		"build",
		"runtime",
		"tooling",
		"deps",
		"dependency",
		"node modules",
		"node_modules",
	}
	hasValidationCommand := containsAny(text, validationCommandMarkers)
	hasValidationContext := containsAny(text, validationContextMarkers)

	// Some agent responses only include missing validation command output plus
	// an "Alternative validation" section without explicit "validation unavailable"
	// wording.
	if strings.Contains(text, "alternative validation") && hasValidationCommand {
		return true
	}

	if hasValidationCommand && hasValidationContext {
		return true
	}
	return false
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isRecoveredTransientRegistryLookupFailure(detail string, res execx.Result) bool {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		detail,
		res.Stdout,
		res.Stderr,
	}, "\n")))
	if text == "" {
		return false
	}
	if !strings.Contains(text, "one registry query command failed") {
		return false
	}
	if !strings.Contains(text, "retry succeeded") {
		return false
	}

	transientMarkers := []string{
		"eai_again",
		"getaddrinfo",
		"transient dns/network",
		"temporary failure in name resolution",
		"network is unreachable",
		"etimedout",
		"econnreset",
	}
	for _, marker := range transientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func codexExtractErrorDetail(output string) string {
	lines := splitOutputLines(output)
	if len(lines) == 0 {
		return ""
	}

	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}
	if len(nonEmpty) == 0 {
		return ""
	}

	for i, line := range nonEmpty {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "error details:") {
			suffix := strings.TrimSpace(line[len("error details:"):])
			if suffix != "" {
				return "Error details: " + suffix
			}
			extra := codexJoinErrorDetailLines(nonEmpty[i+1:])
			if extra != "" {
				return "Error details: " + extra
			}
			return "Error details: unknown error."
		}
		if isStructuredFailureErrorLine(line) {
			return line
		}
	}

	fallback := codexJoinErrorDetailLines(nonEmpty)
	if fallback == "" {
		return ""
	}
	return "Error details: " + fallback
}

func codexJoinErrorDetailLines(lines []string) string {
	const maxDetailsLines = 3
	const maxDetailChars = 320

	details := make([]string, 0, maxDetailsLines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "failure:") || strings.HasPrefix(lower, "task failed") {
			continue
		}
		details = append(details, trimmed)
		if len(details) >= maxDetailsLines {
			break
		}
	}
	if len(details) == 0 {
		return ""
	}
	joined := strings.Join(details, " | ")
	if len(joined) <= maxDetailChars {
		return joined
	}
	return strings.TrimSpace(joined[:maxDetailChars-3]) + "..."
}

func codexReportedFailureInOutput(output string, allowStructured bool) (bool, string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return false, ""
	}
	lines := splitOutputLines(output)
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}
	if len(nonEmpty) == 0 {
		return false, ""
	}

	// Codex stdout often contains long tool transcripts. To avoid stale
	// mid-run "Failure:" progress updates causing false failures, only
	// treat terminal output lines as authoritative failure markers.
	const terminalFailureWindow = 8
	start := 0
	if len(nonEmpty) > terminalFailureWindow {
		start = len(nonEmpty) - terminalFailureWindow
	}

	var structuredTaskFailureLine string
	var structuredErrorLine string
	for _, trimmed := range nonEmpty[start:] {
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "failure:") {
			return true, trimmed
		}
		if isNoImplementationTargetLine(lower) {
			return true, "Failure: agent did not identify an implementation target."
		}
		if allowStructured && structuredTaskFailureLine == "" && isStructuredTaskFailureLine(trimmed) {
			structuredTaskFailureLine = trimmed
			continue
		}
		if allowStructured && structuredTaskFailureLine != "" && structuredErrorLine == "" && isStructuredFailureErrorLine(trimmed) {
			structuredErrorLine = trimmed
		}
	}

	// Treat structured JSON-style failure output as fatal only when both a
	// task-failure marker and an accompanying error/stack line are present.
	if structuredTaskFailureLine != "" && structuredErrorLine != "" {
		return true, structuredTaskFailureLine + " " + structuredErrorLine
	}
	return false, ""
}

func codexReportedCompactStructuredFailure(output string) (bool, string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return false, ""
	}
	lines := splitOutputLines(output)
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}
	if len(nonEmpty) == 0 || len(nonEmpty) > 8 {
		return false, ""
	}

	var structuredTaskFailureLine string
	var structuredErrorLine string
	for _, line := range nonEmpty {
		if structuredTaskFailureLine == "" && isStructuredTaskFailureLine(line) {
			structuredTaskFailureLine = line
			continue
		}
		if structuredTaskFailureLine != "" && structuredErrorLine == "" && isStructuredFailureErrorLine(line) {
			structuredErrorLine = line
		}
	}
	if structuredTaskFailureLine != "" && structuredErrorLine != "" {
		return true, structuredTaskFailureLine + " " + structuredErrorLine
	}
	return false, ""
}

func isStructuredTaskFailureLine(raw string) bool {
	line := strings.TrimSpace(raw)
	lower := strings.ToLower(line)
	taskFailedMarker := "task failed"
	if !strings.Contains(lower, taskFailedMarker) {
		return false
	}

	// Structured task-failure payloads arrive as JSON-style/escaped key/value
	// lines. Keep matching case-insensitive for quoted keys.
	caseInsensitivePrefixes := []string{
		`"summary":`,
		`\"summary\":`,
		`"message":`,
		`\"message\":`,
	}
	for _, prefix := range caseInsensitivePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	// For unquoted keys, require lowercase prefixes so Go struct fields
	// like `Message: "Task failed..."` do not trigger false positives.
	if strings.HasPrefix(line, "summary:") || strings.HasPrefix(line, "message:") {
		return true
	}
	return false
}

func isStructuredFailureErrorLine(raw string) bool {
	line := strings.TrimSpace(raw)
	lower := strings.ToLower(line)
	caseInsensitivePrefixes := []string{
		`"error":`,
		`\"error\":`,
		`"stack":`,
		`\"stack\":`,
	}
	for _, prefix := range caseInsensitivePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	// For unquoted keys, require lowercase prefixes so Go struct fields
	// like `Error: err.Error()` do not trigger false positives.
	if strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "stack:") {
		return true
	}
	return false
}

func resolveTargetDir(repoDir, targetSubdir string) (string, error) {
	targetDir := filepath.Join(repoDir, filepath.Clean(targetSubdir))
	rel, err := filepath.Rel(repoDir, targetDir)
	if err != nil {
		return "", fmt.Errorf("resolve target subdir: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("targetSubdir escapes repository")
	}
	return targetDir, nil
}

func pathIsDir(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.IsDir()
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var githubURL = regexp.MustCompile(`https://github\.com/[^\s"'\\}\]]+`)

func extractFirstURL(text string) string {
	m := githubURL.FindString(text)
	return strings.TrimSpace(m)
}

type ghPRLookupEntry struct {
	URL string `json:"url"`
}

func parsePRURLFromLookupOutput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var list []ghPRLookupEntry
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		for _, entry := range list {
			if url := strings.TrimSpace(entry.URL); url != "" {
				return url
			}
		}
		return ""
	}

	var single ghPRLookupEntry
	if err := json.Unmarshal([]byte(raw), &single); err == nil {
		return strings.TrimSpace(single.URL)
	}
	return extractFirstURL(raw)
}

func hasRemoteBranch(res execx.Result) bool {
	return strings.TrimSpace(res.Stdout) != ""
}

func preflightCommands() []execx.Command {
	return preflightCommandsWithRuntime(agentruntime.Default())
}

func preflightCommandsWithRuntime(runtime agentruntime.Runtime) []execx.Command {
	return []execx.Command{
		{Name: "git", Args: []string{"--version"}},
		{Name: "gh", Args: []string{"--version"}},
		runtime.PreflightCommand(),
	}
}

func authCommand() execx.Command {
	return execx.Command{Name: "gh", Args: []string{"auth", "status"}}
}

func authSetupGitCommand() execx.Command {
	return execx.Command{Name: "gh", Args: []string{"auth", "setup-git"}}
}

func (h Harness) runAuthSetupGit(ctx context.Context) error {
	for attempt := 1; attempt <= maxAuthSetupGitAttempts; attempt++ {
		authSetupGitMu.Lock()
		_, err := h.runCommand(ctx, "auth", authSetupGitCommand())
		authSetupGitMu.Unlock()
		if err == nil {
			return nil
		}
		if !isGitConfigLockContentionError(err) {
			return err
		}
		if attempt >= maxAuthSetupGitAttempts {
			h.logf(
				"stage=auth status=warn action=setup_git_credentials reason=gitconfig_lock_contended err=%q",
				err,
			)
			return nil
		}
		h.logf(
			"stage=auth status=retry action=setup_git_credentials reason=gitconfig_lock_contended retry=%d/%d err=%q",
			attempt,
			maxAuthSetupGitAttempts-1,
			err,
		)
		if sleepErr := h.Sleep(ctx, time.Duration(attempt)*authSetupGitRetryDelay); sleepErr != nil {
			return sleepErr
		}
	}
	return nil
}

func isGitConfigLockContentionError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	return strings.Contains(text, "could not lock config file") ||
		strings.Contains(text, ".gitconfig: file exists") ||
		strings.Contains(text, ".gitconfig.lock") ||
		strings.Contains(text, "another git process seems to be running")
}

func hasGitHubAuthToken() bool {
	return strings.TrimSpace(os.Getenv("GH_TOKEN")) != "" || strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != ""
}

func shouldSetupGitHubAuthForRepos(repoURLs []string) bool {
	for _, repoURL := range repoURLs {
		repoURL = strings.TrimSpace(strings.ToLower(repoURL))
		if strings.HasPrefix(repoURL, "https://github.com/") || strings.HasPrefix(repoURL, "http://github.com/") {
			return true
		}
	}
	return false
}

func cloneCommand(cfg config.Config, repoDir string) execx.Command {
	if strings.TrimSpace(cfg.BaseBranch) == "" {
		return cloneRepoDefaultBranchCommand(cfg.RepoURL, repoDir)
	}
	return cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir)
}

func cloneRepoCommand(repoURL, baseBranch, repoDir string) execx.Command {
	return execx.Command{
		Name: "git",
		Args: []string{"clone", "--branch", baseBranch, "--single-branch", repoURL, repoDir},
	}
}

func remoteRefsCommand(repoURL string) execx.Command {
	return execx.Command{
		Name: "git",
		Args: []string{"ls-remote", "--heads", "--tags", repoURL},
	}
}

func currentBranchCommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"branch", "--show-current"},
	}
}

func fetchBaseBranchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"fetch", "origin", fmt.Sprintf("%s:refs/remotes/origin/%s", normalizeBranchRef(branch), normalizeBranchRef(branch))},
	}
}

func cloneRepoDefaultBranchCommand(repoURL, repoDir string) execx.Command {
	return execx.Command{
		Name: "git",
		Args: []string{"clone", "--single-branch", repoURL, repoDir},
	}
}

func switchMainBranchCommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"switch", "-C", "main"},
	}
}

func initializeMainBranchCommitCommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{
			"-c",
			"user.name=" + bootstrapGitUserName,
			"-c",
			"user.email=" + bootstrapGitUserEmail,
			"commit",
			"--allow-empty",
			"-m",
			bootstrapMainCommitMessage,
		},
	}
}

func shouldCreateWorkBranch(baseBranch string) bool {
	return isKnownDefaultBranchName(baseBranch)
}

func isKnownDefaultBranchName(branch string) bool {
	switch normalizeBranchRef(branch) {
	case "main", "master":
		return true
	default:
		return false
	}
}

func normalizeBranchRef(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/heads/")
	branch = strings.TrimPrefix(branch, "origin/")
	return branch
}

func branchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"switch", "-c", branch},
	}
}

func codexCommand(targetDir, prompt string) execx.Command {
	return codexCommandWithOptions(targetDir, prompt, codexRunOptions{})
}

func codexCommandWithOptions(targetDir, prompt string, opts codexRunOptions) execx.Command {
	cmd, err := agentCommandWithOptions(agentruntime.Default(), targetDir, prompt, opts)
	if err != nil {
		panic(err)
	}
	return cmd
}

func agentCommandWithOptions(
	runtime agentruntime.Runtime,
	targetDir, prompt string,
	opts codexRunOptions,
) (execx.Command, error) {
	return runtime.BuildCommand(targetDir, withCompletionGatePrompt(prompt), opts)
}

func runtimeLogStage(runtime agentruntime.Runtime) string {
	stage := strings.ToLower(strings.TrimSpace(runtime.Harness))
	if stage == "" {
		return "agent"
	}
	for _, r := range stage {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return "agent"
	}
	return stage
}

func codexImageArgs(targetDir string, imagePaths []string) ([]string, error) {
	targetDir = strings.TrimSpace(targetDir)
	if len(imagePaths) == 0 {
		return nil, nil
	}
	if targetDir == "" {
		return nil, fmt.Errorf("codex target directory is required for image attachments")
	}

	args := make([]string, 0, len(imagePaths))
	for i, imagePath := range imagePaths {
		imagePath = strings.TrimSpace(imagePath)
		if imagePath == "" {
			continue
		}

		st, err := os.Stat(imagePath)
		if err != nil {
			return nil, fmt.Errorf("resolve image path %d (%s): %w", i, imagePath, err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("resolve image path %d (%s): path is a directory", i, imagePath)
		}

		if rel, err := filepath.Rel(targetDir, imagePath); err == nil && rel != "." && rel != ".." &&
			!filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			args = append(args, filepath.ToSlash(rel))
			continue
		}

		args = append(args, imagePath)
	}
	return args, nil
}

func withPromptImagePaths(prompt string, imagePaths []string) string {
	paths := make([]string, 0, len(imagePaths))
	for _, imagePath := range imagePaths {
		imagePath = strings.TrimSpace(imagePath)
		if imagePath == "" {
			continue
		}
		paths = append(paths, imagePath)
	}
	if len(paths) == 0 {
		return prompt
	}

	var b strings.Builder
	b.WriteString(strings.TrimSpace(prompt))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("Prompt image files are available at these paths:\n")
	for _, imagePath := range paths {
		b.WriteString("- ")
		b.WriteString(imagePath)
		b.WriteByte('\n')
	}
	b.WriteString("Use these paths when you need to inspect attached images from the workspace.")
	return b.String()
}

func validateRuntimePromptImages(runtime agentruntime.Runtime, images []config.PromptImage) error {
	if len(images) == 0 || agentruntime.SupportsPromptImages(runtime.Harness) {
		return nil
	}
	return agentruntime.UnsupportedPromptImagesError(runtime.Harness)
}

func materializePromptImages(baseDir string, images []config.PromptImage) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}

	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("prompt image base dir is required")
	}

	dir := filepath.Join(baseDir, "prompt-images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create prompt image dir: %w", err)
	}

	paths := make([]string, 0, len(images))
	for i, image := range images {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(image.DataBase64))
		if err != nil {
			return nil, fmt.Errorf("decode images[%d]: %w", i, err)
		}
		path := filepath.Join(dir, promptImageFilename(image, i))
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return nil, fmt.Errorf("write images[%d]: %w", i, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func promptImageFilename(image config.PromptImage, index int) string {
	base := strings.TrimSpace(image.Name)
	if base != "" {
		base = filepath.Base(base)
		if ext := filepath.Ext(base); ext != "" {
			base = strings.TrimSuffix(base, ext)
		}
	}
	if base == "" {
		base = "prompt-image"
	}

	var b strings.Builder
	lastSep := false
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSep = false
			continue
		}
		if b.Len() > 0 && !lastSep {
			b.WriteByte('-')
			lastSep = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "prompt-image"
	}
	return fmt.Sprintf("%02d-%s%s", index+1, slug, promptImageExtension(image.MediaType))
}

func promptImageExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/png", "":
		return ".png"
	default:
		return ".img"
	}
}

func statusCommand(repoDir string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"status", "--porcelain", "--branch"}}
}

func localBranchFromStatus(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		branch := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		if branch == "" {
			return ""
		}
		if idx := strings.Index(branch, "..."); idx >= 0 {
			branch = branch[:idx]
		}
		if idx := strings.Index(branch, " "); idx >= 0 {
			branch = branch[:idx]
		}
		branch = strings.TrimPrefix(branch, "HEAD (no branch)")
		return strings.TrimSpace(branch)
	}
	return ""
}

func hasTrackedWorktreeChanges(stdout string) bool {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			continue
		}
		return true
	}
	return false
}

func hasAheadCommitsInStatus(stdout string) bool {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		return strings.Contains(line, "[ahead ")
	}
	return false
}

func addCommand(repoDir string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"add", "-A"}}
}

func addPRCommentScreenshotsCommand(repoDir string, files []string) execx.Command {
	args := []string{"add", "-f", "--"}
	if len(files) == 0 {
		args = append(args, prCommentScreenshotsRelDir)
	} else {
		args = append(args, files...)
	}
	return execx.Command{Dir: repoDir, Name: "git", Args: args}
}

func commitCommand(repoDir, msg string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"commit", "-m", msg}}
}

func pushCommand(repoDir, branch string) execx.Command {
	return pushToRemoteCommand(repoDir, publishRemoteOrigin, branch)
}

func pushToRemoteCommand(repoDir, remote, branch string) execx.Command {
	remote = normalizeGitRemoteName(remote)
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"push", "-u", remote, branch}}
}

func pushDryRunCommand(repoDir, branch string) execx.Command {
	return pushDryRunToRemoteCommand(repoDir, publishRemoteOrigin, branch)
}

func pushDryRunToRemoteCommand(repoDir, remote, branch string) execx.Command {
	remote = normalizeGitRemoteName(remote)
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"push", "--dry-run", remote, fmt.Sprintf("HEAD:refs/heads/%s", normalizeBranchRef(branch))},
	}
}

func commitsAheadOfBaseCommand(repoDir, baseBranch string) execx.Command {
	baseBranch = normalizeBranchRef(baseBranch)
	if baseBranch == "" {
		baseBranch = "HEAD"
	}
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"rev-list", "--count", fmt.Sprintf("%s..HEAD", baseBranch)},
	}
}

func fetchBranchCommand(repoDir, branch string) execx.Command {
	return fetchBranchFromRemoteCommand(repoDir, publishRemoteOrigin, branch)
}

func fetchBranchFromRemoteCommand(repoDir, remote, branch string) execx.Command {
	remote = normalizeGitRemoteName(remote)
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"fetch", remote, branch}}
}

func mergeFetchedBranchCommand(repoDir string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"merge", "--no-edit", "FETCH_HEAD"}}
}

func gitRemoteAddCommand(repoDir, remote, remoteURL string) execx.Command {
	remote = normalizeGitRemoteName(remote)
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"remote", "add", remote, remoteURL},
	}
}

func gitRemoteSetURLCommand(repoDir, remote, remoteURL string) execx.Command {
	remote = normalizeGitRemoteName(remote)
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"remote", "set-url", remote, remoteURL},
	}
}

func normalizeGitRemoteName(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return publishRemoteOrigin
	}
	return remote
}

func appendPRReviewers(args []string, reviewers []string) []string {
	for _, reviewer := range reviewers {
		normalized := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reviewer), "@"))
		if normalized == "" || strings.EqualFold(normalized, "none") {
			continue
		}
		args = append(args, "--reviewer", normalized)
	}
	return args
}

func (h Harness) commentPRScreenshots(ctx context.Context, repo repoWorkspace, files []string) error {
	if len(files) == 0 {
		return nil
	}

	body, err := prCommentScreenshotsBody(repo.PRURL, repo.PRHeadOwner, repo.Branch, files)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}

	_, err = h.runCommand(ctx, "pr", prCommentCommand(repo.Dir, repo.PRURL, body))
	return err
}

func prCommentScreenshotFiles(repoDir string) ([]string, error) {
	snapshot, err := prCommentScreenshotSnapshot(repoDir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(snapshot))
	for file := range snapshot {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func changedPRCommentScreenshotFiles(repoDir string, baseline map[string]string) ([]string, error) {
	current, err := prCommentScreenshotSnapshot(repoDir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(current))
	for file, sum := range current {
		if baseline == nil || baseline[file] != sum {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

func prCommentScreenshotSnapshot(repoDir string) (map[string]string, error) {
	root := filepath.Join(repoDir, prCommentScreenshotsRelDir)
	if st, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("stat PR screenshot directory: %w", err)
	} else if !st.IsDir() {
		return nil, fmt.Errorf("PR screenshot path is not a directory: %s", prCommentScreenshotsRelDir)
	}

	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		switch ext {
		case ".png", ".jpg", ".jpeg":
		default:
			return nil
		}
		rel, err := filepath.Rel(repoDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list PR screenshots: %w", err)
	}
	return files, nil
}

func prCommentScreenshotsBody(prURL, headOwner, branch string, files []string) (string, error) {
	owner, repo, ok := parseGitHubPRRepo(prURL)
	if !ok {
		return "", fmt.Errorf("parse GitHub pull request URL for screenshot comment: %s", prURL)
	}
	if override := strings.TrimSpace(headOwner); override != "" {
		owner = override
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", fmt.Errorf("branch is required for screenshot comment links")
	}

	var b strings.Builder
	b.WriteString("Automated screenshots captured during the run.\n\n")
	for _, file := range files {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file == "" {
			continue
		}
		rawURL := rawGitHubFileURL(owner, repo, branch, file)
		b.WriteString(fmt.Sprintf("### %s\n\n![%s](%s)\n\n", file, file, rawURL))
	}
	return strings.TrimSpace(b.String()), nil
}

func parseGitHubPRRepo(raw string) (owner, repo string, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", false
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", false
	}
	owner = strings.TrimSpace(parts[0])
	repo = strings.TrimSpace(parts[1])
	return owner, repo, owner != "" && repo != ""
}

func rawGitHubFileURL(owner, repo, branch, path string) string {
	return fmt.Sprintf(
		"https://raw.githubusercontent.com/%s/%s/%s/%s",
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(branch),
		escapePathSegments(path),
	)
}

func escapePathSegments(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func prCommentCommand(repoDir, prURL, body string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"pr", "comment", prURL, "--body", body},
	}
}

func prCreateCommand(repoDir string, cfg config.Config, branch string) execx.Command {
	return prCreateWithOptionsCommand(repoDir, cfg, cfg.BaseBranch, branch, "")
}

func prCreateWithOptionsCommand(repoDir string, cfg config.Config, baseBranch, headRef, repo string) execx.Command {
	normalizedPRBody := config.NormalizePRBody(cfg.PRBody, cfg.Prompt)
	args := []string{
		"pr", "create",
		"--base", baseBranch,
		"--head", headRef,
		"--title", cfg.PRTitle,
		"--body", normalizedPRBody,
	}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	for _, label := range cfg.Labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		args = append(args, "--label", label)
	}
	args = appendPRReviewers(args, cfg.Reviewers)
	return execx.Command{Dir: repoDir, Name: "gh", Args: args}
}

func prCreateWithoutBaseCommand(repoDir string, cfg config.Config, branch string) execx.Command {
	return prCreateWithoutBaseWithOptionsCommand(repoDir, cfg, branch, "")
}

func prCreateWithoutBaseWithOptionsCommand(repoDir string, cfg config.Config, headRef, repo string) execx.Command {
	normalizedPRBody := config.NormalizePRBody(cfg.PRBody, cfg.Prompt)
	args := []string{
		"pr", "create",
		"--head", headRef,
		"--title", cfg.PRTitle,
		"--body", normalizedPRBody,
	}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	for _, label := range cfg.Labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		args = append(args, "--label", label)
	}
	args = appendPRReviewers(args, cfg.Reviewers)
	return execx.Command{Dir: repoDir, Name: "gh", Args: args}
}

func remoteBranchExistsOnOriginCommand(repoDir, branch string) execx.Command {
	return remoteBranchExistsOnRemoteCommand(repoDir, publishRemoteOrigin, branch)
}

func remoteBranchExistsOnRemoteCommand(repoDir, remote, branch string) execx.Command {
	remote = normalizeGitRemoteName(remote)
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"ls-remote", "--heads", remote, normalizeBranchRef(branch)},
	}
}

func prLookupByHeadCommand(repoDir, branch string) execx.Command {
	return prLookupByHeadWithRepoCommand(repoDir, branch, "")
}

func prLookupByHeadWithRepoCommand(repoDir, headRef, repo string) execx.Command {
	args := []string{"pr", "list", "--state", "open", "--head", headRef, "--json", "url", "--limit", "1"}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: args,
	}
}

func prLookupAnyByHeadCommand(repoDir, branch string) execx.Command {
	return prLookupAnyByHeadWithRepoCommand(repoDir, branch, "")
}

func prLookupAnyByHeadWithRepoCommand(repoDir, headRef, repo string) execx.Command {
	args := []string{"pr", "list", "--state", "all", "--head", headRef, "--json", "url", "--limit", "1"}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: args,
	}
}

func ghViewerLoginCommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"api", "user"},
	}
}

func ghRepoViewVisibilityCommand(repoDir, repo string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"repo", "view", strings.TrimSpace(repo), "--json", "isPrivate,nameWithOwner"},
	}
}

func ghRepoForkCommand(repoDir, repo string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"repo", "fork", strings.TrimSpace(repo)},
	}
}

func prChecksCommand(repoDir, prURL string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{
			"pr", "checks", githubutil.PullRequestSelector(prURL),
			"--watch",
			"--required",
			"--interval", fmt.Sprintf("%d", prChecksWatchIntervalSeconds),
		},
	}
}

func prChecksAnyCommand(repoDir, prURL string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{
			"pr", "checks", githubutil.PullRequestSelector(prURL),
			"--watch",
			"--interval", fmt.Sprintf("%d", prChecksWatchIntervalSeconds),
		},
	}
}

func prChecksJSONCommand(repoDir, prURL string, requiredOnly bool) execx.Command {
	args := []string{
		"pr", "checks", githubutil.PullRequestSelector(prURL),
		"--json", "name,bucket,completedAt,startedAt",
	}
	if requiredOnly {
		args = append(args, "--required")
	}
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: args,
	}
}

func headCommitSHACommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"rev-parse", "HEAD"},
	}
}

func workflowDispatchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"workflow", "run", defaultCIWorkflowPath, "--ref", branch},
	}
}

func workflowDispatchRunsCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{
			"run", "list",
			"--workflow", defaultCIWorkflowPath,
			"--branch", branch,
			"--event", "workflow_dispatch",
			"--json", "status,conclusion,workflowName,displayTitle,headSha",
			"--limit", "1",
		},
	}
}

func requiredStatusChecksCommand(repoDir, repoNameWithOwner, branch string) execx.Command {
	branch = normalizeBranchRef(branch)
	route := fmt.Sprintf(
		"repos/%s/branches/%s/protection/required_status_checks",
		strings.Trim(strings.TrimSpace(repoNameWithOwner), "/"),
		url.PathEscape(branch),
	)
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"api", route},
	}
}

func withCompletionGatePrompt(prompt string) string {
	base := strings.TrimSpace(prompt)
	if base == "" {
		base = "Improve this repository in a minimal, production-ready way."
	}

	return failurefollowup.WithExecutionContract(base)
}

func remediationPrompt(basePrompt, prURL, checkSummary string, attempt int) string {
	return fmt.Sprintf(
		"%s\n\nRemediation round %d/%d.\nAn open PR already exists: %s\n\nRequired CI/CD checks are failing right now.\nLatest check output:\n%s\n\nFix the underlying issues, update tests/workflows as needed, and keep the PR high quality.",
		strings.TrimSpace(basePrompt),
		attempt,
		maxPRCheckRemediationAttempts,
		prURL,
		checkSummary,
	)
}

func remediationPromptForRepo(basePrompt, repoPath, repoURL, prURL, checkSummary string, attempt int, multiRepo bool) string {
	if !multiRepo {
		return remediationPrompt(basePrompt, prURL, checkSummary, attempt)
	}
	return fmt.Sprintf(
		"%s\n\nRemediation round %d/%d.\nTarget repository workspace path: %s\nTarget repository remote: %s\nAn open PR already exists for this repository: %s\n\nRequired CI/CD checks are failing right now for this repository.\nLatest check output:\n%s\n\nFocus remediation changes on this repository, update tests/workflows as needed, and keep the PR high quality. If you also change other repositories, ensure each changed repository has its own branch and PR.",
		strings.TrimSpace(basePrompt),
		attempt,
		maxPRCheckRemediationAttempts,
		repoPath,
		repoURL,
		prURL,
		checkSummary,
	)
}

func summarizeCheckOutput(res execx.Result) string {
	text := strings.TrimSpace(strings.Join([]string{res.Stdout, res.Stderr}, "\n"))
	if text == "" {
		return "No check output was provided by gh."
	}
	if len(text) <= maxCheckSummaryChars {
		return text
	}
	return strings.TrimSpace(text[:maxCheckSummaryChars]) + "...(truncated)"
}

type ghPRCheck struct {
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	CompletedAt string `json:"completedAt"`
	StartedAt   string `json:"startedAt"`
}

type ghWorkflowRun struct {
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	WorkflowName string `json:"workflowName"`
	DisplayTitle string `json:"displayTitle"`
	HeadSHA      string `json:"headSha"`
}

type ghRequiredStatusChecks struct {
	Contexts []string                `json:"contexts"`
	Checks   []ghRequiredStatusCheck `json:"checks"`
}

type ghRequiredStatusCheck struct {
	Context string `json:"context"`
}

type latestCheckState struct {
	Bucket string
	Time   time.Time
	Index  int
}

func (h Harness) reconcileChecksAfterFailure(ctx context.Context, repo repoWorkspace, requiredOnly bool) (bool, string, error) {
	res, err := h.runCommand(ctx, "checks", prChecksJSONCommand(repo.Dir, repo.PRURL, requiredOnly))
	if err != nil {
		return false, "", err
	}

	var checks []ghPRCheck
	if parseErr := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &checks); parseErr != nil {
		return false, "", fmt.Errorf("decode checks snapshot: %w", parseErr)
	}
	if len(checks) == 0 {
		return false, "", nil
	}

	latestByName := make(map[string]latestCheckState, len(checks))
	for i, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			continue
		}
		bucket := strings.ToLower(strings.TrimSpace(check.Bucket))
		candidate := latestCheckState{
			Bucket: bucket,
			Time:   checkSnapshotTime(check),
			Index:  i,
		}
		prev, exists := latestByName[name]
		if !exists || shouldReplaceCheckSnapshot(prev, candidate) {
			latestByName[name] = candidate
		}
	}
	if len(latestByName) == 0 {
		return false, "", nil
	}

	names := make([]string, 0, len(latestByName))
	for name := range latestByName {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names))
	allPassing := true
	for _, name := range names {
		state := latestByName[name]
		lines = append(lines, fmt.Sprintf("%s\t%s", name, state.Bucket))
		if state.Bucket != "pass" && state.Bucket != "skipping" {
			allPassing = false
		}
	}
	return allPassing, strings.Join(lines, "\n"), nil
}

func checkSnapshotTime(check ghPRCheck) time.Time {
	for _, raw := range []string{check.CompletedAt, check.StartedAt} {
		ts := strings.TrimSpace(raw)
		if ts == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func shouldReplaceCheckSnapshot(prev, candidate latestCheckState) bool {
	if candidate.Time.After(prev.Time) {
		return true
	}
	if prev.Time.After(candidate.Time) {
		return false
	}
	if prev.Time.IsZero() && !candidate.Time.IsZero() {
		return true
	}
	if !prev.Time.IsZero() && candidate.Time.IsZero() {
		return false
	}
	return candidate.Index > prev.Index
}

func (h Harness) reconcileNoChecksWithRequiredStatusChecks(ctx context.Context, repo repoWorkspace) (bool, string, error) {
	targetRepo := requiredStatusChecksRepo(repo)
	if targetRepo == "" {
		return false, "", nil
	}
	baseBranch := pickFirstNonEmpty(repo.BaseBranch, repo.Branch)
	if baseBranch == "" {
		return false, "", nil
	}

	res, err := h.runCommand(ctx, "checks", requiredStatusChecksCommand(repo.Dir, targetRepo, baseBranch))
	if err != nil {
		if isRequiredStatusChecksNotConfigured(res, err) {
			return true, fmt.Sprintf("No required status checks are configured for branch %s.", baseBranch), nil
		}
		return false, "", err
	}

	names, parseErr := parseRequiredStatusCheckNames(res.Stdout)
	if parseErr != nil {
		return false, "", parseErr
	}
	if len(names) == 0 {
		return true, fmt.Sprintf("No required status checks are configured for branch %s.", baseBranch), nil
	}
	return false, strings.Join(names, "\n"), nil
}

func (h Harness) reconcileNoChecksWithWorkflowDispatch(ctx context.Context, repo repoWorkspace) (bool, string, error) {
	headRes, headErr := h.runCommand(ctx, "checks", headCommitSHACommand(repo.Dir))
	if headErr != nil {
		return false, "", headErr
	}
	headSHA := strings.ToLower(strings.TrimSpace(headRes.Stdout))
	if headSHA == "" {
		return false, "", nil
	}

	res, err := h.runCommand(ctx, "checks", workflowDispatchRunsCommand(repo.Dir, repo.Branch))
	if err != nil {
		return false, "", err
	}

	raw := strings.TrimSpace(res.Stdout)
	if raw == "" {
		return false, "", nil
	}

	var runs []ghWorkflowRun
	if parseErr := json.Unmarshal([]byte(raw), &runs); parseErr != nil {
		return false, "", fmt.Errorf("decode workflow dispatch runs: %w", parseErr)
	}

	var matching *ghWorkflowRun
	for i := range runs {
		if strings.EqualFold(strings.TrimSpace(runs[i].HeadSHA), headSHA) {
			matching = &runs[i]
			break
		}
	}
	if matching == nil {
		return false, "", nil
	}

	workflowName := pickFirstNonEmpty(matching.DisplayTitle, matching.WorkflowName, defaultCIWorkflowPath)
	bucket := workflowDispatchConclusionBucket(
		strings.ToLower(strings.TrimSpace(matching.Status)),
		strings.ToLower(strings.TrimSpace(matching.Conclusion)),
	)
	if bucket == "" {
		return false, "", nil
	}
	summary := fmt.Sprintf("%s\t%s", workflowName, bucket)
	return bucket == "pass" || bucket == "skipping", summary, nil
}

func requiredStatusChecksRepo(repo repoWorkspace) string {
	if targetRepo := strings.TrimSpace(repo.PRTargetRepo); targetRepo != "" {
		return targetRepo
	}
	ref, ok := parseGitHubRepoRef(repo.URL)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s/%s", ref.owner, ref.name)
}

func parseRequiredStatusCheckNames(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var statusChecks ghRequiredStatusChecks
	if err := json.Unmarshal([]byte(raw), &statusChecks); err != nil {
		return nil, fmt.Errorf("decode required status checks: %w", err)
	}

	seen := make(map[string]struct{})
	var names []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, context := range statusChecks.Contexts {
		add(context)
	}
	for _, check := range statusChecks.Checks {
		add(check.Context)
	}
	sort.Strings(names)
	return names, nil
}

func isRequiredStatusChecksNotConfigured(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	if strings.Contains(text, "branch not protected") ||
		strings.Contains(text, "required status checks are not enabled") ||
		strings.Contains(text, "required_status_checks not found") {
		return true
	}
	return strings.Contains(text, "http 404") && strings.Contains(text, "required_status_checks")
}

func workflowDispatchConclusionBucket(status, conclusion string) string {
	if status != "completed" {
		if status == "" {
			return ""
		}
		return "pending"
	}
	switch conclusion {
	case "success":
		return "pass"
	case "neutral", "skipped":
		return "skipping"
	case "":
		return "pending"
	default:
		return "fail"
	}
}

func remediationCommitMessage(base string, attempt int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "chore: automated update"
	}
	return fmt.Sprintf("%s (ci remediation %d)", base, attempt)
}
