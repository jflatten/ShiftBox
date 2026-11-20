# Feature Specification — Workspace SSH

**Branch** `feature/mvp` · **Created** November 19, 2026 · **Status** Validated for planning

**Input** Confirmed Workspace SSH design interview and follow-up scope decisions

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Set up and discover workspaces (P1)

As a developer, I can install the local helper from ShiftBox and see eligible workspaces in my workstation's SSH host list.

**Why this priority** — Developers need a one-time setup that keeps their workspace list current.

**Independent test** — Try the per-user download and first launch from the upper-right personal settings menu on the MVP Mac. Record that outcome and verify sign-in and host discovery using the downloaded app if it launches, or the locally built copy if policy blocks the download or launch.

1. **Given** a signed-in team member on the MVP Mac, **when** they choose **Install Local Helper**, **then** the controller offers the intended internal macOS app for per-user installation. Attempt the download and first launch under normal macOS security settings. Record whether the downloaded app launches or the exact policy block.
2. **Given** a helper running on the MVP Mac, **when** the developer completes browser sign-in and signs out of and back into macOS, **then** the helper retrieves its scoped credential from Keychain, starts at sign-in, and resumes alias sync while the session is valid. If platform policy blocks the download or launch, verify this flow using a locally built, ad hoc–signed copy and record it separately.
3. **Given** a running workspace and a stopped workspace the user is allowed to connect to and start, **when** the helper syncs, **then** it lists both. A workspace that becomes ineligible is removed, and renaming an eligible workspace preserves its alias.

### User Story 2 — Connect from a terminal or desktop editor (P1)

As a developer, I can open an SSH terminal or use VS Code Remote–SSH in the development environment I use in the browser IDE.

**Why this priority** — The same project, files, and developer identity are available from the user's desktop.

**Independent test** — Open a terminal to a running workspace, then use a clean VS Code Remote–SSH client to connect to the same workspace and open the project.

1. **Given** an eligible running workspace, **when** the developer connects by SSH, **then** the shell opens as the browser IDE's developer identity in the primary devcontainer's project directory.
2. **Given** a clean VS Code Remote–SSH setup, **when** the developer selects the alias, **then** initial server setup can complete, including client-to-workspace transfer when required, and the editor can open and edit the project.
3. **Given** a workspace with a verified host-key pin, **when** it is renamed or stopped and started, **then** the original pin still verifies the workspace. An unexpected key change or a different workspace's identity fails verification with a clear message.

### User Story 3 — Start a stopped workspace from SSH (P1)

As a developer allowed to start my workspace, I can connect to it after it stops without opening the ShiftBox UI.

**Why this priority** — The saved alias works whether the workspace is already running or needs to start.

**Independent test** — Deliberately stop an eligible workspace and connect from the saved alias.

1. **Given** a manually or automatically stopped workspace that the caller may connect to and start, **when** the caller connects by SSH, **then** that attempt starts the workspace, waits for the primary devcontainer, and continues without a second SSH command.
2. **Given** two eligible attempts to the same stopped workspace, **when** they arrive together, **then** both wait for one start operation and open independently once ready and authorized.

### User Story 4 — Enforce private workspace access (P1)

As a workspace owner, I have the same access protections for SSH that I have for the browser IDE.

**Why this priority** — A saved alias and lifecycle permissions must confer no connection authority.

**Independent test** — Exercise the same workspace as its owner, an unrelated member, and an administrator before and after an exact administrator grant is provisioned separately, then revoke access during a session.

1. **Given** current ShiftBox membership and session validity, **when** different users try to connect, **then** the owner and the administrator with an exact active connection grant can connect. Other callers are denied. The administrator's successful connection is audited.
2. **Given** a separately provisioned administrator connection grant, **when** a caller with permission to manage connection grants reloads the existing panel, **then** they can see the active grant and revoke it.
3. **Given** an active connection or a cached alias, **when** the session expires or access is revoked through offboarding or grant revocation, **then** matching active connections close and subsequent attempts are denied.

### Edge Cases

