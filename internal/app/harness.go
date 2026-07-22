package app

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

	"github.com/Molten-Bot/agent_00/internal/agentruntime"
	"github.com/Molten-Bot/agent_00/internal/config"
	"github.com/Molten-Bot/agent_00/internal/execx"
	"github.com/Molten-Bot/agent_00/internal/failurefollowup"
	"github.com/Molten-Bot/agent_00/internal/githubutil"
	"github.com/Molten-Bot/agent_00/internal/library"
	"github.com/Molten-Bot/agent_00/internal/slug"
	"github.com/Molten-Bot/agent_00/internal/workspace"
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
	maxPRCreateAttempts           = 3
	prCreateRetryDelay            = 2 * time.Second
	prChecksWatchIntervalSeconds  = 10
	defaultPRChecksWatchTimeout   = 5 * time.Minute
	// Allow up to ~3 minutes for newly-created PR checks to appear before remediation.
	maxPRChecksNoReportRetries       = 18
	prChecksNoReportRetryDelay       = 10 * time.Second
	maxCheckSummaryChars             = 4000
	defaultCIWorkflowPath            = ".github/workflows/ci.yml"
	ciFixLibraryTaskName             = "fix-pr-ci-tests"
	mergeMainLibraryTaskName         = "fix-merge-main"
	codeReviewLibraryTaskName        = "code-review"
	resolvePRCommentsLibraryTaskName = "resolve-pr-comments"
	maxPushSyncAttempts              = 3
	maxPrePRBaseSyncRemediation      = 1
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
	moltenbotCoAuthorTrailer         = "Co-authored-by: Molten Bot 000 <260473928+moltenbot000@users.noreply.github.com>"
	agentsCredentialGuardInstruction = "YOU ARE NOT ALLOWED TO SHARE: GITHUB PAT and YOUR (AGENTS) AUTH CREDENTIALS"
	prCommentScreenshotsRelDir       = ".moltenhub/pr-comment-screenshots"
	publishRemoteOrigin              = "origin"
	publishRemoteFork                = "fork"
	publishStrategyDirect            = "direct"
	publishStrategyForkFallback      = "fork-fallback"
)

type logFn func(string, ...any)

var authSetupGitMu sync.Mutex

var errPRCreatePermissionDenied = errors.New("pull request create permission denied")
var errTransientPRLookup = errors.New("transient pull request lookup failure")
var errPRChecksWatchTimeout = errors.New("pull request checks watch timed out")

// Result captures run output and status.
type Result struct {
	ExitCode         int
	Err              error
	WorkspaceDir     string
	Branch           string
	PRURL            string
	NoChanges        bool
	NoChangeEvidence bool
	RepoResults      []RepoResult
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
	RequiresNonDefaultBranch    bool
	RequiredBranch              string
	RequiredBranchTask          string
	BaselineHead                string
	CreateWorkBranch            bool
	Changed                     bool
	BranchCollisionSuffix       string
	BranchCollisionAttempts     int
	WriteAccessChecked          bool
	WriteAccessAllowed          bool
	WriteAccessErr              error
	PushRemote                  string
	PublishStrategy             string
	PRCommentScreenshotBaseline map[string]string
	PRCommentScreenshotFiles    []string
}

type codexRunOptions = agentruntime.RunOptions

type agentInvocationLogMetadata struct {
	RunID   string
	Harness string
	Mode    string
	Attempt int
	Repo    string
	RepoDir string
	Target  string
}

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
	// AgentHeartbeatInterval controls running-status log cadence while an agent is executing.
	// Zero or negative uses the default interval.
	AgentHeartbeatInterval time.Duration
	// PRChecksWatchTimeout bounds each gh pr checks --watch call when positive.
	// Zero uses the default timeout; negative disables the timeout.
	PRChecksWatchTimeout time.Duration
	// FinalReviewPasses is the exact number of read-only review passes to run
	// after a changed repository has a pull request and initial checks finish.
	FinalReviewPasses   int
	agentRetryInvariant func(context.Context) error
}

// New returns a harness configured with defaults.
func New(runner execx.Runner) Harness {
	return Harness{
		Runner:                 runner,
		Workspace:              workspace.NewManager(),
		Now:                    time.Now,
		Logf:                   func(string, ...any) {},
		TargetDirOK:            pathIsDir,
		Sleep:                  sleepWithContext,
		AgentHeartbeatInterval: 15 * time.Second,
	}
}

