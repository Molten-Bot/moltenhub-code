package failurefollowup

import (
	"errors"
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
		ExecutionContract,
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

func TestNonRemediableRepoAccessReasonDetectsGitHub403(t *testing.T) {
	t.Parallel()

	err := errors.New("git: remote: Write access to repository not granted.\nfatal: unable to access 'https://github.com/acme/repo.git/': The requested URL returned error: 403")
	if got := NonRemediableRepoAccessReason(err); got != "write access to repository not granted" {
		t.Fatalf("NonRemediableRepoAccessReason() = %q", got)
	}
}
