package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidatePromptImageValidationPaths(t *testing.T) {
	t.Parallel()

	if err := validatePromptImage(PromptImage{}, 0); err == nil {
		t.Fatal("validatePromptImage(empty) error = nil, want non-nil")
	}
	if err := validatePromptImage(PromptImage{
		MediaType:  "text/plain",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("x")),
	}, 1); err == nil {
		t.Fatal("validatePromptImage(non-image mediaType) error = nil, want non-nil")
	}
	if err := validatePromptImage(PromptImage{
		MediaType:  "image/png",
		DataBase64: "%%%not-base64%%%",
	}, 2); err == nil {
		t.Fatal("validatePromptImage(invalid base64) error = nil, want non-nil")
	}
	if err := validatePromptImage(PromptImage{
		MediaType:  "image/png",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("hello")),
	}, 3); err != nil {
		t.Fatalf("validatePromptImage(valid) error = %v", err)
	}
}

func TestValidateSubdirAndRepoRefEdgeCases(t *testing.T) {
	t.Parallel()

	if err := validateSubdir("/abs/path"); err == nil {
		t.Fatal("validateSubdir(abs) error = nil, want non-nil")
	}
	if err := validateSubdir("../../escape"); err == nil {
		t.Fatal("validateSubdir(escape) error = nil, want non-nil")
	}
	if err := validateSubdir("nested/../safe"); err != nil {
		t.Fatalf("validateSubdir(clean relative) error = %v", err)
	}

	if err := validateRepoRef(" "); err == nil {
		t.Fatal("validateRepoRef(empty) error = nil, want non-nil")
	}
	if err := validateRepoRef("https://github.com"); err == nil {
		t.Fatal("validateRepoRef(missing path) error = nil, want non-nil")
	}
	if err := validateRepoRef("file://"); err == nil {
		t.Fatal("validateRepoRef(file missing path) error = nil, want non-nil")
	}
	if err := validateRepoRef("git@github.com:acme/repo.git"); err != nil {
		t.Fatalf("validateRepoRef(scp syntax) error = %v", err)
	}
}

func TestSummarizeAndFirstNonEmptyTrimmed(t *testing.T) {
	t.Parallel()

	if got := summarize("   ", 12); got != "" {
		t.Fatalf("summarize(empty) = %q, want empty", got)
	}
	if got := summarize("alpha beta gamma delta", 12); got != "alpha beta" {
		t.Fatalf("summarize(max=12) = %q, want %q", got, "alpha beta")
	}
	if got := summarize("value,,,", 32); got != "value" {
		t.Fatalf("summarize(trailing punctuation) = %q, want %q", got, "value")
	}

	if got := firstNonEmptyTrimmed(" ", "\n", " value "); got != "value" {
		t.Fatalf("firstNonEmptyTrimmed() = %q, want %q", got, "value")
	}
}

func TestTrimGeneratedPRTitleSuffixAndEnsureFooter(t *testing.T) {
	t.Parallel()

	if got := trimGeneratedPRTitleSuffix("cleanup-20260407-002959"); got != "cleanup" {
		t.Fatalf("trimGeneratedPRTitleSuffix() = %q, want %q", got, "cleanup")
	}
	if got := trimGeneratedPRTitleSuffix("release---"); got != "release" {
		t.Fatalf("trimGeneratedPRTitleSuffix(no generated suffix) = %q, want %q", got, "release")
	}
	if got := ensurePRBodyFooter("body\n\n" + prBodyFooter); strings.Count(got, "https://molten.bot/hub") != 1 {
		t.Fatalf("ensurePRBodyFooter(contains footer) duplicated link: %q", got)
	}
}
