# MoltenHub Code

MoltenHub Code is a Go harness that turns agent prompts into repository changes across one or more GitHub repositories. It runs Codex, Claude, or Auggie inside isolated workspaces, stages branch and pull request creation, and waits for required CI checks. It supports direct local runs, parallel multiplexed runs, and a persistent MoltenHub listener with a local monitoring UI for submitted tasks. When tasks fail, it returns explicit failure details and can queue focused follow-up remediation runs against the same repository.

## What It Does

For each run:

1. Verifies required tools (`git`, `gh`, and the selected agent CLI) plus GitHub auth.
2. Creates an isolated workspace (`/dev/shm/temp/<guid>`, fallback `/tmp/temp/<guid>`).
3. Seeds `AGENTS.md` from [`library/AGENTS.md`](library/AGENTS.md).
4. Clones configured repos and checks out `baseBranch`.
5. Runs the selected agent harness in `targetSubdir` (or workspace root for multi-repo runs).
6. For changed repos:
   - If `baseBranch` is `main`, creates a `moltenhub-*` branch.
   - Otherwise reuses the existing non-`main` branch.
7. Creates or reuses PRs with `moltenhub-*` titles.
8. Watches required CI checks and performs remediation retries when checks fail.

If a task fails, no PR is created for that run; the runtime returns an explicit failure payload and queues a follow-up remediation task that targets the same repository.

## Run

### Go
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

Optional explicit startup:

```bash
./bin/harness hub --init ./init.example.json
./bin/harness hub --config ./.moltenhub/config.json
```

### Container

Build a container image:

```bash
docker build -t moltenhub-code:latest .
```

Build for a specific harness:

```bash
docker build \
  --build-arg AGENT_HARNESS=claude \
  --build-arg AGENT_NPM_PACKAGE=@anthropic-ai/claude-code@latest \
  -t moltenhub-code:claude .
```

Supported harness values:

- `codex` (`@openai/codex@latest`)
- `claude` (`@anthropic-ai/claude-code@latest`)
- `auggie` (`@augmentcode/auggie@latest`)

Pass secrets at container runtime, not at build time. `.env` is excluded from Docker build context via `.dockerignore` so tokens are never copied into image layers.

Optional `.env` flow (`./.env`):

```dotenv
GITHUB_TOKEN=ghp_xxx
OPENAI_API_KEY=sk_xxx
MOLTEN_HUB_TOKEN=agent_or_bind_token
```

Optional `init.json` bootstrap flow (no `.env` required for hub mode):

- Add `github_token` to satisfy `gh` auth setup
- Add `openai_api_key` when using the Codex harness
- Add `augment_session_auth` when using the Auggie harness; use the full session JSON from `auggie token print`

Run with Docker Compose (`docker-compose.yml`):

```bash
mkdir -p .moltenhub

HOST_UID="$(id -u)" HOST_GID="$(id -g)" docker compose up
```

`docker compose` uses a persistent bind mount at `./.moltenhub -> /workspace/config` and starts:

```bash
harness hub --ui-listen :7777
```

To avoid passing IDs on every run, create a local `.env` once:

```dotenv
HOST_UID=<output of id -u>
HOST_GID=<output of id -g>
```

With this layout:

```bash
/workspace/config/config.json -> ./.moltenhub/config.json
```

After the first successful onboarding/auth flow, the runtime persists a hub-bootable `config.json` so later boots can start directly from persisted config.

GitHub Actions publish flow:

- `deploy-vnext` runs automatically on pushes to `main` (including PR merges) and publishes:
  - `moltenai/moltenhub-code:vnext`, `moltenai/moltenhub-code:<yyyy.mm.dd.run_number>` (default Codex image)
  - `moltenai/moltenhub-code:vnext-codex|claude|auggie`
  - `moltenai/moltenhub-code:<yyyy.mm.dd.run_number>-codex|claude|auggie`
- `deploy-prod` is manual-only (`workflow_dispatch`) and promotes:
  - selected source tag family through one concurrent release matrix for `codex`, `claude`, and `auggie`
  - `vnext` promotes to `latest-codex` and keeps `latest` as a Codex alias
  - `vnext-claude`, `vnext-auggie` promote to `latest-claude`, `latest-auggie`
