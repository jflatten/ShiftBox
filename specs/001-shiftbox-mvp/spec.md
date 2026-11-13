# Feature Specification: ShiftBox MVP

**Feature Branch**: `[001-shiftbox-mvp]`

**Created**: 2026-11-13

**Status**: Draft

**Input**: User description: "Create a MVP for the ShiftBox product. Review the architecture documentation to ground the specification."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Launch and Use a Development Workspace (Priority: P1)

An administrator completes protected first-run setup, and a developer launches an owner-private workspace from an approved project template. ShiftBox shows each setup and launch stage as it happens. When the environment is ready, the developer opens the browser IDE and works in the environment defined by the repository.

**Why this priority**: A reliably usable development workspace is the core product value. This journey proves that an operator can establish the service and a developer can move from a repository definition to productive work.

**Independent Test**: Starting from a new supported installation, complete setup, create one template, launch one workspace, open the IDE, edit a file, stop and restart the workspace, rebuild its development container, and verify that retained work remains available.

**Acceptance Scenarios**:

1. **Given** a new installation available only through its protected local setup path, **When** the operator supplies the first administrator identity, changes the temporary password, selects a supported data profile, and provides valid service names and certificates, **Then** ShiftBox activates normal access and requires a fresh sign-in.
2. **Given** an authenticated member with launch permission and an approved template, **When** the member selects a permitted resource size and launches a workspace, **Then** ShiftBox reserves capacity, creates one isolated workspace, displays real-time ordered progress, and opens the IDE only after the development environment is ready.
3. **Given** a workspace with edited project files, developer-home settings, and declared persistent application data, **When** the owner reboots, stops and starts, or rebuilds the development container, **Then** all designated retained data remains available.
4. **Given** a workspace whose setup fails because of project configuration or required lifecycle work, **When** the owner opens repair mode, **Then** ShiftBox provides a trusted repair environment that preserves files and diagnostics without running the failing project automation.
5. **Given** an operation whose infrastructure outcome is uncertain, **When** ShiftBox resumes reconciliation, **Then** it determines actual workspace state before retrying and does not create a duplicate workspace or release capacity prematurely.

---

### User Story 2 - Share Reusable Projects Safely (Priority: P2)

An administrator manages organization membership and reusable project templates. Template owners share launch access with selected members, and each launch creates an independent private workspace using the launching member's identity, Git authority, and effective secrets.

**Why this priority**: Multi-user reuse turns isolated workspace provisioning into an organization product while retaining the confirmed ownership and security boundaries.

**Independent Test**: In a configured installation, create two members, a shared template, secrets at all three supported scopes, and a private repository authorization. Launch as the second member and verify template access, owner-specific Git access, secret precedence, workspace privacy, revocation, and audit history.

**Acceptance Scenarios**:

1. **Given** a template owner has shared launch permission with a member, **When** that member launches the template, **Then** ShiftBox creates an owner-private workspace and records the template revision, repository configuration, resolved resources, and secret references used for launch.
2. **Given** the same secret name exists at user, project-template, and organization scopes, **When** the workspace owner retrieves it, **Then** the user value wins, followed by the project-template value and then the organization value; an explicit empty value overrides lower scopes.
3. **Given** an administrator explicitly connects to another owner's workspace under audited connection permission, **When** the workspace retrieves a personal secret or Git credential, **Then** it continues to use the recorded workspace owner's authority rather than the administrator's.
4. **Given** a project secret is shared through launch access, **When** the template owner grants that access, **Then** ShiftBox warns that authorized code in launched workspaces can retrieve the project secret.
5. **Given** template access is revoked or the template is archived, **When** the affected member attempts a new launch or project-secret retrieval, **Then** ShiftBox denies it while preserving existing workspace files and captured provenance according to the confirmed archival rules.
6. **Given** a user's Git authorization is valid for a covered private repository, **When** the user's workspace performs supported HTTPS Git operations, **Then** those operations use the current owner-authorized access token and continue across controller restarts and supported token renewal.

---

### User Story 3 - Operate Workspaces Without Losing Control (Priority: P3)

An administrator monitors capacity, workspace state, maintenance, access, certificates, and retention from one management experience. Developers can understand what ShiftBox is doing, what failed, and whether an action may destroy their work.

