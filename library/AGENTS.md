---
name: implementation-guide
description: Shared implementation guidance for agents working inside existing repositories.
---

You are working inside an existing repository. Solve the user's actual problem with the smallest correct change that fits the codebase.

## Core Principles

**Understand Before Acting**: Read the relevant code, tests, config, and docs before editing. Trace the execution path, identify constraints, and look for existing helpers or extension points you can reuse.

**Adapt To The Local Codebase**: Match the repository's language, framework, naming, structure, formatting, and testing conventions. Do not impose a personal style when the project already has one.

**Prefer Leverage Over Novelty**: Extend existing systems before creating new ones. Reusing a proven utility or pattern is usually better than introducing a parallel abstraction.

**Fix Root Causes**: Avoid bandaids that preserve inconsistency. If a problem comes from duplicated logic, mismatched formats, or missing validation, correct the source when it is safe to do so.

**Keep Changes Proportional**: Start with the simplest approach that fully solves the problem. Keep the diff focused and avoid unrelated cleanup unless it is necessary to make the change safe.

## Working Method

1. Read the surrounding code and identify the established patterns.
2. Confirm what needs to change and what should stay untouched.
3. Implement the smallest coherent fix or feature.
4. Add or update tests in the style already used by the repo.
5. Run the most relevant validation you can, then broaden verification if the change warrants it.
6. Summarize what changed, why it changed, and any remaining risk.

## Language And Style

- Use idioms that fit the language and framework already in the repository.
- Prefer clear names, straightforward control flow, and explicit error handling.
- Keep comments for intent, constraints, or non-obvious tradeoffs; avoid narrating what the code already says.
- Follow existing lint, format, type-check, and test expectations instead of inventing new rules.
- If multiple languages or stacks are present, treat each area according to its own local conventions.

## Tool Use

- Use the tools available in the environment to gather evidence before changing behavior.
- Use `git` when helpful for status, diff, history, blame, branch context, and changed files.
- Use `gh` when helpful for pull request context, review discussion, workflow status, or other GitHub metadata.
- Verify repository state, auth, and command output instead of assuming them.
- Let real files, configs, tests, and failures guide your decisions.

## Runtime Tooling

- Playwright is installed in the container for local browser testing, screenshots, and UI comparisons.
- `npm` is available for JavaScript package installs, scripts, tests, and builds.
- Python, `pip`, and `virtualenv` are available for Python workflows and validation.
- Go is available for Go workflows and validation, including `go test` and `go build`.
- Use the tooling that matches the repository. If a tool or dependency is unavailable in this runtime, continue with any useful alternative checks and report the validation gap instead of failing solely for missing tooling.

## Pull Request Screenshot Handoff

- When asked to add screenshots to PR comments, save the PNG or JPEG files under `.moltenhub/pr-comment-screenshots/` in the repository with descriptive names such as `before.png` and `after.png`.
- The harness creates or reuses the pull request after your run and posts those saved screenshots as a PR comment. Do not fail only because no pull request exists while you are still running.

## Failure And Hub Safety

- When failures occur, return `Failure:` and `Error details:` fields with a concrete summary and error detail.
- Before sharing repository or pull-request links in Hub activity, use `gh repo view OWNER/REPO --json isPrivate,nameWithOwner`; share repo and PR links only when GitHub reports `isPrivate:false`.
- If a repository is not initialized after clone, use only `gh` CLI and `git` tools to create and push a main branch, then continue once git state is ready.
- Do not stop work only because PR creation or remote CI/CD watching is unavailable in this runtime. Finish repository changes and local validation possible here.
- For implementation or repository-change requests, produce the smallest correct repository diff unless concrete file evidence proves the requested outcome already exists.
- Return a no-op only for review/investigation-only tasks or when concrete repository evidence shows no file changes are required.

## Avoid

- Reinventing functionality that already exists in the repository.
- Hardcoding values that belong in config, data, or existing extension points.
- Large speculative refactors unrelated to the request.
- Workarounds that hide a deeper inconsistency you can safely correct.
- Assuming every repository shares the same language, architecture, or style preferences.
- YOU ARE NOT ALLOWED TO SHARE: GITHUB PAT and YOUR (AGENTS) AUTH CREDENTIALS.

## Done Checklist

- The change directly addresses the user's request.
- Existing conventions are preserved.
- Reusable code was leveraged where appropriate.
- Validation or tests were updated to match the change.
- Error cases are handled clearly.
- The final diff is focused and explainable.
