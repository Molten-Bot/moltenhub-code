# how-i-want-to-code

Minimal Go harness for repeatable Codex dispatch against one or more repositories in a shared workspace.

Also supports multiplexed execution across many task configs in parallel.
Also supports a persistent Hub listener mode that binds to MoltenHub and launches harness runs from websocket skill dispatches (with pull fallback).

## What It Does

For each run, the harness performs this flow:

1. Validate local tooling: `git`, `gh`, `codex`
2. Validate GitHub auth: `gh auth status`
3. Create isolated run workspace: `/dev/shm/temp/<guid>` (fallback `/tmp/temp/<guid>`)
4. Seed `AGENTS.md` into the run root from `./library/AGENTS.md`
5. Clone all configured repos + base branch before prompting Codex
6. Run Codex in the configured target subdirectory (single repo) or shared workspace (multi-repo, with `--skip-git-repo-check`), with prompt context that starts with `you are ./AGENTS.md` and points to the seeded file
7. For each repo with changes: commit, push to a `moltenhub-...` branch, and create a PR with a title that starts with `moltenhub-`
8. Wait for required PR checks (`gh pr checks --watch --required`) per changed repo
9. If required checks fail, re-run Codex with remediation context, push fixes, and re-check until green or retry limit is reached

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

This mode keeps one local process running, listens on hub OpenClaw websocket transport, and falls back to HTTP pull transport if websocket has an issue.
After successful auth, it saves `./.moltenhub/config.json` with `{baseUrl, token, sessionKey, timeoutMs}` and reuses that saved token on subsequent starts (so a fresh bind token is not required every run).

Hub mode now also starts a local monitor web UI by default at:

```bash
http://127.0.0.1:7777
```

Override or disable with:

```bash
./bin/harness hub --init templates/init.example.json --ui-listen :8088
./bin/harness hub --init templates/init.example.json --ui-listen \"\"
```

The UI shows inbound hub events, per-request status, and line-by-line subprocess output (including Codex stdout/stderr) for each running dispatch.

## Multiplex Run

Run multiple task configs concurrently:

```bash
./bin/harness multiplex --config ./tasks --parallel 4
```

You can provide `--config` multiple times. Each value may be:

- a single JSON file
- a directory (all `*.json` files under it, recursively)

Per-session logs are emitted to stderr with `session=<id>` prefixes, and a final per-session status summary is printed to stdout.

## Debug Logs

At runtime, the harness mirrors terminal output into a local `.log` directory in the current working directory.

- Aggregate stream: `.log/terminal.log`
- Task/request stream: `.log/<identifier parts>/terminal.log`

Identifier parts are split by `-` to make isolation fast:

- `request_id=local-1712345678-000001` -> `.log/local/1712345678/000001/terminal.log`
- `session=task-003` -> `.log/task/003/terminal.log`

## Hub Init Config (v1)

Required fields:

- none (for first-time bind, provide `bind_token` or `agent_token`)

Optional fields (with defaults):

- `version` (default: `v1`)
- `base_url` (default: `https://na.hub.molten.bot/v1`)
- `session_key` (default: `main`)
- `handle`
- `profile.display_name`
- `profile.emoji`
- `profile.bio`
- `profile.metadata` (merged into agent metadata patch)
- `skill.name` (default: `code_for_me`)
- `skill.dispatch_type` (default: `skill_request`)
- `skill.result_type` (default: `skill_result`)
- `dispatcher.max_parallel` (default: `2`)

## Hub Bootstrap Flow

`harness hub` uses a hard-coded startup flow:

1. Resolve an agent token:
   - load `./.moltenhub/config.json` token first (if present), else
   - verify `agent_token` (if present), else
   - attempt bind exchange against `/v1/agents/bind-tokens` and `/v1/agents/bind` using `bind_token`.
2. Sync profile:
   - one-time handle update via `/v1/agents/me/metadata` / `/v1/agents/me`
   - metadata patch only, with OpenAPI-compatible `metadata.skills` entries (`name` + `description`)
   - profile values are embedded in metadata (`display_name`, `emoji`, `bio`, `profile_markdown`)
3. Start OpenClaw websocket transport via `/v1/openclaw/messages/ws` (primary).
4. If websocket fails, temporarily fall back to OpenClaw pull transport via `/v1/openclaw/messages/pull` (`timeout_ms` long-poll), then retry websocket.
5. For each inbound message, parse run config JSON and execute a harness run in a worker goroutine.
6. Publish `skill_result` via `/v1/openclaw/messages/publish`.
7. When delivery leases are present (pull transport), Ack/Nack via `/v1/openclaw/messages/ack` and `/v1/openclaw/messages/nack`.

## Hub Skill Payload

Inbound dispatch must match the configured skill and include run config JSON. Supported payload locations include:

- top-level `config` or `input`
- `payload.config` or `payload.input`
- `data.config` or `data.input`

Run config is validated strictly against the harness run schema:

- Required: `prompt` and one of `repo`, `repo_url`, or `repos`
- Optional: `version`, `base_branch`, `target_subdir`, `commit_message`, `pr_title`, `pr_body`, `labels`, `reviewers`
- Additional/unknown fields are rejected

If a matching dispatch fails validation, the daemon responds with `status=error`, parse error details, and `result.required_schema` containing the exact payload schema contract.

## Config (v1)

Config supports JSON with `//` comments (JSONC-style) and reads the first JSON object in the file.

Required fields:

- `prompt`
- one of: `repo`, `repo_url`, or `repos` (`[]string`)

Optional fields (with defaults):

- `version` (default: `v1`)
- `repo` / `repo_url` (single-repo aliases; when combined with `repos`, they are included as the first repo)
- `repos` (`[]string`; if set, all repos are cloned before Codex runs)
- `base_branch` (default: `main`)
- `target_subdir` (default: `.` for repo root)
- `target_subdir` applies to the first repo in `repos` for multi-repo jobs
- `commit_message` (default: auto-generated from prompt)
- `pr_title` (default: auto-generated from prompt and prefixed with `moltenhub-`; custom values are also prefixed automatically)
- `pr_body` (default: auto-generated with prompt summary)
- `labels` (`[]string`)
- `reviewers` (`[]string`)

## Exit Codes

- `0`: success (PR created with required checks passing, or no changes)
- `2`: usage error
- `10`: config error
- `20`: preflight/tooling error
- `21`: auth error
- `30`: workspace error
- `40`: clone error
- `50`: codex execution error
- `60`: git workflow error
- `70`: PR/checks error

## Test

```bash
go test ./...
```
