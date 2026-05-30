package main

import (
	"fmt"
	"strings"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

const prReviewFeedbackCommitMessage = "fix: address pull request review feedback"

func prReviewFeedbackRunConfig(original config.Config, task web.Task, feedback web.PRReviewFeedback) (config.Config, error) {
	cfg := original
	if len(cfg.RepoList()) == 0 {
		return config.Config{}, fmt.Errorf("original task has no repository")
	}
	headBranch := firstNonEmptyString(feedback.HeadBranch, task.Branch, cfg.BaseBranch)
	if strings.TrimSpace(headBranch) == "" {
		return config.Config{}, fmt.Errorf("pull request head branch is required")
	}

	cfg.LibraryTaskName = ""
	cfg.LibraryTaskDisplayName = ""
	cfg.Review = nil
	cfg.BaseBranch = headBranch
	cfg.Prompt = prReviewFeedbackPrompt(original.Prompt, task, feedback)
	cfg.CommitMessage = prReviewFeedbackCommitMessage
	if strings.TrimSpace(cfg.PRTitle) == "" {
		cfg.PRTitle = firstNonEmptyString(feedback.Title, task.Prompt)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("validate review-feedback run config: %w", err)
	}
	return cfg, nil
}

func prReviewFeedbackPrompt(originalPrompt string, task web.Task, feedback web.PRReviewFeedback) string {
	var b strings.Builder
	b.WriteString("Update the existing pull request to address review feedback.\n\n")
	b.WriteString("Original task prompt:\n")
	b.WriteString(nonEmptyIndented(originalPrompt, "(not available)"))
	b.WriteString("\n\nPull request:\n")
	writePromptField(&b, "URL", firstNonEmptyString(feedback.PRURL, task.PRURL))
	writePromptField(&b, "Title", feedback.Title)
	writePromptField(&b, "Head branch to update", firstNonEmptyString(feedback.HeadBranch, task.Branch))
	writePromptField(&b, "Base branch", feedback.BaseBranch)
	writePromptField(&b, "Review decision", feedback.ReviewDecision)
	b.WriteString("\nReview feedback to address:\n")
	for i, item := range feedback.Items {
		b.WriteString(fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(item.Kind)))
		if author := strings.TrimSpace(item.Author); author != "" {
			b.WriteString(" by ")
			b.WriteString(author)
		}
		if state := strings.TrimSpace(item.State); state != "" {
			b.WriteString(" (")
			b.WriteString(state)
			b.WriteString(")")
		}
		b.WriteString(":\n")
		b.WriteString(nonEmptyIndented(item.Body, "(empty feedback body)"))
		b.WriteString("\n")
	}
	b.WriteString("\nRequirements:\n")
	b.WriteString("- Work on the existing pull request branch named above; do not create a separate PR.\n")
	b.WriteString("- Keep the original task scope, but make the changes needed to satisfy the review feedback.\n")
	b.WriteString("- Add or update tests when the feedback describes behavior that can regress.\n")
	b.WriteString("- Preserve unrelated code and avoid broad refactors.\n")
	b.WriteString("- Push the fix to the same PR branch so the existing pull request is updated.\n")
	return strings.TrimSpace(b.String())
}

func writePromptField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString("- ")
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\n")
}

func nonEmptyIndented(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = "  " + strings.TrimRight(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}
