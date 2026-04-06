package harness

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
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
	maxPRChecksNoReportRetries    = 6
	prChecksNoReportRetryDelay    = 10 * time.Second
	maxCheckSummaryChars          = 4000
	defaultCIWorkflowPath         = ".github/workflows/ci.yml"
	maxPushSyncAttempts           = 3
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

type codexRunOptions struct {
	SkipGitRepoCheck bool
}

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

	h.logf("stage=preflight status=start")
	for _, cmd := range preflightCommands() {
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
		return h.fail(ExitConfig, "config", fmt.Errorf("one of repo, repo_url, or repos[] is required"), runDir)
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
		cloneRes, cloneErr := h.runCommand(ctx, "clone", cloneRepoCommand(repoURL, branchForClone, repoDir))
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
			if _, err := h.runCommand(ctx, "clone", cloneRepoDefaultBranchCommand(repoURL, repoDir)); err != nil {
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
		return h.fail(ExitConfig, "config", fmt.Errorf("target_subdir does not exist or is not a directory: %s", cfg.TargetSubdir), runDir)
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
				"stage=git status=ok action=branch_reuse branch=%s base_branch=%s repo=%s repo_dir=%s",
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

	codexDir := targetDir
	if len(repos) > 1 {
		codexDir = runDir
	}
	codexOpts := codexRunOptions{SkipGitRepoCheck: len(repos) > 1}
	codexBasePrompt := workspaceCodexPrompt(cfg.Prompt, cfg.TargetSubdir, repos)
	if strings.TrimSpace(agentsPath) != "" {
		codexBasePrompt = withAgentsPrompt(codexBasePrompt, agentsPath)
	}
	codexTargetLabel := codexTargetLabel(cfg.TargetSubdir, len(repos) > 1)

	h.logf("stage=codex status=start target=%s", codexTargetLabel)
	codexStart := time.Now()
	if err := h.runCodexWithHeartbeat(ctx, codexDir, codexBasePrompt, codexOpts); err != nil {
		return h.fail(ExitCodex, "codex", err, runDir)
	}
	h.logf("stage=codex status=ok elapsed_s=%d", int(time.Since(codexStart).Seconds()))

	for i := range repos {
		statusRes, err := h.runCommand(ctx, "git", statusCommand(repos[i].Dir))
		if err != nil {
			return h.fail(ExitGit, "git", err, runDir)
		}
		repos[i].Changed = strings.TrimSpace(statusRes.Stdout) != ""
		h.logf("stage=git status=scan repo=%s repo_dir=%s changed=%t", repos[i].URL, repos[i].RelDir, repos[i].Changed)
	}

	changedCount := 0
	for _, repo := range repos {
		if repo.Changed {
			changedCount++
		}
	}
	if changedCount == 0 {
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
			codexDir,
			codexOpts,
			codexBasePrompt,
			codexTargetLabel,
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
	codexDir string,
	codexOpts codexRunOptions,
	codexBasePrompt string,
	codexTargetLabel string,
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
		prLookupRes, err := h.runCommand(ctx, "pr", prLookupByHeadCommand(repo.Dir, repo.Branch))
		if err != nil {
			return ExitPR, "pr", err
		}
		repo.PRURL = extractFirstURL(prLookupRes.Stdout)
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
			return ExitPR, "pr", err
		}
		repo.PRURL = extractFirstURL(prRes.Stdout)
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
				checkRes, checkErr = h.runCommand(ctx, "checks", prChecksAnyCommand(repo.Dir, repo.PRURL))
			}
			if checkErr == nil {
				h.logf("stage=checks status=ok repo=%s repo_dir=%s pr_url=%s attempt=%d", repo.URL, repo.RelDir, repo.PRURL, attempt+1)
				return ExitSuccess, "", nil
			}

			checkSummary = summarizeCheckOutput(checkRes)
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
			"stage=codex status=start target=%s mode=remediation attempt=%d repo=%s repo_dir=%s",
			codexTargetLabel,
			attempt+1,
			repo.URL,
			repo.RelDir,
		)
		codexStart := time.Now()
		if err := h.runCodexWithHeartbeat(ctx, codexDir, repairPrompt, codexOpts); err != nil {
			return ExitCodex, "codex", err
		}
		h.logf(
			"stage=codex status=ok elapsed_s=%d mode=remediation attempt=%d repo=%s repo_dir=%s",
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
			return ExitPR, "checks", fmt.Errorf("required PR checks failed and codex produced no remediation changes for repo %s", repo.URL)
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

func repoWorkspaceDirName(repoURL string, index, total int) string {
	if total <= 1 {
		return "repo"
	}
	return fmt.Sprintf("repo-%02d-%s", index+1, repoDirSlug(repoURL))
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
		return streamRunner.RunStream(ctx, cmd, onLine)
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

func isNonFastForwardPush(res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "non-fast-forward") || strings.Contains(text, "fetch first")
}

func shouldFallbackCloneToDefaultBranch(baseBranch string, res execx.Result, err error) bool {
	if err == nil {
		return false
	}
	normalized := normalizeBranchRef(baseBranch)
	if !strings.HasPrefix(normalized, "moltenhub-") {
		return false
	}
	text := strings.ToLower(strings.Join([]string{res.Stdout, res.Stderr, err.Error()}, "\n"))
	return strings.Contains(text, "could not find remote branch") ||
		(strings.Contains(text, "remote branch") && strings.Contains(text, "not found"))
}

func (h Harness) runCodexWithHeartbeat(ctx context.Context, targetDir, prompt string, opts codexRunOptions) error {
	done := make(chan error, 1)
	go func() {
		_, err := h.runCommand(ctx, "codex", codexCommandWithOptions(targetDir, prompt, opts))
		done <- err
	}()

	start := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			h.logf("stage=codex status=running elapsed_s=%d", int(time.Since(start).Seconds()))
		case <-ctx.Done():
			err := <-done
			if err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}
func resolveTargetDir(repoDir, targetSubdir string) (string, error) {
	targetDir := filepath.Join(repoDir, filepath.Clean(targetSubdir))
	rel, err := filepath.Rel(repoDir, targetDir)
	if err != nil {
		return "", fmt.Errorf("resolve target subdir: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("target_subdir escapes repository")
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

func preflightCommands() []execx.Command {
	return []execx.Command{
		{Name: "git", Args: []string{"--version"}},
		{Name: "gh", Args: []string{"--version"}},
		{Name: "codex", Args: []string{"--help"}},
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
	args := []string{"exec", "--sandbox", "workspace-write"}
	if opts.SkipGitRepoCheck {
		args = append(args, "--skip-git-repo-check")
	}
	args = append(args, withCompletionGatePrompt(prompt))

	return execx.Command{
		Dir:  targetDir,
		Name: "codex",
		Args: args,
	}
}

func statusCommand(repoDir string) execx.Command {
	return execx.Command{Dir: repoDir, Name: "git", Args: []string{"status", "--porcelain"}}
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

	return base + `

When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.

When a task fails:
- Queue a follow-up task dedicated to reviewing the logs and fixing all underlying issues in this codebase.
- Pass the relevant failing file/folder log path(s) into that follow-up task context.
- Use this follow-up run config payload shape:
  {"repos":["git@github.com:jefking/moltenhub-code.git"],"base_branch":"main","target_subdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}

Completion requirements:
- Only create a new branch when starting from 'main'; if you're fixing an existing non-'main' branch, stay on it.
- Start every new branch and every PR title with 'moltenhub-'.
- Keep working until there is a PR for your changes and required CI/CD checks are green.
- If CI/CD fails, continue fixing code/tests/workflows until checks pass.
- If you changed multiple repositories, ensure each changed repository has its own branch and PR.
- Optimize for the highest-quality PR you can produce with focused, production-ready changes.`
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

func remediationCommitMessage(base string, attempt int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "chore: automated update"
	}
	return fmt.Sprintf("%s (ci remediation %d)", base, attempt)
}
