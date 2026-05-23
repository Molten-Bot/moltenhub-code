package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
)

func TestPathIsDirAndSleepWithContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if !pathIsDir(dir) {
		t.Fatalf("pathIsDir(%q) = false, want true", dir)
	}

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if pathIsDir(filePath) {
		t.Fatalf("pathIsDir(%q) = true, want false", filePath)
	}
	if pathIsDir(filepath.Join(dir, "missing")) {
		t.Fatal("pathIsDir(missing) = true, want false")
	}

	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepWithContext(0) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(ctx, 10*time.Millisecond); err == nil {
		t.Fatal("sleepWithContext(canceled) error = nil, want non-nil")
	}
}

func TestPromptImageExtensionMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mediaType string
		want      string
	}{
		{mediaType: "image/jpeg", want: ".jpg"},
		{mediaType: "image/jpg", want: ".jpg"},
		{mediaType: "image/gif", want: ".gif"},
		{mediaType: "image/webp", want: ".webp"},
		{mediaType: "image/png", want: ".png"},
		{mediaType: "", want: ".png"},
		{mediaType: "application/octet-stream", want: ".img"},
	}
	for _, tt := range tests {
		if got := promptImageExtension(tt.mediaType); got != tt.want {
			t.Fatalf("promptImageExtension(%q) = %q, want %q", tt.mediaType, got, tt.want)
		}
	}
}

func TestRunRenamesGeneratedWorkBranchWhenRemoteBranchAlreadyDiverged(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	renamedBranch := "moltenhub-build-api-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	pushRejected := execx.Result{Stderr: "! [rejected] HEAD -> " + branch + " (fetch first)\n"}

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: execx.Command{Name: "git", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"--version"}}},
		{cmd: execx.Command{Name: "codex", Args: []string{"--help"}}},
		{cmd: execx.Command{Name: "gh", Args: []string{"auth", "status"}}},
		{cmd: cloneCommand(cfg, repoDir)},
		{cmd: branchCommand(repoDir, branch)},
		{cmd: pushDryRunCommand(repoDir, branch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: branchMoveCommand(repoDir, renamedBranch)},
		{cmd: pushDryRunCommand(repoDir, renamedBranch)},
		{cmd: codexCommand(targetDir, withAgentsPrompt(cfg.Prompt, agentsPath))},
		{cmd: statusCommand(repoDir), res: execx.Result{Stdout: "## " + renamedBranch + "\n M file.go\n"}},
		{cmd: addCommand(repoDir)},
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, renamedBranch)},
		{cmd: prCreateCommand(repoDir, cfg, renamedBranch), res: execx.Result{Stdout: prURL + "\n"}},
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
	if got, want := res.Branch, renamedBranch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestRunRenamesGeneratedWorkBranchWhenPushCollidesAfterCommit(t *testing.T) {
	t.Parallel()

	cfg := sampleConfig()
	now := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	guid := "abcdef123456"
	runDir := testRunDir(guid)
	agentsPath := filepath.Join(runDir, "AGENTS.md")
	repoDir := filepath.Join(runDir, "repo")
	targetDir := filepath.Join(repoDir, cfg.TargetSubdir)
	branch := "moltenhub-build-api"
	renamedBranch := "moltenhub-build-api-abcdef12"
	prURL := "https://github.com/acme/repo/pull/42"
	pushRejected := execx.Result{Stderr: "! [rejected] " + branch + " -> " + branch + " (fetch first)\n"}

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
		{cmd: commitCommand(repoDir, cfg.CommitMessage)},
		{cmd: pushCommand(repoDir, branch), res: pushRejected, err: errors.New("push rejected")},
		{cmd: branchMoveCommand(repoDir, renamedBranch)},
		{cmd: pushCommand(repoDir, renamedBranch)},
		{cmd: prCreateCommand(repoDir, cfg, renamedBranch), res: execx.Result{Stdout: prURL + "\n"}},
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
	if got, want := res.Branch, renamedBranch; got != want {
		t.Fatalf("Branch = %q, want %q", got, want)
	}
	if got, want := res.PRURL, prURL; got != want {
		t.Fatalf("PRURL = %q, want %q", got, want)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}
