package githubreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
)

const (
	defaultPollInterval = time.Minute
	codeReviewTaskName  = "code-review"
)

type EnqueueFunc func(context.Context, config.Config) (string, error)

type Options struct {
	PollInterval time.Duration
	Writeback    string
	AutoMerge    bool
	MergeMethod  string
	ResponseMode string
}

type Watcher struct {
	Runner  execx.Runner
	Enqueue EnqueueFunc
	Logf    func(string, ...any)
	Options Options

	mu   sync.Mutex
	seen map[string]struct{}
}

func (w *Watcher) Run(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("github review watcher is required")
	}
	if w.Runner == nil {
		return fmt.Errorf("github review watcher runner is required")
	}
	if w.Enqueue == nil {
		return fmt.Errorf("github review watcher enqueue function is required")
	}

	interval := w.Options.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}

	if err := w.PollOnce(ctx); err != nil {
		w.logf("review_watch status=warn action=poll err=%q", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.PollOnce(ctx); err != nil {
				w.logf("review_watch status=warn action=poll err=%q", err)
			}
		}
	}
}

func (w *Watcher) PollOnce(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("github review watcher is required")
	}
	threads, err := w.notifications(ctx)
	if err != nil {
		return err
	}

	viewer, err := w.viewerLogin(ctx)
	if err != nil {
		return err
	}
	if viewer == "" {
		return fmt.Errorf("github viewer login was empty")
	}

	for _, thread := range threads {
		if !thread.isReviewRequestPullRequest() {
			continue
		}
		ownerRepo, prNumber, ok := pullRequestRefFromAPIURL(thread.Subject.URL)
		if !ok {
			w.logf("review_watch status=warn action=skip reason=invalid_pr_subject thread_id=%s subject_url=%s", thread.ID, thread.Subject.URL)
			continue
		}

		w.queueCandidate(ctx, reviewCandidate{
			OwnerRepo:              ownerRepo,
			PRNumber:               prNumber,
			NotificationThreadID:   thread.ID,
			NotificationCloneURL:   thread.Repository.CloneURL,
			NotificationSSHURL:     thread.Repository.SSHURL,
			NotificationSubjectURL: thread.Subject.URL,
		}, viewer)
	}

	searchResults, err := w.requestedReviewPullRequests(ctx)
	if err != nil {
		w.logf("review_watch status=warn action=search_requested_reviews err=%q", err)
		return nil
	}
	for _, result := range searchResults {
		ownerRepo := strings.TrimSpace(result.Repository.NameWithOwner)
		if ownerRepo == "" || result.Number <= 0 {
			continue
		}
		w.queueCandidate(ctx, reviewCandidate{
			OwnerRepo: ownerRepo,
			PRNumber:  result.Number,
			PRURL:     result.URL,
		}, viewer)
	}

	return nil
}

type reviewCandidate struct {
	OwnerRepo              string
	PRNumber               int
	PRURL                  string
	NotificationThreadID   string
	NotificationCloneURL   string
	NotificationSSHURL     string
	NotificationSubjectURL string
}