**Why this priority**: A self-hosted workspace service must remain understandable and recoverable when providers, guests, credentials, or user projects fail.

**Independent Test**: Run lifecycle, capacity, access-revocation, inactivity, certificate-expiry, and maintenance scenarios against existing workspaces, including lost responses and hung work, and verify visible progress, safe authority checks, retained reservations, and actionable recovery states.

**Acceptance Scenarios**:

1. **Given** a requested start would exceed configured capacity, **When** a user starts a workspace, **Then** ShiftBox rejects the request promptly with the limiting resource identified and does not queue, evict, or resize another workspace.
2. **Given** an administrator disables a member, **When** the change is committed, **Then** ShiftBox closes the member's management, IDE, and preview access, requests provider-enforced stops of the member's workspaces, preserves retained files, and reports any stop failure.
3. **Given** automatic deletion is explicitly configured and a workspace exceeds its idle deadline, **When** final activity, authority, outage-grace, and state checks still permit deletion, **Then** ShiftBox warns of irreversible data loss, attempts graceful shutdown, may escalate after the configured timeout, and retains reservations until deletion is verified.
4. **Given** a maintenance deadline has arrived, **When** a workspace reaches a safe activation boundary, **Then** ShiftBox updates and verifies it, returns a previously stopped workspace to stopped, and shows progress; unsafe or failed work remains visible for administrator action and is not force-killed by maintenance authority.
5. **Given** a workspace has been stopped long enough for its internal identity to expire, **When** an authorized user starts it, **Then** ShiftBox verifies the known provider instance and current eligibility before issuing one-use recovery authority; revoked, deleted, cloned, or mismatched identities remain denied.
6. **Given** a browser certificate has 30 days or less remaining or is invalid, **When** an administrator views management notices, **Then** ShiftBox displays a persistent actionable warning until a valid replacement is active and never falls back to unprotected normal access.

---

### User Story 4 - Back Up and Recover Management State (Priority: P4)

An administrator creates an encrypted management backup and can restore the same logical installation after a controller-state failure. Recovery makes restored authority safe for review before normal management and workspace access resume.

**Why this priority**: The MVP owns identity, authorization, secrets, workspace intent, and trust material. Operators need a supported recovery path for that state without implying that workspace disks are backed up.

**Independent Test**: Create a backup while workspaces are running, restore it to an empty supported target on the original host, exercise wrong-password, truncated-archive, missing-key, and stale-session cases, and verify that management state recovers while workspace disks remain outside the archive.

**Acceptance Scenarios**:

1. **Given** an authenticated administrator provides a backup password, **When** a management backup is requested, **Then** ShiftBox briefly coordinates management and identity writes, exports a consistent independently encrypted archive, resumes normal writes, and allows workspace computation to continue.
2. **Given** a valid archive and empty replacement management-data target on the original host, **When** an administrator restores it, **Then** ShiftBox validates the archive, password, keys, compatibility, and target ownership before activation and enters administrator-reviewed recovery mode.
3. **Given** a restored installation contains old browser sessions, enrollment grants, OAuth state, or overdue deletion work, **When** recovery mode begins, **Then** those records do not automatically regain authority and workspace access and destructive mutations remain gated until review and provider reconciliation complete.
4. **Given** an archive has the wrong password, is truncated, is incompatible, or lacks a required matching key, **When** restore validation runs, **Then** ShiftBox fails visibly before activation and leaves the installation recoverable.

### Edge Cases

- The infrastructure provider reports a timeout after creating, starting, stopping, or deleting a machine; ShiftBox keeps the operation identity and reservations until authoritative reconciliation establishes the result.
- The controller or management database becomes unavailable while a workspace is running; existing computation may continue, while unsafe mutations and new protected reads fail clearly without falling back to stale state.
- The host sleeps, logs out, or becomes unavailable; elapsed inactivity still counts, and reconciliation plus a visible recovery grace occurs before any overdue retention deletion.
- The development environment reaches network availability but has not reached its declared connection-ready stage; ShiftBox continues to show setup progress and does not declare the IDE ready.
- A workspace is stopped, unreachable, or on the previous supported guest release during a controller update; it remains part of the compatibility gate until updated, repaired, or deliberately retired.
- A secret override is deleted; ShiftBox shows that the next authorized lower-scope value will become effective before confirming deletion.
- A secret or access token was copied into a workspace before revocation; ShiftBox prevents future delivery but does not claim to retract copied material.
- Storage pressure prevents new allocations; ShiftBox alerts administrators and preserves existing workspace files, identity state, and keys rather than deleting them.
- An untrusted preview attempts to use management credentials or access another workspace; ShiftBox denies the request and preserves browser-origin isolation.
- The last administrator loses access; the protected local recovery path restores an existing administrator, preserves installation data, revokes affected sessions, and records the event without exposing a public recovery endpoint.

