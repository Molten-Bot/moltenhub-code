package failurefollowup

import (
	"path/filepath"
	"strings"
)

const RequiredPrompt = "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."

const ExecutionContract = `Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.

When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.

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
	logFileName           = "terminal.log"
	legacyTaskLogFileName = "term"
	fallbackLogSubdir     = "main"
)

func TaskLogPaths(logRoot, requestID string) []string {
	logDir, ok := TaskLogDir(logRoot, requestID)
	if !ok {
		return nil
	}
	return []string{
		logDir,
		filepath.Join(logDir, legacyTaskLogFileName),
		filepath.Join(logDir, logFileName),
	}
}

func TaskLogDir(logRoot, requestID string) (string, bool) {
	logRoot = strings.TrimSpace(logRoot)
	requestID = strings.TrimSpace(requestID)
	if logRoot == "" || requestID == "" {
		return "", false
	}

	subdir, ok := identifierSubdir(requestID)
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

func identifierSubdir(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}

	rawParts := strings.Split(id, "-")
	parts := make([]string, 0, len(rawParts))
	for _, rawPart := range rawParts {
		part := sanitizeLogPathPart(rawPart)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return fallbackLogSubdir, true
	}
	return filepath.Join(parts...), true
}

func sanitizeLogPathPart(part string) string {
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