// Run executes a full automation attempt.
func (h Harness) Run(ctx context.Context, cfg config.Config) Result {
	if h.Runner == nil {
		return h.fail(ExitUsage, "usage", fmt.Errorf("runner is required"), "")
	}
	cfg.ApplyDefaults()
	normalizeFailureFollowUpTargeting(&cfg)
	requiresNonDefaultBranch, err := libraryTaskRequiresNonDefaultBranch(cfg)
	if err != nil {
		return h.fail(ExitConfig, "config", err, "")
	}
	cfg.RequiresNonDefaultBranch = requiresNonDefaultBranch
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
	h.logf(
		"stage=config status=ok review_mode=%s review_total=%d",
		finalReviewMode(h.FinalReviewPasses),
		h.FinalReviewPasses,
	)
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
	reviewRun := runCfg.Review != nil
	cloneBaseBranch := strings.TrimSpace(runCfg.BaseBranch)

	repos := make([]repoWorkspace, 0, len(repoURLs))
	for i, repoURL := range repoURLs {
		relDir := repoWorkspaceDirName(repoURL, i, len(repoURLs))
		repoDir := filepath.Join(runDir, relDir)
		repos = append(repos, repoWorkspace{
			URL:                      repoURL,
			Dir:                      repoDir,
			RelDir:                   relDir,
			RequiresNonDefaultBranch: runCfg.RequiresNonDefaultBranch,
			RequiredBranch:           normalizeBranchRef(cloneBaseBranch),
			RequiredBranchTask:       strings.TrimSpace(runCfg.LibraryTaskName),
			PushRemote:               publishRemoteOrigin,
			PublishStrategy:          publishStrategyDirect,
		})
	}
	if err := h.cloneRepositories(ctx, repos, cloneBaseBranch); err != nil {
		return h.fail(ExitClone, "clone", err, runDir)
	}
	if err := h.validateRequiredNonDefaultBranches(ctx, runCfg, repos); err != nil {
		return h.fail(ExitGit, "git", err, runDir)
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

	runNow := h.Now()
	generatedBranch := slug.BranchName(cfg.Prompt, runNow, guid)
	branchCollisionSuffix := runBranchCollisionSuffix(runNow, guid)
	for i := range repos {
		branch := strings.TrimSpace(repos[i].BaseBranch)
		if repos[i].CreateWorkBranch && !reviewRun {
			branch = generatedBranch
			repos[i].BranchCollisionSuffix = branchCollisionSuffix
		}
		repos[i].Branch = branch
		if !repos[i].CreateWorkBranch || reviewRun {
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
	if runCfg.RequiresNonDefaultBranch {
		h.agentRetryInvariant = func(ctx context.Context) error {
			return h.validateEnforcedNonDefaultBranches(ctx, repos)
		}
	}
	if !reviewRun {
		for i := range repos {
			if err := h.preparePublishWorkflow(ctx, &repos[i]); err != nil {
				return h.fail(ExitGit, "workflow", err, runDir)
			}
		}
		for i := range repos {
			if !repos[i].CreateWorkBranch {
				head, err := h.currentHead(ctx, repos[i])
				if err != nil {
					return h.fail(ExitGit, "git", err, runDir)
				}
				repos[i].BaselineHead = head
			}
			screenshotSnapshot, err := prCommentScreenshotSnapshot(repos[i].Dir)
			if err != nil {
				return h.fail(ExitGit, "git", err, runDir)
			}
			repos[i].PRCommentScreenshotBaseline = screenshotSnapshot
		}
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
	var reviewContext *preparedReviewContext
	if reviewPrompt, preparedContext, err := h.prepareReviewPrompt(ctx, runCfg, repos, codexBasePrompt); err != nil {
		return h.fail(ExitPR, "review", err, runDir)
	} else {
		codexBasePrompt = reviewPrompt
		reviewContext = preparedContext
	}
	agentResponseMode := cfg.ResponseMode
	invocationMode := "implementation"
	if reviewRun {
		codexBasePrompt, err = withReviewSkillPrompt(codexBasePrompt)
		if err != nil {
			return h.fail(ExitConfig, "review", err, runDir)
		}
		agentResponseMode = config.DisabledResponseMode
		invocationMode = "review"
	}
	if reviewContext != nil && reviewContext.GitHubTokenEnvSanitized {
		codexOpts.Env = githubTokenSanitizedEnv()
	}
	if env, err := prepareAgentIOEnv(runDir, codexOpts.Env); err != nil {
		return h.fail(ExitWorkspace, "workspace", err, runDir)
	} else {
		codexOpts.Env = env
	}
	codexBasePrompt = withBackpressurePrompt(codexBasePrompt, h.collectBackpressureRequirements(repos, cfg.TargetSubdir))
	codexTargetLabel := codexTargetLabel(cfg.TargetSubdir, len(repos) > 1)
	initialRepo, initialRepoDir := initialAgentInvocationRepoMetadata(repos)
	initialAgentInvocation := newAgentInvocationLogMetadata(runtime, invocationMode, 1, initialRepo, initialRepoDir, codexTargetLabel)

	h.logf("stage=%s status=start target=%s%s", agentStage, codexTargetLabel, initialAgentInvocation.logFieldsSuffix())
	codexStart := time.Now()
	agentRes, err := h.runCodexCapture(ctx, runtime, codexDir, codexBasePrompt, codexOpts, agentsPath, agentResponseMode, initialAgentInvocation)
	if err != nil {
		return h.fail(ExitCodex, agentStage, err, runDir)
	}
	h.logf("stage=%s status=ok elapsed_s=%d%s", agentStage, int(time.Since(codexStart).Seconds()), initialAgentInvocation.logFieldsSuffix())
	if err := h.validateEnforcedNonDefaultBranches(ctx, repos); err != nil {
		return h.fail(ExitGit, "git", err, runDir)
	}

	for i := range repos {
		statusRes, err := h.runCommand(ctx, "git", statusCommand(repos[i].Dir))
		if err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		if err := updateRepoBranchFromStatus(&repos[i], statusRes.Stdout); err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		var changed bool
		if reviewRun {
			changed = hasTrackedWorktreeChanges(statusRes.Stdout)
		} else {
			detected, detectErr := h.repoHasPendingChanges(ctx, repos[i], statusRes.Stdout)
			if detectErr != nil {
				return h.fail(ExitGit, "git", detectErr, runDir)
			}
			changed = detected
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
	if reviewRun {
		if changedCount > 0 {
			return h.fail(
				ExitCodex,
				agentStage,
				fmt.Errorf("review task modified files; review tasks must be read-only"),
				runDir,
			)
		}
		if err := h.completeReviewRun(ctx, runCfg, &repos[0], reviewContext, agentRes, runDir); err != nil {
			return h.failWithRepos(ExitPR, "review", err, runDir, repos)
		}
		res := buildResult(runDir, repos, true)
		res.ExitCode = ExitSuccess
		return res
	}
	if changedCount == 0 {
		if agentOutputClaimsFileChanges(agentRes) {
			return h.fail(
				ExitCodex,
				agentStage,
				fmt.Errorf("%s reported file changes, but git detected no worktree or branch changes", agentStage),
				runDir,
			)
		}
		if requiresConcreteNoChangeEvidence(cfg.Prompt) && !agentOutputCitesMoltenHubCodeNoChangeEvidence(agentRes) {
			return h.fail(
				ExitCodex,
				agentStage,
				fmt.Errorf("%s completed a failure/no-changes follow-up with no repository changes and no concrete MoltenHub Code evidence for a no-op", agentStage),
				runDir,
			)
		}
		if err := h.populateNoChangePRURLs(ctx, repos, cfg.Prompt, requiresVerifiedNoChangePRURL(cfg.Prompt)); err != nil {
			return h.failWithRepos(ExitPR, "pr", err, runDir, repos)
		}
		h.logf("stage=git status=no_changes")
		res := buildResult(runDir, repos, true)
		res.NoChangeEvidence = agentOutputCitesConcreteNoChangeEvidence(agentRes, cfg.Prompt)
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
		if err := h.populateNoChangePRURLs(ctx, repos, cfg.Prompt, requiresVerifiedNoChangePRURL(cfg.Prompt)); err != nil {
			return h.failWithRepos(ExitPR, "pr", err, runDir, repos)
		}
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
	exitCode, stage, err := h.processChangedRepoWithoutFinalReviews(
		ctx, cfg, repo, runtime, codexDir, codexOpts, codexBasePrompt,
		agentsPath, codexTargetLabel, agentStage, multiRepo,
	)
	if err != nil || h.FinalReviewPasses <= 0 || isReviewOnlyRun(cfg) || repo == nil || !repo.Changed || strings.TrimSpace(repo.PRURL) == "" {
		return exitCode, stage, err
	}
	return h.runFinalReviewCycle(
		ctx, cfg, repo, runtime, codexDir, codexOpts, codexBasePrompt,
		agentsPath, codexTargetLabel, agentStage, multiRepo,
	)
}

func isReviewOnlyRun(cfg config.Config) bool {
	if cfg.Review != nil {
		return true
	}
	taskName := strings.ToLower(strings.TrimSpace(cfg.LibraryTaskName))
	taskName = strings.ReplaceAll(taskName, "_", "-")
	return taskName == codeReviewLibraryTaskName
}

func (h Harness) processChangedRepoWithoutFinalReviews(
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
	if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
		return ExitGit, "git", err
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
	alreadyCommitted := false
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
		alreadyCommitted = true
	}
	if exitCode, stage, err := h.syncGeneratedWorkBranchWithBase(
		ctx,
		repo,
		runtime,
		codexDir,
		codexOpts,
		codexBasePrompt,
		agentsPath,
		cfg.ResponseMode,
		codexTargetLabel,
		agentStage,
	); err != nil {
		return exitCode, stage, err
	}
	if alreadyCommitted {
		if hasDelta, err := h.repoHasPullRequestDelta(ctx, *repo); err != nil {
			return ExitGit, "git", err
		} else if !hasDelta {
			repo.Changed = false
			h.logf(
				"stage=git status=ok action=skip_pr reason=no_delta_from_base repo=%s repo_dir=%s branch=%s baseBranch=%s",
				repo.URL,
				repo.RelDir,
				repo.Branch,
				repo.BaseBranch,
			)
			return ExitSuccess, "", nil
		}
	}
	if exitCode, stage, err := h.pushWithSyncAndConflictResolution(
		ctx,
		repo,
		0,
		runtime,
		codexDir,
		codexOpts,
		codexBasePrompt,
		agentsPath,
		cfg.ResponseMode,
		codexTargetLabel,
		agentStage,
	); err != nil {
		return exitCode, stage, err
	}
	h.logf("stage=git status=ok action=commit repo=%s repo_dir=%s", repo.URL, repo.RelDir)

	h.logf("stage=pr status=start repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	createWorkBranch := repo.CreateWorkBranch
	headRef := repoPRHeadRef(*repo)
	targetRepo := repoPRTargetRepo(*repo)
	if !createWorkBranch {
		prURL, err := h.lookupOpenPRURLByHead(ctx, *repo)
		if err != nil {
			if shouldTreatReusedBranchPRLookupFailureAsNonFatal(err) {
				h.logf(
					"stage=pr status=warn action=lookup_existing reason=transient_failed_after_push repo=%s repo_dir=%s branch=%s err=%q",
					repo.URL,
					repo.RelDir,
					repo.Branch,
					err,
				)
				return ExitSuccess, "", nil
			}
			return ExitPR, "pr", err
		}
		repo.PRURL = prURL
	}

	if repo.PRURL == "" {
		prURL, err := h.createPullRequestURL(ctx, *repo, cfg, createWorkBranch, headRef, targetRepo)
		if err != nil {
			if errors.Is(err, errPRCreatePermissionDenied) {
				h.logf(
					"stage=pr status=warn action=manual_create_required reason=create_pull_request_permission_denied repo=%s repo_dir=%s branch=%s head=%s target_repo=%s err=%q",
					repo.URL,
					repo.RelDir,
					repo.Branch,
					headRef,
					targetRepo,
					err,
				)
				return ExitSuccess, "", nil
			}
			return ExitPR, "pr", err
		}
		repo.PRURL = prURL
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
			requiredChecksOnly := false
			h.logf("stage=checks status=start repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
			checkRes, checkErr = h.runPRChecksWatch(ctx, repo.Dir, repo.PRURL)
			if isGitHubAuthFailure(checkRes, checkErr) {
				h.logf(
					"stage=checks status=warn action=watch_skipped reason=github_auth_unavailable repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
					repo.URL,
					repo.RelDir,
					repo.PRURL,
					attempt+1,
					checkErr,
				)
				return ExitSuccess, "", nil
			}
			if errors.Is(checkErr, errPRChecksWatchTimeout) {
				h.logf(
					"stage=checks status=warn action=watch_timeout repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
					repo.URL,
					repo.RelDir,
					repo.PRURL,
					attempt+1,
					checkErr,
				)
				timedOutChecksPassing, timedOutChecksSummary, timedOutChecksErr := h.latestChecksAreAllPassing(ctx, *repo, true)
				if timedOutChecksErr != nil {
					h.logf(
						"stage=checks status=warn action=watch_timeout_snapshot reason=query_failed repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.PRURL,
						attempt+1,
						timedOutChecksErr,
					)
					checkErr = fmt.Errorf("checks snapshot failed after watch timeout: %w", timedOutChecksErr)
				} else {
					if timedOutChecksSummary != "" {
						checkSummary = timedOutChecksSummary
					}
					if timedOutChecksPassing {
						h.logf("stage=checks status=ok reason=watch_timeout repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
						return ExitSuccess, "", nil
					}
					checkErr = errors.New("checks failed after watch timeout")
				}
			}
			if checkErr != nil && isNoRequiredChecksReported(checkRes, checkErr) {
				h.logf(
					"stage=checks status=fallback reason=no_required_checks repo=%s repo_dir=%s pr_url=%s attempt=%d",
					repo.URL,
					repo.RelDir,
					repo.PRURL,
					attempt+1,
				)
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
				requiredChecksOnly = false
				checkRes, checkErr = h.runPRChecksAnyWatch(ctx, repo.Dir, repo.PRURL)
				if isGitHubAuthFailure(checkRes, checkErr) {
					h.logf(
						"stage=checks status=warn action=watch_skipped reason=github_auth_unavailable repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.PRURL,
						attempt+1,
						checkErr,
					)
					return ExitSuccess, "", nil
				}
				if errors.Is(checkErr, errPRChecksWatchTimeout) {
					h.logf(
						"stage=checks status=warn action=watch_timeout repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.PRURL,
						attempt+1,
						checkErr,
					)
					timedOutChecksPassing, timedOutChecksSummary, timedOutChecksErr := h.latestChecksAreAllPassing(ctx, *repo, false)
					if timedOutChecksErr != nil {
						h.logf(
							"stage=checks status=warn action=watch_timeout_snapshot reason=query_failed repo=%s repo_dir=%s pr_url=%s attempt=%d err=%q",
							repo.URL,
							repo.RelDir,
							repo.PRURL,
							attempt+1,
							timedOutChecksErr,
						)
						checkErr = fmt.Errorf("checks snapshot failed after watch timeout: %w", timedOutChecksErr)
					} else {
						if timedOutChecksSummary != "" {
							checkSummary = timedOutChecksSummary
						}
						if timedOutChecksPassing {
							h.logf("stage=checks status=ok reason=watch_timeout repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
							return ExitSuccess, "", nil
						}
						checkErr = errors.New("checks failed after watch timeout")
					}
				}
			}
			if checkErr == nil {
				h.logf("stage=checks status=ok repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
				return ExitSuccess, "", nil
			}

			if summary := summarizeCheckOutput(checkRes); summary != "" {
				checkSummary = summary
			}
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
				recreated, recreateErr := h.recreatePullRequestIfClosed(ctx, repo, cfg)
				if recreateErr != nil {
					h.logf(
						"stage=checks status=warn action=recreate_closed_pr reason=failed repo=%s repo_dir=%s branch=%s pr_url=%s attempt=%d err=%q",
						repo.URL,
						repo.RelDir,
						repo.Branch,
						repo.PRURL,
						attempt+1,
						recreateErr,
					)
					return ExitPR, "pr", recreateErr
				}
				if recreated {
					noReportRetry = -1
					continue
				}
			}
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
		remediationInvocation := newAgentInvocationLogMetadata(runtime, "remediation", attempt+1, repo.RelDir, repo.RelDir, codexTargetLabel)
		h.logf(
			"stage=%s status=start target=%s mode=remediation attempt=%d repo=%s repo_dir=%s%s",
			agentStage,
			codexTargetLabel,
			attempt+1,
			repo.URL,
			repo.RelDir,
			remediationInvocation.logFieldsSuffix(),
		)
		codexStart := time.Now()
		agentRes, err := h.runCodexCapture(ctx, runtime, codexDir, repairPrompt, codexOpts, agentsPath, cfg.ResponseMode, remediationInvocation)
		if err != nil {
			return ExitCodex, agentStage, err
		}
		h.logf(
			"stage=%s status=ok elapsed_s=%d mode=remediation attempt=%d repo=%s repo_dir=%s%s",
			agentStage,
			int(time.Since(codexStart).Seconds()),
			attempt+1,
			repo.URL,
			repo.RelDir,
			remediationInvocation.logFieldsSuffix(),
		)
		if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
			return ExitGit, "git", err
		}

		statusRes, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
		if err != nil {
			return ExitGit, "git", err
		}
		if strings.TrimSpace(statusRes.Stdout) == "" {
			if agentOutputClaimsFileChanges(agentRes) {
				return ExitCodex, agentStage, fmt.Errorf("%s reported remediation file changes, but git detected no worktree or branch changes for repo %s", agentStage, repo.URL)
			}
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
		if exitCode, stage, err := h.pushWithSyncAndConflictResolution(
			ctx,
			repo,
			attempt+1,
			runtime,
			codexDir,
			codexOpts,
			codexBasePrompt,
			agentsPath,
			cfg.ResponseMode,
			codexTargetLabel,
			agentStage,
		); err != nil {
			return exitCode, stage, err
		}
		h.logf("stage=git status=ok action=repair_commit attempt=%d repo=%s repo_dir=%s", attempt+1, repo.URL, repo.RelDir)
	}
}

type prStateView struct {
	URL         string `json:"url"`
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	HeadRefName string `json:"headRefName"`
}

func (h Harness) recreatePullRequestIfClosed(ctx context.Context, repo *repoWorkspace, cfg config.Config) (bool, error) {
	if repo == nil {
		return false, fmt.Errorf("repo workspace is required")
	}
	if !repo.CreateWorkBranch || strings.TrimSpace(repo.PRURL) == "" {
		return false, nil
	}
	state, err := h.loadPullRequestState(ctx, repo.Dir, repo.PRURL)
	if err != nil {
		h.logf(
			"stage=checks status=warn action=recreate_closed_pr reason=state_lookup_failed repo=%s repo_dir=%s branch=%s pr_url=%s err=%q",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			repo.PRURL,
			err,
		)
		return false, nil
	}
	if pullRequestStateIsOpen(state) {
		return false, nil
	}

	oldPRURL := repo.PRURL
	oldBranch := repo.Branch
	pushRemote := repoPushRemote(*repo)
	if err := h.renameGeneratedWorkBranch(ctx, repo, pushRemote, "closed_pr"); err != nil {
		return false, err
	}
	repo.PRURL = ""
	if err := h.pushWithSync(ctx, repo, 0, false); err != nil {
		return false, err
	}
	prURL, err := h.createPullRequestURL(ctx, *repo, cfg, true, repoPRHeadRef(*repo), repoPRTargetRepo(*repo))
	if err != nil {
		return false, err
	}
	repo.PRURL = prURL
	h.logf(
		"stage=pr status=ok action=recreate_closed_pr repo=%s repo_dir=%s old_branch=%s branch=%s old_pr_url=%s pr_url=%s",
		repo.URL,
		repo.RelDir,
		oldBranch,
		repo.Branch,
		oldPRURL,
		repo.PRURL,
	)
	return true, nil
}

func (h Harness) loadPullRequestState(ctx context.Context, repoDir, prURL string) (prStateView, error) {
	res, err := h.runCommand(ctx, "pr", prStateViewCommand(repoDir, prURL))
	if err != nil {
		return prStateView{}, commandErrorWithDetails(
			fmt.Sprintf("load pull request state for %s", prURL),
			err,
			res,
			maxGitErrorDetailChars,
		)
	}
	var state prStateView
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &state); err != nil {
		return prStateView{}, fmt.Errorf("decode pull request state for %s: %w", prURL, err)
	}
	return state, nil
}

func pullRequestStateIsOpen(state prStateView) bool {
	if strings.TrimSpace(state.MergedAt) != "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(state.State), "open")
}

func promptPullRequestMatchesRepoBranch(state prStateView, repo repoWorkspace) bool {
	actual := normalizeBranchRef(state.HeadRefName)
	if actual == "" {
		return false
	}
	expected := normalizeBranchRef(repo.Branch)
	if expected == "" {
		expected = branchNameFromHeadRef(repoPRHeadRef(repo))
	}
	return actual == expected
}

func branchNameFromHeadRef(headRef string) string {
	headRef = strings.TrimSpace(headRef)
	if _, branch, ok := strings.Cut(headRef, ":"); ok {
		headRef = branch
	}
	return normalizeBranchRef(headRef)
}

func (h Harness) syncGeneratedWorkBranchWithBase(
	ctx context.Context,
	repo *repoWorkspace,
	runtime agentruntime.Runtime,
	codexDir string,
	codexOpts codexRunOptions,
	codexBasePrompt string,
	agentsPath string,
	responseMode string,
	codexTargetLabel string,
	agentStage string,
) (int, string, error) {
	if repo == nil {
		return ExitConfig, "config", fmt.Errorf("repo workspace is required")
	}
	if !repo.CreateWorkBranch {
		return ExitSuccess, "", nil
	}

	baseBranch := normalizeBranchRef(repo.BaseBranch)
	if baseBranch == "" {
		return ExitGit, "git", fmt.Errorf("base branch is required before syncing generated work branch for repo %s", repo.URL)
	}

	for attempt := 0; attempt <= maxPrePRBaseSyncRemediation; attempt++ {
		h.logf(
			"stage=git status=start action=sync_base repo=%s repo_dir=%s branch=%s baseBranch=%s attempt=%d",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			baseBranch,
			attempt+1,
		)
		fetchRes, fetchErr := h.runCommand(ctx, "git", fetchBranchCommand(repo.Dir, baseBranch))
		if fetchErr != nil {
			return ExitGit, "git", commandErrorWithDetails(
				fmt.Sprintf("fetch latest base branch %q for repo %s", baseBranch, repo.URL),
				fetchErr,
				fetchRes,
				maxGitErrorDetailChars,
			)
		}

		mergeRes, mergeErr := h.runCommand(ctx, "git", mergeFetchedBranchCommand(repo.Dir))
		if mergeErr == nil {
			h.logf(
				"stage=git status=ok action=sync_base repo=%s repo_dir=%s branch=%s baseBranch=%s attempt=%d",
				repo.URL,
				repo.RelDir,
				repo.Branch,
				baseBranch,
				attempt+1,
			)
			return ExitSuccess, "", nil
		}

		if !isMergeConflictResult(mergeRes, mergeErr) || attempt >= maxPrePRBaseSyncRemediation {
			return ExitGit, "git", commandErrorWithDetails(
				fmt.Sprintf("merge latest base branch %q into work branch %q for repo %s", baseBranch, repo.Branch, repo.URL),
				mergeErr,
				mergeRes,
				maxGitErrorDetailChars,
			)
		}

		h.logf(
			"stage=git status=warn action=sync_base reason=merge_conflict repo=%s repo_dir=%s branch=%s baseBranch=%s attempt=%d",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			baseBranch,
			attempt+1,
		)
		if exitCode, stage, err := h.resolveBaseSyncConflictWithAgent(
			ctx,
			repo,
			runtime,
			codexDir,
			codexOpts,
			codexBasePrompt,
			agentsPath,
			responseMode,
			codexTargetLabel,
			agentStage,
			mergeRes,
			attempt+1,
		); err != nil {
			return exitCode, stage, err
		}
	}

	return ExitGit, "git", fmt.Errorf("base branch sync retries exhausted for repo %s branch %q", repo.URL, repo.Branch)
}

func (h Harness) resolveBaseSyncConflictWithAgent(
	ctx context.Context,
	repo *repoWorkspace,
	runtime agentruntime.Runtime,
	codexDir string,
	codexOpts codexRunOptions,
	codexBasePrompt string,
	agentsPath string,
	responseMode string,
	codexTargetLabel string,
	agentStage string,
	mergeRes execx.Result,
	attempt int,
) (int, string, error) {
	if repo == nil {
		return ExitConfig, "config", fmt.Errorf("repo workspace is required")
	}
	baseBranch := normalizeBranchRef(repo.BaseBranch)
	prompt := baseSyncConflictPrompt(
		codexBasePrompt,
		repo.RelDir,
		repo.URL,
		repo.Branch,
		baseBranch,
		mergeRes,
	)
	invocation := newAgentInvocationLogMetadata(runtime, "base_sync_conflict", attempt, repo.RelDir, repo.RelDir, codexTargetLabel)
	h.logf(
		"stage=%s status=start target=%s mode=base_sync_conflict attempt=%d repo=%s repo_dir=%s%s",
		agentStage,
		codexTargetLabel,
		attempt,
		repo.URL,
		repo.RelDir,
		invocation.logFieldsSuffix(),
	)
	agentStart := time.Now()
	if _, err := h.runCodexCapture(ctx, runtime, codexDir, prompt, codexOpts, agentsPath, responseMode, invocation); err != nil {
		return ExitCodex, agentStage, err
	}
	h.logf(
		"stage=%s status=ok elapsed_s=%d mode=base_sync_conflict attempt=%d repo=%s repo_dir=%s%s",
		agentStage,
		int(time.Since(agentStart).Seconds()),
		attempt,
		repo.URL,
		repo.RelDir,
		invocation.logFieldsSuffix(),
	)
	if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
		return ExitGit, "git", err
	}

	statusRes, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
	if err != nil {
		return ExitGit, "git", err
	}
	if hasUnmergedPaths(statusRes.Stdout) {
		return ExitGit, "git", fmt.Errorf("base branch sync for repo %s still has unmerged paths after remediation", repo.URL)
	}
	if !hasTrackedWorktreeChanges(statusRes.Stdout) && !hasAheadCommitsInStatus(statusRes.Stdout) {
		return ExitSuccess, "", nil
	}

	h.logf("stage=git status=start action=sync_base_commit repo=%s repo_dir=%s baseBranch=%s", repo.URL, repo.RelDir, baseBranch)
	if _, err := h.runCommand(ctx, "git", addCommand(repo.Dir)); err != nil {
		return ExitGit, "git", err
	}
	commitRes, commitErr := h.runCommand(ctx, "git", commitCommand(repo.Dir, baseSyncCommitMessage(baseBranch)))
	if commitErr != nil {
		noChanges, statusErr := h.refreshRepoChangeStateAfterNoOpCommit(ctx, repo, commitRes, commitErr)
		if statusErr != nil {
			return ExitGit, "git", statusErr
		}
		if noChanges {
			h.logf("stage=git status=ok action=sync_base_commit repo=%s repo_dir=%s reason=no_changes_after_add", repo.URL, repo.RelDir)
			return ExitSuccess, "", nil
		}
		if !isNothingToCommitResult(commitRes, commitErr) {
			return ExitGit, "git", commitErr
		}
	}
	h.logf("stage=git status=ok action=sync_base_commit repo=%s repo_dir=%s baseBranch=%s", repo.URL, repo.RelDir, baseBranch)
	return ExitSuccess, "", nil
}

func (h Harness) resolveRemoteBranchSyncConflictWithAgent(
	ctx context.Context,
	repo *repoWorkspace,
	runtime agentruntime.Runtime,
	codexDir string,
	codexOpts codexRunOptions,
	codexBasePrompt string,
	agentsPath string,
	responseMode string,
	codexTargetLabel string,
	agentStage string,
	remote string,
	mergeRes execx.Result,
	attempt int,
) (int, string, error) {
	if repo == nil {
		return ExitConfig, "config", fmt.Errorf("repo workspace is required")
	}
	branch := normalizeBranchRef(repo.Branch)
	prompt := remoteBranchSyncConflictPrompt(
		codexBasePrompt,
		repo.RelDir,
		repo.URL,
		branch,
		remote,
		mergeRes,
	)
	invocation := newAgentInvocationLogMetadata(runtime, "remote_branch_sync_conflict", attempt, repo.RelDir, repo.RelDir, codexTargetLabel)
	h.logf(
		"stage=%s status=start target=%s mode=remote_branch_sync_conflict attempt=%d repo=%s repo_dir=%s%s",
		agentStage,
		codexTargetLabel,
		attempt,
		repo.URL,
		repo.RelDir,
		invocation.logFieldsSuffix(),
	)
	agentStart := time.Now()
	if _, err := h.runCodexCapture(ctx, runtime, codexDir, prompt, codexOpts, agentsPath, responseMode, invocation); err != nil {
		return ExitCodex, agentStage, err
	}
	h.logf(
		"stage=%s status=ok elapsed_s=%d mode=remote_branch_sync_conflict attempt=%d repo=%s repo_dir=%s%s",
		agentStage,
		int(time.Since(agentStart).Seconds()),
		attempt,
		repo.URL,
		repo.RelDir,
		invocation.logFieldsSuffix(),
	)
	if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
		return ExitGit, "git", err
	}

	statusRes, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
	if err != nil {
		return ExitGit, "git", err
	}
	if hasUnmergedPaths(statusRes.Stdout) {
		return ExitGit, "git", fmt.Errorf("remote branch sync for repo %s still has unmerged paths after remediation", repo.URL)
	}
	if !hasTrackedWorktreeChanges(statusRes.Stdout) && !hasAheadCommitsInStatus(statusRes.Stdout) {
		return ExitSuccess, "", nil
	}

	h.logf("stage=git status=start action=sync_remote_branch_commit repo=%s repo_dir=%s branch=%s remote=%s", repo.URL, repo.RelDir, branch, remote)
	if _, err := h.runCommand(ctx, "git", addCommand(repo.Dir)); err != nil {
		return ExitGit, "git", err
	}
	commitRes, commitErr := h.runCommand(ctx, "git", commitCommand(repo.Dir, remoteBranchSyncCommitMessage(branch)))
	if commitErr != nil {
		noChanges, statusErr := h.refreshRepoChangeStateAfterNoOpCommit(ctx, repo, commitRes, commitErr)
		if statusErr != nil {
			return ExitGit, "git", statusErr
		}
		if noChanges {
			h.logf("stage=git status=ok action=sync_remote_branch_commit repo=%s repo_dir=%s reason=no_changes_after_add", repo.URL, repo.RelDir)
			return ExitSuccess, "", nil
		}
		if !isNothingToCommitResult(commitRes, commitErr) {
			return ExitGit, "git", commitErr
		}
	}
	h.logf("stage=git status=ok action=sync_remote_branch_commit repo=%s repo_dir=%s branch=%s remote=%s", repo.URL, repo.RelDir, branch, remote)
	return ExitSuccess, "", nil
}

func isMergeConflictResult(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		res.Stdout,
		res.Stderr,
		err.Error(),
	}, "\n")))
	if text == "" {
		return false
	}
	markers := []string{
		"automatic merge failed",
		"merge conflict",
		"conflict (",
		"fix conflicts",
		"resolve your current index first",
		"you have unmerged paths",
	}
	return containsAny(text, markers)
}

func hasUnmergedPaths(statusStdout string) bool {
	for _, line := range strings.Split(statusStdout, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "## ") {
			continue
		}
		if len(line) < 2 {
			continue
		}
		x := line[0]
		y := line[1]
		if x == 'U' || y == 'U' {
			return true
		}
		switch string([]byte{x, y}) {
		case "AA", "DD":
			return true
		}
	}
	return false
}

func baseSyncCommitMessage(baseBranch string) string {
	baseBranch = normalizeBranchRef(baseBranch)
	if baseBranch == "" {
		baseBranch = "base"
	}
	return fmt.Sprintf("fix: sync with %s", baseBranch)
}

func remoteBranchSyncCommitMessage(branch string) string {
	branch = normalizeBranchRef(branch)
	if branch == "" {
		branch = "remote branch"
	}
	return fmt.Sprintf("fix: sync with %s", branch)
}

type remoteBranchSyncConflictError struct {
	err      error
	mergeRes execx.Result
	remote   string
	branch   string
}

func (e *remoteBranchSyncConflictError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *remoteBranchSyncConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (h Harness) pushWithSyncAndConflictResolution(
	ctx context.Context,
	repo *repoWorkspace,
	remediationAttempt int,
	runtime agentruntime.Runtime,
	codexDir string,
	codexOpts codexRunOptions,
	codexBasePrompt string,
	agentsPath string,
	responseMode string,
	codexTargetLabel string,
	agentStage string,
) (int, string, error) {
	for attempt := 0; attempt <= maxPrePRBaseSyncRemediation; attempt++ {
		err := h.pushWithSync(ctx, repo, remediationAttempt, true)
		if err == nil {
			return ExitSuccess, "", nil
		}
		var conflictErr *remoteBranchSyncConflictError
		if !errors.As(err, &conflictErr) || attempt >= maxPrePRBaseSyncRemediation {
			return ExitGit, "git", err
		}
		h.logf(
			"stage=git status=warn action=push_sync reason=merge_conflict repo=%s repo_dir=%s branch=%s remote=%s attempt=%d",
			repo.URL,
			repo.RelDir,
			repo.Branch,
			conflictErr.remote,
			attempt+1,
		)
		if exitCode, stage, resolveErr := h.resolveRemoteBranchSyncConflictWithAgent(
			ctx,
			repo,
			runtime,
			codexDir,
			codexOpts,
			codexBasePrompt,
			agentsPath,
			responseMode,
			codexTargetLabel,
			agentStage,
			conflictErr.remote,
			conflictErr.mergeRes,
			attempt+1,
		); resolveErr != nil {
			return exitCode, stage, resolveErr
		}
	}
	return ExitGit, "git", fmt.Errorf("push sync conflict retries exhausted for branch %q on remote %q", repo.Branch, repoPushRemote(*repo))
}

func (h Harness) pushWithSync(ctx context.Context, repo *repoWorkspace, remediationAttempt int, leaveMergeConflicts bool) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}
	pushRemote := repoPushRemote(*repo)
	for pushAttempt := 1; pushAttempt <= maxPushSyncAttempts; pushAttempt++ {
		if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
			return err
		}
		res, err := h.runCommand(ctx, "git", pushToRemoteCommand(repo.Dir, pushRemote, repo.Branch))
		if err == nil {
			return nil
		}
		if !isNonFastForwardPush(res, err) || pushAttempt >= maxPushSyncAttempts {
			return err
		}
		if repo.CreateWorkBranch {
			if err := h.renameWorkBranchForRemoteCollision(ctx, repo, pushRemote); err != nil {
				return fmt.Errorf("avoid remote branch collision before push retry: %w", err)
			}
			pushRemote = repoPushRemote(*repo)
			continue
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
		if mergeRes, syncErr := h.runCommand(ctx, "git", mergeFetchedBranchCommand(repo.Dir)); syncErr != nil {
			mergeErr := commandErrorWithDetails(
				fmt.Sprintf("sync branch %q on remote %q before push retry", repo.Branch, pushRemote),
				syncErr,
				mergeRes,
				maxGitErrorDetailChars,
			)
			if isMergeConflictResult(mergeRes, syncErr) {
				conflictErr := &remoteBranchSyncConflictError{
					err:      fmt.Errorf("%w; merge conflict while syncing remote branch before push retry", mergeErr),
					mergeRes: mergeRes,
					remote:   pushRemote,
					branch:   repo.Branch,
				}
				if leaveMergeConflicts {
					return conflictErr
				}
				if _, abortErr := h.runCommand(ctx, "git", mergeAbortCommand(repo.Dir)); abortErr != nil {
					return fmt.Errorf("%w; abort merge after push retry conflict: %v", mergeErr, abortErr)
				}
				return conflictErr
			}
			return mergeErr
		}
	}
	return fmt.Errorf("push retries exhausted for branch %q on remote %q", repo.Branch, pushRemote)
}

func (h Harness) renameWorkBranchForRemoteCollision(ctx context.Context, repo *repoWorkspace, remote string) error {
	return h.renameGeneratedWorkBranch(ctx, repo, remote, "remote_non_fast_forward")
}

func (h Harness) renameGeneratedWorkBranch(ctx context.Context, repo *repoWorkspace, remote, reason string) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}
	oldBranch := normalizeBranchRef(repo.Branch)
	if oldBranch == "" {
		return fmt.Errorf("work branch is required")
	}
	repo.BranchCollisionAttempts++
	newBranch := collisionAvoidanceBranchName(
		oldBranch,
		branchCollisionSuffixVariant(repo.BranchCollisionSuffix, repo.BranchCollisionAttempts),
	)
	if newBranch == "" || newBranch == oldBranch {
		return fmt.Errorf("cannot derive alternate branch for colliding work branch %q", oldBranch)
	}
	remote = normalizeGitRemoteName(remote)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "branch_collision"
	}
	h.logf(
		"stage=git status=start action=branch_rename reason=%s repo=%s repo_dir=%s branch=%s new_branch=%s remote=%s",
		reason,
		repo.URL,
		repo.RelDir,
		oldBranch,
		newBranch,
		remote,
	)
	if _, err := h.runCommand(ctx, "git", branchMoveCommand(repo.Dir, newBranch)); err != nil {
		return fmt.Errorf("rename work branch %q to %q after remote non-fast-forward on %q: %w", oldBranch, newBranch, remote, err)
	}
	repo.Branch = newBranch
	syncRepoPublishHeadRef(repo)
	h.logf(
		"stage=git status=ok action=branch_rename reason=%s repo=%s repo_dir=%s branch=%s previous_branch=%s remote=%s",
		reason,
		repo.URL,
		repo.RelDir,
		newBranch,
		oldBranch,
		remote,
	)
	return nil
}

