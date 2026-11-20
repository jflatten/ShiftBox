# Implementation Plan — Workspace SSH

**Branch** `feature/mvp` · **Date** November 19, 2026 · **Input** [Workspace SSH specification](spec.md)

## Summary

Add a macOS helper that maintains standard SSH host entries and connects through one dedicated route on the existing controller HTTPS origin. The controller authorizes individual connections, admits an ordinary workspace start when required, and routes to the primary devcontainer through the current VM tunnel. Use separately provisioned administrator connection grants with the existing list and revoke behavior.

The [specification](spec.md) limits the locally built, ad hoc–signed helper to the private Mac MVP and requires strict per-workspace host-key verification. Proposed route, local configuration, and pairing details are in [research](research.md). The user will assess shared-tunnel performance during human review, with a separate specification for any follow-up.

## Technical Context

| Area | Existing system and proposed change |
| --- | --- |
| Language and dependencies | Repository-pinned Go 1.27.4, `x/crypto/ssh`, Yamux, and existing HTTPS/WebSocket support. The static TypeScript/Next.js UI uses Mantine and the shared branding. Add native platform integration only for the helper's Keychain storage, sign-in startup, and packaging. |
| Storage | Existing workspace, grant, audit, and operation records. Add a scoped helper credential binding in both PostgreSQL and SQLite. The helper stores its credential in macOS Keychain. Store each public host pin locally under the controller origin and immutable workspace ID. |
| Target platforms | A macOS workstation with native OpenSSH and standard VS Code Remote–SSH. The controller and guest use their existing deployment targets. |
| Helper build and validation | Use the MVP Mac and local build and ad hoc signing tools. Stage the internal per-user app for the controller download. Verify the intended artifact and record the downloaded first-launch outcome under normal macOS security settings. Use a locally built copy for functional validation if Gatekeeper or workstation policy blocks the download or launch. |
| Testing | Test-first Go and focused UI unit coverage. Validate the supported clients on macOS, with integrated lifecycle, permissions, and host identity checks on `shiftbox-demo`. |
| Scope | Interactive SSH, VS Code Remote–SSH, helper synchronization, and enforcement of existing administrator grants. Standalone SFTP/SCP workflows are deferred. |

## Constitution Check

Reviewed before research and again after contracts and data design.

| Principle | Design check |
| --- | --- |
| I. Simplicity | Reuse the controller, tunnel, Dev Container CLI context, lifecycle journal, and grant UI. The requested helper is a per-user executable. Any additional SSH package dependency needs a demonstrated editor or shell requirement. |
| II. Ownership and trust | The controller owns authorization, admission and revocation. The agent serves a fixed current-generation devcontainer target. Browser management credentials stay within the controller/browser boundary. The helper stores only its scoped credential in macOS Keychain. |
| III. Durable work | SSH starts follow the existing admission, capacity, reconciliation, and trust recovery rules. Alias and host-key identity survive normal stops and renames, and project data retain existing lifecycle semantics. |
| IV. Test first | Write the focused tests in the sequence below before implementing each slice. Each test has a behavior comment. Use real clients and the shared demo to verify interoperability and grant persistence. |
| V. Visible workflows | Reuse the upper-right user menu and workspace grant panel. Use the existing Mantine Stepper for workspace startup and a Stepper for observable setup progress, chips for states, and project branding and Carbon conventions. Check keyboard access and focus. Mantine MCP was consulted for the menu, Stepper, and chips. |

Post-design review found the proposed boundaries consistent with the constitution. Platform compatibility and security still require the [MVP validation checks](quickstart.md).

## Project Structure

### Documentation (this feature)

- [Specification](spec.md) and [requirements checklist](checklists/requirements.md)
- [Research](research.md) and [data model](data-model.md)
- [SSH and helper contract](contracts/ssh.md) and [UI and grant contract](contracts/ui-and-grants.md)
- [Validation quickstart](quickstart.md)

### Source Code (repository root)

| Location | Intended responsibility |
| --- | --- |
| `internal/auth`, `internal/web`, `internal/app` | Browser pairing, scoped helper authority, alias discovery, authorized upgrade, and exact-grant enforcement. Mount the route only on management HTTPS. |
| `internal/workspace`, `internal/operation`, `internal/proxy` | Reuse start admission and readiness, audit-bound relay registration and cancellation, and negotiated SSH tunnel transport. |
| `internal/guest`, `cmd/shiftbox-agent` | Serve SSH in the existing signed agent build and reuse the browser IDE's primary container execution context. |
| `cmd/shiftbox-helper`, `platform/helper/macos/` (proposed) | Workstation entry point for browser sign-in, synchronization, and the OpenSSH proxy command. Build and ad hoc sign the per-user app locally for private distribution. |
| `ui/components/app-shell.tsx`, `ui/features/workspaces/connection-grants.tsx` | Discoverable installer and pairing flow, with the existing panel for listing and revoking grants. |
| `migrations/postgres`, `migrations/sqlite` | Matching migration for a helper verifier linked to the approving session. |

**Structure decision** — Use existing controller and agent packages. Add a workstation entry point for native lifecycle and OpenSSH integration.

## Implementation Sequence

1. **Prove the running-workspace path.** Test stream type, workspace/generation binding, primary container selection, and rejection of a key or authorization for a different workspace. Use a native OpenSSH client and a thin helper relay to prove strict host verification, developer shell identity and working directory, and a clean VS Code setup including its local transfer and forwarding. Use an isolated test harness until controller admission is wired in the next slice.
2. **Wire current access and SSH-triggered start.** Test the shared discovery/admission policy, exact admin grant and audit, no start on denial, one durable start for concurrent requests, and authority loss while waiting or connected. Reuse existing launch and start progress shown in Workspaces. Verify pending and live relays close at authorization loss.
3. **Verify separately provisioned grants.** Confirm an exact grant is listed after reload. Verify a granted SSH connection is audited and the existing revoke action disconnects matching SSH access.
4. **Ship the workstation flow.** Test single-use browser pairing, credential scope, alias stability/removal, and preservation of existing SSH entries. Stage the locally built, ad hoc–signed per-user macOS app through **Install Local Helper** in the private deployment.
5. **Run the integrated checks.** Use [quickstart](quickstart.md) from the MVP Mac against the shared demo. Attempt the per-user download and first launch with normal macOS protections enabled. Record a policy block and any local-build fallback as separate outcomes. On the running app, verify browser sign-in, Keychain retrieval, login-item startup, and alias sync along with strict pin reuse and rejection, interactive SSH, and clean VS Code setup transfer. Reuse the demo runs for the access and workspace startup checks. Clean up disposable data and grants.

## Complexity Tracking

No constitution exceptions identified. This plan ends at Phase 1 design.
