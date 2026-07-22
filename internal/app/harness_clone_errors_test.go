package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/Molten-Bot/agent_00/internal/config"
	"github.com/Molten-Bot/agent_00/internal/execx"
)

func TestCloneErrorWithDetails(t *testing.T) {
	t.Parallel()

	if got := cloneErrorWithDetails(nil, execx.Result{}); got != nil {
		t.Fatalf("cloneErrorWithDetails(nil) = %v, want nil", got)
	}

	baseErr := errors.New("clone failed")
	if got := cloneErrorWithDetails(baseErr, execx.Result{}); got != baseErr {
		t.Fatalf("cloneErrorWithDetails(no detail) should return original error")
	}

	got := cloneErrorWithDetails(baseErr, execx.Result{Stderr: " fatal: repository not found "})
	if !errors.Is(got, baseErr) {
		t.Fatalf("cloneErrorWithDetails() should wrap original error: %v", got)
	}
	if !strings.Contains(got.Error(), "fatal: repository not found") {
		t.Fatalf("cloneErrorWithDetails() = %q, want detail text", got.Error())
	}
}

func TestSummarizeCloneErrorDetail(t *testing.T) {
	t.Parallel()

	if got := summarizeCommandErrorDetail(execx.Result{}, maxCloneErrorDetailChars); got != "" {
		t.Fatalf("summarizeCommandErrorDetail(empty) = %q, want empty", got)
	}

	normalized := summarizeCommandErrorDetail(execx.Result{
		Stderr: "fatal:\r\n  repository\r not   found",
		Stdout: "\n warning:   check URL ",
	}, maxCloneErrorDetailChars)
	if got, want := normalized, "fatal: repository not found warning: check URL"; got != want {
		t.Fatalf("summarizeCommandErrorDetail(normalized) = %q, want %q", got, want)
	}

	longInput := strings.Repeat("x", maxCloneErrorDetailChars+40)
	truncated := summarizeCommandErrorDetail(execx.Result{Stderr: longInput}, maxCloneErrorDetailChars)
	if !strings.HasSuffix(truncated, "...(truncated)") {
		t.Fatalf("summarizeCommandErrorDetail(truncated) missing suffix: %q", truncated)
	}
	if got, want := len(truncated), maxCloneErrorDetailChars+len("...(truncated)"); got != want {
		t.Fatalf("len(truncated) = %d, want %d", got, want)
	}
}

func TestIsMissingRemoteBranchCloneError(t *testing.T) {
	t.Parallel()

	if isMissingRemoteBranchCloneError(nil, execx.Result{}) {
		t.Fatal("isMissingRemoteBranchCloneError(nil err) = true, want false")
	}

	errMissing := errors.New("fatal: Could not find remote branch release/2026.04-hotfix to clone")
	if !isMissingRemoteBranchCloneError(errMissing, execx.Result{}) {
		t.Fatal("isMissingRemoteBranchCloneError(could not find remote branch) = false, want true")
	}

	errNotFound := errors.New("clone failed")
	resNotFound := execx.Result{Stderr: "warning: remote branch release/2026.04-hotfix not found in upstream origin"}
	if !isMissingRemoteBranchCloneError(errNotFound, resNotFound) {
		t.Fatal("isMissingRemoteBranchCloneError(remote branch + not found) = false, want true")
	}

	errOther := errors.New("connection timeout")
	if isMissingRemoteBranchCloneError(errOther, execx.Result{Stderr: "unable to access repository"}) {
		t.Fatal("isMissingRemoteBranchCloneError(unrelated error) = true, want false")
	}
}

