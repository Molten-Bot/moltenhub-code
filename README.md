# Molten Hub Code

The highest velocity way to make code changes. Run AI coding agents against GitHub repositories, prompt to PR. Product details live at
[molten.bot/code](https://molten.bot/code).

## Quick Start

### Docker

```bash
docker run -p 7777:7777 moltenai/moltenhub-code
```

### Docker Compose with Prompt Dictation

```bash
cp .env.example .env
docker compose up
```

For a local image build instead of the published Docker Hub image:

```bash
docker compose -f docker-compose.yml -f docker-compose.local.yml up --build
```

The Compose stack runs `moltenhub-code` with a `linuxserver/faster-whisper`
sidecar. The web UI probes `faster-whisper:10300`; when it is reachable, the
Prompt Studio shows a microphone button that appends dictated text to the
prompt field. The Compose sidecar disables Docker log capture because
`wyoming-faster-whisper` logs transcript text at INFO level.
The bundled sidecar defaults to the `base-int8` model and English language
hints for more reliable short-form dictation; set both `WHISPER_LANG=auto` and
`MOLTEN_HUB_SPEECH_LANGUAGE=auto` to use Whisper language detection.

### Local Build

Requires Go `1.26.2` or newer plus `git`, `gh`, and the selected agent CLI.

```bash
go build -o bin/harness ./cmd/harness
./bin/harness hub
```

Local `harness hub` listens on `127.0.0.1:7777` by default.

With GitHub auth configured, local hub mode also watches GitHub review-request
notifications by default. When the authenticated GitHub user is still requested
as a PR reviewer, the harness queues the bundled `code-review` task and posts a
summary comment back to the original PR. Auto-merge is off by default; opt in
with `review_watch.auto_merge: true` in the runtime config.

### Go Module

MoltenHub Code is distributed as a Go module from this Git repository. Use a
Git tag such as `v1.0.0` for stable installs.

```bash
go get github.com/Molten-Bot/moltenhub-code@v1.0.0
go install github.com/Molten-Bot/moltenhub-code/cmd/harness@v1.0.0
```

## Bundled Tools

The Docker image includes `railsmith`, an npm CLI for creating and maintaining
repository `AGENTS.md` guardrails. Container startup also seeds a Codex skill
from the package's `AGENT_GUIDE.md` into the persisted Codex home so agents can
activate Railsmith guidance during coding sessions.

```bash
railsmith guide
railsmith doctor --root .
railsmith diff --root . --mode detailed
railsmith check --root .
```

The image also includes `git-changes-by-day`, a Go CLI for exporting git
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
- `MOLTEN_HUB_DEFAULT_REPOSITORY`: optional repository prefill for Prompt
  Studio; omitted leaves the repository field empty.
- `MOLTEN_HUB_SPEECH_HOST`: optional speech sidecar host; defaults to
  `faster-whisper`.
- `MOLTEN_HUB_SPEECH_PORT`: optional speech sidecar Wyoming port; defaults to
  `10300`.
- `MOLTEN_HUB_SPEECH_LANGUAGE`: optional speech language hint; defaults to
  `en`. Set to `auto` to use Whisper language detection.
- `MOLTEN_HUB_SPEECH_DISABLED`: set to `true` to hide prompt dictation.

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
