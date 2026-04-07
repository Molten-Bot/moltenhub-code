# Docker Runtime Config Mount

`docker-compose.yml` mounts this directory to `/workspace/config` in the container.

Provide one of these files:

- `config.json` to run `harness run --config /workspace/config/config.json`
- `init.json` to run `harness hub --init /workspace/config/init.json` when `config.json` is absent

You can bootstrap from examples:

```bash
cp templates/run.example.json docker/config/config.json
cp templates/init.example.json docker/config/init.json
```
