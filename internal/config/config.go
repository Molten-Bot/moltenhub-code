package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const prTitlePrefix = "moltenhub-"

const prBodyFooter = "If you would like to connect agents together checkout [Molten Bot Hub](https://molten.bot/hub)."

// Config is the v1 public contract for a harness run.
type Config struct {
	Version       string        `json:"version"`
	RepoURL       string        `json:"repo_url"`
	Repo          string        `json:"repo"`
	Repos         []string      `json:"repos"`
	BaseBranch    string        `json:"base_branch"`
	TargetSubdir  string        `json:"target_subdir"`
	Prompt        string        `json:"prompt"`
	Images        []PromptImage `json:"images,omitempty"`
	CommitMessage string        `json:"commit_message"`
	PRTitle       string        `json:"pr_title"`
	PRBody        string        `json:"pr_body"`
	Labels        []string      `json:"labels"`
	GitHubHandle  string        `json:"github_handle"`
	Reviewers     []string      `json:"reviewers"`
}

// PromptImage captures one prompt image attachment.
type PromptImage struct {
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

// Load reads and validates a JSON/JSONC config from disk.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	cleaned := stripLineComments(data)
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(cleaned))
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config json: %w", err)
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ApplyDefaults normalizes aliases and fills optional values.
func (c *Config) ApplyDefaults() {
	c.Version = strings.TrimSpace(c.Version)
	if c.Version == "" {
		c.Version = "v1"
	}

	repos := c.RepoList()
	c.Repos = repos
	if len(repos) > 0 {
		c.RepoURL = repos[0]
		c.Repo = repos[0]
	}

	c.BaseBranch = strings.TrimSpace(c.BaseBranch)
	if c.BaseBranch == "" {
		c.BaseBranch = "main"
	}

	c.TargetSubdir = strings.TrimSpace(c.TargetSubdir)
	if c.TargetSubdir == "" {
		c.TargetSubdir = "."
	}

	c.Prompt = strings.TrimSpace(c.Prompt)
	c.Images = normalizePromptImages(c.Images)
	c.GitHubHandle = normalizeReviewer(c.GitHubHandle)
	c.Reviewers = mergeReviewers(c.Reviewers, c.GitHubHandle)

	if strings.TrimSpace(c.CommitMessage) == "" {
		c.CommitMessage = defaultCommitMessage(c.Prompt)
	}
	if strings.TrimSpace(c.PRTitle) == "" {
		c.PRTitle = defaultPRTitle(c.Prompt)
	} else {
		c.PRTitle = prefixedPRTitle(c.PRTitle)
	}
	if strings.TrimSpace(c.PRBody) == "" {
		c.PRBody = defaultPRBody(c.Prompt)
	} else {
		c.PRBody = ensurePRBodyFooter(c.PRBody)
	}
}

// Validate checks required values and path safety.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if c.Version != "v1" {
		return fmt.Errorf("unsupported version %q", c.Version)
	}
	repos := c.RepoList()
	if len(repos) == 0 {
		return fmt.Errorf("one of repo, repo_url, or repos[] is required")
	}
	for _, repo := range repos {
		if err := validateRepoRef(repo); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.BaseBranch) == "" {
		return fmt.Errorf("base_branch is required")
	}
	if strings.TrimSpace(c.TargetSubdir) == "" {
		return fmt.Errorf("target_subdir is required")
	}
	if err := validateSubdir(c.TargetSubdir); err != nil {
		return err
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	for i, image := range c.Images {
		if err := validatePromptImage(image, i); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.CommitMessage) == "" {
		return fmt.Errorf("commit_message is required")
	}
	if strings.TrimSpace(c.PRTitle) == "" {
		return fmt.Errorf("pr_title is required")
	}
	if strings.TrimSpace(c.PRBody) == "" {
		return fmt.Errorf("pr_body is required")
	}
	return nil
}

// RepoList returns the normalized list of repositories for this run.
func (c Config) RepoList() []string {
	repoURL := strings.TrimSpace(c.RepoURL)
	if repoURL == "" && strings.TrimSpace(c.Repo) != "" {
		repoURL = strings.TrimSpace(c.Repo)
	}

	repos := normalizeNonEmptyStrings(c.Repos)
	if repoURL != "" {
		repos = prependIfMissing(repos, repoURL)
	}
	return repos
}

