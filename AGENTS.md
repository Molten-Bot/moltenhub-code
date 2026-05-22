# AGENTS.md

## Scope

- These instructions apply to the whole repository. A nested `AGENTS.md` closer to the edited file takes precedence for that subtree.
- This file is repository-local guidance for Molten Hub Code. The separate `library/AGENTS.md` file is the runtime seed copied into task workspaces for agents working on customer repositories.
- Keep agent instructions evidence-backed. Add commands, paths, and conventions only when they are present in source, tests, docs, Dockerfiles, or CI.

## Project Overview

- Molten Hub Code is a Go module and CLI for running AI coding agents against GitHub repositories and turning prompts into pull requests.
- The executable lives in `cmd/harness`. Shared behavior belongs in focused `internal/*` packages: `app`, `agentruntime`, `config`, `execx`, `hub`, `library`, `multiplex`, `slug`, `web`, and `workspace`.
- The local web UI is served from embedded files under `internal/web/static`; there is no separate npm frontend build pipeline in this repo.

## Commands

- Use Go `1.26.2` or newer, as declared in `go.mod`.
- Match CI before finishing broad changes:
  - `go mod download`
  - `go build ./...`
  - `go build -o bin/harness ./cmd/harness`
  - `go test ./...`
- For focused validation, prefer the smallest relevant package or test first, for example `go test ./internal/config` or `go test ./internal/app -run TestName`, then broaden when the change has wider impact.
- Build and run the local harness with `go build -o bin/harness ./cmd/harness` and `./bin/harness hub`.
- CLI modes are `harness run --config <path-to-json>`, `harness multiplex --config <path-or-dir> [--parallel <n>]`, and `harness hub [--init <path-to-init-json> | --config <path-to-config-json>] [--parallel <n>] [--ui-listen <host:port>] [--ui-automatic]`.

## Architecture

- Keep CLI argument parsing and terminal concerns in `cmd/harness`; keep reusable orchestration in `internal/app`, hub transport/runtime logic in `internal/hub`, and agent-specific command construction in `internal/agentruntime`.
- The main harness flow is preflight/auth/workspace/clone, agent runtime execution, git commit/push, PR creation, and optional CI remediation. Preserve that staged logging style when extending the flow.
- Prefer dependency injection over process globals for behavior that tests need to control. Existing patterns include injectable runners, clocks, sleep functions, filesystem callbacks, and HTTP test servers.
- Use `internal/execx.Runner` or `StreamRunner` for subprocess work in higher-level packages instead of direct `os/exec` calls.
- Keep package boundaries small and explicit. Avoid introducing cross-package cycles or broad utility packages when an existing focused package already owns the behavior.
- Use explicit error messages with context. Preserve existing sentinel errors and exit-code behavior when changing harness failure paths.

## Config And Contracts

- Run config JSON uses camelCase fields such as `repoUrl`, `targetSubdir`, `agentHarness`, `agentCommand`, `responseMode`, `libraryTaskName`, `githubHandle`, and `prTitle`. Snake_case run config fields are rejected. JSONC-style line comments are supported.
- Hub init and persisted runtime config use snake_case fields such as `base_url`, `bind_token`, `agent_token`, `session_key`, and `github_token`. The default hub endpoint is `https://na.hub.molten.bot/v1` and the default session key is `main`.
- Supported agent harnesses are `codex` and `claude`; default is `codex`. Prompt images are Codex-only. Honor `HARNESS_AGENT_HARNESS` and `HARNESS_AGENT_COMMAND`.
- Supported response modes are `default`, `off`, `caveman-lite`, `caveman-full`, `caveman-ultra`, `caveman-wenyan-lite`, `caveman-wenyan-full`, and `caveman-wenyan-ultra`; omitted or `default` maps to `caveman-full`.
- Library task definitions live in `library/*.json`, use strict camelCase task fields, reject unknown fields, and should keep one top-level task per checked-in file.
- The OpenAPI snapshot in `na.hub.molten.bot.openapi.yaml` guards hub transport contracts. Update it deliberately when the API contract changes.

## Tests

- Prefer focused table or simple unit tests in the package being changed. Use `t.Parallel()` when the test does not mutate shared process state.
- Use `t.TempDir()`, fake `execx` runners, injected clocks/filesystem hooks, and `httptest` servers instead of real external services.
- When environment variables or global process state matter, reset them carefully; several packages use `TestMain` for shared cleanup.
- Keep tests aligned with public behavior and contracts rather than incidental implementation details, especially around CLI output, config validation, and hub API fallbacks.

## Security

- Never print, commit, or include in docs: `GITHUB_TOKEN`, `GH_TOKEN`, `MOLTEN_HUB_TOKEN`, `OPENAI_API_KEY`, GitHub PATs, bind tokens, agent tokens, or agent auth credentials.
- Before sharing repository or PR links in runtime activity, preserve the existing private-repo check using `gh repo view OWNER/REPO --json isPrivate,nameWithOwner`.
- Production hub base URLs must stay on HTTPS Molten Hub `/v1` endpoints. Non-Molten base URLs are for explicit local/test use behind `HARNESS_ALLOW_NON_MOLTEN_HUB_BASE_URL`.

## Release

- Do not invent release flows. `deploy-vnext` verifies the same build/test commands before publishing Docker `vnext`; `deploy-prod` creates semver `vX.Y.Z` tags through GoReleaser and promotes Docker `latest`.
- Do not run publishing, tagging, Docker promotion, or credential-affecting commands unless the user explicitly asks for a release operation.

## PR Expectations

- Keep diffs focused on the requested behavior or documentation change.
- Summaries should include what changed, what was validated, and any remaining risk or validation gap.
- For library prompt changes, ensure `internal/library` tests still pass and that checked-in JSON remains canonical.
