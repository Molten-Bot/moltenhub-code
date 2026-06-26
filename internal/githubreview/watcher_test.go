package githubreview

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

func TestWatcherUpdateOptionsHandlesNilAndStoresSnapshot(t *testing.T) {
	t.Parallel()

	var nilWatcher *Watcher
	nilWatcher.UpdateOptions(Options{ResponseMode: "off"})

	w := &Watcher{}
	options := Options{
		PollInterval: 2 * time.Second,
		Writeback:    "pr-comment",
		AutoMerge:    true,
		MergeMethod:  "merge",
		ResponseMode: "caveman-full",
	}
	w.UpdateOptions(options)

	if got := w.optionsSnapshot(); !reflect.DeepEqual(got, options) {
		t.Fatalf("optionsSnapshot() = %+v, want %+v", got, options)
	}
}

func TestRunValidatesDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		w    *Watcher
		want string
	}{
		{name: "nil watcher", w: nil, want: "github review watcher is required"},
		{name: "missing runner", w: &Watcher{Enqueue: func(context.Context, config.Config) (string, error) { return "", nil }}, want: "github review watcher runner is required"},
		{name: "missing enqueue", w: &Watcher{Runner: &fakeRunner{t: t}}, want: "github review watcher enqueue function is required"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.w.Run(context.Background())
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Run() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunPollsOnceThenStopsWhenContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: `[]`}},
	}}
	w := &Watcher{
		Runner: fake,
		Enqueue: func(context.Context, config.Config) (string, error) {
			t.Fatal("Enqueue called, want no candidates")
			return "", nil
		},
		Options: Options{PollInterval: time.Hour},
	}

	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestPollOnceReturnsPrimaryLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exps []expectedRun
		want string
	}{
		{
			name: "notifications command fails",
			exps: []expectedRun{{cmd: notificationsCommand(), err: errors.New("boom")}},
			want: "load github notifications: boom",
		},
		{
			name: "notifications json invalid",
			exps: []expectedRun{{cmd: notificationsCommand(), res: execx.Result{Stdout: `{`}}},
			want: "decode github notifications:",
		},
		{
			name: "viewer command fails",
			exps: []expectedRun{
				{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
				{cmd: viewerCommand(), err: errors.New("no auth")},
			},
			want: "load github viewer: no auth",
		},
		{
			name: "viewer json invalid",
			exps: []expectedRun{
				{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
				{cmd: viewerCommand(), res: execx.Result{Stdout: `{`}},
			},
			want: "decode github viewer:",
		},
		{
			name: "viewer empty",
			exps: []expectedRun{
				{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
				{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"   "}`}},
			},
			want: "github viewer login was empty",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeRunner{t: t, exps: tt.exps}
			w := &Watcher{
				Runner: fake,
				Enqueue: func(context.Context, config.Config) (string, error) {
					t.Fatal("Enqueue called, want error before candidates")
					return "", nil
				},
			}
			err := w.PollOnce(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PollOnce() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPollOnceRejectsNilWatcher(t *testing.T) {
	t.Parallel()

	var w *Watcher
	err := w.PollOnce(context.Background())
	if err == nil || err.Error() != "github review watcher is required" {
		t.Fatalf("PollOnce() error = %v, want nil watcher error", err)
	}
}

func TestPollOnceLogsAndContinuesWhenSearchFails(t *testing.T) {
	t.Parallel()

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewPullRequestsCommand(), err: errors.New("rate limited")},
	}}
	var logs []string
	w := &Watcher{
		Runner: fake,
		Enqueue: func(context.Context, config.Config) (string, error) {
			t.Fatal("Enqueue called, want no candidates")
			return "", nil
		},
		Logf: func(format string, args ...any) { logs = append(logs, format) },
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v, want nil", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "search_requested_reviews") {
		t.Fatalf("logs = %#v, want search warning", logs)
	}
}

func TestPollOnceLogsAndContinuesWhenSearchJSONInvalid(t *testing.T) {
	t.Parallel()

	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: `[]`}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: `{`}},
	}}
	var logs []string
	w := &Watcher{
		Runner: fake,
		Enqueue: func(context.Context, config.Config) (string, error) {
			t.Fatal("Enqueue called, want no candidates")
			return "", nil
		},
		Logf: func(format string, args ...any) { logs = append(logs, format) },
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v, want nil", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "search_requested_reviews") {
		t.Fatalf("logs = %#v, want search warning", logs)
	}
}

func TestQueueCandidateSkipsClosedDuplicateAndFailedCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exps []expectedRun
		seen string
		want string
	}{
		{
			name: "requested reviewers command fails",
			exps: []expectedRun{{cmd: requestedReviewersCommand("acme/repo", 42), err: errors.New("api down")}},
			want: "verify_requested_reviewer",
		},
		{
			name: "requested reviewers json invalid",
			exps: []expectedRun{{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{`}}},
			want: "verify_requested_reviewer",
		},
		{
			name: "pull request details command fails",
			exps: []expectedRun{
				{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{"users":[{"login":"octocat"}]}`}},
				{cmd: pullRequestDetailsCommand("acme/repo", 42), err: errors.New("missing")},
			},
			want: "load_pr",
		},
		{
			name: "pull request details json invalid",
			exps: []expectedRun{
				{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{"users":[{"login":"octocat"}]}`}},
				{cmd: pullRequestDetailsCommand("acme/repo", 42), res: execx.Result{Stdout: `{`}},
			},
			want: "load_pr",
		},
		{
			name: "closed pull request",
			exps: []expectedRun{
				{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{"users":[{"login":"octocat"}]}`}},
				{cmd: pullRequestDetailsCommand("acme/repo", 42), res: execx.Result{Stdout: `{"state":"closed","head":{"sha":"abc123"}}`}},
			},
			want: "pr_not_open",
		},
		{
			name: "duplicate head",
			seen: "acme/repo#42@abc123",
			exps: []expectedRun{
				{cmd: requestedReviewersCommand("acme/repo", 42), res: execx.Result{Stdout: `{"users":[{"login":"octocat"}]}`}},
				{cmd: pullRequestDetailsCommand("acme/repo", 42), res: execx.Result{Stdout: `{"state":"open","head":{"sha":"abc123"}}`}},
			},
			want: "duplicate",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeRunner{t: t, exps: tt.exps}
			var logs []string
			w := &Watcher{
				Runner: fake,
				Enqueue: func(context.Context, config.Config) (string, error) {
					t.Fatal("Enqueue called, want skip")
					return "", nil
				},
				Logf: func(format string, args ...any) {
					logs = append(logs, format)
				},
			}
			if tt.seen != "" {
				w.markSeen(tt.seen)
			}
			w.queueCandidate(context.Background(), reviewCandidate{OwnerRepo: "acme/repo", PRNumber: 42}, "octocat")
			if len(fake.exps) != 0 {
				t.Fatalf("unconsumed expectations: %d", len(fake.exps))
			}
			if len(logs) != 1 || !strings.Contains(logs[0], tt.want) {
				t.Fatalf("logs = %#v, want containing %q", logs, tt.want)
			}
		})
	}
}

func TestQueueCandidateIgnoresInvalidCandidateWithoutRunner(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		Runner: &fakeRunner{t: t},
		Enqueue: func(context.Context, config.Config) (string, error) {
			t.Fatal("Enqueue called, want invalid candidate ignored")
			return "", nil
		},
	}

	w.queueCandidate(context.Background(), reviewCandidate{}, "octocat")
	w.queueCandidate(context.Background(), reviewCandidate{OwnerRepo: "acme/repo", PRNumber: 0}, "octocat")
}

func TestPollOnceSkipsInvalidNotificationSubjectAndSearchRows(t *testing.T) {
	t.Parallel()

	notifications := `[{
		"id":"thread-42",
		"reason":"review_requested",
		"subject":{"url":"https://api.github.com/repos/acme/repo/issues/42","type":"PullRequest"},
		"repository":{"clone_url":"https://github.com/acme/repo.git"}
	}]`
	searchResults := `[
		{"number":0,"repository":{"nameWithOwner":"acme/repo"}},
		{"number":42,"repository":{"nameWithOwner":"   "}}
	]`
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: notifications}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: searchResults}},
	}}
	var logs []string
	w := &Watcher{
		Runner: fake,
		Enqueue: func(context.Context, config.Config) (string, error) {
			t.Fatal("Enqueue called, want invalid candidates skipped")
			return "", nil
		},
		Logf: func(format string, args ...any) { logs = append(logs, format) },
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "invalid_pr_subject") {
		t.Fatalf("logs = %#v, want invalid subject warning", logs)
	}
}

func TestPollOnceSkipsNonReviewNotifications(t *testing.T) {
	t.Parallel()

	notifications := `[
		{"id":"thread-1","reason":"subscribed","subject":{"url":"https://api.github.com/repos/acme/repo/pulls/1","type":"PullRequest"}},
		{"id":"thread-2","reason":"review_requested","subject":{"url":"https://api.github.com/repos/acme/repo/issues/2","type":"Issue"}}
	]`
	fake := &fakeRunner{t: t, exps: []expectedRun{
		{cmd: notificationsCommand(), res: execx.Result{Stdout: notifications}},
		{cmd: viewerCommand(), res: execx.Result{Stdout: `{"login":"octocat"}`}},
		{cmd: requestedReviewPullRequestsCommand(), res: execx.Result{Stdout: `[]`}},
	}}
	w := &Watcher{
		Runner: fake,
		Enqueue: func(context.Context, config.Config) (string, error) {
			t.Fatal("Enqueue called, want non-review notifications skipped")
			return "", nil
		},
	}

	if err := w.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(fake.exps) != 0 {
		t.Fatalf("unconsumed expectations: %d", len(fake.exps))
	}
}

func TestHelpersCoverBoundaryValues(t *testing.T) {
	t.Parallel()

	w := &Watcher{}
	if w.seenKey(" ") {
		t.Fatal("seenKey(empty) = true, want false")
	}
	w.markSeen(" ")
	if len(w.seen) != 0 {
		t.Fatalf("markSeen(empty) initialized seen map, len = %d", len(w.seen))
	}
	if got := reviewDedupeKey(" ACME/Repo ", 42, " ABC123 "); got != "acme/repo#42@abc123" {
		t.Fatalf("reviewDedupeKey() = %q, want normalized key", got)
	}
	if got := reviewDedupeKey("acme/repo", 42, " "); got != "acme/repo#42@unknown" {
		t.Fatalf("reviewDedupeKey(empty SHA) = %q, want unknown suffix", got)
	}
	if got := reviewDedupeKey("", 42, "abc"); got != "" {
		t.Fatalf("reviewDedupeKey(empty repo) = %q, want empty", got)
	}
	if got := firstNonEmpty(" ", "\t", " value "); got != "value" {
		t.Fatalf("firstNonEmpty() = %q, want trimmed value", got)
	}
	if got := firstNonEmpty(" ", "\t"); got != "" {
		t.Fatalf("firstNonEmpty(empty values) = %q, want empty", got)
	}
	if _, _, ok := pullRequestRefFromAPIURL(":// bad"); ok {
		t.Fatal("pullRequestRefFromAPIURL(malformed) ok = true, want false")
	}
	if _, _, ok := pullRequestRefFromAPIURL("https://api.github.com/repos/acme/repo/pulls/not-a-number"); ok {
		t.Fatal("pullRequestRefFromAPIURL(non-number) ok = true, want false")
	}
	if _, _, ok := pullRequestRefFromAPIURL("https://api.github.com/repos/ /repo/pulls/42"); ok {
		t.Fatal("pullRequestRefFromAPIURL(empty owner) ok = true, want false")
	}
}