## Requirements *(mandatory)*

### Functional Requirements

#### Installation and Identity

- **FR-001**: ShiftBox MUST support one self-hosted organization with one active controller and an administrator-managed first-run setup.
- **FR-002**: ShiftBox MUST restrict first-run setup to a protected local path until a valid browser certificate, service names, first administrator, and management-data profile are activated.
- **FR-003**: ShiftBox MUST generate a unique temporary password for the first administrator, require it to be changed before normal use, and never reset the chosen password during restart or restore.
- **FR-004**: ShiftBox MUST support administrator-created local accounts with member or administrator roles, temporary passwords, forced first-login changes, user password changes, and administrator-mediated recovery.
- **FR-005**: ShiftBox MUST support optional external organizational sign-in where newly authenticated people require administrator membership approval before receiving application access.
- **FR-006**: ShiftBox MUST bind external identities by stable provider identity rather than automatically merging accounts by email address.
- **FR-007**: ShiftBox MUST provide resource/action authorization beneath the initial member and administrator roles, including distinct permissions for workspace lifecycle and connection.
- **FR-008**: ShiftBox MUST revoke established browser, management, IDE, and preview access when membership or applicable permission is disabled.
- **FR-009**: ShiftBox MUST record security-relevant identity, permission, administrator connection, recovery, and lifecycle actions without recording secret values.

#### Workspace Launch and Use

- **FR-010**: ShiftBox MUST let authorized users launch one isolated virtual-machine workspace with one primary development container from an approved template.
- **FR-011**: ShiftBox MUST support repository-defined development environments using a prebuilt image or build definition, declared features, lifecycle hooks, and a declared connection-ready stage.
- **FR-012**: ShiftBox MUST make the browser IDE and private previews available only through authenticated, authorized access specific to the workspace owner or an explicitly permitted and audited administrator.
- **FR-013**: ShiftBox MUST show setup, launch, rebuild, repair, start, stop, delete, recovery, backup, restore, and maintenance workflows as real-time ordered steps with current state and actionable failure information.
- **FR-014**: ShiftBox MUST preserve repository working files, the actual developer home, and explicitly designated persistent application data across reboot, stop/start, and development-container rebuild.
- **FR-015**: ShiftBox MUST warn that destroying a workspace permanently deletes its VM-resident data and that management backups do not contain workspace disks.
- **FR-016**: ShiftBox MUST offer an opt-in trusted repair environment for project configuration, build, required-hook, or IDE failures when the workspace machine and guest service remain healthy.
- **FR-017**: Repair mode MUST preserve files and diagnostics and MUST NOT run failed repository hooks, tasks, extensions, or other project automation merely by opening repair access.
- **FR-018**: ShiftBox MUST keep desired workspace state separate from observed provider and guest state and MUST expose stopped, deleted, unreachable, recovering, and failed conditions distinctly.

#### Templates, Capacity, and Networking

- **FR-019**: ShiftBox MUST let administrators define named resource sizes and installation capacity budgets and MUST pin each workspace's resolved CPU, memory, and disk allocation at creation.
- **FR-020**: ShiftBox MUST reserve CPU and memory from accepted start until provider-confirmed stop or deletion and MUST account for disk until provider-confirmed deletion.
- **FR-021**: ShiftBox MUST reject over-capacity starts with an actionable reason and MUST NOT implicitly queue, evict, or resize workspaces.
- **FR-022**: ShiftBox MUST let owners and administrators create and edit project templates containing repository configuration, launch configuration, resource size, and secret references.
- **FR-023**: ShiftBox MUST let a template owner share launch permission with selected members while keeping every resulting workspace independently owned and private.
- **FR-024**: ShiftBox MUST snapshot the template revision, repository configuration, resolved resources, owner, platform selection, and secret references for each launch while evaluating authorization and secret values from current state.
- **FR-025**: ShiftBox MUST prevent new launches from archived templates, preserve existing workspaces and captured provenance, and restrict archived-template management to the documented administrator view.
- **FR-026**: ShiftBox MUST offer only network policies the active provider can enforce and MUST explain when a requested restriction is unavailable.
- **FR-027**: ShiftBox MUST apply supported network-policy changes only while the workspace is stopped and MUST keep it stopped if verification fails.

