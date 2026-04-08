# Claude Harness Authentication

This repository now treats Claude authentication as an onboarding gate in hub UI mode, so users get blocked early with explicit guidance instead of discovering auth failures after a task starts.

## User Onboarding

Based on Anthropic's authentication docs:

- Individual users can authenticate by running `claude` and signing in through the browser flow.
- If the browser does not open automatically, Claude Code allows the user to copy the login URL from the terminal and open it manually.
- Teams can authenticate with Claude for Teams or Enterprise, Claude Console credentials, or supported cloud-provider credentials.
- Non-browser auth can also come from `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, or supported cloud-provider environment variables.

The hub UI now makes onboarding easier for the `claude` harness by:

- blocking local task submission until Claude auth is ready
- surfacing a Claude-specific title and message instead of Codex-only text
- linking users to the official Claude authentication docs when browser login is still required
- allowing the `Done` verification step even when the flow does not expose a device code

## Readiness Heuristics

The current Claude auth gate treats auth as ready when any of these are present:

- `ANTHROPIC_API_KEY`
- `ANTHROPIC_AUTH_TOKEN`
- `CLAUDE_CODE_USE_BEDROCK`
- `CLAUDE_CODE_USE_VERTEX`
- `CLAUDE_CODE_USE_FOUNDRY`
- a non-empty Claude credential file at `$CLAUDE_CONFIG_DIR/.credentials.json`
- a non-empty Claude credential file at `~/.claude/.credentials.json`

If none of those are present, the UI triggers `claude login`, surfaces the browser URL from CLI output, and asks the user to complete sign-in before clicking `Done`.
The docs link (`https://code.claude.com/docs/en/authentication`) is guidance only and is not an authorization URL.

## Molten Hub Integration Behavior

The repository now includes a focused local snapshot at [`na.hub.molten.bot.openapi.yaml`](../na.hub.molten.bot.openapi.yaml), reviewed against the live spec at `https://na.hub.molten.bot/openapi.yaml`.
The notes below reflect that spec plus the runtime implementation in [`internal/hub/api.go`](../internal/hub/api.go) and [`internal/hub/daemon.go`](../internal/hub/daemon.go).

Observed transport behavior:

- runtime registration patches `/agents/me/metadata`
- task results publish to `/openclaw/messages/publish`
- pull fallback uses `/openclaw/messages/pull`
- delivery acknowledgement uses `/openclaw/messages/ack`
- delivery release uses `/openclaw/messages/nack`
- websocket transport uses `/openclaw/messages/ws`
- runtime offline signaling uses `/openclaw/messages/offline`

Spec review note:

- the live OpenAPI contract does not document `/openclaw/messages/register-plugin`
- runtime registration data belongs in agent metadata, not a separate undocumented transport route

## Failure Contract

When a task fails, the runtime already follows this contract:

- the reply payload marks the task as failed
- the reply payload includes `error`, `message`, and `failure.details`
- the human-readable message is `Failure: task failed. Error details: ...`
- a follow-up remediation task is queued when the error looks locally fixable
- follow-up prompts include the failing log paths when available
- follow-up prompts use the required run-config shape:

```json
{
  "repos": ["<same_repo_as_failed_task>"],
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."
}
```

## Source

Official Claude authentication reference:

- https://code.claude.com/docs/en/authentication