func runBranchCollisionSuffix(now time.Time, guid string) string {
	if suffix := sanitizeBranchSuffix(guid); suffix != "" {
		if len(suffix) > 8 {
			return suffix[:8]
		}
		return suffix
	}
	if !now.IsZero() {
		return now.UTC().Format("20060102-150405")
	}
	return "retry"
}

func branchCollisionSuffixVariant(suffix string, attempt int) string {
	suffix = sanitizeBranchSuffix(suffix)
	if suffix == "" {
		suffix = "retry"
	}
	if attempt <= 1 {
		return suffix
	}
	return fmt.Sprintf("%s-%d", suffix, attempt)
}

func sanitizeBranchSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastSep := false
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_' || r == '.':
			if b.Len() == 0 || lastSep {
				continue
			}
			b.WriteByte('-')
			lastSep = true
		default:
			if b.Len() == 0 || lastSep {
				continue
			}
			b.WriteByte('-')
			lastSep = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func collisionAvoidanceBranchName(branch, suffix string) string {
	branch = strings.Trim(normalizeBranchRef(branch), "-/")
	if branch == "" {
		branch = "moltenhub-task"
	}
	suffix = branchCollisionSuffixVariant(suffix, 1)
	if strings.HasSuffix(branch, "-"+suffix) {
		return branch
	}
	const maxCollisionBranchNameLen = 120
	maxBaseLen := maxCollisionBranchNameLen - len(suffix) - 1
	if maxBaseLen < len("moltenhub-task") {
		maxBaseLen = len("moltenhub-task")
	}
	if len(branch) > maxBaseLen {
		branch = strings.Trim(branch[:maxBaseLen], "-/")
		if branch == "" {
			branch = "moltenhub-task"
		}
	}
	return branch + "-" + suffix
}

type remoteWriteAccessProbe struct {
	NonFastForward bool
}

func (h Harness) verifyRemoteWriteAccess(ctx context.Context, repo repoWorkspace) error {
	return h.verifyRemoteWriteAccessOnRemote(ctx, repo, publishRemoteOrigin, "git")
}

func (h Harness) verifyRemoteWriteAccessOnRemote(ctx context.Context, repo repoWorkspace, remote, stage string) error {
	_, err := h.probeRemoteWriteAccessOnRemote(ctx, repo, remote, stage)
	return err
}

func (h Harness) probeRemoteWriteAccessOnRemote(ctx context.Context, repo repoWorkspace, remote, stage string) (remoteWriteAccessProbe, error) {
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
			return remoteWriteAccessProbe{NonFastForward: true}, nil
		}
		return remoteWriteAccessProbe{}, commandErrorWithDetails(
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
	return remoteWriteAccessProbe{}, nil
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

	probe, err := h.probeRemoteWriteAccessOnRemote(ctx, *repo, publishRemoteOrigin, "workflow")
	if err == nil && probe.NonFastForward && repo.CreateWorkBranch {
		if renameErr := h.renameWorkBranchForRemoteCollision(ctx, repo, publishRemoteOrigin); renameErr != nil {
			return renameErr
		}
		probe, err = h.probeRemoteWriteAccessOnRemote(ctx, *repo, publishRemoteOrigin, "workflow")
		if err == nil && probe.NonFastForward {
			err = fmt.Errorf("generated work branch %q still conflicts with remote %q", repo.Branch, publishRemoteOrigin)
		}
	}

	if err == nil {
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

func (h Harness) populateNoChangePRURLs(ctx context.Context, repos []repoWorkspace, prompt string, requirePRURL bool) error {
	promptPRURL := pullRequestURLFromPrompt(prompt)
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
			if requirePRURL {
				return fmt.Errorf("verify existing pull request for unchanged repo %s branch %q: %w", repos[i].URL, repos[i].Branch, err)
			}
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
				if requirePRURL {
					return fmt.Errorf("verify any pull request for unchanged repo %s branch %q: %w", repos[i].URL, repos[i].Branch, err)
				}
				continue
			}
		}
		if prURL == "" && promptPRURL != "" {
			state, err := h.loadPullRequestState(ctx, repos[i].Dir, promptPRURL)
			if err != nil {
				h.logf(
					"stage=pr status=warn action=lookup_existing reason=prompt_pr_lookup_failed repo=%s repo_dir=%s branch=%s pr_url=%s err=%q",
					repos[i].URL,
					repos[i].RelDir,
					repos[i].Branch,
					promptPRURL,
					err,
				)
				if requirePRURL {
					return fmt.Errorf("verify prompt pull request for unchanged repo %s branch %q: %w", repos[i].URL, repos[i].Branch, err)
				}
			} else if !promptPullRequestMatchesRepoBranch(state, repos[i]) {
				h.logf(
					"stage=pr status=warn action=lookup_existing reason=prompt_pr_branch_mismatch repo=%s repo_dir=%s branch=%s pr_url=%s pr_head=%s",
					repos[i].URL,
					repos[i].RelDir,
					repos[i].Branch,
					promptPRURL,
					state.HeadRefName,
				)
				if requirePRURL {
					return fmt.Errorf(
						"verify prompt pull request for unchanged repo %s branch %q: prompt PR head branch %q did not match",
						repos[i].URL,
						repos[i].Branch,
						strings.TrimSpace(state.HeadRefName),
					)
				}
			} else {
				prURL = pickFirstNonEmpty(state.URL, promptPRURL)
			}
		}
		if prURL == "" {
			if requirePRURL {
				return fmt.Errorf("no pull request found for unchanged repo %s branch %q", repos[i].URL, repos[i].Branch)
			}
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
	return nil
}

func (h Harness) createPullRequestURL(
	ctx context.Context,
	repo repoWorkspace,
	cfg config.Config,
	createWorkBranch bool,
	headRef,
	targetRepo string,
) (string, error) {
	var cmd execx.Command
	if createWorkBranch {
		cmd = prCreateWithOptionsCommand(repo.Dir, cfg, repo.BaseBranch, headRef, targetRepo)
	} else {
		cmd = prCreateWithoutBaseWithOptionsCommand(repo.Dir, cfg, headRef, targetRepo)
	}

	for attempt := 1; ; attempt++ {
		prRes, err := h.runCommand(ctx, "pr", cmd)
		if err != nil {
			if ctx.Err() != nil {
				return "", err
			}
			if existingPRURL, ok := existingPRURLFromCreateFailure(prRes, err); ok {
				h.logf(
					"stage=pr status=warn action=reuse_existing reason=already_exists repo=%s repo_dir=%s branch=%s pr_url=%s",
					repo.URL,
					repo.RelDir,
					repo.Branch,
					existingPRURL,
				)
				return existingPRURL, nil
			}

			retryable := shouldRetryPRCreate(prRes, err)
			if retryable {
				prURL, lookupErr := h.lookupOpenPRURLByHead(ctx, repo)
				if lookupErr == nil && prURL != "" {
					h.logf(
						"stage=pr status=warn action=reuse_existing reason=create_transient_failed repo=%s repo_dir=%s branch=%s pr_url=%s",
						repo.URL,
						repo.RelDir,
						repo.Branch,
						prURL,
					)
					return prURL, nil
				}
				if lookupErr != nil {
					h.logf(
						"stage=pr status=warn action=lookup_existing reason=create_transient_lookup_failed repo=%s repo_dir=%s branch=%s err=%q",
						repo.URL,
						repo.RelDir,
						repo.Branch,
						lookupErr,
					)
				}
			}

			if !retryable || attempt >= maxPRCreateAttempts {
				if isPRCreatePermissionDenied(prRes, err) {
					return "", fmt.Errorf("%w: %v", errPRCreatePermissionDenied, err)
				}
				return "", err
			}
			h.logf(
				"stage=pr status=retry reason=transient_create_error repo=%s repo_dir=%s branch=%s retry=%d/%d err=%q",
				repo.URL,
				repo.RelDir,
				repo.Branch,
				attempt,
				maxPRCreateAttempts-1,
				err,
			)
			if sleepErr := h.Sleep(ctx, prCreateRetryDelay); sleepErr != nil {
				return "", fmt.Errorf("pr create retry interrupted: %w", sleepErr)
			}
			continue
		}

		if prURL := extractFirstURL(prRes.Stdout); prURL != "" {
			return prURL, nil
		}
		if prURL := extractFirstURL(prRes.Stderr); prURL != "" {
			return prURL, nil
		}

		prURL, verifyErr := h.lookupOpenPRURLByHead(ctx, repo)
		if verifyErr != nil {
			return "", fmt.Errorf("verify open pull request for repo %s: %w", repo.URL, verifyErr)
		}
		if prURL != "" {
			return prURL, nil
		}
		return "", fmt.Errorf("gh pr create did not return a PR URL for repo %s", repo.URL)
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
		if shouldRetryPRCreate(remoteRes, remoteErr) {
			return "", fmt.Errorf("%w: verify remote branch %q for repo %s on remote %q: %w", errTransientPRLookup, branch, repo.URL, pushRemote, remoteErr)
		}
		return "", fmt.Errorf("verify remote branch %q for repo %s on remote %q: %w", branch, repo.URL, pushRemote, remoteErr)
	}
	if !hasRemoteBranch(remoteRes) {
		return "", nil
	}

	lookupRes, err := h.runCommand(ctx, "pr", prLookupByHeadWithRepoCommand(repo.Dir, headRef, repoPRTargetRepo(repo)))
	if err != nil {
		return "", commandErrorWithDetails(
			fmt.Sprintf("lookup open pull request for repo %s branch %q", repo.URL, headRef),
			err,
			lookupRes,
			maxGitErrorDetailChars,
		)
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
		return "", commandErrorWithDetails(
			fmt.Sprintf("lookup any pull request for repo %s branch %q", repo.URL, headRef),
			err,
			lookupRes,
			maxGitErrorDetailChars,
		)
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

func (h Harness) validateRequiredNonDefaultBranches(ctx context.Context, cfg config.Config, repos []repoWorkspace) error {
	if !cfg.RequiresNonDefaultBranch {
		return nil
	}
	for i := range repos {
		requiredBranch := normalizeBranchRef(repos[i].BaseBranch)
		repos[i].RequiresNonDefaultBranch = true
		repos[i].RequiredBranch = requiredBranch
		repos[i].RequiredBranchTask = strings.TrimSpace(cfg.LibraryTaskName)
		if strings.TrimSpace(repos[i].Branch) == "" {
			repos[i].Branch = requiredBranch
		}
	}
	return h.validateEnforcedNonDefaultBranches(ctx, repos)
}

func (h Harness) validateEnforcedNonDefaultBranches(ctx context.Context, repos []repoWorkspace) error {
	for _, repo := range repos {
		if err := h.validateEnforcedNonDefaultBranch(ctx, repo); err != nil {
			return err
		}
	}
	return nil
}

func (h Harness) validateEnforcedNonDefaultBranchCheckouts(ctx context.Context, repos []repoWorkspace) error {
	for _, repo := range repos {
		if err := h.validateEnforcedNonDefaultBranchCheckout(ctx, repo); err != nil {
			return err
		}
	}
	return nil
}

func (h Harness) validateEnforcedNonDefaultBranch(ctx context.Context, repo repoWorkspace) error {
	if !repo.RequiresNonDefaultBranch {
		return nil
	}

	requiredBranch := normalizeBranchRef(repo.RequiredBranch)
	taskName := strings.TrimSpace(repo.RequiredBranchTask)
	if isProtectedDefaultBranchName(requiredBranch) {
		return fmt.Errorf(
			"library task %q requires a non-default branch; configured base branch %q is a protected default branch name for repo %s",
			taskName,
			requiredBranch,
			repo.URL,
		)
	}

	res, err := h.runCommand(ctx, "git", remoteDefaultBranchCommand(repo.Dir))
	if err != nil {
		return commandErrorWithDetails(
			fmt.Sprintf("resolve repository default branch for repo %s", repo.URL),
			err,
			res,
			maxGitErrorDetailChars,
		)
	}
	defaultBranch := remoteDefaultBranchFromLSRemote(res.Stdout)
	if defaultBranch == "" {
		return fmt.Errorf("resolve repository default branch for repo %s: git ls-remote --symref origin HEAD returned no branch", repo.URL)
	}
	if requiredBranch == defaultBranch {
		return fmt.Errorf(
			"library task %q requires a non-default branch; configured base branch %q is repository default %q for repo %s",
			taskName,
			requiredBranch,
			defaultBranch,
			repo.URL,
		)
	}
	if err := h.validateEnforcedNonDefaultBranchCheckout(ctx, repo); err != nil {
		return err
	}

	remoteHeadRes, remoteHeadErr := h.runCommand(ctx, "git", remoteBranchExistsOnOriginCommand(repo.Dir, requiredBranch))
	if remoteHeadErr != nil {
		return commandErrorWithDetails(
			fmt.Sprintf("verify remote branch head %q for repo %s", requiredBranch, repo.URL),
			remoteHeadErr,
			remoteHeadRes,
			maxGitErrorDetailChars,
		)
	}
	if !remoteBranchHeadExists(remoteHeadRes.Stdout, requiredBranch) {
		return fmt.Errorf(
			"library task %q requires a remote branch head; configured base branch %q does not resolve to refs/heads/%s for repo %s",
			taskName,
			requiredBranch,
			requiredBranch,
			repo.URL,
		)
	}
	return nil
}

func (h Harness) validateEnforcedNonDefaultBranchCheckout(ctx context.Context, repo repoWorkspace) error {
	if !repo.RequiresNonDefaultBranch {
		return nil
	}

	requiredBranch := normalizeBranchRef(repo.RequiredBranch)
	taskName := strings.TrimSpace(repo.RequiredBranchTask)
	if isProtectedDefaultBranchName(requiredBranch) {
		return fmt.Errorf(
			"library task %q requires a non-default branch; configured base branch %q is a protected default branch name for repo %s",
			taskName,
			requiredBranch,
			repo.URL,
		)
	}
	if publishBranch := strings.TrimSpace(repo.Branch); publishBranch != requiredBranch {
		return fmt.Errorf(
			"library task %q requires publish branch %q to remain pinned; harness publish branch is %q for repo %s",
			taskName,
			requiredBranch,
			publishBranch,
			repo.URL,
		)
	}

	currentRes, currentErr := h.runCommand(ctx, "git", currentBranchCommand(repo.Dir))
	if currentErr != nil {
		return commandErrorWithDetails(
			fmt.Sprintf("verify checked out branch for repo %s", repo.URL),
			currentErr,
			currentRes,
			maxGitErrorDetailChars,
		)
	}
	currentBranch := strings.TrimSpace(currentRes.Stdout)
	if currentBranch == "" {
		return fmt.Errorf(
			"library task %q requires a checked-out non-default branch; configured base branch %q left HEAD detached for repo %s",
			taskName,
			requiredBranch,
			repo.URL,
		)
	}
	if currentBranch != requiredBranch {
		return fmt.Errorf(
			"library task %q requires configured base branch %q to remain checked out; current branch is %q for repo %s",
			taskName,
			requiredBranch,
			currentBranch,
			repo.URL,
		)
	}

	return nil
}

func libraryTaskRequiresNonDefaultBranch(cfg config.Config) (bool, error) {
	taskName := strings.TrimSpace(cfg.LibraryTaskName)
	if taskName == "" {
		return cfg.RequiresNonDefaultBranch, nil
	}

	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err != nil {
		return false, fmt.Errorf("load library task metadata for %q: %w", taskName, err)
	}
	if task, ok := catalog.Task(taskName); ok {
		return cfg.RequiresNonDefaultBranch || task.RequiresNonDefaultBranch, nil
	}
	return cfg.RequiresNonDefaultBranch, nil
}

func isProtectedDefaultBranchName(branch string) bool {
	switch normalizeBranchRef(branch) {
	case "main", "master", "trunk":
		return true
	default:
		return false
	}
}

func remoteBranchHeadExists(output, branch string) bool {
	wantRef := "refs/heads/" + normalizeBranchRef(branch)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == wantRef {
			return true
		}
	}
	return false
}

func remoteDefaultBranchFromLSRemote(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "ref:" || fields[2] != "HEAD" {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(fields[1], "refs/heads/"))
	}
	return ""
}

func (h Harness) cloneRepository(ctx context.Context, repo *repoWorkspace, branch string, repoOwnerHints []string) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}

	branch = strings.TrimSpace(branch)
	if repo.RequiresNonDefaultBranch {
		requiredBranch := normalizeBranchRef(repo.RequiredBranch)
		if requiredBranch == "" {
			return fmt.Errorf("library task %q requires a non-default branch before clone", repo.RequiredBranchTask)
		}
		if isProtectedDefaultBranchName(requiredBranch) {
			return fmt.Errorf(
				"library task %q requires a non-default branch; configured base branch %q is a protected default branch name for repo %s",
				repo.RequiredBranchTask,
				requiredBranch,
				repo.URL,
			)
		}
		if normalizeBranchRef(branch) != requiredBranch {
			return fmt.Errorf(
				"library task %q requires branch %q, but clone requested branch %q for repo %s",
				repo.RequiredBranchTask,
				requiredBranch,
				normalizeBranchRef(branch),
				repo.URL,
			)
		}
	}

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
	if repo.RequiresNonDefaultBranch {
		return fmt.Errorf(
			"library task %q requires existing remote branch %q for repo %s; refusing default-branch fallback: %w",
			repo.RequiredBranchTask,
			normalizeBranchRef(repo.RequiredBranch),
			repo.URL,
			cloneErr,
		)
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
	if err := updateRepoBranchFromStatus(repo, statusRes.Stdout); err != nil {
		return false, err
	}
	changed, detectErr := h.repoHasPendingChanges(ctx, *repo, statusRes.Stdout)
	if detectErr != nil {
		return false, detectErr
	}
	repo.Changed = changed
	return !repo.Changed, nil
}

