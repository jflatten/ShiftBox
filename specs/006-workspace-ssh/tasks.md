---
description: "Implementation tasks for Workspace SSH"
---

# Tasks — Workspace SSH

**Input** — [Specification](spec.md), [implementation plan](plan.md), [requirements checklist](checklists/requirements.md), [research](research.md), [data model](data-model.md), [SSH contract](contracts/ssh.md), [UI and grant contract](contracts/ui-and-grants.md), and [validation quickstart](quickstart.md).

Write and run the commented unit tests at the start of each coding task. Confirm the relevant regression fails before changing production code, then rerun the focused suite. Check off each task as soon as its work and verification pass. Tasks marked `[P]` can run in separate subagents alongside the specified peer after their prerequisites pass. Follow [repository instructions](../../AGENTS.md) and keep paths relative to the repository root.

## Phase 1 — Setup

- [X] T001 Prepare the running-workspace proof with a disposable primary devcontainer, known developer identity and project directory, an isolated test harness or authorized route, native OpenSSH on macOS, and a clean VS Code Remote–SSH profile. Use the versions and fixtures described in `specs/006-workspace-ssh/quickstart.md`. Identify the MVP Mac, local build and ad hoc signing tools, and `shiftbox-demo` access for the integrated checks.

## Phase 2 — Foundation

- [X] T002 Add focused tests in `internal/proxy/tunnel_test.go` and `internal/guest/tunnel_test.go` for SSH capability negotiation, fixed service and current workspace/generation binding, and existing services with an older peer. Add the negotiated SSH service to `internal/proxy/{services,tunnel}.go`, `internal/app/guest_tunnel.go`, and `internal/guest/{agent_tunnel,tunnel}.go` on the existing authenticated VM tunnel. Reject arbitrary targets and keep the SSH capability disabled until the guest can serve it. **FR-004, FR-012**

## Phase 3 — US2 Connect from a terminal or desktop editor (P1)

**Independent verification** — On a running workspace through the proof harness, the SSH terminal uses the browser IDE's developer identity and project directory. A clean VS Code Remote–SSH client completes initial transfer and edits that project. An unexpected host key or workspace mismatch stops the connection.

- [X] T003 [P] [US2] Add developer-context and channel tests in `internal/guest/ssh_test.go`. Serve SSH in the existing guest binary through `internal/guest/{ssh,tunnel,devcontainer,workspace_command}.go`. Reuse the browser IDE's primary container, developer identity and project directory for PTY, exec, editor loopback forwarding and required setup transfer. Keep the host private key in VM-persistent agent state and bind the public key to that stream's workspace identity. Use the existing guest binary for any required in-container bridge. Keep the SSH service private to the tunnel. **FR-004–FR-006, FR-012**
- [X] T004 [P] [US2] Add pinning and relay tests in `cmd/shiftbox-helper/ssh_proxy_test.go`, then implement the thin OpenSSH proxy command in `cmd/shiftbox-helper/ssh_proxy.go` against the SSH contract's readiness frame. Pin the authenticated controller's host public key by controller origin and immutable workspace ID. Require the SSH server to present that pin and reject changed keys or mismatched workspace responses with a clear error without rewriting the pin. Keep SSH bytes on stdout and diagnostics on stderr. Use a fixture for the controller stream. **FR-004, FR-012**
- [X] T005 [US2] Prove the running-workspace path through an authorized route or isolated harness before exposing the production route. Use native OpenSSH on macOS to verify shell identity and directory and a clean VS Code Remote–SSH profile with forced local-download transfer to verify setup, editor forwarding and a file edit shared with the browser IDE. Verify strict host-key rejection with the real client. Use `specs/006-workspace-ssh/quickstart.md` for the client steps. **SC-002, SC-006**

## Phase 4 — US4 Enforce private workspace access (P1)

**Independent verification** — Exercise discovery and connection as the owner, an unrelated member, and an administrator before and after an exact grant. Use a separately provisioned grant and reload the existing panel to confirm it is listed. Verify the SSH connection audit, revoke while connected, and confirm the stream closes and a saved alias cannot reconnect.

- [X] T006 [US4] Add credential-scope, expiry and revocation tests in `internal/auth/helper_session_test.go` and migration coverage in `tests/contract/database_migrations_test.go`. Add paired migrations under `migrations/postgres/` and `migrations/sqlite/` and credential helpers in `internal/auth/helper_session.go`. Persist only the verifier digest, approving session ID, creation and expiry timestamps. Restrict use to helper routes and end validity at the earliest applicable deadline or revocation. **FR-002, FR-009**
- [X] T007 [US4] Add policy, audit and disconnect tests in `internal/web/workspace_ssh_test.go` and `tests/contract/openapi_routes_test.go`. Wire discovery and authorized SSH upgrade on management HTTPS in `internal/web/{workspace_ssh,routes}.go` and `internal/app/management.go`. Use current browser IDE connection policy, the exact administrator grant and durable admission audit. Bind caller, session, workspace and generation to the current tunnel. Send only that workspace ID and public key before SSH bytes. Use `internal/proxy/registry.go` to close matching streams on expiry, logout, offboarding or grant revocation. Cover denial for expired credentials, ungranted administrators and different-workspace grants. Keep the route declarations in `specs/001-shiftbox-mvp/contracts/openapi.yaml` and generated `ui/lib/api-types.ts` in sync. **FR-004, FR-008, FR-009, FR-012**
- [X] T008 [P] [US4] Verify that a separately provisioned administrator grant appears in the existing grant list. Verify that the existing permission protects revocation, that revoking an exact grant closes the matching live SSH connection, and that it denies reconnect and leaves unrelated connections open. Grant provisioning is a separate prerequisite. **FR-011, SC-004**

