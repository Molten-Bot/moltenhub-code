package githubutil

import (
	"net/url"
	"strconv"
	"strings"
)

// PullRequestSelector converts a GitHub PR URL into the numeric selector that
// gh prefers for repository-scoped PR operations. Non-PR URLs are returned as-is.
func PullRequestSelector(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parts, ok := githubPullRequestURLParts(raw)
	if !ok {
		return raw
	}
	return strconv.Itoa(parts.number)
}

// PullRequestRepository extracts OWNER/REPO from a GitHub PR URL.
// Non-PR selectors return an empty string.
func PullRequestRepository(raw string) string {
	parts, ok := githubPullRequestURLParts(raw)
	if !ok {
		return ""
	}
	return parts.owner + "/" + parts.repo
}

type githubPullRequestParts struct {
	owner  string
	repo   string
	number int
}

func githubPullRequestURLParts(raw string) (githubPullRequestParts, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return githubPullRequestParts{}, false
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return githubPullRequestParts{}, false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	if host != "github.com" && host != "www.github.com" {
		return githubPullRequestParts{}, false
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return githubPullRequestParts{}, false
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSpace(parts[1])
	if owner == "" || repo == "" {
		return githubPullRequestParts{}, false
	}

	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return githubPullRequestParts{}, false
	}
	return githubPullRequestParts{owner: owner, repo: repo, number: number}, true
}