func updateRepoBranchFromStatus(repo *repoWorkspace, statusStdout string) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}
	observedBranch := strings.TrimSpace(localBranchFromStatus(statusStdout))
	if repo.RequiresNonDefaultBranch {
		requiredBranch := normalizeBranchRef(repo.RequiredBranch)
		if observedBranch == "" {
			return fmt.Errorf(
				"library task %q requires branch %q to remain checked out; git status did not report a branch for repo %s",
				repo.RequiredBranchTask,
				requiredBranch,
				repo.URL,
			)
		}
		if observedBranch != requiredBranch {
			return fmt.Errorf(
				"library task %q requires branch %q to remain checked out; git status reported %q for repo %s",
				repo.RequiredBranchTask,
				requiredBranch,
				observedBranch,
				repo.URL,
			)
		}
		repo.Branch = requiredBranch
	} else {
		repo.Branch = pickFirstNonEmpty(observedBranch, repo.Branch)
	}
	syncRepoPublishHeadRef(repo)
	return nil
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
	if hasTrackedWorktreeChanges(statusStdout) {
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
	if changed, err := h.repoHeadChangedSinceBaseline(ctx, repo); err != nil {
		return false, err
	} else if changed {
		return true, nil
	}
	if !repo.CreateWorkBranch {
		return false, nil
	}
	return h.repoHasPullRequestDelta(ctx, repo)
}

func (h Harness) repoHasPullRequestDelta(ctx context.Context, repo repoWorkspace) (bool, error) {
	if !repo.CreateWorkBranch {
		return true, nil
	}
	commitsAhead, err := h.countCommitsAheadOfBase(ctx, repo, normalizeBranchRef(repo.BaseBranch))
	if err != nil {
		return false, err
	}
	return commitsAhead > 0, nil
}

func (h Harness) repoHeadChangedSinceBaseline(ctx context.Context, repo repoWorkspace) (bool, error) {
	baseline := strings.TrimSpace(repo.BaselineHead)
	if baseline == "" {
		return false, nil
	}
	head, err := h.currentHead(ctx, repo)
	if err != nil {
		return false, err
	}
	return head != baseline, nil
}

func (h Harness) currentHead(ctx context.Context, repo repoWorkspace) (string, error) {
	res, err := h.runCommand(ctx, "git", headCommitSHACommand(repo.Dir))
	if err != nil {
		return "", commandErrorWithDetails(
			fmt.Sprintf("read current HEAD for repo %s", repo.URL),
			err,
			res,
			maxGitErrorDetailChars,
		)
	}
	head := strings.TrimSpace(res.Stdout)
	if head == "" {
		return "", fmt.Errorf("read current HEAD for repo %s: git rev-parse HEAD returned empty output", repo.URL)
	}
	return head, nil
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
) (string, *preparedReviewContext, error) {
	if cfg.Review == nil {
		return basePrompt, nil, nil
	}
	if len(repos) != 1 {
		return "", nil, fmt.Errorf("review tasks support exactly one repository")
	}

	repo := repos[0]
	h.logf("stage=review status=start repo=%s repo_dir=%s", repo.URL, repo.RelDir)
	reviewContext, err := h.buildReviewPromptContext(ctx, repo, *cfg.Review)
	if err != nil {
		return "", nil, err
	}
	h.logf("stage=review status=ok repo=%s repo_dir=%s", repo.URL, repo.RelDir)

	if strings.TrimSpace(reviewContext.Prompt) == "" {
		return basePrompt, reviewContext, nil
	}
	if strings.TrimSpace(basePrompt) == "" {
		return reviewContext.Prompt, reviewContext, nil
	}
	return strings.TrimSpace(basePrompt + "\n\n" + reviewContext.Prompt), reviewContext, nil
}

func (h Harness) runFinalReviewCycle(
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
	if repo == nil || h.FinalReviewPasses <= 0 || strings.TrimSpace(repo.PRURL) == "" {
		return ExitSuccess, "", nil
	}

	for pass := 1; pass <= h.FinalReviewPasses; pass++ {
		reviewMode := finalReviewMode(h.FinalReviewPasses)
		h.logf(
			"stage=review status=start action=pass review_mode=%s pass=%d total=%d repo=%s repo_dir=%s pr_url=%s",
			reviewMode,
			pass,
			h.FinalReviewPasses,
			repo.URL,
			repo.RelDir,
			repo.PRURL,
		)
		headBefore, err := h.currentHead(ctx, *repo)
		if err != nil {
			return ExitGit, "git", err
		}
		reviewCfg := config.ReviewConfig{
			PRURL:      repo.PRURL,
			HeadBranch: repo.Branch,
			Trigger:    "post-task-cycle",
			Writeback:  "summary-comment",
		}
		reviewContext, err := h.buildReviewPromptContext(ctx, *repo, reviewCfg)
		if err != nil {
			return ExitPR, "review", err
		}
		reviewPrompt := finalReviewPrompt(cfg.Prompt, reviewContext.Prompt, pass, h.FinalReviewPasses)
		reviewPrompt, err = withReviewSkillPrompt(reviewPrompt)
		if err != nil {
			return ExitConfig, "review", err
		}
		reviewOpts := codexOpts
		reviewOpts.ImagePaths = nil
		if reviewContext.GitHubTokenEnvSanitized {
			if len(reviewOpts.Env) == 0 {
				reviewOpts.Env = githubTokenSanitizedEnv()
			} else {
				reviewOpts.Env = environWithoutKeys(reviewOpts.Env, "GH_TOKEN", "GITHUB_TOKEN")
			}
		}
		invocation := newAgentInvocationLogMetadata(runtime, "review", pass, repo.RelDir, repo.RelDir, repo.RelDir)
		reviewRes, err := h.runCodexCapture(
			ctx,
			runtime,
			repo.Dir,
			reviewPrompt,
			reviewOpts,
			agentsPath,
			config.DisabledResponseMode,
			invocation,
		)
		if err != nil {
			return ExitCodex, agentStage, err
		}
		if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
			return ExitGit, "git", err
		}
		statusRes, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
		if err != nil {
			return ExitGit, "git", err
		}
		headAfter, err := h.currentHead(ctx, *repo)
		if err != nil {
			return ExitGit, "git", err
		}
		if hasTrackedWorktreeChanges(statusRes.Stdout) || headAfter != headBefore {
			return ExitCodex, "review", fmt.Errorf("post-task review pass %d modified repository files or history", pass)
		}

		outcome, ok := parseReviewOutcome(reviewOutputText(reviewRes))
		if !ok {
			return ExitCodex, "review", fmt.Errorf("post-task review pass %d returned no valid structured outcome", pass)
		}
		switch outcome.Status {
		case "blocked":
			return ExitCodex, "review", fmt.Errorf("post-task review pass %d was blocked: %s", pass, strings.TrimSpace(outcome.Summary))
		case "clean":
			if len(outcome.Findings) != 0 || !outcome.MergeReady {
				return ExitCodex, "review", fmt.Errorf("post-task review pass %d returned an inconsistent clean outcome", pass)
			}
		case "findings":
			if len(outcome.Findings) == 0 {
				return ExitCodex, "review", fmt.Errorf("post-task review pass %d reported findings without actionable items", pass)
			}
		default:
			return ExitCodex, "review", fmt.Errorf("post-task review pass %d returned unsupported status %q", pass, outcome.Status)
		}

		commentBody := finalReviewCommentBody(outcome, pass, h.FinalReviewPasses)
		bodyPath := finalReviewSummaryBodyPath(filepath.Dir(repo.Dir), reviewContext.Metadata.Number, pass)
		if err := h.writeWorkspaceFile(bodyPath, []byte(commentBody), 0o600); err != nil {
			return ExitWorkspace, "review", fmt.Errorf("write post-task review comment: %w", err)
		}
		selector, targetRepo := reviewWritebackTarget(reviewContext.Metadata, reviewCfg, reviewContext.Selector)
		if err := h.postReviewSummary(
			ctx,
			repo.Dir,
			selector,
			targetRepo,
			reviewContext.Metadata.Number,
			bodyPath,
			repo.PRURL,
			reviewContext.GitHubTokenEnvSanitized,
		); err != nil {
			return ExitPR, "review", fmt.Errorf("post review cycle comment for pass %d: %w", pass, err)
		}
		h.logf(
			"stage=review status=ok action=pass review_mode=%s pass=%d total=%d findings=%d repo=%s repo_dir=%s pr_url=%s",
			reviewMode,
			pass,
			h.FinalReviewPasses,
			len(outcome.Findings),
			repo.URL,
			repo.RelDir,
			repo.PRURL,
		)
		if len(outcome.Findings) == 0 {
			continue
		}

		h.logf(
			"stage=review_fix status=start action=agent review_mode=%s pass=%d total=%d repo=%s repo_dir=%s pr_url=%s",
			reviewMode,
			pass,
			h.FinalReviewPasses,
			repo.URL,
			repo.RelDir,
			repo.PRURL,
		)
		fixHeadBefore, err := h.currentHead(ctx, *repo)
		if err != nil {
			return ExitGit, "git", err
		}
		fixPrompt := finalReviewFixPrompt(cfg.Prompt, *repo, outcome.Findings, pass, h.FinalReviewPasses)
		fixInvocation := newAgentInvocationLogMetadata(runtime, "review_fix", pass, repo.RelDir, repo.RelDir, repo.RelDir)
		if _, err := h.runCodexCapture(
			ctx,
			runtime,
			repo.Dir,
			fixPrompt,
			codexOpts,
			agentsPath,
			cfg.ResponseMode,
			fixInvocation,
		); err != nil {
			return ExitCodex, agentStage, err
		}
		if err := h.validateEnforcedNonDefaultBranch(ctx, *repo); err != nil {
			return ExitGit, "git", err
		}
		fixStatus, err := h.runCommand(ctx, "git", statusCommand(repo.Dir))
		if err != nil {
			return ExitGit, "git", err
		}
		fixHeadAfter, err := h.currentHead(ctx, *repo)
		if err != nil {
			return ExitGit, "git", err
		}
		hasRealFixDiff := hasTrackedWorktreeChanges(fixStatus.Stdout)
		if fixHeadAfter != fixHeadBefore {
			fixDiff, diffErr := h.runCommand(ctx, "git", reviewFixDiffCommand(repo.Dir, fixHeadBefore))
			if diffErr != nil {
				return ExitGit, "git", fmt.Errorf("verify review fix diff for pass %d: %w", pass, diffErr)
			}
			hasRealFixDiff = hasRealFixDiff || strings.TrimSpace(fixDiff.Stdout) != ""
		}
		if !hasRealFixDiff {
			return ExitCodex, "review_fix", fmt.Errorf("review fix pass %d produced no repository changes", pass)
		}

		fixCfg := cfg
		fixCfg.CommitMessage = fmt.Sprintf("fix: address review findings (pass %d)", pass)
		h.logf(
			"stage=review_checks status=start action=verify review_mode=%s pass=%d total=%d repo=%s repo_dir=%s pr_url=%s",
			reviewMode,
			pass,
			h.FinalReviewPasses,
			repo.URL,
			repo.RelDir,
			repo.PRURL,
		)
		exitCode, stage, err := h.processChangedRepoWithoutFinalReviews(
			ctx,
			fixCfg,
			repo,
			runtime,
			codexDir,
			codexOpts,
			codexBasePrompt,
			agentsPath,
			codexTargetLabel,
			agentStage,
			multiRepo,
		)
		if err != nil {
			return exitCode, stage, err
		}
		h.logf(
			"stage=review_checks status=ok action=verify review_mode=%s pass=%d total=%d repo=%s repo_dir=%s pr_url=%s",
			reviewMode,
			pass,
			h.FinalReviewPasses,
			repo.URL,
			repo.RelDir,
			repo.PRURL,
		)
	}
	return ExitSuccess, "", nil
}

