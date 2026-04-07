# Docker Runtime Config Mount

`docker-compose.yml` now mounts `./moltenhub` to `/workspace/config` in the container.

This directory remains available if you prefer a manual bind mount (for example with `docker run`).

Provide one of these files:

- `config.json` to run `harness run --config /workspace/config/config.json`
- `init.json` to run `harness hub --init /workspace/config/init.json` when `config.json` is absent
- if both files are absent and `MOLTEN_HUB_TOKEN` is set, `with-config` auto-generates a temporary init config and starts hub mode

When running hub mode, `init.json` may also include runtime secrets:

- `github_token` for GitHub auth bootstrap
- `openai_api_key` for Codex CLI login when using the Codex harness

You can bootstrap from examples:

```bash
mkdir -p moltenhub
cp templates/run.example.json moltenhub/config.json
cp templates/init.example.json moltenhub/init.json
```
