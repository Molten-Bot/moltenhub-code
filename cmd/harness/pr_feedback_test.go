package main

import (
	"strings"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/web"
)

func TestPRReviewFeedbackRunConfigUpdatesExistingPRBranch(t *testing.T) {
	t.Parallel()

	original := config.Config{
		RepoURL:       "git@github.com:acme/repo.git",
		BaseBranch:    "main",
		TargetSubdir:  ".",
		Prompt:        "Build keyboard-accessible custom themes.",
		CommitMessage: "feat: build custom themes",
		PRTitle:       "Build custom themes",
		PRBody:        "Original body",
		Reviewers:     []string{"octocat"},
	}
	original.ApplyDefaults()

	task := web.Task{
		RequestID: "req-117",
		PRURL:     "https://github.com/acme/repo/pull/117",
		Branch:    "moltenhub-custom-themes",
	}
	feedback := web.PRReviewFeedback{
		PRURL:          "https://github.com/acme/repo/pull/117",
		Title:          "Build custom themes",
		HeadBranch:     "moltenhub-custom-themes",
		BaseBranch:     "main",
		ReviewDecision: "REVIEW_REQUIRED",
		Items: []web.PRReviewFeedbackItem{{
			Kind:   "review",
			Author: "reviewer",
			State:  "COMMENTED",
			Body:   "[Medium] src/main.jsx:1139 - Custom theme radiogroup lacks arrow-key navigation.",
		}},
	}

	cfg, err := prReviewFeedbackRunConfig(original, task, feedback)
	if err != nil {
		t.Fatalf("prReviewFeedbackRunConfig() error = %v", err)
	}
	if got, want := cfg.BaseBranch, "moltenhub-custom-themes"; got != want {
		t.Fatalf("cfg.BaseBranch = %q, want %q", got, want)
	}
	if cfg.LibraryTaskName != "" || cfg.Review != nil {
		t.Fatalf("cfg kept review/library mode: library=%q review=%#v", cfg.LibraryTaskName, cfg.Review)
	}
	if got, want := cfg.CommitMessage, prReviewFeedbackCommitMessage; got != want {
		t.Fatalf("cfg.CommitMessage = %q, want %q", got, want)
	}
	for _, want := range []string{
		"Original task prompt:",
		"Build keyboard-accessible custom themes.",
		"Head branch to update: moltenhub-custom-themes",
		"Custom theme radiogroup lacks arrow-key navigation",
		"do not create a separate PR",
	} {
		if !strings.Contains(cfg.Prompt, want) {
			t.Fatalf("cfg.Prompt missing %q:\n%s", want, cfg.Prompt)
		}
	}
}

func TestTaskStartSourceForSubmissionTreatsPRFeedbackAsReviewSource(t *testing.T) {
	t.Parallel()

	if got, want := taskStartSourceForSubmission(prReviewFeedbackSource, config.Config{}), "review"; got != want {
		t.Fatalf("taskStartSourceForSubmission(prReviewFeedbackSource) = %q, want %q", got, want)
	}
}
