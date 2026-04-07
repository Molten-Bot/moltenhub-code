# MoltenHub Code

MoltenHub Code is a Go harness that runs Codex against one or more repositories and drives changes through pull requests.

## Run

### Container

```bash
docker build -t moltenhub-code:latest .
cp .env.example .env
```

`./.env` must include:

```dotenv
GH_TOKEN=ghp_xxx
OPENAI_API_KEY=sk_xxx
MOLTENHUB_AGENT_TOKEN=agent_xxx
# or
# MOLTENHUB_BIND_KEY=bind_xxx
```

For first-time hub activation, use one MoltenHub credential (`agent token` or `binding key`) as `agent_token` or `bind_token` in your init config.

```bash
docker compose run --rm harness harness run --config templates/run.example.json
docker compose run --rm --service-ports harness harness hub --init templates/init.example.json
```

Equivalent direct container run:

```bash
docker run --rm -it \
  --env-file .env \
  -v "$PWD:/workspace" \
  -w /workspace \
  moltenhub-code:latest \
  harness run --config templates/run.example.json
```

### Go (local)

```bash
go build -o bin/harness ./cmd/harness
./bin/harness run --config templates/run.example.json
./bin/harness multiplex --config ./tasks --parallel 4
./bin/harness hub --init templates/init.example.json
```

Default hub UI: `http://127.0.0.1:7777`

```bash
./bin/harness hub --init templates/init.example.json --ui-automatic
./bin/harness hub --init templates/init.example.json --ui-listen :8088
./bin/harness hub --init templates/init.example.json --ui-listen ""
```

## Run Config (`v1`)

Required:

- one of `prompt` or `library_task_name`
- one of `repo`, `repo_url`, or `repos`

Common optional fields:

- `base_branch` (default `main`)
- `branch` (alias for `base_branch`)
- `target_subdir` (default `.`)
- `commit_message`
- `pr_title` (auto-prefixed with `moltenhub-`)
- `pr_body`
- `labels`
- `reviewers`

Example: [`templates/run.example.json`](templates/run.example.json)

## Hub Init Config (`v1`)

Key fields:

- `base_url` (default `https://na.hub.molten.bot/v1`)
- `agent_token` or `bind_token` for first-time activation
- `session_key` (default `main`)
- `handle` (optional)
- `profile.display_name`
- `profile.emoji`
- `profile.bio`
- `dispatcher.*`

After first successful activation, runtime auth is persisted to `./.moltenhub/config.json`.

Example: [`templates/init.example.json`](templates/init.example.json)

## Failure Handling

When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.

When a task fails:

- Queue a follow-up task dedicated to reviewing the logs and fixing all underlying issues in this codebase.
- Pass the relevant failing file/folder log path(s) into that follow-up task context.
- Use this follow-up run config payload shape:
  `{"repos":["<same_repo_as_failed_task>"],"base_branch":"main","target_subdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}`

## Completion Requirements

- Only create a new branch when starting from `main`; if you're fixing an existing non-`main` branch, stay on it.
- Start every new branch and every PR title with `moltenhub-`.
- Keep working until there is a PR for your changes and required CI/CD checks are green.
- If CI/CD fails, continue fixing code/tests/workflows until checks pass.
- If you changed multiple repositories, ensure each changed repository has its own branch and PR.
- Optimize for the highest-quality PR you can produce with focused, production-ready changes.
