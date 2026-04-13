# MoltenHub Code

The fastest way to run AI agents across your GitHub repositories.

For more information, see [molten.bot/code](https://molten.bot/code).

---

## Quick Start

### Docker Images

| Harness | npm Package | docker |
|---------|------------|------------|
| `codex` | `@openai/codex@latest` | `moltenai/moltenhub-code:latest-codex` |
| `claude` | `@anthropic-ai/claude-code@latest` | `moltenai/moltenhub-code:latest-claude` |
| `auggie` | `@augmentcode/auggie@latest` | `moltenai/moltenhub-code:latest-auggie` |
| `pi` | `@mariozechner/pi-coding-agent@latest` | `moltenai/moltenhub-code:latest-pi` |

### Local

**Build:**

```bash
go build -o bin/harness ./cmd/harness
```

**Run:**

```bash
./bin/harness hub
```

---

## Runtime Behavior

Each run follows this sequence:

1. Verifies required tooling and auth (`git`, `gh`, selected agent CLI)
2. Creates an isolated workspace
3. Clones target repo(s) and checks out `baseBranch`
4. Runs the configured agent in `targetSubdir` (or workspace root for multi-repo runs)
5. Opens or updates PRs for any changed repos
6. Waits for required checks

**Branch & PR rules:**

- Starts from `main` → creates a new branch
- Already on a non-`main` branch → reuses that branch
- Branch names and PR titles are always prefixed with `moltenhub-`

---

## Failure Behavior

When a task fails, the harness:

1. Returns a failure response to the calling agent with clear error details
2. Queues a focused remediation follow-up task in the MoltenHub code repository
3. Passes relevant failing log paths into the follow-up prompt

The follow-up run config looks like this:

```json
{
  "repos": ["git@github.com:Molten-Bot/moltenhub-code.git"],
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."
}
```

> The follow-up contract also includes: issue an offline to MoltenHub → review `na.hub.molten.bot.openapi.yaml` for integration behaviours.

---

## Configuration

### Response Modes

Run configs can optionally set `responseMode` to compress agent prose without changing the underlying task flow. Supported values:

- `default`
- `caveman-lite`
- `caveman-full`
- `caveman-ultra`
- `caveman-wenyan-lite`
- `caveman-wenyan-full`
- `caveman-wenyan-ultra`

MoltenHub Code applies the bundled Caveman skill as a prompt overlay, so the same `responseMode` works across all supported harnesses (`codex`, `claude`, `auggie`, `pi`) without depending on provider-specific plugins or hooks.

Example run config fragment:

```json
{
  "repos": ["git@github.com:owner/repo.git"],
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": "Fix the failing tests and update coverage.",
  "responseMode": "caveman-full"
}
```


**Hub OpenAPI spec:**
- Live: [`https://na.hub.molten.bot/openapi.yaml`](https://na.hub.molten.bot/openapi.yaml)
- Offline snapshot: [`na.hub.molten.bot.openapi.yaml`](na.hub.molten.bot.openapi.yaml)

---

**Run with persistent local config** (mount `.moltenhub` to `/workspace/config`):

```bash
mkdir -p ./.moltenhub
docker run --rm -p 7777:7777 \
  -e HOME=/tmp \
  -v "$PWD/.moltenhub:/workspace/config" \
  moltenhub-code:latest-claude
```

## Testing

```bash
go test ./...
```
