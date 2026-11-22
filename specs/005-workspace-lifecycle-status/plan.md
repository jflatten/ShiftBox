# Implementation Plan: Workspace Lifecycle Status

**Branch** — `feature/mvp`

**Date** — 2026-11-18

**Input** — [Feature specification](spec.md)

## Summary

Use one controller-derived lifecycle projection in the existing workspace reads. The Workspaces list filters on the current chip before pagination. Both views render the same Mantine StateTransition, and workspace detail uses the existing operation Stepper. Extend the operation journal with stable transition deadlines and timeout evidence so a late completion can update the live chips. Preserve workspace progress behind a single app-wide notice while reads recover.

## Technical Context

| Area | Existing project and implementation target |
| --- | --- |
| Runtime | Go 1.27.4 controller serving a static TypeScript/React/Next.js UI. Workspace verification reuses existing provider, guest and proxy paths. |
| UI | Mantine StateTransition, Stepper and Modal with the shared theme. TanStack Query handles active-view refresh. |
| Storage | Existing PostgreSQL and SQLite operation journal. Add equivalent migrations for deadline and timeout fields. Use a single statement for the shared lifecycle projection to keep workspace and journal evidence consistent. |
| Testing | Commented Go and Jest unit tests before implementation. Extend the existing OpenAPI and authorization checks, and use a short browser validation on private `shiftbox-demo`. |
| Scope and performance | List and detail at the existing roughly three-second cadence. One batched list read, existing cursor pagination, and one selected operation stream on detail. |
| Constraints | Respect deployed and historical data, existing authorization and workflow authority, retained data and resource ownership, and existing retention and maintenance safeguards. |

## Constitution Check

| Principle | Design and validation |
| --- | --- |
| I. Keep it simple | Reuse the workspace reads, journal, reconciliation loop, browser queries and Mantine components. |
| II. Keep ownership and trust explicit | The controller projects verified evidence. Caller-specific IDE and operation authorization remain enforced by the existing routes. |
| III. Preserve work and reconcile before acting | A fenced final observation precedes timeout recording. The timeout annotation carries zero authority to act on provider resources. |
| IV. Test first and verify real failures | Each coding task starts with commented tests. Verify lost responses, supersession, unavailable reads and safe recovery with representative cases. |
| V. Make workflows visible | Both views use the current and next chips, detail uses the full Stepper, and the shared modal has a title and managed focus. |

The design passes the constitution check after reviewing the lifecycle, control-plane failure and identity design. Use the repository's demo workflow for implementation validation. [Approved architecture](../../documentation/architecture/design-readback.md)

## Project Structure

### Planning artifacts

- [Specification](spec.md) and [requirements checklist](checklists/requirements.md)
- [Research decisions](research.md)
- [Lifecycle milestones and durable fields](data-model.md)
- [Workspace and operation contract](contracts/workspace-lifecycle.md)
- [Calibration and verification guide](quickstart.md)

### Source code

| Area | Files |
| --- | --- |
| Workspace evidence and authority | `internal/workspace/lifecycle.go`, `launch.go`, `power.go`, `delete.go`, `inactivity.go`, `offboarding.go`, `maintenance*.go`, `ide.go` |
| Journal and storage | `internal/operation/{model,journal,store,reconciler}.go`, `migrations/{postgres,sqlite}/` |
| Management API | `internal/web/workspace_read.go`, `internal/web/operation_routes.go`, `internal/web/proxy_routes.go`, `specs/001-shiftbox-mvp/contracts/openapi.yaml`, `ui/lib/api-types.ts` |
| Workspace UI | `ui/features/workspaces/{workspaces,workspace-inventory-row,workspace-detail}.tsx`, `workspace.ts`, `workspace-commands.ts`, `ui/components/{state-transition,workflow-stepper}.tsx` |
| Read recovery | `ui/app/providers.tsx`, `ui/lib/{query-client,api-problem,use-session,use-operation,operation-events}.ts` |

## Implementation Sequence

1. **Show lifecycle status.** Select the authoritative workspace workflow using current-generation operation evidence and derive current, next and mode for both reads. Preserve the confirmed base when an authorized replacement takes over. Keep the active-work reference for command gating and a durable display reference for the Stepper. Apply the additive lifecycle filter before pagination. Update OpenAPI, then wire both views, keep scoped invalidation and focus/reconnect refresh, and verify Ready and Repair-ready separately from access and probe results.
2. **Record durable timeouts.** Calibrate the available displayed transitions on `shiftbox-demo` using [the compact measurement procedure](quickstart.md), and document the owner-approved provisional defaults for the unavailable scenarios. In the journal, set the deadline at first activation using the recorded duration. Backfill/derive it from the original first activation for in-flight rows that already have a start time. Reuse worker observation, normal completion and claim fencing. An expired transition with verified evidence completes through the usual path. An expired, unverified transition gets a bounded, idempotent journal annotation that leaves reconciliation eligible. Carry the annotation on authorized operation reads/events so Stepper history persists through reload and late success.
3. **Handle controller unavailability.** Report outcomes of participating management reads to the UI provider. Recognize network rejection, unusable gateway/controller responses and the generic unexpected controller failure separately from valid domain and authorization problems. Ignore aborted or superseded requests. Keep the most recent authorized view mounted as display-only during the outage, even if a session refetch fails. Preserve its connector and progress, pause the animation in place, and keep visible read polling with explicit reconnect refresh and a recovery path after an initial session failure. Resume normal error handling or use fresh data after recovery. Keep command and proxy authorization based on the existing live checks.
4. **Verify normally.** Run focused tests and contract generation, then validate the built app on the demo with disposable cases. Use authorized owner and administrator views for the handoff and access regressions. Follow the repository rollout procedure during implementation.

Use [the data model](data-model.md) to match each target to its verification evidence. Apply timeout metadata only through lifecycle workers and keep existing domain escalation rules tied to their original authorization and policy.