- required repository secret: `DOCKERHUB_TOKEN`

Simplest container startup (no mounted config required):

```bash
docker run --rm \
  -p 7777:7777 \
  moltenai/moltenhub-code:latest-codex
```

Notes:

- Put Docker runtime flags (like `-p`) before the image name.
- If `/workspace/config` is not mounted, startup creates runtime config state in the container volume and enters onboarding mode.
- Container hub mode now binds UI to `:7777` by default.

Equivalent direct `docker run` with explicit env and persistent config mount:

```bash
docker run --rm -it \
  -u "$(id -u):$(id -g)" \
  -e HOME=/tmp \
  -e HARNESS_RUNTIME_CONFIG_PATH=/workspace/config/config.json \
  -e GITHUB_TOKEN=ghp_xxx \
  -e OPENAI_API_KEY=sk_xxx \
  -p 3300:7777 \
  -v "$PWD/.moltenhub:/workspace/config" \
  -w /workspace \
  moltenhub-code:latest \
  harness hub --ui-listen :7777
```

Container startup pre-registers auth before any agent stage:

- maps `GITHUB_TOKEN` to `GH_TOKEN` for `gh` commands
- if `GITHUB_TOKEN`/`GH_TOKEN` are not set, it reads `github_token` from runtime/init JSON and exports it
- runs `gh auth status` and `gh auth setup-git`
- configures GitHub URL rewrites so `git@github.com:*` and `ssh://git@github.com/*` can use PAT-backed HTTPS
- for Codex, it reads `openai_api_key` from runtime/init JSON when `OPENAI_API_KEY` is unset and performs `codex login --with-api-key`
- for Auggie, it reads `augment_session_auth` from runtime/init JSON when `AUGMENT_SESSION_AUTH` is unset and exports it for non-interactive CLI runs
- for all harnesses (`codex`, `claude`, `auggie`), startup now blocks Studio task submission until GitHub auth is configured
- when Auggie auth is missing, the UI now shows a `Setup Auggie` screen:
  - copy `auggie token print`, run it locally, and paste the returned JSON
  - the payload is schema-validated and persisted to runtime `config.json` as `augment_session_auth`
- when Codex auth is still missing, the UI now shows an authorization pre-screen:
  - startup checks `codex login status` from an empty temp working directory
  - it automatically launches `codex login --device-auth` and surfaces URL/code in the pre-screen
  - `Done` re-checks readiness before allowing Studio submissions
- for Claude, the UI now blocks Studio submissions until Claude auth is ready and points users to the browser-login flow described in [doc/CLAUDE.md](doc/CLAUDE.md)
- if remote Hub auth fails (`401`) and the UI is enabled, harness now remains in local-only mode so you can still complete Codex device auth and run local Studio tasks

On startup, hub mode emits a boot diagnosis checklist for:

- `git` CLI availability
- `gh` CLI availability
- selected agent CLI availability (`codex`, `claude`, or `auggie`)
- `gh auth` readiness
- Hub endpoint health at `<base_url host>/ping` (must return `2xx` before connecting remote transport)
- a Molten Hub connection recommendation (`https://molten.bot/hub`) when the runtime is not connected yet

If the ping check fails and the UI is enabled, hub mode now stays up in local-only mode so Studio and local submissions remain available.
If the ping check fails and `--ui-listen ""` is set, startup behavior is now non-fatal:
- with Hub credentials configured, startup continues and still attempts remote transport
- without Hub credentials, startup exits successfully after logging a headless no-op local-only status

## UI

Hub mode starts a local monitor UI by default at `http://127.0.0.1:7777`.

The Studio panel defaults to a schema builder that stores requested repositories in browser local storage and reuses them as a repo picker. When saved repos exist, the picker preselects the most recently used entry; otherwise it defaults to `git@github.com:Molten-Bot/moltenhub-code.git` so contributors can open PRs against this app without additional setup. In Builder mode, you can paste clipboard PNG screenshots into the prompt field and they will be attached to the initial run. Raw JSON mode remains available for advanced or multi-repo payloads. The UI also includes a browser-local `Hide` toggle so you can collapse that section without restarting the harness.

