package failurefollowup

import (
	"path/filepath"
	"strings"
)

const RequiredPrompt = "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."

const ExecutionContract = `Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.

When failures occur, send a response back to the calling agent that begins with "Failure:" and clearly includes the error details.

When a task fails:
- Queue a follow-up task dedicated to reviewing the logs and fixing all underlying issues in this codebase.
- Pass the relevant failing file/folder log path(s) into that follow-up task context.
- Use this follow-up run config payload shape:
  {"repos":["<same_repo_as_failed_task>"],"baseBranch":"main","targetSubdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}

Completion requirements:
- Only create a new branch when starting from 'main'; if you're fixing an existing non-'main' branch, stay on it.
- Start every new branch and every PR title with 'moltenhub-'.
- Keep working until there is a PR for your changes and required CI/CD checks are green, unless you can verify the request is already satisfied with no required code/workflow/docs changes.
- If no file changes are required, return a clear no-op result with concrete evidence instead of forcing an empty PR.
- If CI/CD fails, continue fixing code/tests/workflows until checks pass.
- If you changed multiple repositories, ensure each changed repository has its own branch and PR.
- Optimize for the highest-quality PR you can produce with focused, production-ready changes.`

const (
	LogFileName           = "terminal.log"
	LegacyTaskLogFileName = "term"
	FallbackLogSubdir     = "main"
)

var nonRemediableRepoAccessMarkers = []string{
	"write access to repository not granted",
	"requested url returned error: 403",
	"authentication failed",
	"could not read username for 'https://github.com'",
}

func WithExecutionContract(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ExecutionContract
	}
	if strings.Contains(base, ExecutionContract) {
		return base
	}
	return base + "\n\n" + ExecutionContract
}

func ComposePrompt(requiredPrompt string, logPaths, fallbackLogPaths []string, noPathGuidance, contextBlock string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(requiredPrompt))
	b.WriteString("\n\nRelevant failing log path(s):")

	appendPaths := func(paths []string) bool {
		wrote := false
		for _, path := range paths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			b.WriteString("\n- ")
			b.WriteString(path)
			wrote = true
		}
		return wrote
	}

	if !appendPaths(logPaths) && !appendPaths(fallbackLogPaths) && strings.TrimSpace(noPathGuidance) != "" {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimSpace(noPathGuidance))
	}

	contextBlock = strings.TrimSpace(contextBlock)
	if contextBlock != "" {
		b.WriteString("\n\n")
		b.WriteString(contextBlock)
	}

	return WithExecutionContract(b.String())
}

func TaskLogPaths(logRoot, requestID string) []string {
	logDir, ok := TaskLogDir(logRoot, requestID)
	if !ok {
		return nil
	}
	return []string{
		logDir,
		filepath.Join(logDir, LegacyTaskLogFileName),
		filepath.Join(logDir, LogFileName),
	}
}

func TaskLogDir(logRoot, requestID string) (string, bool) {
	logRoot = strings.TrimSpace(logRoot)
	requestID = strings.TrimSpace(requestID)
	if logRoot == "" || requestID == "" {
		return "", false
	}

	subdir, ok := IdentifierSubdir(requestID)
	if !ok {
		return "", false
	}
	subdir = filepath.Clean(subdir)
	if subdir == "." || subdir == "" || subdir == ".." {
		return "", false
	}
	if filepath.IsAbs(subdir) || strings.HasPrefix(subdir, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(logRoot, subdir), true
}

func IdentifierSubdir(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}

	rawParts := strings.Split(id, "-")
	parts := make([]string, 0, len(rawParts))
	for _, rawPart := range rawParts {
		part := SanitizeLogPathPart(rawPart)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return FallbackLogSubdir, true
	}
	return filepath.Join(parts...), true
}

func SanitizeLogPathPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}

	var b strings.Builder
	lastSeparator := false
	for i := 0; i < len(part); i++ {
		ch := part[i]
		switch {
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9'):
			b.WriteByte(ch)
			lastSeparator = false
		case ch == '.' || ch == '_' || ch == '-':
			if b.Len() == 0 || lastSeparator {
				continue
			}
			b.WriteByte(ch)
			lastSeparator = false
		default:
			if b.Len() == 0 || lastSeparator {
				continue
			}
			b.WriteByte('-')
			lastSeparator = true
		}
	}

	trimmed := strings.Trim(b.String(), ".-_")
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func NonRemediableRepoAccessReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return ""
	}
	for _, marker := range nonRemediableRepoAccessMarkers {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return ""
}
