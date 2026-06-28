package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/githubutil"
)

const defaultPRMergePollInterval = 10 * time.Second

// PRMergeMonitor watches task pull requests and marks merged tasks as done.
type PRMergeMonitor struct {
	Runner                      execx.Runner
	Broker                      *Broker
	Logf                        func(string, ...any)
	CleanupTask                 func(context.Context, string) error
	OnReviewFeedback            func(context.Context, Task, PRReviewFeedback) (string, error)
	DeleteMergedBranches        bool
	DeleteMergedBranchesEnabled func() bool
	PollInterval                time.Duration

	mu             sync.Mutex
	inFlight       map[string]struct{}
	merged         map[string]struct{}
	feedbackQueued map[string]string
}

type prViewState struct {
	State          string                 `json:"state"`
	MergedAt       string                 `json:"mergedAt"`
	URL            string                 `json:"url"`
	Number         int                    `json:"number"`
	Title          string                 `json:"title"`
	HeadRefName    string                 `json:"headRefName"`
	BaseRefName    string                 `json:"baseRefName"`
	HeadRepository prRepository           `json:"headRepository"`
	HeadRepoOwner  prActor                `json:"headRepositoryOwner"`
	ReviewDecision string                 `json:"reviewDecision"`
	LatestReviews  []prReviewEntry        `json:"latestReviews"`
	Comments       []prCommentEntry       `json:"comments"`
	ReviewComments []prReviewCommentEntry `json:"-"`
}

type prRepository struct {
	Name          string  `json:"name"`
	NameWithOwner string  `json:"nameWithOwner"`
	Owner         prActor `json:"owner"`
}

