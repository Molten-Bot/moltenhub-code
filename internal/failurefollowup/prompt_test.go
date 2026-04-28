package failurefollowup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithExecutionContractAppendsSharedContract(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.HasPrefix(got, "Base prompt\n\n") {
		t.Fatalf("WithExecutionContract() prefix = %q", got)
	}
	if !strings.Contains(got, ExecutionContract) {
		t.Fatalf("WithExecutionContract() missing shared contract: %q", got)
	}
}

func TestWithExecutionContractIncludesFailureResponseInstruction(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.Contains(got, FailureResponseInstruction) {
		t.Fatalf("WithExecutionContract() missing failure response instruction: %q", got)
	}
	if !strings.Contains(got, "Use explicit `Failure:` and `Error details:` fields.") {
		t.Fatalf("WithExecutionContract() missing explicit failure field guidance: %q", got)
	}
}

func TestWithExecutionContractIncludesRuntimeToolingInstruction(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.Contains(got, RuntimeToolingInstruction) {
		t.Fatalf("WithExecutionContract() missing runtime-tooling instruction: %q", got)
	}
	for _, want := range []string{
		"Playwright",
		"npm",
		"Python",
		"Go",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WithExecutionContract() missing runtime-tooling detail %q: %q", want, got)
		}
	}
}

func TestWithExecutionContractIncludesValidationToolingInstruction(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.Contains(got, ValidationToolingInstruction) {
		t.Fatalf("WithExecutionContract() missing validation-tooling instruction: %q", got)
	}
}

func TestWithExecutionContractIncludesHubActivityPrivacyInstruction(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.Contains(got, HubActivityPrivacyInstruction) {
		t.Fatalf("WithExecutionContract() missing hub-activity privacy instruction: %q", got)
	}
	for _, want := range []string{
		"`gh repo view OWNER/REPO --json isPrivate,nameWithOwner`",
		"Share repo and PR links only when GitHub reports `isPrivate:false`",
		"never share private repository links",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WithExecutionContract() missing privacy detail %q: %q", want, got)
		}
	}
}

func TestWithExecutionContractIncludesUninitializedRepoInstruction(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.Contains(got, UninitializedRepoInstruction) {
		t.Fatalf("WithExecutionContract() missing uninitialized-repo instruction: %q", got)
	}
}

func TestWithExecutionContractIncludesRemoteOperationsHandoff(t *testing.T) {
	t.Parallel()

	got := WithExecutionContract("Base prompt")
	if !strings.Contains(got, RemoteOperationsInstruction) {
		t.Fatalf("WithExecutionContract() missing remote-operations guidance: %q", got)
	}
}

func TestRequiredPromptScopesFailureFollowUpToMoltenHubCode(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"fix the underlying MoltenHub Code application issues in this repository",
		"Treat the original task prompt as failure context only",
		"do not implement that requested product change here unless it is required to fix MoltenHub Code failure handling",
	} {
		if !strings.Contains(RequiredPrompt, want) {
			t.Fatalf("RequiredPrompt missing %q: %q", want, RequiredPrompt)
		}
	}
}

func TestComposePromptUsesFallbackPathsAndContract(t *testing.T) {
	t.Parallel()

	got := ComposePrompt(
		RequiredPrompt,
		nil,
		[]string{
			".log/local/<request timestamp>/<request sequence>",
			".log/local/<request timestamp>/<request sequence>/term",
			".log/local/<request timestamp>/<request sequence>/terminal.log",
		},
		"",
		"Observed failure context:\n- exit_code=40",
	)

	for _, want := range []string{
		RequiredPrompt,
		"Relevant failing log path(s):",
		".log/local/<request timestamp>/<request sequence>",
		".log/local/<request timestamp>/<request sequence>/term",
		".log/local/<request timestamp>/<request sequence>/terminal.log",
		"Observed failure context:",
		OfflineReviewInstruction,
		FailureResponseInstruction,
		RuntimeToolingInstruction,
		ValidationToolingInstruction,
		HubActivityPrivacyInstruction,
		UninitializedRepoInstruction,
		RemoteOperationsInstruction,
		ActionableChangeInstruction,
		NoOpInstruction,
		fmt.Sprintf(`{"repos":["git@github.com:Molten-Bot/moltenhub-code.git"],"baseBranch":"main","targetSubdir":".","prompt":"%s"}`, RequiredPrompt),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ComposePrompt() missing %q: %q", want, got)
		}
	}
}

