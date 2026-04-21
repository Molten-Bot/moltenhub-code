package githubutil

import "testing"

func TestPullRequestSelectorUsesNumericSelectorForGitHubPRURLs(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https://github.com/acme/repo/pull/42":             "42",
		"https://github.com/acme/repo/pull/42/files":       "42",
		"https://www.github.com/acme/repo/pull/42?foo=bar": "42",
	}
	for raw, want := range tests {
		if got := PullRequestSelector(raw); got != want {
			t.Fatalf("PullRequestSelector(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestPullRequestSelectorLeavesNonPRSelectorsUntouched(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                                    "",
		"17":                                  "17",
		"feature/my-branch":                   "feature/my-branch",
		"%":                                   "%",
		"https://github.com/acme/repo":        "https://github.com/acme/repo",
		"https://github.com/acme/repo/pull/0": "https://github.com/acme/repo/pull/0",
		"https://github.com/acme/repo/pull/not-a-number": "https://github.com/acme/repo/pull/not-a-number",
		"https://example.com/acme/repo/42":               "https://example.com/acme/repo/42",
	}
	for raw, want := range tests {
		if got := PullRequestSelector(raw); got != want {
			t.Fatalf("PullRequestSelector(%q) = %q, want %q", raw, got, want)
		}
	}
}
