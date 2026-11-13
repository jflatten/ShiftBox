# ShiftBox design decisions

Updated November 13, 2026, America/New_York. Responses recorded through Q155, plus the subsequent explicit Mantine component-library decision. The closing round is complete; the design document series is ready for review. Shared-understanding confirmation and implementation authorization remain outstanding.

## Status and authority

Canonical decision record: `/Users/jflatten/Workspace/ShiftBox/documentation/architecture/shiftbox-design-decisions.md`. Relocated at the user's request from `/Users/jflatten/Workspace/CloudBox/docs/vm-platform-design-decisions.md`; the original reference checkout remains otherwise unchanged by this move.

This is the working decision record for a **greenfield project** implementing a provider-backed VM CDE architecture. On November 13 the user explicitly clarified that the current codebase will not be migrated. Existing CloudBox code is research/reference material, not an implementation to adapt. The user has selected the greenfield ShiftBox project and requested this record at ShiftBox/documentation/architecture/shiftbox-design-decisions.md. This documentation relocation does not migrate the reference codebase.

**Product-level interview responses are complete through Q155. The user has authorized design synthesis, but has not confirmed the final readback or authorized implementation.** Updating this record and creating the design document series are authorized. It does not authorize modifying the runtime, creating a plugin fork, building images, changing UTM, migrating data, or replacing existing specifications and the constitution.

Responses through Q155 are recorded. Q148 accepts encrypted persistent GitHub access/refresh tokens with automatic renewal. Q149 accepts restricted loopback HTTPS bootstrap before validated uploaded browser TLS. Q150 accepts four-hour leaf/key rotation, eight-hour validity and a stable CA, with additional long-offline recovery research incorporated below. Q151 accepts bounded graceful shutdown followed by provider-enforced deletion under an authorized retention policy. Q152 requires an administrator-configurable external OIDC authorization interval; four hours is the researched initial engineering default, not a universal industry standard. Q153–Q154 settle operator updates, backup prerequisites, administration deferrals and storage pressure. Q155 authorizes synthesis and bounded engineering choices, with functional/security validation and final review still required.

Start with [the design readback](design-readback.md), which links the supporting documents and architecture diagrams. The [technology decision register](technology-decisions.md) consolidates selected dependencies, engineering proposals, ownership and outstanding version choices. The design series expresses the current effective contracts; historical recommendations below remain preserved for provenance. A later explicit outcome supersedes an earlier proposal. Proposed numerical engineering defaults are labeled in [validation and engineering defaults](validation-and-engineering-defaults.md). No runtime implementation has been started.

**The greenfield product is now named ShiftBox**, explicitly confirmed by the user. CloudBox denotes the reference project and historical discussion; the legacy installation remains protected. Historical questions, research and source paths may retain the former name for provenance. Existing examples of the `cloudbox` CLI are historical: final ShiftBox command naming remains to be specified rather than silently claiming an implemented command.

## Purpose and governing choices

- Engineering and operational simplicity are the primary optimization goals. Security, data preservation, functionality, and performance contracts must be explicit.
- ShiftBox owns workspace lifecycle and reconciliation. The VM provider owns machine infrastructure, including its disks and networking, through a platform provisioning adapter.
- A single binary is a packaging goal, not a prohibition on supporting services or additional execution roles.
- Implement the greenfield controller and guest services in **Go**, producing appropriate native macOS controller and Linux amd64 guest builds. The UI uses React, Next.js and TypeScript with **Mantine as the component library**, statically exported and embedded in Go. Q106 selects Go as the HTTPS server with live Go APIs; no Next/Node server runtime is required. Embedded Dex is selected for identity.
- The first deployment serves one organization with multiple users. A shared multi-organization SaaS is outside the initial scope.
- The established first deployment runs the ShiftBox binary natively beside **UTM** on Mac; phase two adds **AWS EC2**. Q114 now explicitly requires portable controller/key-storage design, including other hosts and containers. Q116 keeps native Mac/UTM as the initial supported deployment and introduces Linux/container production with the EC2 phase; portability is required in the design now. No remote UTM adapter or Windows deployment is implicitly added. Do not apply Mac-only key custody to the portable controller.
- Each workspace receives its own VM. Phase one supports one primary devcontainer from an image or Dockerfile, including Features and lifecycle hooks. Platform-managed Compose project definitions are deferred.
- Phase one uses **Ubuntu 24.04 LTS Minimal amd64/x86_64 under QEMU emulation** on the ARM64 Mac, pinned to an exact image revision/digest. This supersedes Alpine; CPU architecture has not changed. There is no measured fastest-image claim.
- Phase one supports **VS Code Browser only**. Other IDE integrations are deferred. This narrowing does not itself remove Git or devcontainer requirements; the accepted glibc/amd64 project-container scope and required compatibility checks are documented below.
- **OIDC remains the native authentication boundary. Embedded Dex is selected**, retaining the external OIDC integration direction. ShiftBox owns application membership/authorization. Dex owns local credentials without a duplicate ShiftBox password store. ShiftBox enforces a restricted first-login password-change flow server-side. Each installation generates a unique temporary administrator password delivered locally; restart/restore must not reset the chosen password. MFA is deferred to phase II and will be optional configuration. Dex storage/embedding and account-management details still require validation. External-Keycloak-only deployment is superseded.
- Retain **one active controller** initially. Q115 selects SQLite for demos/very-small installations and PostgreSQL for production, with externally operated PostgreSQL and ShiftBox-managed schema/seed operations. Q117 applies these profiles to both ShiftBox management/application state and Dex identity state; their schema/store ownership remains separate. PostgreSQL support alone does not authorize HA/distributed controllers.
- Design for growth, but **defer scaling/load tests until the product is fully implemented**. The proposed 100-running-workspace/10-concurrent-start target was not adopted. Functional correctness and provider compatibility are separate from load testing.
- Phase one requires a logged-in, awake macOS host with UTM running. Do not promise availability during sleep/logout or unattended pre-login operation; initial operation is operator-installed and manually started (Q153); startup convenience is a delegated engineering detail.
- Users select **T-shirt sizes**, for example Small, Medium, Large and XLarge. Administrators define each size's CPU, memory and disk plus installation budgets. Reserve CPU/RAM on accepted start and release only on confirmed stop/deletion; account disk until verified deletion. Uncertain operations retain reservations. Over-capacity starts fail clearly; no automatic queues, eviction or resizing. Exact values remain administrator configuration.

- Each workspace pins its resolved CPU/memory/disk values at creation. Administrator edits to T-shirt definitions affect future workspaces, not existing allocations. Reservations use those pinned values; any future explicit resize is a separate operation.

## Agreed architecture

```text
Platform image release process
  HashiCorp Packer + pinned UTM-compatible plugin fork
    -> reusable, versioned Ubuntu 24.04 LTS Minimal amd64 platform artifact
       containing guest service and required runtime tooling

macOS host
  ShiftBox binary
    UI Web Service
      -> Controller API Service
         -> Platform Provisioning Service
            -> UTM lifecycle, disks and supported network configuration
    Proxy Service
      <- authenticated browser IDE and application-preview connections

  One UTM VM per workspace
    Ubuntu 24.04 LTS Minimal amd64 platform OS
      DevContainer Provisioning Service
        -> polls Controller API for desired state and identified operations
        -> runs Dev Container CLI and approved local lifecycle operations
        -> monitors administrator-configured inactivity signals
        -> establishes a persistent outbound tunnel to Proxy Service
      Project devcontainer and related containers
        <- browser IDE / preview traffic carried through the proxy tunnel

Supporting identity service
  Embedded Dex OIDC provider/broker (Dex credentials; ShiftBox setup gate)
  Optional external OIDC provider
  ShiftBox -> membership, permissions and workspace authorization
```

The platform OS and project devcontainer OS are different layers. Phase one now explicitly supports **glibc-based amd64 Linux project containers**, with Debian/Ubuntu reference cases; Alpine/musl project containers are deferred. This scope is independently accepted under Q71 and does not follow automatically from the platform OS.

The guest's desired-state polling and user-traffic tunnel are logically separate. Both may share an internal agent HTTPS endpoint while remaining separate logical channels; internal mTLS certificate selection is separate from browser-facing uploaded TLS. The proxy carries user ingress into workspaces; it is not automatically a proxy for Git, package downloads, or arbitrary workspace egress.

Throughout the effective summaries, remaining exact versions, paths, algorithms and validation mechanics are delegated engineering work under Q155, not additional unanswered product questions. The design series and explicit defaults distinguish those details from accepted contracts.

### Platform artifacts and provider integration