func finalReviewMode(passes int) string {
	switch passes {
	case 1:
		return "low"
	case 3:
		return "medium"
	case 6:
		return "high"
	default:
		return "off"
	}
}

func finalReviewPrompt(originalPrompt, preparedContext string, pass, total int) string {
	return strings.TrimSpace(fmt.Sprintf(
		"%s\n\nPost-task review pass %d/%d. Review the current pull request independently, even if an earlier pass was clean. Follow the bundled review skill as the controlling review standard and return its structured outcome.\n\nOriginal task prompt:\n%s\n\n%s",
		codeReviewLibraryPrompt(),
		pass,
		total,
		indentedPromptBlock(originalPrompt, "(not available)"),
		strings.TrimSpace(preparedContext),
	))
}

func finalReviewCommentBody(outcome reviewOutcome, pass, total int) string {
	marker := fmt.Sprintf("<!-- moltenhub-review-cycle pass=%d total=%d -->", pass, total)
	return truncateReviewCommentBody(marker + "\n" + conciseReviewCommentBody(outcome))
}

func finalReviewSummaryBodyPath(runDir string, prNumber, pass int) string {
	if prNumber > 0 {
		return filepath.Join(runDir, fmt.Sprintf("review-cycle-pr-%d-pass-%d.md", prNumber, pass))
	}
	return filepath.Join(runDir, fmt.Sprintf("review-cycle-pass-%d.md", pass))
}

func finalReviewFixPrompt(originalPrompt string, repo repoWorkspace, findings []reviewFinding, pass, total int) string {
	var b strings.Builder
	b.WriteString(resolvePRCommentsLibraryPrompt())
	b.WriteString(fmt.Sprintf("\n\nAutomated review repair pass %d/%d.\n", pass, total))
	b.WriteString("For this automated pass, the isolated findings below are the complete repair input. Do not independently collect, act on, or broaden the change to other PR comments or earlier pass findings.\n")
	b.WriteString("Update the existing pull request branch; do not create a new pull request or change branches.\n")
	b.WriteString("Do not commit or push; leave the verified changes for the harness.\n\n")
	b.WriteString("Original task prompt:\n")
	b.WriteString(indentedPromptBlock(originalPrompt, "(not available)"))
	b.WriteString("\n\nPull request:\n")
	b.WriteString("- URL: " + strings.TrimSpace(repo.PRURL) + "\n")
	b.WriteString("- Head branch: " + strings.TrimSpace(repo.Branch) + "\n")
	b.WriteString("\nIsolated findings from the current review pass:\n")
	for i, finding := range findings {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, reviewFindingPoint(finding)))
	}
	return strings.TrimSpace(b.String())
}

func indentedPromptBlock(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = "  " + strings.TrimRight(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}

func withAgentsPrompt(prompt, agentsPath string) string {
	base := strings.TrimSpace(prompt)
	agentsPath = strings.TrimSpace(agentsPath)

	location := "./AGENTS.md"
	if agentsPath != "" {
		location = agentsPath
	}

	directive := fmt.Sprintf(
		"Use %s as your primary implementation instructions before making any changes.\n%s",
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
	HeadRefOID  string `json:"headRefOid"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

type preparedReviewContext struct {
	Selector                string
	Metadata                reviewPRMetadata
	Prompt                  string
	GitHubTokenEnvSanitized bool
}

func (h Harness) buildReviewPromptContext(
	ctx context.Context,
	repo repoWorkspace,
	reviewCfg config.ReviewConfig,
) (*preparedReviewContext, error) {
	selector := reviewSelector(reviewCfg)
	if selector == "" {
		return nil, fmt.Errorf("review selector is required")
	}

	metadataCmd := prReviewMetadataCommand(repo.Dir, selector)
	metaRes, err := h.runCommand(ctx, "review", metadataCmd)
	gitHubTokenEnvSanitized := false
	if err != nil {
		if !isGitHubUnauthorizedError(err) {
			return nil, fmt.Errorf("load pull request metadata: %w", err)
		}
		retryCmd := commandWithoutGitHubTokenEnv(metadataCmd)
		retryRes, retryErr := h.runCommand(ctx, "review", retryCmd)
		if retryErr != nil {
			return nil, fmt.Errorf("load pull request metadata: %w (retry without GH_TOKEN/GITHUB_TOKEN: %v)", err, retryErr)
		}
		h.logf("stage=review status=warn action=retry_without_env_github_token reason=github_auth_unauthorized repo=%s repo_dir=%s", repo.URL, repo.RelDir)
		metaRes = retryRes
		gitHubTokenEnvSanitized = true
	}

	var metadata reviewPRMetadata
	if err := json.Unmarshal([]byte(strings.TrimSpace(metaRes.Stdout)), &metadata); err != nil {
		return nil, fmt.Errorf("decode pull request metadata: %w", err)
	}
	if metadata.Number <= 0 {
		return nil, fmt.Errorf("pull request metadata did not include a valid number")
	}
	if strings.TrimSpace(metadata.BaseRefName) == "" {
		return nil, fmt.Errorf("pull request metadata did not include a base branch")
	}

	if _, err := h.runCommand(ctx, "review", fetchRemoteBranchCommand(repo.Dir, metadata.BaseRefName)); err != nil {
		return nil, fmt.Errorf("fetch pull request base branch %q: %w", metadata.BaseRefName, err)
	}
	if _, err := h.runCommand(ctx, "review", fetchPullRequestHeadCommand(repo.Dir, metadata.Number)); err != nil {
		return nil, fmt.Errorf("fetch pull request head for #%d: %w", metadata.Number, err)
	}

	discussionText := h.loadPullRequestDiscussion(ctx, repo.Dir, metadata.Number, gitHubTokenEnvSanitized)

	baseRef := remoteTrackingRef(metadata.BaseRefName)
	headRef := pullRequestTrackingRef(metadata.Number)

	diffStatRes, err := h.runCommand(ctx, "review", reviewDiffStatCommand(repo.Dir, baseRef, headRef))
	if err != nil {
		return nil, fmt.Errorf("summarize pull request diff: %w", err)
	}
	diffPatchRes, err := h.runCommand(ctx, "review", reviewDiffPatchCommand(repo.Dir, baseRef, headRef))
	if err != nil {
		return nil, fmt.Errorf("load pull request diff: %w", err)
	}

	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode pull request metadata: %w", err)
	}

	var b strings.Builder
	b.WriteString("Prepared pull-request review context (collected before you started):\n")
	b.WriteString(fmt.Sprintf("- Repository remote: %s\n", repo.URL))
	b.WriteString(fmt.Sprintf("- Pull request: #%d\n", metadata.Number))
	b.WriteString(fmt.Sprintf("- Pull request URL: %s\n", pickFirstNonEmpty(metadata.URL, reviewCfg.PRURL)))
	b.WriteString(fmt.Sprintf("- Base branch: %s\n", metadata.BaseRefName))
	b.WriteString(fmt.Sprintf("- Head branch: %s\n", pickFirstNonEmpty(metadata.HeadRefName, reviewCfg.HeadBranch)))
	if trigger := strings.TrimSpace(reviewCfg.Trigger); trigger != "" {
		b.WriteString(fmt.Sprintf("- Review trigger: %s\n", trigger))
	}
	if reviewer := strings.TrimSpace(reviewCfg.RequestedReviewer); reviewer != "" {
		b.WriteString(fmt.Sprintf("- Verified requested reviewer: %s\n", reviewer))
	}
	b.WriteString("- Existing PR discussion has already been fetched for you below.\n")
	b.WriteString("- The git diff below was generated locally after fetching the PR head and base refs.\n")
	b.WriteString("- Treat this prepared context as a starting point and verify important claims yourself before concluding.\n\n")
	b.WriteString("Pull request metadata:\n```json\n")
	b.WriteString(truncateForPrompt(string(metadataJSON), maxReviewMetadataChars))
	b.WriteString("\n```\n\n")
	b.WriteString("Existing pull request discussion:\n```text\n")
	b.WriteString(truncateForPrompt(nonEmptyOrDefault(discussionText, "No pull-request discussion was returned by GitHub."), maxReviewCommentsChars))
	b.WriteString("\n```\n\n")
	b.WriteString("Local git diff summary:\n```text\n")
	b.WriteString(truncateForPrompt(nonEmptyOrDefault(joinCommandOutput(diffStatRes), "No diff summary output was returned by git diff --stat --summary."), maxReviewDiffStatChars))
	b.WriteString("\n```\n\n")
	b.WriteString("Local git diff patch:\n```diff\n")
	b.WriteString(truncateForPrompt(nonEmptyOrDefault(diffPatchRes.Stdout, "No diff patch output was returned by git diff."), maxReviewDiffPatchChars))
	b.WriteString("\n```")
	return &preparedReviewContext{
		Selector:                selector,
		Metadata:                metadata,
		Prompt:                  strings.TrimSpace(b.String()),
		GitHubTokenEnvSanitized: gitHubTokenEnvSanitized,
	}, nil
}

func (h Harness) loadPullRequestDiscussion(ctx context.Context, repoDir string, number int, sanitizeGitHubTokenEnv bool) string {
	if number <= 0 {
		return "Pull-request discussion unavailable: invalid pull request number."
	}
	sections := []struct {
		title string
		cmd   execx.Command
	}{
		{title: "Issue comments", cmd: prReviewIssueCommentsCommand(repoDir, number)},
		{title: "Review comments", cmd: prReviewReviewCommentsCommand(repoDir, number)},
		{title: "Reviews", cmd: prReviewReviewsCommand(repoDir, number)},
	}
	var b strings.Builder
	for i, section := range sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section.title)
		b.WriteString(":\n")
		cmd := section.cmd
		if sanitizeGitHubTokenEnv {
			cmd = commandWithoutGitHubTokenEnv(cmd)
		}
		res, err := h.runCommand(ctx, "review", cmd)
		if err != nil {
			b.WriteString("Unavailable: ")
			b.WriteString(err.Error())
			continue
		}
		b.WriteString(nonEmptyOrDefault(strings.TrimSpace(res.Stdout), "[]"))
	}
	return strings.TrimSpace(b.String())
}

type reviewFinding struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Title    string `json:"title"`
}

type reviewOutcome struct {
	Status     string          `json:"status"`
	MergeReady bool            `json:"mergeReady"`
	Summary    string          `json:"summary"`
	Positives  []string        `json:"positives"`
	Findings   []reviewFinding `json:"findings"`
}

func (h Harness) completeReviewRun(
	ctx context.Context,
	cfg config.Config,
	repo *repoWorkspace,
	reviewContext *preparedReviewContext,
	agentRes execx.Result,
	runDir string,
) error {
	if repo == nil {
		return fmt.Errorf("repo workspace is required")
	}
	if cfg.Review == nil {
		return fmt.Errorf("review config is required")
	}

	selector := reviewSelector(*cfg.Review)
	var metadata reviewPRMetadata
	if reviewContext != nil {
		selector = pickFirstNonEmpty(reviewContext.Selector, selector)
		metadata = reviewContext.Metadata
	}
	if selector == "" {
		return fmt.Errorf("review selector is required")
	}

	prURL := pickFirstNonEmpty(metadata.URL, cfg.Review.PRURL)
	repo.PRURL = prURL
	if head := strings.TrimSpace(metadata.HeadRefName); head != "" {
		if repo.RequiresNonDefaultBranch && head != normalizeBranchRef(repo.RequiredBranch) {
			return fmt.Errorf(
				"library task %q requires review branch %q; pull request metadata reported head branch %q",
				repo.RequiredBranchTask,
				normalizeBranchRef(repo.RequiredBranch),
				head,
			)
		}
		repo.Branch = head
	}

	outcome, outcomeOK := parseReviewOutcome(reviewOutputText(agentRes))
	writebackSelector, writebackRepo := reviewWritebackTarget(metadata, *cfg.Review, selector)
	if writebackSelector == "" {
		return fmt.Errorf("review selector is required")
	}

	if reviewWritebackMode(cfg.Review.Writeback) == "summary-comment" {
		body := reviewCommentBody(agentRes, outcome, outcomeOK)
		bodyPath := reviewSummaryBodyPath(runDir, metadata.Number)
		if err := h.writeWorkspaceFile(bodyPath, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write review summary body: %w", err)
		}
		h.logf("stage=review status=start action=comment pr_url=%s", prURL)
		if err := h.postReviewSummary(ctx, repo.Dir, writebackSelector, writebackRepo, metadata.Number, bodyPath, prURL, reviewContext != nil && reviewContext.GitHubTokenEnvSanitized); err != nil {
			if isTransientReviewWritebackError(err) {
				h.logf("stage=review status=warn action=comment_writeback_deferred pr_url=%s err=%q", prURL, err)
			} else {
				return fmt.Errorf("post pull request review summary: %w", err)
			}
		} else {
			h.logf("stage=review status=ok action=comment pr_url=%s", prURL)
		}
	}

	if cfg.Review.AutoMerge {
		if err := h.autoMergeCleanReview(ctx, *repo, writebackSelector, writebackRepo, cfg.Review.MergeMethod, metadata, outcome, reviewContext != nil && reviewContext.GitHubTokenEnvSanitized); err != nil {
			return err
		}
	}
	return nil
}

func reviewWritebackTarget(metadata reviewPRMetadata, reviewCfg config.ReviewConfig, fallbackSelector string) (string, string) {
	prURL := pickFirstNonEmpty(metadata.URL, reviewCfg.PRURL)
	selector := strings.TrimSpace(fallbackSelector)
	if metadata.Number > 0 {
		selector = strconv.Itoa(metadata.Number)
	} else if prSelector := githubutil.PullRequestSelector(prURL); prSelector != "" {
		selector = prSelector
	}
	return selector, githubutil.PullRequestRepository(prURL)
}

func (h Harness) postReviewSummary(
	ctx context.Context,
	repoDir string,
	selector string,
	targetRepo string,
	prNumber int,
	bodyPath string,
	prURL string,
	sanitizeGitHubTokenEnv bool,
) error {
	cmd := prReviewSummaryCommand(repoDir, selector, targetRepo, bodyPath)
	if sanitizeGitHubTokenEnv {
		cmd = commandWithoutGitHubTokenEnv(cmd)
	}
	if _, err := h.runCommand(ctx, "review", cmd); err == nil {
		return nil
	} else {
		h.logf("stage=review status=warn action=comment_review_failed pr_url=%s err=%q", prURL, err)
		fallbackCmd := prReviewSummaryFallbackCommand(repoDir, selector, targetRepo, prNumber, bodyPath)
		if sanitizeGitHubTokenEnv {
			fallbackCmd = commandWithoutGitHubTokenEnv(fallbackCmd)
		}
		if _, fallbackErr := h.runCommand(ctx, "review", fallbackCmd); fallbackErr != nil {
			return reviewSummaryWritebackError{reviewErr: err, fallbackErr: fallbackErr}
		}
	}
	h.logf("stage=review status=ok action=comment_fallback pr_url=%s", prURL)
	return nil
}

type reviewSummaryWritebackError struct {
	reviewErr   error
	fallbackErr error
}

func (e reviewSummaryWritebackError) Error() string {
	return fmt.Sprintf("review command failed: %v; fallback pull request comment failed: %v", e.reviewErr, e.fallbackErr)
}

func (e reviewSummaryWritebackError) Unwrap() error {
	return e.fallbackErr
}

func isTransientReviewWritebackError(err error) bool {
	var writebackErr reviewSummaryWritebackError
	if errors.As(err, &writebackErr) {
		return isTransientGitHubCLIError(writebackErr.fallbackErr)
	}
	return isTransientGitHubCLIError(err)
}

