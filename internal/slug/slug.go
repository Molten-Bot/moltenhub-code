package slug

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
var generatedPromptSuffixRE = regexp.MustCompile(`-[0-9]{8}-[0-9]{6}(?:-[0-9a-f]{1,8})?$`)

// FromPrompt converts prompt text into a branch-safe slug.
func FromPrompt(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	lower = trimGeneratedPromptSuffix(lower)
	if lower == "" {
		return "task"
	}
	s := nonAlnum.ReplaceAllString(lower, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "task"
	}
	if len(s) > 40 {
		s = s[:40]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		return "task"
	}
	return s
}

func trimGeneratedPromptSuffix(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	trimmed := generatedPromptSuffixRE.ReplaceAllString(prompt, "")
	trimmed = strings.Trim(trimmed, "-")
	if trimmed == "" {
		return prompt
	}
	return trimmed
}

// BranchName builds a branch name with a stable prompt slug.
func BranchName(prompt string, now time.Time, guid string) string {
	slug := FromPrompt(prompt)
	_ = now
	_ = guid
	return fmt.Sprintf("moltenhub-%s", slug)
}
