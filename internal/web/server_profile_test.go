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
