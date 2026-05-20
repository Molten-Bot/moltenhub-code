# MoltenHub Code

The highest velocity way to make code changes. Run AI coding agents against GitHub repositories, prompt to PR. Product details live at
[molten.bot/code](https://molten.bot/code).

## Quick Start

### Docker

```bash
docker run -p 7777:7777 moltenai/moltenhub-code
```

### Local Build

Requires Go `1.26.2` or newer plus `git`, `gh`, and the selected agent CLI.

```bash
go build -o bin/harness ./cmd/harness
./bin/harness hub
```

Local `harness hub` listens on `127.0.0.1:7777` by default.

### Go Module

MoltenHub Code is distributed as a Go module from this Git repository. Use a
Git tag such as `v1.0.0` for stable installs.

```bash
go get github.com/Molten-Bot/moltenhub-code@v1.0.0
go install github.com/Molten-Bot/moltenhub-code/cmd/harness@v1.0.0
```

## Bundled Tools

The Docker image includes `git-changes-by-day`, a Go CLI for exporting git
history to CSV. Agents can use it when a task needs per-commit change data.

```bash
git-changes-by-day -repo /path/to/repo -text-out /tmp/commit-text.csv
```

The CSV includes UTC datetime/date columns, commit metadata, changed file
counts, and line change counts.

## Environment Variables

Useful environment variables:

- `GITHUB_TOKEN` or `GH_TOKEN`: GitHub auth for clone, push, PRs, and checks.
- `MOLTEN_HUB_TOKEN`: remote Hub agent token.
- `MOLTEN_HUB_REGION`: `na` or `eu`; defaults to `na`.
- `MOLTEN_HUB_URL`: explicit hosted Hub API URL,
  `https://na.hub.molten.bot/v1` or `https://eu.hub.molten.bot/v1`.
- `MOLTEN_HUB_SESSION_KEY`: runtime config session key; defaults to `main`.
- `HARNESS_AGENT_HARNESS`: default agent harness.
- `HARNESS_AGENT_COMMAND`: default agent executable.
- `OPENAI_API_KEY`: Codex login bootstrap.
- `AUGMENT_SESSION_AUTH`: Auggie session JSON from `auggie token print`.
- `PI_PROVIDER_AUTH` or `PI_AUTH_JSON`: Pi provider auth bootstrap.

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

Omitted or `default` maps to `caveman-full`. The harness prepends the bundled
[Caveman skill](skills/caveman/SKILL.md) to the agent prompt unless
`responseMode` is `off`.

## Development

```bash
go test ./...
```