func (h Harness) autoMergeCleanReview(
	ctx context.Context,
	repo repoWorkspace,
	selector string,
	targetRepo string,
	mergeMethod string,
	metadata reviewPRMetadata,
	outcome reviewOutcome,
	sanitizeGitHubTokenEnv bool,
) error {
	if !reviewOutcomeAllowsAutoMerge(outcome) {
		h.logf("stage=review status=skip action=auto_merge reason=review_not_clean pr_url=%s status=%s", metadata.URL, outcome.Status)
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.State), "OPEN") {
		h.logf("stage=review status=skip action=auto_merge reason=pr_not_open pr_url=%s state=%s", metadata.URL, metadata.State)
		return nil
	}
	if metadata.IsDraft {
		h.logf("stage=review status=skip action=auto_merge reason=draft pr_url=%s", metadata.URL)
		return nil
	}
	headRefOID := strings.TrimSpace(metadata.HeadRefOID)
	if headRefOID == "" {
		h.logf("stage=review status=skip action=auto_merge reason=missing_head_ref_oid pr_url=%s", metadata.URL)
		return nil
	}

	method := reviewMergeMethod(mergeMethod)
	h.logf("stage=review status=start action=auto_merge method=%s pr_url=%s", method, metadata.URL)
	cmd := prMergeAutoCommand(repo.Dir, selector, targetRepo, method, headRefOID)
	if sanitizeGitHubTokenEnv {
		cmd = commandWithoutGitHubTokenEnv(cmd)
	}
	if _, err := h.runCommand(ctx, "review", cmd); err != nil {
		if isAutoMergeUnsupportedError(err) {
			h.logf("stage=review status=skip action=auto_merge reason=unsupported_or_unconfigured pr_url=%s err=%q", metadata.URL, err)
			return nil
		}
		return fmt.Errorf("enable clean-review auto-merge: %w", err)
	}
	h.logf("stage=review status=ok action=auto_merge method=%s pr_url=%s", method, metadata.URL)
	return nil
}

func isAutoMergeUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	unsupportedFragments := []string{
		"enablepullrequestautomerge",
		"protected branch rules not configured",
		"auto-merge is disabled",
		"auto merge is disabled",
		"automerge is disabled",
		"auto-merge is not enabled",
		"does not have the correct permissions",
		"mergepullrequest",
		"resource not accessible by integration",
	}
	for _, fragment := range unsupportedFragments {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func (h Harness) writeWorkspaceFile(path string, data []byte, perm os.FileMode) error {
	writeFile := h.Workspace.WriteFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	return writeFile(path, data, perm)
}

func reviewWritebackMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "summary", "comment", "summary-comment":
		return "summary-comment"
	case "off", "none", "disabled":
		return "off"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func reviewMergeMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "merge", "rebase":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "squash"
	}
}

func reviewOutputText(res execx.Result) string {
	stdout := strings.TrimSpace(res.Stdout)
	stderr := strings.TrimSpace(res.Stderr)
	switch {
	case stdout != "" && stderr != "":
		return strings.TrimSpace(stdout + "\n\n" + stderr)
	case stdout != "":
		return stdout
	default:
		return stderr
	}
}

func reviewCommentBody(res execx.Result, outcome reviewOutcome, outcomeOK bool) string {
	if outcomeOK {
		return truncateReviewCommentBody(conciseReviewCommentBody(outcome))
	}

	body := strings.TrimSpace(stripReviewOutcomeJSON(reviewOutputText(res)))
	if body == "" {
		body = strings.TrimSpace(outcome.Summary)
	}
	if body == "" {
		if reviewOutcomeAllowsAutoMerge(outcome) {
			body = "Review completed. No material issues were found."
		} else {
			body = "Review completed, but no review summary was returned by the agent."
		}
	}
	return truncateReviewCommentBody(body)
}

func conciseReviewCommentBody(outcome reviewOutcome) string {
	positives := normalizeReviewCommentPoints(outcome.Positives, 3)
	if len(positives) == 0 {
		if reviewOutcomeAllowsAutoMerge(outcome) || (strings.EqualFold(outcome.Status, "clean") && len(outcome.Findings) == 0) {
			positives = []string{"No material issues found."}
		} else if summary := cleanReviewCommentPoint(outcome.Summary); summary != "" {
			positives = []string{summary}
		} else {
			positives = []string{"Review completed."}
		}
	}

	negatives := reviewFindingPoints(outcome.Findings, 6)
	if len(negatives) == 0 {
		if strings.EqualFold(outcome.Status, "blocked") && strings.TrimSpace(outcome.Summary) != "" {
			negatives = []string{cleanReviewCommentPoint(outcome.Summary)}
		} else {
			negatives = []string{"No material issues found."}
		}
	}

	var b strings.Builder
	b.WriteString("**Positive**\n")
	writeReviewCommentBullets(&b, positives)
	b.WriteString("\n**Negative**\n")
	writeReviewCommentBullets(&b, negatives)
	return strings.TrimSpace(b.String())
}

func writeReviewCommentBullets(b *strings.Builder, points []string) {
	for _, point := range points {
		point = cleanReviewCommentPoint(point)
		if point == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(point)
		b.WriteString("\n")
	}
}

func normalizeReviewCommentPoints(points []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, min(len(points), limit))
	for _, point := range points {
		point = cleanReviewCommentPoint(point)
		if point == "" {
			continue
		}
		out = append(out, point)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func reviewFindingPoints(findings []reviewFinding, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, min(len(findings), limit))
	for _, finding := range findings {
		point := cleanReviewCommentPoint(reviewFindingPoint(finding))
		if point == "" {
			continue
		}
		out = append(out, point)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func reviewFindingPoint(finding reviewFinding) string {
	title := cleanReviewCommentPoint(finding.Title)
	if title == "" {
		return ""
	}
	severity := strings.TrimSpace(finding.Severity)
	if severity == "" {
		severity = "Finding"
	}
	location := strings.TrimSpace(finding.Path)
	if location != "" && finding.Line > 0 {
		location = fmt.Sprintf("%s:%d", location, finding.Line)
	}
	if location == "" {
		return fmt.Sprintf("[%s] %s", severity, title)
	}
	return fmt.Sprintf("[%s] %s - %s", severity, location, title)
}

func cleanReviewCommentPoint(point string) string {
	point = strings.TrimSpace(point)
	point = strings.TrimLeft(point, "-* \t")
	point = strings.Join(strings.Fields(point), " ")
	const maxPointChars = 220
	if len(point) <= maxPointChars {
		return point
	}
	return strings.TrimSpace(point[:maxPointChars-3]) + "..."
}

const maxReviewCommentBodyChars = 60000

func truncateReviewCommentBody(body string) string {
	body = strings.TrimSpace(body)
	if len(body) <= maxReviewCommentBodyChars {
		return body
	}
	return strings.TrimSpace(body[:maxReviewCommentBodyChars]) + "\n\n...(truncated)"
}

func reviewSummaryBodyPath(runDir string, prNumber int) string {
	if prNumber > 0 {
		return filepath.Join(runDir, fmt.Sprintf("review-summary-pr-%d.md", prNumber))
	}
	return filepath.Join(runDir, "review-summary.md")
}

var fencedJSONBlockPattern = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

func parseReviewOutcome(text string) (reviewOutcome, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return reviewOutcome{}, false
	}
	matches := fencedJSONBlockPattern.FindAllStringSubmatch(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		if len(matches[i]) < 2 {
			continue
		}
		if outcome, ok := parseReviewOutcomeJSON(matches[i][1]); ok {
			return outcome, true
		}
	}
	if start := strings.LastIndex(text, "{"); start >= 0 {
		if outcome, ok := parseReviewOutcomeJSON(text[start:]); ok {
			return outcome, true
		}
	}
	return reviewOutcome{}, false
}

func parseReviewOutcomeJSON(raw string) (reviewOutcome, bool) {
	var outcome reviewOutcome
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &outcome); err != nil {
		return reviewOutcome{}, false
	}
	if strings.TrimSpace(outcome.Status) == "" && !outcome.MergeReady && strings.TrimSpace(outcome.Summary) == "" && len(outcome.Positives) == 0 && len(outcome.Findings) == 0 {
		return reviewOutcome{}, false
	}
	outcome.Status = strings.ToLower(strings.TrimSpace(outcome.Status))
	outcome.Positives = normalizeReviewCommentPoints(outcome.Positives, 3)
	return outcome, true
}

func stripReviewOutcomeJSON(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	matches := fencedJSONBlockPattern.FindAllStringSubmatchIndex(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		if len(match) < 4 {
			continue
		}
		if _, ok := parseReviewOutcomeJSON(text[match[2]:match[3]]); !ok {
			continue
		}
		return strings.TrimSpace(text[:match[0]] + text[match[1]:])
	}
	return text
}

func reviewOutcomeAllowsAutoMerge(outcome reviewOutcome) bool {
	if !outcome.MergeReady {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(outcome.Status)) {
	case "clean", "merge-ready", "merge_ready":
	default:
		return false
	}
	for _, finding := range outcome.Findings {
		switch strings.ToLower(strings.TrimSpace(finding.Severity)) {
		case "critical", "high", "medium":
			return false
		}
	}
	return true
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
			"--json", "number,title,body,url,state,isDraft,baseRefName,headRefName,headRefOid,author",
		},
	}
}

func prReviewIssueCommentsCommand(repoDir string, number int) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"api", fmt.Sprintf("repos/:owner/:repo/issues/%d/comments", number), "--paginate"},
	}
}

func prReviewReviewCommentsCommand(repoDir string, number int) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"api", fmt.Sprintf("repos/:owner/:repo/pulls/%d/comments", number), "--paginate"},
	}
}

func prReviewReviewsCommand(repoDir string, number int) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: []string{"api", fmt.Sprintf("repos/:owner/:repo/pulls/%d/reviews", number), "--paginate"},
	}
}

func prReviewSummaryCommand(repoDir, selector, targetRepo, bodyFile string) execx.Command {
	args := []string{"pr", "review", selector, "--comment", "--body-file", bodyFile}
	args = appendGHRepoArg(args, targetRepo)
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: args,
	}
}

func prReviewSummaryFallbackCommand(repoDir, selector, targetRepo string, prNumber int, bodyFile string) execx.Command {
	targetRepo = strings.TrimSpace(targetRepo)
	if targetRepo != "" {
		if prNumber <= 0 {
			prNumber, _ = strconv.Atoi(strings.TrimSpace(selector))
		}
		if prNumber > 0 {
			return execx.Command{
				Dir:  repoDir,
				Name: "gh",
				Args: []string{
					"api",
					fmt.Sprintf("repos/%s/issues/%d/comments", targetRepo, prNumber),
					"-F",
					"body=@" + bodyFile,
				},
			}
		}
	}
	args := []string{"pr", "comment", selector, "--body-file", bodyFile}
	args = appendGHRepoArg(args, targetRepo)
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: args,
	}
}

func prMergeAutoCommand(repoDir, selector, targetRepo, method, headRefOID string) execx.Command {
	args := []string{"pr", "merge", selector, "--auto", "--match-head-commit", headRefOID}
	switch reviewMergeMethod(method) {
	case "merge":
		args = append(args, "--merge")
	case "rebase":
		args = append(args, "--rebase")
	default:
		args = append(args, "--squash")
	}
	args = appendGHRepoArg(args, targetRepo)
	return execx.Command{
		Dir:  repoDir,
		Name: "gh",
		Args: args,
	}
}

func appendGHRepoArg(args []string, targetRepo string) []string {
	targetRepo = strings.TrimSpace(targetRepo)
	if targetRepo == "" {
		return args
	}
	return append(args, "--repo", targetRepo)
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

func (h Harness) runPRChecksWatch(ctx context.Context, repoDir, prURL string) (execx.Result, error) {
	return h.runPRChecksWatchCommand(ctx, prChecksCommand(repoDir, prURL))
}

func (h Harness) runPRChecksAnyWatch(ctx context.Context, repoDir, prURL string) (execx.Result, error) {
	return h.runPRChecksWatchCommand(ctx, prChecksAnyCommand(repoDir, prURL))
}

func (h Harness) runPRChecksWatchCommand(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	timeout := h.prChecksWatchTimeout()
	if timeout <= 0 {
		return h.runCommand(ctx, "checks", cmd)
	}
	watchCtx, cancel := context.WithTimeoutCause(ctx, timeout, errPRChecksWatchTimeout)
	defer cancel()
	res, err := h.runCommand(watchCtx, "checks", cmd)
	if errors.Is(context.Cause(watchCtx), errPRChecksWatchTimeout) {
		return res, errPRChecksWatchTimeout
	}
	return res, err
}

func (h Harness) prChecksWatchTimeout() time.Duration {
	if h.PRChecksWatchTimeout < 0 {
		return 0
	}
	if h.PRChecksWatchTimeout > 0 {
		return h.PRChecksWatchTimeout
	}
	return defaultPRChecksWatchTimeout
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

func isPRCreatePermissionDenied(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	if isGitHubAuthFailure(res, err) {
		return true
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	if !strings.Contains(text, "createpullrequest") {
		return false
	}
	return strings.Contains(text, "correct permissions") ||
		strings.Contains(text, "resource not accessible by integration") ||
		strings.Contains(text, "permission") ||
		strings.Contains(text, "forbidden")
}

func isGitHubAuthFailure(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "requires authentication") ||
		strings.Contains(text, "try authenticating with") ||
		strings.Contains(text, "gh auth login") ||
		strings.Contains(text, "bad credentials") ||
		strings.Contains(text, "http 401")
}

func shouldRetryPRCreate(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	transientMarkers := []string{
		"http 500",
		"http 502",
		"http 503",
		"http 504",
		"we couldn't respond to your request in time",
		"try resubmitting your request",
		"context deadline exceeded",
		"client.timeout exceeded",
		"tls handshake timeout",
		"i/o timeout",
		"connection reset by peer",
		"econnreset",
		"etimedout",
		"failed to connect",
		"could not connect to server",
		"temporary failure in name resolution",
	}
	for _, marker := range transientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isTransientGitHubCLIError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	transientMarkers := []string{
		"githubstatus.com",
		"http 500",
		"http 502",
		"http 503",
		"http 504",
		"we couldn't respond to your request in time",
		"try resubmitting your request",
		"context deadline exceeded",
		"client.timeout exceeded",
		"tls handshake timeout",
		"i/o timeout",
		"connection reset by peer",
		"econnreset",
		"etimedout",
		"failed to connect",
		"could not connect to server",
		"temporary failure in name resolution",
		"failed to lookup address information",
		"error connecting to",
		"check your internet connection",
	}
	for _, marker := range transientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func shouldTreatReusedBranchPRLookupFailureAsNonFatal(err error) bool {
	return errors.Is(err, errTransientPRLookup) || shouldRetryPRCreate(execx.Result{}, err)
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

func normalizeFailureFollowUpTargeting(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if !isMoltenHubFailureFollowUpConfig(*cfg) {
		return
	}
	cfg.BaseBranch = ""
	cfg.TargetSubdir = "."
}

func isMoltenHubFailureFollowUpConfig(cfg config.Config) bool {
	if !strings.Contains(cfg.Prompt, failurefollowup.RequiredPrompt) {
		return false
	}
	for _, repoURL := range cfg.RepoList() {
		if isMoltenHubCodeRepository(repoURL) {
			return true
		}
	}
	return false
}

func isMoltenHubCodeRepository(repoURL string) bool {
	ref, ok := parseGitHubRepoRef(repoURL)
	if !ok {
		return false
	}
	return strings.EqualFold(ref.owner, config.DefaultRepositoryOwner) && isMoltenHubCodeRepositoryName(ref.name)
}

func isMoltenHubCodeRepositoryName(name string) bool {
	return strings.EqualFold(name, config.DefaultRepositoryName) ||
		strings.EqualFold(name, "moltenhub-code")
}

func isGitHubSSHRemoteURL(rawURL string) bool {
	rawURL = strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(rawURL, "git@github.com:") || strings.HasPrefix(rawURL, "ssh://git@github.com/")
}

func (r gitHubRepoRef) withOwner(owner string) (string, bool) {
	return r.withOwnerAndName(owner, r.name)
}

func (r gitHubRepoRef) withOwnerAndName(owner, name string) (string, bool) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" ||
		(strings.EqualFold(owner, r.owner) && strings.EqualFold(name, r.name)) {
		return "", false
	}
	if strings.ContainsAny(owner, " \t\r\n") || strings.Contains(owner, "/") ||
		strings.ContainsAny(name, " \t\r\n") || strings.Contains(name, "/") {
		return "", false
	}

	repoName := name
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

	type candidate struct {
		owner string
		name  string
	}
	candidates := make([]candidate, 0, len(ownerHints)+1)
	seen := make(map[string]struct{}, len(ownerHints)+2)
	seen[strings.ToLower(strings.TrimSpace(ref.owner))+"/"+strings.ToLower(strings.TrimSpace(ref.name))] = struct{}{}
	appendCandidate := func(owner, name string) {
		owner = strings.TrimSpace(owner)
		name = strings.TrimSpace(name)
		if owner == "" || name == "" {
			return
		}
		key := strings.ToLower(owner) + "/" + strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate{owner: owner, name: name})
	}

	defaultRef, hasDefaultRef := parseGitHubRepoRef(config.DefaultRepositoryURL)
	for _, owner := range ownerHints {
		name := ref.name
		if hasDefaultRef && strings.EqualFold(owner, defaultRef.owner) && isMoltenHubCodeRepositoryName(ref.name) {
			name = defaultRef.name
		}
		appendCandidate(owner, name)
	}
	if hasDefaultRef && isMoltenHubCodeRepositoryName(ref.name) {
		appendCandidate(defaultRef.owner, defaultRef.name)
	}

	for _, candidate := range candidates {
		candidateURL, ok := ref.withOwnerAndName(candidate.owner, candidate.name)
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
	normalized := cloneFallbackBranchName(baseBranch)
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
	_, err := h.runCodexCapture(ctx, runtime, targetDir, prompt, opts, agentsPath, responseMode, agentInvocationLogMetadata{})
	return err
}

func (h Harness) runCodexCapture(
	ctx context.Context,
	runtime agentruntime.Runtime,
	targetDir string,
	prompt string,
	opts codexRunOptions,
	agentsPath string,
	responseMode string,
	invocation agentInvocationLogMetadata,
) (execx.Result, error) {
	finalPrompt := withBackpressurePrompt(prompt, nil)
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
		targetAgentsPath, targetAgentsCleanup, ensureErr := selectAgentsPromptFile(targetDir, stagedAgentsPath)
		if ensureErr != nil {
			h.logf(
				"stage=workspace status=warn action=select_agents_for_agent target=%s source=%s err=%q",
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
		return execx.Result{}, err
	} else {
		finalPrompt = promptWithResponseMode
	}

	invocation = invocation.withRuntimeDefaults(runtime)
	res, err := h.runCodexWithHeartbeat(ctx, runtime, targetDir, finalPrompt, opts, "", invocation)
	if shouldRetryCodexWithoutSandbox(res, err) {
		if h.agentRetryInvariant != nil {
			if invariantErr := h.agentRetryInvariant(ctx); invariantErr != nil {
				err = fmt.Errorf("verify required branch before agent sandbox retry: %w", invariantErr)
			} else {
				res, err = h.retryCodexWithoutSandbox(ctx, runtime, targetDir, finalPrompt, opts, invocation)
			}
		} else {
			res, err = h.retryCodexWithoutSandbox(ctx, runtime, targetDir, finalPrompt, opts, invocation)
		}
	}
	if err != nil {
		agentStage := runtimeLogStage(runtime)
		h.logf("stage=%s status=error%s err=%q", agentStage, invocation.logFieldsSuffix(), err)
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		h.logf(
			"stage=workspace status=warn action=cleanup_agents_for_agent target=%s err=%q",
			targetDir,
			cleanupErr,
		)
	}
	return res, err
}

func (h Harness) retryCodexWithoutSandbox(
	ctx context.Context,
	runtime agentruntime.Runtime,
	targetDir string,
	prompt string,
	opts codexRunOptions,
	invocation agentInvocationLogMetadata,
) (execx.Result, error) {
	agentStage := runtimeLogStage(runtime)
	h.logf(
		"stage=%s status=warn action=retry_without_sandbox reason=%q%s",
		agentStage,
		"detected bubblewrap namespace sandbox failure; retrying with danger-full-access",
		invocation.logFieldsSuffix(),
	)
	return h.runCodexWithHeartbeat(ctx, runtime, targetDir, prompt, opts, "danger-full-access", invocation)
}

func agentOutputClaimsFileChanges(res execx.Result) bool {
	text := strings.ReplaceAll(res.Stdout, "\r\n", "\n")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "\ndiff --git a/") || strings.HasPrefix(lower, "diff --git a/") {
		return true
	}
	for _, line := range splitOutputLines(trimmed) {
		line = strings.TrimSpace(line)
		lineLower := strings.ToLower(line)
		if strings.HasPrefix(lineLower, "changed [") || strings.HasPrefix(lineLower, "changed `") {
			return true
		}
	}
	return false
}

func requiresConcreteNoChangeEvidence(prompt string) bool {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
		return false
	}
	return strings.Contains(text, "fix the underlying moltenhub code application issue") &&
		strings.Contains(text, "only return a no-op") &&
		(strings.Contains(text, "no-change") || strings.Contains(text, "failed task"))
}

func requiresVerifiedNoChangePRURL(prompt string) bool {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
		return false
	}
	if strings.Contains(text, "existing pull request") ||
		strings.Contains(text, "same pr branch") ||
		strings.Contains(text, "head branch to update") {
		return true
	}
	return strings.Contains(text, "pull request:") && strings.Contains(text, "head branch")
}

func pullRequestURLFromPrompt(prompt string) string {
	matches := regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+`).FindAllString(prompt, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.TrimSpace(matches[0])
}

func agentOutputCitesConcreteNoChangeEvidence(res execx.Result, prompt string) bool {
	if requiresConcreteNoChangeEvidence(prompt) {
		return agentOutputCitesMoltenHubCodeNoChangeEvidence(res)
	}
	return agentOutputCitesGeneralNoChangeEvidence(res)
}

func agentOutputCitesMoltenHubCodeNoChangeEvidence(res execx.Result) bool {
	text := strings.ToLower(strings.TrimSpace(res.Stdout))
	if !agentOutputHasNoChangeClaim(text) {
		return false
	}

	evidenceMarkers := []string{
		"internal/", "cmd/", "library/", "na.hub.molten.bot.openapi.yaml",
	}
	return containsAnySubstring(text, evidenceMarkers)
}

func agentOutputCitesGeneralNoChangeEvidence(res execx.Result) bool {
	text := strings.ToLower(strings.TrimSpace(res.Stdout))
	if !agentOutputHasNoChangeClaim(text) {
		return false
	}
	return agentOutputHasConcreteFileEvidence(text)
}

func agentOutputHasNoChangeClaim(text string) bool {
	if text == "" || strings.Contains(text, "failure:") || strings.Contains(text, "error details:") {
		return false
	}

	noOpMarkers := []string{
		"no-op",
		"no op",
		"zero commits",
		"0 commits",
		"no repo diff",
		"no repository diff",
		"no tracked file changes",
		"git diff empty",
		"no file changes required",
		"no repository changes required",
		"no changes needed",
		"no changes required",
		"no deletion needed",
		"no deletion required",
		"no removal needed",
		"no removal required",
		"nothing to delete",
		"nothing to remove",
		"already implemented",
		"already present",
		"already satisfied",
		"requested state already",
	}
	return containsAnySubstring(text, noOpMarkers)
}

func agentOutputHasConcreteFileEvidence(text string) bool {
	evidencePatterns := []*regexp.Regexp{
		regexp.MustCompile(`\[[^\]\n]+\]\([^)]+:[0-9]+\)`),
		regexp.MustCompile("`[^`\\n]+\\.[a-z0-9]+`"),
		regexp.MustCompile(`(^|[\s(/])(?:src|app|pages|routes|components|styles|public|worker|internal|cmd|library|testdata)/[^\s)]+`),
		regexp.MustCompile(`(^|[\s(/])(?:package\.json|go\.mod|vite\.config\.[jt]s|next\.config\.[jt]s|tsconfig\.json|index\.html)(:[0-9]+)?`),
	}
	for _, pattern := range evidencePatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

