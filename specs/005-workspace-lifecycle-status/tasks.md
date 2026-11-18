# Tasks: Workspace Lifecycle Status

**Input** — [Spec](spec.md), [plan](plan.md), [milestone map](data-model.md), [contract](contracts/workspace-lifecycle.md), [research](research.md), and [quickstart](quickstart.md)

Write and run the commented unit tests at the start of each coding task. Confirm the relevant regression fails before changing production code. Check off each task in this file as soon as it passes.

## Phase 1 — Setup

Use the existing toolchain, application and test fixtures. The quickstart lists demo prerequisites.

## Phase 2 — Foundation

- [X] T001 Write commented projection and API tests in `internal/web/workspace_authorization_test.go` and `tests/contract/openapi_routes_test.go`, then implement the shared, single-statement lifecycle snapshot and pre-pagination `lifecycleState` filter in `internal/web/workspace_read.go`. Cover the milestone map, older records, failed steps, owner/deleted filters, offboarding takeover and queued maintenance. Keep the existing observed-state filter and proxy authorization. Enforce the [snapshot constraints](data-model.md#workspace-snapshot): `current` is required. `next` is nullable. `mode` is one of `settled`, `transitioning`, `failed`. Settled requires a null next value. Transitioning requires a next milestone. Failed requires next exactly `failed`. Include a nullable Stepper workflow reference. Update `specs/001-shiftbox-mvp/contracts/openapi.yaml` and regenerate `ui/lib/api-types.ts`.

## Phase 3 — US1 Follow workspace progress (P1)

**Independent test** — Start a stopped workspace and follow it from both views and the state filter until Ready. Check repair/retry, later hooks, independent probe status and IDE authorization.

- [X] T002 [US1] Write commented list/detail regressions in `ui/features/workspaces/workspaces.test.tsx`, then wire `ui/components/state-transition.tsx` and `ui/features/workspaces/{workspace.ts,workspaces.tsx,workspace-inventory-row.tsx,workspace-detail.tsx,workspace-commands.ts}` to the snapshot and server-side filter. Cover one settled chip, the immediate target, a skipped polling interval, accepted command refresh, and Stepper selection after reload. Preserve focus/reconnect refresh, probe details and authorized entry. Use the existing selected-operation feed in `ui/lib/use-operation.ts` for detail progress and scoped refresh.

## Phase 4 — US2 See failed and overdue transitions (P2)

**Independent test** — Fail setup and reload. With a controlled clock, trigger an overdue stop and then verify its late completion. The chip changes and the Stepper retains Timed out.

- [X] T003 [P] [US2] Run the single compact calibration pass in `specs/005-workspace-lifecycle-status/quickstart.md` on `shiftbox-demo`. Use the existing first-start and completion timestamps with repeated realistic slow-path runs for each entered transition, including long human waits where applicable. Record sample counts, per-scenario P95 and **2 × the highest scenario P95** per transition in `specs/005-workspace-lifecycle-status/research.md`. Document the approved provisional estimates for unmeasurable retention stop and guest maintenance. Confirm that every default has measured evidence or an explicitly approved provisional estimate before T004.
- [X] T004 [US2] Write commented clock/restart/safety tests in `internal/operation/reconciler_test.go` and `internal/workspace/power_test.go` first. Add equivalent migrations in `migrations/postgres/` and `migrations/sqlite/`. In `internal/operation/{model,journal,store,reconciler}.go`, use the recorded measured and provisional defaults and store the cutoff on first activation. A deadline is an absolute UTC instant, nullable for steps outside scope or transitions never entered, and set once per entered transition. Preserve original entry for in-flight records. After final observation, record one claim-fenced timeout with its occurrence time, blocked step and bounded non-secret reason. Confirm late completion, completion at expiry, retry/restart, and unchanged reconciliation and resource ownership. Expose the annotation in journal reads/events and OpenAPI, regenerate `ui/lib/api-types.ts`, and use it in `internal/web/workspace_read.go` while applicable.
- [X] T005 [US2] Write commented failure/reload and late-completion tests in `ui/components/workflow-stepper.test.tsx` and `ui/features/workspaces/workspaces.test.tsx`. In `ui/components/{state-transition,workflow-stepper}.tsx` and `ui/features/workspaces/workspace-detail.tsx`, render last confirmed → Failed without animation and show the affected milestone and reason in the Stepper. Keep the Timed out annotation visible in the recorded operation after success. Halve the shared gap between chips and verify both views at normal and narrow browser widths.

## Phase 5 — US3 Keep progress visible during a controller outage (P3)

**Independent test** — Lose network or controller access while a connector is displayed. Recover after several steps. Confirm one notice, frozen progress, updated chips and normal handling for a genuine permission/domain response.

- [X] T006 [US3] Write commented outage/recovery and permission tests in `ui/features/workspaces/workspaces.test.tsx` and `ui/lib/use-operation.test.tsx`. Use `ui/app/providers.tsx` for one Mantine modal and the existing read paths in `ui/lib/{api-problem,query-client,use-session,use-operation}.ts` and `ui/features/workspaces/{workspaces,workspace-detail}.tsx`. Keep the last authorized view and connector position visible during unavailability, keep checking even after an initial session failure, and dismiss on a successful response or a genuine authorization/domain response. Cover transport and generic controller failures, recovery, and normal permission/domain handling. Preserve existing action authority and verify focus return.

## Phase 6 — Final verification

- [X] T007 Run the focused checks and review scenarios in `specs/005-workspace-lifecycle-status/quickstart.md`. Deploy the implementation to `shiftbox-demo` using the procedure in `AGENTS.md`. Verify the owner and admin cases in the browser, including refresh/reload, keyboard focus, handoff, IDE permission and safe completion. Record the review URL, ECS revision and results in the quickstart and clean up disposable demo resources.

## Dependencies and execution order

- T001 precedes T002 and fixes the evidence mapping for T003. T003 may run in a subagent while T002 is in progress.
- T004 depends on T001 and recorded defaults from T003. Run it after T002 to avoid concurrent edits to the generated API types.
- T005 follows T002 and T004. T006 follows T005 because both change workspace progress rendering. T007 follows all stories.

## Implementation strategy

Validate T001–T002 as the first reviewable slice. Then add deadline handling and outage recovery. Complete T007 before calling the feature ready.
