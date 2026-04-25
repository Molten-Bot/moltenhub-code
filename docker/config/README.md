# Docker Runtime Config Mount

`docker-compose.yml` mounts `./.moltenhub` to `/workspace/config` in the container.

This directory remains available if you prefer a manual bind mount (for example with `docker run`).

`GITHUB_TOKEN` also works as direct bootstrap input. When container starts, entrypoint copies `GITHUB_TOKEN` to `GH_TOKEN`, runs `gh` auth setup, and hub onboarding skips GitHub token prompt if token already exists.

Provide one of these files:

- `config.json` to run `harness run --config /workspace/config/config.json` when it contains task-run fields
- `config.json` to run `harness hub --config /workspace/config/config.json` when it contains hub runtime fields
- `init.json` to run `harness hub --init /workspace/config/init.json` when `config.json` is absent
- if both files are absent and `MOLTEN_HUB_TOKEN` is set, `with-config` auto-generates a temporary init config and starts hub mode
- if both files are absent and `MOLTEN_HUB_TOKEN` is unset, `with-config` starts `harness hub` onboarding mode with defaults (no init required)

When running hub mode, `init.json` may also include runtime secrets:

- `github_token` for GitHub auth bootstrap
- `openai_api_key` for Codex CLI login when using the Codex harness
- `augment_session_auth` for Auggie CLI auth when using the Auggie harness; set it to the full session JSON from `auggie token print`

After first successful onboarding, persisted `config.json` may also contain `github_token`. Future boots load that automatically if `GITHUB_TOKEN`/`GH_TOKEN` are unset.

After the first successful onboarding/hub auth, runtime fields are persisted into `config.json` so later boots can use `config.json` directly.

You can bootstrap from examples:

```bash
mkdir -p .moltenhub
cp run.example.json .moltenhub/config.json
# Optional bootstrap if you want to pre-seed hub credentials:
cp init.example.json .moltenhub/init.json
```

For manual `docker run` with a bind mount, if your host user is not uid/gid `1000`, add `--user "$(id -u):$(id -g)"` to avoid config write-permission failures.

Example with persisted config mount and direct env bootstrap:

```bash
docker run --rm -p 7777:7777 \
  -e GITHUB_TOKEN=ghp_your_token \
  -e MOLTEN_HUB_TOKEN=hub_your_agent_token \
  -e MOLTEN_HUB_REGION=na \
  -v "$PWD/.moltenhub:/workspace/config" \
  moltenhub-code:latest
```

`MOLTEN_HUB_TOKEN` only needs to be present for first bootstrap when no `config.json` or `init.json` exists yet. Optional extras:
- `MOLTEN_HUB_REGION=na|eu` selects hosted hub region
- `MOLTEN_HUB_URL=https://na.hub.molten.bot/v1` sets explicit hosted hub API URL
- `MOLTEN_HUB_SESSION_KEY` customizes generated init config session key
