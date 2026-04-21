package failurefollowup

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jef/moltenhub-code/internal/config"
)

const RequiredPrompt = "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."

const FollowUpRepositoryURL = config.DefaultRepositoryURL

const OfflineReviewInstruction = "Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours."
const FailureResponseInstruction = "When failures occur, send a response back to the calling agent that clearly states failure and includes the error details. Use explicit `Failure:` and `Error details:` fields."
const UninitializedRepoInstruction = "If a repository is not initialized after clone, use only gh CLI/git tools to create and push a main branch, then continue once git state is ready for work."
const RemoteOperationsInstruction = "Do not stop work just because you cannot create a pull request or watch remote CI/CD from inside this agent runtime. Finish the repository changes and local validation you can perform; the harness will handle PR creation/update and remote check monitoring afterward."
const ActionableChangeInstruction = "For implementation or repository-change requests, do not stop at analysis. Produce the smallest correct repository diff that satisfies the request unless you can cite concrete file evidence that the requested outcome already exists."
const NoOpInstruction = "Only return a no-op when the task is genuinely review/investigation-only or you can cite concrete repository evidence that no file changes are required. Do not force an empty PR."

var ExecutionContract = strings.Join([]string{
	FailureResponseInstruction,
	UninitializedRepoInstruction,
	RemoteOperationsInstruction,
	ActionableChangeInstruction,
	NoOpInstruction,
}, "\n\n")

var FollowUpContract = strings.Join([]string{
	OfflineReviewInstruction,
	ExecutionContract,
	fmt.Sprintf(`Harness-managed follow-up targeting:
{"repos":["%s"],"baseBranch":"main","targetSubdir":".","prompt":"%s"}`, FollowUpRepositoryURL, RequiredPrompt),
}, "\n\n")

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
	"doesn't have the rights to pull the code",
	"doesn't have the rights to push a pr",
	"refusing to allow an oauth app to create or update workflow",
	"without workflow scope",
	"without `workflow` scope",
}

var nonRemediableFailureMarkers = []string{
	"quota exceeded",
	"insufficient_quota",
	"billing",
	"401 unauthorized",
	"missing bearer or basic authentication",
	"invalid api key",
	"invalid_authentication",
	"authentication error",
	"target ui source missing",
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

func WithFollowUpContract(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return FollowUpContract
	}
	if strings.Contains(base, FollowUpContract) {
		return base
	}
	if !strings.Contains(base, OfflineReviewInstruction) {
		base += "\n\n" + OfflineReviewInstruction
	}
	if !strings.Contains(base, fmt.Sprintf(`{"repos":["%s"],"baseBranch":"main","targetSubdir":".","prompt":"%s"}`, FollowUpRepositoryURL, RequiredPrompt)) {
		base += "\n\n" + fmt.Sprintf(`Harness-managed follow-up targeting:
{"repos":["%s"],"baseBranch":"main","targetSubdir":".","prompt":"%s"}`, FollowUpRepositoryURL, RequiredPrompt)
	}
	return WithExecutionContract(base)
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

	return WithFollowUpContract(b.String())
}

func FollowUpTargeting(baseBranch, targetSubdir, currentBranch string) (string, string) {
	return "main", "."
}

func TaskLogPaths(logRoot, requestID string) []string {
	logRoot = strings.TrimSpace(logRoot)
	logDir, ok := TaskLogDir(logRoot, requestID)
	if !ok {
		return nil
	}

	paths := make([]string, 0, 5)
	seen := make(map[string]struct{}, 5)
	appendPath := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, exists := seen[path]; exists {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	appendTaskLogPaths := func(taskDir string, includeDir bool) {
		if includeDir {
			appendPath(taskDir)
		}
		appendPath(filepath.Join(taskDir, LegacyTaskLogFileName))
		appendPath(filepath.Join(taskDir, LogFileName))
	}

	appendTaskLogPaths(logDir, true)
	if shouldIncludeFallbackTaskLogs(requestID) {
		appendTaskLogPaths(filepath.Join(logRoot, FallbackLogSubdir), false)
	}

	return paths
}

func shouldIncludeFallbackTaskLogs(requestID string) bool {
	subdir, ok := IdentifierSubdir(requestID)
	if !ok {
		return true
	}
	subdir = filepath.Clean(subdir)
	localPrefix := "local" + string(filepath.Separator)
	return !(subdir == "local" || strings.HasPrefix(subdir, localPrefix))
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

func NonRemediableFailureReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return ""
	}
	for _, marker := range nonRemediableFailureMarkers {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return NonRemediableRepoAccessReason(err)
}