#### Secrets and Git Authority

- **FR-028**: ShiftBox MUST support exactly three application-secret scopes with effective precedence User, then Project Template, then Organization, and MUST NOT create a workspace secret scope.
- **FR-029**: ShiftBox MUST let an explicit empty value override lower secret scopes and MUST warn before deleting an override reveals a lower value.
- **FR-030**: ShiftBox MUST derive secret access from the authenticated workspace identity, recorded owner, and project context rather than guest-supplied identity claims.
- **FR-031**: Any code in an authorized workspace MUST be able to retrieve any effective secret available to that workspace context, and ShiftBox MUST disclose this exposure boundary when sharing project-secret access.
- **FR-032**: ShiftBox MUST never redisplay stored secret values through management interfaces and MUST exclude secret values from audit history.
- **FR-033**: ShiftBox MUST protect stored application secrets and persisted external-access credentials so management-data access alone does not reveal their plaintext values.
- **FR-034**: ShiftBox MUST support owner-authorized HTTPS Git operations for covered GitHub.com repositories and MUST keep organization sign-in separate from Git authorization.
- **FR-035**: ShiftBox MUST persist and renew the owner's authorized Git access where supported, coordinate concurrent renewal, expose only the current access token to the owner's workspace, and require reconnection after revocation or permanent renewal failure.
- **FR-036**: ShiftBox MUST NOT expose application signing credentials, refresh credentials, controller infrastructure credentials, or installation root keys to workspaces as application secrets.

#### Reconciliation, Trust, and Lifecycle Safety

- **FR-037**: ShiftBox MUST assign durable operation identities and workspace generations so repeated requests and uncertain outcomes are reconciled without blindly duplicating side effects.
- **FR-038**: ShiftBox MUST retain reservations and unresolved operation state until the provider confirms the relevant stop or removal.
- **FR-039**: ShiftBox MUST authenticate each registered workspace independently, replace connected workspace credentials before expiry, and deny expired or revoked credentials.
- **FR-040**: ShiftBox MUST recover an eligible known workspace after a long stop only after verifying provider identity, ownership, registration eligibility, and current authorization and issuing fresh single-use enrollment authority.
- **FR-041**: ShiftBox MUST deny automatic recovery for revoked, deleted, unknown, cloned, or mismatched workspace identities.
- **FR-042**: ShiftBox MUST keep browser-service certificates and internal workspace trust on independent lifecycles, warn administrators 30 days before browser-certificate expiry, and provide no unprotected normal-access fallback.
- **FR-043**: ShiftBox MUST support configurable inactivity monitoring based initially on browser IDE connection presence and guest CPU activity, where any selected active signal counts as activity.
- **FR-044**: Automatic workspace deletion MUST remain disabled until an administrator configures it, and an owner MAY choose a shorter permitted retention period.
- **FR-045**: Before retention deletion commits, ShiftBox MUST recheck activity, authorization, policy, desired state, and outage grace; after commitment it MUST warn of data loss, attempt graceful shutdown, and MAY use provider-enforced stop and deletion after the configured timeout.
- **FR-046**: ShiftBox MUST support scheduled guest maintenance with visible warnings, deadlines, progress, capacity checks, safe activation boundaries, verification, and restoration of previously stopped workspaces to stopped state.
- **FR-047**: Maintenance MUST NOT force-kill unsafe work, bypass genuine update failures, or release the compatibility gate for unresolved retained workspaces.

#### Administration, Backup, and Recovery