func (w *Watcher) queueCandidate(ctx context.Context, candidate reviewCandidate, viewer string) {
	ownerRepo := strings.TrimSpace(candidate.OwnerRepo)
	prNumber := candidate.PRNumber
	if ownerRepo == "" || prNumber <= 0 {
		return
	}

	requested, err := w.isRequestedReviewer(ctx, ownerRepo, prNumber, viewer)
	if err != nil {
		w.logf("review_watch status=warn action=verify_requested_reviewer owner_repo=%s pr=%d err=%q", ownerRepo, prNumber, err)
		return
	}
	if !requested {
		w.logf("review_watch status=skip reason=not_current_requested_reviewer owner_repo=%s pr=%d reviewer=%s", ownerRepo, prNumber, viewer)
		return
	}

	details, err := w.pullRequestDetails(ctx, ownerRepo, prNumber)
	if err != nil {
		w.logf("review_watch status=warn action=load_pr owner_repo=%s pr=%d err=%q", ownerRepo, prNumber, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(details.State), "open") {
		w.logf("review_watch status=skip reason=pr_not_open owner_repo=%s pr=%d state=%s", ownerRepo, prNumber, details.State)
		return
	}

	key := reviewDedupeKey(ownerRepo, prNumber, details.Head.SHA)
	if w.seenKey(key) {
		w.logf("review_watch status=skip reason=duplicate owner_repo=%s pr=%d head=%s", ownerRepo, prNumber, details.Head.SHA)
		return
	}

	runCfg, err := w.runConfigForReview(candidate, ownerRepo, prNumber, details, viewer)
	if err != nil {
		w.logf("review_watch status=warn action=build_config owner_repo=%s pr=%d err=%q", ownerRepo, prNumber, err)
		return
	}
	requestID, err := w.Enqueue(ctx, runCfg)
	if err != nil {
		w.logf("review_watch status=warn action=enqueue owner_repo=%s pr=%d err=%q", ownerRepo, prNumber, err)
		return
	}
	w.markSeen(key)
	w.logf("review_watch status=queued request_id=%s owner_repo=%s pr=%d reviewer=%s head=%s", requestID, ownerRepo, prNumber, viewer, details.Head.SHA)
}

func (w *Watcher) notifications(ctx context.Context) ([]notificationThread, error) {
	res, err := w.Runner.Run(ctx, notificationsCommand())
	if err != nil {
		return nil, fmt.Errorf("load github notifications: %w", err)
	}
	var threads []notificationThread
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &threads); err != nil {
		return nil, fmt.Errorf("decode github notifications: %w", err)
	}
	return threads, nil
}

func (w *Watcher) viewerLogin(ctx context.Context) (string, error) {
	res, err := w.Runner.Run(ctx, viewerCommand())
	if err != nil {
		return "", fmt.Errorf("load github viewer: %w", err)
	}
	var viewer struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &viewer); err != nil {
		return "", fmt.Errorf("decode github viewer: %w", err)
	}
	return strings.TrimSpace(viewer.Login), nil
}

func (w *Watcher) requestedReviewPullRequests(ctx context.Context) ([]searchPullRequest, error) {
	res, err := w.Runner.Run(ctx, requestedReviewPullRequestsCommand())
	if err != nil {
		return nil, fmt.Errorf("search requested review pull requests: %w", err)
	}
	var prs []searchPullRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &prs); err != nil {
		return nil, fmt.Errorf("decode requested review pull request search: %w", err)
	}
	return prs, nil
}

func (w *Watcher) isRequestedReviewer(ctx context.Context, ownerRepo string, prNumber int, viewer string) (bool, error) {
	res, err := w.Runner.Run(ctx, requestedReviewersCommand(ownerRepo, prNumber))
	if err != nil {
		return false, fmt.Errorf("load requested reviewers: %w", err)
	}
	var requested requestedReviewers
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &requested); err != nil {
		return false, fmt.Errorf("decode requested reviewers: %w", err)
	}
	for _, user := range requested.Users {
		if strings.EqualFold(strings.TrimSpace(user.Login), viewer) {
			return true, nil
		}
	}
	return false, nil
}

func (w *Watcher) pullRequestDetails(ctx context.Context, ownerRepo string, prNumber int) (pullRequestDetails, error) {
	res, err := w.Runner.Run(ctx, pullRequestDetailsCommand(ownerRepo, prNumber))
	if err != nil {
		return pullRequestDetails{}, fmt.Errorf("load pull request details: %w", err)
	}
	var details pullRequestDetails
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &details); err != nil {
		return pullRequestDetails{}, fmt.Errorf("decode pull request details: %w", err)
	}
	return details, nil
}

