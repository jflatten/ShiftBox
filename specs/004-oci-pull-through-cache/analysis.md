# Specification Analysis Report

**Feature/date**: Embedded OCI pull-through image cache, 2026-11-17.\
**Current baseline**: 24 tasks, six combined validation checks, existing settings/journal/guest/UI patterns. Three planning tasks complete after final verification; 21 runtime tasks remain unchecked. This reports static documentation alignment only.

## Workflow and pass history

Applied `speckit-specify → speckit-plan → speckit-tasks → speckit-analyze`, authorized fixes, then analysis reruns. Resolved composed spec/task templates and ran setup-plan, setup-tasks and scoped prerequisite scripts with `SPECIFY_FEATURE_DIRECTORY=specs/004-oci-pull-through-cache`; no MVP feature artifact was overwritten. `.specify/extensions.yml` is absent, so no mandatory hooks were registered. Analysis passes were read-only; reports/remediation were saved outside those passes. No speckit-converge or runtime implementation was run; the final request is preimplementation document alignment.

| Pass | Findings | Remediation / outcome |
| --- | --- | --- |
| 1 | 5: 2 high, 2 medium, 1 low | Clarified bounded work and non-stale 429, corrected task counts, explicitly tasked URL-tab persistence and saved analysis. |
| 2 | 0 | Expanded draft static coverage clean; runtime gates unproved. |
| 3 | 0 | Checked delivered expanded draft. Subsequent user simplification superseded that draft. |
| 4 | 2: 1 medium, 1 low | Simplified draft still had old task mapping in report and overly broad edge-case list. Replaced both; aligned all documents with quick delivery. |
| 5 | **0 at all severities** | Final simplified artifacts align; all 17 requirements, 3 stories, 12 UI rows and 7 gates map to focused work/checks. |

The user then required a quicker implementation, so the final design removes arbitrary new pool/timeout/buffer defaults, separate activation/metrics/retry subsystems, per-variant test files and the exhaustive registry × builder × platform matrix. Use existing persistence/journal and a few fixtures. Physical disk/write reservations, public-only outbound paths and the full admin UI remain necessary accepted behavior; simplification does not waive them.

## Current requirements-to-work coverage

Each row has buildable tasks and evidence in the six checks of [quickstart.md](quickstart.md). No requirement is treated as passed runtime behavior.

| Requirement | Has task? | Task IDs | Focused evidence |
| --- | --- | --- | --- |
| FR-001 Embed / license | Yes | T004–T008, T021–T022 | Check 1 + actual linked packaging inventory in check 6. |
| FR-002 Anonymous public / outbound isolation | Yes | T005, T007–T008, T013–T014 | Shared HTTP/DNS fixture, check 2. |
| FR-003 Original refs / prebuilt and FROM | Yes | T006–T008, T012 | Same unchanged prebuilt/Dockerfile fixture, checks 1/6. |
| FR-004 Tags / shared digest | Yes | T009–T012 | Tag/digest and count-once cases, checks 1/3. |
| FR-005 Complete stale / warning recovery | Yes | T013–T016 | Check 4, UI-12. |
| FR-006 Registered guest / image-only exception | Yes | T006–T008, T013–T014 | Current identity and restricted routing, check 2. |
| FR-007 Pre-write cache/volume budget | Yes | T009–T010, T017–T020 | Two concurrent pulls, pressure, oversize and path case, checks 3/5. |
| FR-008 No retention / allowed direct fallback | Yes | T009–T010, T013–T014 | Zero temp writes and restricted/allowed mode, checks 2/3. |
| FR-009 Complete admin UI | Yes | T017–T020 | Two UI/API journeys, check 5; all matrix rows. |
| FR-010 Usage / effectiveness / byte truth | Yes | T009–T011, T018–T020, T023 | Same fixture's byte counters and timing, checks 3/5/6. |
| FR-011a Read-only cached-image inventory | Yes | T018–T020 | Existing admin journey, UI-13. |
| FR-011 Settings / restart / rollback | Yes | T014, T017–T022 | Existing settings/migration/recovery fixtures, checks 3/5/6. |
| FR-012 Commented tests first / shared demo | Yes | T005–T024 | Focused unit/UI checks precede implementation; final demo/cleanup. |
| SC-001 Original refs across two registries | Yes | T006–T008, T012, T023 | One primary fixture + second-registry compatibility, checks 1/6. |
| SC-002 Warm bytes / actual IDE timing | Yes | T009–T012, T023 | Three fresh direct/cold/warm runs, check 6. |
| SC-003 Trust/policy failures denied | Yes | T005–T008, T013–T014 | Representative outbound/identity and fallback checks 2/4. |
| SC-004 Capacity under normal concurrency | Yes | T009–T010, T017–T020 | Two pulls + bounded pressure/interrupt, check 3. |
| SC-005 UI and recovery truthful | Yes | T015–T020, T023–T024 | Checks 4/5 + signed-in demo review. |

