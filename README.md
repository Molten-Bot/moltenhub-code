# MoltenHub Code

The fastest way to run AI agents across your GitHub repositories.

For more information, see [molten.bot/code](https://molten.bot/code).

---

## Quick Start

### Docker Images

| Harness | npm Package | cmd |
|---------|------------|------------|
| `codex` | `@openai/codex@latest` | docker run -p 7777:7777 moltenai/moltenhub-code:latest-codex |
| `claude` | `@anthropic-ai/claude-code@latest` | docker run -p 7777:7777 moltenai/moltenhub-code:latest-claude |
| `auggie` | `@augmentcode/auggie@latest` | docker run -p 7777:7777 moltenai/moltenhub-code:latest-auggie |
| `pi` | `@mariozechner/pi-coding-agent@latest` | docker run -p 7777:7777 moltenai/moltenhub-code:latest-pi |

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

The harness owns steps 5 and 6. Agent prompts are limited to repository changes, local validation, and clear failure/no-op reporting so tasks do not fail just because remote GitHub or CI access is unavailable inside the agent runtime.

**Branch & PR rules:**

- Starts from `main` → creates a new branch
- Already on a non-`main` branch → reuses that branch
- Branch names are always prefixed with `moltenhub-`
- PR titles must not be prefixed with `moltenhub-`

---

## Failure Behavior

When a task fails, the harness:

1. Returns a failure response to the calling agent with clear error details
2. Re-runs the original task once
3. Queues a focused remediation follow-up task in the MoltenHub code repository
4. Passes relevant failing log paths into the follow-up prompt

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

Follow-up prompts also tell the agent to report failures clearly, return documented no-op results when nothing needs to change, and leave PR creation plus remote check monitoring to the harness.

---

## Configuration

### Response Modes

Run configs can optionally set `responseMode` to compress agent prose without changing the underlying task flow. Supported values:

- `default`
- `off`
- `caveman-lite`
- `caveman-full`
- `caveman-ultra`
- `caveman-wenyan-lite`
- `caveman-wenyan-full`
- `caveman-wenyan-ultra`

MoltenHub Code defaults omitted or `default` `responseMode` to `caveman-full`. Set `off` for normal prose.

MoltenHub Code applies the bundled Caveman skill as a prompt overlay, so the same `responseMode` works across all supported harnesses (`codex`, `claude`, `auggie`, `pi`) without depending on provider-specific plugins or hooks.

Example run config fragment:

```json
{
  "repos": ["git@github.com:owner/repo.git"],
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": "Fix the failing tests and update coverage.",
  "responseMode": "off"
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
