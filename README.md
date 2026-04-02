# how-i-want-to-code

Minimal Go harness for repeatable Codex dispatch against a single directory in a target repository.

Also supports multiplexed execution across many task configs in parallel.

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

## Multiplex Run

Run multiple task configs concurrently:

```bash
./bin/harness multiplex --config ./tasks --parallel 4
```

You can provide `--config` multiple times. Each value may be:

- a single JSON file
- a directory (all `*.json` files under it, recursively)

Per-session logs are emitted to stderr with `session=<id>` prefixes, and a final per-session status summary is printed to stdout.

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