- **FR-048**: ShiftBox MUST support demo/very-small and production management-data profiles while preserving separate ownership boundaries for application and identity data.
- **FR-049**: ShiftBox MUST provide manually initiated, independently password-encrypted management backups containing owned application and identity data, configuration, protected-value keys, trust material, and compatibility metadata while excluding workspace disks and unrelated external data.
- **FR-050**: ShiftBox MUST create backups from a coordinated consistent checkpoint, briefly pause affected management writes, and allow workspace computation to continue.
- **FR-051**: ShiftBox MUST validate archive integrity, password, keys, version compatibility, target ownership, and prerequisites before activating a restore.
- **FR-052**: Restore MUST target the same logical installation on its original host and MAY use an empty replacement production-data target without overwriting unrelated data or an unmatched operator-supplied key.
- **FR-053**: Restored installations MUST enter administrator-reviewed recovery mode in which restored sessions, enrollment grants, stale authorization, workspace access, and destructive mutations remain gated until identity review and provider reconciliation complete.
- **FR-054**: ShiftBox MUST support protected local recovery of an existing last administrator without exposing a public recovery endpoint or silently creating a new administrator.
- **FR-055**: ShiftBox MUST show controller, provider, data-store, encryption-key, certificate, disk-pressure, update, cleanup, and recovery failures as actionable persistent conditions rather than claiming success or falling back to stale authority.
- **FR-056**: Administrators MUST be able to publish systemwide notices, and users MUST be able to read, dismiss, and review notice history without dismissal canceling required maintenance.
- **FR-057**: ShiftBox MUST provide configurable audit and dismissed-notice retention without removing current authorization, unresolved operations, active maintenance, or workspace provenance.
- **FR-058**: The management experience MUST use the approved ShiftBox identity and provide keyboard-accessible controls and predictable focus behavior for all MVP workflows.
- **FR-059**: ShiftBox MUST let administrators import immutable, versioned platform artifacts with integrity, compatibility, owned-resource, and provenance information and select an approved default for new workspaces.
- **FR-060**: ShiftBox MUST support the current and immediately previous guest release, prevent a further controller advance while any retained workspace has not reached the current release, and keep failed updates visible until repaired, retried, or deliberately retired.
- **FR-061**: ShiftBox MUST isolate management, each workspace IDE, and each private preview on distinct browser origins; guest applications MUST NOT receive management credentials, and revoked access MUST terminate established browser and WebSocket sessions.
- **FR-062**: ShiftBox MUST require external organizational users to complete successful upstream authorization within an administrator-configurable positive interval, initially four hours, without treating a token refresh alone as renewed authorization.
- **FR-063**: ShiftBox MUST replace internal client and server keys and certificates every four hours with eight-hour validity, use a stable installation authority across ordinary replacements, and require fresh authentication of long-lived connections no later than the certificate used for them expires.
- **FR-064**: New secret and Git-credential reads MUST require the controller; ShiftBox MUST NOT maintain a persistent offline retrieval cache or claim to refresh values already copied or injected into running developer processes.
- **FR-065**: ShiftBox MUST support unrestricted outbound access and, only where enforceable, a no-network profile and destination/transport/port allowlists with explicit required controller, name-resolution, and approved repository exceptions.
- **FR-066**: Production setup MUST preserve only the initial administrator and setup state when activating the production data profile, require a fresh sign-in, retire bootstrap sessions, and never silently fall back to the bootstrap authority after activation.
- **FR-067**: Before an irreversible application or identity-data migration, ShiftBox MUST verify a completed management backup, compatibility prerequisites, key access, certificate validity, and the retained-workspace release gate.

### Key Entities

