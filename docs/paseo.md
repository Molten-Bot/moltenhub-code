# Paseo Feature Report

This report stack-ranks interesting and core features in `repo-01-paseo`. Rank weights product centrality, implementation depth, and how much feature defines Paseo versus being supporting infrastructure.

## 1. Self-Hosted Agent Daemon

**Summary:** Paseo's core is a local daemon that runs coding agents as subprocesses on the user's own machine, keeps sessions alive, persists state, and exposes a WebSocket/API surface to every client. This is the feature that makes Paseo distinct from hosted agent products: credentials, tools, working directories, and model CLIs stay in the user's environment while clients only control and observe the daemon.

**Areas:**

- `packages/server/src/server/bootstrap.ts`
- `packages/server/src/server/websocket-server.ts`
- `packages/server/src/server/agent/agent-manager.ts`
- `packages/server/src/server/agent/agent-storage.ts`
- `packages/server/src/server/session.ts`
- `packages/protocol/src/messages.ts`
- `public-docs/why.md`
- `public-docs/security.md`

## 2. Multi-Provider Agent Runtime

**Summary:** Paseo wraps many agent CLIs behind one provider contract. Native providers include Claude, Codex, OpenCode, Copilot, and pi; generic ACP support extends that to a catalog of 30+ agents and user-defined providers. Provider definitions include launch behavior, models, modes, permission posture, runtime settings, and voice eligibility, so the UI/CLI can treat different agent systems as one operational surface.

**Areas:**

- `packages/server/src/server/agent/provider-registry.ts`
- `packages/server/src/server/agent/providers/`
- `packages/protocol/src/provider-manifest.ts`
- `packages/protocol/src/importable-providers.ts`
- `packages/app/src/components/combined-model-selector.tsx`
- `packages/app/src/components/add-provider-modal.tsx`
- `packages/app/src/components/provider-settings-host.tsx`
- `public-docs/providers.md`
- `public-docs/supported-providers.md`
- `public-docs/custom-providers.md`

## 3. Cross-Device Client Model With End-to-End Encrypted Relay

**Summary:** Paseo supports desktop, mobile, web, and CLI clients connecting to the same daemon. Remote access is not bolted on: pairing offers, QR codes, relay transport, direct host connections, and password auth are part of the runtime. The relay is designed as untrusted transport, using daemon keypairs and encrypted channels so mobile clients can reach a local daemon without opening inbound ports.

**Areas:**

- `packages/server/src/server/relay-transport.ts`
- `packages/server/src/server/connection-offer.ts`
- `packages/server/src/server/pairing-offer.ts`
- `packages/server/src/server/pairing-qr.ts`
- `packages/server/src/server/daemon-keypair.ts`
- `packages/server/src/server/auth.ts`
- `packages/relay/src/e2ee.ts`
- `packages/relay/src/encrypted-channel.ts`
- `packages/client/src/daemon-client-relay-e2ee-transport.ts`
- `packages/app/src/app/pair-scan.tsx`
- `packages/app/src/components/pair-link-modal.tsx`
- `public-docs/security.md`

## 4. Git Worktrees, Workspace Setup, and Per-Worktree Services

**Summary:** Paseo treats parallel agent work as a git/workspace problem. Agents can run in isolated worktrees, branch from a base, attach to PRs, run setup/teardown hooks, open configured terminals, and launch project scripts. Long-running services get assigned ports and reverse-proxy hostnames, letting multiple worktree copies of the same app run side by side without port collisions.

**Areas:**

- `packages/server/src/server/worktree-session.ts`
- `packages/server/src/server/paseo-worktree-service.ts`
- `packages/server/src/server/worktree/`
- `packages/server/src/server/worktree-bootstrap.ts`
- `packages/server/src/server/service-proxy.ts`
- `packages/server/src/server/workspace-script-runtime-store.ts`
- `packages/server/src/utils/worktree.ts`
- `packages/cli/src/commands/worktree/`
- `packages/app/src/components/workspace-setup-dialog.tsx`
- `public-docs/worktrees.md`

## 5. Unified Workspace UI: Agents, Terminals, Files, Diffs, Browser Panes

**Summary:** The Expo app is not just chat. It is a multi-pane workspace that can hold agent streams, terminals, file explorer panes, diff views, browser panes, and draft tabs. The app has command-center navigation, draggable split panes, mounted tab preservation, sidebars, archive views, provider settings, PR panes, and terminal/browser/file interactions across desktop, web, and mobile targets.

**Areas:**

- `packages/app/src/screens/workspace/`
- `packages/app/src/components/split-container.tsx`
- `packages/app/src/components/command-center.tsx`
- `packages/app/src/components/agent-list.tsx`
- `packages/app/src/components/terminal-pane.tsx`
- `packages/app/src/components/file-explorer-pane.tsx`
- `packages/app/src/components/diff-viewer.tsx`
- `packages/app/src/components/browser-pane.*.tsx`
- `packages/app/e2e/`

