package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func githubProfileResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func githubResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestResolveAuthenticatedGitHubProfileURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	if _, err := resolveAuthenticatedGitHubProfileURL(context.Background(), &http.Client{}); err == nil || !strings.Contains(err.Error(), "github token is not configured") {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(no token) error = %v, want token configuration error", err)
	}

	t.Setenv("GH_TOKEN", "test-token")
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got, want := req.Method, http.MethodGet; got != want {
				t.Fatalf("request method = %q, want %q", got, want)
			}
			if got, want := req.URL.String(), "https://api.github.com/user"; got != want {
				t.Fatalf("request URL = %q, want %q", got, want)
			}
			if got, want := req.Header.Get("Authorization"), "Bearer test-token"; got != want {
				t.Fatalf("authorization header = %q, want %q", got, want)
			}
			if got, want := req.Header.Get("Accept"), "application/vnd.github+json"; got != want {
				t.Fatalf("accept header = %q, want %q", got, want)
			}
			if got, want := req.Header.Get("X-GitHub-Api-Version"), "2022-11-28"; got != want {
				t.Fatalf("x-github-api-version header = %q, want %q", got, want)
			}
			return githubProfileResponse(http.StatusOK, `{"html_url":"https://github.com/molten-bot"}`), nil
		}),
	}
	if got, err := resolveAuthenticatedGitHubProfileURL(context.Background(), client); err != nil || got != "https://github.com/molten-bot" {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(html_url) = (%q, %v), want expected profile URL", got, err)
	}

	client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return githubProfileResponse(http.StatusOK, `{"login":"molten-bot"}`), nil
		}),
	}
	if got, err := resolveAuthenticatedGitHubProfileURL(context.Background(), client); err != nil || got != "https://github.com/molten-bot" {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(login fallback) = (%q, %v), want login-derived profile URL", got, err)
	}

	client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return githubProfileResponse(http.StatusUnauthorized, `{"message":"Bad credentials"}`), nil
		}),
	}
	if _, err := resolveAuthenticatedGitHubProfileURL(context.Background(), client); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(non-2xx) error = %v, want API message", err)
	}

	client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return githubProfileResponse(http.StatusOK, `{`), nil
		}),
	}
	if _, err := resolveAuthenticatedGitHubProfileURL(context.Background(), client); err == nil || !strings.Contains(err.Error(), "decode github profile") {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(invalid json) error = %v, want decode error", err)
	}

	client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return githubProfileResponse(http.StatusOK, `{"login":"","html_url":""}`), nil
		}),
	}
	if _, err := resolveAuthenticatedGitHubProfileURL(context.Background(), client); err == nil || !strings.Contains(err.Error(), "missing profile url") {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(missing profile fields) error = %v, want missing profile url error", err)
	}

	client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport down")
		}),
	}
	if _, err := resolveAuthenticatedGitHubProfileURL(context.Background(), client); err == nil || !strings.Contains(err.Error(), "load github profile") {
		t.Fatalf("resolveAuthenticatedGitHubProfileURL(transport error) error = %v, want load github profile error", err)
	}
}

func TestServerResolveGitHubProfileURLUsesOverride(t *testing.T) {
	t.Parallel()

	srv := NewServer("", NewBroker())
	srv.ResolveGitHubProfileURL = func(context.Context) (string, error) {
		return "https://github.com/custom-agent", nil
	}

	got, err := srv.resolveGitHubProfileURL(context.Background())
	if err != nil {
		t.Fatalf("resolveGitHubProfileURL() error = %v", err)
	}
	if got != "https://github.com/custom-agent" {
		t.Fatalf("resolveGitHubProfileURL() = %q, want custom URL", got)
	}
}

func TestResolveAuthenticatedGitHubRepos(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	if _, err := resolveAuthenticatedGitHubRepos(context.Background(), &http.Client{}); err == nil || !strings.Contains(err.Error(), "github token is not configured") {
		t.Fatalf("resolveAuthenticatedGitHubRepos(no token) error = %v, want token configuration error", err)
	}

	t.Setenv("GH_TOKEN", "test-token")
	requests := 0
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if got, want := req.Method, http.MethodGet; got != want {
				t.Fatalf("request method = %q, want %q", got, want)
			}
			if got, want := req.Header.Get("Authorization"), "Bearer test-token"; got != want {
				t.Fatalf("authorization header = %q, want %q", got, want)
			}
			if got, want := req.Header.Get("Accept"), "application/vnd.github+json"; got != want {
				t.Fatalf("accept header = %q, want %q", got, want)
			}
			switch requests {
			case 1:
				if got, want := req.URL.String(), "https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&sort=pushed"; got != want {
					t.Fatalf("request URL = %q, want %q", got, want)
				}
				header := make(http.Header)
				header.Set("Link", `<https://api.github.com/user/repos?page=2>; rel="next"`)
				return githubResponse(http.StatusOK, `[{"name":"repo","full_name":"acme/repo","description":"Docs","html_url":"https://github.com/acme/repo","owner":{"type":"Organization","avatar_url":"https://avatars.githubusercontent.com/u/42?v=4"},"default_branch":"trunk","private":true,"language":"Go","updated_at":"2026-05-01T00:00:00Z","pushed_at":"2026-05-02T00:00:00Z"}]`, header), nil
			case 2:
				if got, want := req.URL.String(), "https://api.github.com/user/repos?page=2"; got != want {
					t.Fatalf("request URL = %q, want %q", got, want)
				}
				return githubResponse(http.StatusOK, `[{"name":"web","full_name":"acme/web","html_url":"https://github.com/acme/web","owner":{"type":"User"},"private":false,"updated_at":"2026-04-01T00:00:00Z","pushed_at":"2026-05-03T00:00:00Z"}]`, nil), nil
			default:
				t.Fatalf("unexpected request %d", requests)
			}
			return nil, nil
		}),
	}

	repos, err := resolveAuthenticatedGitHubRepos(context.Background(), client)
	if err != nil {
		t.Fatalf("resolveAuthenticatedGitHubRepos() error = %v", err)
	}
	if len(repos) != 2 || repos[0].FullName != "acme/web" || repos[0].OwnerType != "User" || repos[0].OwnerKind != "personal" || !repos[0].Public || !repos[0].Personal || repos[0].Visibility != "public" || repos[0].PushedAt != "2026-05-03T00:00:00Z" || repos[1].FullName != "acme/repo" || repos[1].OwnerType != "Organization" || repos[1].OwnerKind != "organization" || repos[1].OwnerAvatarURL != "https://avatars.githubusercontent.com/u/42?v=4" || repos[1].DefaultBranch != "trunk" || !repos[1].Private || repos[1].Public || !repos[1].Organization || repos[1].Visibility != "private" || repos[1].Language != "Go" {
		t.Fatalf("repos = %#v, want paged repository summaries", repos)
	}

	client = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return githubResponse(http.StatusForbidden, `{"message":"API rate limit exceeded"}`, nil), nil
		}),
	}
	if _, err := resolveAuthenticatedGitHubRepos(context.Background(), client); err == nil || !strings.Contains(err.Error(), "API rate limit exceeded") {
		t.Fatalf("resolveAuthenticatedGitHubRepos(non-2xx) error = %v, want API message", err)
	}
}