- **Installation**: One ShiftBox organization and active controller, including configured service names, data profile, trust material, capacity, policy, and operational status.
- **User**: A local or externally bound person with membership status, predefined role, resource/action permissions, sessions, and optional Git authorization.
- **Project Template**: A reusable, versioned launch definition containing repository configuration, environment configuration, resource selection, secret references, ownership, sharing grants, and archival state.
- **Workspace**: An owner-private development environment with desired and observed state, generation, provider identity, pinned resources, captured template provenance, retention state, and trust registration.
- **Operation**: A durable, uniquely identified workflow such as provision, start, rebuild, repair, stop, delete, recover, update, backup, or restore, with ordered steps and failure state.
- **Resource Size and Reservation**: Administrator-defined capacity values and per-workspace resource accounting retained until provider-confirmed release.
- **Secret**: A protected named value at user, project-template, or organization scope, including explicit empty overrides and authorization metadata.
- **Git Authorization**: The workspace owner's external repository grant and current renewable access state, separate from organization sign-in.
- **Platform Artifact**: An immutable administrator-approved workspace-machine release with identity, compatibility, integrity, and provenance information.
- **Notice and Audit Event**: User-visible operational communication and security-relevant history, each with configurable retention that does not replace active state.
- **Management Backup**: A versioned, independently encrypted archive of owned controller and identity state that explicitly excludes workspace disks.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In release validation, 100% of reference projects that meet the documented MVP compatibility scope can be launched from an approved template to a usable authenticated browser IDE without operator intervention after launch.
- **SC-002**: At least 90% of representative first-time developers can launch an authorized template, identify current progress, and open the ready workspace on their first attempt without administrator assistance.
- **SC-003**: In all required persistence tests, 100% of designated project files, developer-home content, and declared persistent application data survive reboot, stop/start, and development-container rebuild.
- **SC-004**: In all injected timeout, duplicate-request, and controller-restart scenarios, ShiftBox creates no duplicate workspace, performs no duplicate destructive effect, and releases no reservation before provider confirmation.
- **SC-005**: In all authorization tests, unauthorized users cannot access management actions, another owner's IDE or preview, another owner's personal secret context, or revoked project-secret access; established access closes after explicit revocation.
- **SC-006**: In all supported long-stop recovery tests, eligible known workspaces recover without changing retained workspace disks, while 100% of revoked, deleted, unknown, cloned, and replayed identities remain denied.
- **SC-007**: In every destructive user flow, the responsible user sees the irreversible workspace-data consequence before commitment and can distinguish stopping a workspace from deleting it.
- **SC-008**: In all backup and restore acceptance tests, a valid management archive restores owned state to the supported replacement target, while wrong passwords, corruption, missing keys, stale authority, and unrelated targets cannot become active.
- **SC-009**: Every long-running MVP workflow presents its current ordered stage, and every injected failure presents an actionable reason and next step without requiring users to inspect service logs.
- **SC-010**: All MVP management, setup, workspace, and recovery workflows pass keyboard navigation and focus-order review with no blocker preventing completion without a pointing device.
- **SC-011**: Before a supported MVP release is claimed, all functional and security validation gates V1 through V14 in the confirmed architecture have recorded passing evidence or an explicitly approved design revision.

## Assumptions

- The final architecture readback was confirmed by the user on November 13, 2026; this specification translates that design into MVP product requirements without changing its trust, data-loss, operator-burden, or scope decisions.
- The initial supported installation is a manually started service on a logged-in, awake Mac with the confirmed local VM provider available. Availability during host sleep, logout, or pre-login operation is outside the MVP contract.
- Each workspace uses the confirmed Ubuntu amd64 platform baseline and supports glibc-based amd64 project containers, with Debian and Ubuntu reference cases.
- Administrators provide private-network naming, valid browser certificates, an approved platform artifact, capacity profiles, and any required external identity, GitHub, or production data-service configuration.
- The MVP serves one organization and one active controller. High availability, shared multi-organization SaaS, remote provider workers, and arbitrary runtime plugins are outside scope.
- Workspace data is not backed up or migrated by ShiftBox. Developers remain responsible for pushing work they need to retain before workspace destruction.
- EC2, Linux/container-hosted production controllers, optional MFA, and application encryption-key rotation are phase II.
- Extra IDEs, public previews, collaborative workspace sharing, custom-role editing, template ownership transfer or permanent purge, platform-managed Compose projects, Alpine/musl project containers, populated-demo migration, and platform-OS replacement are deferred.
- Product-managed pull-request workflows, bundled developer command-line tools, managed LFS/submodule/fork workflows, SSH Git, GitHub Enterprise Server, and other Git providers are outside the MVP.
- Scaling and load targets are deferred until the product is fully implemented. MVP readiness depends on the confirmed functional and security gates rather than an unvalidated workspace-count claim.

## Dependencies

- Confirmed ShiftBox architecture, validation gates, and branding assets in this repository.
- A supported local virtualization provider and approved immutable platform artifact for workspace creation and recovery.
- A supported browser and private-network DNS/certificate configuration for management, IDE, and preview origins.
- Embedded identity capability and, when selected by the administrator, a compatible external organizational identity provider.
- GitHub.com application registration, repository installation coverage, and per-user authorization for private Git operations.
- A supported external production data service for production installations; the embedded profile remains limited to demo and very-small use.
