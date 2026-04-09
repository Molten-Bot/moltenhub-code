package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/failurefollowup"
	"github.com/jef/moltenhub-code/internal/slug"
	"github.com/jef/moltenhub-code/internal/workspace"
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
	maxPRChecksNoReportRetries = 18
	prChecksNoReportRetryDelay = 10 * time.Second
	maxCheckSummaryChars       = 4000
	defaultCIWorkflowPath      = ".github/workflows/ci.yml"
	maxPushSyncAttempts        = 3
	maxCloneAttempts           = 3
	cloneRetryDelay            = 2 * time.Second
	maxCloneErrorDetailChars   = 500
	maxGitErrorDetailChars     = 500
	maxReviewMetadataChars     = 12000
	maxReviewCommentsChars     = 16000
	maxReviewDiffStatChars     = 12000
	maxReviewDiffPatchChars    = 30000
)

type logFn func(string, ...any)

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
	URL     string
	Dir     string
	RelDir  string
	Branch  string
	PRURL   string
	Changed bool
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
	if hasGitHubAuthToken() {
		if _, err := h.runCommand(ctx, "auth", authSetupGitCommand()); err != nil {
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
	if cloneBaseBranch == "" {
		cloneBaseBranch = "main"
	}

	repos := make([]repoWorkspace, 0, len(repoURLs))
	for i, repoURL := range repoURLs {
		relDir := repoWorkspaceDirName(repoURL, i, len(repoURLs))
		repoDir := filepath.Join(runDir, relDir)
		branchForClone := cloneBaseBranch
		h.logf("stage=clone status=start repo=%s branch=%s repo_dir=%s", repoURL, branchForClone, relDir)
		cloneRes, cloneErr := h.runCloneWithRetry(
			ctx,
			repoURL,
			branchForClone,
			repoDir,
			relDir,
			cloneRepoCommand(repoURL, branchForClone, repoDir),
		)
		if cloneErr != nil {
			if !shouldFallbackCloneToDefaultBranch(branchForClone, cloneRes, cloneErr) {
				return h.fail(ExitClone, "clone", cloneErr, runDir)
			}

			h.logf(
				"stage=clone status=warn action=fallback_default_branch reason=missing_remote_branch repo=%s branch=%s repo_dir=%s",
				repoURL,
				branchForClone,
				relDir,
			)
			if err := os.RemoveAll(repoDir); err != nil {
				return h.fail(ExitClone, "clone", fmt.Errorf("cleanup failed clone dir %s: %w", repoDir, err), runDir)
			}
			if _, err := h.runCloneWithRetry(
				ctx,
				repoURL,
				"",
				repoDir,
				relDir,
				cloneRepoDefaultBranchCommand(repoURL, repoDir),
			); err != nil {
				return h.fail(ExitClone, "clone", err, runDir)
			}
			cloneBaseBranch = "main"
			h.logf(
				"stage=clone status=ok action=fallback_default_branch repo=%s repo_dir=%s resolved_branch=%s",
				repoURL,
				relDir,
				cloneBaseBranch,
			)
		} else {
			h.logf("stage=clone status=ok repo=%s repo_dir=%s", repoURL, relDir)
		}
		if !shouldCreateWorkBranch(cloneBaseBranch) {
			h.logf("stage=clone status=start action=fetch_main repo=%s repo_dir=%s", repoURL, relDir)
			if _, err := h.runCommand(ctx, "clone", fetchMainBranchCommand(repoDir)); err != nil {
				return h.fail(ExitClone, "clone", err, runDir)
			}
			h.logf("stage=clone status=ok action=fetch_main repo=%s repo_dir=%s", repoURL, relDir)
		}
		repos = append(repos, repoWorkspace{
			URL:    repoURL,
			Dir:    repoDir,
			RelDir: relDir,
		})
	}
	runCfg.BaseBranch = cloneBaseBranch

	targetDir, err := resolveTargetDir(repos[0].Dir, cfg.TargetSubdir)
	if err != nil {
		return h.fail(ExitConfig, "config", err, runDir)
	}
	if !h.TargetDirOK(targetDir) {
		return h.fail(ExitConfig, "config", fmt.Errorf("targetSubdir does not exist or is not a directory: %s", cfg.TargetSubdir), runDir)
	}

	createWorkBranch := shouldCreateWorkBranch(runCfg.BaseBranch)
	branch := strings.TrimSpace(runCfg.BaseBranch)
	if createWorkBranch {
		branch = slug.BranchName(cfg.Prompt, h.Now(), guid)
	}
	for i := range repos {
		repos[i].Branch = branch
		if !createWorkBranch {
			h.logf(
				"stage=git status=ok action=branch_reuse branch=%s baseBranch=%s repo=%s repo_dir=%s",
				branch,
				runCfg.BaseBranch,
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
		if err := h.verifyRemoteWriteAccess(ctx, repos[i]); err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
	}

	codexDir := targetDir
	if len(repos) > 1 {
		codexDir = runDir
	}
	imagePaths, err := materializePromptImages(codexDir, cfg.Images)
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
	codexBasePrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, repos)
	if reviewPrompt, err := h.prepareReviewPrompt(ctx, runCfg, repos, codexBasePrompt); err != nil {
		return h.fail(ExitPR, "review", err, runDir)
	} else {
		codexBasePrompt = reviewPrompt
	}
	codexTargetLabel := codexTargetLabel(cfg.TargetSubdir, len(repos) > 1)

	h.logf("stage=%s status=start target=%s", agentStage, codexTargetLabel)
	codexStart := time.Now()
	if err := h.runCodex(ctx, runtime, codexDir, codexBasePrompt, codexOpts, agentsPath); err != nil {
		return h.fail(ExitCodex, agentStage, err, runDir)
	}
	h.logf("stage=%s status=ok elapsed_s=%d", agentStage, int(time.Since(codexStart).Seconds()))

	for i := range repos {
		statusRes, err := h.runCommand(ctx, "git", statusCommand(repos[i].Dir))
		if err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		repos[i].Branch = pickFirstNonEmpty(localBranchFromStatus(statusRes.Stdout), repos[i].Branch)
		repos[i].Changed = hasTrackedWorktreeChanges(statusRes.Stdout)
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
			return h.fail(exitCode, stage, err, runDir)
		}
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

	h.logf("stage=git status=start action=commit repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	if _, err := h.runCommand(ctx, "git", addCommand(repo.Dir)); err != nil {
		return ExitGit, "git", err
	}
	if _, err := h.runCommand(ctx, "git", commitCommand(repo.Dir, cfg.CommitMessage)); err != nil {
		return ExitGit, "git", err
	}
	if err := h.pushWithSync(ctx, *repo, 0); err != nil {
		return ExitGit, "git", err
	}
	h.logf("stage=git status=ok action=commit repo=%s repo_dir=%s", repo.URL, repo.RelDir)

	h.logf("stage=pr status=start repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	createWorkBranch := shouldCreateWorkBranch(cfg.BaseBranch)
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
			prRes, err = h.runCommand(ctx, "pr", prCreateCommand(repo.Dir, cfg, repo.Branch))
		} else {
			prRes, err = h.runCommand(ctx, "pr", prCreateWithoutBaseCommand(repo.Dir, cfg, repo.Branch))
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
		if err := h.runCodex(ctx, runtime, codexDir, repairPrompt, codexOpts, agentsPath); err != nil {
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
		if _, err := h.runCommand(ctx, "git", commitCommand(repo.Dir, remediationCommitMessage(cfg.CommitMessage, attempt+1))); err != nil {
			return ExitGit, "git", err
		}
		if err := h.pushWithSync(ctx, *repo, attempt+1); err != nil {
			return ExitGit, "git", err
		}
		h.logf("stage=git status=ok action=repair_commit attempt=%d repo=%s repo_dir=%s", attempt+1, repo.URL, repo.RelDir)
	}
}

func (h Harness) pushWithSync(ctx context.Context, repo repoWorkspace, remediationAttempt int) error {
	for pushAttempt := 1; pushAttempt <= maxPushSyncAttempts; pushAttempt++ {
		res, err := h.runCommand(ctx, "git", pushCommand(repo.Dir, repo.Branch))
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
		if _, syncErr := h.runCommand(ctx, "git", pullRebaseCommand(repo.Dir, repo.Branch)); syncErr != nil {
			return fmt.Errorf("sync branch %q before push retry: %w", repo.Branch, syncErr)
		}
	}
	return fmt.Errorf("push retries exhausted for branch %q", repo.Branch)
}

func (h Harness) verifyRemoteWriteAccess(ctx context.Context, repo repoWorkspace) error {
	h.logf(
		"stage=git status=start action=probe_write_access repo=%s repo_dir=%s branch=%s",
		repo.URL,
		repo.RelDir,
		repo.Branch,
	)
	res, err := h.runCommand(ctx, "git", pushDryRunCommand(repo.Dir, repo.Branch))
	if err != nil {
		if isNonFastForwardPush(res, err) {
			h.logf(
				"stage=git status=ok action=probe_write_access reason=non_fast_forward repo=%s repo_dir=%s branch=%s",
				repo.URL,
				repo.RelDir,
				repo.Branch,
			)
			return nil
		}
		return commandErrorWithDetails(
			fmt.Sprintf("verify remote write access for repo %s branch %q", repo.URL, repo.Branch),
			err,
			res,
			maxGitErrorDetailChars,
		)
	}
	h.logf(
		"stage=git status=ok action=probe_write_access repo=%s repo_dir=%s branch=%s",
		repo.URL,
		repo.RelDir,
		repo.Branch,
	)
	return nil
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
	branch := normalizeBranchRef(repo.Branch)
	if branch == "" {
		return "", nil
	}

	remoteRes, remoteErr := h.runCommand(ctx, "git", remoteBranchExistsOnOriginCommand(repo.Dir, branch))
	if remoteErr != nil {
		return "", fmt.Errorf("verify remote branch %q for repo %s: %w", branch, repo.URL, remoteErr)
	}
	if !hasRemoteBranch(remoteRes) {
		return "", nil
	}

	lookupRes, err := h.runCommand(ctx, "pr", prLookupByHeadCommand(repo.Dir, branch))
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
	b.WriteString("- Only create a new branch when starting from 'main'; if you're fixing an existing non-'main' branch, stay on it.\n")
	b.WriteString("- Start every new branch name and PR title with 'moltenhub-'.\n")
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
		"you are ./AGENTS.md\nUse %s as your primary implementation instructions before making any changes.",
		location,
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
	return strings.TrimSpace(reviewCfg.PRURL)
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

func (h Harness) logf(format string, args ...any) {
	h.Logf(format, args...)
}

func (h Harness) runCommand(ctx context.Context, phase string, cmd execx.Command) (execx.Result, error) {
	onLine := func(stream, line string) {
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

func shouldFallbackCloneToDefaultBranch(baseBranch string, res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	normalized := normalizeBranchRef(baseBranch)
	if !strings.HasPrefix(normalized, "moltenhub-") {
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

	type codexRunResult struct {
		res execx.Result
		err error
	}
	done := make(chan codexRunResult, 1)
	agentStage := runtimeLogStage(runtime)
	go func() {
		runRes, runErr := h.runCommand(ctx, agentStage, cmd)
		done <- codexRunResult{res: runRes, err: runErr}
	}()

	start := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case run := <-done:
			if run.err == nil {
				if failed, detail := codexReportedFailure(run.res); failed {
					return run.res, fmt.Errorf("%s reported failure: %s", agentStage, detail)
				}
			}
			return run.res, run.err
		case <-ticker.C:
			h.logf("stage=%s status=running elapsed_s=%d", agentStage, int(time.Since(start).Seconds()))
		case <-ctx.Done():
			run := <-done
			if run.err == nil {
				if failed, detail := codexReportedFailure(run.res); failed {
					return run.res, fmt.Errorf("%s reported failure: %s", agentStage, detail)
				}
			}
			if run.err != nil {
				return run.res, run.err
			}
			return run.res, ctx.Err()
		}
	}
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
	combined := strings.TrimSpace(strings.Join([]string{res.Stdout, res.Stderr}, "\n"))
	if combined == "" {
		return false, ""
	}
	lines := splitOutputLines(combined)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "failure:") {
			return true, trimmed
		}
	}
	return false, ""
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

func hasGitHubAuthToken() bool {
	return strings.TrimSpace(os.Getenv("GH_TOKEN")) != "" || strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != ""
}

func cloneCommand(cfg config.Config, repoDir string) execx.Command {
	return cloneRepoCommand(cfg.RepoURL, cfg.BaseBranch, repoDir)
}

func cloneRepoCommand(repoURL, baseBranch, repoDir string) execx.Command {
	return execx.Command{
		Name: "git",
		Args: []string{"clone", "--branch", baseBranch, "--single-branch", repoURL, repoDir},
	}
}

func fetchMainBranchCommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"fetch", "origin", "main:refs/remotes/origin/main"},
	}
}

func cloneRepoDefaultBranchCommand(repoURL, repoDir string) execx.Command {
	return execx.Command{
		Name: "git",
		Args: []string{"clone", "--single-branch", repoURL, repoDir},
	}
}

func shouldCreateWorkBranch(baseBranch string) bool {
	return normalizeBranchRef(baseBranch) == "main"
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

func materializePromptImages(baseDir string, images []config.PromptImage) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
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

func addCommand(repoDir string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"add", "-A"}}
}

