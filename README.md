# how-i-want-to-code

Minimal Go harness for repeatable Codex dispatch against a single directory in a target repository.

Also supports multiplexed execution across many task configs in parallel.
Also supports a persistent Hub listener mode that binds to MoltenHub and launches harness runs from websocket skill dispatches.

## What It Does

For each run, the harness performs this flow:

1. Validate local tooling: `git`, `gh`, `codex`
2. Validate GitHub auth: `gh auth status`
3. Create isolated run workspace: `/dev/shm/<guid>` (fallback `/tmp/<guid>`)
4. Clone target repo + base branch
5. Run Codex in exactly one configured subdirectory
6. If changes exist: commit, push, and create PR (always the final step)

If Codex fails, no PR is created and the workspace path is printed for inspection.

## Requirements

- Go 1.24+
- `git` on `PATH`
- `gh` on `PATH`, already authenticated (`gh auth login`)
- `codex` on `PATH`

## Build

```bash
go build -o bin/harness ./cmd/harness
```

## Run

Use the template at [`templates/run.example.json`](templates/run.example.json):

```bash
./bin/harness run --config templates/run.example.json
```

## Hub Run

Use the template at [`templates/init.example.json`](templates/init.example.json):

```bash
./bin/harness hub --init templates/init.example.json
```

This mode keeps one local process running, listens on hub websocket events, and spins worker harness sessions as matching skill requests arrive.

## Multiplex Run

Run multiple task configs concurrently:

```bash
./bin/harness multiplex --config ./tasks --parallel 4
```

You can provide `--config` multiple times. Each value may be:

- a single JSON file
- a directory (all `*.json` files under it, recursively)

Per-session logs are emitted to stderr with `session=<id>` prefixes, and a final per-session status summary is printed to stdout.

## Hub Init Config (v1)

Required fields:

- `bind_token` or `agent_token`

Optional fields (with defaults):

- `version` (default: `v1`)
- `base_url` (default: `https://na.hub.molten.bot/v1`)
- `session_key` (default: `main`)
- `handle`
- `profile.display_name`
- `profile.emoji`
- `profile.bio`
- `profile.metadata` (merged into agent metadata patch)
- `skill.name` (default: `codex_harness_run`)
- `skill.dispatch_type` (default: `skill_request`)
- `skill.result_type` (default: `skill_result`)
- `dispatcher.max_parallel` (default: `2`)

## Hub Bootstrap Flow

`harness hub` uses a hard-coded startup flow:

1. Resolve an agent token:
   - verify `agent_token` (if present), else
   - attempt bind exchange against `/v1/agents/bind-tokens` and `/v1/agents/bind` using `bind_token`.
2. Sync profile:
   - handle finalize/update attempts
   - profile patch (`display_name`, `emoji`, `bio`)
   - metadata patch (includes runtime + skill metadata)
3. Register runtime at `/v1/openclaw/messages/register-plugin` (non-fatal warning on failure).
4. Connect websocket at `/v1/openclaw/messages/ws`.
5. For each matching skill dispatch, parse run config JSON and execute a harness run in a worker goroutine.
6. Send `skill_result` back over websocket (HTTP publish fallback if websocket send fails).

## Hub Skill Payload

Inbound dispatch must match the configured skill and include run config JSON. Supported payload locations include:

- top-level `config` or `input`
- `payload.config` or `payload.input`
- `data.config` or `data.input`
- `config_path` (path to a run config file)

Run config schema is the same as standard harness `run` config (`repo`/`repo_url`, `prompt`, and optional branch/subdir/PR metadata).

## Config (v1)

Config supports JSON with `//` comments (JSONC-style) and reads the first JSON object in the file.

Required fields:

- `prompt`
- `repo` or `repo_url`

Optional fields (with defaults):

- `version` (default: `v1`)
- `base_branch` (default: `main`)
- `target_subdir` (default: `.` for repo root)
- `commit_message` (default: auto-generated from prompt)
- `pr_title` (default: auto-generated from prompt)
- `pr_body` (default: auto-generated with prompt summary)
- `labels` (`[]string`)
- `reviewers` (`[]string`)

## Exit Codes

- `0`: success (PR created or no changes)
- `2`: usage error
- `10`: config error
- `20`: preflight/tooling error
- `21`: auth error
- `30`: workspace error
- `40`: clone error
- `50`: codex execution error
- `60`: git workflow error
- `70`: PR creation error

## Test

```bash
go test ./...
```
