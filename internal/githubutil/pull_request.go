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

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	if host != "github.com" && host != "www.github.com" {
		return raw
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return raw
	}

	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return raw
	}
	return strconv.Itoa(number)
}