type prReviewEntry struct {
	ID          string  `json:"id"`
	Author      prActor `json:"author"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	SubmittedAt string  `json:"submittedAt"`
}

type prCommentEntry struct {
	ID        string  `json:"id"`
	Author    prActor `json:"author"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

type prReviewCommentEntry struct {
	ID           int64   `json:"id"`
	User         prActor `json:"user"`
	Body         string  `json:"body"`
	Path         string  `json:"path"`
	Line         int     `json:"line"`
	OriginalLine int     `json:"original_line"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type prActor struct {
	Login string `json:"login"`
}

type PRReviewFeedback struct {
	PRURL          string
	PRNumber       int
	Title          string
	HeadBranch     string
	BaseBranch     string
	ReviewDecision string
	Digest         string
	Items          []PRReviewFeedbackItem
}

type PRReviewFeedbackItem struct {
	Kind      string
	ID        string
	Author    string
	State     string
	CreatedAt string
	Body      string
}

// Run polls tracked PRs until ctx is canceled.
func (m *PRMergeMonitor) Run(ctx context.Context) error {
	if m == nil || m.Broker == nil || m.Runner == nil {
		return nil
	}
	if m.Logf == nil {
		m.Logf = func(string, ...any) {}
	}
	if m.PollInterval <= 0 {
		m.PollInterval = defaultPRMergePollInterval
	}

	ticker := time.NewTicker(m.PollInterval)
	defer ticker.Stop()

	m.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.pollOnce(ctx)
		}
	}
}

func (m *PRMergeMonitor) pollOnce(ctx context.Context) {
	snapshot := m.Broker.Snapshot()
	active := make(map[string]struct{}, len(snapshot.Tasks))
	activePRs := make(map[string]struct{}, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		active[task.RequestID] = struct{}{}
		if !shouldMonitorTaskPR(task) {
			continue
		}
		if key := feedbackKeyForTask(task); key != "" {
			activePRs[key] = struct{}{}
		}
		if !m.beginCheck(task.RequestID) {
			continue
		}
		go func(task Task) {
			defer m.endCheck(task.RequestID)
			m.checkTaskPR(ctx, task)
		}(task)
	}
	m.forgetMissingTasks(active, activePRs)
}

func shouldMonitorTaskPR(task Task) bool {
	if strings.TrimSpace(task.PRURL) == "" {
		return false
	}
	switch strings.TrimSpace(task.Status) {
	case "completed", "no_changes":
		return true
	default:
		return false
	}
}

func (m *PRMergeMonitor) beginCheck(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight == nil {
		m.inFlight = map[string]struct{}{}
	}
	if m.merged == nil {
		m.merged = map[string]struct{}{}
	}
	if _, exists := m.inFlight[requestID]; exists {
		return false
	}
	if _, exists := m.merged[requestID]; exists {
		return false
	}
	m.inFlight[requestID] = struct{}{}
	return true
}

func (m *PRMergeMonitor) endCheck(requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inFlight, strings.TrimSpace(requestID))
}

func (m *PRMergeMonitor) markMerged(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.merged == nil {
		m.merged = map[string]struct{}{}
	}
	m.merged[requestID] = struct{}{}
}

func (m *PRMergeMonitor) forgetMissingTasks(active map[string]struct{}, activePRs map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for requestID := range m.merged {
		if _, ok := active[requestID]; ok {
			continue
		}
		delete(m.merged, requestID)
	}
	for prURL := range m.feedbackQueued {
		if _, ok := activePRs[prURL]; ok {
			continue
		}
		delete(m.feedbackQueued, prURL)
	}
}

func (m *PRMergeMonitor) checkTaskPR(ctx context.Context, task Task) {
	state, err := m.prState(ctx, task.PRURL)
	if err != nil {
		m.Logf("hub.ui status=warn event=pr_monitor request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
		return
	}
	if !state.Merged() {
		m.maybeQueueReviewFeedback(ctx, task, state)
		return
	}
	m.Broker.RecordReleaseFromTask(task, state.MergedAt)
	if err := m.Broker.MarkTaskPRMerged(task.RequestID, state.MergedAt); err != nil {
		switch {
		case err == ErrTaskNotFound:
			m.markMerged(task.RequestID)
			return
		default:
			m.Logf("hub.ui status=warn event=pr_monitor_mark_merged request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
			return
		}
	}
	deleteMergedBranches := m.deleteMergedBranchesEnabled()
	if deleteMergedBranches {
		if err := m.deleteMergedBranch(ctx, task, state); err != nil {
			m.Logf("hub.ui status=warn event=pr_monitor_delete_branch request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
			return
		}
	}
	m.Broker.DropTaskRunConfig(task.RequestID)
	if deleteMergedBranches {
		if err := m.Broker.CloseTask(task.RequestID); err != nil {
			m.Logf("hub.ui status=warn event=pr_monitor_close_deleted_branch_task request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
			return
		}
	}
	if m.CleanupTask != nil {
		if err := m.CleanupTask(ctx, task.RequestID); err != nil {
			m.Logf("hub.ui status=warn event=pr_monitor_cleanup request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
		}
	}
	m.markMerged(task.RequestID)
	m.Logf("hub.ui status=ok event=pr_merged request_id=%s pr_url=%s branch_deleted=%t", task.RequestID, task.PRURL, deleteMergedBranches)
}

func (m *PRMergeMonitor) deleteMergedBranchesEnabled() bool {
	if m.DeleteMergedBranchesEnabled != nil {
		return m.DeleteMergedBranchesEnabled()
	}
	return m.DeleteMergedBranches
}

func (m *PRMergeMonitor) deleteMergedBranch(ctx context.Context, task Task, state prViewState) error {
	repo := state.HeadRepositoryNameWithOwner()
	if repo == "" {
		repo = githubutil.PullRequestRepository(task.PRURL)
	}
	if repo == "" {
		return fmt.Errorf("pull request head repository is required")
	}
	branch := strings.TrimSpace(state.HeadRefName)
	if branch == "" {
		branch = strings.TrimSpace(task.Branch)
	}
	if owner, name, ok := strings.Cut(branch, ":"); ok && strings.TrimSpace(owner) != "" {
		branch = strings.TrimSpace(name)
	}
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if branch == "" {
		return fmt.Errorf("pull request head branch is required")
	}
	if base := strings.TrimSpace(state.BaseRefName); base != "" && branch == base {
		return fmt.Errorf("refusing to delete base branch %q", branch)
	}
	_, err := m.Runner.Run(ctx, execx.Command{
		Name: "gh",
		Args: []string{"api", "-X", "DELETE", fmt.Sprintf("repos/%s/git/refs/heads/%s", repo, branch)},
	})
	if err != nil {
		if isMissingGitHubRefError(err) {
			return nil
		}
		return err
	}
	return nil
}

func isMissingGitHubRefError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "reference does not exist") ||
		strings.Contains(message, "not found (http 404)")
}

func (s prViewState) HeadRepositoryNameWithOwner() string {
	if repo := strings.TrimSpace(s.HeadRepository.NameWithOwner); repo != "" {
		return repo
	}
	owner := strings.TrimSpace(firstNonEmpty(s.HeadRepository.Owner.Login, s.HeadRepoOwner.Login))
	name := strings.TrimSpace(s.HeadRepository.Name)
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

func (m *PRMergeMonitor) maybeQueueReviewFeedback(ctx context.Context, task Task, state prViewState) {
	if m.OnReviewFeedback == nil || !shouldMonitorTaskReviewFeedback(task) || !state.Open() {
		return
	}
	feedback := actionablePRReviewFeedback(task, state)
	if len(feedback.Items) == 0 || feedback.Digest == "" {
		return
	}
	if !m.beginFeedbackQueue(task, feedback.Digest) {
		return
	}
	followUpRequestID, err := m.OnReviewFeedback(ctx, task, feedback)
	if err != nil {
		m.rollbackFeedbackQueue(task, feedback.Digest)
		m.Logf("hub.ui status=warn event=pr_review_feedback request_id=%s pr_url=%s err=%q", task.RequestID, task.PRURL, err)
		return
	}
	m.Logf(
		"hub.ui status=ok event=pr_review_feedback_queued request_id=%s follow_up_request_id=%s pr_url=%s items=%d",
		task.RequestID,
		followUpRequestID,
		task.PRURL,
		len(feedback.Items),
	)
}

func (m *PRMergeMonitor) beginFeedbackQueue(task Task, digest string) bool {
	key := feedbackKeyForTask(task)
	if key == "" || strings.TrimSpace(digest) == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedbackQueued == nil {
		m.feedbackQueued = map[string]string{}
	}
	if m.feedbackQueued[key] == digest {
		return false
	}
	m.feedbackQueued[key] = digest
	return true
}

func (m *PRMergeMonitor) rollbackFeedbackQueue(task Task, digest string) {
	key := feedbackKeyForTask(task)
	if key == "" || strings.TrimSpace(digest) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedbackQueued[key] == digest {
		delete(m.feedbackQueued, key)
	}
}

func (m *PRMergeMonitor) prState(ctx context.Context, prURL string) (prViewState, error) {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return prViewState{}, fmt.Errorf("pull request url is required")
	}
	args := []string{"pr", "view", githubutil.PullRequestSelector(prURL), "--json", "state,mergedAt,url,number,title,headRefName,baseRefName,headRepository,headRepositoryOwner,reviewDecision,latestReviews,comments"}
	if repo := githubutil.PullRequestRepository(prURL); repo != "" {
		args = append(args, "--repo", repo)
	}
	res, err := m.Runner.Run(ctx, execx.Command{
		Name: "gh",
		Args: args,
	})
	if err != nil {
		return prViewState{}, err
	}
	var state prViewState
	if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &state); decodeErr != nil {
		return prViewState{}, fmt.Errorf("decode gh pr view response: %w", decodeErr)
	}
	if state.Open() {
		reviewComments, commentsErr := m.prReviewComments(ctx, prURL)
		if commentsErr != nil {
			m.Logf("hub.ui status=warn event=pr_monitor_review_comments pr_url=%s err=%q", prURL, commentsErr)
		} else {
			state.ReviewComments = reviewComments
		}
	}
	return state, nil
}

func (m *PRMergeMonitor) prReviewComments(ctx context.Context, prURL string) ([]prReviewCommentEntry, error) {
	repo := githubutil.PullRequestRepository(prURL)
	selector := githubutil.PullRequestSelector(prURL)
	if repo == "" || selector == "" {
		return nil, nil
	}
	res, err := m.Runner.Run(ctx, execx.Command{
		Name: "gh",
		Args: []string{"api", fmt.Sprintf("repos/%s/pulls/%s/comments", repo, selector), "-F", "per_page=100"},
	})
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(res.Stdout)
	if raw == "" {
		return nil, nil
	}
	var comments []prReviewCommentEntry
	if decodeErr := json.Unmarshal([]byte(raw), &comments); decodeErr != nil {
		return nil, fmt.Errorf("decode pull request review comments response: %w", decodeErr)
	}
	return comments, nil
}

func (s prViewState) Merged() bool {
	return strings.EqualFold(strings.TrimSpace(s.State), "merged") || strings.TrimSpace(s.MergedAt) != ""
}

func (s prViewState) Open() bool {
	return strings.EqualFold(strings.TrimSpace(s.State), "open")
}

func shouldMonitorTaskReviewFeedback(task Task) bool {
	if !shouldMonitorTaskPR(task) {
		return false
	}
	if normalizeTaskSource(task.Source) == "review" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(task.Workflow)) {
	case "code-review", "code_review", "pull request code review":
		return false
	default:
		return true
	}
}

func actionablePRReviewFeedback(task Task, state prViewState) PRReviewFeedback {
	feedback := PRReviewFeedback{
		PRURL:          firstNonEmpty(state.URL, task.PRURL),
		PRNumber:       state.Number,
		Title:          strings.TrimSpace(state.Title),
		HeadBranch:     strings.TrimSpace(state.HeadRefName),
		BaseBranch:     strings.TrimSpace(state.BaseRefName),
		ReviewDecision: strings.TrimSpace(state.ReviewDecision),
	}
	for _, review := range state.LatestReviews {
		body := conciseReviewFeedbackBody(review.Body)
		if body == "" {
			continue
		}
		if !reviewEntryNeedsFollowUp(review) {
			continue
		}
		feedback.Items = append(feedback.Items, PRReviewFeedbackItem{
			Kind:      "review",
			ID:        strings.TrimSpace(review.ID),
			Author:    strings.TrimSpace(review.Author.Login),
			State:     strings.TrimSpace(review.State),
			CreatedAt: strings.TrimSpace(review.SubmittedAt),
			Body:      body,
		})
	}
	for _, comment := range state.Comments {
		body := conciseReviewFeedbackBody(comment.Body)
		if body == "" || !commentBodyNeedsFollowUp(comment.Body) {
			continue
		}
		feedback.Items = append(feedback.Items, PRReviewFeedbackItem{
			Kind:      "comment",
			ID:        strings.TrimSpace(comment.ID),
			Author:    strings.TrimSpace(comment.Author.Login),
			CreatedAt: firstNonEmpty(comment.UpdatedAt, comment.CreatedAt),
			Body:      body,
		})
	}
	for _, comment := range state.ReviewComments {
		body := conciseReviewFeedbackBody(comment.Body)
		if body == "" || !commentBodyNeedsFollowUp(comment.Body) {
			continue
		}
		if location := reviewCommentLocation(comment); location != "" {
			body = location + " - " + body
		}
		feedback.Items = append(feedback.Items, PRReviewFeedbackItem{
			Kind:      "review_comment",
			ID:        fmt.Sprint(comment.ID),
			Author:    strings.TrimSpace(comment.User.Login),
			CreatedAt: firstNonEmpty(comment.UpdatedAt, comment.CreatedAt),
			Body:      body,
		})
	}
	feedback.Digest = reviewFeedbackDigest(feedback)
	return feedback
}

func reviewEntryNeedsFollowUp(review prReviewEntry) bool {
	switch strings.ToUpper(strings.TrimSpace(review.State)) {
	case "CHANGES_REQUESTED":
		return strings.TrimSpace(review.Body) != ""
	case "APPROVED":
		return reviewBodyHasActionableNegative(review.Body)
	default:
		return reviewBodyHasActionableNegative(review.Body) || commentBodyNeedsFollowUp(review.Body)
	}
}

func commentBodyNeedsFollowUp(body string) bool {
	if reviewBodyHasActionableNegative(body) {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(body))
	if normalized == "" {
		return false
	}
	needles := []string{
		"please ",
		"can you ",
		"could you ",
		"should ",
		"must ",
		"needs ",
		"need to ",
		"fix ",
		"bug",
		"regression",
		"failing",
		"fails",
		"broken",
	}
	for _, needle := range needles {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func reviewBodyHasActionableNegative(body string) bool {
	for _, bullet := range negativeReviewBullets(body) {
		if !strings.Contains(strings.ToLower(bullet), "no material issues found") {
			return true
		}
	}
	return false
}

func negativeReviewBullets(body string) []string {
	var bullets []string
	inNegative := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		heading := strings.ToLower(strings.Trim(trimmed, "*# "))
		switch heading {
		case "negative":
			inNegative = true
			continue
		case "positive":
			if inNegative {
				return bullets
			}
			continue
		}
		if inNegative && isReviewSectionHeading(trimmed) {
			return bullets
		}
		if !inNegative {
			continue
		}
		if bullet := trimFeedbackBullet(trimmed); bullet != "" {
			bullets = append(bullets, bullet)
		}
	}
	return bullets
}

func isReviewSectionHeading(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "#") {
		return true
	}
	return strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**")
}

func trimFeedbackBullet(line string) string {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func conciseReviewFeedbackBody(body string) string {
	negativeBullets := negativeReviewBullets(body)
	if len(negativeBullets) > 0 {
		return truncateFeedbackText(strings.Join(negativeBullets, "\n"))
	}
	return truncateFeedbackText(body)
}

func reviewCommentLocation(comment prReviewCommentEntry) string {
	path := strings.TrimSpace(comment.Path)
	if path == "" {
		return ""
	}
	line := comment.Line
	if line <= 0 {
		line = comment.OriginalLine
	}
	if line <= 0 {
		return path
	}
	return fmt.Sprintf("%s:%d", path, line)
}

func truncateFeedbackText(value string) string {
	const maxFeedbackChars = 6000
	value = strings.TrimSpace(value)
	if len(value) <= maxFeedbackChars {
		return value
	}
	return strings.TrimSpace(value[:maxFeedbackChars]) + "\n[truncated]"
}

func reviewFeedbackDigest(feedback PRReviewFeedback) string {
	if len(feedback.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(feedback.PRURL))
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(feedback.HeadBranch))
	b.WriteString("\n")
	for _, item := range feedback.Items {
		b.WriteString(strings.TrimSpace(item.Kind))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(item.ID))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(item.Author))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(item.State))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(item.CreatedAt))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(item.Body))
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func feedbackKeyForTask(task Task) string {
	return strings.TrimSpace(task.PRURL)
}
