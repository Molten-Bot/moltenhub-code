# MoltenHub Code

MoltenHub Code is a small Go harness that runs Codex against one or more repositories, opens PRs, and waits for required checks.
It supports single runs, parallel local runs, and a persistent MoltenHub listener with a local monitoring UI.

## What It Does

For each run:

1. Verifies required tools (`git`, `gh`, `codex`) and GitHub auth.
2. Creates an isolated workspace (`/dev/shm/temp/<guid>`, fallback `/tmp/temp/<guid>`).
3. Seeds `AGENTS.md` from [`library/AGENTS.md`](library/AGENTS.md).
4. Clones configured repos and checks out `base_branch`.
5. Runs Codex in `target_subdir` (or workspace root for multi-repo runs).
6. For changed repos:
   - If `base_branch` is `main`, creates a `moltenhub-*` branch.
   - Otherwise reuses the existing non-`main` branch.
7. Creates or reuses PRs with `moltenhub-*` titles.
8. Watches required CI checks and performs remediation retries when checks fail.

If a task fails, no PR is created for that run, and the workspace path is logged.

## Commands

Build:

```bash
go build -o bin/harness ./cmd/harness
```

## Container

Build a container image:

```bash
docker build -t moltenhub-code:latest .
```

GitHub Actions publish flow:

- `deploy-vnext` runs automatically on pushes to `main` (including PR merges) and publishes:
  - `moltenai/moltenhub-code:vnext`
  - `moltenai/moltenhub-code:<yyyy.mm.dd.run_number>` (example: `2026.04.04.5`)
- `deploy-prod` is manual-only (`workflow_dispatch`) and promotes a selected source tag (default `vnext`) to `moltenai/moltenhub-code:latest` without rebuilding
- required repository secret: `DOCKERHUB_TOKEN`

Run with PAT auth via `GITHUB_TOKEN`:

```bash
docker run --rm -it \
  -e GITHUB_TOKEN=ghp_xxx \
  -v "$PWD:/workspace" \
  -w /workspace \
  moltenhub-code:latest \
  harness run --config templates/run.example.json
```

Container startup pre-registers auth before any Codex stage:

- maps `GITHUB_TOKEN` to `GH_TOKEN` for `gh` commands
- runs `gh auth status` and `gh auth setup-git`
- configures GitHub URL rewrites so `git@github.com:*` and `ssh://git@github.com/*` can use PAT-backed HTTPS

Single run:

```bash
./bin/harness run --config templates/run.example.json
```

Parallel local runs:

```bash
./bin/harness multiplex --config ./tasks --parallel 4
```

Hub listener:

```bash
./bin/harness hub --init templates/init.example.json
```

## UI

Hub mode starts a local monitor UI by default at `http://127.0.0.1:7777`.

The local prompt panel defaults to a schema builder that stores requested repositories in browser local storage and reuses them as a repo picker. In Builder mode, you can paste clipboard PNG screenshots into the prompt field and they will be attached to the initial Codex run. Raw JSON mode remains available for advanced or multi-repo payloads.

Automatic mode is available as a runtime flag and hides the browser-local prompt form entirely:

```bash
./bin/harness hub --init templates/init.example.json --ui-automatic
```

Override or disable:

```bash
./bin/harness hub --init templates/init.example.json --ui-listen :8088
./bin/harness hub --init templates/init.example.json --ui-listen ""
```

## Run Config (`v1`)

Required:

- one of `prompt` or `library_task_name`
- one of `repo`, `repo_url`, or `repos`

Common optional fields:

- `base_branch` (default `main`)
- `branch` (alias for `base_branch`, mainly for library-backed skill calls)
- `target_subdir` (default `.`)
- `commit_message`
- `pr_title` (auto-prefixed with `moltenhub-`)
- `pr_body`
- `labels`
- `github_handle` (single GitHub reviewer alias; mapped to PR reviewer)
- `reviewers`

Example: [`templates/run.example.json`](templates/run.example.json)

Library-backed runs can also use:

```json
{
  "repo": "git@github.com:acme/target-repo.git",
  "branch": "main",
  "library_task_name": "unit-test-coverage"
}
```

## Hub Init Config (`v1`)

Key fields:

- `base_url` (default `https://na.hub.molten.bot/v1`)
- `bind_token` or `agent_token`
- `session_key` (default `main`)
- `profile.*`
- `skill.*`
- `dispatcher.*` (adaptive worker parallelism)

Example: [`templates/init.example.json`](templates/init.example.json)

## Logs And Failure Follow-Up

Runtime logs are mirrored to `.log`:

- aggregate stream: `.log/terminal.log`
- per task/request stream: `.log/<identifier parts>/terminal.log`

When a task fails (local or hub-dispatched), the harness queues a follow-up local task that:

- includes relevant failing log paths in prompt context
- uses run config shape: `{"repos":["git@github.com:jefking/moltenhub-code.git"],"base_branch":"main","target_subdir":".","prompt":"..."}`
- asks for root-cause fixes (not superficial bandaids)

## Exit Codes

- `0` success
- `2` usage error
- `10` config error
- `20` preflight/tooling error
- `21` auth error
- `30` workspace error
- `40` clone error
- `50` Codex execution error
- `60` git workflow error
- `70` PR/checks error

## Test

```bash
go test ./...
```