func commitCommand(repoDir, msg string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"commit", "-m", msg}}
}

func pushCommand(repoDir, branch string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"push", "-u", "origin", branch}}
}

func pushDryRunCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"push", "--dry-run", "origin", fmt.Sprintf("HEAD:refs/heads/%s", normalizeBranchRef(branch))},
	}
}

func pullRebaseCommand(repoDir, branch string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"pull", "--rebase", "origin", branch}}
}

func prCreateCommand(repoDir string, cfg config.Config, branch string) execx.Command {
	args := []string{
		"pr", "create",
		"--base", cfg.BaseBranch,
		"--head", branch,
		"--title", cfg.PRTitle,
		"--body", cfg.PRBody,
	}
	for _, label := range cfg.Labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		args = append(args, "--label", label)
	}
	for _, reviewer := range cfg.Reviewers {
		if strings.TrimSpace(reviewer) == "" {
			continue
		}
		args = append(args, "--reviewer", reviewer)
	}
	return execx.Command{Dir: repoDir, Name: "gh", Args: args}
}

func prCreateWithoutBaseCommand(repoDir string, cfg config.Config, branch string) execx.Command {
	args := []string{
		"pr", "create",
		"--head", branch,
		"--title", cfg.PRTitle,
		"--body", cfg.PRBody,
	}
	for _, label := range cfg.Labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		args = append(args, "--label", label)
	}
	for _, reviewer := range cfg.Reviewers {
		if strings.TrimSpace(reviewer) == "" {
			continue
		}
		args = append(args, "--reviewer", reviewer)
	}
	return execx.Command{Dir: repoDir, Name: "gh", Args: args}
}

func remoteBranchExistsOnOriginCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"ls-remote", "--heads", "origin", normalizeBranchRef(branch)},
	}
}

func prLookupByHeadCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"pr", "list", "--state", "open", "--head", branch, "--json", "url", "--limit", "1"},
	}
}

func prChecksCommand(repoDir, prURL string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{
			"pr", "checks", prURL,
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
			"pr", "checks", prURL,
			"--watch",
			"--interval", fmt.Sprintf("%d", prChecksWatchIntervalSeconds),
		},
	}
}

func prChecksJSONCommand(repoDir, prURL string, requiredOnly bool) execx.Command {
	args := []string{
		"pr", "checks", prURL,
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

func workflowDispatchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"workflow", "run", defaultCIWorkflowPath, "--ref", branch},
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

func remediationCommitMessage(base string, attempt int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "chore: automated update"
	}
	return fmt.Sprintf("%s (ci remediation %d)", base, attempt)
}