func (w *Watcher) runConfigForReview(candidate reviewCandidate, ownerRepo string, prNumber int, details pullRequestDetails, viewer string) (config.Config, error) {
	repoURL := firstNonEmpty(
		candidate.NotificationCloneURL,
		details.Base.Repo.CloneURL,
		fmt.Sprintf("https://github.com/%s.git", ownerRepo),
		candidate.NotificationSSHURL,
		details.Base.Repo.SSHURL,
	)
	catalog, err := library.LoadCatalog(library.DefaultDir)
	if err != nil {
		return config.Config{}, err
	}
	runCfg, err := catalog.ExpandRunConfig(codeReviewTaskName, repoURL, "")
	if err != nil {
		return config.Config{}, err
	}
	runCfg.ResponseMode = firstNonEmpty(w.Options.ResponseMode, "off")
	runCfg.Review = &config.ReviewConfig{
		PRNumber:                 prNumber,
		PRURL:                    firstNonEmpty(candidate.PRURL, details.HTMLURL, fmt.Sprintf("https://github.com/%s/pull/%d", ownerRepo, prNumber)),
		HeadBranch:               strings.TrimSpace(details.Head.Ref),
		Trigger:                  "github-notification",
		NotificationThreadID:     strings.TrimSpace(candidate.NotificationThreadID),
		RequestedReviewer:        strings.TrimSpace(viewer),
		RequireRequestedReviewer: true,
		Writeback:                firstNonEmpty(w.Options.Writeback, "summary-comment"),
		AutoMerge:                w.Options.AutoMerge,
		MergeMethod:              firstNonEmpty(w.Options.MergeMethod, "squash"),
	}
	runCfg.ApplyDefaults()
	if err := runCfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return runCfg, nil
}

func (w *Watcher) seenKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]struct{}{}
	}
	_, ok := w.seen[key]
	return ok
}

func (w *Watcher) markSeen(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]struct{}{}
	}
	w.seen[key] = struct{}{}
}

func (w *Watcher) logf(format string, args ...any) {
	if w != nil && w.Logf != nil {
		w.Logf(format, args...)
	}
}

type notificationThread struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Subject struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
	} `json:"repository"`
}

func (t notificationThread) isReviewRequestPullRequest() bool {
	return strings.EqualFold(strings.TrimSpace(t.Reason), "review_requested") &&
		strings.EqualFold(strings.TrimSpace(t.Subject.Type), "PullRequest")
}

type requestedReviewers struct {
	Users []struct {
		Login string `json:"login"`
	} `json:"users"`
}

type pullRequestDetails struct {
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Base    struct {
		Repo struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			SSHURL   string `json:"ssh_url"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

type searchPullRequest struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	State      string `json:"state"`
	IsDraft    bool   `json:"isDraft"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

func notificationsCommand() execx.Command {
	return execx.Command{
		Name: "gh",
		Args: []string{"api", "--method", "GET", "notifications", "-F", "participating=true", "-F", "per_page=50"},
	}
}

func requestedReviewPullRequestsCommand() execx.Command {
	return execx.Command{
		Name: "gh",
		Args: []string{
			"search", "prs",
			"--review-requested=@me",
			"--state=open",
			"--limit", "50",
			"--json", "number,url,state,isDraft,repository",
		},
	}
}

func viewerCommand() execx.Command {
	return execx.Command{Name: "gh", Args: []string{"api", "user"}}
}

func requestedReviewersCommand(ownerRepo string, prNumber int) execx.Command {
	return execx.Command{
		Name: "gh",
		Args: []string{"api", fmt.Sprintf("repos/%s/pulls/%d/requested_reviewers", ownerRepo, prNumber)},
	}
}

func pullRequestDetailsCommand(ownerRepo string, prNumber int) execx.Command {
	return execx.Command{
		Name: "gh",
		Args: []string{"api", fmt.Sprintf("repos/%s/pulls/%d", ownerRepo, prNumber)},
	}
}

func pullRequestRefFromAPIURL(raw string) (string, int, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+4 < len(parts); i++ {
		if parts[i] != "repos" || parts[i+3] != "pulls" {
			continue
		}
		number, err := strconv.Atoi(parts[i+4])
		if err != nil || number <= 0 {
			return "", 0, false
		}
		owner := strings.TrimSpace(parts[i+1])
		repo := strings.TrimSpace(parts[i+2])
		if owner == "" || repo == "" {
			return "", 0, false
		}
		return owner + "/" + repo, number, true
	}
	return "", 0, false
}

func reviewDedupeKey(ownerRepo string, prNumber int, headSHA string) string {
	ownerRepo = strings.ToLower(strings.TrimSpace(ownerRepo))
	headSHA = strings.ToLower(strings.TrimSpace(headSHA))
	if ownerRepo == "" || prNumber <= 0 {
		return ""
	}
	if headSHA == "" {
		headSHA = "unknown"
	}
	return fmt.Sprintf("%s#%d@%s", ownerRepo, prNumber, headSHA)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