- **HashiCorp Packer builds platform artifacts in an image-release process**, not during ordinary workspace creation.
- ShiftBox consumes prepared artifacts, provisions individual VMs, assigns their identities, and invokes the Dev Container CLI inside them for project-specific environments.
- The selected starting plugin is [naveenrajm7/packer-plugin-utm](https://github.com/naveenrajm7/packer-plugin-utm).
- Maintain a **pinned UTM-compatible fork**, starting from the existing UTM 5 compatibility work. This is an agreed future implementation direction, not permission to create or modify a fork during the interview.
- Acceptance of the builder must include the selected Ubuntu 24.04 Minimal amd64 build, shutdown, export, import and boot path. Removing a version check alone does not establish support.
- Administrators explicitly import versioned platform artifacts into a local ShiftBox catalog and select a default for new workspaces. Record each workspace's exact artifact revision; do not silently fetch a moving latest release. Remote catalogs/automatic downloads are deferred.
- Accept only self-contained manifest-described artifacts recording architecture, platform version, agent protocol version and expected digests. Persistent disks belong to the artifact, not external files/backing images. Administrators approve the source; a checksum alone is not publisher authentication. Catalog artifacts remain immutable, separate from writable workspace copies.
- **Once launched, a workspace remains on its platform OS/image; updated images apply only to new workspaces. Q76 explicitly exempts the DevContainer Provisioning Service from this freeze.** After a controller API update, that service self-upgrades on its next polling cycle, falling back to the previous working version if the upgrade fails. The UI reports the failure. Q86 defines compatibility as current and immediately previous published ShiftBox release (N/N−1), with sequential controller upgrades, shared product release IDs and separately tracked protocol revisions. Every retained workspace must reach the current controller release before the next controller upgrade; stopped/pending/unreachable/rolled-back workspaces count. Old catalog images require compatible admission. Administrators may schedule start–upgrade–stop maintenance, allow a user grace period and force maintenance afterward so user postponement cannot block upgrades indefinitely. Q97–Q99 settle maintenance state/capacity/retention behavior, safe-boundary enforcement and genuine failure handling below; exact timings remain open. No automatic OS migration/replacement; publishing a platform image alone does not patch existing OS installations.
- Q87 accepts next-poll staging with activation at a safe lifecycle-operation boundary, verified/authenticated artifacts, independent supervision, recoverable local state and health verification. Running containers should survive; brief proxy/browser disconnection is acceptable.
- Q88 accepts no automatic reactivation of the same failed artifact after rollback until an administrator retries or supplies a corrected compatible artifact. Keep the error and upgrade block visible; transient download failures may use bounded backoff.
- Q97 accepts management-only maintenance boots for previously stopped workspaces, returning them to stopped; preserve running workloads where possible and honor newer explicit desired-state changes. Capacity limits apply, maintenance is not user activity, and inactivity deletion must be coordinated. Reconcile overdue schedules after host recovery.
- Q98 ends user postponement after the maintenance deadline, blocks new conflicting lifecycle operations and waits for a safe activation boundary. Hung/unsafe operations require administrator handling; no automatic force-kill/power-off is authorized.
- Q99 retains the controller compatibility gate after real maintenance failure until explicit repair/retry or deliberate retirement. No deadline-based compatibility bypass, automatic destruction or quarantine exception is accepted.
- Initial VM provider adapters are compiled into Go behind an internal lifecycle/capability contract (UTM first, EC2 second). Public runtime plugins and remote workers are deferred. Exact base/tool versions, cache cleanup, publisher authentication, upgrade safety and compatibility details remain open.

### Workspace lifecycle and retained files

- ShiftBox reconciles durable desired state and explicitly identified operations. Repeated delivery must not blindly repeat destructive work.
- The guest service executes approved lifecycle operations; its management API is not a general-purpose remote shell.
- Designated user files survive **VM reboot, VM stop/start, and devcontainer rebuild**.
- **Destroying the VM loses workspace data.** Users are responsible for persisting their work to Git before destruction. There is no agreed automatic workspace-data backup or post-destruction recovery guarantee.
- Retain project working files, the selected developer account's home (including IDE settings/extensions stored there), and application data explicitly placed in designated persistent mounts. A developer home need not be `/home/vscode`.
- Container writable layers, images, build caches and changes outside retained areas have no preservation guarantee. Caches may survive but are disposable.
- Use **one persistent, VM-owned disk** for the OS and managed project/home/application-data areas. Retained mounts remain outside disposable devcontainer layers. It survives reboot/stop/rebuild and is removed on verified destruction. Separate data disks are deferred until a concrete replacement/migration requirement exists. Exact paths, mount declarations and infrastructure-failure recovery remain open.
- Intentional destruction requires an authorized user/admin action **or an explicitly configured inactivity-retention policy**.
- Destruction is complete only after the provider confirms removal of the VM and all inventoried workspace-owned resources covered by the deletion contract. Shared resources are preserved; incomplete cleanup stays durable, visible, and retryable.
- Healthy workloads should continue computing through controller outages. Proxy connections may be interrupted. Recovery must reconcile surviving VMs rather than assume that a lost response means an operation never happened.
- Reconnection authenticates an existing workspace/VM identity. Unknown agents do not silently recreate ownership or permissions; they require explicit recovery.
- IDE connection readiness requires authenticated guest, project container, the standard Dev Container `waitFor` gate and a working browser IDE proxy route; later setup has separate progress/failure reporting.
- Offer explicit repair mode for project configuration/build/required-hook/IDE failures when the VM and guest service remain healthy. Preserve retained data and diagnostics; use a known-good platform-supplied environment without failed-project hooks/tasks/extensions or unapproved downloads.

### Network configuration

- The platform provisioning service translates a standard administrator-facing network configuration into supported provider controls.
- **Provider metadata declares network-restriction capabilities.** Unsupported controls are not configurable in the UI; the UI instead describes the provider's default access.
- Do not claim a requested restriction is enforced when the provider cannot enforce it. The exact capability schema and validation behavior still need to be specified.
- **All outbound access is a valid configuration.** The user explicitly rejected requiring host/sibling network isolation as a universal eligibility condition. A VM execution boundary does not imply a network-isolation guarantee.
- Offer the exact label **No Network Access**, with explicit exceptions for required Git-provider access and guest-to-ShiftBox management access. The later accepted tunnel decision includes controller API and proxy connectivity through the ShiftBox HTTPS endpoint.
- The standard restricted-policy rule model is an **outbound destination-IP-range/TCP-or-UDP/destination-port allowlist**. Required exceptions are the configured Git origin, explicitly approved auxiliary Git/LFS/submodule hosts, a designated DNS resolver and ShiftBox API/proxy endpoints. Repository content/redirects do not widen access automatically. Git credentials separately enforce repository authorization. Unsupported effective policies are unavailable, not silently broadened.
- Authorized policy changes occur only while the VM is stopped initially. Preserve retained files; apply/verify before start and keep stopped on failure. Capability metadata covers updates as well as creation. Address refresh, IPv4/IPv6 and enforcement mechanics remain open.
- The restricted profile does not silently permit registry, package, extension-marketplace, or arbitrary build downloads. Necessary artifacts must be locally available or delivered through a separately approved mechanism. Failed prohibited downloads require a clear explanation, not a hidden policy bypass.
- There is no decision that every provider must support every profile. There is also no verified claim that UTM currently implements the desired fine-grained restrictions.

### Guest trust, authentication and connections

- **The entire guest VM, including its provisioning service, may be compromised.** Infrastructure credentials and controller-wide authority stay outside it. Guest API permissions are scoped to its workspace. Q79 adds delivery of application secrets, including organization-scoped secrets: those delivered values are exposed to a potentially compromised guest, so secret selection and blast radius require explicit policy.
- Use **mutual TLS with a unique, revocable per-VM identity** for polling and proxy tunnels. Do not bake a shared agent credential into the platform artifact.
- Application-level operation IDs and freshness/replay controls remain necessary. Baseline mutual TLS does not include independently signing every application payload.
- Authenticated communication establishes identity and message integrity, **not the truthfulness of a compromised guest's reports**.
- The guest establishes a persistent outbound connection to the proxy for browser IDE and preview traffic. Directly reachable guest ports are not the selected user-access path.
- Automatically enroll each newly provisioned VM with a short-lived, single-use credential scoped to its workspace and VM instance, delivered separately from the reusable image. Supply controller trust information; the guest generates its private key and exchanges the credential for a scoped client certificate.
- Connected agents automatically renew certificates before expiry. Expired credentials cannot renew themselves: eligible known VMs recover through fresh controller-authorized, provider-delivered enrollment, not a guest identity claim alone. Revoked identities never recover automatically; administrator remediation and fresh authorization are required. Revocation closes polling/proxy access; host-side stop/delete remains possible.
- A host-created per-VM seed disk is the UTM enrollment candidate. Pinned UTM source supports replacing a designated drive source while stopped, preserving the complete drive list. Delivery, per-boot consumption, cleanup and replay resistance remain end-to-end validation gates; see the certificate design.
- Q150 settles Q145: internal mTLS client/server leaf certificates and private keys rotate every four hours, with eight-hour validity under a stable setup-generated installation CA. The CA does not rotate every four hours. Existing tunnels reauthenticate no later than the certificate used for authentication expires; revocation closes access immediately. Offline VMs cannot rotate: on authorized return, validate the exact provider VM/current eligible registration and deliver a fresh short-lived single-use grant plus current trust through the verified provider channel. Never accept expired trust, automatically restore revoked identities, or force-stop running work merely to swap recovery media. CA expiry, guest clock correctness and N/N−1 compatibility remain explicit checks. Full recovery contract and sources: [Certificates and guest trust](certificates-and-guest-trust.md).
- Uploaded browser TLS and independently rotating internal mTLS are separate certificate lifecycles. TLS certificate choice precedes HTTP path routing; endpoint/SNI/listener separation is an engineering requirement to design, not a claim that one certificate can satisfy both lifecycles by path alone.

### Inactivity and automatic deletion

- The **DevContainer Provisioning Service monitors inactivity**. Initial selectable monitors are browser IDE connection presence and guest CPU activity. Any selected monitor reporting active keeps the workspace active; all selected monitors must report idle before inactivity begins. An unattended open IDE counts as active when connection monitoring is enabled. Thresholds, defaults, freshness and sampling intervals remain open.
- Administrators set permitted retention limits; workspace owners may choose shorter retention periods. Automatic deletion is disabled until explicitly configured, and the applicable policy is visible to the owner.
- **Stopped or missing polling counts toward inactivity.** The earlier recommendation to treat missing telemetry as unknown and never count it was rejected.
- Stopping a guest monitor must not pause the controller's retention countdown.
- **Phase one accepts that a compromised guest can send validly authenticated false activity reports and evade an inactivity timeout.** Cleanup is cooperative resource management, not guaranteed enforcement against a malicious live guest.
- No independent hard maximum lifetime was approved. It remains a possible separate policy decision if later desired.
- Following a known controller/host outage, elapsed inactivity time still counts, but ShiftBox must reconcile and allow a recovery grace period before executing overdue destruction. An individual agent disappearing while the controller remains healthy follows the normal configured timeout.
- Use a continuous, controller-owned deadline visible in the UI. Fresh selected activity resets it; ordinary idle polls do not. Stopped VMs continue counting. Missing polls use controller-observed contact time without erasing an earlier idle start; stale active reports cannot keep renewing. Known outages retain reconciliation/grace safeguards.
- Q151 accepts final fresh-activity/authority/policy/desired-state checks, outage recovery grace and serialized deletion, followed by graceful stop and provider-enforced stop/deletion after a configurable timeout even if the guest/build is hung. This does not authorize unsafe maintenance force-kills. Deletion remains disabled until configured and loses workspace data. Numerical engineering defaults are documented separately; no timeout alone proves removal.

### Users, permissions and audit

- Initial roles are **admin** and **member**. Workspaces are owner-private; initial sharing/collaboration and custom-role management are deferred.
- Start with resource-aware permission checks and predefined role-to-permission mappings, so future granular roles do not require rewriting feature checks.
- Do not introduce an external authorization engine, general policy language, or custom-role editor merely to support the initial two roles.
- Admins may manage organization settings, membership, infrastructure policy, and workspace lifecycle.
- Admins also have an **explicit, audited permission to connect to another user's workspace**. Connecting and lifecycle administration are separate permissions, enabling future roles to separate them.
- Embedded Dex owns OIDC credentials; ShiftBox enforces the first-login setup gate and application authorization. ShiftBox owns membership/permissions; login alone does not confer membership or administration.
- Additional externally authenticated users request membership; an admin explicitly approves them as member or admin. Q136 local accounts are administrator-created with an explicitly assigned role, without a redundant membership request. Unapproved users have no workspace access. Membership keys are stable issuer/subject identifiers, not email. Q84–Q85 settle Dex-owned credentials, a server-enforced first-login gate and a unique temporary password per installation delivered locally; Q140 requires collecting the initial administrator email through local setup/configuration before generating the temporary credential, preserving password-change-before-PG order. Q136 accepts email-address login, administrator-created temporary passwords, mandatory first-login change, self-service password change and administrator-mediated recovery, without public registration or email delivery in phase one.
- Disabling membership immediately revokes ShiftBox access/proxy sessions and requests provider-enforced stops of owned workloads. Preserve VM data under retention policy and track stop failures. Detection of external identity-account changes remains separately specified.
- Invitations and automatic provider group-to-role synchronization remain deferred. Permission matrix, audit retention and setup remain open. Q141 accepts a one-time displayed temporary password generated by an authorized admin for private delivery, forced change and affected-session revocation, with no public reset links/email. Q142 accepts local CLI recovery of an existing admin using protected installation state/DB credentials, preserving data and auditing recovery; it is not an unauthenticated network endpoint or arbitrary admin-creation path; the former existing-first-admin binding applied to an external issuer and is superseded by Q73.

### Control-plane state and backups

- Retain one active controller. Q117 settles both application and Dex storage: SQLite for demo/very-small installations, externally operated PostgreSQL for production. Keep external side effects out of metadata transactions and preserve durable operation/recovery boundaries.
- PostgreSQL server deployment/operation is outside ShiftBox. ShiftBox manages its schema and initial seed data. After the administrator changes the initial password, UI setup requests PostgreSQL connection details and seeds the initial schema. CLI setup also prompts privately for PostgreSQL administration credentials, seeds a new target and updates ShiftBox connection configuration. Q118 accepts an existing empty administrator-provided target or optional creation of owned database/roles with adequate privileges. Discard elevated bootstrap credentials after use, retain only scoped service credentials, validate/seed before config activation, and make retries recoverable without overwriting unrelated data/resetting passwords. Dex retains necessary schema-management authority in its own namespace. Q123 accepts one PostgreSQL database with separate ShiftBox/Dex schemas and scoped owner accounts. Validate database-specific role search paths and privileges before activation; Dex has no assumed schema configuration property. Exact migration/utility delivery remains open.
- Q124 accepts restricted local SQLite bootstrap until the initial password is changed and a profile is chosen. Production transfers only initial-owner/setup state to PostgreSQL, preserving identity/password and requiring fresh sign-in; no normal accounts, workspaces, templates or secrets before setup completion. Explicit demo mode may complete on SQLite. Validate before activation and never silently revert to SQLite once PG is authoritative. General populated-demo migration is deferred; the constrained transfer still needs functional validation.
- Q114 rejects mandatory Mac Keychain custody. Encryption key storage must be portable and application-managed, including persistent container deployments. Q119 accepts a unique installation key in protected persistent storage outside the DB, with an operator-supplied mounted-key-file option. Apply platform-appropriate permissions, retain keys beyond container replacement and include recoverable material in the independently encrypted management archive. Missing/wrong keys for existing ciphertext fail clearly, never silently regenerate. This does not protect against a compromised controller account or host root; Q134 includes required key material even when supplied through a mounted file, under the independent archive password. Validate matching keys on restore and never silently overwrite a supplied key. Q135 defers application encryption-key rotation to phase II; do not schedule its phase-one implementation. This does not defer provider token expiration or Dex signing-key behavior.
- Include **operator-triggered management-state backup and restore** in phase one. Q122 confirms the encrypted archive includes owned application/identity data and recovery configuration even with external PostgreSQL; it excludes the PostgreSQL server, unrelated databases and workspace disks. The operator runs the database infrastructure. Coordinated snapshots, role restoration and portable restore-target details remain open.
- Q125 permits a brief pause/drain of management/sign-in and background mutations to capture matching application/Dex/key/configuration state while VMs keep running. Resume after consistent snapshots/copies are secured; show progress and fail clearly if consistency cannot be obtained. Exact duration is not guaranteed.
- Q133 accepts production live Dex protection through restricted access, verified PG TLS and operator-provided encrypted storage, without additional Dex field encryption initially. Authorized DB admins may read sensitive Dex records; app-secret encryption does not cover Dex writes by implication. Demo SQLite protection depends on disclosed host-storage limits.
- **Built-in encrypted management backups are now required**, superseding plaintext export with external administrator encryption. Recovery must remain possible after loss of the installation; the only usable decryption key cannot exist solely inside its encrypted archive. At Q75 the user accepted an operator-supplied encryption password through either CLI or UI. ShiftBox does not retain that backup passphrase; the operator must preserve it independently.
- These backups do not back up workspace files or change the VM-destruction data-loss contract.
- Include ShiftBox's database/configuration, OIDC client configuration/secret and required decryption/trust keys for self-contained recovery of its owned state. Include installation-owned Dex state in the coordinated archive; exact Dex persistence/snapshot mechanics remain open. VM disks remain excluded.
- Q146 retains the original Mac/logical installation recovery scope and accepts an operator-selected empty replacement PostgreSQL target with recreated/validated owned schemas/roles. Replacement credentials may be supplied; recovery does not require the failed database or assume archived DB credentials remain valid. Do not overwrite unrelated targets or broaden this into cross-host failover. Restore enters administrator-reviewed recovery mode. Archived sessions/enrollment credentials do not automatically become usable; mutations, automatic deletion and workspace access stay gated until membership/resources/agent identities are reviewed. Guest computation may continue; cross-host migration/failover is deferred.
- The goal is one encrypted archive of coordinated controller and locally owned identity state; this does not require one process/database and does not roll back running VM files or external identity systems. Exact snapshot tools/coordination, supplied-key restore destinations and restore validation remain open. Key custody is accepted; application-key rotation is phase II.

### Application secrets and project templates

- The guest DevContainer Provisioning Service retrieves authorized controller secrets and performs technology-specific delivery: files/environment bindings, automatic integrations such as a Git credential helper, developer-controlled consumption through devcontainer.json, and `cloudbox secrets get-secret <name>` returning a value to its caller.
- **Q104 supersedes the earlier binding-only retrieval restriction.** The CLI may retrieve any secret available to its authorized owner-user/project/organization context by key; workspace identifies context but is not a secret storage scope. Technology-specific injection/binding remains useful, but is not an additional CLI authorization boundary. The controller derives the permissible context from authenticated identity and owned records; guest-supplied IDs do not authorize other users/workspaces.
- **Q111 fixes the complete hierarchy: User → Project Template → Organization**, highest priority first. Present a flat effective key space. There is no individual workspace secret scope; Q111 supersedes Q102. Q121 accepts deletion of an override revealing the next authorized lower-scope value; an explicitly saved empty value remains an override. Show fallback consequences before deletion. Revoking project access excludes that project source immediately without removing independently authorized user/organization sources.
- User settings manage personal secrets; project owners manage project secrets in their templates; admins manage organization secrets. Template owner/admin permissions from Q101 remain. No individual workspace secret-value store or workspace secret-management UI is included.
- Q112 accepts owner-derived lookup context from controller records and authenticated VM identity; an admin browser connection does not switch the guest to the admin's personal secrets.
- Q113 accepts immediate loss of project-secret retrieval and new-launch rights when template access is removed. Existing owner-private workspaces/files remain intact; already-delivered values cannot be retracted. Full organization offboarding remains the separate accepted stop policy.
- Q90 introduces shareable project templates defining repository, environment size, secrets and launch configuration. Q100 accepts each launch as an independent owner-private workspace from a recorded template revision with captured configuration, resolved size and secret references. Later template edits affect new workspaces only; secret values and revoked permissions are not frozen by this snapshot.
- Q101 accepts user-owned templates, sharing launch permission with selected members, and template owner/admin edit and project-secret management rights. Collaborative editors/public sharing are deferred. Sharing does not grant access to an existing running workspace.
- Q137 accepts archival instead of destructive template removal: no new launches, preserved workspaces/history and secret grants until explicit revocation. Archived templates are visible only in a filtered administrator view. Q144 allows existing workspaces to display their captured project name/revision while members cannot open archived-template details; archival does not itself revoke existing project-secret access. Q154 defers permanent purge and owner transfer; administrators retain management of archived/orphaned templates.
- Q103 accepts that template use grants access to its shared project secrets, with a visible warning of that consequence. Personal requirements resolve against the launching user, never silently against the template author's credentials. Store references in template configuration, not embedded plaintext values. Initial Git access must be resolvable before reading a private repository's devcontainer.json.
- Q90 retains no administrative UI/API redisplay of saved values and metadata-only auditing. The authorized in-container CLI intentionally returns values. Q91 accepts technology-specific file-first delivery with explicit environment compatibility. Q96 defers application secrets during Docker image builds; runtime and Git setup delivery are in scope.
- Q105 accepts on-demand current authorized reads, no persistent offline retrieval cache, and clear failure when the controller is unavailable. Existing workloads/already-injected values can continue; new secret reads/Git authentication may fail. Already-running environments do not automatically reload rotated values.
- Q92 accepts application-level authenticated encryption of individual values, contextual binding and key versions; this applies independently of the eventual SQLite/PostgreSQL deployment profile. Metadata is not promised value-level encryption; the full backup is encrypted. Portable persistent-file/mounted-key custody and encrypted-backup inclusion are accepted; Q135 defers application-key rotation to phase II.
- A compromised guest can request/copy any secret authorized to its effective scope, even when no application explicitly injects it. Future delivery revocation cannot revoke an already copied external credential. This wider exposure follows the user's Q104 decision. No application-secret lookup grants access to controller infrastructure credentials, encryption root keys, signing keys or identity-service internals.

### Git provider integration

- Phase one uses a GitHub App with browser OAuth user authorization, administrator-owned registration/installation setup and GitHub.com HTTPS Git access under the workspace owner's GitHub authority. SSH, Enterprise Server and other provider integrations remain deferred; OIDC login and GitHub authorization remain separate.
- **Developer tools are developer-managed.** Q127 emphasizes freedom inside the devcontainer; Q128 rejects a bundled/supported gh installation or ShiftBox launcher. This does not remove existing VM/network/authorization boundaries or require ShiftBox to manage every tool a developer chooses.
- Q126 accepts Contents/Pull requests read-write and Metadata/Checks/Commit statuses read; Q127 accepts Workflows write for developer workflow-file changes. GitHub user rights, branch rules and app approval still apply. No product PR UI/launch/review/merge/status management is in scope.
- Q129's requested integration is exposure of the user's GitHub OAuth token for Git HTTPS, rather than the proposed dedicated credential broker/custody contract. The source calls this an “OAuth JWT”; the interoperable design term is **GitHub App user OAuth access token**, treated as opaque. The app-signed JWT used for app authentication is different and is not the documented Git HTTPS credential. Do not expose app signing keys or replace user authority with installation authority.
- **Q139 and Q148 require encrypted persistence of user access and refresh tokens across controller restarts**, coordinated automatic renewal when supported, and user reconnection after revocation or permanent renewal failure. Expose only the current authorized access token to the guest. Already-copied tokens are not automatically replaced. Do not reintroduce a separate broker product or developer-tool management merely to implement the agreed token exposure. Administrator-owned application configuration remains required and must stay protected.
- Q130 rejects the proposed webhook-free checking/status contract by reference to Q129. This does not affirmatively request webhooks. Do not infer either a webhook service or a managed access-status feature; Q148 settles token renewal without adding webhooks or a managed access-status feature.
- Q131 rejects the managed LFS/submodule support proposal and Q132 rejects the managed personal-account installation proposal, both referring to token exposure. They do not prohibit developers using their own tools where GitHub permissions and network policy allow it. ShiftBox makes no new supported-tool/fork-access guarantee; exposing a token cannot bypass app repository/install coverage.
- Existing general secret retrieval, guest lifecycle and provider controls remain. Q148 settles automatic renewal and current-token exposure; precise names/interfaces remain delegated engineering choices within the developer-managed tooling contract. Do not silently revive a reserved broker namespace or automatic gh authentication that was rejected.

### Notices and maintenance communication

- Q138 accepts in-product maintenance warnings/countdown with an administrator-selected deadline, without requiring an external notification service. Lack of user acknowledgement does not block the accepted safe mandatory-maintenance policy.
- Administrators can publish systemwide notices through a notifications component; a user may dismiss a notice after reading. Q143 accepts per-user read/dismissal history; dismissal hides a notice only for that user, does not prove understanding and does not cancel maintenance. Maintenance deadlines/progress remain on affected workspaces. General notices do not create enforcement actions; no outbound messaging integration is authorized. Q145 adds administrator notifications 30 days before uploaded web TLS certificate expiry.

### Browser UI and access

- **Subsequent explicit user decision: Mantine is the ShiftBox UI component library.** This settles the earlier open library choice. Use Mantine components and a shared Mantine theme for the application UI, following the [Mantine documentation supplied by the user](https://mantine.dev/llms-full.txt) and [official Next.js integration guide](https://mantine.dev/guides/next/). Integrate `MantineProvider` and required package styles; keep the Next.js build compatible with static export and embed the resulting assets in Go. Exact package versions remain implementation choices to pin and validate. This decision concerns the greenfield ShiftBox UI; it does not authorize changes to the reference CloudBox component library.
- Q106 selects **Go HTTPS + embedded Next.js static export** and live Go APIs, using export-compatible routing. React/Next.js/TypeScript remain the UI stack; Node is a build dependency, not a required runtime UI service. Go owns runtime authentication/authorization and installation APIs/configuration.
- Separate browser origins for management UI, each workspace IDE and each preview service, with scoped credentials.
- Administrator-provided base DNS and separate management/IDE/preview origins remain accepted. **Q145 now requires browser-facing TLS certificate upload through the UI and admin expiry notices at 30 days**, superseding the proposed general local-CA web leaf auto-renewal contract. Internal mTLS has its own setup-generated CA/four-hour policy. Q149 accepts loopback-only temporary HTTPS setup with explicit operator trust, or supplied initial certificate/key paths. Normal network serving starts only after validating the uploaded chain, hostname coverage and matching private key. Expiry never triggers silent HTTP or temporary-certificate fallback. Internal certificate selection remains separate.
- Q147 requires authenticated previews restricted to the owner or explicit audited admin connection permission. No anonymous/public preview links or preview sharing initially. Reuse sign-in through narrow service access and never forward management credentials to guest applications; cookie/redirect/WebSocket details are engineering validation obligations.
- Support private-network HTTPS and localhost development; public tunnels/automatic Internet exposure remain deferred. Logged-in awake Mac availability limits still apply.

## Additional closing contracts

- **Q152 external identity:** administrators may configure a positive finite upstream authorization interval in either direction; four hours is the initial engineering default. Require successful actual upstream authorization by that deadline; broker-local sign-in or token refresh alone does not reset it. Upstream SSO may satisfy authorization without a password prompt. On failure close affected browser/API/IDE/preview access while VM computation may continue. Explicit ShiftBox offboarding remains immediate access revocation and a stop request. No universal directory-disablement SLA or MFA assurance claim is made.
- **Q153 operation and updates:** initial native Mac operation is manual and operator-installed, with prerequisite/state/guest compatibility checks and a completed verified coordinated management backup before irreversible ShiftBox or Dex schema changes. Bundle or explicitly validate PostgreSQL utilities. Recovery after migration uses a compatible release and backup through the existing review gate; binary-only rollback is not promised.
- **Q154 administration:** defer permanent template purge and ownership transfer. Audit/notice retention is configurable; preserve required provenance, active maintenance and current authorization state. Storage pressure blocks allocations and alerts admins; it grants no new authority to delete retained workspace or identity data.
- **Q155 synthesis:** produce a reviewable design series and diagrams. Internal paths, algorithms, endpoints, libraries and bounded defaults are delegated engineering work. Product/security tradeoffs discovered by validation return to the user. Scale testing stays deferred; no implementation authorization follows automatically.

## Historical closing round Q148 through Q155

**The original question/recommendation wording below is historical.** Q148–Q155 are answered in the ledger. Q150 additionally requires the researched offline recovery path; Q152 permits administrator configuration, superseding the recommendation to allow only stricter intervals. Q155 requests the design series and diagrams. Final readback confirmation remains outstanding; next unused number is Q156 only if a material new decision arises.

### Q148 Persistent OAuth renewal

Should ShiftBox encrypt and persist both GitHub user access tokens and their refresh tokens, renew automatically when supported, and request GitHub reconnection if authorization is revoked or renewal permanently fails?

**Recommendation:** Yes. This makes restart persistence useful beyond the access token's expiry. Coordinate refresh per user and expose only their current authorized access token to the guest. Developer-managed tools may need to retrieve a replacement after expiry; already-copied tokens are not magically updated. Do not add a separate broker product, bundled gh or product PR management.

### Q149 First HTTPS setup and uploaded certificates

Should first launch without a supplied certificate start only a loopback HTTPS setup UI using a temporary generated certificate that the operator explicitly trusts, then enable normal network access only after uploading and validating the browser certificate chain and matching private key?

**Recommendation:** Yes. Initial certificate/key file paths are an optional headless/operator path; ordinary replacements remain available through the admin UI. Validate hostname coverage and pair consistency before activation, notify admins at 30 days remaining, and never silently fall back to HTTP or temporary production certificates after expiry. Production web TLS is separate from internal mTLS.

### Q150 Complete four-hour mTLS contract

Should internal client/server leaf certificates and their private keys rotate every four hours, with eight-hour certificate validity for bounded overlap/retry, under a stable installation CA generated at setup?

**Recommendation:** Yes. The CA does not rotate every four hours. Use an internal agent endpoint/certificate selection separate from uploaded browser TLS. Reauthenticate long-lived tunnels no later than their authenticated certificate's expiry, accepting a brief reconnect if needed; current revocation rules still close access immediately. Automatically re-enroll an expired known VM only after controller/provider verification through the existing authorized delivery path; never accept expiry alone as authority and never automatically recover a revoked identity. If reliable provider verification/delivery is unavailable, show the recovery failure for administrator action. This eight-hour validity is an explicit proposed margin, not a hidden interpretation of the user's four-hour requirement.

### Q151 Retention deletion versus hung work

When an explicitly enabled retention policy authorizes deletion, should ShiftBox recheck fresh activity, authorization, policy and desired state; honor outage recovery grace; serialize conflicting operations; then attempt graceful shutdown before allowing provider-enforced stop/deletion after a configurable timeout even if a build or guest is stuck?

**Recommendation:** Yes. Show the deadline/warnings and allow fresh activity to reset the inactivity deadline before deletion commits. Retention deletion remains disabled until configured, and deletion loses workspace data as already agreed. This authority is for deliberate/policy deletion, not maintenance upgrades, which still cannot force-kill unsafe work. Timeouts are not proof of deletion: preserve reservations and visible retryable failures until inventoried resource removal is confirmed.

### Q152 External identity sessions and offboarding

Should external OIDC sessions require successful upstream authorization at least every four hours by default, closing browser/API/IDE/preview access when that deadline cannot be met, while allowing VM computation to continue?

**Recommendation:** Yes. Administrators may configure a stricter interval. Successful authorization may reuse upstream SSO; this bounds time since provider acceptance, not guaranteed detection of directory disablement. Immediate offboarding remains explicit ShiftBox membership disablement, which revokes access and requests workload stops. Preserve local administrator recovery, stable issuer/subject/connector bindings, and administrator-approved identity linking; never merge accounts automatically by email. No universal forced-password-login or instantaneous upstream revocation guarantee is implied.

### Q153 Operator installation and safe updates

Should the initial supported operation be operator-installed and manually started on the logged-in Mac, with release prerequisite checks, preserved installation state, the accepted guest compatibility gate, and a completed verified management backup before either ShiftBox or Dex makes irreversible schema changes?

**Recommendation:** Yes. Bundle or explicitly validate required PostgreSQL client utilities rather than letting backup fail unexpectedly. Do not promise automatic binary-only rollback after database migration; recover using a matching release and coordinated management backup through the accepted review gate. VM files and external effects are not rolled back. Startup convenience, exact utility packaging and internal migration mechanics can be documented engineering choices under Q155, without changing the awake/logged-in UTM requirement.

### Q154 Remaining administration and storage limits

Should permanent template purge and ownership-transfer features be deferred, while administrators retain management of archived/orphaned templates; audit/notice retention is configurable; and storage pressure blocks new allocations and alerts administrators rather than automatically deleting retained user files or identity state?

**Recommendation:** Yes. Retention cleanup of logs/notices must preserve required workspace provenance, active maintenance records and authorization state. Existing explicitly configured workspace-deletion policies remain valid; resource pressure creates no additional deletion permission. Numerical defaults and operational limits will be listed in the readback/implementation contract for review, not silently assumed.

### Q155 Design closure and engineering responsibility

Should the final readback consolidate the accepted product/security/operations contracts, explicitly proposed engineering defaults and deferred scope, while leaving internal algorithms, paths, endpoint names, library selection and similar implementation choices to engineering within those boundaries?

**Recommendation:** Yes. Include a mandatory functional/security validation backlog: exact UTM/Packer image lifecycle and provider capabilities; retained files/repair; crash, lost-response and resource-pressure reconciliation; certificate enrollment/rotation/expiry/revocation; Dex bootstrap and scoped PG access; encrypted backup/restore; owner/admin/offboarding/secret authorization; browser/preview isolation and accessibility. Keep scale/load testing deferred as requested. Review and confirm shared understanding before a separate implementation authorization. A failed validation that requires a product/security tradeoff returns to the user rather than silently weakening the design. Do not claim the architecture is production-proven merely because the design is approved.

## Final readback delivery

The requested series has been prepared in this architecture folder. [Start with the design readback](design-readback.md). Seven supporting documents cover architecture/communication, lifecycle/providers, identity/browser security, templates/secrets/Git, certificates/guest trust, operations/recovery, and validation/defaults. Seven SVG diagrams have editable Mermaid source companions under `diagrams/`.

Shared-understanding confirmation remains a user decision after review. Implementation requires separate authorization; no code migration or runtime changes follow automatically from these answers.

## Historical round Q139 through Q147

All answers are recorded. Q139 requires persistence instead of memory-only tokens; Q145 replaces the proposed certificate policy. The other recommendations were accepted. Original questions are preserved for provenance, not interpreted as accepted where rejected.

### Q139 OAuth token lifetime without persistence

Should ShiftBox keep the user's GitHub OAuth access token only in controller memory and require GitHub reconnection after token expiry or controller restart, rather than persist user access/refresh tokens?

**Recommendation:** Yes if “not storing GitHub credentials” is a strict persistence requirement. This is the simplest faithful interpretation, with an explicit cost: unattended Git authentication stops after expiry/loss until reconnection. GitHub App user access tokens expire after eight hours by default. If unattended renewal is required instead, select encrypted refresh-token storage explicitly. Do not call a refresh token a non-credential; no separate broker service or bundled developer tooling is implied. Administrator-owned OAuth application configuration remains a separate required setup concern.

### Q140 First administrator email

Should local installation setup request the first administrator's email address before creating the initial Dex account and generating its unique temporary password?

**Recommendation:** Yes. Obtain it through the local setup command/configuration, deliver the temporary password locally, and preserve password-change-before-database-configuration. No email delivery/verification service is required initially. Avoid a shared published password or inventing a permanent fake email identity.

### Q141 Administrator-assisted password recovery

Should an authorized administrator reset a local user's password by generating a temporary password shown once, then deliver it privately to that user?

**Recommendation:** Yes. Force password change on the next sign-in and revoke affected sessions/grants as already accepted. No recovery emails or public reset links in phase one. Audit the reset action without recording the password; last-administrator lockout is a separate local recovery path proposed at Q142.

### Q142 Last-administrator recovery

Should an operator with access to the controller's local data/configuration and database credentials have a local CLI recovery command that resets a chosen existing administrator's password without recreating the installation?

**Recommendation:** Yes. Require possession of the installation's existing protected state, generate a one-time temporary credential locally, revoke affected sessions, preserve identity/data and audit recovery. Do not expose this as an unauthenticated network endpoint, grant admin to an arbitrary new identity, or bypass encrypted-state recovery when keys/database access are missing. This is separate from normal signed-in administrator reset.

### Q143 Notice history and dismissal

Should each user's read/dismissed notices remain available in their notification history, while maintenance deadlines and progress continue to appear on affected workspace pages?

**Recommendation:** Yes. Dismissal hides the notice from that user's active list, not other users' lists, and never cancels maintenance. Do not treat a dismissal record as proof the person understood it. Retention, edits and expiration mechanics follow; general administrator notices remain distinct from enforced upgrade schedules.

### Q144 Archived-template provenance

Should an existing workspace continue showing its captured project name and template revision even after the template is archived, while members cannot open the archived template itself?

**Recommendation:** Yes. Preserve workspace provenance and already accepted secret grants without exposing archived-template UI outside the administrator's filtered view. This does not restore launch rights or make the archived template visible in normal catalogs.

### Q145 TLS renewal responsibility

Should ShiftBox automatically renew server certificates issued by its local CA, while administrators remain responsible for replacing certificates they supplied themselves?

**Recommendation:** Yes. Preserve CA trust across ordinary leaf-certificate renewal, validate replacements before activation, and warn about approaching expiry. CA replacement and redistribution of trust remain separate lifecycle work; do not assume public ACME access or automatic external-certificate renewal.

### Q146 PostgreSQL restore destination

Should restoring the same logical installation on its original Mac allow an operator-selected empty replacement PostgreSQL database, recreating and validating ShiftBox-owned schemas/roles before activation?

**Recommendation:** Yes. Permit replacement connection credentials during guarded recovery; do not require access to the failed DB or assume archived DB passwords remain valid. Preserve the accepted administrator review/reconciliation gate. Do not overwrite an unrelated target, migrate workspace disks or broaden this to cross-host controller failover.

### Q147 Private application previews

Should phase-one preview URLs require ShiftBox sign-in and permit only the workspace owner or an administrator exercising the explicit audited connection permission?

**Recommendation:** Yes. No anonymous public links or preview sharing initially. Reuse existing sign-in via narrowly scoped preview access, without sending management credentials to workspace applications. Exact host/cookie/redirect/WebSocket mechanisms are engineering obligations under this boundary, not product PR or IDE integrations.

## Historical round Q123 through Q138

Responses are recorded in the outcome ledger and effective sections. Q128–Q132 reject the managed developer-tool/Git integration proposals; Q135 defers rotation; Q137–Q138 add archive visibility and systemwide notice requirements. Preserve these original questions as history, not current accepted requirements.

### Q123 PostgreSQL layout

Should production use one PostgreSQL database with separate ShiftBox and Dex schemas, each owned by its own scoped service account?

**Recommendation:** Yes. Separate permissions and schema search paths, validate them on fresh connections before activation, and keep server operation external. This simplifies coordinated backup without mixing ownership. Dex's inspected configuration has no schema setting; database-specific PostgreSQL role defaults are the proposed integration, subject to validation. Exact migration and backup tooling follows this layout decision.

### Q124 First-run transition to PostgreSQL

Should initial setup use restricted local SQLite storage until the administrator changes the initial password and chooses the database profile, then transfer only the initial administrator/setup state to PostgreSQL and require a fresh sign-in for production?

**Recommendation:** Yes. Preserve stable owner identity and the changed password; permit no normal accounts/workspaces/templates/secrets before setup completes. Explicit demo mode can finish on SQLite. Validate before activation, invalidate bootstrap sessions, and never silently revert to bootstrap SQLite after PostgreSQL becomes authoritative. General migration of an already-used demo installation is deferred; the constrained bootstrap transfer needs functional validation.

### Q125 Backup maintenance pause

May ShiftBox briefly pause management changes and sign-in activity while capturing a consistent checkpoint of application data, Dex state, keys and configuration?

**Recommendation:** Yes. Drain/pause background mutations as well as UI/API writes, keep workspace VMs running, show progress, and resume once consistent snapshots/local copies are secured. Do not promise a fixed pause duration or imply a single database dump automatically coordinates local files. Exact tools and restore procedure follow.

### Q126 GitHub App permission baseline

Should the GitHub App request Contents and Pull requests read/write, plus Metadata, Checks and Commit statuses read, for developer tools inside workspaces?

**Recommendation:** Yes. Preserve the user's own GitHub rights and repository branch rules. This is a permission baseline to validate against a documented tool command set, not a guarantee that every GitHub CLI operation works and not a ShiftBox PR feature. Actions administration, CI reruns and unrelated organization permissions are not implied.

### Q127 Workflow-file development

Should developers also be able to commit changes under .github/workflows using their linked GitHub credentials?

**Recommendation:** Yes. Request the separate Workflows write permission, subject to administrator/installation approval and the developer's own authority. This does not introduce Actions administration or ShiftBox CI controls.

### Q128 Supported GitHub CLI

Should ShiftBox provide a supported gh installation and a launcher that obtains the current workspace owner's GitHub token for each invocation?

**Recommendation:** Yes. Authenticate developer tools without adding PR management to ShiftBox. Do not save a static token in shell startup files. The platform tooling/environment delivery mechanism remains a later design choice; a command spanning token replacement may fail and must not be advertised as uninterrupted. Exact supported commands require validation.

### Q129 Generated Git credentials versus application secrets

Should generated GitHub credentials use a dedicated broker interface, outside the User → Project Template → Organization secret-name override hierarchy, with refresh tokens and app credentials retained only by the controller?

**Recommendation:** Yes. Guests obtain current owner-authorized access tokens through supported helpers/CLI integration; ordinary application secrets cannot override app credentials or select another GitHub identity. Coordinate refresh across the owner's workspaces and persist token replacements safely; require relinking when refresh is no longer possible. This formalizes the already accepted controller-internal credential boundary, without denying the guest access to the short-lived credential it must use.

### Q130 GitHub operation without webhooks

Should phase one disable GitHub webhooks and discover authorization changes through on-demand API checks, operation failures and an explicit connection-status refresh action?

**Recommendation:** Yes. Keep private-network deployment free of public webhook ingress. GitHub enforces its own revocations; the ShiftBox display may lag until its next check. Do not promise instantaneous UI synchronization or bypass organization app-approval requirements.

### Q131 Git LFS and submodules

Should phase one support GitHub-hosted LFS download/upload and recursive HTTPS submodules on GitHub.com, provided the owner/app can access each repository and all required destinations are permitted by network policy?

**Recommendation:** Yes. Authenticate each repository/destination appropriately. Do not let repository-controlled URLs automatically broaden credentials or network exceptions; unsupported hosts/transports fail clearly. Custom LFS services and SSH submodules remain outside this initial supported integration.

### Q132 Personal fork access

Should administrators configure the GitHub App to allow installation on developers' personal accounts as well as the organization's account, so developers can work with their own forks?

**Recommendation:** Yes. Use selected-repository installations and each developer's authority. An installable-across-accounts GitHub App does not require Marketplace publication or making the ShiftBox controller public. This is developer Git access, not product fork/PR management; ShiftBox does not automatically fork repositories or grant access to repositories the user/app cannot use.

### Q133 Live identity-state protection

Should production protect Dex's live data through restricted access, verified TLS to PostgreSQL and operator-provided encrypted storage, without adding custom field encryption inside Dex in phase one?

**Recommendation:** Yes. Keep application-secret value encryption separate and disclose that authorized database administrators can read Dex's sensitive records. Encrypted backup files alone do not protect the live database. Document the storage prerequisite; demo SQLite must disclose its host-storage protection limits. Do not assume Dex writes pass through ShiftBox's secret-encryption layer.

### Q134 Supplied keys in recovery archives

Should the encrypted recovery archive include the installation-key material required by that archive even when the operator supplied it through a mounted file?

**Recommendation:** Yes. Supplied-key mode is a custody/input option, not a non-exportability guarantee. Protect exported key versions with the independent backup password. On restore, validate matching keys and either recover to a protected destination or use the operator's matching mounted key; never overwrite an existing mounted key silently. Exact restore mechanics follow.

### Q135 Installation-key rotation

Should phase one provide administrator-triggered, resumable rotation of the application-secret encryption key, re-encrypting current protected values without invalidating older recovery archives?

**Recommendation:** Yes. Retain needed key versions during interrupted rotation, verify completion before retiring old live material, and require explicit replacement staging for operator-supplied keys. Existing archives keep their own keys/passwords; rotation does not revoke copied archives or external service credentials. This is separate from Dex signing-key rotation.

### Q136 Local account lifecycle

Should local accounts use email-address sign-in, be created by administrators with a temporary password and mandatory first-login change, and support user password changes plus administrator-mediated recovery, without public registration or email delivery in phase one?

**Recommendation:** Yes. Dex owns credentials; ShiftBox owns authorized UI, membership and setup gates. Account creation includes an explicit role decision rather than granting access merely for authenticating. Password changes/recovery revoke affected ShiftBox sessions and applicable identity grants. Exact initial-owner identifier, recovery delivery and external-identity enrollment follow; no assertion that unmodified Dex provides this complete UX.

### Q137 Archiving project templates

Should removing a template initially mean archiving it: prevent new launches while preserving existing workspaces, pinned template history and existing secret-access grants until those grants are explicitly revoked?

**Recommendation:** Yes. Distinguish archiving from membership removal and secret revocation; do not delete project secrets or user data as an archive side effect. Show that consequence in the UI. Permanent purge and ownership transfer follow as separate lifecycle decisions.

### Q138 Maintenance notifications

Should phase-one scheduled upgrades use an administrator-selected deadline with in-product warnings/countdown, without requiring email, Slack or another notification service?

**Recommendation:** Yes. Show deadline, affected workspaces and progress in the UI; the accepted safe mandatory-maintenance policy applies after the deadline even if a user has not acknowledged the notice. Do not require an acknowledgement that can block upgrades forever. Exact grace defaults, overdue-host handling and maintenance batch concurrency remain open.

## Historical round Q116 through Q122

All seven responses are recorded in the ledger and effective sections. Q120 rejects product PR management and narrows the requirement to permissions for developer workspace tools. Other recommendations were accepted. Original question wording is preserved below for provenance.

### Q116 Portable controller release scope

Should the first tested UTM deployment remain a native Mac controller, with portable controller/key-storage design now and Linux/container production deployment introduced with the EC2 phase?

**Recommendation:** Yes. Keep UTM beside its native Mac controller initially; do not imply a Linux container can directly control UTM. If Linux/container production is required in phase one, explicitly expand the provider/host-adapter deployment design. Portable key storage is required immediately regardless of rollout timing; Windows support is not silently added.

### Q117 Production database ownership scope

Should PostgreSQL store both CloudBox management/application state and embedded Dex identity state in production, with SQLite supporting both in demo/very-small mode?

**Recommendation:** Yes. Use consistent database profiles and separate owned schemas/stores for CloudBox and Dex; exact database-versus-schema layout follows. PostgreSQL server installation/operation stays external, and PostgreSQL support does not imply multiple active controllers or HA. Do not treat this as already accepted solely because Q115 originally concerned Dex.

### Q118 Database seeding authority

Should UI/CLI setup accept an existing empty administrator-provided PostgreSQL target, and optionally create CloudBox's database/roles when the supplied bootstrap account has the required privileges, discarding those bootstrap credentials afterward?

**Recommendation:** Yes. Store only ongoing service credentials scoped to CloudBox-owned database objects; never retain a general PostgreSQL administrator/superuser credential. Dex's startup migrations need ownership/DDL rights in its own namespace, so do not promise DML-only runtime privileges without a separate integration. Validate and seed before activating connection configuration; retries must not overwrite unrelated data or reset passwords. CloudBox does not deploy or upgrade the PostgreSQL server.

### Q119 Portable encryption key

Should CloudBox generate a unique installation encryption key in protected persistent application storage, outside the database, with an option for an operator-supplied mounted key file?

**Recommendation:** Yes. Enforce platform-appropriate access permissions, persist keys outside disposable container layers, and include required recoverable key material in the password-encrypted management archive. No mandatory Keychain/KMS, hard-coded shared key or raw-key command-line argument. Missing/wrong keys for existing ciphertext cause a clear failure, never silent replacement. This protects database-only exposure, not a compromised controller account/host root. Detailed rotation and externally managed-key backup policy follow.

### Q120 First PR workflow

Should phase-one PR support let a user select a PR to launch a workspace, check out its head, edit/push where GitHub allows, create a PR from workspace changes, and see status/checks plus a GitHub link, while review conversations, approvals and merging stay on GitHub initially?

**Recommendation:** Yes. Define this bounded first workflow; decide same-repository versus fork coverage and CloudBox UI versus authenticated CLI mechanics afterward. PR refs are read-only: updating an existing PR requires pushing its actual head branch with appropriate authority. Do not silently promise CI reruns or full GitHub review UI.

### Q121 Removing a secret override

With User → Project Template → Organization settled, should deleting a higher-priority value reveal the next authorized value with that key, while an explicitly saved empty value remains an override rather than being treated as missing?

**Recommendation:** Yes. Show scope/fallback consequences before deletion without redisplaying values. Project access revocation removes that project source from resolution immediately; it does not remove independently authorized user/organization sources. A deleted override must not look like revocation of every value with that key.

### Q122 Backups with external PostgreSQL

Should the CloudBox encrypted management archive still include its owned application/identity data and recovery configuration when PostgreSQL is externally operated, while excluding the server itself and unrelated databases?

**Recommendation:** Yes. External PostgreSQL deployment/administration remains the operator's responsibility; CloudBox preserves the agreed recovery contract for its owned state. Coordinated snapshot, service-role restoration and portable restore-target rules follow the selected storage/deployment scope. Do not imply that a single pg_dump automatically coordinates separate databases and local keys/files, or that this includes workspace disks.

## Historical round Q106 through Q115

Responses are recorded in the ledger/effective sections. Q108 adds PR support, Q111 removes workspace secret scope, Q114 rejects Mac-only custody, and Q115 requires PostgreSQL production support. Original recommendations are preserved for history, not treated as accepted where superseded.

### Q106 HTTPS and Next.js deployment choice

Should CloudBox use Go's HTTPS server to serve embedded Next.js static-export assets and its live APIs, accepting export-compatible client routing? The alternative is Go HTTPS reverse-proxying a supervised Node/Next standalone server, which enables normal request-time Next features but adds a runtime/process and extracted application files.

**Recommendation:** Yes to Go HTTPS plus embedded static assets, because no settled dashboard requirement currently requires a Next runtime and simplicity is the priority. Static content still runs a live React application using Go APIs. Choose the Node/Next alternative if ordinary dynamic App Router routes, SSR or Server Actions are worth the additional runtime. Do not replace Next's Node environment with an ad hoc embedded JS engine. This is a reopened choice, not an assertion that Q80 already settles it.

### Q107 GitHub application type

Should phase one register a GitHub App and use its browser-based OAuth user-authorization flow, rather than use a separately registered OAuth App?

**Recommendation:** Yes. GitHub Apps offer finer permissions and installation repository selection. Both app types support OAuth; requesting OAuth does not require an OAuth App registration. Installation and user authorization are separate steps.

### Q108 Whose GitHub authority applies

Should developer clone/fetch/push operations use the workspace owner's linked GitHub identity and remain constrained by that developer's GitHub permissions, rather than gain repository access solely because a CloudBox template is shared?

**Recommendation:** Yes. Template launch permission is not a grant of GitHub repository membership. With a GitHub App this favors user access tokens, whose authority intersects the app's and user's permissions. Such a token can still cover multiple allowed repositories; a helper does not narrow its upstream authority. Git commit author metadata is a separate concern.

### Q109 Initial GitHub coverage

Should phase one support GitHub.com over HTTPS for clone/fetch/push, deferring SSH transport, GitHub Enterprise Server and other Git providers?

**Recommendation:** Yes. Keep Git helper integration focused. Explicitly settle LFS/submodules, workflow-file changes and read/write requirements afterward; do not silently treat them as accepted or excluded by basic Git support.

### Q110 Application registration ownership

Should each CloudBox installation use an administrator-owned GitHub application registration, rather than depend on a centrally operated CloudBox GitHub application/service?

**Recommendation:** Yes. Accept one-time registration/installation setup to preserve self-hosting and separate trust boundaries. This applies whichever app type is selected. Callback URLs, organization approval, webhook needs and bootstrap UX follow. CloudBox admin status does not bypass GitHub organization policy.

### Q111 Complete secret precedence

Should the effective secret-name hierarchy, highest priority first, be user → individual workspace → project template → organization?

**Recommendation:** Yes. This preserves the user's stated personal-over-workspace-over-organization rule and places the newly retained project scope between workspace and organization. Expose the winning scope in management metadata without revealing its value. Deletion/fallback, empty values, reserved generated credential names and explicit scope lookup follow only after the hierarchy is settled.

### Q112 Identity used for in-container secrets

Should a workspace's secret lookup always use its recorded owner's user context, its own workspace/project, and its organization, even when an administrator connects to it?

**Recommendation:** Yes. Resolve context from controller-owned records and VM identity, not a caller-supplied user ID or the latest browser connection. An administrator connection must not introduce the administrator's personal secrets into the guest. As already accepted, connecting admins can see owner secrets retrievable within that guest.

### Q113 Revoking template use

Should removing someone's template-use permission immediately prevent new launches and new retrieval of that project's secrets from their existing workspaces, while leaving those owner-private workspaces and retained files intact?

**Recommendation:** Yes. Do not stop/delete an owned workspace merely because its template sharing was removed. Already-delivered values remain exposed and require upstream rotation to invalidate. This is distinct from full organization membership disablement, whose accepted policy stops owned workloads.

### Q114 Installation encryption-key custody

Should the Mac store CloudBox's random application-secret encryption root key in an application-owned, non-synchronizing Keychain item, and include recoverable key material only inside the independently password-encrypted management backup?

**Recommendation:** Yes. Restore imports the verified application key through Keychain APIs on the accepted original-Mac recovery path; do not copy the entire login Keychain. Missing/locked/denied key access blocks secret-dependent operations and requests local unlock/recovery, never creates a replacement over existing encrypted data. Exact Keychain type/code-signing integration and key rotation follow.

### Q115 Embedded Dex persistence

Should embedded Dex use a separate SQLite identity database under an explicit CloudBox-owned support/validation commitment, despite Dex's documented warning against production SQLite?

**Recommendation:** Yes for the initial single-controller product, conditional on functional durability, migration, restart and coordinated backup/restore acceptance before declaring it supported. This is a support exception, not an upstream recommendation or evidence that it already works. PostgreSQL is the alternative if following upstream storage guidance outweighs another service. Keep schemas separate; CloudBox application-value encryption does not automatically encrypt Dex signing keys/tokens, and live identity-state protection remains a separate open decision. Load tests remain deferred as requested.

## Historical round Q93 through Q105

Responses are recorded in the ledger and effective sections. Q93, Q94, Q102 and Q104 reject or reopen original recommendations; other questions were accepted. The original wording below is historical, not the current decision.

### Q93 Initial Git credential integration

Should phase-one private Git access use explicitly bound, user/admin-supplied credentials through the secrets service, with narrow repository permissions configured at the Git provider, while automatic Git App/OAuth credential brokering is deferred?

**Recommendation:** Yes for simplicity. CloudBox enforces which workspace receives a credential, but must not claim it can narrow an already broad personal token's upstream permissions. Keep identity-provider login separate from Git credentials and do not forward host credentials automatically. Supported Git transports/providers and read/write behavior follow.

### Q94 Next.js static deployment contract

Should Next.js be strictly a static UI build, with all runtime authentication, authorization, APIs and installation configuration owned by Go? Runtime-created workspace IDs need export-compatible client routing rather than build-time generation of every workspace page.

**Recommendation:** Yes. No Next server runtime, Server Actions or request-time middleware dependency. Use a fixed exportable route with an ID parameter initially; fetch installation configuration from Go rather than baking it into JavaScript. Build-time rendering must contain no installation secrets.

### Q95 Private-network names and certificates

Should the administrator supply a base DNS domain resolving the management/workspace/preview names to the Mac, and either a trusted wildcard certificate or a CloudBox-managed local CA whose trust they install on client machines?

**Recommendation:** Yes. Use a documented wildcard naming layout with separate origins and explicit private CA trust distribution. Avoid making localhost-only names, public tunnels or automatic public certificate issuance prerequisites. Exact labels, cookie/grant boundaries and certificate renewal follow.

### Q96 Secrets during container builds

Should phase-one secret delivery be limited to runtime and Git setup, with application secrets during Docker image builds deferred? Build-time secrets are a separate channel; ordinary build arguments or image environment variables can persist credentials in images.

**Recommendation:** Yes. If build-time access is required now, it needs explicit BuildKit secret bindings and its own authorization/exposure contract. Runtime delivery alone must not silently expose organization/user secrets to every image build.

### Q97 Scheduled maintenance behavior

Should a scheduled upgrade give users a visible deadline to update themselves, then automatically start maintenance without further user approval: boot management services only for stopped workspaces, upgrade, and return them to stopped; keep previously running workloads running where possible?

**Recommendation:** Yes. Restore the intended state while respecting newer explicit user/admin actions. Maintenance launches remain subject to capacity limits, do not count as user activity, and must not race inactivity deletion. The logged-in awake host requirement still applies; overdue maintenance must be reconciled visibly.

### Q98 Meaning of forcing an upgrade

Should the deadline end user postponement but preserve the accepted safe-activation rule: block new conflicting lifecycle operations, wait for a safe boundary, and report a hung/unsafe operation for administrator intervention rather than automatically force-kill the VM?

**Recommendation:** Yes. Authorize mandatory maintenance, not unbounded user postponement or unapproved power-off. Arbitrary hooks can hang; a timeout does not make interruption safe. Automatic cancellation/forced shutdown, if desired, needs an explicit data-loss/interruption policy.

### Q99 Failed maintenance and controller advancement

If mandatory maintenance still fails, should the next controller upgrade remain blocked until explicit repair/retry or deliberate workspace retirement?

**Recommendation:** Yes. Keep the one-release compatibility gate and suppression of retries of the same failed artifact. A deadline can end user deferral, but cannot prove a broken update succeeded; no automatic deletion or quarantine exception is implied.

### Q100 Project template revisions

Should each launch create an independent owner-private workspace from a recorded template revision, capturing repository/configuration, resolved size and binding references, with later template edits affecting new workspaces only?

**Recommendation:** Yes. Multiple users can launch multiple independent workspaces from a shared template. Sharing the template does not share a running workspace. Snapshot configuration and references, not secret values; rotation and revoked permissions remain separate live policies.

### Q101 Template ownership and sharing permissions

Should users create/manage their own templates, grant selected members permission to launch them, and reserve editing and project-secret management to the template owner and administrators initially?

**Recommendation:** Yes. Keep use and edit permissions separate; defer collaborative template editors and public/anonymous sharing. Shared users can launch but cannot silently change the configuration or project secrets used by others.

### Q102 Project scope versus individual workspace scope

Should project-template secrets replace the previously proposed per-workspace secret store in phase one, leaving organization, project-template and user secret scopes?

**Recommendation:** Yes for simplicity. Each workspace references the approved secrets from these scopes; defer per-workspace secret-value overrides. This is a proposed simplification, not an assumption inferred from the new template requirement.

### Q103 What sharing grants for secrets

Should permission to use a shared template explicitly include use of its bound project secrets, while personal-secret requirements resolve against the launching user's own secrets rather than the author's?

**Recommendation:** Yes. Clearly show that project-secret use grants access to the value through the resulting workspace. Store references/requirements in the template, not values in exported configuration. Require an authorized shared source for nonpersonal bindings; sharing does not transfer the author's personal credentials. Initial Git access must be resolvable before cloning and reading devcontainer.json.

### Q104 In-container secret CLI authority

Should `cloudbox secrets get-secret <name>` return only an approved secret alias for that workspace, rather than every secret belonging to its owner or organization?

**Recommendation:** Yes. The guest handles technology-specific injection/helpers and local CLI delivery; the controller checks allowed scope/bindings. Automatic helpers and devcontainer.json declarations cannot grant themselves additional authority. Output the requested value on stdout, errors on stderr, and no secret values in audit/diagnostic logs.

### Q105 Secret reads during controller outages

Should on-demand CLI/helper requests fetch the current authorized value and fail clearly when the controller is unavailable, without a persistent offline cache for those requests?

**Recommendation:** Yes for phase one. Existing workloads and already-injected values can continue; new reads/Git authentication may fail during an outage. Removing a binding stops future authorized retrieval but cannot retract copied values. Environment consumers need restart/reload to observe changes; do not promise automatic refresh of already-running processes.

## Historical round Q84 through Q96

Questions are preserved as asked. Q84–Q92 responses are recorded in the ledger/effective sections. Q86, Q89–Q91 expand or modify the original proposals. Q93–Q96 remain unanswered and are carried forward. Original recommendations are not automatically accepted.

### Q84 Dex-owned credentials and forced setup

Should “no local identity” mean no separate CloudBox credential database: embedded Dex owns the local administrator credentials, while CloudBox supplies a restricted first-login password-change screen and privately calls Dex's management API? Dex calls this its local connector; inspected upstream has no must-change-password flag. CloudBox would deny normal APIs/proxy access until setup completes; this does not claim Dex independently enforces the gate for arbitrary other OIDC clients.

**Recommendation:** Yes. Keep password hashes in Dex, only setup/authorization state in CloudBox, and enforce the setup gate server-side. Later decide email login, additional-account creation/recovery, Dex persistence and embedding support.

### Q85 Initial administrator credential

Should the default administrator receive a unique randomly generated temporary password per installation, delivered once through a local setup command/protected local file, instead of a shared published default password?

**Recommendation:** Yes. Force replacement before normal use and never reset it automatically on restart or restore. Exact identifier and recovery procedure follow Q84.

### Q86 One-version compatibility and upgrade gate

Should one version mean the immediately preceding published CloudBox product release, with controller and guest sharing release IDs? Before advancing the controller, every retained workspace must already be confirmed at the currently deployed release, including stopped, unreachable, pending and rolled-back workspaces. A stopped workspace may lag after one controller update, but then blocks the next until updated or explicitly retired. Old catalog images also must pass compatibility admission.

**Recommendation:** Yes. Support N and N−1, upgrade sequentially, track protocol revisions separately, and never start or delete a workspace merely to clear the gate. Compatibility with retained platform OS/tool baselines is also required; ordinary guest reports are operational evidence, not attestation.

### Q87 Safe guest update activation

Should the next poll begin the update process, with activation waiting for an in-progress provisioning/rebuild operation to reach a safe boundary? Running containers should continue, but restarting the guest service may briefly disconnect the browser/proxy tunnel.

**Recommendation:** Yes. Stage and authenticate the release, use independent supervision and recoverable state, and verify new-service health before committing. Do not interrupt lifecycle operations or promise uninterrupted browser connections.

### Q88 Failed update retry policy

After falling back, should the guest stop automatically retrying that same failed artifact until an administrator explicitly retries or supplies a corrected compatible artifact?

**Recommendation:** Yes. Keep the error and controller-upgrade block visible; avoid a restart loop on every poll. Transient download/connectivity failures can have bounded backoff separately from a failed activation.

### Q89 Which secrets a workspace receives

Should organization and user scopes define availability, while each workspace explicitly binds the particular secrets it receives, rather than inheriting every eligible value automatically? Organization secrets remain available to all members, but a compromised workspace would expose only its selected set.

**Recommendation:** Yes. The controller authorizes bindings using its stored workspace owner/membership and authenticated VM identity; a guest-supplied user or organization ID cannot widen access. Secret aliases/collisions and default binding templates follow this decision.

### Q90 Secret management permissions

Should admins manage organization secrets, users manage their own personal secrets, and workspace owners plus explicitly authorized admins manage workspace secrets? Should saved values be replaceable/deletable but not redisplayed in the ordinary UI/API?

**Recommendation:** Yes. Audit metadata/actions without values. Do not imply this hides delivered secrets from admins who already have permission to connect to a workspace, or from the guest itself. Personal-secret scope does not grant other users access.

### Q91 Runtime secret exposure

Should runtime secret delivery default to read-only files mounted into the devcontainer, with environment-variable injection available only when explicitly selected for an application that needs it?

**Recommendation:** Yes. Keep managed copies outside Git and retained project/home areas, redact diagnostics, and document that either method exposes the value to the receiving workload and a compromised VM. Consumer reload, refresh and revocation semantics follow this choice; removing a local copy cannot retract a stolen value.

### Q92 Encrypting secrets in SQLite

Should phase one encrypt individual secret values before storing them in ordinary SQLite, rather than require whole-database encryption? Names, scopes and audit metadata would remain readable to someone possessing the database; the full backup remains encrypted.

**Recommendation:** Yes. Use authenticated application-level encryption with contextual binding and key versions. Decide master-key storage/unlock/rotation next; do not store an unprotected key beside the encrypted values or imply at-rest encryption protects a running compromised controller.

### Q93 Initial Git credential integration

Should phase-one private Git access use explicitly bound, user/admin-supplied credentials through the secrets service, with narrow repository permissions configured at the Git provider, while automatic Git App/OAuth credential brokering is deferred?

**Recommendation:** Yes for simplicity. CloudBox enforces which workspace receives a credential, but must not claim it can narrow an already broad personal token's upstream permissions. Keep identity-provider login separate from Git credentials and do not forward host credentials automatically. Supported Git transports/providers and read/write behavior follow.

### Q94 Next.js static deployment contract

Should Next.js be strictly a static UI build, with all runtime authentication, authorization, APIs and installation configuration owned by Go? Runtime-created workspace IDs need export-compatible client routing rather than build-time generation of every workspace page.

**Recommendation:** Yes. No Next server runtime, Server Actions or request-time middleware dependency. Use a fixed exportable route with an ID parameter initially; fetch installation configuration from Go rather than baking it into JavaScript. Build-time rendering must contain no installation secrets.

### Q95 Private-network names and certificates

Should the administrator supply a base DNS domain resolving the management/workspace/preview names to the Mac, and either a trusted wildcard certificate or a CloudBox-managed local CA whose trust they install on client machines?

**Recommendation:** Yes. Use a documented wildcard naming layout with separate origins and explicit private CA trust distribution. Avoid making localhost-only names, public tunnels or automatic public certificate issuance prerequisites. Exact labels, cookie/grant boundaries and certificate renewal follow.

### Q96 Secrets during container builds

Should phase-one secret delivery be limited to runtime and Git setup, with application secrets during Docker image builds deferred? Build-time secrets are a separate channel; ordinary build arguments or image environment variables can persist credentials in images.

**Recommendation:** Yes. If build-time access is required now, it needs explicit BuildKit secret bindings and its own authorization/exposure contract. Runtime delivery alone must not silently expose organization/user secrets to every image build.

## Historical round Q73 through Q83

The following preserves the questions as asked. Responses are in the outcome ledger and effective sections above. Q73, Q74, Q76, Q79 and Q80 changed the proposals; do not treat original recommendations as accepted.

```text
Go + product-managed OIDC -> Q73 local accounts; Q74 MFA requirement
  -> later: identity engine/storage, first-owner setup and recovery factors
Encrypted manual backups -> Q75 recovery-passphrase custody
  -> later: archive format, coordinated snapshots and restore procedure
Frozen workspace platforms -> Q76 controller upgrade compatibility
  -> later: protocol/version contract and compatibility acceptance
Standard Dev Containers + browser IDE -> Q77 connection readiness
  -> later: states, progress and functional readiness evidence
Retained files + guest agent -> Q78 project-failure repair
  -> later: trusted repair artifact and diagnostics
Untrusted guest + private repositories -> Q79 Git authority
  -> later: providers, user attribution and credential/relay mechanism
Go service distribution -> Q80 browser UI stack
  -> later: frontend build, accessibility and visual design
User ingress through proxy -> Q81 browser origins
  -> later: hostname, TLS and grant-cookie details
Multiuser Mac installation -> Q82 private-network access
  -> later: setup, DNS/certificates and deployment checks
VM-provider abstraction -> Q83 adapter packaging
  -> later: exact interfaces, capabilities and error/reconciliation contract
```

### Q73 Initial local account model

Should phase-one local identity use administrator-created username/password accounts, self-service password changes and administrator-mediated password recovery, without public self-registration or email-delivery infrastructure? Password changes/recovery must explicitly invalidate affected login/proxy sessions rather than assume a credential update does it automatically. Membership and workspace permissions remain separate from the identity account.

**Recommendation:** Yes. Start with this bounded local-account contract and admin-mediated recovery. MFA requirements are independently proposed in Q74; factor/recovery mechanics and initial-owner bootstrap follow engine selection.

### Q74 Administrator MFA requirement

Should local administrator accounts require multi-factor authentication in phase one, with MFA optional for members? This states the required assurance, not a choice of TOTP/passkeys or identity engine. External OIDC assurance mapping is a separate integration contract.

**Recommendation:** Yes. Require MFA for local administrators and offer it to members. Choose the factors and recovery process afterward; do not assume an engine's recently added or experimental features satisfy the requirement without validation.

### Q75 Backup recovery passphrase

For operator-triggered backups, should each archive be encrypted using an operator-supplied recovery passphrase that CloudBox does not retain? The administrator must keep it independently; losing it loses the supported recovery path for that archive. A Mac-keychain-only key would instead tie recovery to retaining that keychain material.

**Recommendation:** Yes. Use a separately retained passphrase for manual backups and an established authenticated streaming format. Keep credentials/inventory inside the encrypted payload and verify the full archive before committing a restore. Automated unattended backups and alternate recipient/key schemes remain outside this initial choice.

### Q76 Controller upgrades and frozen agents

Should a controller upgrade be blocked when it would make any retained workspace's agent protocol unsupported, including stopped VMs? The administrator could keep the existing controller or deliberately retire/recreate affected workspaces. Automatic agent replacement would contradict platform pinning.

**Recommendation:** Yes. Check compatibility before upgrade and preserve running workloads. Maintain an explicit supported-protocol contract without promising every old platform is supported forever. Never silently upgrade a guest or strand it behind an incompatible controller.

### Q77 Browser connection readiness

Should the Open IDE action become available only when the authenticated guest, project container, required Dev Container waitFor stage and browser IDE path through the proxy are ready, while any later setup continues with separate visible status? The standard waitFor default can allow connection before all hooks finish; a TCP/HTTP connection alone is not proof of a usable IDE.

**Recommendation:** Yes. Honor the repository's standard waitFor connection gate and show remaining setup progress/failure separately. Preserve lifecycle hook ordering and distinguish agent reachability, connectability and completed setup.

### Q78 Repair after project failures

Should configuration, image/build, required-hook or IDE failures preserve retained data and diagnostics and offer an explicit Repair mode when the VM/guest service is still healthy? The repair environment must be platform-supplied, known-good and usable without running the failed repository's hooks, automatic tasks or extensions. It must not depend on an unapproved network download.

**Recommendation:** Yes. Offer an opt-in trusted repair environment for editing the retained project and retrying. Do not reset Git, delete the VM or mark normal startup successful as a repair shortcut. VM/agent failures remain separately diagnosed.

### Q79 Repository-scoped Git authority

Should CloudBox provide each workspace access only to explicitly approved repositories, with separate read/write authorization, instead of forwarding the user's broad host Git identity? Broad upstream credentials/refresh tokens should remain outside the untrusted VM where possible; enforcement may use provider-scoped credentials or a bounded controller-side operation.

**Recommendation:** Yes. Scope CloudBox-granted Git authority by repository and operation, and recheck current authorization. This is not a claim that network rules prevent all public Git access or that users cannot introduce their own credentials. Provider selection, user attribution and credential transport follow.

### Q80 Browser UI technology

Should the greenfield browser UI use React and TypeScript, compiled into static assets served by the Go controller? Node-based tools would be build dependencies rather than a required UI server process on the operator's Mac. This does not mean migrating existing application code.

**Recommendation:** Yes. Use a React/TypeScript UI embedded in the Go distribution. Decide component library, frontend build tool, visual design and accessibility acceptance separately. **Later outcome:** the user explicitly selected Mantine after Q155; the library choice is now settled.

### Q81 Separate browser origins

Should the management UI, each workspace IDE and each preview service use separate browser origins? User-controlled workspace content must not share the management UI's origin or its credentials. This can use wildcard routing to one proxy rather than a separate service or DNS record for every workspace.

**Recommendation:** Yes. Separate these browser security boundaries and scope credentials to the intended service. Choose exact hostnames, certificates and cookie/grant mechanics afterward.

### Q82 Private-network multiuser access

Should phase one support browser access from other machines on an administrator-configured LAN/private network, using configured HTTPS names and trust, while retaining localhost development access? This does not include operating a public tunnel or automatic Internet exposure.

**Recommendation:** Yes. Make private-network access an explicit deployment capability. Administrators provide suitable networking/DNS/TLS configuration; decide the supported setup flow next. Native UTM still requires the agreed logged-in awake Mac.

### Q83 Provider adapter packaging

Should initial VM providers be compiled into the Go product behind an internal lifecycle/capability interface, rather than loaded dynamically as third-party plugins at runtime? UTM is first and EC2 second; hosting and guest protocol remain separate concerns.

**Recommendation:** Yes. Start with compiled adapters and an explicit contract. Defer a public plugin SDK, runtime code loading and remote provisioning workers until a concrete deployment requirement needs them. This keeps provider independence without promising arbitrary plugin compatibility immediately.

## Accepted language decision Q67

**Q67 is accepted: Go for both controller and guest services.** The options below preserve the researched alternatives. At Q67, Go did not imply an identity/UI choice; Q73 later selected embedded Dex and Q80 selected React/Next.js/TypeScript static export.

### Q67 Service language after comparative research

Should we use Go for both the controller and guest services, or select one of the researched alternatives below?

- **Go — recommended:** strongest overall fit for simpler service implementation, native delivery and potential in-process Dex integration. Native SQLite/Dex dependencies still affect cross-builds.
- **Rust:** strongest choice when ownership guarantees and direct allocation control justify more integration/build complexity, especially with an experienced Rust team.
- **C#/.NET:** strong application/proxy framework and identity-library ecosystem; choose when those benefits outweigh deployment-mode and NativeAOT constraints.
- **TypeScript with Node.js/Bun:** credible executable packaging and potential shared language with the UI; choose when team productivity in TypeScript outweighs runtime/dependency and event-loop considerations.

**Recommendation:** Yes means Go for both services. The user may instead name Rust, C#/.NET or TypeScript. Prefer one service language initially unless a concrete requirement justifies a mixed controller/agent stack. Identity selection follows this decision.

## Round Q66 through Q72 responses

The user accepted Q66 and Q68–Q72, then requested language research for Q67 and subsequently selected Go for both services. Identity-engine selection still follows its own requirements.

### Q66 Minimal platform image

Should phase one use **Ubuntu 24.04 LTS Minimal amd64** as the Packer input, pinned to a specific image revision/digest? It is the smallest published QCOW2 download among the inspected Debian/Ubuntu candidates and has standard security support through May 2029. Ubuntu 26.04 Minimal offers a longer support window but a larger current artifact. No exact-stack benchmark establishes a fastest option.

**Recommendation:** Yes. Choose Ubuntu 24.04 LTS Minimal for the initial footprint-first platform; keep amd64 emulation unchanged. Validate the complete tooling image after design approval, without treating download size as RAM or boot-speed evidence.

### Q67 Greenfield service language

Should the new project use **Go for the controller and guest services**, producing the appropriate macOS controller and Linux amd64 guest builds? This is a new-project choice, not a requirement to reuse legacy code. In-process Dex is a viable candidate only if a compatible implementation language/integration is chosen.

**Recommendation:** Yes. Use Go for these services while keeping OIDC as the authentication protocol boundary. UI technology, new repository location and scaffolding remain separate decisions; do not create the new project yet.

### Q68 Installation-owned local identity

Should a new installation provide **product-managed local identity by default**, while retaining the ability to configure an external OIDC provider? This would replace the prior requirement to bring a separately administered Keycloak service. It does not yet choose Dex, Keycloak, or same-process versus supervised-service packaging.

**Recommendation:** Yes. Make local identity part of the product experience and retain external OIDC as an option. Select the engine after settling its account-management, recovery and storage requirements; embedded Dex is a promising candidate, not an approved implementation.

### Q69 Built-in backup encryption

Should we replace the prior plaintext-export decision with **built-in encrypted management backups**? The encryption recovery mechanism must remain usable after loss of the installation; its only usable key cannot exist solely inside the backup it encrypts.

**Recommendation:** Yes. Make encryption part of the backup contract. Decide recovery-key/passphrase custody and coordinated identity snapshot contents after the identity architecture is chosen. A single archive need not imply one process or database.

### Q70 Minimal VM disk layout

Given that existing workspace platforms are not upgraded in place, should phase one use **one persistent, VM-owned disk** containing the OS and managed project/home/application-data areas? Retained mounts remain outside the disposable devcontainer layer. The disk stays with the workspace through reboot/stop/rebuild and is removed on verified VM destruction.

**Recommendation:** Yes. Start with one owned disk; defer a separate data disk until an explicit OS-replacement or data-migration requirement justifies it. This does not expand the retained-data guarantee beyond designated areas.

### Q71 Project-container OS scope

Independently of the platform VM image, should phase-one browser workspaces require **glibc-based amd64 Linux project containers**, with Debian/Ubuntu reference cases, and defer Alpine/musl project-container support? The platform OS change does not change a project container's userspace. Standard prebuilt browser-IDE components have a simpler glibc installation path; musl needs separate support.

**Recommendation:** Yes. Use glibc-based project containers initially and make the compatibility limit visible. This is a new scope confirmation: the earlier Q57 response did not settle it. The greenfield implementation is not obliged to reuse the legacy browser installer.

### Q72 T-shirt profile revisions

Should a workspace capture the actual CPU/memory/disk values of its selected T-shirt size when created, so an administrator later editing Small/Medium/Large/XLarge changes future workspaces rather than silently changing existing ones?

**Recommendation:** Yes. Pin resolved resource values per workspace and use those values for reservations. An explicit resize workflow, if later supported, is a separate operation; editing a size definition does not resize existing VMs.

## Round Q55 through Q65 responses

Responses were received November 13. Original questions are preserved as history: Q55/Q56/Q58–Q60/Q63–Q65 accepted; Q57 changes platform OS and requests research; Q61 accepts owned recovery state but reopens identity packaging; Q62 accepts guarded restore and conditionally proposes encrypted identity-inclusive backup. Effective decisions and the ledger override superseded wording below.

```text
Imported artifacts -> Q55 acceptance contract
  -> later: import validation, publication and cache cleanup
Versioned platform images -> Q56 existing-VM upgrade policy
  -> later: physical disk layout and controller/agent compatibility window
Browser IDE + Dev Container forms -> Q57 project-container OS support
  -> later: installer paths and functional acceptance matrix
Capacity limits -> Q58 reservation accounting
  -> later: resource values and operation races
Idle monitors -> Q59 retention clock
  -> later: thresholds, freshness, warning and recovery-grace values
Per-VM mTLS enrollment -> Q60 credential renewal/recovery
  -> later: lifetimes, bootstrap cleanup and revocation tests
External Keycloak + management backup -> Q61 archive recovery boundary
  -> later: snapshot consistency and archive format
Management restore -> Q62 initial target and recovery gate
  -> later: detailed restore validation and reconciliation
Restricted network exceptions -> Q63 Git/DNS scope
  -> later: destination resolution and provider capability validation
Network policy updates -> Q64 stopped-only changes
  -> later: apply/verify failure behavior and profile revisions
Membership approval -> Q65 offboarding behavior
  -> later: identity-provider event propagation and audit/permission details
```

### Q55 Platform artifact acceptance

Should the catalog accept only self-contained platform bundles with a release manifest identifying architecture, platform version, agent protocol version and expected content digests? Persistent disks must belong to the bundle rather than reference arbitrary external files or backing images. The administrator explicitly approves the source; a checksum alone does not establish publisher trust.

**Recommendation:** Yes. Require this artifact contract and preserve an immutable catalog copy, separate from writable workspace copies. Reject structurally incompatible or externally dependent bundles rather than importing arbitrary UTM machines as workspace templates.

### Q56 Existing VM platform upgrades

Should new platform-image releases affect only newly created VMs in phase one, with existing VMs retaining their original platform image? A devcontainer rebuild would not upgrade Alpine or the guest service. Moving an existing workspace to a new platform image would require deliberate recreation after the developer persists the necessary work; there is no automatic OS migration or platform replacement.

**Recommendation:** Yes. Keep existing VMs pinned and make replacement explicit, with its established data-loss consequence. Do not imply that publishing an image patches existing VMs. Controller/agent compatibility and maintenance constraints must then be defined.

### Q57 Project container operating systems

Should phase one require glibc-based amd64 Linux project containers, using Debian/Ubuntu-based reference cases, while retaining Alpine for the platform VM? The existing code-server standalone installer targets glibc; an Alpine project container requires a separate native/npm installation path.

**Recommendation:** Yes. Start with glibc-based project containers and defer Alpine/musl project-container support. This does not change the selected Alpine platform OS. Unsupported project environments should receive a clear compatibility error.

### Q58 Capacity reservation accounting

Should CloudBox reserve configured CPU/RAM capacity when accepting a start, release it only after confirmed stop or deletion, and account for workspace disk allocations until verified deletion? Uncertain operations keep their reservations until reconciliation establishes the actual resource state.

**Recommendation:** Yes. Base admission on durable reservations for CloudBox-owned VMs against administrator-configured budgets, leaving host headroom under administrator control. Do not infer successful stop/deletion from a timeout or lost response. This is accounting, not a performance guarantee.

### Q59 Continuous inactivity clock

Should a fresh report of selected activity reset the inactivity deadline, while ordinary idle polls do not? A continuous idle period would trigger the configured retention action; stopped VMs continue counting toward it. If polling ceases, stale activity cannot keep extending the deadline: the missing-poll rule uses controller-observed contact time, preserving an earlier idle start. Known controller/host outages retain the agreed reconciliation/grace safeguard.

**Recommendation:** Yes. Use a controller-owned deadline and expose it in the UI. Keep heartbeat freshness, startup grace, recovery grace and retention duration explicit/configurable; successful polling alone is not activity.

### Q60 Agent credential renewal and recovery

Should connected agents renew their per-VM certificates automatically before expiry, while expired or revoked credentials cannot renew themselves? Recovery would use a new controller-authorized, provider-delivered enrollment credential for the known VM, without trusting a guest's self-asserted identity. Revocation closes its polling and proxy access; VM stop/delete remains available through the host-side provider.

**Recommendation:** Yes. Automate ordinary renewal and use host-mediated re-enrollment for recovery. Never silently accept an expired credential or a revoked certificate as sufficient authority to restore access. Exact lifetimes, recovery authorization and delivery remain to be specified.

### Q61 Self-contained CloudBox management backup

Should the unencrypted management archive include CloudBox's database, configuration, OIDC client configuration/secret, and the private/key material required to recover its stored secrets and VM trust? It would exclude VM disks and the independently operated Keycloak server/database. Its purpose is to recover CloudBox-owned state; Keycloak and workspace data remain separately preserved by their owners.

**Recommendation:** Yes. Make the archive self-contained for CloudBox's own recovery dependencies, including necessary decryption/trust keys. Keep archive encryption an external administrator operation as already decided. Document the separate Keycloak recovery requirement.

### Q62 Restore target and recovery gate

Should phase-one restore target the same logical installation on its original Mac and enter an administrator-reviewed recovery mode? Restored user sessions and outstanding enrollment credentials would not become usable automatically; mutations, automatic deletion and workspace access remain gated while the administrator reviews membership and reconciles surviving UTM resources and agent identities. Guest computation can continue.

**Recommendation:** Yes. Restore into recovery mode and resume explicitly after reconciliation. Defer controller-host migration and automatic cross-host failover. An old archive cannot know revocations or deletions made after it was created; never describe restoration as blindly replaying saved state.

### Q63 Required Git and DNS exceptions

Should No Network Access allow only the workspace's configured Git origin, administrator-approved auxiliary Git endpoints such as required LFS/submodule hosts, a designated DNS resolver, and the already agreed CloudBox API/proxy endpoints? Repository content, redirects or newly discovered hosts would not automatically widen the allowlist. This controls destinations, not repository authorization on a shared Git host.

**Recommendation:** Yes. Declare these exceptions explicitly and apply Git permissions through credentials separately. If a provider cannot enforce the resulting destination policy, the profile is unsupported; do not silently grant broader access. Exact name-resolution/address-refresh mechanics follow.

### Q64 Network changes while stopped

Should authorized network-profile changes be supported only while the VM is stopped in phase one, preserving retained data and applying/verifying the new policy before the next start? A running workspace must first be stopped deliberately; live policy mutation is deferred.

**Recommendation:** Yes. Permit supported changes on stopped VMs without requiring recreation. If the adapter cannot apply or verify the selected policy, keep the VM stopped and report the failure rather than fall back to broader access. Provider capability metadata must cover update support as well as initial creation.

### Q65 Member offboarding

Should disabling a CloudBox member immediately revoke that user's CloudBox access and proxy sessions and request provider-enforced stops of their running VMs? The VM files remain for administrator review under the existing retention/deletion policy; disabling membership does not itself destroy them. Closing browser access alone does not stop code already executing inside the VM.

**Recommendation:** Yes. Block the member's control-plane access immediately, close their connections, and stop owned workloads while preserving data. Track stop failures explicitly. External Keycloak-account changes need a separately specified synchronization/detection contract; they are not automatically observed merely because CloudBox uses OIDC.

## Accepted round Q45 through Q54

The user explicitly answered “Yes to Q45–Q54.” The questions and accepted recommendations below preserve the scope of that approval. Open implementation details remain open; this approval does not authorize implementation before the interview's final confirmation.

```text
Packer platform artifacts -> Q45 delivery and selection
  -> later: publication trust, caching and artifact upgrades
Retained files -> Q46 exact preservation boundary
  -> later: disk layout, rebuild behavior and migration
Guest Dev Container execution -> Q47 supported configuration forms
  -> later: project OS compatibility and functional acceptance
One Mac hosting workspace VMs -> Q48 capacity admission
  -> later: resource values, reservations and concurrency limits
Configurable inactivity -> Q49 initial monitor policy
  -> later: thresholds, timers, recovery grace and warnings
Keycloak authentication -> Q50 deployment ownership
  -> later: installation, upgrades and identity backup boundary
App-owned membership -> Q51 joining and role assignment
  -> later: offboarding and complete permission matrix
Per-VM mutual TLS -> Q52 first enrollment
  -> later: bootstrap transport validation, renewal and revocation
Native UTM host -> Q53 operational availability scope
  -> later: startup and host-recovery behavior
Capability-aware networking -> Q54 common rule model
  -> later: Git/DNS exceptions and adapter enforcement
```

### Q45 Platform artifact delivery

Should phase one have an administrator explicitly import a versioned Packer-built platform artifact into a local CloudBox catalog and select the default for new workspaces? Each workspace would record the exact artifact revision used; creating a workspace would not silently fetch a moving latest release.

**Recommendation:** Yes. Start with explicit administrator import and version selection. Remote catalogs and automatic downloads can follow later. Artifact trust/verification and existing-VM upgrades are separate decisions.

### Q46 Retained user state

Should the guaranteed retained state consist of project working files, the selected developer account's home directory (including browser IDE settings/extensions stored there), and application data explicitly placed in designated persistent mounts? Container writable layers, images, build caches and changes elsewhere would not receive that guarantee. A home directory need not be `/home/vscode`.

**Recommendation:** Yes. Preserve those designated user-data areas across reboot, stop/start and devcontainer rebuild. Treat caches as disposable and make application data retention an explicit mount contract. Select physical disk layout afterward.

### Q47 Dev Container configuration scope

Should phase one support a single primary devcontainer configured from a prebuilt image or Dockerfile, including Dev Container Features and lifecycle hooks, while deferring Compose-based multi-container project definitions? Supporting related containers inside a VM does not itself require CloudBox to support every multi-container configuration form.

**Recommendation:** Yes. Support image, Dockerfile, Features and hooks first; defer platform-managed Compose projects. This preserves existing product goals without treating current incomplete implementation as acceptance evidence. Project-container OS compatibility remains a separate decision.

### Q48 Capacity admission

Should administrators define resource profiles and installation capacity limits, with CloudBox rejecting a new start clearly when the configured capacity cannot accommodate it? The alternative is a queue or automatic eviction/resizing, each of which adds lifecycle behavior.

**Recommendation:** Yes. Use explicit profiles and capacity checks, fail promptly with a useful reason, and do not add automatic queuing, eviction or resizing in phase one. Numerical limits and reservation accounting follow this decision; this does not introduce early scale tests.

### Q49 Initial inactivity monitors

Should the first configurable monitors be browser IDE connection presence and guest CPU activity, with any selected monitor reporting activity keeping the workspace active? Inactivity begins only when all selected monitors report idle. An open but unattended IDE therefore counts as active when that monitor is enabled; this is not a claim to detect human input. Missing polls still count under the already accepted controller timeout rule.

**Recommendation:** Yes. Start with these two selectable monitors and an all-selected-monitors-idle rule. Keep thresholds and timing configurable; add more signal types later. This remains cooperative telemetry under the accepted whole-VM threat model.

### Q50 Keycloak deployment ownership

Should phase one connect to an administrator-managed Keycloak deployment, including an existing one, while the administrator remains responsible for running, upgrading and recovering Keycloak? The native CloudBox binary would configure its OIDC integration and manage application membership/permissions, but would not become a Keycloak server supervisor or account/password-management application.

**Recommendation:** Yes. Use administrator-managed Keycloak initially. Define its setup requirements and recovery boundary explicitly. This decision does not authorize changing or replacing the protected existing local Keycloak installation.

### Q51 Membership admission

Should setup bind one existing Keycloak identity as the first CloudBox administrator, with subsequent authenticated users requesting membership and an administrator explicitly approving them as members or admins? An authenticated but unapproved user would receive no workspace access. Membership would use the stable issuer/subject identity rather than email as its key.

**Recommendation:** Yes. Use an explicit first-admin binding and administrator approval for additional members. Defer invitation delivery and automatic Keycloak group-to-role synchronization.

### Q52 First VM enrollment

Should CloudBox automatically enroll a newly provisioned VM using a short-lived, single-use credential bound to that workspace and VM instance, delivered separately from the reusable platform image? The guest would receive controller trust information, generate its own private key and exchange the enrollment credential for its scoped client certificate.

**Recommendation:** Yes. Use automatic per-VM enrollment with a one-use bootstrap credential and guest-generated key. A host-created per-VM seed disk is the UTM candidate; exact delivery, cleanup, expiry and rotation mechanics follow. Never bake shared enrollment credentials into the artifact.

### Q53 Mac operational scope

Should the first UTM release require a logged-in macOS session with UTM running, and make no availability promise while the host is asleep or logged out? UTM's documented headless mode still requires the application; unattended pre-login service operation has not been established for this stack.

**Recommendation:** Yes. Make logged-in, awake-host operation the phase-one support boundary. Handle interruption and recovery through the agreed reconciliation/grace rules. Decide startup automation afterward rather than promising unattended server behavior now.

### Q54 Common outbound network rules

Should the standard restricted-policy model be an outbound allowlist of destination IP ranges, TCP/UDP protocols and destination ports, plus the visible required Git and CloudBox exceptions? All outbound access remains a valid profile. The provider metadata determines which rule fields/profiles are supported; unavailable controls remain hidden with actual default access described.

**Recommendation:** Yes. Start with this small common rule model rather than a general firewall language. Specify IPv4/IPv6, DNS, exact Git endpoints and enforcement details afterward; this does not claim UTM already supports every rule.

## Complete question outcome ledger

This table records the current outcome of every asked question. Later refinements are incorporated instead of preserving superseded recommendations as requirements.

| Question | Topic | Current outcome |
| --- | --- | --- |
| Q1 | Primary optimization | Simplicity first; performance/cost/capacity improvements require evidence. |
| Q2 | Responsibility boundary | CloudBox owns workspace lifecycle; provider owns VM infrastructure; hosting is separate. Native macOS is the first deployment. |
| Q3 | Single binary | Packaging goal, not a single-process/no-dependencies restriction. |
| Q4 | Audience | One self-hosted organization with multiple users per installation. |
| Q5 | Devcontainer execution | Image-baked guest service polls controller and runs standard Dev Container tooling locally. |
| Q6 | Infrastructure ownership | Provider handles platform VM infrastructure, disks and networking. |
| Q7 | Durable object | Files persist across reboot but are lost on VM destruction; developer persists work to Git. Q16 extends non-destructive preservation. |
| Q8 | Controller outages | Running VMs continue computing; reconnect when controller returns. User connections may be disrupted. |
| Q9 | Isolation unit | One VM per workspace; sandboxing is a goal. Network posture is separately configurable. |
| Q10 | Polling execution | Desired-state reconciliation plus durable identified operations, not blind command replay. |
| Q11 | Destruction authority | Explicit authorized destruction or administrator/user-configured inactive-retention policy. |
| Q12 | Re-registration | Authenticated reconnection to existing identity; unknown agents require explicit recovery. |
| Q13 | Scale validation | Design for scalability; no 100/10 envelope adopted; load testing deferred until product fully implemented. |
| Q14 | Guest trust | Entire VM, including agent, potentially compromised; no broad infrastructure authority inside. |
| Q15 | Retention authority | Admin limits, users may shorten; auto-delete off until configured; applicable policy visible. |
| Q16 | File preservation | Designated files survive reboot, stop/start and devcontainer rebuild. |
| Q17 | Provider sequence | UTM first on macOS; EC2 second phase. UTL was corrected to UTM. |
| Q18 | Deletion completion | Verify cleanup of workspace-owned resources; retain and retry incomplete work. |
| Q19 | Initial topology | Native standalone binary on the same Mac as UTM. |
| Q20 | Network policy ownership | Platform service translates supported controls; admins select network access configuration. Refined by Q25/Q26/Q36. |
| Q21 | Inactivity observation | Guest provisioning service monitors administrator-configured signal types; no fixed signal set adopted. |
| Q22 | User access | All workspace user traffic through Proxy Service. |
| Q23 | IDE scope | VS Code Browser only initially; other IDEs future phase. Exact non-IDE compatibility matrix remains open. |
| Q24 | Roles | Admin/member initially, extensible RBAC framework; owner-private workspaces with explicit admin access under Q34. |
| Q25 | Unsupported restrictions | Capability metadata drives UI; hide unavailable controls and show actual provider default access. |
| Q26 | Restricted profile | Label No Network Access; allow required Git and CloudBox communication. Q38 includes proxy tunnel at CloudBox endpoint. |
| Q27 | Silent agent | Secure authenticated communication required; missing polls count toward inactivity. Truthfulness limitation resolved by Q31/Q40. |
| Q28 | Proxy scope | User ingress to workspace; management and workspace egress remain separate paths. |
| Q29 | RBAC structure | Resource/action permission checks with two predefined roles; granular roles possible later. |
| Q30 | Initial state topology | One active controller. Initial SQLite-only scope is superseded by Q117: SQLite demo/small; PostgreSQL production for application and Dex state. HA remains deferred. |
| Q31 | Threat model confirmation | Keep whole VM untrusted; cryptography does not make telemetry truthful. |
| Q32 | Outage recovery | Elapsed inactivity counts, with reconciliation and recovery grace before overdue destruction. |
| Q33 | Restricted provisioning | Offline/approved artifact requirements; no hidden provisioning-time network bypass. |
| Q34 | Administrator entry | Explicit audited permission to connect to any workspace, separate from lifecycle permissions. |
| Q35 | Management recovery | Operator-triggered management backup/restore; not workspace-data backup. |
| Q36 | Mandatory isolation floor | Rejected; no network isolation is valid and All outbound access must be offered. |
| Q37 | Initial guest architecture | amd64 emulation retained; platform now Ubuntu 24.04 LTS Minimal under Q66. |
| Q38 | Proxy transport | Guest-initiated persistent tunnel; polling and traffic logically separate at CloudBox HTTPS endpoint. |
| Q39 | Backup encryption | Superseded by Q69: built-in encrypted backups required; recovery-key custody still open. |
| Q40 | Falsified activity | Accept timeout evasion through authenticated false guest reports for initial cooperative cleanup. |
| Q41 | Packer plugin support | Maintain pinned UTM-compatible fork and validate exact configuration after design approval. |
| Q42 | Packer lifecycle | Release-time platform-image build; runtime consumes prepared artifact and creates project devcontainer separately. |
| Q43 | Login provider | OIDC and app-owned authorization retained; Q73 selects embedded Dex, superseding Keycloak. |
| Q44 | Guest channel security | Mutual TLS, unique revocable VM credentials, operation freshness; no shared baked-in agent secret or baseline per-payload signing. |
| Q45 | Artifact delivery | Admin imports versioned artifacts into local catalog/selects default; exact revision per workspace; no moving-latest downloads. |
| Q46 | Retained state | Project files, effective developer home/IDE state there, and designated application-data mounts retained; caches/layers/other changes not guaranteed. |
| Q47 | Dev Container forms | Images, Dockerfiles, Features and hooks for one primary devcontainer; platform-managed Compose projects deferred. |
| Q48 | Capacity admission | Admin profiles and limits; refuse over-capacity starts clearly; no automatic queue, eviction or resizing initially. |
| Q49 | Inactivity signals | Browser connections and guest CPU selectable; any selected active signal keeps active; all must be idle; missing polls still count. |
| Q50 | Keycloak ownership | Superseded by Q68: product-managed local identity default, with an external OIDC option. |
| Q51 | Membership admission | Administrator-approved membership and stable issuer/subject identities. Earlier first-admin binding is superseded by Dex-owned local bootstrap/email setup in Q84/Q140. |
| Q52 | VM enrollment | Automatic short-lived single-use bootstrap per VM, guest-generated private key, scoped certificate; seed disk is candidate transport. |
| Q53 | Mac availability | Logged-in, awake host with UTM running; no initial sleep/logout/pre-login availability guarantee. |
| Q54 | Network rule model | Outbound destination-IP-range/TCP-or-UDP/destination-port allowlist plus visible Git/CloudBox exceptions; capability-dependent. |
| Q55 | Artifact acceptance | Self-contained manifest-described bundles, expected digests, approved source, immutable catalog and separate writable copies. |
| Q56 | Platform pinning | Platform OS/image remains pinned; Q76 later exempts the guest service and requires automatic upgrades with fallback. |
| Q57 | OS reconsideration | Resolved by Q66 platform Ubuntu 24.04 Minimal and Q71 glibc project-container scope. |
| Q58 | T-shirt sizes | Reservation accounting accepted; users choose size labels, admins define CPU/memory/disk per size. |
| Q59 | Idle deadline | Continuous controller-owned deadline; activity resets, idle polls do not; stopped/missing-poll time counts with outage grace. |
| Q60 | Certificate lifecycle | Pre-expiry renewal; Q150 permits verified provider-mediated re-enrollment of expired eligible known VMs. Revoked identities never automatically recover. |
| Q61 | Backup and identity | Owned CloudBox keys/secrets/state included; local identity ownership and alternatives reopened. |
| Q62 | Recovery checkpoint | Original-Mac guarded restore accepted; encrypted controller/owned-identity archive proposed conditionally, not yet selected. |
| Q63 | Git/DNS exceptions | Configured Git origin, approved auxiliary hosts, designated resolver and CloudBox endpoints; no discovered-host auto-expansion. |
| Q64 | Network changes | Stopped-only supported changes; apply/verify before start, keep stopped on failure. |
| Q65 | Offboarding | Revoke access, close sessions, stop owned workloads and retain data; external-IdP change detection separately specified. |
| Q66 | Platform release | Ubuntu 24.04 LTS Minimal amd64 selected; pin image revision/digest, keep emulation. |
| Q67 | Implementation language | Go selected for both controller and guest services after research; no legacy-code migration and no implied Dex selection. |
| Q68 | Identity ownership | Product-managed OIDC and external integration direction; Q73 selects embedded Dex and reopens precise local-credential/bootstrap meaning. |
| Q69 | Backup encryption | Built-in encryption required, superseding plaintext export; recovery key must remain available after installation loss. |
| Q70 | Workspace disks | One persistent VM-owned disk containing OS and designated retained areas; separate data disk deferred. |
| Q71 | Project OS support | glibc-based amd64 Linux project containers initially; Alpine/musl project containers deferred. |
| Q72 | Size revisions | Pin actual CPU/memory/disk per workspace at creation; later profile edits affect future workspaces only. |
| Q73 | Embedded identity/bootstrap | Embedded Dex selected; default administrator/password and forced first-login password change required. Separate local identity rejected; Dex credential ownership and bootstrap implementation need clarification. |
| Q74 | MFA | Optional configuration deferred to phase II; mandatory phase-one administrator MFA rejected. |
| Q75 | Backup password | Operator supplies encryption password through CLI or UI for each backup; accepted independent recovery custody/no retained passphrase. |
| Q76 | Guest self-upgrade | One-version backward compatibility; next-poll guest-service update after controller update; failed update falls back and is shown in UI. Further controller update blocked by workspaces failing to reach current version. Exact version unit and non-reporting/stopped cases open. |
| Q77 | IDE readiness | Accepted authenticated guest/container/waitFor/proxied IDE readiness gate and separately visible later setup. |
| Q78 | Repair mode | Accepted explicit known-good repair environment preserving files/diagnostics for project failures. |
| Q79 | Scoped secrets | Encrypted controller secrets retrieved by guest service. Early workspace scope is superseded by Q111: User → Project Template → Organization only. Git user authority is settled in later outcomes. |
| Q80 | Web UI | React, Next.js and TypeScript; static export compiled into binary. Subsequent explicit user decision selects Mantine as the component library. |
| Q81 | Browser origins | Separate management, workspace IDE and preview origins accepted. |
| Q82 | Private access | LAN/private-network HTTPS plus localhost development accepted. |
| Q83 | Provider packaging | Compiled Go adapters/internal contract accepted; runtime plugins and remote workers deferred. |
| Q84 | Credential ownership | Dex owns credential hashes; CloudBox owns setup/authorization state and enforces restricted first-login password change server-side. |
| Q85 | Bootstrap password | Unique random temporary password per installation, delivered locally and forced to change; no automatic reset on restart/restore. |
| Q86 | Compatibility and maintenance | Current/previous product release compatibility and sequential upgrade gate accepted. Admin-scheduled start–upgrade–stop with user grace period then mandatory maintenance added. Safe interruption/failure limits require follow-up. |
| Q87 | Upgrade activation | Next-poll staging, authenticated artifact, safe-operation boundary, independent recovery, health verification and possible brief browser reconnect accepted. |
| Q88 | Failed artifact retries | Rollback and visible error; no automatic retry of same failed artifact until administrator retry or corrected compatible artifact. |
| Q89 | Secret delivery responsibility | Explicit eligible bindings retained; guest performs technology-specific mapping/injection, some automatic helpers and developer consumption through devcontainer.json. Controller authorization boundary retained. |
| Q90 | Secret ownership and project templates | Admins own organization secrets; users own personal secrets. Shareable templates define repo, environment size, secrets and launch multiple workspaces. Project-scoped secrets introduced; exact template ACL and replacement of per-workspace scope open. No admin UI redisplay/audit values accepted. |
| Q91 | Runtime secret interface | Technology-specific file/env delivery plus in-container cloudbox secrets get-secret command returning value. Exact CLI lookup, caching/rotation behavior open. |
| Q92 | Stored secret encryption | Encrypt values in ordinary SQLite with authenticated contextual encryption/key versions; metadata plaintext, backups fully encrypted; master-key custody still open. |
| Q93 | GitHub integration | GitHub App/OAuth required in phase one; manual-credentials-first/deferred broker proposal rejected. App/token authority and provider scope still open. |
| Q94 | HTTPS and UI runtime | CloudBox must serve HTTPS. Research Go serving React/Next versus a Next server; static-only runtime contract reopened, no Node server selected yet. |
| Q95 | DNS/certificates | Admin-provided DNS and separate origins. General managed browser-CA option is superseded by Q145/Q149: uploaded production TLS, temporary loopback bootstrap only. |
| Q96 | Build-time secrets | Application build secrets deferred; runtime and Git setup in scope. |
| Q97 | Maintenance final state | Management-only boot for stopped workspaces then stop; preserve running workloads/newer desired state, capacity and retention safeguards. |
| Q98 | Mandatory maintenance limits | Deadline ends postponement but safe activation remains; hung/unsafe operations require admin handling, no automatic force-kill. |
| Q99 | Failed maintenance gate | Compatibility gate remains until explicit repair/retry/retirement; no automatic deletion or bypass. |
| Q100 | Template revision snapshot | Independent owner-private workspace captures launch template revision/config/resources/references; later edits affect new launches, not frozen secret values/permissions. |
| Q101 | Template sharing permissions | Owner/admin edits and manages project secrets; selected members may launch; collaborative editors/public sharing deferred. |
| Q102 | Secret scopes superseded | Original four-scope proposal superseded by Q111: only User → Project Template → Organization; no workspace scope. |
| Q103 | Shared secret authority | Template use grants shared project-secret use; personal secrets resolve against launching user, not author; no plaintext values embedded in shared template. |
| Q104 | Flat secret lookup | Any authorized secret retrievable by key, not only bound aliases. User overrides project/workspace, which override organization; project/workspace relative order unresolved. |
| Q105 | Retrieval outage policy | Current reads fail clearly without controller; no persistent offline retrieval cache. Existing injected values/workloads continue; copied credentials cannot be retracted. |
| Q106 | Web serving | Go HTTPS with embedded static Next.js assets/live Go APIs and export-compatible routing accepted. No production Node UI runtime. |
| Q107 | GitHub app type | GitHub App with browser OAuth user authorization accepted. |
| Q108 | Git authority and PRs | Workspace-owner GitHub authority accepted; ability to work with PRs added, precise workflow pending. |
| Q109 | Git transport/provider scope | GitHub.com HTTPS clone/fetch/push initially; SSH, Enterprise Server and other providers deferred. |
| Q110 | App ownership | Administrator ownership accepted; user explicitly says admin owns app installation. Registration, installation and user authorization are distinct setup steps. |
| Q111 | Secret scopes/precedence | Only User → Project Template → Organization; individual workspace scope removed, superseding Q102. |
| Q112 | Secret lookup identity | Recorded workspace owner context; admin connection does not expose admin's personal secrets. |
| Q113 | Template access removal | Immediate project-secret retrieval/new-launch revocation; preserve existing workspaces/files; copied credentials cannot be retracted. |
| Q114 | Portable key custody | Mac Keychain dependency rejected; controller may run other platforms/containers, encryption key application-managed as securely as possible. Mechanism and deployment timing open. |
| Q115 | Database profiles/setup | SQLite demo/very-small; external PostgreSQL production. CloudBox schema/seed management, post-password-change UI PG setup and CLI privileged seeding/config update required. App/Dex scope and safe bootstrap details open. |
| Q116 | Deployment rollout | Native Mac/UTM initial deployment; portable design now, Linux/container production with EC2 phase. |
| Q117 | Database profile scope | Both application and Dex state use PostgreSQL in production and SQLite for demo/very-small installations; separate ownership boundaries. |
| Q118 | Database seeding | Empty provided target or authorized database/role creation; discard elevated setup credentials, retain scoped service access, validate before config activation, recoverable non-destructive retries. |
| Q119 | Portable encryption key | Protected persistent installation-specific key outside DB, optional supplied mounted key file, encrypted backup recovery; missing/wrong key fails without replacement. |
| Q120 | PR requirement narrowed | No product PR management. GitHub App permissions should let developers work with PRs from workspace tools under their GitHub authority. |
| Q121 | Secret fallback | Deleting override reveals next authorized lower scope; explicit empty value overrides; revoked project source removed without revoking independent user/org sources. |
| Q122 | External-PG backups | Encrypted archive still covers owned application/identity data/configuration, excluding database server/unrelated data/workspace disks. |
| Q123 | PostgreSQL layout | One PG database, separate ShiftBox/Dex schemas and scoped owner accounts; validated role search paths/permissions. |
| Q124 | Bootstrap transition | Restricted SQLite initial setup; preserve owner/password, transfer only bootstrap state to PG, fresh sign-in, no normal use before completion; no automatic fallback or populated-demo migration. |
| Q125 | Backup pause | Brief control/sign-in/background-write pause for consistent checkpoint; VMs continue; duration not guaranteed. |
| Q126 | GitHub permissions | Contents/PR read-write, Metadata/Checks/Commit statuses read accepted for developer tools, not product PR management. |
| Q127 | Workflow edits | Workflows write accepted; developers configure/use their devcontainer environment freely within agreed infrastructure constraints. |
| Q128 | gh tooling | Bundled supported gh/launcher rejected; developer configures tooling. |
| Q129 | Git token model | Expose opaque GitHub App user OAuth access token for Git HTTPS; not an app JWT or separate broker product. Q139/Q148 subsequently require encrypted access/refresh persistence and renewal. |
| Q130 | Webhooks/status checking | Proposal rejected by reference to Q129; no affirmative webhook or managed-status requirement inferred. |
| Q131 | Managed LFS/submodules | Supported-integration proposal rejected by reference to Q129; developer tools may use granted token/network access, no product guarantee inferred. |
| Q132 | Personal-account app installation | Managed personal-fork installation proposal rejected by reference to Q129; no automatic access to arbitrary forks implied. |
| Q133 | Live Dex protection | Production restricted DB access, verified TLS/encrypted operator storage accepted; custom Dex field encryption deferred, app-secret encryption unchanged. |
| Q134 | Supplied keys in backups | Required mounted/supplied key versions included inside independently encrypted archive; validate match, no silent mounted-key overwrite. |
| Q135 | Application key rotation | Deferred to phase II; phase-one resumable rotation proposal rejected. |
| Q136 | Local accounts | Email login, admin-created temp passwords/role choice, forced change, user password changes/admin recovery; no public registration/email delivery initially. |
| Q137 | Template archival | Archive prevents launch, preserves workspaces/history/grants; visible only in filtered admin view. |
| Q138 | Notices | In-product maintenance notices accepted; admins may publish systemwide notices dismissible by users after reading. |
| Q139 | Persisted user tokens | Persist user tokens across restarts. Q148 completes encrypted access/refresh persistence, automatic renewal and reconnection on permanent failure. |
| Q140 | Initial owner email | Collect first admin email in local setup/config before creating account/temp password; keep password change before PG setup. |
| Q141 | Admin reset delivery | One-time displayed temporary password, privately delivered by authorized admin, forced change/session revocation/audit; no email/public reset link. |
| Q142 | Last-admin recovery | Local protected-state/DB-authorized recovery of existing admin accepted; preserve data/identity, revoke sessions, audit; no public endpoint or arbitrary elevation. |
| Q143 | Notice history | Per-user read/dismissed history; maintenance remains visible and binding; dismissal does not prove comprehension. |
| Q144 | Archived provenance | Existing workspace may show captured name/revision; archived-template UI stays restricted to admin filtered view. |
| Q145 | Certificate policy | Web TLS uploaded in UI; warn admins 30 days before expiry through notices. Internal mTLS uses setup-generated CA and client/server rotation every four hours. Q149–Q150 complete bootstrap, validity, CA, overlap and offline recovery contracts. |
| Q146 | PG recovery target | Same original Mac/logical installation may restore owned schemas/roles into empty replacement PG target, validate before activation; no cross-host VM migration. |
| Q147 | Preview access | Sign-in and owner/explicit audited admin permission required; no anonymous links/sharing, no management credentials to guest. |
| Q148 | Persistent OAuth renewal | Accepted: encrypted access/refresh persistence; coordinated per-user renewal; expose current access token; reconnect on revocation/permanent failure; no managed developer tools. |
| Q149 | First HTTPS and uploaded TLS | Accepted: loopback temporary HTTPS with explicit trust or initial certificate/key paths; normal access only after validated uploaded pair; 30-day warnings; no silent fallback. |
| Q150 | Internal mTLS and long shutdowns | Accepted: rotate leaf/key every 4h, validity 8h, stable CA; research incorporated for expired known VM re-enrollment using verified provider delivery; never automatic recovery of revoked identities. |
| Q151 | Retention versus hung work | Accepted: final activity/authority/state checks and outage grace; graceful stop then configured provider-enforced deletion despite hung work; retain reservations until verified cleanup. Maintenance authority unchanged. |
| Q152 | External authorization interval | Administrator-configurable interval; researched engineering default 4h, not a universal standard. Actual upstream authorization, no refresh-only extension or directory-disablement guarantee. |
| Q153 | Operator installation and updates | Accepted: manual native operation, prerequisites, compatibility gate and verified backup before irreversible app/Dex migrations; compatible-release restore, not binary-only rollback. |
| Q154 | Administration and storage | Accepted: defer purge/ownership transfer; configurable audit/notice retention preserves required records; pressure blocks allocations and alerts, no extra data-deletion authority. |
| Q155 | Design synthesis and closure | Accepted: series of design documents and architecture/communication/boundary diagrams; delegated bounded engineering details and validation backlog; final readback confirmation and implementation authorization pending. |

## Research findings and validation limits

These are findings from read-only investigation, not additional user decisions or proof that the new architecture works. Existing-code findings now describe a reference project only: no migration/reuse is required for the greenfield implementation.

### Closing research incorporated after Q155

**Offline certificates:** no offline process can renew; validity follows wall-clock time. Known eligible VMs must receive fresh enrollment through a verified provider path on return, with exact-instance/generation binding, one-use atomic consumption, current trust and new guest key proof. Stopped UTM media replacement is source-supported but not end-to-end proven. Preserve every existing drive when updating configuration; do not assume cloud-init reruns the same instance. Running expired VMs fail closed for management and require a separately validated live host path or an authorized restart. No expired-cert exception, permanent recovery password or forced restart merely for media injection. Stable CA expiry and guest clock correctness need explicit lifecycle validation. Full primary-source analysis: [Certificates and guest trust](certificates-and-guest-trust.md).

**OIDC interval:** there is no universal industry-standard authorization interval. NIST authentication guidance, Keycloak maximum sessions and Okta idle policies measure different clocks; none establishes ShiftBox compliance or a mandatory four-hour value. Four hours is a conservative configurable initial engineering default. Actual upstream authorization may reuse SSO, but token refresh alone does not establish a new authorization deadline or directory-enable check. Sources and comparison: [External identity default rationale](validation-and-engineering-defaults.md#external-identity-default-rationale).

### Existing CloudBox

- The application already has a unified binary and durable coordinator. The main simplification target is the workspace runtime and its infrastructure dependencies. See [coordinator](/Users/jflatten/Workspace/CloudBox/internal/workspace/coordinator.go) and [runtime provider interfaces](/Users/jflatten/Workspace/CloudBox/internal/runtime/provider.go).
- Provider interfaces and runtime dispatch still contain Kubernetes-specific concepts. A new VM adapter alone will not create a fully generic boundary. See [Kubernetes service](/Users/jflatten/Workspace/CloudBox/internal/workspace/kubernetes.go).
- Current storage is SQLite with single-writer assumptions and a daemon lock. Existing authentication is single-owner; the agreed multiuser authorization is real new work. See [store](/Users/jflatten/Workspace/CloudBox/internal/store/store.go), [daemon lock](/Users/jflatten/Workspace/CloudBox/internal/server/lock.go), and [authentication](/Users/jflatten/Workspace/CloudBox/internal/server/auth.go).
- The existing browser IDE is code-server, with useful session-bound gateway controls. Its heartbeat indicates connections, not reliable human activity. See [browser IDE](/Users/jflatten/Workspace/CloudBox/internal/ide/web.go), [gateway](/Users/jflatten/Workspace/CloudBox/internal/server/workspace_gateway.go), and [code-server health endpoint](https://coder.com/docs/code-server/FAQ#what-is-the-healthz-endpoint).
- Existing implementation evidence is mixed and incomplete. Do not interpret this new design, or earlier product goals, as proof that private Git, every devcontainer configuration, or every IDE path already passes.

### November 13 platform OS research

The user has now selected **Ubuntu 24.04 LTS Minimal amd64** under Q66. The following comparison is the evidence behind that choice, not a benchmark. These are official indexes' displayed QCOW2 download sizes inspected November 13, not installed sizes, RAM use, boot timings or final images after Docker/Node/agent installation.

| Candidate | Published amd64 QCOW2 size | Support window | Relevant tradeoff |
| --- | --- | --- | --- |
| Ubuntu 24.04 LTS Minimal | 248M | Standard security through May 2029 | Smallest inspected download; footprint-first recommendation. |
| Ubuntu 26.04 LTS Minimal | 402M | Standard security through May 2031 | Longest remaining standard support among these candidates. |
| Debian 13 genericcloud | 326M | Full support through August 9, 2028; LTS through June 30, 2030 | Reduced-driver kernel; validate UTM devices. |
| Debian 13 generic | 417M | Same Debian lifecycle | Debian recommends generic for maximum compatibility. |

Sources: [Ubuntu 24.04 Minimal index](https://cloud-images.ubuntu.com/minimal/releases/noble/release/), [Ubuntu 26.04 Minimal index](https://cloud-images.ubuntu.com/minimal/releases/resolute/release/), [Debian image index](https://cloudfront.debian.net/cdimage/cloud/trixie/latest/), [Ubuntu lifecycle](https://ubuntu.com/about/release-cycle), [Debian lifecycle](https://www.debian.org/releases/trixie/), [Debian Cloud FAQ](https://wiki.debian.org/Cloud).

- Docker supports these Debian/Ubuntu releases and amd64. Their glibc userspace is a straightforward match for standard Node/Dev Container tooling. Pin supported tool versions rather than interpreting a minimum version as a recommendation. [Docker Ubuntu](https://docs.docker.com/engine/install/ubuntu/), [Docker Debian](https://docs.docker.com/engine/install/debian/), [Node platform requirements](https://github.com/nodejs/node/blob/v24.x/BUILDING.md).
- Debian generic/genericcloud include cloud-init; Debian's image named nocloud does not. A cloud-init **NoCloud datasource** is a separate concept. Ubuntu Minimal or Debian generic can use a local seed. [Debian image definitions](https://cloudfront.debian.net/cdimage/cloud/), [NoCloud datasource](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html).
- If selecting Ubuntu 26.04, distinguish ordinary amd64 images from amd64v3 variants for the emulated target. [Canonical architecture variants](https://ubuntu.com/cloud/public-cloud/docs/all-clouds-explanation/architecture-variants/).
- No evidence identifies the fastest/lowest-RAM candidate on the exact UTM stack. Existing workspaces remain frozen: distro support means patches are available, not that frozen workspace images receive them automatically.
- The platform image and project container remain separate compatibility layers. [code-server installation](https://coder.com/docs/code-server/install) documents glibc requirements for standalone archives and a different Alpine/npm path; a Debian/Ubuntu VM does not inject glibc into Alpine project containers.

### November 13 identity and backup research

**Keycloak can be packaged as part of one product installation, but it is a separate JVM server, not a documented embeddable Go identity library.** A single installer, no separately administered service, and one Go process are three different requirements. CloudBox can use OIDC in all three architectures. [Keycloak architecture](https://www.keycloak.org/docs/latest/server_admin/#_keycloak-features-and-concepts), [supported configurations](https://www.keycloak.org/server/supported-configurations).

| Candidate | Fit | Limitation or decision still needed |
| --- | --- | --- |
| Embedded Dex | Native Go provider/broker with real embedding precedent; local users plus social/enterprise connectors. | No stable upstream embedding contract established; CloudBox owns account/recovery/admin integration and version compatibility. SQLite support policy needs resolution. |
| Product-managed Keycloak | Mature account management, identity brokering/federation and administrator UI; can be one managed product experience. | Separate JVM and supported database such as PostgreSQL; not one Go process. Production support matrix does not list macOS, so supported Linux hosting or explicit platform validation is needed. |
| Bundled ZITADEL v4 | Full identity product and federation. | PostgreSQL plus default separate Login V2 service; not a simplification to one embedded process. |
| External OIDC | Smallest identity responsibility inside CloudBox. | Does not provide installation-owned accounts or a complete local identity backup. |

- Keycloak H2/dev-file is development-only and not a production solution; SQLite is not a supported Keycloak database. Do not hide these dependencies behind the word embedded. [Keycloak databases](https://www.keycloak.org/server/db), [upgrade guide](https://www.keycloak.org/docs/latest/upgrading/).
- NetBird provides concrete precedent: embedded Dex inside its Go management service, local users/external providers, and a separate SQLite identity database. This is stronger than merely being able to import a Go package. [NetBird local identity](https://docs.netbird.io/selfhosted/identity-providers/local), [NetBird configuration](https://docs.netbird.io/selfhosted/maintenance/configuration-files).
- Dex's own storage documentation warns SQLite is not appropriate for real workloads, despite the NetBird deployment choice. Resolve persistence/support expectations before committing to a production claim. Dex also leaves management API access controls and UI to integrating applications. [Dex storage](https://dexidp.io/docs/configuration/storage/), [Dex API](https://dexidp.io/docs/configuration/api/), [connectors](https://dexidp.io/docs/connectors/).
- Do not repeat outdated claims that Dex has no MFA: v2.46.0 added TOTP/WebAuthn and authentication sessions; exact version/feature configuration must be validated. [Dex release](https://github.com/dexidp/dex/releases/tag/v2.46.0).
- ZITADEL v4 requires PostgreSQL; new deployments default to the separately deployed Login V2. Its newer preview does not yet provide the same production identity capabilities. [ZITADEL deployment](https://zitadel.com/docs/self-hosting/deploy/overview), [Login V2](https://zitadel.com/docs/self-hosting/manage/login-client), [version comparison](https://preview.zitadel.com/choose-version).
- Authentik's server/worker/PostgreSQL topology and Hydra's separate login/identity responsibilities make them less compelling for this particular embedding goal. [Authentik architecture](https://docs.goauthentik.io/core/architecture/), [Hydra](https://www.ory.com/docs/oel/hydra/quickstart).
- A single encrypted archive can hold multiple databases and configurations. Consistency requires coordinated snapshots/write quiescence, not simply zipping live files. A Keycloak realm export is not a complete identity backup: sessions, revoked tokens and other state are omitted. [Keycloak export limits](https://www.keycloak.org/server/importExport).
- A control-plane checkpoint does not rewind live workspace disks, undo external Git/cloud actions, or restore upstream Google/enterprise identity systems. Restoring old identity state can restore old credentials/roles/revocation state; the accepted reviewed recovery gate remains necessary.

Provisional research recommendation: for a greenfield Go/native-local direction, **embedded Dex is the strongest candidate to evaluate**, with explicit ownership of account lifecycle and a persistence/support validation gate. Product-managed Keycloak remains the fuller-featured alternative if those functions should come ready-made. This was the recommendation before Q73. Q73 now selects embedded Dex; no prototype or implementation is authorized.

### November 13 implementation language research

This research informed the now-accepted Q67 choice of Go. No benchmarks or prototypes were run. All four shortlisted languages can implement the required macOS ARM64 controller, Ubuntu amd64 guest agent, HTTP/proxy/tunnel communication and durable SQLite-backed operations. Relative development-effort assessments below are engineering judgments, not measured performance rankings.

| Language | Fit for this architecture | Main cost or constraint | Local identity implications |
| --- | --- | --- | --- |
| Go | Native targets, direct concurrent I/O, standard HTTP/reverse-proxy/TLS tooling; straightforward pure-Go builds. | Garbage-collected runtime; native dependencies can complicate cross-builds. | In-process Dex is practical, but requires maintained integration and currently its SQLite path uses C. |
| Rust | Native executables, ownership checks and direct allocation control without tracing GC; capable async networking ecosystem. | More explicit ownership/async/library composition and target toolchains; payoff depends on team expertise and requirements. | Dex is most straightforward as a supervised companion service; cross-language embedding is additional work. |
| C#/.NET | ASP.NET Core/Kestrel, YARP proxying and Microsoft.Data.Sqlite are a strong application stack. | Self-contained single-file versus NativeAOT must be chosen deliberately; AOT has library and build restrictions. | OpenIddict plus account components can be in-process, but CloudBox still owns account/recovery/brokering integration. |
| TypeScript | Node/Bun can package application and runtime into an executable; can share language with the browser UI if desired. | Runtime/native-dependency compatibility and blocking-work discipline remain important. | An OIDC protocol implementation can run in-process, but is not a complete identity/account-management product. |

**Go and Rust facts**

- Both support the target platforms. Go's pure-Go cross-build path is straightforward; Rust cross-compilation also needs suitable target linkers/native toolchains. [Go target table](https://go.dev/doc/install/source#environment), [Rust targets](https://doc.rust-lang.org/rustc/platform-support.html), [Rust cross-compilation](https://rust-lang.github.io/rustup/cross-compilation.html).
- Go provides [HTTP reverse proxying](https://pkg.go.dev/net/http/httputil) and [TLS client authentication](https://pkg.go.dev/crypto/tls). Rust's [Tokio](https://tokio.rs/tokio/tutorial), [Axum WebSockets](https://docs.rs/axum/latest/axum/extract/ws/index.html) and [Rustls](https://github.com/rustls/rustls) cover the networking requirements. Neither supplies CloudBox's operation recovery, tunnel authorization or reconnect policy automatically.
- Go SQLite can use a [cgo-free implementation](https://pkg.go.dev/modernc.org/sqlite) or a C-backed driver. **Current Dex SQLite imports mattn/go-sqlite3 and has a cgo build constraint.** Go plus embedded Dex/SQLite is therefore not automatically a C-free cross-build. [Dex SQLite source](https://github.com/dexidp/dex/blob/master/storage/sql/sqlite.go), [Go cgo requirements](https://pkg.go.dev/cmd/cgo).
- Rust's bundled rusqlite compiles SQLite C source. A musl target can statically link the C runtime, but does not prove every application dependency is static. [rusqlite build notes](https://github.com/rusqlite/rusqlite#notes-on-building-rusqlite-and-libsqlite3-sys), [Rust linkage](https://doc.rust-lang.org/reference/linkage.html#static-and-dynamic-c-runtimes).
- Rust's ownership model and Go's garbage collector are different memory-management mechanisms, not evidence of a particular CloudBox RAM/latency ratio. [Rust ownership](https://doc.rust-lang.org/book/ch04-01-what-is-ownership.html), [Go GC guide](https://go.dev/doc/gc-guide).

**C#/.NET facts**

- .NET 10 is the current LTS, with documented support for Apple Silicon macOS and Ubuntu 24.04. [Support policy](https://dotnet.microsoft.com/en-us/platform/support/policy/dotnet-core), [macOS](https://learn.microsoft.com/en-us/dotnet/core/install/macos), [Ubuntu](https://learn.microsoft.com/en-us/dotnet/core/install/linux-ubuntu-install).
- YARP supports WebSocket forwarding and NativeAOT publishing; do not describe the proxy itself as an unsupported AOT blocker. Full application compatibility still depends on the selected middleware and identity stack. [WebSockets](https://learn.microsoft.com/en-us/aspnet/core/fundamentals/servers/yarp/websockets?view=aspnetcore-10.0), [YARP 2.3 release](https://github.com/dotnet/yarp/releases/tag/v2.3.0).
- Self-contained single-file includes the runtime; it is not the same as NativeAOT. Native libraries can need extraction. NativeAOT cross-OS compilation is unsupported, so Mac and Linux builds need their respective build environments. ASP.NET AOT supports a subset of the framework and requires deliberate serialization/reflection choices. [Single-file deployment](https://learn.microsoft.com/en-us/dotnet/core/deploying/single-file/overview), [AOT cross-compilation](https://learn.microsoft.com/en-us/dotnet/core/deploying/native-aot/cross-compile), [ASP.NET AOT](https://learn.microsoft.com/en-us/aspnet/core/fundamentals/native-aot?view=aspnetcore-10.0).
- Microsoft.Data.Sqlite uses native SQLite libraries and provides online backup. Database encryption and encrypted backup archives are separate requirements. [SQLite dependencies](https://learn.microsoft.com/en-us/dotnet/standard/data/sqlite/custom-versions), [backup API](https://learn.microsoft.com/en-us/dotnet/standard/data/sqlite/backup).
- OpenIddict supplies OIDC/OAuth protocol stacks but explicitly leaves user authentication to the implementer. ASP.NET Identity can supply account primitives; the combined account/recovery/admin experience remains product integration. Complete NativeAOT compatibility of that assembled identity stack was not established. [OpenIddict scope](https://documentation.openiddict.com/introduction).

**TypeScript and other considered options**

- Node single-executable applications bundle the runtime and application. Cross-platform generation has version/snapshot/code-cache constraints; embedded native addons require extraction. The feature is documented as active development. [Node SEA](https://nodejs.org/api/single-executable-applications.html).
- Bun's established `build --compile` supports macOS ARM64 and Linux x64 executable targets, assets and SQLite. Do not confuse it with its newer experimental native AOT path. Persistent database state must live outside the executable. [Bun executables](https://bun.sh/docs/compiler/executables), [Bun compiler](https://bun.com/docs/compiler).
- Runtime/dependency compatibility and blocking work require care, but there is no measured throughput failure for this design. [Node event loop guidance](https://nodejs.org/learn/asynchronous-work/dont-block-the-event-loop), [Bun compatibility](https://bun.com/docs/runtime/nodejs-compat).
- TypeScript's [oidc-provider](https://github.com/panva/node-oidc-provider) is credible protocol machinery, not turnkey local-account recovery and enterprise identity administration.
- Java/Kotlin with GraalVM is viable but was not shortlisted: native-image compatibility/metadata and target builds add complexity without a clear requirement-specific advantage here. Java does not magically make Keycloak a supported embedded library. [GraalVM native image](https://www.graalvm.org/latest/reference-manual/native-image/), [Keycloak server distribution](https://www.keycloak.org/getting-started/getting-started-zip).

**Conditional recommendation:** assuming no dominant team-language preference, Go best fits the chosen simplicity priority and possible Dex/Packer integration. C# is a strong application-framework alternative; Rust suits explicit ownership/resource-control priorities; TypeScript suits a strong team preference for one web/service language. Do not choose on invented speed or executable-size numbers. No controller language removes the cost of amd64 guest emulation on ARM hardware. Browser UI language remains independent.

### Research supporting Q73 through Q83

- Dex v2.46.1 has password CRUD/verification, connector CRUD and session/MFA administration, but product account screens, recovery authorization and membership remain integration responsibilities. Updating a password hash does not automatically revoke sessions; MFA reset also does not terminate them by itself. [API schema](https://github.com/dexidp/dex/blob/v2.46.1/api/v2/api.proto), [password implementation](https://github.com/dexidp/dex/blob/v2.46.1/server/apiserver/passwords.go), [MFA implementation](https://github.com/dexidp/dex/blob/v2.46.1/server/apiserver/mfa.go).
- Dex sessions, dynamic connector CRUD and session/identity administration are separately flagged and default disabled. MFA requires sessions. NetBird uses a pinned Dex fork, not unmodified current upstream; the fork and upstream have diverged database migrations. Do not assume their data files or feature sets are interchangeable. [Flags](https://github.com/dexidp/dex/blob/v2.46.1/pkg/featureflags/set.go), [NetBird dependencies](https://github.com/netbirdio/netbird/blob/main/go.mod), [NetBird embedding](https://github.com/netbirdio/netbird/blob/main/management/server/idp/embedded.go).
- Dex's documented SQLite warning has no published user-count/throughput threshold. Current upstream and the inspected NetBird fork use a C-backed SQLite driver with one open connection and migration-on-open. Embedding precedent does not settle production persistence or upgrade policy. [Storage docs](https://dexidp.io/docs/configuration/storage/), [upstream SQLite](https://github.com/dexidp/dex/blob/v2.46.1/storage/sql/sqlite.go), [NetBird adapter](https://github.com/netbirdio/netbird/blob/main/idp/dex/sqlite_cgo.go).
- Dev Container lifecycle scripts have defined ordering. waitFor defaults to updateContentCommand, permitting later work while a UI connects; a failing hook prevents subsequent hooks. Respecting that is different from waiting for every hook before offering access. [Lifecycle specification](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainerjson-reference.md#lifecycle-scripts).
- A source mount outside the disposable container permits preservation when the container cannot start. A working repair IDE still requires an independent known-good environment; the agent alone does not provide one. [Mount specification](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainer-reference.md#mounts). Legacy debug behavior covered configuration validation failures, not every build/hook/IDE failure; Q78 proposes new greenfield scope rather than inheriting an existing guarantee.
- Go's age library offers authenticated streaming encryption and passphrase recipients. Its passphrase recipient must be the sole recipient, so do not silently promise simultaneous passphrase and keychain recipients in that mode. Archive staging/full authentication before restore is an application requirement. [age library](https://pkg.go.dev/filippo.io/age), [format](https://c2sp.org/age).
- GitHub App user tokens and installation tokens differ in authority and attribution. Installation tokens can be scoped to selected repositories/permissions; user tokens act within user/app access. SSH-agent forwarding allows use of the user's agent and is not intrinsically repository-scoped. [User tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app), [installation tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation), [SSH-agent forwarding](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/using-ssh-agent-forwarding).

### Research supporting Q84 through Q96

- Upstream Dex v2.46.1's local connector stores credentials in Dex. Its password model has no must-change field; username is display-only and email is the login key. Static password entries are read-only, so a changeable bootstrap identity must use mutable storage. CloudBox must provide and enforce its restricted setup flow; changing a hash alone does not revoke sessions. [Local connector](https://dexidp.io/docs/connectors/local/), [password API](https://github.com/dexidp/dex/blob/v2.46.1/api/v2/api.proto), [static storage](https://github.com/dexidp/dex/blob/v2.46.1/storage/static.go), [management API](https://dexidp.io/docs/configuration/api/). These are inspected-version findings, not claims about all future releases. Selecting Dex does not resolve its previously recorded SQLite/embedding validation issues.
- Ubuntu 24.04 systemd provides readiness, timeouts and restart supervision, but restart of a failed binary is not binary rollback. Independent recovery, retained previous executable and compatible local state are required. Atomic rename alone is not a durable upgrade transaction. These are engineering implications of the requested fallback behavior. [systemd.service](https://manpages.ubuntu.com/manpages/noble/man5/systemd.service.5.html), [rename](https://man7.org/linux/man-pages/man2/rename.2.html), [fsync](https://man7.org/linux/man-pages/man2/fsync.2.html). Signed updates also need freshness/key-rotation policy; signatures alone are not full TUF protection. [TUF specification](https://theupdateframework.github.io/specification/latest/index.html).
- Next.js static export produces files Go can embed; no request-time Next server features are available. Dynamic route parameters must be known at build time unless routing is designed differently. Go may still implement HTTP cookies/authentication and live APIs. Public build environment variables are embedded in client assets. [Static export](https://nextjs.org/docs/app/guides/static-exports), [environment variables](https://nextjs.org/docs/app/guides/environment-variables), [Go embed](https://pkg.go.dev/embed).
- Encrypted secret values and whole-database encryption are distinct choices; SQLCipher is a specialized encrypted SQLite implementation. Key custody remains separate. File and environment exposure both trust the consuming workload; delivered credentials cannot be retracted from a compromised guest merely by deleting a binding. [SQLCipher design](https://www.zetetic.net/sqlcipher/design/), [OWASP secret management](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html). Docker recommends dedicated build-secret mounts instead of persistent ARG/ENV credentials; build access is distinct from runtime access. [Docker build secrets](https://docs.docker.com/build/building/secrets/).

### Research supporting Q97 through Q105

- Dev Containers supports top-level secret-name declarations with optional description/documentationUrl. They describe recommended secrets; the standard does not grant access or define CloudBox provider bindings. Product-specific metadata can use customizations. Initial private clone needs credentials before repository devcontainer.json is readable. [Declarative secrets](https://github.com/devcontainers/spec/blob/main/docs/specs/declarative-secrets.md), [schema](https://github.com/devcontainers/spec/blob/main/schemas/devContainer.base.schema.json), [delivery support](https://github.com/devcontainers/spec/blob/main/docs/specs/secrets-support.md). Template snapshot/permission recommendations are design inferences, not requirements imposed by the standard.
- Git helpers receive credential context and return values; credential.useHttpPath enables path-aware matching for HTTP. A helper is not a boundary hiding the credential from the authorized guest, and HTTPS helpers do not automatically implement SSH. [Git helpers](https://git-scm.com/docs/gitcredentials), [credential protocol](https://git-scm.com/docs/git-credential). Local pathname sockets can enforce ordinary process permissions, but not protect against compromised guest root. [Unix sockets](https://man7.org/linux/man-pages/man7/unix.7.html).
- containerEnv does not dynamically change already-running processes; remoteEnv concerns supporting-tool processes and children. Runtime rotation/reload requires a defined contract. [Dev Container metadata](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainerjson-reference.md).
- A maintenance boot can automatically launch containers under Docker restart policy unless deliberately suppressed. Force stop may escalate to killing processes; it does not prove application consistency or preserve unsaved memory. UTM documents risks of forced shutdown. [Docker restart policy](https://docs.docker.com/engine/containers/start-containers-automatically/), [Docker stop](https://docs.docker.com/reference/cli/docker/container/stop/), [UTM controls](https://docs.getutm.app/basics/controls/). The awake/logged-in Mac and open UTM requirement remains; schedules cannot guarantee completion during host outage. [UTM headless operation](https://docs.getutm.app/advanced/headless/).

### Research supporting Q106 through Q115

#### Go HTTPS and React/Next.js serving

HTTPS does not require a Next.js runtime: Go supports TLS serving and reverse proxying. [Go HTTPS](https://pkg.go.dev/net/http#Server.ListenAndServeTLS), [Go reverse proxy](https://pkg.go.dev/net/http/httputil#ReverseProxy).

| Option | Runtime distribution | Benefits | Costs/limits |
| --- | --- | --- | --- |
| Go HTTPS plus embedded Next static export | Go executable with HTML/JS/CSS assets | Fewest runtime parts; live UI calls Go APIs | No request-time Next features; unknown future workspace IDs need fixed/client route design |
| Go HTTPS plus supervised Next standalone | Go executable/product plus Node and traced app files | Normal Next server features, SSR and dynamic routes | Process supervision, runtime updates, extracted assets/cache and native packaging |
| Embedded JS/Node engine in Go | Custom native/runtime integration | Potential single process | No documented turnkey Next-on-Go deployment; Node embedding API may break between majors |

Static export cannot support arbitrary unknown dynamic route parameters using an ordinary App Router route without build-time params. It also excludes Server Actions and request-time Next cookies/middleware; Go may still handle cookies/auth/APIs. A fixed /workspace?id=... page can fetch live data. [Next static export](https://nextjs.org/docs/app/guides/static-exports).

Next standalone creates server.js and traced dependencies, **not an executable or a bundled Node runtime**. public and .next/static assets require explicit inclusion. A supervised private Node process behind Go is a supported-pattern proposal, and Next recommends a reverse proxy. A custom Next server is not compatible with standalone output. [Standalone output](https://nextjs.org/docs/app/api-reference/config/next-config-js/output), [self-hosting](https://nextjs.org/docs/app/guides/self-hosting), [custom server](https://nextjs.org/docs/app/guides/custom-server). Packaging Node/app assets inside a self-extracting Go release is possible engineering, not an automatic Next feature; no prototype or artifact was produced. Avoid promising that Goja/V8 alone implements Next's Node runtime. [Node embedding limitations](https://nodejs.org/api/embedding.html).

Recommendation is an inference from the product's simplicity priority and currently settled dashboard requirements: Go HTTPS plus static export; supervised Next is the alternative if its request-time capabilities justify the additional runtime. No measured performance/size claim is made.

#### GitHub App and OAuth

Both GitHub Apps and OAuth Apps use OAuth. GitHub recommends GitHub Apps for finer permissions/repository selection. Installation tokens act as the app, may be repository/permission narrowed, expire in an hour and do not inherently enforce the launching user's GitHub rights. User tokens intersect app/user authority and may still cover several repositories. [App choice](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/deciding-when-to-build-a-github-app), [installation tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation), [user tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app).

Expiring GitHub App user tokens use refresh rotation; simultaneous requests need coordinated refresh. Keep refresh tokens/app private keys/client secrets on the controller, not in the ordinary application-secret resolver. Current OAuth App docs also support optional expiring tokens/refresh and PKCE; do not repeat the outdated claim that OAuth App tokens never expire. [App refresh](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens), [OAuth authorization](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps). Installation and user authorization are separate and may require GitHub organization approval. Token principal is distinct from Git commit author metadata. [Permissions](https://docs.github.com/en/enterprise-cloud%40latest/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app).

#### Dex and encryption-key custody

Dex warns SQLite is inappropriate for real workloads, gives no supported small-installation threshold, describes automatic SQL migrations without rollback, and calls the storage interface unstable. It stores identity-sensitive material including signing keys/password hashes; CloudBox's application-secret value encryption does not cover Dex by implication. Pinning/functional validation is CloudBox's proposed support responsibility, not evidence of upstream production endorsement. [Dex storage](https://dexidp.io/docs/configuration/storage/). Current v2.46.1 SQLite uses a C-backed driver and cgo; single-executable delivery is still possible with suitable builds. [Pinned SQLite implementation](https://github.com/dexidp/dex/blob/v2.46.1/storage/sql/sqlite.go).

macOS file-based login Keychain and data-protection Keychain have different signing/entitlement/packaging requirements; logged in does not guarantee unlocked. Use supported APIs to restore an application-owned key, not a copy of the entire Keychain DB, whose portability has additional restrictions in macOS 26.4+. [Apple TN3137](https://developer.apple.com/documentation/technotes/tn3137-on-mac-keychains), [Keychain locking](https://support.apple.com/en-tm/guide/keychain-access/kyca1242/mac). Native Go bindings may use cgo; specific library/Keychain type remains open. Backup recovery remains the accepted original-Mac/logical-installation scope; this research does not authorize cross-host VM migration.

### Research supporting Q116 through Q122

- UTM's documented command-line control wraps macOS AppleScript and requires UTM to remain open. Portable controller code or file-based key custody does not by itself create a remote UTM adapter. [UTM scripting](https://docs.getutm.app/scripting/scripting/), [headless operation](https://docs.getutm.app/advanced/headless/).
- Dex requires durable storage before authenticating an administrator. Its SQL open/migration path uses its configured credential; do not promise DML-only runtime access without a separately validated integration. Migrations have no rollback. Initial-admin hash/user-ID creation primitives permit a proposed constrained bootstrap cutover, not a proven whole-database migration. [Dex storage](https://dexidp.io/docs/configuration/storage/), [pinned SQL configuration](https://github.com/dexidp/dex/blob/v2.46.0/storage/sql/config.go), [account API](https://github.com/dexidp/dex/blob/v2.46.0/api/v2/api.proto).
- PostgreSQL database/role creation requires suitable CREATEDB/CREATEROLE authority, not necessarily a cluster superuser. CREATE DATABASE cannot participate in a single transaction with all later local-config/seed steps; resumable activation and target ownership checks are required. Schema isolation depends on privileges/search_path, not names alone. [CREATE DATABASE](https://www.postgresql.org/docs/current/sql-createdatabase.html), [CREATE ROLE](https://www.postgresql.org/docs/current/sql-createrole.html), [schemas](https://www.postgresql.org/docs/current/ddl-schemas.html). pg_dump gives a consistent snapshot of one database, not coordinated snapshots across multiple stores/local files; cluster roles are outside its scope. [pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html).
- Portable key storage can use an installation-specific cryptographic random key in a restricted file outside the DB, optionally supplied through a mounted secret file. Container key storage must persist beyond the container writable layer. Permission mechanisms differ: Go chmod 0600 does not enforce Windows ACL isolation. No raw key should be baked into the binary or casually transported in command arguments/environment. This protects DB-only exposure, not controller/root compromise. [Go crypto/rand](https://pkg.go.dev/crypto/rand), [Go permissions](https://pkg.go.dev/os#Chmod), [OWASP cryptographic storage](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html#key-storage), [Docker volumes](https://docs.docker.com/engine/storage/volumes/), [Compose secrets](https://docs.docker.com/compose/how-tos/use-secrets/). Key wrapping with an adjacent key does not create independent custody; passphrase sealing introduces an unlock requirement unless another trusted source supplies it. External KMS remains optional future scope, not an assumed dependency.
- GitHub App user tokens can create/update PRs with Pull requests permission; Git push uses Contents and workflow edits need separate Workflows permission. Checking status needs relevant Checks/Commit statuses read access. refs/pull/N/head can be fetched but is read-only; push to the actual head branch to update a PR. Fork write permission and app installation coverage are separate. [PR API](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request), [App permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app), [local PR checkout](https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/checking-out-pull-requests-locally?tool=webui).
- Git helpers do not automatically authenticate gh API calls. gh accepts GH_TOKEN; a wrapper can retrieve a current token, but static environment injection does not imply automatic refresh. PR review code may invoke the broad authorized secret resolver even when values are not automatically injected. Fork/untrusted-code policy follows the PR workflow decision. [gh environment](https://cli.github.com/manual/gh_help_environment), [GitHub secure-use guidance](https://docs.github.com/en/actions/reference/security/secure-use). These are integration implications, not implemented guarantees.

### Research supporting Q123 through Q138

These are source findings and proposed integration implications, not tested implementation guarantees. Read-only investigation found no applicable AGENTS.md in the ShiftBox documentation ancestors during this round.

- Dex v2.46.1 PostgreSQL configuration has no schema/search_path field, builds fixed connection parameters and migrates on Open. PostgreSQL supports per-role, per-database settings at login; separate schema-owner logins and validated search paths can place the tables without a fictitious Dex setting. Schema names alone do not isolate access. [Dex SQL config](https://github.com/dexidp/dex/blob/v2.46.1/storage/sql/config.go), [PostgreSQL role defaults](https://www.postgresql.org/docs/current/sql-alterrole.html), [schema/search-path trust](https://www.postgresql.org/docs/current/ddl-schemas.html).
- Dex's mutable-password API can accept a hash and explicit user ID. Preserving issuer/connector/user identity and ShiftBox membership with a fresh sign-in is a proposed constrained bootstrap transfer, not a documented full backend migration. Never silently fall back to old SQLite after PostgreSQL activation. [Password API](https://github.com/dexidp/dex/blob/v2.46.1/api/v2/api.proto), [local connector](https://dexidp.io/docs/connectors/local/).
- One PostgreSQL database supports shared exported snapshots across scoped sessions, but local files/keys and cross-service operations still need coordination. pg_dump excludes cluster roles; schema-filtered exports can omit dependencies. Client utility/server-major compatibility matters, and the Go binary does not automatically include those utilities. Tool packaging waits for the backup mechanism decision. [Snapshot synchronization](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-SNAPSHOT-SYNCHRONIZATION), [pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html).
- GitHub App Contents/Pull requests permissions cover Git and PR APIs; workflow-file changes require Workflows. Checks and Commit statuses are distinct APIs. Validate actual GraphQL/CLI operations instead of promising every gh command from a permission list. [App permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app), [PR API](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request), [Checks](https://docs.github.com/en/rest/checks/runs#list-check-runs-for-a-git-reference), [statuses](https://docs.github.com/en/rest/commits/statuses#get-the-combined-status-for-a-specific-reference).
- Refreshing an expiring GitHub App user token replaces and invalidates the old access/refresh pair. Shared-owner workspaces need coordinated refresh; an already-running tool can retain a stale access token. gh accepts GH_TOKEN, which is distinct from the Git credential helper. [Refresh behavior](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens), [gh environment](https://cli.github.com/manual/gh_help_environment).
- GitHub permits webhook delivery to be disabled. A private controller can use browser-reachable OAuth callbacks plus outbound GitHub access; UI recognition of revocation can lag until an API check even though GitHub already enforces it. An app restricted to its own account cannot be installed on personal accounts; making an app installable on other accounts does not require Marketplace publication or public controller ingress. [App registration](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app), [callback](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-user-authorization-callback-url), [app visibility](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/making-a-github-app-public-or-private).
- LFS and submodules use separate endpoint/repository requests; credentials and network policy must be applied to each. Repository configuration is not authority to widen either. [Git LFS](https://github.com/git-lfs/git-lfs/blob/main/CONTRIBUTING.md), [submodules](https://git-scm.com/docs/git-submodule).
- Dex's live SQL state includes serialized signing keys and client secrets; its earlier field encryption was not ported to v2. ShiftBox value encryption does not cover those writes by implication. Disk/transport encryption has different boundaries than field encryption and does not hide live records from an authorized database administrator. [Dex storage](https://dexidp.io/docs/configuration/storage/), [pinned SQL writes](https://github.com/dexidp/dex/blob/v2.46.1/storage/sql/crud.go), [PostgreSQL encryption](https://www.postgresql.org/docs/current/encryption-options.html).
- Versioned keys and resumable re-encryption preserve recovery through rotation. Each existing encrypted archive retains its own recovery material/password; live-key rotation neither rewrites nor revokes copied archives. Supplied mounted-key export into the archive is a product custody decision, not a cryptographic requirement. [OWASP rotation](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html#key-lifetimes-and-rotation), [age passphrase encryption](https://pkg.go.dev/filippo.io/age).

### Research supporting Q139 through Q147

Read-only investigation again found no applicable AGENTS.md in the ShiftBox documentation ancestors. No implementation/network experiments or credential access occurred.

- GitHub explicitly supports user and installation access tokens for HTTPS Git when appropriate app permissions are granted. The app-signed JWT authenticates designated app API operations and is not the documented Git credential. The intended user-authority design therefore exposes a GitHub App user OAuth access token and treats its representation as opaque. Do not incorrectly claim that all GitHub access tokens can never be JWTs: GitHub completed the stateless installation-token rollout on October 2, 2026, without changing installation-token authority. [Git access](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app#choosing-permissions-for-git-access), [app JWT](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app), [installation-token rollout](https://github.blog/changelog/2026-10-02-stateless-github-app-installation-tokens-rolled-out/).
- GitHub App user tokens expire after eight hours by default; refresh tokens last six months. Expiration can be configured, so eight hours is not asserted as an immutable universal limit. Refreshing replaces/invalidate the old pair. No retained refresh token means another authorization is needed after expiration; controller-memory-only storage also loses the token on restart. These are consequences to decide, not proof that the user already accepted persistence. App installation/user authorization still intersect; exposing a token does not confer arbitrary private fork access. [Refresh behavior](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens), [user authority](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-with-a-github-app-on-behalf-of-a-user).
- A single-label hostname layout (app.base, ide-ID.base, p-ID-port.base) can fit one *.base certificate; nested host labels do not match that certificate. DNS wildcard synthesis and TLS matching are different rules. [RFC 9525 wildcard matching](https://www.rfc-editor.org/rfc/rfc9525.html#section-6.3), [DNS wildcards](https://www.rfc-editor.org/rfc/rfc4592.html#section-3.3.1).
- Separate subdomain origins can remain same-site. SameSite cookies alone are insufficient CSRF protection against hostile sibling preview content. Use host-scoped authentication, explicit origin/CSRF checks, and no parent-domain management cookie. __Host- cookies forbid a Domain attribute under the cookie specification; browser acceptance still requires validation. [HTML origin/site definitions](https://html.spec.whatwg.org/multipage/browsers.html#same-site), [cookie prefixes](https://httpwg.org/http-extensions/draft-ietf-httpbis-rfc6265bis.html#name-the-host-prefix), [CSRF guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html).
- Authenticated preview navigation can exchange a short-lived one-use server-side grant for a preview-host cookie, stripped before forwarding to the guest. This is an integration proposal, not a new product-facing endpoint requirement. Browser WebSocket handshakes include credentials but reject redirects; authenticate normal navigation first and authorize the upgrade. Cookie expiry alone does not close an existing socket, so accepted access revocation must be enforced explicitly. [WebSocket handshake](https://websockets.spec.whatwg.org/#opening-handshake), [WebSocket security](https://www.rfc-editor.org/rfc/rfc6455.html#section-10.2).

### Research and closure audit after Q147

- Web HTTPS needs a certificate and matching private key before the first TLS handshake. Go supports validating a PEM pair and selecting a replacement via TLS callbacks. A UI-only certificate upload therefore needs an explicitly selected first-run HTTPS path, such as initial file inputs or a locally trusted loopback-only generated certificate; generated localhost TLS is not automatically browser-trusted. Certificate hostname coverage must include the configured service hosts. [Go HTTPS](https://pkg.go.dev/net/http#Server.ListenAndServeTLS), [TLS key pair](https://pkg.go.dev/crypto/tls#LoadX509KeyPair), [TLS configuration](https://pkg.go.dev/crypto/tls#Config), [localhost certificates](https://letsencrypt.org/docs/certificates-for-localhost/), [hostname verification](https://pkg.go.dev/crypto/x509#Certificate.VerifyHostname).
- CA trust anchors, leaf certificate validity and replacement cadence are different. The TLS handshake selects a server certificate before HTTP path routing. Four-hour mTLS rotation cannot silently apply to uploaded browser TLS or imply root replacement every four hours. Certificate notBefore/notAfter define validity; pre-expiry renewal/overlap must be explicit. [RFC 5280 trust anchors](https://www.rfc-editor.org/rfc/rfc5280.html#section-6.1.1), [validity](https://www.rfc-editor.org/rfc/rfc5280.html#section-4.1.2.5), [Go TLS](https://pkg.go.dev/crypto/tls#Config).
- Certificate replacement does not automatically reauthenticate or terminate established tunnels. TLS KeyUpdate replaces traffic keys, not identity certificates; tunnel reconnect/re-authentication and immediate revocation are application obligations. Session resumption must honor current authorization; Go VerifyConnection runs for resumed sessions while VerifyPeerCertificate does not. [TLS authentication](https://www.rfc-editor.org/rfc/rfc8446.html#section-4.4), [KeyUpdate](https://www.rfc-editor.org/rfc/rfc8446.html#section-4.6.3), [Go TLS callbacks](https://pkg.go.dev/crypto/tls#Config).
- Stopped/offline VMs cannot participate in four-hour rotation. Preserve previously accepted provider-authorized re-enrollment for expired known identities and no renewal based solely on an expired/revoked credential. Decide automatic versus explicit recovery authorization; do not invent an expiry bypass or claim a stopped VM stays credential-current. [Go certificate verification](https://pkg.go.dev/crypto/x509#Certificate.Verify).
- The full-record closure audit separates scope decisions from implementation proof. Q139 authorizes persistence, not necessarily refresh-token retention; Q145 introduces the remaining material certificate-lifecycle ambiguity. Failure/operations and external-identity contracts can be consolidated for final approval. Do not reopen rejected managed Git tooling or phase-II features merely to close the design tree.

### Final closing round fact check (Q148–Q155)

No new implementation or credential access occurred. Read-only checking again found no applicable AGENTS.md in the ShiftBox documentation ancestors.

- The one-database bootstrap/backup and TLS constraints previously researched remain. The proposed four-hour leaf replacement/eight-hour validity is a deliberate product recommendation needing approval; eight hours is not an X.509 requirement. Loopback TLS bootstrap needs explicit local trust, and uploaded certificates require a matching private key. No production HTTP/temporary-certificate fallback is implied.
- External OIDC reauthorization/refresh can confirm upstream acceptance of a grant but cannot universally prove a directory account is enabled. Refreshed ID tokens retain the original authentication time; max_age/auth_time semantics require verified broker/provider support. Inspected Dex v2.46.1 refreshes upstream grants and optionally calls UserInfo but does not forward max_age in the inspected authorization builder. Therefore Q152 proposes a bounded upstream-authorization deadline, not universal forced fresh password authentication or a bounded directory-disablement detection SLA. [OIDC stable identity](https://openid.net/specs/openid-connect-core-1_0.html#ClaimStability), [OIDC refresh response](https://openid.net/specs/openid-connect-core-1_0.html#RefreshTokenResponse), [Dex OIDC implementation](https://github.com/dexidp/dex/blob/v2.46.1/connector/oidc/oidc.go), [Dex connector configuration](https://dexidp.io/docs/connectors/oidc/).
- Retention-authorized force stop/delete is a new proposed closure of hung-work behavior, not a reinterpretation of Q98. Fresh activity, explicit policy enablement, outage grace and confirmed cleanup remain mandatory. No workspace-data backup or recoverability after destruction is introduced.
- Release rollback cannot be promised by restoring an older binary alone after irreversible schema migration. The proposed contract requires a coordinated management backup/matching release and the existing recovery gate; external VM/Git effects are not rewound. Required backup utilities must be bundled or explicitly validated as prerequisites rather than hidden dependencies.

### Ona and control-plane design

- [Ona's October 31, 2024 article](https://ona.com/stories/we-are-leaving-kubernetes) describes CDE difficulties involving state, isolation and operational overhead. Its replacement retained declarative APIs and control loops. It supports reconsidering the workspace substrate, not a blanket claim that Kubernetes cannot scale or cannot host the control plane.

### Host, UTM and Packer

- Read-only host inspection found ARM64 macOS 26.6.2 and UTM 5.0.8. These describe this machine, not a settled universal support matrix.
- UTM automation operates through the native application and AppleScript/utmctl. Headless operation still requires UTM open. Startup before login, logout behavior, permissions, sleep and host-restart recovery need validation. See [scripting](https://docs.getutm.app/scripting/scripting/) and [headless mode](https://docs.getutm.app/advanced/headless/).
- AMD64 guests on ARM64 hosts require QEMU emulation. No startup/build performance claim or multicore correctness claim has been validated for this configuration. See [UTM architecture settings](https://docs.getutm.app/settings-qemu/system/).
- UTM network modes do not establish arbitrary CIDR/port egress filtering. Shared/NAT modes may allow host or peer access. Port forwarding is not equivalent to an outbound allowlist. See [Apple networking](https://docs.getutm.app/settings-apple/devices/network/), [QEMU networking](https://docs.getutm.app/settings-qemu/devices/network/network/), and [forwarding](https://docs.getutm.app/settings-qemu/devices/network/port-forwarding/).
- The Packer plugin's released v4.0.0 explicitly accepts UTM 4.5/4.6/4.7 and rejects other major/minor versions. See [tagged driver](https://github.com/naveenrajm7/packer-plugin-utm/blob/v4.0.0/builder/utm/common/driver.go).
- [Open PR 50](https://github.com/naveenrajm7/packer-plugin-utm/pull/50) adds UTM 5.0 support by extending the existing driver. Its author reported a Debian ARM64 test on UTM 5.0.3, not our exact Alpine amd64 environment. It is a starting point, not acceptance evidence.
- Packer builders can export `.utm` bundles. The ordinary runtime can consume artifacts without Packer installed. See [builder documentation](https://github.com/naveenrajm7/packer-plugin-utm/blob/main/docs/README.md).
- UTM clone creates a separately owned package and a fresh UUID; filesystem cloning can share unchanged APFS blocks. MAC regeneration is preference-dependent. Do not assume every cloned identity is fresh or every external disk is covered by bundle deletion. See [UTM 5.0.8 clone implementation](https://github.com/utmapp/UTM/blob/v5.0.8/Platform/UTMData.swift#L516) and [drive ownership](https://docs.getutm.app/settings-qemu/drive/drive/).

### Alpine, developer tools and identity

- Alpine's musl environment requires deliberate tool installation. The generic Dev Containers standalone installer does not establish Alpine compatibility; Alpine Node/npm plus a pinned CLI is a researched candidate, not an accepted/tested recipe. See [CLI installer](https://github.com/devcontainers/cli/blob/main/scripts/install.sh) and [CLI documentation](https://github.com/devcontainers/cli).
- The existing code-server standalone binary has glibc requirements in the project container. Alpine project containers need a different installation path or an explicit compatibility limit; using an Alpine platform VM does not resolve or mandate that choice. See [code-server installation](https://coder.com/docs/code-server/install).
- Signed or mutually authenticated messages cannot establish truthful reports from a compromised legitimate signer. See [TLS 1.3](https://www.rfc-editor.org/rfc/rfc8446.html) and [HTTP signature security considerations](https://www.rfc-editor.org/rfc/rfc9421.html#section-7).
- EC2 reboot, stop/start and termination have distinct storage semantics, and termination does not inherently delete every volume. The future provider must explicitly implement CloudBox's preservation/cleanup contract. See [EC2 lifecycle](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-lifecycle.html) and [volume persistence](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/preserving-volumes-on-termination.html).

No new architecture prototype, VM build, fork, migration, or performance test has been executed as part of this interview.

## Design tree and review frontier

All product-contract branches through Q155 have recorded outcomes. The requested research has been incorporated; no additional interview question is currently pending. Proposed engineering defaults and validation work are explicit and do not silently change accepted behavior.

```text
ShiftBox: simplicity, owned workspace lifecycle, provider-managed infrastructure
├─ Product/deployment: Go + static React/Next.js UI with Mantine; native UTM first; EC2/Linux phase II
│  ├─ Artifacts: Packer, pinned Ubuntu Minimal amd64, immutable catalog
│  └─ Operations: manual start, logged-in/awake host, release prerequisite checks
├─ Workspaces: one VM + primary devcontainer; owner-private browser IDE
│  ├─ Durable state/operations -> reconciliation, capacity, repair, retained files
│  ├─ Provider capabilities -> truthful network options and stopped-policy updates
│  ├─ Inactivity -> enabled retention -> final checks/grace -> verified deletion
│  └─ N/N−1 upgrades -> safe maintenance -> rollback/failure gate
├─ Access: Dex OIDC + ShiftBox permissions -> local bootstrap / external authorization
│  ├─ Admin/member -> owner-private workspaces; explicit audited admin connection
│  ├─ Browser origins -> private IDE/previews; scoped credentials
│  └─ External OIDC -> configurable interval, default 4h, explicit offboarding
├─ Secrets/Git: User > Project Template > Organization; no workspace scope
│  ├─ Shared template -> current access; snapshots preserve configuration only
│  └─ Developer-managed tools -> encrypted user OAuth persistence and renewal
├─ Trust: untrusted entire guest; infrastructure authority stays on host/controller
│  ├─ Browser TLS -> loopback bootstrap -> uploaded validated chain/key
│  └─ Internal mTLS -> 4h rotation / 8h validity / stable CA
│     └─ Long shutdown -> verified provider re-enrollment; revoked identity denied
├─ State: SQLite demo / external PG production with separate application/Dex schemas
│  ├─ Portable key custody -> encrypted application values; key rotation phase II
│  └─ Coordinated encrypted backup -> replacement PG -> admin-reviewed recovery
└─ Closure: design series + bounded defaults + functional/security validation gates
   └─ CURRENT REVIEW FRONTIER: user confirms final readback/shared understanding
      └─ Separate implementation authorization
```

See [validation and engineering defaults](validation-and-engineering-defaults.md) for release gates, technical choices and deferrals. Scale/load testing remains deferred until product completion. A failed validation that requires a product/security tradeoff returns to the user rather than silently weakening this tree. Design approval alone does not establish a production-proven release.

## Diagram and repository preservation

The latest supplied drawing is `/Users/jflatten/Downloads/ShiftBox Architecture.pdf`. It contains UI, Controller API, Platform Provisioning and Proxy services in the binary; a platform VM containing the DevContainer Provisioning Service and a devcontainer; and retained-path labels `/home/vscode`, `/opt/shiftbox`, `/workspaces`. It shows a user-to-proxy-to-devcontainer path. The polling/tunnel directions and later decisions in this record clarify the high-level drawing; its arrows are not a protocol specification. The external PDF has been revised in place and is not copied into this checkpoint.

Preserve this existing checkout and its unrelated work. The implementation will be a new greenfield project: do not migrate current code or rewrite its specifications as if it were the implementation target. The user has selected the ShiftBox project folder, authorized relocation/update of this decision record, and at Q155 authorized the design document series and diagrams. New scaffolding and implementation specifications await final shared-understanding confirmation and subsequent authorization. Existing data import/API compatibility is not implied; the old installation remains protected.

Preserve the original `cloudbox-us1` Docker Desktop installation at `.cache/us1-installation`, its database, Keycloak data, TLS files, and private bootstrap credentials. No credential values belong in this record. Any later local Compose testing must follow the repository's existing installation instructions; this checkpoint does not authorize rebuilding or resetting it.