- If lifecycle policy denies start or startup fails, the current SSH attempt ends with an actionable error. A later attempt follows the ordinary lifecycle retry rules.
- If an administrator can start a workspace but lacks its connection grant, SSH does not start it.
- If access changes while a workspace starts, the pending caller must pass authorization again before receiving a stream.
- If a user already has an SSH host with the preferred alias, ShiftBox assigns a distinct readable alias and preserves the user's entry.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001** The private macOS MVP MUST offer **Install Local Helper** from the upper-right personal settings menu for participating developers and team members. The action MUST serve the locally built, ad hoc–signed internal app for per-user installation. Once the app launches, the user signs in through the browser.
- **FR-002** The helper MUST store credentials in macOS Keychain, start automatically at macOS user sign-in, and sync in the background while its ShiftBox session is valid. When ShiftBox requires sign-in again, the helper MUST direct the user to the browser.
- **FR-003** The helper MUST maintain one human-readable entry per eligible workspace in standard workstation SSH configuration. It MUST include authorized running workspaces and authorized stopped workspaces the caller can start, remove ineligible entries on sync, preserve aliases across workspace renames, and honor existing user SSH configuration.
- **FR-004** ShiftBox MUST carry SSH over a dedicated authorized route on its existing HTTPS address and through the existing authenticated outbound VM tunnel. The unified ShiftBox guest binary MUST provide the SSH capability without requiring installation of another guest daemon, agent, or package for SSH.
- **FR-005** Interactive SSH MUST enter the current primary devcontainer under its configured developer identity and in its project directory. ShiftBox MUST confine this access to that devcontainer.
- **FR-006** Standard VS Code Remote–SSH MUST be able to connect to the same primary devcontainer, complete first-time server setup including its required transfer, and work on the project.
- **FR-007** An SSH attempt to an eligible stopped workspace MUST start it subject to existing lifecycle permission and policy, wait for the primary devcontainer, and continue the connection. Concurrent attempts MUST share the admitted start. A failed or denied start MUST report its outcome without bypassing ordinary retry rules.
- **FR-008** Discovery and connection MUST use current browser IDE connection policy. Owners can connect to their own eligible workspaces. An administrator connecting to another owner's workspace MUST have an explicit grant for that workspace and the connection MUST be audited. For a stopped workspace, the caller also needs separate start authority.
- **FR-009** ShiftBox MUST authorize each new connection and recheck authorization before admitting a caller who waited for startup. Existing session validity, expiry, and offboarding MUST apply. Session or grant revocation MUST close the corresponding active SSH connections.
- **FR-011** An administrator connection grant is provisioned separately. The existing **Administrator connection grants** panel MUST list active grants for authorized administrators after reload. The existing revoke action MUST terminate matching SSH access.
- **FR-012** The helper MUST obtain the intended workspace's host public key from the authenticated controller, pin it to that workspace, and require strict SSH host-key verification. The pin MUST survive ordinary stops, starts, and renames. If the key changes unexpectedly or the workspace identity does not match, the connection MUST stop with a clear verification error. Authorization and host-key verification MUST apply to the same requested workspace.

### Key Entities

- **Workstation session** — The current sign-in that permits the local helper to discover workspaces and request SSH connections.
- **Managed SSH entry** — A stable workstation alias for an eligible workspace in the user's SSH host list.
- **Workspace host-key pin** — The public key the helper uses to verify a specific workspace across ordinary stops, starts, and renames.
- **Workspace connection** — An SSH connection bound to a signed-in user, a workspace, its current primary devcontainer, and the exact grant for administrator access when required.
- **Administrator connection grant** — The existing explicit permission for one administrator and one workspace, shared by browser IDE and SSH and subject to audit and revocation.
- **Start operation** — The existing workspace lifecycle operation an authorized SSH caller can initiate or wait for.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001** On the MVP Mac, confirm the menu offers the intended internal artifact and attempt its download and first launch under normal macOS security settings. Record a successful downloaded launch or the exact Gatekeeper or workstation policy block. If blocked, record the local build's first launch and functional checks separately. The running app completes browser sign-in, stores its scoped credential in Keychain, and starts automatically after macOS sign-out and sign-in. Eligible aliases appear, a renamed workspace keeps its alias, an ineligible alias is removed, and the unrelated user entry is preserved.
- **SC-002** On macOS, an OpenSSH shell reports the expected developer identity and project directory. A clean VS Code Remote–SSH setup completes its initial transfer and edits a file visible in the browser IDE.
- **SC-003** Two simultaneous authorized SSH attempts after a deliberate stop generate one start and both connect to the ready devcontainer. A disallowed SSH start leaves the workspace stopped.
- **SC-004** The owner, unrelated member, administrator without a grant, and administrator with an exact grant produce the expected allow or deny result in both discovery and connection. An authorized administrator can see the separately provisioned grant in the existing panel after reload. The granted SSH connection produces an audit record for that workspace and exact grant. Revocation closes the live connection and a cached alias cannot reconnect.
- **SC-006** On macOS, the same workspace pin verifies the host after a rename and an ordinary stop/start. An unexpected key change or another workspace's key fails with a clear verification error.

## Assumptions

- Workstation availability is limited to developers and team members in the private macOS MVP. Apple Developer Program registration, Developer ID signing, and notarization are deferred to a later broader-distribution release. Windows helper support is deferred to a separate feature request.
- The workstation can reach and verify the installation's HTTPS address and has a supported OpenSSH client. Codex and Claude Code motivated standard SSH host discovery. Release validation covers interactive SSH and VS Code Remote–SSH.
- Standalone SFTP and SCP workflows are deferred. Transfer used by a supported developer shell or editor remains within that connection's developer permissions.
- SSH uses existing workspace lifecycle policy. Synchronization intervals and connection deadlines are engineering choices in the [plan](plan.md) and [research](research.md).