The Tasks panel shows live task cards sorted by activity, with inline output previews and a full-screen view for deeper inspection.

Automatic mode is available as a runtime flag and hides the browser Studio form entirely:

```bash
./bin/harness hub --ui-automatic
```

Override or disable:

```bash
./bin/harness hub --ui-listen :8088
./bin/harness hub --ui-listen ""
```

## Run Config (`v1`)

Required:

- one of `prompt` or `libraryTaskName`
- one of `repo`, `repoUrl`, or `repos`

Common optional fields:

- `baseBranch` (default `main`)
- `branch` (alias for `baseBranch`, mainly for library-backed skill calls)
- `targetSubdir` (default `.`)
- `agentHarness` (optional: `codex`, `claude`, `auggie`; defaults to `codex` or `HARNESS_AGENT_HARNESS`)
- `agentCommand` (optional CLI executable override)
- `commitMessage`
- `prTitle` (auto-prefixed with `moltenhub-`)
- `prBody`
- `labels`
- `githubHandle` (single GitHub reviewer alias; mapped to PR reviewer)
- `reviewers`

Run payloads and library task JSON definitions use camelCase keys only. Legacy snake_case aliases are rejected.

Example: [`run.example.json`](run.example.json)

Library-backed runs can also use:

```json
{
  "repo": "git@github.com:Molten-Bot/moltenhub-code.git",
  "branch": "main",
  "libraryTaskName": "unit-test-coverage"
}
```

## Hub Init Config (`v1`)

Key fields:

- `base_url` (default `https://na.hub.molten.bot/v1`)
- `bind_token` or `agent_token` for first-time activation only
- `session_key` (default `main`)
- `handle` (optional)
- `profile.display_name`
- `profile.emoji`
- `profile.bio`
- `agent_harness` (optional: `codex`, `claude`, `auggie`; defaults to `codex` or `HARNESS_AGENT_HARNESS`)
- `agent_command` (optional CLI executable override)
- `dispatcher.*` (adaptive worker parallelism)
- `github_token` (optional GitHub PAT for container bootstrap)
- `openai_api_key` (optional Codex API key for container bootstrap)
- `augment_session_auth` (optional Auggie session JSON for container bootstrap)

After first successful activation, runtime auth is persisted to a sibling `config.json` next to the init file used for startup. With the default repo-root layout, that remains `./.moltenhub/config.json`. The runtime also reads the legacy `config/config.json` path next to that file for compatibility with existing mounts.

The live Hub OpenAPI spec is published at `https://na.hub.molten.bot/openapi.yaml`.
A focused local runtime snapshot is checked in at [`na.hub.molten.bot.openapi.yaml`](na.hub.molten.bot.openapi.yaml) for offline review of the routes this harness depends on.
Runtime config keys `sessionKey` and `timeoutMs` are optional; missing values default to `main` and `20000`.

Local-only behavior:

- If `harness hub` is started without `--init`/`--config` and no persisted runtime config exists, startup now uses defaults and enters onboarding/local mode (no `init.json` required).
- If `bind_token`/`agent_token` are missing and no persisted runtime token exists, `harness hub` now starts in local-only mode instead of exiting with auth error.
- If Hub ping precheck fails but the UI is enabled, `harness hub` stays available in local-only mode and skips remote transport startup.
- If Hub ping precheck fails with `--ui-listen ""` and Hub credentials are configured, `harness hub` continues remote startup (the ping probe can be transient).
- If Hub ping precheck fails with `--ui-listen ""` and Hub credentials are not configured, startup exits successfully with a headless no-op local-only status.
- In local-only mode, the monitor UI and `/api/local-prompt` remain available for local runs, and remote Hub transport is skipped.
- Library mode in the monitor UI loads task definitions from `/api/library` and submits selected catalog tasks via `/api/library/run`.

Runtime-owned fields:

- skill contract is fixed to `code_for_me` / `skill_request` / `skill_result`
- profile visibility metadata is managed by runtime and forced public

Example: [`init.example.json`](init.example.json)

## Test

```bash
go test ./...
```