func TestIsRepoNotFoundCloneError(t *testing.T) {
	t.Parallel()

	if isRepoNotFoundCloneError(nil, execx.Result{}) {
		t.Fatal("isRepoNotFoundCloneError(nil err) = true, want false")
	}

	tests := []struct {
		name string
		err  error
		res  execx.Result
		want bool
	}{
		{
			name: "remote repository not found",
			err:  errors.New("clone failed"),
			res:  execx.Result{Stderr: "remote: Repository not found."},
			want: true,
		},
		{
			name: "fatal repository not found",
			err:  errors.New("clone failed"),
			res:  execx.Result{Stderr: "fatal: repository 'git@github.com:acme/repo.git' not found"},
			want: true,
		},
		{
			name: "generic repository not found",
			err:  errors.New("repository not found"),
			res:  execx.Result{},
			want: true,
		},
		{
			name: "not a git repository",
			err:  errors.New("does not appear to be a git repository"),
			res:  execx.Result{},
			want: true,
		},
		{
			name: "repository does not exist",
			err:  errors.New("repository does not exist"),
			res:  execx.Result{},
			want: true,
		},
		{
			name: "unrelated clone failure",
			err:  errors.New("failed to connect to github.com"),
			res:  execx.Result{},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isRepoNotFoundCloneError(tt.err, tt.res); got != tt.want {
				t.Fatalf("isRepoNotFoundCloneError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepoOwnerFallbackURL(t *testing.T) {
	t.Parallel()

	repoURL := "git@github.com:moltenbot000/moltenhub-code.git"
	hints := repoOwnerFallbackCandidates([]string{
		"git@github.com:Molten-Bot/user-portal.git",
		repoURL,
	})

	got, ok := repoOwnerFallbackURL(repoURL, hints)
	if !ok {
		t.Fatal("repoOwnerFallbackURL() ok = false, want true")
	}
	if got != "git@github.com:Molten-Bot/agent_00.git" {
		t.Fatalf("repoOwnerFallbackURL() = %q, want %q", got, "git@github.com:Molten-Bot/agent_00.git")
	}
}

func TestRepoOwnerFallbackURLMigratesLegacyCanonicalURL(t *testing.T) {
	t.Parallel()

	got, ok := repoOwnerFallbackURL("git@github.com:Molten-Bot/moltenhub-code.git", nil)
	if !ok {
		t.Fatal("repoOwnerFallbackURL() ok = false, want true")
	}
	if got != config.DefaultRepositoryURL {
		t.Fatalf("repoOwnerFallbackURL() = %q, want %q", got, config.DefaultRepositoryURL)
	}
}

func TestRepoOwnerFallbackURLFindsRenamedCanonicalOwner(t *testing.T) {
	t.Parallel()

	got, ok := repoOwnerFallbackURL("git@github.com:moltenbot000/agent_00.git", nil)
	if !ok {
		t.Fatal("repoOwnerFallbackURL() ok = false, want true")
	}
	if got != config.DefaultRepositoryURL {
		t.Fatalf("repoOwnerFallbackURL() = %q, want %q", got, config.DefaultRepositoryURL)
	}
}

func TestRepoOwnerFallbackURLNoCandidate(t *testing.T) {
	t.Parallel()

	repoURL := "git@github.com:acme/private-repo.git"
	if got, ok := repoOwnerFallbackURL(repoURL, repoOwnerFallbackCandidates([]string{repoURL})); ok || got != "" {
		t.Fatalf("repoOwnerFallbackURL() = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestParseGitHubRepoRefSupportsSSHAndHTTPS(t *testing.T) {
	t.Parallel()

	if ref, ok := parseGitHubRepoRef("git@github.com:Molten-Bot/agent_00.git"); !ok || ref.owner != "Molten-Bot" || ref.name != "agent_00" {
		t.Fatalf("parseGitHubRepoRef(scp) = (%+v, %v), want owner/name parsed", ref, ok)
	}
	if ref, ok := parseGitHubRepoRef("ssh://git@github.com/Molten-Bot/agent_00.git"); !ok || ref.owner != "Molten-Bot" || ref.name != "agent_00" {
		t.Fatalf("parseGitHubRepoRef(ssh URL) = (%+v, %v), want owner/name parsed", ref, ok)
	}
	if ref, ok := parseGitHubRepoRef("https://github.com/Molten-Bot/agent_00.git"); !ok || ref.owner != "Molten-Bot" || ref.name != "agent_00" {
		t.Fatalf("parseGitHubRepoRef(https URL) = (%+v, %v), want owner/name parsed", ref, ok)
	}
}

func TestGitHubRepoRefWithHTTPSOwner(t *testing.T) {
	t.Parallel()

	ref, ok := parseGitHubRepoRef("git@github.com:Molten-Bot/agent_00.git")
	if !ok {
		t.Fatal("parseGitHubRepoRef(scp) = false, want true")
	}
	got, ok := ref.withHTTPSOwner("octocat")
	if !ok {
		t.Fatal("withHTTPSOwner() ok = false, want true")
	}
	if got != "https://github.com/octocat/agent_00.git" {
		t.Fatalf("withHTTPSOwner() = %q, want %q", got, "https://github.com/octocat/agent_00.git")
	}
}

func TestIsMoltenHubCodeRepositorySupportsRenamedAndLegacyURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		want    bool
	}{
		{name: "renamed SSH", repoURL: config.DefaultRepositoryURL, want: true},
		{name: "renamed HTTPS", repoURL: "https://github.com/Molten-Bot/agent_00.git", want: true},
		{name: "legacy redirect", repoURL: "git@github.com:Molten-Bot/moltenhub-code.git", want: true},
		{name: "wrong owner", repoURL: "git@github.com:octocat/agent_00.git", want: false},
		{name: "other repository", repoURL: "git@github.com:Molten-Bot/moltenhub.git", want: false},
		{name: "invalid", repoURL: "not-a-repository", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isMoltenHubCodeRepository(tt.repoURL); got != tt.want {
				t.Fatalf("isMoltenHubCodeRepository(%q) = %v, want %v", tt.repoURL, got, tt.want)
			}
		})
	}
}

func TestIsForkAlreadyExistsErrorRecognizesAlreadyHaveFork(t *testing.T) {
	t.Parallel()

	err := errors.New("exit status 1")
	res := execx.Result{Stderr: "You already have a fork of acme/repo."}
	if !isForkAlreadyExistsError(res, err) {
		t.Fatal("isForkAlreadyExistsError(already have a fork) = false, want true")
	}
}