func normalizePromptImages(images []PromptImage) []PromptImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]PromptImage, 0, len(images))
	for _, image := range images {
		image.Name = strings.TrimSpace(image.Name)
		image.MediaType = strings.TrimSpace(image.MediaType)
		image.DataBase64 = strings.TrimSpace(image.DataBase64)
		if image.Name == "" && image.MediaType == "" && image.DataBase64 == "" {
			continue
		}
		out = append(out, image)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validatePromptImage(image PromptImage, index int) error {
	if image.DataBase64 == "" {
		return fmt.Errorf("images[%d].data_base64 is required", index)
	}
	if image.MediaType != "" && !strings.HasPrefix(strings.ToLower(image.MediaType), "image/") {
		return fmt.Errorf("images[%d].media_type must be an image MIME type", index)
	}
	if _, err := base64.StdEncoding.DecodeString(image.DataBase64); err != nil {
		return fmt.Errorf("images[%d].data_base64 is invalid base64: %w", index, err)
	}
	return nil
}

func validateSubdir(subdir string) error {
	clean := filepath.Clean(subdir)
	if clean == "" {
		return fmt.Errorf("target_subdir must be a relative path")
	}
	if filepath.IsAbs(clean) {
		return fmt.Errorf("target_subdir must be relative")
	}
	if strings.HasPrefix(clean, "..") {
		return fmt.Errorf("target_subdir cannot escape repository root")
	}
	if strings.Contains(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target_subdir cannot contain parent traversals")
	}
	return nil
}

func validateRepoRef(repo string) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return fmt.Errorf("repository URL is required")
	}
	if strings.ContainsAny(repo, " \t\r\n") {
		return fmt.Errorf("invalid repository URL %q: whitespace is not allowed", repo)
	}

	// scp-like git SSH syntax is valid and intentionally does not parse as a URL:
	// git@github.com:owner/repo.git
	if !strings.Contains(repo, "://") {
		return nil
	}

	parsed, err := url.Parse(repo)
	if err != nil {
		msg := strings.ToLower(strings.TrimSpace(err.Error()))
		if strings.HasPrefix(strings.ToLower(repo), "ssh://") && strings.Contains(msg, "invalid port") {
			return fmt.Errorf(
				"invalid repository URL %q: mixed SSH URL styles. Use either git@host:owner/repo.git or ssh://git@host/owner/repo.git",
				repo,
			)
		}
		return fmt.Errorf("invalid repository URL %q: %v", repo, err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "ssh", "http", "https", "git":
		if strings.TrimSpace(parsed.Host) == "" {
			return fmt.Errorf("invalid repository URL %q: missing host", repo)
		}
		if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
			return fmt.Errorf("invalid repository URL %q: missing repository path", repo)
		}
	case "file":
		if strings.TrimSpace(parsed.Path) == "" {
			return fmt.Errorf("invalid repository URL %q: missing filesystem path", repo)
		}
	}

	return nil
}

func defaultCommitMessage(prompt string) string {
	summary := summarize(prompt, 64)
	if summary == "" {
		return "chore: automated update"
	}
	return "chore: " + summary
}

func defaultPRTitle(prompt string) string {
	summary := summarize(prompt, 72)
	if summary == "" {
		summary = "Automated update"
	}
	return prefixedPRTitle(summary)
}

func defaultPRBody(prompt string) string {
	summary := summarize(prompt, 500)
	if summary == "" {
		return ensurePRBodyFooter("Automated change generated by MoltenHub Code.")
	}
	return ensurePRBodyFooter("Automated change generated by MoltenHub Code.\n\nPrompt:\n" + summary)
}

var wsRE = regexp.MustCompile(`\s+`)
var generatedPRTitleSuffixRE = regexp.MustCompile(`-[0-9]{8}-[0-9]{6}(?:-[0-9a-fA-F]{1,8})?$`)

func prefixedPRTitle(title string) string {
	title = trimGeneratedPRTitleSuffix(strings.TrimSpace(title))
	if title == "" {
		return prTitlePrefix + "Automated update"
	}
	if strings.HasPrefix(strings.ToLower(title), prTitlePrefix) {
		return title
	}
	return prTitlePrefix + title
}

func trimGeneratedPRTitleSuffix(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	trimmed := generatedPRTitleSuffixRE.ReplaceAllString(title, "")
	trimmed = strings.TrimSpace(strings.TrimRight(trimmed, "-_."))
	if trimmed == "" {
		return title
	}
	return trimmed
}

func ensurePRBodyFooter(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return prBodyFooter
	}
	if strings.Contains(body, "https://molten.bot/hub") {
		return body
	}
	return body + "\n\n" + prBodyFooter
}

func summarize(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = wsRE.ReplaceAllString(s, " ")
	if len(s) <= max {
		return strings.Trim(s, ",.;:-")
	}

	s = s[:max]
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ",.;:-")
	return s
}

func stripLineComments(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}

		// Drop JSONC-style single-line comments.
		if ch == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) && data[i] == '\n' {
				out = append(out, '\n')
			}
			continue
		}

		out = append(out, ch)
	}

	return out
}

func normalizeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func prependIfMissing(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append([]string{value}, values...)
}

func mergeReviewers(reviewers []string, githubHandle string) []string {
	normalized := normalizeReviewerList(reviewers)
	handle := normalizeReviewer(githubHandle)
	if handle == "" {
		return normalized
	}
	for _, reviewer := range normalized {
		if strings.EqualFold(reviewer, handle) {
			return normalized
		}
	}
	return append([]string{handle}, normalized...)
}

func normalizeReviewerList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := normalizeReviewer(value)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeReviewer(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "@")
	return strings.TrimSpace(value)
}