func containsAnySubstring(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func newAgentInvocationLogMetadata(
	runtime agentruntime.Runtime,
	mode string,
	attempt int,
	repo string,
	repoDir string,
	target string,
) agentInvocationLogMetadata {
	mode = strings.TrimSpace(mode)
	return agentInvocationLogMetadata{
		RunID:   agentInvocationRunID(mode, repoDir, attempt),
		Harness: strings.TrimSpace(runtime.Harness),
		Mode:    mode,
		Attempt: attempt,
		Repo:    pickFirstNonEmpty(strings.TrimSpace(repo), strings.TrimSpace(repoDir)),
		RepoDir: strings.TrimSpace(repoDir),
		Target:  strings.TrimSpace(target),
	}
}

func initialAgentInvocationRepoMetadata(repos []repoWorkspace) (string, string) {
	switch len(repos) {
	case 0:
		return "", ""
	case 1:
		repoDir := strings.TrimSpace(repos[0].RelDir)
		return repoDir, repoDir
	default:
		return "multi-repo", "."
	}
}

func agentInvocationRunID(mode, repoDir string, attempt int) string {
	mode = sanitizeAgentInvocationIDPart(pickFirstNonEmpty(mode, "implementation"))
	repoDir = sanitizeAgentInvocationIDPart(repoDir)
	parts := []string{"agent", mode}
	if repoDir != "" && mode != "implementation" {
		parts = append(parts, repoDir)
	}
	if attempt > 0 {
		parts = append(parts, strconv.Itoa(attempt))
	}
	return strings.Join(parts, "-")
}

func sanitizeAgentInvocationIDPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (m agentInvocationLogMetadata) withRuntimeDefaults(runtime agentruntime.Runtime) agentInvocationLogMetadata {
	if strings.TrimSpace(m.RunID) == "" {
		return m
	}
	if strings.TrimSpace(m.Harness) == "" {
		m.Harness = strings.TrimSpace(runtime.Harness)
	}
	if strings.TrimSpace(m.Mode) == "" {
		m.Mode = "implementation"
	}
	if m.Attempt <= 0 {
		m.Attempt = 1
	}
	if strings.TrimSpace(m.Repo) == "" {
		m.Repo = strings.TrimSpace(m.RepoDir)
	}
	return m
}

func (m agentInvocationLogMetadata) logFieldsSuffix() string {
	if strings.TrimSpace(m.RunID) == "" {
		return ""
	}
	fields := []string{
		logKV("agent_run_id", m.RunID),
		logKV("agent_harness", m.Harness),
		logKV("mode", m.Mode),
	}
	if m.Attempt > 0 {
		fields = append(fields, logKV("attempt", strconv.Itoa(m.Attempt)))
	}
	fields = append(fields,
		logKV("repo", m.Repo),
		logKV("repo_dir", m.RepoDir),
		logKV("target", m.Target),
	)
	fields = compactNonEmptyStrings(fields)
	if len(fields) == 0 {
		return ""
	}
	return " " + strings.Join(fields, " ")
}

func logKV(key, value string) string {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return ""
	}
	if isSimpleLogKVValue(value) {
		return key + "=" + value
	}
	return key + "=" + strconv.Quote(value)
}

func isSimpleLogKVValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@':
		default:
			return false
		}
	}
	return true
}

func compactNonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
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

func selectAgentsPromptFile(targetDir, agentsPath string) (string, func() error, error) {
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

	return agentsPath, func() error { return nil }, nil
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
	invocation agentInvocationLogMetadata,
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
	heartbeatInterval := h.AgentHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case run := <-done:
			if failed, detail := codexReportedFailure(run.res); failed {
				detail = codexFailureDetailWithErrorDetails(run.res, detail)
				if isImplementationTargetFailure(detail) {
					return run.res, fmt.Errorf("%s reported failure: %s", agentStage, detail)
				}
				if isNonFatalValidationToolingFailure(detail, run.res) {
					h.logf(
						"stage=%s status=warn action=validation_tooling_unavailable detail=%q%s",
						agentStage,
						detail,
						invocation.logFieldsSuffix(),
					)
					return run.res, nil
				}
				if isRecoveredTransientRegistryLookupFailure(detail, run.res) {
					h.logf(
						"stage=%s status=warn action=recovered_transient_registry_lookup detail=%q%s",
						agentStage,
						detail,
						invocation.logFieldsSuffix(),
					)
					return run.res, nil
				}
				if isNonFatalHubSnapshotRefreshFailure(detail, run.res) {
					h.logf(
						"stage=%s status=warn action=hub_snapshot_refresh_unavailable detail=%q%s",
						agentStage,
						detail,
						invocation.logFieldsSuffix(),
					)
					return run.res, nil
				}
				if isNonFatalRemoteDeploymentAuthFailure(detail, run.res) {
					h.logf(
						"stage=%s status=warn action=remote_deployment_auth_unavailable detail=%q%s",
						agentStage,
						detail,
						invocation.logFieldsSuffix(),
					)
					return run.res, nil
				}
				return run.res, fmt.Errorf("%s reported failure: %s", agentStage, detail)
			}
			if run.err != nil {
				if isCodexExecutionTransportFailure(run.res, run.err) {
					return run.res, run.err
				}
				if isNonFatalValidationToolingFailure("", run.res) {
					h.logf(
						"stage=%s status=warn action=validation_tooling_unavailable detail=%q%s",
						agentStage,
						strings.TrimSpace(strings.Join([]string{run.res.Stdout, run.res.Stderr}, "\n")),
						invocation.logFieldsSuffix(),
					)
					return run.res, nil
				}
				return run.res, run.err
			}
			return run.res, nil
		case <-ticker.C:
			h.logf("stage=%s status=running elapsed_s=%d%s", agentStage, int(time.Since(start).Seconds()), invocation.logFieldsSuffix())
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
	lowerLine = strings.Trim(strings.TrimSpace(lowerLine), "`\"'")
	for _, marker := range []string{
		"no implementation target given",
		"no implementation target was given",
		"no implementation target provided",
		"no implementation target was provided",
		"no implementation task given",
		"no implementation task was given",
		"no implementation task provided",
		"no implementation task was provided",
		"no task given",
		"no task was given",
		"no task provided",
		"no task was provided",
	} {
		if strings.Contains(lowerLine, marker) {
			return true
		}
	}
	if len(lowerLine) <= 160 {
		for _, marker := range []string{
			"send implementation task",
			"send target bug/feature/change",
			"waiting for task",
			"provide implementation task",
			"provide an implementation task",
			"send actual change request",
			"send actual repo task",
			"need actual change request",
			"need actual repo task",
		} {
			if strings.Contains(lowerLine, marker) {
				return true
			}
		}
		if containsStandaloneSentence(lowerLine, "send task") {
			return true
		}
	}
	return false
}

func containsStandaloneSentence(lowerLine, sentence string) bool {
	for _, part := range strings.FieldsFunc(lowerLine, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	}) {
		if strings.TrimSpace(part) == sentence {
			return true
		}
	}
	return false
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
	text := strings.ToLower(strings.TrimSpace(detail))
	if text == "" {
		text = strings.ToLower(strings.TrimSpace(strings.Join([]string{
			res.Stdout,
			res.Stderr,
		}, "\n")))
	}
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
		strings.Contains(text, "missing deps") ||
		strings.Contains(text, "missing dependencies") ||
		strings.Contains(text, "missing dependency") ||
		strings.Contains(text, "shim missing") ||
		strings.Contains(text, "tool missing") ||
		strings.Contains(text, "runner missing") ||
		strings.Contains(text, "executable missing") ||
		strings.Contains(text, "binary missing") ||
		strings.Contains(text, "command missing") ||
		strings.Contains(text, "`uv` missing") ||
		strings.Contains(text, "uv missing") ||
		strings.Contains(text, "node_modules missing")
	if !missingTooling {
		return false
	}
	if strings.Contains(text, "smoke command") {
		return containsAny(text, []string{
			"fallback succeeded",
			"fallback passed",
			"fallback smoke check succeeded",
			"fallback smoke check passed",
		})
	}
	if validationUnavailable {
		return true
	}

	validationCommandMarkers := []string{
		"smoke command",
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

func isImplementationTargetFailure(detail string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(detail)), "agent did not identify an implementation target")
}

func isCodexExecutionTransportFailure(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		err.Error(),
		res.Stdout,
		res.Stderr,
	}, "\n")))
	if text == "" {
		return false
	}
	transportMarkers := []string{
		"backend-api/codex",
		"responses_websocket",
		"stream disconnected before completion",
		"failed to connect to websocket",
		"failed to lookup address information",
		"temporary failure in name resolution",
	}
	return containsAny(text, transportMarkers)
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

func isNonFatalHubSnapshotRefreshFailure(detail string, res execx.Result) bool {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		detail,
		res.Stdout,
		res.Stderr,
	}, "\n")))
	if text == "" {
		return false
	}
	snapshotRefreshMarkers := []string{
		"non-fatal prebuild snapshot refresh warning",
		"prebuild hub snapshot refresh could not fetch live snapshot",
		"hub snapshot refresh unavailable during build pre-step",
		"hub snapshot refresh could not fetch live snapshot",
		"hub snapshot refresh unavailable",
	}
	existingSnapshotMarkers := []string{
		"build kept existing `hub-snapshot.json` and completed",
		"build continued using existing snapshot",
		"build still completed",
		"using existing snapshot",
	}
	if !containsAny(text, snapshotRefreshMarkers) {
		return false
	}
	if !strings.Contains(text, "moltenhub_admin_snapshot_key is not configured") {
		return false
	}
	if !containsAny(text, existingSnapshotMarkers) {
		return false
	}
	return true
}

func isNonFatalRemoteDeploymentAuthFailure(detail string, res execx.Result) bool {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		detail,
		res.Stdout,
		res.Stderr,
	}, "\n")))
	if text == "" {
		return false
	}
	remoteCheckMarkers := []string{
		"remote check",
		"reported failed remotely",
		"still reported failed remotely",
		"cloudflare workers builds check",
		"cloudflare build",
		"cloudflare builds",
	}
	authUnavailableMarkers := []string{
		"require cloudflare auth",
		"requires cloudflare auth",
		"not authenticated",
		"cloudflare_api_token",
		"cloudflare api token",
		"wrangler api token",
		"auth unavailable",
	}
	localPassMarkers := []string{
		"local wrangler dry-run deploy passes",
		"wrangler dry-run deploy passes",
		"local dry-run deploy passes",
		"dry-run deploy passes",
		"local validation passed",
		"local checks passed",
		"github ci build passed",
		"github ci passed",
		"no repo-side failure reproduced",
		"no repository-side failure reproduced",
	}
	if !containsAny(text, remoteCheckMarkers) {
		return false
	}
	if !containsAny(text, authUnavailableMarkers) {
		return false
	}
	if !containsAny(text, localPassMarkers) {
		return false
	}
	return true
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
	for i, trimmed := range nonEmpty[start:] {
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "failure:") {
			if isBareFailureHeadingAfterCompletion(trimmed, nonEmpty[:start+i]) {
				continue
			}
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

func isBareFailureHeadingAfterCompletion(line string, previous []string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.EqualFold(trimmed, "failure:") {
		return false
	}
	for _, prev := range previous {
		switch strings.ToLower(strings.TrimSpace(prev)) {
		case "done.", "success:", "changed:", "verification:", "verified:":
			return true
		}
	}
	return false
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

func isGitHubUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "http 401") ||
		strings.Contains(text, "requires authentication") ||
		strings.Contains(text, "bad credentials")
}

func commandWithoutGitHubTokenEnv(cmd execx.Command) execx.Command {
	cmd.Env = githubTokenSanitizedEnv()
	return cmd
}

func githubTokenSanitizedEnv() []string {
	return environWithoutKeys(os.Environ(), "GH_TOKEN", "GITHUB_TOKEN")
}