## 6. Agent Timeline, Streaming, Permissions, and Attention Routing

**Summary:** Paseo normalizes agent output into timelines, tool-call displays, stream events, permission requests, status buckets, and attention notifications. This lets clients show rich progress instead of raw terminal text, while still allowing live attach/log views. Permission flows are first-class across UI, CLI, MCP, and push notifications.

**Areas:**

- `packages/server/src/server/agent/agent-timeline-store.ts`
- `packages/server/src/server/agent/timeline-projection.ts`
- `packages/server/src/server/agent/activity-curator.ts`
- `packages/server/src/server/agent/permission-response.ts`
- `packages/server/src/server/agent-attention-policy.ts`
- `packages/protocol/src/agent-attention-notification.ts`
- `packages/protocol/src/tool-call-display.ts`
- `packages/app/src/agent-stream/`
- `packages/app/src/components/tool-call-details.tsx`
- `packages/app/src/components/question-form-card.tsx`
- `packages/cli/src/commands/permit/`

## 7. Voice Control and Local-First Speech

**Summary:** Paseo has dictation and realtime voice mode, with local STT/TTS as the default path and OpenAI speech as a configurable option. Voice mode uses hidden agent sessions and MCP-style tools so spoken requests can start and control agents. Local model management, readiness reporting, multilingual STT options, and TTS tuning are represented in config and daemon capability state.

**Areas:**

- `packages/server/src/server/speech/`
- `packages/server/src/server/dictation/`
- `packages/server/src/server/agent/stt-manager.ts`
- `packages/server/src/server/agent/tts-manager.ts`
- `packages/server/src/server/voice-types.ts`
- `packages/server/src/server/voice-local-agent.e2e.test.ts`
- `packages/app/src/components/dictation-controls.tsx`
- `packages/app/src/components/realtime-voice-overlay.tsx`
- `packages/expo-two-way-audio/`
- `public-docs/voice.md`

## 8. Paseo MCP: Agents Controlling Agents

**Summary:** Paseo exposes an MCP server that can be injected into launched agents. Tools cover creating agents, waiting for completion, sending prompts, managing terminals, schedules, providers, worktrees, permissions, and voice `speak`. This turns Paseo from a human-operated dashboard into an agent orchestration substrate: an agent can delegate work, monitor another agent, inspect activity, or create worktrees without leaving the daemon's controlled API.

**Areas:**

- `packages/server/src/server/agent/mcp-server.ts`
- `packages/server/src/server/agent/mcp-shared.ts`
- `packages/server/src/server/agent/agent-mcp.e2e.test.ts`
- `packages/server/src/server/agent/mcp-parity.e2e.test.ts`
- `packages/server/src/server/bootstrap.ts`
- `public-docs/mcp.md`
- `public-docs/skills.md`

## 9. CLI Parity and Scriptable Agent Operations

**Summary:** The `paseo` CLI mirrors the daemon API for agent lifecycle, logs, streaming, messages, imports, daemon control, terminals, loops, schedules, permissions, providers, speech, and worktrees. It supports remote daemon targets, relay offer URLs, JSON/YAML/table output, quiet IDs for shell scripts, image attachments, structured output schemas, and detached runs. This makes Paseo usable headless and useful as a tool inside other agents' workflows.

**Areas:**

- `packages/cli/src/cli.ts`
- `packages/cli/src/commands/agent/`
- `packages/cli/src/commands/daemon/`
- `packages/cli/src/commands/terminal/`
- `packages/cli/src/commands/loop/`
- `packages/cli/src/commands/schedule/`
- `packages/cli/src/commands/provider/`
- `packages/cli/src/output/`
- `packages/client/src/daemon-client.ts`
- `public-docs/cli.md`

## 10. Loops and Schedules for Autonomous Follow-Through

**Summary:** Paseo includes two automation layers. Loops repeatedly run worker agents, execute verification commands, optionally ask verifier agents for structured pass/fail judgments, and track iteration logs. Schedules run prompts later or repeatedly on new, existing, or self-targeted agents using interval or cron cadence with run history, expiry, max-runs, pause/resume, and manual trigger controls.

**Areas:**

- `packages/server/src/server/loop-service.ts`
- `packages/server/src/server/schedule/service.ts`
- `packages/server/src/server/schedule/cron.ts`
- `packages/protocol/src/loop/rpc-schemas.ts`
- `packages/protocol/src/schedule/`
- `packages/cli/src/commands/loop/`
- `packages/cli/src/commands/schedule/`
- `public-docs/schedules.md`

## 11. GitHub and PR-Aware Review/Ship Workflow

