# PR 603 Review

## Summary
- Updates task progress agent-logo rendering so the agent step gets an `is-agent-logo` class when its progress icon key is `agent`.
- Adds task progress logo filter variables and dark/night active styling so the active agent logo is white, with a muted grey treatment while inactive.
- Adds string-based stylesheet assertions in `internal/hubui/server_additional_test.go`.
- Also includes an unrelated harness auth-gate refactor that centralizes configurable auth state helpers and wrapped JSON decoding.

## PR Discussion
- GitHub PR context reviewed: PR [#603](https://github.com/Molten-Bot/moltenhub-code/pull/603), base `main`, head `moltenhub-agent-logo-is-not-visible-in-dark-mode-s`.
- Existing PR comments: none.
- Existing PR reviews: none.

## Findings

### Medium
1. **Existing hub UI test assertion no longer matches the CSS, so CI will fail once Go tests run.**
- Evidence:
  - The PR inserts `filter: var(--task-progress-logo-active-filter);` into the active icon block at `internal/hubui/static/style.css:2483`.
  - Existing test `TestStaticStyleIncludesTaskSurfaceStyles` still checks for a contiguous block with `width`, `height`, then `opacity` and closing brace at `internal/hubui/server_test.go:1903`.
  - With the new `filter` line between `height` and `opacity`, that literal `strings.Contains` check cannot match the stylesheet.
- Impact:
  - `go test ./internal/hubui` should fail in environments with Go installed, blocking the PR even though the visual change may be correct.
- Recommendation:
  - Update the existing assertion to include the new `filter` line, or make the assertion less brittle by checking the active icon selector and key declarations separately.

## Open Questions
- None.

## Validation
- Reviewed diff against `origin/main`.
- Reviewed affected renderer and CSS around `internal/hubui/static/index.html:6484`, `internal/hubui/static/style.css:2416`, and `internal/hubui/static/style.css:2480`.
- Reviewed shared auth helper changes around `cmd/harness/auth_gate_shared.go:64`, `cmd/harness/auth_gate_shared.go:71`, and `cmd/harness/auth_gate_shared.go:334`.
- Reviewed PR discussion with `gh pr view --json comments,reviews`; no comments or reviews were present.
- `git diff --check origin/main...HEAD` passed.
- Attempted `go test ./internal/hubui`, but Go is unavailable in this runtime: `/bin/sh: go: not found`.

## Conclusion
- Material issue found: test assertion update is needed before merge.
