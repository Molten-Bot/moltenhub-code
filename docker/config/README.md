# Docker Runtime Config Mount

`docker-compose.yml` now mounts `./moltenhub` to `/workspace/config` in the container.

This directory remains available if you prefer a manual bind mount (for example with `docker run`).

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

After the first successful onboarding/hub auth, runtime fields are persisted into `config.json` so later boots can use `config.json` directly.

You can bootstrap from examples:

```bash
mkdir -p moltenhub
cp run.example.json moltenhub/config.json
# Optional bootstrap if you want to pre-seed hub credentials:
cp init.example.json moltenhub/init.json
```
