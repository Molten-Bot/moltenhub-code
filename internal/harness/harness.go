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

	"github.com/jef/how-i-want-to-code/internal/config"
	"github.com/jef/how-i-want-to-code/internal/execx"
	"github.com/jef/how-i-want-to-code/internal/slug"
	"github.com/jef/how-i-want-to-code/internal/workspace"
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
}

// Harness executes the clone -> codex -> PR workflow.
type Harness struct {
	Runner      execx.Runner
	Workspace   workspace.Manager
	Now         func() time.Time
	Logf        logFn
	TargetDirOK func(string) bool
}

// New returns a harness configured with defaults.
func New(runner execx.Runner) Harness {
	return Harness{
		Runner:      runner,
		Workspace:   workspace.NewManager(),
		Now:         time.Now,
		Logf:        func(string, ...any) {},
		TargetDirOK: pathIsDir,
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
	h.logf("stage=auth status=ok")

	h.logf("stage=workspace status=start")
	runDir, guid, err := h.Workspace.CreateRunDir()
	if err != nil {
		return h.fail(ExitWorkspace, "workspace", err, "")
	}
	h.logf("stage=workspace status=ok run_dir=%s guid=%s", runDir, guid)

	repoDir := filepath.Join(runDir, "repo")
	h.logf("stage=clone status=start repo=%s branch=%s", cfg.RepoURL, cfg.BaseBranch)
	if _, err := h.runCommand(ctx, "clone", cloneCommand(cfg, repoDir)); err != nil {
		return h.fail(ExitClone, "clone", err, runDir)
	}
	h.logf("stage=clone status=ok")

	targetDir, err := resolveTargetDir(repoDir, cfg.TargetSubdir)
	if err != nil {
		return h.fail(ExitConfig, "config", err, runDir)
	}
	if !h.TargetDirOK(targetDir) {
		return h.fail(ExitConfig, "config", fmt.Errorf("target_subdir does not exist or is not a directory: %s", cfg.TargetSubdir), runDir)
	}

	branch := slug.BranchName(cfg.Prompt, h.Now(), guid)
	h.logf("stage=git status=start action=branch branch=%s", branch)
	if _, err := h.runCommand(ctx, "git", branchCommand(repoDir, branch)); err != nil {
		return h.fail(ExitGit, "git", err, runDir)
	}

	h.logf("stage=codex status=start target=%s", cfg.TargetSubdir)
	codexStart := time.Now()
	if err := h.runCodexWithHeartbeat(ctx, targetDir, cfg.Prompt); err != nil {
		return h.fail(ExitCodex, "codex", err, runDir)
	}
	h.logf("stage=codex status=ok elapsed_s=%d", int(time.Since(codexStart).Seconds()))

	statusRes, err := h.runCommand(ctx, "git", statusCommand(repoDir))
	if err != nil {
		return h.fail(ExitGit, "git", err, runDir)
	}
	if strings.TrimSpace(statusRes.Stdout) == "" {
		h.logf("stage=git status=no_changes")
		return Result{ExitCode: ExitSuccess, WorkspaceDir: runDir, Branch: branch, NoChanges: true}
	}

	h.logf("stage=git status=start action=commit")
	if _, err := h.runCommand(ctx, "git", addCommand(repoDir)); err != nil {
		return h.fail(ExitGit, "git", err, runDir)
	}
	if _, err := h.runCommand(ctx, "git", commitCommand(repoDir, cfg.CommitMessage)); err != nil {
		return h.fail(ExitGit, "git", err, runDir)
	}
	if _, err := h.runCommand(ctx, "git", pushCommand(repoDir, branch)); err != nil {
		return h.fail(ExitGit, "git", err, runDir)
	}
	h.logf("stage=git status=ok action=commit")

	h.logf("stage=pr status=start")
	prRes, err := h.runCommand(ctx, "pr", prCreateCommand(repoDir, cfg, branch))
	if err != nil {
		return h.fail(ExitPR, "pr", err, runDir)
	}
	prURL := extractFirstURL(prRes.Stdout)
	h.logf("stage=pr status=ok pr_url=%s", prURL)

	return Result{
		ExitCode:     ExitSuccess,
		WorkspaceDir: runDir,
		Branch:       branch,
		PRURL:        prURL,
	}
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

func (h Harness) runCodexWithHeartbeat(ctx context.Context, targetDir, prompt string) error {
	done := make(chan error, 1)
	go func() {
		_, err := h.runCommand(ctx, "codex", codexCommand(targetDir, prompt))
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

var githubURL = regexp.MustCompile(`https://github\.com/\S+`)

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

func cloneCommand(cfg config.Config, repoDir string) execx.Command {
	return execx.Command{
		Name: "git",
		Args: []string{"clone", "--branch", cfg.BaseBranch, "--single-branch", cfg.RepoURL, repoDir},
	}
}

func branchCommand(repoDir, branch string) execx.Command {
	return execx.Command{
		Dir:  repoDir,
		Name: "git",
		Args: []string{"switch", "-c", branch},
	}
}

func codexCommand(targetDir, prompt string) execx.Command {
	return execx.Command{
		Dir:  targetDir,
		Name: "codex",
		Args: []string{"exec", "--sandbox", "workspace-write", prompt},
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