func prepareAgentIOEnv(runDir string, environ []string) ([]string, error) {
	runDir = strings.TrimSpace(runDir)
	if runDir == "" {
		return nil, fmt.Errorf("run directory is required for agent io")
	}

	root := filepath.Join(runDir, ".moltenhub-agent-io")
	homeDir := filepath.Join(root, "home")
	tmpDir := filepath.Join(root, "tmp")
	configDir := filepath.Join(root, "config")
	codexConfigDir := filepath.Join(configDir, "codex")
	claudeConfigDir := filepath.Join(configDir, "claude")
	cacheDir := filepath.Join(root, "cache")
	stateDir := filepath.Join(root, "state")
	logDir := filepath.Join(root, "log")
	runtimeDir := filepath.Join(root, "runtime")
	for _, dir := range []string{homeDir, tmpDir, configDir, codexConfigDir, claudeConfigDir, cacheDir, stateDir, logDir, runtimeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("prepare agent io dir %s: %w", dir, err)
		}
	}

	if len(environ) == 0 {
		environ = os.Environ()
	}
	if err := seedAgentConfigDir(agentConfigSource(environ, "CODEX_HOME", filepath.Join(".codex")), codexConfigDir); err != nil {
		return nil, fmt.Errorf("seed codex config dir: %w", err)
	}
	if err := seedAgentConfigDir(agentConfigSource(environ, "CLAUDE_CONFIG_DIR", filepath.Join(".claude")), claudeConfigDir); err != nil {
		return nil, fmt.Errorf("seed claude config dir: %w", err)
	}
	return environWithOverrides(environ,
		"MOLTENHUB_AGENT_IO_DIR="+root,
		"HOME="+homeDir,
		"TMPDIR="+tmpDir,
		"TEMP="+tmpDir,
		"TMP="+tmpDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_CACHE_HOME="+cacheDir,
		"XDG_STATE_HOME="+stateDir,
		"XDG_RUNTIME_DIR="+runtimeDir,
		"CODEX_HOME="+codexConfigDir,
		"CLAUDE_CONFIG_DIR="+claudeConfigDir,
		"npm_config_cache="+filepath.Join(cacheDir, "npm"),
		"YARN_CACHE_FOLDER="+filepath.Join(cacheDir, "yarn"),
		"PNPM_HOME="+filepath.Join(cacheDir, "pnpm"),
		"PIP_CACHE_DIR="+filepath.Join(cacheDir, "pip"),
		"UV_CACHE_DIR="+filepath.Join(cacheDir, "uv"),
		"GOCACHE="+filepath.Join(cacheDir, "go-build"),
		"GOMODCACHE="+filepath.Join(cacheDir, "go-mod"),
		"CARGO_HOME="+filepath.Join(cacheDir, "cargo"),
		"GRADLE_USER_HOME="+filepath.Join(cacheDir, "gradle"),
		"PLAYWRIGHT_BROWSERS_PATH="+filepath.Join(cacheDir, "ms-playwright"),
		"LOGDIR="+logDir,
	), nil
}

func agentConfigSource(environ []string, configEnvKey, homeRel string) string {
	if configured, ok := environValue(environ, configEnvKey); ok && strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured)
	}
	if home, ok := environValue(environ, "HOME"); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(strings.TrimSpace(home), homeRel)
	}
	return ""
}

func seedAgentConfigDir(sourcePath, targetDir string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	targetDir = strings.TrimSpace(targetDir)
	if sourcePath == "" || targetDir == "" || pathContains(targetDir, sourcePath) || pathContains(sourcePath, targetDir) {
		return nil
	}

	info, err := os.Lstat(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if !info.IsDir() {
		return copyFile(sourcePath, filepath.Join(targetDir, filepath.Base(sourcePath)), info.Mode().Perm())
	}

	entries, err := os.ReadDir(sourcePath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := copyFile(filepath.Join(sourcePath, entry.Name()), filepath.Join(targetDir, entry.Name()), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(sourcePath, targetPath string, mode os.FileMode) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(targetPath, content, mode)
}

func pathContains(parent, child string) bool {
	parent = filepath.Clean(strings.TrimSpace(parent))
	child = filepath.Clean(strings.TrimSpace(child))
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func environValue(environ []string, key string) (string, bool) {
	var (
		found bool
		value string
	)
	for _, entry := range environ {
		name, entryValue, ok := strings.Cut(entry, "=")
		if ok && name == key {
			value = entryValue
			found = true
		}
	}
	return value, found
}

func environWithOverrides(environ []string, overrides ...string) []string {
	overrideKeys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			if name = strings.TrimSpace(name); name != "" {
				overrideKeys[name] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(environ)+len(overrides))
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := overrideKeys[name]; replaced {
				continue
			}
		}
		out = append(out, entry)
	}
	for _, entry := range overrides {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func environWithoutKeys(environ []string, keys ...string) []string {
	if len(environ) == 0 || len(keys) == 0 {
		return append([]string(nil), environ...)
	}
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			blocked[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			name = entry
		}
		if _, skip := blocked[name]; skip {
			continue
		}
		out = append(out, entry)
	}
	return out
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

func remoteDefaultBranchCommand(repoDir string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"ls-remote", "--symref", "origin", "HEAD"},
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
	args := []string{
		"-c",
		"user.name=" + bootstrapGitUserName,
		"-c",
		"user.email=" + bootstrapGitUserEmail,
		"commit",
		"--allow-empty",
	}
	args = append(args, commitMessageArgs(bootstrapMainCommitMessage)...)
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: args,
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

func cloneFallbackBranchName(branch string) string {
	branch = normalizeBranchRef(branch)
	owner, head, ok := strings.Cut(branch, ":")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(head) == "" {
		return branch
	}
	return strings.TrimSpace(head)
}

func branchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"switch", "-c", branch},
	}
}

func branchMoveCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"branch", "-m", branch},
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

func reviewFixDiffCommand(repoDir, beforeHead string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"diff", "--binary", strings.TrimSpace(beforeHead)}}
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
	args := append([]string{"commit"}, commitMessageArgs(msg)...)
	return execx.Command{Dir: repoDir, Name: "git", Args: args}
}

func commitMessageArgs(msg string) []string {
	args := []string{"-m", msg}
	if !strings.Contains(msg, moltenbotCoAuthorTrailer) {
		args = append(args, "-m", moltenbotCoAuthorTrailer)
	}
	return args
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
	baseRef := remoteBaseComparisonRef(baseBranch)
	if baseRef == "" {
		baseRef = "HEAD"
	}
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"rev-list", "--count", fmt.Sprintf("%s..HEAD", baseRef)},
	}
}

func remoteBaseComparisonRef(baseBranch string) string {
	baseBranch = strings.TrimSpace(baseBranch)
	baseBranch = strings.TrimPrefix(baseBranch, "refs/remotes/origin/")
	baseBranch = normalizeBranchRef(baseBranch)
	if baseBranch == "" {
		return ""
	}
	return remoteTrackingRef(baseBranch)
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

func mergeAbortCommand(repoDir string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"merge", "--abort"}}
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
		imageURL := githubFileImageURL(owner, repo, branch, file)
		b.WriteString(fmt.Sprintf("### %s\n\n![%s](%s)\n\n", file, file, imageURL))
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

func githubFileImageURL(owner, repo, branch, path string) string {
	return fmt.Sprintf(
		"https://github.com/%s/%s/blob/%s/%s?raw=1",
		url.PathEscape(owner),
		url.PathEscape(repo),
		escapePathSegments(branch),
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

func prStateViewCommand(repoDir, prURL string) execx.Command {
	selector := githubutil.PullRequestSelector(prURL)
	args := []string{"pr", "view", selector, "--json", "url,state,mergedAt,headRefName"}
	if repo := githubutil.PullRequestRepository(prURL); repo != "" {
		args = append(args, "--repo", repo)
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

	return failurefollowup.WithExecutionContract(withImplementationTargetGuard(base))
}

func withImplementationTargetGuard(prompt string) string {
	base := strings.TrimSpace(prompt)
	if base == "" {
		return ""
	}

	return fmt.Sprintf(
		"Agent input:\n%s\n\nTask packet handling:\nThe agent input above includes the requested repository work plus any local instructions. Treat non-empty product, bug, feature, or review text in it as the implementation target. If wording is terse, inspect the repository to identify the target area before asking follow-up. Do not answer that no implementation task was given when the agent input includes a requested repository change.",
		base,
	)
}

func remediationPrompt(basePrompt, prURL, checkSummary string, attempt int) string {
	ciFixPrompt := ciFixLibraryPrompt()
	return fmt.Sprintf(
		"%s\n\nRemediation round %d/%d.\nAn open PR already exists: %s\n\nPR CI/CD checks are failing right now.\nLatest check output:\n%s\n\nOriginal task context:\n%s",
		ciFixPrompt,
		attempt,
		maxPRCheckRemediationAttempts,
		prURL,
		checkSummary,
		strings.TrimSpace(basePrompt),
	)
}

func remediationPromptForRepo(basePrompt, repoPath, repoURL, prURL, checkSummary string, attempt int, multiRepo bool) string {
	if !multiRepo {
		return remediationPrompt(basePrompt, prURL, checkSummary, attempt)
	}
	ciFixPrompt := ciFixLibraryPrompt()
	return fmt.Sprintf(
		"%s\n\nRemediation round %d/%d.\nTarget repository workspace path: %s\nTarget repository remote: %s\nAn open PR already exists for this repository: %s\n\nPR CI/CD checks are failing right now for this repository.\nLatest check output:\n%s\n\nFocus remediation changes on this repository. If you also change other repositories, ensure each changed repository has its own branch and PR.\n\nOriginal task context:\n%s",
		ciFixPrompt,
		attempt,
		maxPRCheckRemediationAttempts,
		repoPath,
		repoURL,
		prURL,
		checkSummary,
		strings.TrimSpace(basePrompt),
	)
}

func baseSyncConflictPrompt(basePrompt, repoPath, repoURL, branch, baseBranch string, mergeRes execx.Result) string {
	mergePrompt := mergeMainLibraryPrompt()
	mergeSummary := summarizeCommandErrorDetail(mergeRes, maxGitErrorDetailChars)
	if mergeSummary == "" {
		mergeSummary = "git merge reported conflicts but did not provide output."
	}
	return fmt.Sprintf(
		"%s\n\nBase-branch sync conflict remediation.\nTarget repository workspace path: %s\nTarget repository remote: %s\nCurrent work branch: %s\nBase branch being merged: %s\n\nThe harness fetched the latest base branch and `git merge --no-edit FETCH_HEAD` reported conflicts before PR creation.\nMerge output:\n%s\n\nResolve the merge conflicts while preserving the original task work and the latest base-branch behavior. Do not discard unrelated work, reset history, force push, or change branches. After resolving conflicts, leave the repository ready for the harness to commit the merge resolution and retry the base sync.\n\nOriginal task context:\n%s",
		mergePrompt,
		strings.TrimSpace(repoPath),
		strings.TrimSpace(repoURL),
		strings.TrimSpace(branch),
		strings.TrimSpace(baseBranch),
		mergeSummary,
		strings.TrimSpace(basePrompt),
	)
}

func remoteBranchSyncConflictPrompt(basePrompt, repoPath, repoURL, branch, remote string, mergeRes execx.Result) string {
	mergePrompt := mergeMainLibraryPrompt()
	mergeSummary := summarizeCommandErrorDetail(mergeRes, maxGitErrorDetailChars)
	if mergeSummary == "" {
		mergeSummary = "git merge reported conflicts but did not provide output."
	}
	if strings.TrimSpace(remote) == "" {
		remote = publishRemoteOrigin
	}
	return fmt.Sprintf(
		"%s\n\nRemote-branch sync conflict remediation.\nTarget repository workspace path: %s\nTarget repository remote: %s\nCurrent work branch: %s\nRemote being merged: %s/%s\n\nThe harness committed the original task work, then `git push` was rejected because the remote branch had new commits. The harness fetched that branch and `git merge --no-edit FETCH_HEAD` reported conflicts before push retry.\nMerge output:\n%s\n\nResolve the merge conflicts while preserving the original task work and the latest remote-branch behavior. Do not discard unrelated work, reset history, force push, or change branches. After resolving conflicts, leave the repository ready for the harness to commit the merge resolution and retry the push.\n\nOriginal task context:\n%s",
		mergePrompt,
		strings.TrimSpace(repoPath),
		strings.TrimSpace(repoURL),
		strings.TrimSpace(branch),
		strings.TrimSpace(remote),
		strings.TrimSpace(branch),
		mergeSummary,
		strings.TrimSpace(basePrompt),
	)
}

func ciFixLibraryPrompt() string {
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err == nil {
		if task, ok := catalog.Task(ciFixLibraryTaskName); ok && strings.TrimSpace(task.Prompt) != "" {
			return strings.TrimSpace(task.Prompt)
		}
	}
	return "You are a senior software engineer fixing pull-request CI failures.\n\nFix the root cause of broken CI with the smallest coherent repository diff. Validate locally where possible. If CI, tests, or validation fail, report `Failure:` and `Error details:`."
}

func codeReviewLibraryPrompt() string {
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err == nil {
		if task, ok := catalog.Task(codeReviewLibraryTaskName); ok && strings.TrimSpace(task.Prompt) != "" {
			return strings.TrimSpace(task.Prompt)
		}
	}
	return "Perform a read-only pull request review. Prioritize correctness, security, regressions, and missing tests. Return a structured clean, findings, or blocked outcome."
}

func resolvePRCommentsLibraryPrompt() string {
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err == nil {
		if task, ok := catalog.Task(resolvePRCommentsLibraryTaskName); ok && strings.TrimSpace(task.Prompt) != "" {
			return strings.TrimSpace(task.Prompt)
		}
	}
	return "Modify the existing pull request branch to resolve the supplied review findings with the smallest coherent diff. Preserve unrelated work and validate the result."
}

func mergeMainLibraryPrompt() string {
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err == nil {
		if task, ok := catalog.Task(mergeMainLibraryTaskName); ok && strings.TrimSpace(task.Prompt) != "" {
			return strings.TrimSpace(task.Prompt)
		}
	}
	return "You are a senior software engineer resolving merge conflicts with the latest base branch. Preserve both sides' intended behavior, keep the diff scoped to conflict resolution, validate locally where possible, and report `Failure:` and `Error details:` if the conflict cannot be resolved safely."
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
		if !isUnsupportedPRChecksJSON(res, err) {
			return false, "", err
		}
		snapshot, parseErr := latestCheckSnapshotFromText(res.Stdout)
		if parseErr != nil {
			return false, "", err
		}
		return !snapshot.HasFailures && snapshot.AllPassing, snapshot.Summary, nil
	}

	snapshot, err := latestCheckSnapshot(res.Stdout)
	if err != nil {
		return false, "", err
	}
	return !snapshot.HasFailures && snapshot.AllPassing, snapshot.Summary, nil
}

func (h Harness) latestChecksAreAllPassing(ctx context.Context, repo repoWorkspace, requiredOnly bool) (bool, string, error) {
	res, err := h.runCommand(ctx, "checks", prChecksJSONCommand(repo.Dir, repo.PRURL, requiredOnly))
	if err != nil {
		if !isUnsupportedPRChecksJSON(res, err) {
			return false, "", err
		}
		snapshot, parseErr := latestCheckSnapshotFromText(res.Stdout)
		if parseErr != nil {
			return false, "", err
		}
		return snapshot.AllPassing, snapshot.Summary, nil
	}
	snapshot, err := latestCheckSnapshot(res.Stdout)
	if err != nil {
		return false, "", err
	}
	return snapshot.AllPassing, snapshot.Summary, nil
}

type latestChecksSnapshot struct {
	AllPassing  bool
	HasFailures bool
	Summary     string
}

func latestCheckSnapshot(raw string) (latestChecksSnapshot, error) {
	var checks []ghPRCheck
	if parseErr := json.Unmarshal([]byte(strings.TrimSpace(raw)), &checks); parseErr != nil {
		return latestChecksSnapshot{}, fmt.Errorf("decode checks snapshot: %w", parseErr)
	}
	if len(checks) == 0 {
		return latestChecksSnapshot{}, nil
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
		return latestChecksSnapshot{}, nil
	}
	return latestCheckSnapshotFromStates(latestByName), nil
}

func isUnsupportedPRChecksJSON(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "unknown flag: --json") &&
		strings.Contains(text, "gh pr checks")
}

func latestCheckSnapshotFromText(raw string) (latestChecksSnapshot, error) {
	lines := splitOutputLines(raw)
	if len(lines) == 0 {
		return latestChecksSnapshot{}, fmt.Errorf("parse checks text snapshot: no check output")
	}

	latestByName := make(map[string]latestCheckState)
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		bucket := strings.ToLower(strings.TrimSpace(fields[1]))
		if name == "" || bucket == "" || strings.Contains(name, "refreshing checks status") {
			continue
		}
		latestByName[name] = latestCheckState{
			Bucket: bucket,
			Index:  i,
		}
	}
	if len(latestByName) == 0 {
		return latestChecksSnapshot{}, fmt.Errorf("parse checks text snapshot: no tabular checks found")
	}
	return latestCheckSnapshotFromStates(latestByName), nil
}

func latestCheckSnapshotFromStates(latestByName map[string]latestCheckState) latestChecksSnapshot {
	names := make([]string, 0, len(latestByName))
	for name := range latestByName {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names))
	allPassing := true
	hasFailures := false
	for _, name := range names {
		state := latestByName[name]
		lines = append(lines, fmt.Sprintf("%s\t%s", name, state.Bucket))
		switch state.Bucket {
		case "pass", "skipping":
		case "fail", "cancel", "cancelled", "timed_out", "action_required":
			allPassing = false
			hasFailures = true
		default:
			allPassing = false
		}
	}
	return latestChecksSnapshot{
		AllPassing:  allPassing,
		HasFailures: hasFailures,
		Summary:     strings.Join(lines, "\n"),
	}
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
