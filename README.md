# MoltenHub Code

Run AI coding agents across GitHub repositories, publish their changes, and watch pull request checks.

For product details, see [molten.bot/code](https://molten.bot/code).

## Quick Start

### Docker

```bash
mkdir -p ./.moltenhub
docker run --rm -p 7777:7777 \
  -e GITHUB_TOKEN \
  -v "$PWD/.moltenhub:/workspace/config" \
  moltenai/moltenhub-code:latest
```

The container starts the hub UI on port `7777`. It persists onboarding, runtime config, and CLI auth home data in `/workspace/config`, so mount that path for repeat runs.

The runtime image includes:

- Go `1.26.1`
- Python 3, `pip`, `virtualenv`, and the latest OpenAI Python SDK (`openai`)
- `git`, `gh`, `jq`, `openssh-client`, `rg`, `file`
- `@openai/codex`, `@anthropic-ai/claude-code`, `@augmentcode/auggie`, `@mariozechner/pi-coding-agent`, and `@playwright/test`
- Playwright Chromium browser binaries and system dependencies for screenshots and UI checks

### Local Build

```bash
go build -o bin/harness ./cmd/harness
./bin/harness hub
```

Local `harness hub` listens on `127.0.0.1:7777` by default.

## CLI

```bash
harness run --config run.example.json
harness multiplex --config ./tasks --parallel 2
harness hub --config ./.moltenhub/config.json
harness hub --init ./.moltenhub/init.json
```

`harness run` executes one task. `harness multiplex` runs multiple task configs. `harness hub` starts the local UI and optional remote Hub transport.

## Run Config

Run configs are JSON or JSONC. Minimal example:

```json
{
  "repo": "git@github.com:owner/repo.git",
  "targetSubdir": ".",
  "agentHarness": "codex",
  "prompt": "Update README setup instructions."
}
```

Important fields:

- `repo`, `repoUrl`, or `repos`: target repository or repositories.
- `baseBranch`: optional branch to clone. Omit it to use the repository default branch; `branch` is accepted as an alias.
- `targetSubdir`: working directory for a single-repo task. Defaults to `.`.
- `prompt`: task sent to the selected agent.
- `agentHarness`: `codex`, `claude`, `auggie`, or `pi`. Defaults to `codex` or `HARNESS_AGENT_HARNESS`.
- `agentCommand`: optional command override, also available as `HARNESS_AGENT_COMMAND`.
- `responseMode`: agent prose mode. Defaults to `caveman-full`; use `off` for normal prose.
- `images`: optional base64 prompt images. Supported by `codex` and `pi`.
- `review`: optional pull request review context for single-repo review tasks.

See [run.example.json](run.example.json) for a commented template.

## Runtime Behavior

Each task run:

1. Checks required tools and selected agent CLI.
2. Verifies GitHub auth with `gh auth status`.
3. Creates an isolated workspace under `/workspace`.
4. Clones each repo at `baseBranch`, or the repository default branch when no branch is specified. Missing `main` on an empty repo is bootstrapped with an empty commit.
5. Creates a `moltenhub-...` work branch when starting from the repository default branch; otherwise reuses the requested branch.
6. Probes publish access before agent execution. Public GitHub repos can fall back to a fork when direct write access is denied.
7. Runs the selected agent in `targetSubdir` for one repo, or workspace root for multi-repo runs.
8. Commits changed repos, pushes branches, opens or reuses PRs, and watches required checks.
9. If checks fail, runs up to three focused remediation attempts and pushes follow-up commits.

If no repository changes remain after the agent runs, the task exits successfully with `status=no_changes`.

## Hub Configuration

The Docker entrypoint looks for config in this order:

1. `/workspace/config/config.json`
2. `/workspace/config/init.json`
3. `MOLTEN_HUB_TOKEN` bootstrap
4. Local onboarding UI

Useful environment variables:

- `GITHUB_TOKEN` or `GH_TOKEN`: GitHub auth for clone, push, PR, and checks.
- `MOLTEN_HUB_TOKEN`: remote Hub agent token.
- `MOLTEN_HUB_REGION`: `na` or `eu`; defaults to `na`.
- `MOLTEN_HUB_URL`: explicit Hub API URL, either `https://na.hub.molten.bot/v1` or `https://eu.hub.molten.bot/v1`.
- `MOLTEN_HUB_SESSION_KEY`: generated init config session key. Defaults to `main`.
- `OPENAI_API_KEY`, `AUGMENT_SESSION_AUTH`, `PI_PROVIDER_AUTH`, `PI_AUTH_JSON`: optional agent auth values loaded by the entrypoint or persisted config.

Hub OpenAPI:

- Live: [`https://na.hub.molten.bot/openapi.yaml`](https://na.hub.molten.bot/openapi.yaml)
- Offline snapshot: [na.hub.molten.bot.openapi.yaml](na.hub.molten.bot.openapi.yaml)

## Response Modes

Supported `responseMode` values:

- `default`
- `off`
- `caveman-lite`
- `caveman-full`
- `caveman-ultra`
- `caveman-wenyan-lite`
- `caveman-wenyan-full`
- `caveman-wenyan-ultra`

Omitted or `default` maps to `caveman-full`. The mode is applied by prepending the bundled [Caveman skill](skills/caveman/SKILL.md) to the agent prompt.

## Development

```bash
go test ./...
```

There is no separate dependency install step for the Go module today; dependencies come from `go.mod`.
