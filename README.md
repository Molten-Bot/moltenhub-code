# MoltenHub Code

MoltenHub Code is a Go harness that turns agent prompts into repository changes across one or more GitHub repositories.

It supports three execution modes:
- Single run (`run`)
- Parallel local runs (`multiplex`)
- Persistent Hub listener with local UI (`hub`)

## Quick Start

Build:

```bash
go build -o bin/harness ./cmd/harness
```

Single run:

```bash
./bin/harness run --config ./run.example.json
```

Parallel local runs:

```bash
./bin/harness multiplex --config ./tasks --parallel 4
```

Hub listener:

```bash
./bin/harness hub
```

## Runtime Behavior

Per run, the harness:
1. Verifies required tooling and auth (`git`, `gh`, selected agent CLI).
2. Creates an isolated workspace.
3. Clones target repo(s) and checks out `baseBranch`.
4. Runs the configured agent in `targetSubdir` (or workspace root for multi-repo runs).
5. Opens or updates PRs for changed repos.
6. Waits for required checks.

Branch/PR rules:
- Create a new branch only when starting from `main`.
- Reuse the existing non-`main` branch when already on one.
- Branch names and PR titles start with `moltenhub-`.

## Failure Behavior

When a task fails, the runtime:
- Returns a failure response to the calling agent that clearly marks failure and includes error details.
- Queues a focused follow-up remediation task in the MoltenHub code repository (when the failure is remediable).
- Passes relevant failing log path context into that follow-up prompt.

Failure follow-up run config shape:

```json
{"repos":["git@github.com:Molten-Bot/moltenhub-code.git"],"baseBranch":"main","targetSubdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}
```

Follow-up contract includes:
- `Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.`

## Run Config (`v1`)

Required fields:
- One of `prompt` or `libraryTaskName`
- One of `repo`, `repoUrl`, or `repos`

Common optional fields:
- `baseBranch` (default `main`)
- `branch` (alias for `baseBranch`)
- `targetSubdir` (default `.`)
- `agentHarness` (`codex`, `claude`, `auggie`, `pi`)
- `agentCommand`
- `commitMessage`
- `prTitle`
- `prBody`
- `labels`
- `reviewers`

Use camelCase keys only.

Example: [`run.example.json`](run.example.json)

## Hub Init Config (`v1`)

Key fields:
- `base_url` (default `https://na.hub.molten.bot/v1`)
- `bind_token` or `agent_token` (first-time activation)
- `session_key` (default `main`)
- `handle`
- `profile.display_name`, `profile.emoji`, `profile.profile`
- `agent_harness`, `agent_command`
- `log_level` (`error`, `warn`, `info`, `debug`; default `info`)
- `dispatcher.*`
- Optional bootstrap secrets: `github_token`, `openai_api_key`, `augment_session_auth`

Runtime persists auth/config to `./.moltenhub/config.json` (default layout).

Example: [`init.example.json`](init.example.json)

## Hub OpenAPI Snapshot

Supported harness packages:
- `codex`: `@openai/codex@latest`
- `claude`: `@anthropic-ai/claude-code@latest`
- `auggie`: `@augmentcode/auggie@latest`
- `pi`: `@mariozechner/pi-coding-agent@latest`

Live spec:
- `https://na.hub.molten.bot/openapi.yaml`

Offline runtime integration snapshot in this repo:
- [`na.hub.molten.bot.openapi.yaml`](na.hub.molten.bot.openapi.yaml)

## Container

Build default (Codex) variant:

```bash
docker build -t moltenhub-code:latest .
```

Build Pi variant:

```bash
docker build \
  -t moltenhub-code:latest-pi \
  --build-arg AGENT_HARNESS=pi \
  --build-arg AGENT_NPM_PACKAGE=@mariozechner/pi-coding-agent@latest \
  .
```

Run hub mode (default UI on `:7777`) from Docker Hub:

```bash
docker run --rm -p 7777:7777 moltenai/moltenhub-code:latest-codex
```

Run Pi variant from Docker Hub:

```bash
docker run --rm -p 7777:7777 moltenai/moltenhub-code:latest-pi
```

For persistent local runtime config, mount `./.moltenhub` to `/workspace/config`:

```bash
mkdir -p ./.moltenhub
docker run --rm -p 7777:7777 \
  -e HOME=/tmp \
  -v "$PWD/.moltenhub:/workspace/config" \
  moltenhub-code:latest-pi
```

If your host user is not uid/gid `1000`, add `--user "$(id -u):$(id -g)"` so onboarding can persist `config.json` to the bind mount.

## Test

```bash
go test ./...
```