## UI, stories and gates

The [matrix](contracts/admin-ui.md) retains every required control/status with backend contract, persistence, permission and E2E evidence; it does not require twelve separate test suites.

- UI-01–05 settings/activation: T017–T020, check 5.
- UI-06 health/identity and UI-07–08 usage/eviction/fallback: T005–T014/T017–T020, checks 2/3/5.
- UI-09 effectiveness: T009–T011/T018–T020/T023, checks 3/5/6.
- UI-10 retired paths and UI-11 refresh/nonhappy/access states: T017–T020/T024, check 5.
- UI-12 workspace freshness: T013–T016/T020, check 4.
- UI-13 cached images and permissions: T018–T020, check 5.

US1 has an independent transparent reuse check (T012); US2 outage/policy check (T016); US3 save/reload/permission check (T020). G1/G2 map to focused check 1, G3 to 2, G4 to 3, G5 to 4, G6 to 5 and G7 to 6. All stories and production eviction/admin UI are required to ship. No task is unmapped: planning serves governance; foundation serves shared trust/routing; release serves packaging/compatibility/demo/cleanup.

## Constitution and scope

KISS uses one controller process and existing product patterns. Trust remains controller-owned, public-only and per-VM. Eviction never gains authority over user/DB/key data. Tests are commented and first, focused on actual accepted behavior; no new general testing framework. Mantine MCP, Stepper/chips, Carbon/branding and permissions/loading/error states remain explicit. No constitution MUST is weakened and no architecture reference is modified.

No product decision blocks planning. Distribution's safe adaptation, actual Docker/BuildKit any-public routing, no-retention streaming, pre-write volume protection and live UI/performance remain **unproved implementation gates**. Source evidence is not runtime proof or a speed claim.

## Branch and delivery evidence

Saved checkout `/Users/jflatten/Workspace/ShiftBox` stays on `feature/mvp`; planning worktree `/Users/jflatten/.codex/worktrees/6262/ShiftBox` uses `codex/004-oci-pull-through-cache`. Both HEADs/merge base are `798d5299d5798da664b68269e9963087a27a5137`; left/right commit difference is `0 0`. Existing keep-alive source/test/task changes are preserved. Only this new feature directory and the exact root AGENTS.md UI-completeness rule are delivered; no runtime file is copied.

Final verification records prerequisite/Markdown link/task-dependency checks, matching document copies, clean whitespace, unchanged architecture and preserved unrelated hashes. No runtime tests, AWS operations, migrations, deployment, commit or push occurred.

## Final analysis and validation result

Pass 5 completed all six read-only analysis categories with zero unresolved findings at every severity: critical 0, high 0, medium 0, low 0. Static requirement coverage is 17/17 (100%); 3/3 stories, 12/12 UI rows and G1–G7 map to the six focused combined checks. The 24 sequential tasks have valid backward dependencies, four [P] tasks in two groups and no unmapped work. Only T001–T003 are completed; all 21 runtime/implementation tasks remain unchecked.

Scoped prerequisites, local Markdown links, task format/dependency checks, whitespace and unrelated-file hashes pass. Both delivered documentation copies are kept identical. There is no unresolved planning blocker or product question; technical feasibility, live behavior and performance remain honestly unproved implementation gates. No further testing permutations or generic infrastructure were added to clear analysis.