**Summary:** Paseo observes git state and GitHub PR metadata for workspaces: branch, base ref, ahead/behind counts, diff stats, pull request state, checks, review decision, mergeability, timelines, and PR checkout targets. The app can surface PR panes and diff status, while the daemon watches repos, caches heavy git reads, polls GitHub, and invalidates snapshots as work changes.

**Areas:**

- `packages/server/src/server/workspace-git-service.ts`
- `packages/server/src/services/github-service.ts`
- `packages/server/src/server/checkout-diff-manager.ts`
- `packages/server/src/server/checkout/`
- `packages/server/src/server/auto-archive-on-merge/`
- `packages/app/src/components/diff-viewer.tsx`
- `packages/app/e2e/pr-pane.spec.ts`
- `packages/app/e2e/workspace-cwd.spec.ts`

## 12. Terminal Multiplexing and Binary Stream Protocol

**Summary:** Paseo includes managed terminals, terminal tabs, attach/capture/send-key operations, split-resize behavior, alternate-screen handling, and performance-focused terminal stream routing. Protocol packages define binary terminal frames, snapshots, key input, stream parsing, and restore behavior so terminal I/O can move efficiently over WebSocket instead of being treated as loose text.

**Areas:**

- `packages/server/src/terminal/`
- `packages/protocol/src/binary-frames/terminal.ts`
- `packages/protocol/src/terminal-stream-protocol.ts`
- `packages/protocol/src/terminal-snapshot.ts`
- `packages/protocol/src/terminal-key-input.ts`
- `packages/client/src/terminal-stream-router.ts`
- `packages/cli/src/commands/terminal/`
- `packages/app/src/components/terminal-pane.tsx`
- `packages/app/src/components/terminal-emulator.*.tsx`
- `packages/app/e2e/terminal-*.spec.ts`

## 13. Attachments, File Links, and File Transfer

**Summary:** Paseo supports richer prompts and outputs than plain text. Client and protocol code handle image/file attachments, assistant file links, file explorer views, highlighted code, secure download tokens, and binary file-transfer frames. This matters because coding-agent sessions often need screenshots, generated files, diff inspection, and click-through source references.

**Areas:**

- `packages/app/src/attachments/`
- `packages/app/src/assistant-file-links/`
- `packages/app/src/components/file-drop-zone.tsx`
- `packages/app/src/components/attachment-pill.tsx`
- `packages/app/src/components/attachment-lightbox.tsx`
- `packages/server/src/server/file-download/`
- `packages/server/src/server/file-explorer/`
- `packages/protocol/src/binary-frames/file-transfer.ts`
- `packages/app/e2e/composer-attachments.spec.ts`

## 14. Configuration, Security, and Host Hardening

**Summary:** Paseo has a detailed configuration model covering daemon listen targets, host allowlists, CORS, MCP, worktree roots, logging, auth, relay, providers, and voice. Security-relevant pieces include localhost defaults, Unix sockets/pipes, DNS rebinding host validation, password authentication with bcrypt hashes, relay encryption, and explicit warnings around binding public interfaces.

**Areas:**

- `packages/server/src/server/config.ts`
- `packages/server/src/server/persisted-config.ts`
- `packages/server/src/server/daemon-config-store.ts`
- `packages/server/src/server/hostnames.ts`
- `packages/server/src/server/auth.ts`
- `packages/protocol/src/paseo-config-schema.ts`
- `packages/cli/src/commands/daemon/set-password.ts`
- `public-docs/configuration.md`
- `public-docs/security.md`

## 15. Desktop Distribution and Embedded Daemon UX

**Summary:** Paseo's desktop app wraps the daemon startup path so users can launch a local host without manually running the CLI. The Electron package pairs with the Expo web export and server build, and the app contains startup/bootstrap flows for detecting the embedded daemon, showing startup progress, and routing to the last active workspace.

**Areas:**

- `packages/desktop/`
- `packages/app/src/desktop/`
- `packages/app/src/app/host-runtime-bootstrap.ts`
- `packages/app/src/screens/startup-splash-screen.tsx`
- `packages/app/src/app/index.tsx`
- `packages/app/e2e/desktop-updates.spec.ts`
- `public-docs/index.md`

## Notes From Review

- `packages/server/README.md` appears stale: it describes an older voice-assistant/Express/Vite plan, while the root README, `public-docs`, package scripts, and server source show the current daemon/client/orchestration product.
- The most product-defining capabilities are not isolated in one package. They cross `server`, `protocol`, `client`, `app`, `cli`, and `relay`; feature ownership is best understood by following daemon API flows and protocol messages.
- Paseo's strongest through-line is "run existing agents locally, then make them controllable from anywhere." Worktrees, relay, MCP, CLI parity, schedules, voice, and UI panes all reinforce that same center.
