# PR Review: #376 `moltenhub-On click -> Done -> all fields should become readonly; while the`

## Summary
- Scope reviewed: hub setup onboarding progress tracking across backend state (`cmd/harness/main.go`) and frontend rendering/interaction (`internal/hubui/static/index.html`, `internal/hubui/static/style.css`) plus related tests.
- GitHub context verified: PR [#376](https://github.com/Molten-Bot/moltenhub-code/pull/376), base `main`, head `moltenhub-on-click-done-all-fields-should-become-r`.
- Existing PR discussion reviewed before conclusions: no existing comments or reviews at time of review.
- Offline integration check performed against `na.hub.molten.bot.openapi.yaml` for expected Hub endpoint behavior alignment.

## Findings (Ordered by Severity)

### Medium
1. **Onboarding error states can retain a stale `current` step, causing contradictory progress UI after failure.**
- Evidence:
  - The flow marks `bind` as `current` before validation/network work starts: `cmd/harness/main.go:1552`.
  - Several early returns can fail without converting that step to `error` (for example `load runtime config`, token validation, missing saved credentials): `cmd/harness/main.go:1560`, `cmd/harness/main.go:1568`, `cmd/harness/main.go:1573`.
  - In a later failure case, `work_bind` is left as `current` while `profile_set` is marked `error`: `cmd/harness/main.go:1592`, `cmd/harness/main.go:1611`.
  - Frontend renders every non-`pending` step and styles `current` visually as in-progress, so stale `current` status remains visible after request failure: `internal/hubui/static/index.html:1394`, `internal/hubui/static/index.html:1401`.
- Impact:
  - Users can see an in-progress indicator and an error indicator simultaneously after a failed submission, which misrepresents runtime state and makes failure recovery ambiguous.
- Recommendation:
  - Ensure all error exits explicitly transition the active step to `error` (or move step state forward before risky calls), and guarantee only one terminally failed step is marked when the operation aborts.

## Open Questions
- Should onboarding semantics enforce a single active/terminal step invariant (`current` xor `error`), or is mixed state display intentional for partial-progress diagnostics?
- Should `ActivationReady` represent "credentials configured" or "currently connected/active"? Current code sets it both ways depending on code path (`cmd/harness/main.go:1538`, `cmd/harness/main.go:1714`).

## Validation Notes
- Local verification run:
  - `go test ./...` (pass)
- Offline Hub snapshot inspected:
  - `na.hub.molten.bot.openapi.yaml` (reviewed for integration behavior context; no new endpoint changes introduced by this PR).
