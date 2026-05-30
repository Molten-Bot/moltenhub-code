package githubreview

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
)

type expectedRun struct {
	cmd execx.Command
	res execx.Result
	err error
}

type fakeRunner struct {
	t     *testing.T
	exps  []expectedRun
	calls []execx.Command
}

func (f *fakeRunner) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	f.t.Helper()
	if len(f.exps) == 0 {
		f.t.Fatalf("unexpected command: %+v", cmd)
	}
	exp := f.exps[0]
	if !reflect.DeepEqual(exp.cmd, cmd) {
		f.t.Fatalf("command mismatch\n got:  %+v\n want: %+v", cmd, exp.cmd)
	}
	f.exps = f.exps[1:]
	f.calls = append(f.calls, cmd)
	return exp.res, exp.err
}

func TestPollOnceQueuesReviewOnlyForCurrentRequestedReviewer(t *testing.T) {
	t.Parallel()

	notifications := `[{
		"id":"thread-42",
		"reason":"review_requested",
		"subject":{"title":"Improve tests","url":"https://api.github.com/repos/acme/repo/pulls/42","type":"PullRequest"},
		"repository":{"full_name":"acme/repo","clone_url":"https://github.com/acme/repo.git","ssh_url":"git@github.com:acme/repo.git"}
	}]`
	requested := `{"users":[{"login":"octocat"}],"teams":[]}`
	details := `{"html_url":"https://github.com/acme/repo/pull/42","state":"open","draft":false,"head":{"ref":"feature/improve-tests","sha":"abc123"}}`

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: notifications}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: requested}},
		{cmd: pullRequestDetailsCommand("acme/repo", 42), res: execx.Result{Stdout: details}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: `[]`}},
	}}

	var enqueued config.Config
	w := &Watcher{
		Runner: fake,
		Enqueue: func(_ context.Context, cfg config.Config) (string, error) {
			enqueued = cfg
			return "local-123", nil
		},
		Options: Options{
			PollInterval: time.Second,
			Writeback:    "summary-comment",
			AutoMerge:    true,
			MergeMethod:  "squash",
			ResponseMode: "off",
		},
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if got, want := enqueued.RepoURL, "https://github.com/acme/repo.git"; got != want {
		t.Fatalf("RepoURL = %q, want %q", got, want)
	}
	if got, want := enqueued.LibraryTaskName, codeReviewTaskName; got != want {
		t.Fatalf("LibraryTaskName = %q, want %q", got, want)
	}
	if enqueued.Review == nil {
		t.Fatal("Review = nil, want populated review config")
	}
	if got, want := enqueued.Review.Trigger, "github-notification"; got != want {
		t.Fatalf("Review.Trigger = %q, want %q", got, want)
	}
	if got, want := enqueued.Review.RequestedReviewer, "octocat"; got != want {
		t.Fatalf("Review.RequestedReviewer = %q, want %q", got, want)
	}
	if !enqueued.Review.RequireRequestedReviewer {
		t.Fatal("Review.RequireRequestedReviewer = false, want true")
	}
	if !enqueued.Review.AutoMerge {
		t.Fatal("Review.AutoMerge = false, want true")
	}
	if got, want := enqueued.Review.PRURL, "https://github.com/acme/repo/pull/42"; got != want {
		t.Fatalf("Review.PRURL = %q, want %q", got, want)
	}
}

func TestPollOnceSkipsWhenViewerIsNoLongerRequested(t *testing.T) {
	t.Parallel()

	notifications := `[{
		"id":"thread-42",
		"reason":"review_requested",
		"subject":{"url":"https://api.github.com/repos/acme/repo/pulls/42","type":"PullRequest"},
		"repository":{"clone_url":"https://github.com/acme/repo.git"}
	}]`
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: notifications}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{"users":[{"login":"someone-else"}]}`}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: `[]`}},
	}}

	called := false
	w := &Watcher{
		Runner: fake,
		Enqueue: func(context.Context, config.Config) (string, error) {
			called = true
			return "", nil
		},
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if called {
		t.Fatal("Enqueue was called, want skip")
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestPollOnceFallsBackToRequestedReviewSearch(t *testing.T) {
	t.Parallel()

	searchResults := `[{
		"number":42,
		"url":"https://github.com/acme/repo/pull/42",
		"state":"open",
		"isDraft":false,
		"repository":{"nameWithOwner":"acme/repo"}
	}]`
	details := `{"html_url":"https://github.com/acme/repo/pull/42","state":"open","draft":false,"base":{"repo":{"full_name":"acme/repo","clone_url":"https://github.com/acme/repo.git","ssh_url":"git@github.com:acme/repo.git"}},"head":{"ref":"feature/improve-tests","sha":"abc123"}}`

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: searchResults}},
		{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{"users":[{"login":"octocat"}]}`}},
		{cmd: pullRequestDetailsCommand("acme/repo", 42), res: execx.Result{Stdout: details}},
	}}

	var enqueued config.Config
	w := &Watcher{
		Runner: fake,
		Enqueue: func(_ context.Context, cfg config.Config) (string, error) {
			enqueued = cfg
			return "local-123", nil
		},
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if got, want := enqueued.RepoURL, "https://github.com/acme/repo.git"; got != want {
		t.Fatalf("RepoURL = %q, want %q", got, want)
	}
	if enqueued.Review == nil {
		t.Fatal("Review = nil, want populated review config")
	}
	if got, want := enqueued.Review.PRNumber, 42; got != want {
		t.Fatalf("Review.PRNumber = %d, want %d", got, want)
	}
	if got, want := enqueued.Review.PRURL, "https://github.com/acme/repo/pull/42"; got != want {
		t.Fatalf("Review.PRURL = %q, want %q", got, want)
	}
}

func TestPullRequestRefFromAPIURL(t *testing.T) {
	t.Parallel()

	ownerRepo, number, ok := pullRequestRefFromAPIURL("https://api.github.com/repos/acme/repo/pulls/42")
	if !ok {
		t.Fatal("pullRequestRefFromAPIURL() ok = false, want true")
	}
	if ownerRepo != "acme/repo" || number != 42 {
		t.Fatalf("pullRequestRefFromAPIURL() = %q %d, want acme/repo 42", ownerRepo, number)
	}
	if _, _, ok := pullRequestRefFromAPIURL("https://api.github.com/repos/acme/repo/issues/42"); ok {
		t.Fatal("pullRequestRefFromAPIURL(issue) ok = true, want false")
	}
}
