# MoltenHub Code

Turn a prompt into review-ready pull requests.

MoltenHub Code is a small Go harness that runs an agent CLI (Codex, Claude, or Auggie) against one or more repositories, opens PRs, and waits for required checks.
It supports single runs, parallel local runs, and a persistent MoltenHub listener with a local monitoring UI.

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

Optional `.env` flow:

```bash
cp .env.example .env
```

`./.env`:

```dotenv
GITHUB_TOKEN=ghp_xxx
OPENAI_API_KEY=sk_xxx
MOLTEN_HUB_TOKEN=agent_or_bind_token
```

`init.json` flow (no `.env` required for hub mode):

- Add `github_token` to satisfy `gh` auth setup
- Add `openai_api_key` when using the Codex harness

Run with Docker Compose (`docker-compose.yml`):

```bash
mkdir -p moltenhub
cp templates/run.example.json moltenhub/config.json
docker compose up
```

`docker compose` uses a persistent bind mount at `./moltenhub -> /workspace/config` and starts `with-config`, which auto-selects:

```bash
# run mode when config exists
/workspace/config/config.json

# hub mode when run config is absent and init exists
/workspace/config/init.json

# hub mode from env when both config files are absent
MOLTEN_HUB_TOKEN (+ optional MOLTEN_HUB_URL, MOLTEN_HUB_SESSION_KEY)
```

Hub mode example:

```bash
rm -f moltenhub/config.json
cp templates/init.example.json moltenhub/init.json
docker compose up
```

For `init.json`-only startup, ensure the container user can read the file:

```bash
chmod 644 moltenhub/init.json
```

GitHub Actions publish flow:

- `deploy-vnext` runs automatically on pushes to `main` (including PR merges) and publishes:
  - `moltenai/moltenhub-code:vnext`, `moltenai/moltenhub-code:<yyyy.mm.dd.run_number>` (default Codex image)
  - `moltenai/moltenhub-code:vnext-codex|claude|auggie`
  - `moltenai/moltenhub-code:<yyyy.mm.dd.run_number>-codex|claude|auggie`
- `deploy-prod` is manual-only (`workflow_dispatch`) and promotes:
  - selected source tag (default `vnext`) to `moltenai/moltenhub-code:latest`
  - selected source tag variants (`-codex`, `-claude`, `-auggie`) to `latest-codex`, `latest-claude`, `latest-auggie`
- required repository secret: `DOCKERHUB_TOKEN`

Equivalent direct `docker run`:

```bash
docker run --rm -it \
  -e GITHUB_TOKEN=ghp_xxx \
  -e OPENAI_API_KEY=sk_xxx \
  -v "$PWD/moltenhub:/workspace/config" \
  -w /workspace \
  moltenhub-code:latest \
  with-config
```

Container startup pre-registers auth before any agent stage:

- maps `GITHUB_TOKEN` to `GH_TOKEN` for `gh` commands
- if `GITHUB_TOKEN`/`GH_TOKEN` are not set, it reads `github_token` from init JSON and exports it
- runs `gh auth status` and `gh auth setup-git`
- configures GitHub URL rewrites so `git@github.com:*` and `ssh://git@github.com/*` can use PAT-backed HTTPS
- for Codex, it reads `openai_api_key` from init JSON when `OPENAI_API_KEY` is unset and performs `codex login --with-api-key`
- when Codex auth is still missing, the UI now shows an authorization pre-screen:
  - startup checks `codex login status` from an empty temp working directory
  - `Start Device Auth` launches `codex login --device-auth` and surfaces URL/code in the pre-screen
  - `Done, Check Again` re-checks readiness before allowing Studio submissions
- if remote Hub auth fails (`401`) and the UI is enabled, harness now remains in local-only mode so you can still complete Codex device auth and run local Studio tasks

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

On startup, hub mode emits a boot diagnosis checklist for:

- `git` CLI availability
- `gh` CLI availability
- selected agent CLI availability (`codex`, `claude`, or `auggie`)
- `gh auth` readiness
- Hub endpoint health at `<base_url host>/ping` (must return `2xx` before connecting)
- a Molten Hub connection recommendation (`https://molten.bot/hub`) when the runtime is not connected yet

If the ping check fails, hub mode exits early instead of entering transport retry loops.

## UI

Hub mode starts a local monitor UI by default at `http://127.0.0.1:7777`.

The Studio panel defaults to a schema builder that stores requested repositories in browser local storage and reuses them as a repo picker. In Builder mode, you can paste clipboard PNG screenshots into the prompt field and they will be attached to the initial run. Raw JSON mode remains available for advanced or multi-repo payloads. The UI also includes a browser-local `Hide` toggle so you can collapse that section without restarting the harness.

The Tasks panel shows live task cards sorted by activity, with inline output previews and a full-screen view for deeper inspection.

Automatic mode is available as a runtime flag and hides the browser Studio form entirely:

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

Example: [`templates/run.example.json`](templates/run.example.json)

Library-backed runs can also use:

```json
{
  "repo": "git@github.com:acme/target-repo.git",
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

After first successful activation, runtime auth is persisted to `./.moltenhub/config.json`, so `bind_token` and `handle` are not required in `init.json` for subsequent runs.
Runtime config keys `sessionKey` and `timeoutMs` are optional; missing values default to `main` and `20000`.

Local-only behavior:

- If `bind_token`/`agent_token` are missing and no persisted runtime token exists, `harness hub` now starts in local-only mode instead of exiting with auth error.
- In local-only mode, the monitor UI and `/api/local-prompt` remain available for local runs, and remote Hub transport is skipped.
- If local-only mode is used with `--ui-listen ""`, startup exits with an auth/config error because there is no remote or local submission channel.

Runtime-owned fields:

- skill contract is fixed to `code_for_me` / `skill_request` / `skill_result`
- profile visibility metadata is managed by runtime and forced public

Example: [`templates/init.example.json`](templates/init.example.json)

## Test

```bash
go test ./...
```