## Phase 5 — US3 Start a stopped workspace from SSH (P1)

**Independent verification** — Deliberately stop an eligible workspace. Two authorized attempts produce one Start operation and both continue to the primary devcontainer. An attempt missing connection or start authority leaves the workspace stopped.

- [X] T009 [P] [US3] Add tests in `internal/web/workspace_ssh_test.go` and `tests/contract/workspace_power_api_test.go` for denial without start, concurrent join, failed startup and authority loss while waiting. In `internal/web/workspace_ssh.go` use the existing `workspace.start` admission and journal in `internal/workspace/lifecycle.go`. Use the same connect and start permissions for stopped-workspace discovery, start admission and joining an in-flight start. Share the operation for the current generation, surface its normal progress and retry outcomes, and reauthorize the ready primary devcontainer before relaying. Cancel a revoked or disconnected wait without changing the durable operation. Use the existing Workspaces Stepper for startup progress. **FR-007–FR-009, SC-003**

## Phase 6 — US1 Set up and discover workspaces (P1)

**Independent verification** — Confirm the upper-right personal settings menu offers the intended internal app. Attempt the per-user download and first launch with normal macOS security settings and record the exact outcome. If policy blocks that path, record it as blocked and verify the following separately with the locally built copy. Complete browser sign-in, verify the Keychain credential, and sign out of and back into macOS. Confirm automatic startup and sync of eligible running and stopped workspaces, stable aliases, removal of ineligible aliases, and preservation of unrelated SSH entries.

- [X] T010 [P] [US1] Add pairing tests in `internal/web/helper_pairing_test.go` for replay, expiry, incorrect proof, callback mismatch and invalid browser session. Implement approval and exchange in `internal/web/helper_pairing.go` and mount them through `internal/web/routes.go` and `internal/app/management.go`. Use state, S256 proof challenge, the exact numeric loopback callback and approval through a normal browser session with existing origin and CSRF checks. Consume the code once and issue the scoped helper credential. Update `specs/001-shiftbox-mvp/contracts/openapi.yaml` and regenerate `ui/lib/api-types.ts`. **FR-001, FR-002**
- [X] T011 [P] [US1] Add commented credential-store and alias reconciliation tests in `cmd/shiftbox-helper/{session_test,ssh_config_test}.go`. Use the pairing and discovery contracts to implement browser sign-in, macOS Keychain storage, automatic startup at macOS sign-in, expiry handling and background synchronization in `cmd/shiftbox-helper/{main,session,ssh_config}.go`. Write one readable host per eligible running or startable stopped workspace. Key alias allocation to the installation, account and immutable workspace ID, keep it through renames, remove ineligible entries and update only the helper-owned block atomically. Wire the strict verification settings from T004 into each managed host and retain pins across normal lifecycle changes. Test existing host collisions and config preservation. **FR-002, FR-003**
- [X] T012 [US1] Add per-user app packaging under `platform/helper/macos/` for the `cmd/shiftbox-helper` build. Build and ad hoc sign the macOS app locally, verify the signature, and stage the intended artifact for the private MVP controller download. Record the artifact identity for the downloaded-path check in `specs/006-workspace-ssh/quickstart.md`. **FR-001**
- [X] T013 [US1] Add commented menu and approval-flow tests in `ui/components/app-shell.test.tsx` and `ui/app/account/helper/helper.test.tsx`. Add **Install Local Helper** to `ui/components/app-shell.tsx` and the internal artifact download and browser approval flow under `ui/app/account/helper/`. Verify the menu offers the intended internal artifact. Show the installation and account being paired, use branded Mantine controls, Stepper and state chips as specified, and verify keyboard access and focus. **FR-001, SC-001**

## Phase 7 — Integrated verification

- [X] T014 Run the focused suites and [quickstart](quickstart.md) against an authorized `shiftbox-demo` rollout with the paired schema and compatible guest build. On the MVP Mac, attempt the controller download and first launch with normal security settings. Record any exact policy block and the local build's first launch separately. Verify browser sign-in, Keychain retrieval, login-item startup, discovery, strict pin reuse and rejection, interactive SSH, and clean VS Code setup transfer on the app that launches. On the demo verify one start for concurrent requests, owner/admin/grant boundaries, visibility of the separately provisioned grant after reload, audit and live disconnect. Record client versions, demo revision and results in `specs/006-workspace-ssh/quickstart.md` and clean up disposable resources. **SC-001–SC-004, SC-006**

## Dependencies and execution order

| Tasks | Prerequisites and parallel work |
| --- | --- |
| T001 → T002 | Establish the isolated proof fixture first. T012 and T014 use local build and ad hoc signing tools and the MVP Mac. |
| T003 and T004 → T005 | After T002, use separate subagents for guest execution and the workstation proxy. Join them for the first native-client proof. |
| T006 → T007 | Start after T005. Wire scoped session storage, then the production access boundary. |
| T008 and T009 | After T007, use separate subagents for the existing grant list and revoke verification and SSH startup. T008 covers the management grant endpoints. T009 owns the SSH handler and lifecycle integration. |
| T010 and T011 → T012 → T013 | After T008 and T009, use separate subagents for the controller pairing flow and workstation sync against contract fixtures. Pairing and helper work must pass before packaging and menu integration. |
| T014 | After T005 and T008–T013. Run the four story checks on macOS and the demo. |

## Implementation strategy

Use T005 as the first reviewable proof. Then integrate lifecycle and administrator authority before enabling the private workstation workflow. The MVP includes all four stories and the targeted checks in T014.
