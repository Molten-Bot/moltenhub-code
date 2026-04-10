# Native Desktop Distribution Plan

## Decision

Use a native-first distribution model, not WASM.

Reason: this app is fundamentally a local process orchestrator. It must run host tools (`git`, `gh`, `codex`/`claude`/`auggie`), manage local workspaces, and perform authenticated CLI flows. Those are native OS responsibilities and are already implemented in Go.

## What We Are Shipping

Primary runtime:
- One cross-compiled Go binary per OS/arch target (`harness`).
- Existing local web UI served by `harness hub`.

Packaging:
- macOS: tarball + Homebrew formula (optional `.pkg`/`.dmg` later).
- Windows: zip + winget/Scoop manifest (optional `.msi` later).
- Linux: tarball + `.deb` + `.rpm` (and optional `.AppImage`).

## Platform Targets

Initial support matrix:
- `darwin/arm64`
- `darwin/amd64`
- `windows/amd64`
- `linux/amd64`
- `linux/arm64` (if CI capacity allows)

## Runtime Dependency Model

The Go binary is portable; task execution still depends on host tools.

Required external tools:
- `git`
- `gh` (GitHub CLI)
- selected agent CLI: `codex`, `claude`, or `auggie`

Approach:
1. Do not bundle these initially.
2. Keep boot diagnostics as gate checks (already present).
3. Add explicit install instructions per OS in docs and startup output.
4. Optionally evaluate bundling only after release stability.

## Configuration and Data Paths

Move from cwd-relative defaults to OS-native paths while keeping override support.

Targets:
- macOS: `~/Library/Application Support/MoltenHubCode/`
- Windows: `%AppData%\MoltenHubCode\`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/moltenhub-code/`

Keep compatibility:
- Continue honoring `HARNESS_RUNTIME_CONFIG_PATH`.
- Migrate legacy `./.moltenhub/config.json` when discovered.

## Implementation Plan

## Phase 1: Binary Portability Hardening

Deliverables:
- Verify `CGO_ENABLED=0` builds for all target OS/arch pairs.
- Add CI matrix for build + smoke start (`harness --help`, `harness hub --ui-listen ""`).
- Document supported architectures and minimum OS versions.

Acceptance:
- Reproducible artifacts for each target.
- Startup works without platform-specific code changes.

## Phase 2: Native Path Migration

Deliverables:
- Introduce centralized runtime-path resolver for config/log/state locations.
- Default to OS-native paths.
- Add migration logic from existing relative paths.

Acceptance:
- Fresh install works from any working directory.
- Existing users retain config after upgrade.

## Phase 3: Packaging Pipeline

Deliverables:
- Add `goreleaser` (or equivalent) config for:
  - archives (`.tar.gz`, `.zip`)
  - Linux packages (`.deb`, `.rpm`)
- Add Homebrew tap formula publishing.
- Add winget/Scoop manifest generation.

Acceptance:
- Installable artifacts produced from tagged releases.
- Checksums and changelog published with each release.

## Phase 4: Signing and Trust

Deliverables:
- macOS signing (+ notarization if `.pkg`/`.dmg` used).
- Windows code signing for installer/binary where applicable.
- Artifact provenance: SHA256 + SBOM.

Acceptance:
- No trust warnings beyond unavoidable first-party policy prompts.
- Signed artifacts verified in CI release checks.

## Phase 5: Operator UX for Dependencies

Deliverables:
- Add `harness doctor` command to print:
  - required tools availability/versions
  - auth readiness (`gh auth status`, agent auth state)
  - config path and permissions diagnostics
- Add platform-specific remediation hints.

Acceptance:
- Users can resolve most setup failures without reading source or logs.

## Optional Phase 6: Thin Native Launcher

If product direction needs app-like UX:
- Add a thin native launcher (Tauri/Electron/native shell) that starts the Go daemon and opens the local UI.
- Keep all runtime logic in Go.

This is optional and should not block native CLI packaging.

## Trade-offs

Benefits:
- Minimal architecture churn.
- Fast path to reliable desktop distribution.
- Preserves existing process and filesystem capabilities.

Costs:
- External dependency management remains real (`git`, `gh`, agent CLI).
- Packaging/signing complexity still exists per OS.

## Risks and Mitigations

1. Missing host dependencies
- Mitigation: `harness doctor`, startup diagnostics, install docs, package-manager instructions.

2. Path/permission regressions during migration
- Mitigation: migration tests and fallback to explicit env overrides.

3. Cross-platform behavior drift
- Mitigation: CI matrix with smoke tests on all release targets.

4. Signing/notarization delays
- Mitigation: implement signing early, before public release automation.

## Recommended First PRs

1. Add `docs/NATIVE_DESKTOP.md` and remove WASM plan.
2. Implement runtime path resolver + migration.
3. Add release build matrix for `darwin/windows/linux`.
4. Add package publishing pipeline (`.deb`, `.rpm`, archives).
5. Add `harness doctor` diagnostics command.