func TestTaskLogPathsBuildsExpectedLegacyAndCurrentFiles(t *testing.T) {
	t.Parallel()

	root := filepath.Join("/workspace", ".log", "local")
	got := TaskLogPaths(root, "1775613327-000024")
	want := []string{
		filepath.Join(root, "1775613327", "000024"),
		filepath.Join(root, "1775613327", "000024", "term"),
		filepath.Join(root, "1775613327", "000024", "terminal.log"),
		filepath.Join(root, FallbackLogSubdir, "term"),
		filepath.Join(root, FallbackLogSubdir, "terminal.log"),
	}
	if len(got) != len(want) {
		t.Fatalf("len(TaskLogPaths()) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TaskLogPaths()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFollowUpTargetingDefaultsToMainAndRoot(t *testing.T) {
	t.Parallel()

	baseBranch, targetSubdir := FollowUpTargeting("", "", "")
	if baseBranch != "main" {
		t.Fatalf("baseBranch = %q, want %q", baseBranch, "main")
	}
	if targetSubdir != "." {
		t.Fatalf("targetSubdir = %q, want %q", targetSubdir, ".")
	}
}

func TestFollowUpTargetingForcesMainAndRootForNonMainInputs(t *testing.T) {
	t.Parallel()

	baseBranch, targetSubdir := FollowUpTargeting("release/2026.04-hotfix", "internal/hub", "release/2026.04-hotfix")
	if baseBranch != "main" {
		t.Fatalf("baseBranch = %q, want %q", baseBranch, "main")
	}
	if targetSubdir != "." {
		t.Fatalf("targetSubdir = %q, want %q", targetSubdir, ".")
	}
}

func TestFollowUpTargetingKeepsMainAndRootWhenBaseIsMain(t *testing.T) {
	t.Parallel()

	baseBranch, targetSubdir := FollowUpTargeting("main", ".", "moltenhub-fix-issue")
	if baseBranch != "main" {
		t.Fatalf("baseBranch = %q, want %q", baseBranch, "main")
	}
	if targetSubdir != "." {
		t.Fatalf("targetSubdir = %q, want %q", targetSubdir, ".")
	}
}

func TestNonRemediableRepoAccessReasonDetectsGitHub403(t *testing.T) {
	t.Parallel()

	err := errors.New("git: remote: Write access to repository not granted.\nfatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403")
	if got := NonRemediableRepoAccessReason(err); got != "write access to repository not granted" {
		t.Fatalf("NonRemediableRepoAccessReason() = %q", got)
	}
}

func TestNonRemediableRepoAccessReasonDetectsAgentRepoRightsFailures(t *testing.T) {
	t.Parallel()

	err := errors.New("target repository git@github.com:acme/private.git doesn't have the rights to pull the code or push a PR")
	if got := NonRemediableRepoAccessReason(err); got != "doesn't have the rights to pull the code" {
		t.Fatalf("NonRemediableRepoAccessReason() = %q", got)
	}
}

func TestNonRemediableRepoAccessReasonDetectsGitHubSSHPermissionDenied(t *testing.T) {
	t.Parallel()

	err := errors.New("ERROR: Permission to JuliusBrussee/caveman.git denied to octocat.\nfatal: Could not read from remote repository.")
	if got := NonRemediableRepoAccessReason(err); got != ".git denied to" {
		t.Fatalf("NonRemediableRepoAccessReason() = %q", got)
	}
}

func TestNonRemediableRepoAccessReasonDetectsGitHubSSHPermissionDeniedWithoutDotGit(t *testing.T) {
	t.Parallel()

	err := errors.New("ERROR: Permission to JuliusBrussee/caveman denied to octocat.\nfatal: Could not read from remote repository.")
	if got := NonRemediableRepoAccessReason(err); got != githubSSHPermissionDeniedReason {
		t.Fatalf("NonRemediableRepoAccessReason() = %q", got)
	}
}

func TestNonRemediableRepoAccessReasonDetectsWorkflowScopeRejection(t *testing.T) {
	t.Parallel()

	err := errors.New("remote: refusing to allow an OAuth App to create or update workflow `.github/workflows/docker-release.yml` without `workflow` scope")
	if got := NonRemediableRepoAccessReason(err); got != "refusing to allow an oauth app to create or update workflow" {
		t.Fatalf("NonRemediableRepoAccessReason() = %q", got)
	}
}
