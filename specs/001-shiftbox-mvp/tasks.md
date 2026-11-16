---

description: "Dependency-ordered implementation tasks for the ShiftBox MVP"
---

# Tasks: ShiftBox MVP

**Input**: Design documents from `/specs/001-shiftbox-mvp/`

**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, and `quickstart.md`

**Tests**: Required by the specification, constitution, and project instructions. Every test task below MUST be completed before its paired implementation task, MUST first demonstrate a meaningful failure, and every test MUST include a comment explaining the real-world behavior or failure it covers. An implementation task may not start until its behavior is covered by an earlier failing test task; adding uncovered behavior requires adding or extending the earlier test task first.

**Organization**: Tasks are grouped by user story so each story can be implemented and tested as an independently demonstrable increment. Exact package boundaries follow `plan.md`; do not add services, abstractions, or deferred product scope.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it owns different files and has no dependency on an incomplete task
- **[Story]**: Maps the task to a specification user story
- Every task names the exact file or directory it changes

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the planned Go, static UI, platform, and validation skeleton without implementing product behavior.

- [X] T001 Create the planned controller, guest, internal package, migration, UI, platform, and test directory skeleton with package documentation placeholders in `cmd/`, `internal/`, `migrations/`, `ui/`, `platform/`, and `tests/`
- [X] T002 Initialize the single Go 1.27.4 module and pin the approved direct dependencies from `plan.md` in `go.mod` and `go.sum`
- [X] T003 [P] Initialize the Node 26 LTS static UI workspace with Next.js 16, React 19.3, TypeScript, Mantine 9, TanStack Query 5, Jest, React Testing Library, and Playwright in `ui/package.json` and `ui/package-lock.json`
- [X] T004 [P] Configure Next.js static export, strict TypeScript, Jest, and Playwright projects without production server features in `ui/next.config.ts`, `ui/tsconfig.json`, `ui/jest.config.ts`, and `ui/playwright.config.ts`
- [X] T005 [P] Configure Go formatting, vet, race, unit, integration, UI, browser, and opt-in release commands in `Makefile` and `.github/workflows/ci.yml`
- [X] T006 [P] Configure checked-in release-manifest and platform-artifact schemas for pinned controller, Dex, UTM, Packer fork, Ubuntu image, guest, IDE, protocol, and digest metadata in `platform/manifests/release.schema.json` and `platform/manifests/artifact.schema.json`
- [X] T007 [P] Copy only the approved ShiftBox light/dark logos and required brand assets into `ui/public/brand/` and record their source mapping in `ui/public/brand/README.md`
- [X] T008 [P] Add package-level test helpers for deterministic clocks, UUIDs, and commented-test enforcement in `internal/testsupport/clock.go`, `internal/testsupport/ids.go`, and `internal/testsupport/test_comments_test.go`
- [X] T009 Document local prerequisites and the future validation commands from `quickstart.md` in `README.md` without claiming unimplemented commands work

**Checkpoint**: The repository structure and build/test entry points match the approved plan; no product behavior exists yet.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Implement the smallest shared trust, persistence, operation, HTTP, and UI primitives required by every user story.

**Critical**: No user-story implementation begins until this phase passes its unit and contract tests.

### Tests first

- [X] T010 [P] Write failing SQLite/PostgreSQL migration and configuration tests for equivalent schema versions, rollback boundaries, transaction behavior, operator-owned PostgreSQL schemas, explicit configuration paths, invalid values, and redacted errors in `tests/contract/database_migrations_test.go` and `internal/config/config_test.go`
- [X] T011 [P] Write failing protected-value and internal-PKI tests for AES-256-GCM random nonces, contextual AAD, tamper/wrong-record rejection, plaintext omission, stable installation CA loading, TLS 1.3 verification, and invalid key/certificate rejection in `internal/cryptostore/store_test.go` and `internal/guest/ca_test.go`
- [X] T012 [P] Write failing operation-journal and reconciler tests for unique `(requested_by, request_key)`, generation fencing, renewable leases, monotonic events, preserved completed steps, atomic success evidence, commit-before-effect ordering, and restart-safe work claiming in `internal/operation/journal_test.go` and `internal/operation/reconciler_test.go`
- [X] T013 [P] Write failing session, authorization-matrix, Dex, and OIDC tests for opaque token digests, membership/session gates, every route-to-action mapping in `contracts/authorization-matrix.md`, owner/non-owner/admin exact-grant decisions, CSRF, exact Origin, stable upstream identity, supervisor failure, and revoke-before-close ordering in `internal/auth/session_test.go`, `internal/auth/policy_test.go`, and `internal/identity/dex_test.go`
- [X] T014 [P] Write failing HTTP/proxy boundary tests for RFC 3339 timestamps, UUIDs, stable problems, request IDs, conditional versions, idempotency keys, body limits, unknown-field rejection, management-header stripping, service allowlisting, and OpenAPI route/action drift in `internal/web/problem_test.go`, `internal/web/middleware_test.go`, `internal/proxy/sanitize_test.go`, and `tests/contract/openapi_routes_test.go`
- [X] T015 [P] Write failing SSE tests for ordered event IDs, Last-Event-ID replay, sequence-gap recovery, authorization recheck, and immediate closure on revocation in `internal/web/events_test.go`
- [X] T016 [P] Write failing UI component tests for server-derived Mantine Stepper state, disabled future steps, retained failure history, accessible labels, live status semantics, and refetch on SSE gaps in `ui/components/workflow-stepper.test.tsx`

### Shared implementation

- [X] T017 Implement the embedded Goose runner, explicit SQLite/PostgreSQL openers, transaction helpers, and schema ownership checks proven by T010 in `internal/database/database.go`, `internal/database/migrate.go`, and `internal/database/transaction.go`
- [X] T018 Implement base installation/configuration/session/operation migrations in `migrations/sqlite/00001_foundation.sql` and `migrations/postgres/00001_foundation.sql`, preserving these verbatim constraints: Installation `id` is a "Stable opaque UUID; exactly one active installation record", `state` is "`bootstrap`, `configuring`, `active`, `recovery`, or `blocked`", `organization_name` is a "Non-empty display name", `data_profile` is "`sqlite` or `postgres`; immutable after active configuration except guarded restore activation", `management_origin` is a "Canonical HTTPS origin; unique and distinct from IDE/preview patterns", `ide_host_pattern` "maps exactly one workspace ID", `preview_host_pattern` maps workspace/service identity, `external_auth_interval_seconds` is a "Positive finite value; initial default 14,400 seconds", and `config_generation` "Increments on durable configuration activation"; ActivatedConfiguration generation "increases exactly by one" and "the last active generation is unique"; BrowserCertificate chain "covers every configured service hostname", fingerprint is unique, and state is "`pending`, `active`, `replaced`, or `invalid`"; Session `token_digest` is a "Unique digest of a random cookie value; raw token is never stored" and mode is "`bootstrap_restricted`, `password_change`, `normal`, or `recovery_review`"; Operation `(requested_by, request_key)` is unique, type is "`provision`, `start`, `open_ide`, `rebuild`, `repair`, `stop`, `delete`, `recover`, `update`, `backup`, `restore`, or setup operation", status is "`pending`, `running`, `waiting`, `succeeded`, `failed`, or `cancelled`", step status is "`pending`, `active`, `succeeded`, `failed`, or `skipped`", and `event_id` is "Monotonic per operation; used for SSE replay"
- [X] T019 Implement authenticated encryption and protected installation-key file loading proven by T011 in `internal/cryptostore/store.go` and `internal/cryptostore/keyfile.go`
- [X] T020 [P] Implement typed configuration loading with explicit paths, no environment reads below composition, and redacted validation errors in `internal/config/config.go` and `internal/config/validate.go`
- [X] T021 Implement the durable operation journal, leases, ordered steps/events, generation checks, and idempotent request lookup proven by T012 in `internal/operation/model.go`, `internal/operation/store.go`, and `internal/operation/journal.go`
- [X] T022 Implement reusable reconciler scheduling that commits intent before external calls and commits observed evidence afterward in `internal/operation/reconciler.go` and `internal/operation/worker.go`
- [X] T023 Implement User, IdentityBinding, Session, and ResourceGrant stores and policy evaluation proven by T013 in `internal/auth/model.go`, `internal/auth/store.go`, `internal/auth/session.go`, and `internal/auth/policy.go`, preserving these verbatim constraints: User `primary_email` is a "Normalized email for display/local login; not a cross-provider merge key", `display_name` is optional, `role` is "`member` or `administrator`", `membership_state` is "`pending`, `active`, or `disabled`", and `credential_mode` is "`local` or `external`"; IdentityBinding `issuer` is an "Exact canonical issuer string", `subject` is an "Exact non-empty provider subject", `(issuer, subject, connector_id)` is unique, and "Email never creates a binding automatically"; Session is usable only when membership, mode, time bounds, and installation gate permit it; ResourceGrant `action` is an enumerated permission, only active grants participate, and administrator connection requires a distinct active grant plus audit event
- [X] T024 Implement managed Dex child-process configuration, health supervision, OIDC relying-party verification, and clean shutdown in `internal/identity/dex.go`, `internal/identity/oidc.go`, and `internal/identity/supervisor.go`
- [X] T025 Implement `/session` read/logout handlers, management session cookies, CSRF/Origin middleware, authorization middleware, version/idempotency parsing, stable problems, and redacted structured request logging proven by T013-T014 in `internal/web/session_routes.go`, `internal/web/middleware.go`, `internal/web/problem.go`, and `internal/web/logging.go`
- [X] T026 Implement authorized operation SSE replay and connection closure proven by T015 in `internal/web/events.go` and `internal/auth/revocation.go`
- [X] T027 [P] Implement installation ECDSA P-256 CA loading plus TLS 1.3 server/client certificate verification primitives without guest enrollment behavior in `internal/guest/ca.go` and `internal/guest/tls.go`
- [X] T028 Implement management/IDE/preview connection registries and a reverse-proxy sanitizer that strips ShiftBox cookies, CSRF tokens, origin grants, and hop-by-hop headers in `internal/proxy/registry.go` and `internal/proxy/sanitize.go`
- [X] T029 Implement the static Mantine AppShell, ShiftBox theme/branding, role-aware navigation, persistent Alert region, TanStack Query client with no persistence, API problem view, and reusable WorkflowStepper proven by T016 in `ui/app/layout.tsx`, `ui/app/providers.tsx`, `ui/components/app-shell.tsx`, `ui/components/workflow-stepper.tsx`, and `ui/lib/query-client.ts`
  - [X] Move Sign out to the bottom of the shared left navigation tray; verify the restricted-session sign-out regression, typecheck, production build, and placement in the updated running demo.

**Checkpoint**: Database profiles, protected values, authentication primitives, durable operations, HTTP boundaries, SSE, connection revocation, and the visible workflow shell are testable shared foundations.

---

## Phase 3: User Story 1 — Launch and Use a Development Workspace (Priority: P1) — MVP

**Goal**: An operator activates a protected installation, creates an approved template, and a member launches, opens, stops/starts, rebuilds, and repairs one owner-private UTM workspace while designated files persist.

**Independent Test**: From a new supported installation, complete setup, create one template, launch one workspace, open the IDE, edit retained files, stop/start, rebuild, and enter repair; verify ordered progress, no duplicate provider effects, and retained data.

### Tests first

- [X] T030 [P] [US1] Write failing setup and controller CLI contract tests for protected loopback access, one-time temporary password, forced change, resumable steps, certificate/profile validation, activation, bootstrap-session retirement, fresh sign-in, `shiftbox serve`, and secret-safe command output in `tests/contract/setup_api_test.go` and `cmd/shiftbox/main_test.go`
- [X] T031 [P] [US1] Write failing setup integration tests for SQLite bootstrap, PostgreSQL ownership/TLS/schema preflight, activation copy of only initial administrator/setup state, and prohibition on SQLite fallback in `tests/integration/setup_activation_test.go`
- [X] T032 [P] [US1] Write the reusable failing provider contract suite for inspect-before-retry, idempotent create/start, complete inventory, partial deletion, shared-resource preservation, generation fencing, and typed failures in `tests/contract/provider_suite_test.go`
- [X] T033 [P] [US1] Write failing platform-image and UTM adapter tests for pinned Packer inputs, Ubuntu 24.04 Minimal amd64, supervised guest agent, VS Code Browser, immutable artifact manifest/digests, import/boot health, normalized complete inventory, preserved drive lists, supported capabilities, lost responses, and no direct bundle edits in `tests/release/platform_image_test.go` and `internal/provider/utm/provider_test.go`
- [X] T034 [P] [US1] Write failing guest protocol tests for mTLS-derived identity, N/N−1 compatibility, poll replay, operation fencing, enrollment replay/alternate-key denial, and readiness evidence in `tests/contract/guest_protocol_test.go`
- [X] T035 [P] [US1] Write failing workspace-domain tests for pinned launch snapshots, capacity reservation timing, desired/observed separation, owner privacy, lifecycle transitions, repair automation suppression, and retained-data contracts in `internal/workspace/service_test.go`
- [X] T036 [P] [US1] Write failing workspace API tests for `/workspaces`, lifecycle actions, operation lookup/events, conditional versions, idempotency, capacity problems, and short-lived entry grants in `tests/contract/workspace_api_test.go`
- [X] T037 [P] [US1] Write failing guest executor tests for persisted operation attempts, repository preparation, prebuilt-image and Dockerfile/build-definition Dev Containers, declared features, lifecycle-hook ordering/failure, separate declared `waitFor` and proxy readiness, rebuild retention checks, and repair without project automation in `internal/guest/executor_test.go`
- [X] T038 [P] [US1] Write failing UI tests for setup forms, first-invalid focus, launch review, capacity refusal, server-driven progress, desired/observed state, retained-data copy, and repair explanation in `ui/features/setup/setup.test.tsx` and `ui/features/workspaces/workspaces.test.tsx`
- [X] T039 [P] [US1] Write failing Playwright MVP journey for keyboard-only setup, launch through SSE reconnect, delayed IDE readiness, file persistence across stop/start/rebuild, and repair access in `ui/tests/e2e/mvp-workspace.spec.ts`

### Models and stores

- [X] T040 [US1] Add platform/workspace/provider/guest/origin migrations in `migrations/sqlite/00002_workspace.sql` and `migrations/postgres/00002_workspace.sql`, preserving these verbatim constraints: PlatformArtifact `release_id` is a "Unique human-readable release identity", `architecture` "Must be `amd64` for MVP", `is_default` is "At most one compatible default", state is "`validating`, `available`, or `rejected`", and available content/manifest fields are immutable; ResourceProfile `name` is "Unique, non-empty name" and resource values are "Positive values inside provider capability bounds"; CapacityBudget limits are "Positive configured totals"; Workspace `owner_user_id` is "Immutable owner context", resource values are "Pinned resolved resources", desired state is "`stopped`, `running`, or `deleted`", observed state is "`unknown`, `provisioning`, `stopped`, `starting`, `running`, `unreachable`, `stopping`, `deleting`, `deleted`, or `failed`", `provider_instance_id` is "Null until reconciled creation; unique while live", and deleted has "no transition"; Reservation has "Unique active reservation per workspace", CPU/memory release requires confirmed stop/deletion, disk release requires verified complete deletion, and unknown outcomes retain reservations; ProviderResource type is "`vm`, `disk`, `media`, `network`, or adapter-defined owned type" and deletion succeeds only when all owned resources are absent; GuestRegistration state is "`eligible`, `active`, `expired`, `revoked`, or `deleted`" and revoked/deleted registrations cannot automatically re-enroll; EnrollmentGrant ID is a "Random high-entropy identifier stored as a digest", state is "`issued`, `consumed`, `expired`, or `invalidated`", expires "Initially 15 minutes after minting", and is single use; OriginGrant has opaque token digest and exact user/resource/service/origin binding
- [X] T041 [US1] Add ProjectTemplate and TemplateRevision migrations needed for single-owner launch in `migrations/sqlite/00003_templates.sql` and `migrations/postgres/00003_templates.sql`, preserving these verbatim constraints: ProjectTemplate `owner_user_id` means "Owner; administrator management does not silently transfer ownership", `name` is "Non-empty display name", `state` is "`active` or `archived`", and `current_revision` is an "Existing immutable revision number"; TemplateRevision `(template_id, revision)` is a "Composite immutable identity", `repository_url` is an "HTTPS GitHub.com URL in MVP", `devcontainer_path` is a "Normalized repository-relative path", and `secret_names_json` contains "References only; never plaintext values"
- [X] T042 [US1] Implement installation, activated configuration, browser certificate, platform artifact, resource profile, capacity, template revision, workspace, reservation, provider resource, guest registration, enrollment grant, and origin grant stores in `internal/config/store.go`, `internal/release/store.go`, `internal/template/store.go`, `internal/workspace/store.go`, and `internal/guest/store.go`

### Setup and provider implementation

- [X] T043 [US1] Implement resumable protected setup state, first-administrator creation, one-time password output, password-change gate, and exact activation transitions proven by T030 in `internal/app/setup.go` and `cmd/shiftbox/setup.go`
- [X] T044 [US1] Implement SQLite bootstrap and PostgreSQL profile preflight/activation with separate ShiftBox/Dex ownership and no fallback after activation proven by T031 in `internal/database/profile.go` and `internal/app/activate.go`
- [X] T045 [US1] Implement browser certificate chain/key/hostname validation and atomic configuration activation in `internal/config/certificate.go`
- [X] T046 [US1] Build the pinned Packer Ubuntu 24.04 Minimal amd64 artifact with supervised guest agent and VS Code Browser, then implement immutable artifact import, manifest/digest/provenance validation, default selection, resource profiles, capacity budget, and provider capability admission proven by T033 in `platform/packer/shiftbox.pkr.hcl`, `platform/guest/`, `internal/release/catalog.go`, and `internal/workspace/capacity.go`
- [X] T047 [US1] Define the consumer-owned Provider interface, complete inventory/types, semantic errors, fake provider, and shared suite adapter proven by T032 in `internal/provider/provider.go`, `internal/provider/types.go`, `internal/provider/errors.go`, and `internal/provider/fake/fake.go`
- [X] T048 [US1] Implement the compiled UTM provider using only the supported local scripting surface, complete inventory comparison, serialized mutations, inspect-after-effect, and preserved drive lists proven by T033 in `internal/provider/utm/provider.go`, `internal/provider/utm/inventory.go`, and `internal/provider/utm/script.go`

### Guest, workspace, API, and UI implementation

- [X] T049 [US1] Implement provider-mediated one-use enrollment, CSR key-proof retry, controller-owned certificate fields, and four-hour replacement/eight-hour validity in `internal/guest/enrollment.go` and `internal/guest/certificates.go`
- [X] T050 [US1] Implement authenticated guest poll handling with derived identity, one active operation, generation fencing, bounded evidence, 30-second jittered cadence, and N/N−1 checks proven by T034 in `internal/guest/protocol.go` and `internal/web/guest_routes.go`
- [X] T051 [US1] Implement `shiftbox-agent serve` supervision and persistent operation attempt/step journal in `cmd/shiftbox-agent/main.go`, `internal/guest/agent.go`, and `internal/guest/local_journal.go`
- [X] T052 [US1] Implement the fixed approved guest command executor for repository preparation, prebuilt-image and Dockerfile/build-definition Dev Containers, declared features, lifecycle hooks, declared connection-ready stage, rebuild, IDE verification, repair entry/exit, and graceful shutdown proven by T037 in `internal/guest/executor.go`, `internal/guest/devcontainer.go`, and `internal/guest/repair.go`
  - [X] Apply the developer's UID and GID throughout the retained home on initial seed, restart, and rebuild before remote hooks run.
- [X] T053 [US1] Implement guest-initiated mTLS Yamux registration, one stream per HTTP/WebSocket request, service allowlisting, heartbeats, and expiry/revocation closure in `internal/guest/tunnel.go` and `internal/proxy/tunnel.go`
- [X] T054 [US1] Implement template creation/revision validation and workspace admission that snapshots owner, template revision, repository, resources, platform, network policy, and secret references before reserving capacity in `internal/template/service.go` and `internal/workspace/launch.go`
- [X] T055 [US1] Implement provision/start/stop/rebuild/repair/open-IDE reconciliation with inspect-before-retry, desired/observed separation, durable steps, readiness evidence, and reservation release rules in `internal/workspace/reconciler.go` and `internal/workspace/lifecycle.go`
- [X] T056 [US1] Implement the setup, template, workspace, operation, event, and entry-grant OpenAPI routes proven by T030 and T036 in `internal/web/setup_routes.go`, `internal/web/template_routes.go`, `internal/web/workspace_routes.go`, and `internal/web/operation_routes.go`
- [X] T057 [US1] Implement owner/audited-admin IDE and preview routing that exchanges a short-lived exact-origin grant for a separate host-only cookie and never forwards management authority in `internal/proxy/grants.go`, `internal/proxy/router.go`, and `internal/web/proxy_routes.go`
- [X] T058 [US1] Implement the keyboard-accessible Mantine setup Stepper and forms, including one-time password acknowledgement, profile/certificate/provider validation, activation warning, and fresh sign-in in `ui/features/setup/setup-flow.tsx` and `ui/app/setup/page.tsx`
- [X] T059 [US1] Implement template creation plus workspace launch/detail/lifecycle/repair screens with Mantine controls and server-derived WorkflowStepper state in `ui/features/templates/template-form.tsx`, `ui/features/workspaces/launch-flow.tsx`, `ui/features/workspaces/workspace-detail.tsx`, and `ui/app/workspaces/page.tsx`
  - [X] Display observed and desired workspace states as read-only Mantine chips using Carbon status colors on the inventory and detail views. Verified UI unit tests and narrow-screen Chrome navigation in the updated demo.
  - [X] Show only observed state in the inventory and make the existing workspace controls, confirmations, and workflow progress available in expandable rows. Verified 137 UI unit tests, the production build, and narrow-screen keyboard access in Chrome against the updated demo.
  - [X] Replace expanded workspace controls with a Templates-style row Actions menu for direct commands and Open workspace. Keep focused dialogs for delete confirmation and on-demand workflow progress. Verified 137 UI unit tests, the production build, and narrow-screen Chrome navigation in the updated demo.
  - [X] Add a Target State column for active transitions and place a chip-height Mantine spinner beside observed state. Remove the progress message from the Actions cell. Verified automatic completion updates and measured spinner alignment in Chrome against the updated demo.
  - [X] Present launch failure details in an inline Mantine warning above the Stepper. Verified failed and in-progress launches in Chrome against the updated demo.
  - [X] Improve the workspace inventory and detail layout with observed-state filtering, resources/activity columns, refresh/retry controls, pagination feedback, and grouped lifecycle/configuration/preview/deletion sections. Hide confirmed deleted workspaces by default using a server filter before pagination, with an explicit history toggle. Verified 18 workspace unit tests, eight browser scenarios, TypeScript, production builds, Go vet, web/contract race tests, and the running demo.
  - Named resource profile and platform image choices are implemented and verified in the production export with Chrome, Firefox, and WebKit fixtures. The native workspace journey remains pending.
- [X] T060 [US1] Wire static UI embedding and controller/agent CLI composition so `shiftbox serve`, `shiftbox setup`, and `shiftbox-agent serve` satisfy their contracts in `internal/web/static.go`, `cmd/shiftbox/main.go`, and `cmd/shiftbox-agent/main.go`
- [X] T061 [US1] Add and run the exact-stack Go acceptance test together with the T039 Playwright journey, then record separate test results and pinned release evidence without broadening supported versions in `tests/release/mvp_workspace_test.go`, `documentation/validation/mvp-workspace.md`, and `platform/manifests/release.json`

**Checkpoint**: User Story 1 is a demonstrable MVP slice and must pass independently before later stories are required.

---

## Phase 4: User Story 2 — Share Reusable Projects Safely (Priority: P2)

**Goal**: Administrators and template owners manage members, immutable revisions, launch grants, three-scope secrets, and owner-bound GitHub HTTPS authority without weakening workspace privacy.

**Independent Test**: Create two members, share a template, configure all three secret scopes including an explicit empty value, connect private GitHub authorization, launch as the grantee, then revoke/archive and verify privacy, precedence, denial, and audit evidence.

### Tests first

- [X] T062 [P] [US2] Write failing membership, identity, password, and audit API/store tests for local users, pending external users, stable issuer/subject/connector binding, explicit role approval, self password change, actual upstream reauthorization, protected recovery boundaries, disable revocation, append-only redacted events, and protected retention in `tests/contract/users_api_test.go` and `internal/audit/store_test.go`
- [X] T063 [P] [US2] Write failing template service/API tests for immutable revisions, owner/admin edit rules, update/new-revision routes, acknowledged launch grants, owner-private launches, revocation, archival, and preserved provenance in `internal/template/service_test.go` and `tests/contract/templates_api_test.go`
- [X] T064 [P] [US2] Write failing secret service/API tests for metadata-only listing, exactly three scopes, User → Project Template → Organization precedence, explicit empty override, fallback preview on delete, ciphertext context binding, and management/audit omission in `internal/secret/service_test.go` and `tests/contract/secrets_api_test.go`
- [X] T065 [P] [US2] Write failing management and guest secret/Git contract tests for metadata-only secret listing, mTLS-derived owner/template context, denied guest-supplied scope, exit codes 0/3/4/5, no persistent cache, authorization initiation/state-bound callback/revocation, current owner token, coordinated refresh, and reconnect-required failure in `tests/contract/credentials_test.go` and `tests/contract/git_authorization_api_test.go`
- [X] T066 [P] [US2] Write failing GitHub authorization tests for repository coverage, stable provider identity, fenced concurrent renewal, app/refresh credential exclusion, revocation, and permanent renewal failure in `internal/identity/github_test.go`
- [X] T067 [P] [US2] Write failing UI tests for people states, template grant exposure warning, secret metadata-only views, empty override, fallback deletion warning, archival filters, and no secret value rendering in `ui/features/admin/people.test.tsx`, `ui/features/templates/sharing.test.tsx`, and `ui/features/secrets/secrets.test.tsx`
- **T068 [P] [US2] — Waived for the MVP.** The user approved skipping the live two-user Playwright sharing journey. The opt-in test remains in `ui/tests/e2e/template-sharing.spec.ts`.

### Models and implementation

- [X] T069 [US2] Add sharing/secret/Git/audit migrations in `migrations/sqlite/00004_sharing.sql` and `migrations/postgres/00004_sharing.sql`, preserving these verbatim constraints: TemplateGrant `(template_id, user_id)` is a "Unique active grant pair" and records `project_secret_warning_ack_at`; SecretValue `scope_type` is "`user`, `template`, or `organization` only", `name` is a "Normalized non-empty key; unique within active scope", `is_explicit_empty` "Distinguishes empty override from absence", and values never appear in audit/read models; GitAuthorization has "One record per user and app registration", provider "`github.com` in MVP", state "`connected`, `refreshing`, `reconnect_required`, or `revoked`", and `refresh_generation` fences concurrent results; AuditEvent metadata is "Redacted bounded context; schema rejects known secret fields"
- [X] T070 [US2] Implement administrator local-user creation, self password change, external connector configuration, external identity approval by stable binding, actual upstream reauthorization within the configured positive interval (initially four hours, never token-refresh-only), role/membership updates, protected local recovery of an existing administrator, and transactional session/grant revocation proven by T062 in `internal/identity/users.go`, `internal/identity/password.go`, `internal/identity/external.go`, `internal/identity/recovery.go`, and `internal/auth/membership.go`
- [X] T071 [US2] Implement append-only security audit writes and configurable retention that never removes current authority, operations, maintenance, or workspace provenance in `internal/audit/store.go` and `internal/audit/service.go`
- [X] T072 [US2] Implement immutable template revisions, launch grants with required project-secret acknowledgement, current-state launch authorization, revocation, archival, and administrator archive filtering proven by T063 in `internal/template/sharing.go` and `internal/template/archive.go`
- [X] T073 [US2] Implement protected three-scope secret storage/resolution, explicit empty handling, authorized fallback preview, and metadata-only management reads proven by T064 in `internal/secret/store.go`, `internal/secret/resolver.go`, and `internal/secret/service.go`
- [X] T074 [US2] Implement owner-bound GitHub.com App authorization initiation, state-bound callback, repository coverage, encrypted token storage, single-flight fenced refresh, revoke, and reconnect-required state proven by T065-T066 in `internal/identity/github.go`, `internal/identity/github_oauth.go`, and `internal/identity/github_store.go`
- [X] T075 [US2] Implement guest secret resolution and Git token endpoints plus `shiftbox secrets get-secret` and internal credential-helper exit behavior proven by T065 in `internal/web/credential_routes.go`, `internal/guest/credentials.go`, and `cmd/shiftbox-agent/credentials.go`
- [X] T076 [US2] Implement self password/external reauthorization, users/membership/external connector, template update/new revision/grant/revoke/archive, secret metadata/set/delete, Git authorization/callback/revoke, and audit read handlers from the OpenAPI and authorization-matrix contracts in `internal/web/session_routes.go`, `internal/web/user_routes.go`, `internal/web/template_grant_routes.go`, `internal/web/secret_routes.go`, `internal/web/git_routes.go`, and `internal/web/audit_routes.go`
- [X] T077 [US2] Implement Mantine password change, external reauthorization, People, template revision/sharing/archive, secret metadata/edit/delete, Git connection, and audit screens with the required warnings in `ui/features/session/password.tsx`, `ui/features/session/external-reauthorization.tsx`, `ui/features/admin/people.tsx`, `ui/features/templates/sharing.tsx`, `ui/features/secrets/secrets.tsx`, `ui/features/git/connection.tsx`, and `ui/app/audit/page.tsx`
  - [X] Improve user management using Carbon data-table and W3C sortable-table guidance: full-directory name/email search, role/status filters, accessible sorting, 10/25/50-row pagination, account action menus, and focused creation/role dialogs. Preserve authorization, version checks, password delivery, and offboarding workflows. Verified 18 focused unit tests, three narrow-screen Chrome keyboard tests, typecheck, production build, and the updated running demo.
  - [X] Rename the navigation and page labels from People to Users, including search, loading, empty results, and singular/plural result counts; update existing regression assertions and verify the production build.
  - [X] Improve Templates using Carbon and PatternFly table guidance: server-side name/repository search before cursor pagination, readable owner/profile/image names, compact row action menus, read-only details, refresh/retry controls, and contextual empty states. Preserve active-only defaults, administrator archive filtering, sharing warnings, and revision/version safeguards. Verified nine template unit tests, two narrow-screen Chrome keyboard tests, API contract and race tests, Go vet, typecheck, production build, and the updated running demo.
  - [X] Rename Account to Personal settings and move its navigation plus Sign out to a top-right avatar menu with user identity. Organize profile, sign-in security, GitHub, and personal secrets into labeled cards; add a focused password dialog with requirements, cancellation, and pending-request protection while preserving the required password-change flow. Apply Carbon profile-menu and GOV.UK password guidance with Mantine controls. Verified all 107 UI unit tests, three narrow-screen Chrome keyboard tests, typecheck, production build, and the updated running demo.
  - [X] Require Confirm password to exactly match the current New password before submitting changes, including the required password-change flow. Keep errors inline, focus invalid confirmation, and send only the existing API fields. Verified 11 focused unit tests, two Chrome keyboard tests, and the production build; updated the running demo.
  - [X] Replace technical password-length wording with plain-language guidance and specific corrections for short, oversized, or unchanged passwords, preserving existing validation limits. Verified 14 focused unit tests and the production build; updated the running demo.
  - [X] Present the ShiftBox-branded Mantine login form using Carbon's login pattern and skip the managed identity broker's first-party consent screen. Verified the native local and organization login flows, setup, incorrect-password retry, 133 UI unit tests, and keyboard access in the updated demo.
  - [X] Redirect sign-out and management session loss directly to the login form. Explain expired sessions in a persistent Mantine alert and retain access to organization login. Verified in Chrome against the updated demo and with Go session tests.
  - [X] Make Workspaces the home page for signed-in users. Replace the generic landing page with direct navigation and link the ShiftBox header logo to Workspaces. Verified native login, protected setup, and keyboard navigation in the updated demo.
- **T078 [US2] — Waived for the MVP.** The live two-user Playwright and Go journal run was part of T068. The verifier remains in `tests/acceptance/template_sharing_test.go`. Separate privacy, revocation, secret non-disclosure, and owner-bound Git evidence is recorded in `documentation/validation/template-sharing.md`.
- [X] T127 [US2] Implement and verify signed-in administrator password resets for active local accounts, with dedicated authorization, session and browser grant revocation, forced password change, redacted audit, and one-time delivery from People.

**Checkpoint**: User Story 2 works against the User Story 1 launch path but is independently testable with its own users, template, secrets, and Git authorization.

---

## Phase 5: User Story 3 — Operate Workspaces Without Losing Control (Priority: P3)

**Goal**: Administrators safely operate capacity, access revocation, networking, deletion/retention, maintenance, certificates, notices, release compatibility, and uncertain provider outcomes with visible recovery states.

**Independent Test**: Exercise capacity refusal, provider timeouts, disable/revoke, idle deletion, certificate expiry, long-stop recovery, and maintenance with hung work; verify reservations, authority boundaries, warnings, safe boundaries, and actionable progress.

### Tests first

- [X] T079 [P] [US3] Write failing capacity/reconciliation tests for CPU, memory, and disk refusal; no queue/eviction/resize; lost response; controller restart; conflicting inventory; and reservation retention until confirmed evidence in `tests/integration/capacity_reconciliation_test.go`
- [X] T080 [P] [US3] Write failing network-policy tests for only provider-supported modes, stopped-only changes, required controller/DNS/repository exceptions, allowlist validation, verification failure, and remain-stopped behavior in `internal/workspace/network_test.go`
- [X] T081 [P] [US3] Write failing revocation integration tests that close management SSE, IDE, preview, WebSocket, poll, and tunnel connections before reporting success and request stops for disabled users in `tests/integration/revocation_test.go`
- [X] T082 [P] [US3] Write failing retention tests for disabled-by-default deletion, selected activity signals, outage grace, final transaction recheck, graceful timeout, explicit deletion authority, partial cleanup, and disk reservation retention in `internal/workspace/retention_test.go`
- [X] T083 [P] [US3] Write failing maintenance tests for deadlines, warnings, capacity, safe activation boundaries, no maintenance force-kill, verification/rollback evidence, prior stopped-state restoration, and compatibility blocking in `internal/workspace/maintenance_test.go`
- [X] T084 [P] [US3] Write failing long-stop recovery tests for exact provider instance/ownership/generation, one-use delivery, preserved disks/drive lists, and denial of revoked/deleted/unknown/cloned/mismatched identities in `tests/integration/long_stop_recovery_test.go`
- [X] T085 [P] [US3] Write failing certificate/release/preflight API and CLI tests for 30-day browser warnings, independent internal PKI, invalid replacement rejection, no plaintext fallback, N/N−1 guest support, retained-workspace gate, backup/key/certificate/provider/tool checks, stable exits, and actionable blocks in `internal/release/preflight_test.go` and `cmd/shiftbox/migrate_test.go`
- [X] T086 [P] [US3] Write failing notice, infrastructure, maintenance, and audit-retention API tests for persistent conditions, publish/list/read/dismiss/history semantics, maintenance independence, provider/capacity/artifact/profile/policy management, and protected active records in `internal/app/notices_test.go` and `tests/contract/admin_operations_api_test.go`
- [X] T087 [P] [US3] Write failing UI tests for capacity errors, desired/observed state, destructive modal safe focus/typed name/checkbox, retention warnings, offboarding stop failures, maintenance steps, certificate/storage alerts, and narrow keyboard navigation in `ui/features/workspaces/operations.test.tsx` and `ui/features/admin/maintenance.test.tsx`
- [X] T088 [P] [US3] Write failing Playwright operations journey for hostile preview isolation, cross-origin mutation denial, live revocation, deletion focus/copy, partial cleanup reservation, inactivity, long-stop recovery, and maintenance in `ui/tests/e2e/operations.spec.ts`

### Models and implementation

- [X] T089 [US3] Add network/activity/maintenance/notice migrations in `migrations/sqlite/00005_operations.sql` and `migrations/postgres/00005_operations.sql`, preserving these verbatim constraints: NetworkPolicy `name` is a "Unique display name", mode is "`all`, `no_network`, or `allowlist`", and required exceptions include controller/proxy, DNS, Git, and approved auxiliary destinations; any selected active ActivityPolicy signal resets the deadline and an idle report does not; MaintenanceCampaign state is "`scheduled`, `active`, `blocked`, `completed`, `cancelled`" and MaintenanceTarget state is "`pending`, `waiting_safe`, `updating`, `verified`, `restoring_state`, `failed`, `completed`"; persistent operational notices remain visible while their condition is active regardless of dismissal history
- [X] T090 [US3] Complete admission/reconciliation behavior for explicit limiting-resource errors, no queue/eviction/resize, retained reservations, orphan/conflict visibility, and restart-safe inspection proven by T079 in `internal/workspace/capacity.go`, `internal/workspace/reconciler.go`, and `internal/provider/reconcile.go`
- [X] T091 [US3] Implement provider-capability-derived network policies, stopped-only application, explicit required exceptions, post-change verification, and remain-stopped failure behavior proven by T080 in `internal/workspace/network.go` and `internal/provider/utm/network.go`
- [X] T092 [US3] Implement transactional user/permission/session/origin/guest revocation publication and closure of all established connection types proven by T081 in `internal/auth/revocation.go`, `internal/proxy/registry.go`, and `internal/guest/tunnel.go`
- [X] T093 [US3] Implement activity observations, retention deadlines, host-outage grace, precommit authority/activity/state recheck, graceful then provider-enforced deletion, and verified reservation release proven by T082 in `internal/workspace/activity.go`, `internal/workspace/retention.go`, and `internal/workspace/delete.go`
- [X] T094 [US3] Implement long-stop eligibility verification and provider-mediated enrollment recovery without disk/identity reset proven by T084 in `internal/workspace/recovery.go` and `internal/provider/utm/enrollment.go`
- [X] T095 [US3] Implement maintenance campaign scheduling, warnings, per-target durable operations, safe-boundary activation, signed update staging, verification/rollback evidence, prior-state restoration, and blocked completion proven by T083 in `internal/workspace/maintenance.go` and `internal/guest/update.go`
- [X] T096 [US3] Implement browser-certificate expiry/invalid notices, storage/provider/controller/key persistent conditions, and no unprotected listener fallback in `internal/app/conditions.go` and `internal/config/certificate.go`
- [X] T097 [US3] Implement current/N−1 compatibility evaluation and irreversible migration preflight requiring verified backup, guest gate, tools, keys, provider, database ownership, certificate, and CA lifetime in `internal/release/compatibility.go`, `internal/release/preflight.go`, and `cmd/shiftbox/migrate.go`
- [X] T098 [US3] Implement system notices, receipts, condition persistence, dismissal/history, and protected retention pruning proven by T086 in `internal/app/notices.go` and `internal/app/retention.go`
- [X] T099 [US3] Implement workspace policy/action, user disable, notice publish/list/receipt, infrastructure status, browser-certificate replacement, platform artifact, resource profile, capacity, network policy, maintenance, and migration-preflight handlers from the OpenAPI and authorization-matrix contracts in `internal/web/workspace_policy_routes.go`, `internal/web/admin_routes.go`, `internal/web/notice_routes.go`, `internal/web/infrastructure_routes.go`, and `internal/web/maintenance_routes.go`
- [X] T100 [US3] Implement the accessible destructive workspace Modal with initial focus on Cancel, typed workspace name, “I have pushed work I need to keep” acknowledgement, retained-path list, and management-backup exclusion in `ui/features/workspaces/delete-workspace-modal.tsx`
- [X] T101 [US3] Implement Mantine Infrastructure, People offboarding, Notices, Retention, and Maintenance screens with server-driven Steppers and actionable persistent conditions in `ui/features/admin/infrastructure.tsx`, `ui/features/admin/offboarding.tsx`, `ui/features/notices/notices.tsx`, `ui/features/workspaces/retention.tsx`, and `ui/features/admin/maintenance.tsx`
  - [X] Organize Infrastructure into permission-aware Resources, Certificates, Maintenance, and Secrets tabs; clarify provider details and capacity units, preserve drafts across tabs, and add capacity discard controls. Verified with 10 unit tests, two mobile keyboard browser tests, TypeScript, the production build, and the updated running demo.
- [X] T102 [US3] Implement `shiftbox recover-admin` and `shiftbox migrate preflight` human/JSON output, stable exit codes, secret-safe arguments, and audit behavior in `cmd/shiftbox/recover_admin.go`, `cmd/shiftbox/migrate.go`, and `cmd/shiftbox/output.go`
- [X] T103 [US3] Add and run the Go operations acceptance test together with the T088 Playwright journey, then record separate evidence for capacity, revocation, destructive clarity, recovery denial, and maintenance authority in `tests/acceptance/workspace_operations_test.go` and `documentation/validation/workspace-operations.md`

**Checkpoint**: User Story 3 exposes every operational workflow and failure state without weakening ownership, reservations, or retained user work.

---

## Phase 6: User Story 4 — Back Up and Recover Management State (Priority: P4)

**Goal**: An administrator creates an independently encrypted, verified management backup and restores it transactionally into a reviewed recovery mode without claiming to back up VM disks or reviving stale authority.

**Independent Test**: Back up a running installation, validate and restore to an empty replacement target on the original host, reject wrong-password/truncated/missing-key/incompatible/unowned targets, reconcile provider state, review authority, and explicitly reactivate.

### Tests first

- [X] T104 [P] [US4] Write failing backup tests proving management writes pause only while application/Dex checkpoints are acquired, resume before archive assembly/encryption and after checkpoint failure, workspace computation remains uninterrupted, and the canonical age-encrypted verified archive explicitly excludes VM disks in `internal/backup/backup_test.go`
- [X] T105 [P] [US4] Write failing restore validation tests for wrong password, truncation, digest mismatch, incompatible release/schema, mismatched key, wrong host, non-empty/unowned target, and zero mutation before validation in `internal/backup/restore_test.go`
- [X] T106 [P] [US4] Write failing recovery tests for invalidated sessions/origin/enrollment grants/OAuth state, provider inventory matches/conflicts, gated workspace entry/destructive actions, identity/authorization review, and explicit activation in `internal/backup/recovery_test.go`
- [X] T107 [P] [US4] Write failing backup/restore API and CLI contract tests for password handling, stable JSON/exit codes, operation steps, validation IDs, separate activation, and no protected data in diagnostics in `tests/contract/backup_restore_test.go`
- [X] T108 [P] [US4] Write failing UI and Playwright tests for backup scope copy, cleared password state, ordered Stepper, validation findings, recovery gating, invalidated authority, provider conflicts, and keyboard focus in `ui/features/recovery/recovery.test.tsx` and `ui/tests/e2e/backup-recovery.spec.ts`

### Models and implementation

- [X] T109 [US4] Add BackupRecord and RecoveryReview migrations in `migrations/sqlite/00018_recovery.sql` and `migrations/postgres/00018_recovery.sql`, preserving these verbatim constraints: BackupRecord state is "`preparing`, `checkpointing`, `encrypting`, `completed`, or `failed`", the password/archive plaintext are never stored, and completion occurs "only after archive close and integrity verification"; RecoveryReview state is "`identity_review`, `provider_reconcile`, `authorization_review`, `ready`, `activated`, or `blocked`" and normal mutations, workspace access, and overdue destructive retention remain gated until `activated`
- [X] T110 [US4] Implement canonical backup manifest/checkpoint orchestration, scoped SQLite/PostgreSQL and Dex export, protected-file collection, passphrase age encryption, close/digest verification, and workspace-disk exclusion proven by T104 in `internal/backup/manifest.go`, `internal/backup/checkpoint.go`, and `internal/backup/backup.go`
- [X] T111 [US4] Implement whole-archive validation, compatibility/key/host/target ownership checks, empty replacement activation, and no merge/overwrite behavior proven by T105 in `internal/backup/validate.go` and `internal/backup/restore.go`
- [X] T112 [US4] Implement recovery-mode gate, stale-authority invalidation, identity review, provider reconciliation, workspace conflict decisions, authorization/retention review, and explicit normal-service activation proven by T106 in `internal/backup/recovery.go` and `internal/auth/recovery_gate.go`
- [X] T113 [US4] Implement backup, restore-validation, and recovery-activation OpenAPI handlers with durable Operation steps in `internal/web/backup_routes.go` and `internal/web/recovery_routes.go`
- [X] T114 [US4] Implement `shiftbox backup`, `shiftbox restore validate`, and `shiftbox restore activate` with TTY/stdin password handling, human/JSON output, and stable exits proven by T107 in `cmd/shiftbox/backup.go` and `cmd/shiftbox/restore.go`
- [X] T115 [US4] Implement Mantine Backup & Recovery forms, scope warnings, cleared password fields, operation Stepper, validation findings, and gated recovery review in `ui/features/recovery/backup.tsx`, `ui/features/recovery/restore.tsx`, `ui/features/recovery/review.tsx`, and `ui/app/recovery/page.tsx`
- [X] T116 [US4] Add and run the Go recovery acceptance test together with the T108 Playwright journey, then record separate recovery evidence without workspace-disk claims in `tests/acceptance/backup_recovery_test.go` and `documentation/validation/backup-recovery.md`

**Checkpoint**: User Story 4 safely restores owned management state while stale authority and destructive actions remain blocked until reviewed.

---

## Phase 7: Polish and Cross-Cutting Release Gates

**Purpose**: Verify the integrated MVP against the approved architecture without adding deferred product scope.

- [X] T117 [P] First add a failing OpenAPI-to-TypeScript drift test, then generate UI contract types without generated Go handlers and make the drift test pass in `tests/contract/openapi_types_test.go`, `ui/lib/api-types.ts`, and `scripts/check-openapi.sh`
- [X] T118 [P] Add bounded redaction and secret-leak tests across logs, URLs, API responses, browser storage, process arguments, audit metadata, and archives in `tests/integration/secret_leak_test.go` and `ui/tests/e2e/browser-storage.spec.ts`
- [X] T119 [P] Add automated accessibility checks plus manual keyboard/focus evidence for every Mantine Stepper workflow in `ui/tests/e2e/accessibility.spec.ts` and `documentation/validation/accessibility.md`
- [X] T120 Add end-to-end controller restart injection at every persisted setup, lifecycle, maintenance, backup, restore, and recovery step in `tests/acceptance/restart_matrix_test.go`
- [X] T121 Add SQLite/PostgreSQL parity, race, real HTTP/mTLS, Dex/OIDC, hostile-origin, and corrupted-backup integration gates in `tests/integration/release_gate_test.go`
- [X] T122 Run the already-defined exact UTM/Packer/import/boot/recovery/drive-preservation harness under the `utm_release` build tag and record controlled-host evidence in `tests/release/utm_stack_test.go` and `documentation/validation/utm-stack.md`
- [X] T123 Add Debian and Ubuntu glibc/amd64 fixtures that separately exercise prebuilt-image and Dockerfile/build-definition Dev Containers, declared features, lifecycle-hook success/failure, declared connection-ready stages, and retained-data checks in `tests/release/fixtures/debian/` and `tests/release/fixtures/ubuntu/`
- **T124 — Waived for the MVP.** The user approved forgoing the remaining technical release checks and deferred the 10-person first-use study. Recorded evidence and outstanding checks remain in `documentation/validation/mvp-v1-v14.md`. The study protocol remains in `documentation/validation/first-use-study.md`.
- **T125 — Waived for the MVP.** The user approved forgoing the remaining live quickstart scenarios. The eight commands passed earlier, and the available scenario results remain in `documentation/validation/quickstart-results.md`.
- [X] T126 Review all exported Go APIs and non-obvious trust/reconciliation code for concise application-context comments, run formatting/lint/race/UI/browser checks, and record the clean result in `documentation/validation/final-checks.md`

---

## Dependencies and Execution Order

### Phase dependencies

- **Phase 1 — Setup**: starts immediately.
- **Phase 2 — Foundational**: depends on Phase 1 and blocks every user story.
- **Phase 3 — User Story 1**: depends on Phase 2 and is the first independently demonstrable MVP slice.
- **Phase 4 — User Story 2**: domain tests and services may start after Phase 2. The optional T068 and T078 fixtures depend on the User Story 1 launch path through T061.
- **Phase 5 — User Story 3**: domain tests and services may start after Phase 2, but T088 and T103 depend on completed User Story 1 workspaces through T061.
- **Phase 6 — User Story 4**: backup/recovery domain work may start after Phase 2, but T108 and T116 depend on populated User Story 1 management/workspace state through T061.
- **Phase 7 — Polish**: depends on every story included in the release candidate.

### User-story dependency graph

```text
Setup -> Foundation -> US1 (MVP) -> US2 acceptance checkpoint
                    |             -> US3 acceptance checkpoint
                    |             `-> US4 acceptance checkpoint
                    |-> US2 domain/API/UI work
                    |-> US3 domain/API/UI work
                    `-> US4 backup/recovery domain work

US1 + US2 + US3 + US4 checkpoints -> Cross-cutting release gates
```

### Within each story

1. Complete the story's test tasks and observe the intended failures.
2. Apply migrations/models with the quoted constraints.
3. Implement domain services and external boundaries.
4. Implement API/CLI and Mantine UI surfaces.
5. Run the independent acceptance scenario and stop at the checkpoint.

---

## Parallel Execution Examples

### User Story 1

```text
Parallel test work: T030 setup contract, T032 provider contract, T034 guest protocol, T035 workspace domain, T038 UI behavior
After stores exist: T048 UTM adapter and T052 guest executor can proceed in parallel
After API contracts exist: T058 setup UI and T059 workspace UI can proceed in parallel
```

### User Story 2

```text
Parallel test work: T062 membership, T063 template sharing, T064 secrets, T066 GitHub, T067 UI
After T069: T070 membership, T072 template sharing, T073 secrets, and T074 GitHub can proceed in parallel
```

### User Story 3

```text
Parallel test work: T079 capacity, T080 network, T081 revocation, T082 retention, T083 maintenance, T085 certificates/releases
After T089: T091 network, T093 retention, T095 maintenance, T096 conditions, and T098 notices can proceed in parallel
```

### User Story 4

```text
Parallel test work: T104 backup, T105 restore validation, T106 recovery gate, T107 contracts, T108 UI/browser
After T109: T110 backup creation and T112 recovery gating can proceed in parallel; T111 restore activation follows validation behavior
```

---

## Implementation Strategy

### MVP first

1. Complete Setup and Foundational phases.
2. Complete User Story 1 through T061.
3. Stop and run the independent MVP workspace journey.
4. Demonstrate setup, launch, IDE readiness, retained files, reconciliation, and repair before expanding the release candidate.

### Incremental delivery

1. Add User Story 2 and validate sharing/privacy/secrets/Git independently.
2. Add User Story 3 and validate operational safety under injected failures.
3. Add User Story 4 and validate encrypted backup plus reviewed recovery.
4. Complete the cross-cutting release gates only after the desired stories pass their checkpoints.

### Scope discipline

- Implement only the task being executed and its named dependencies.
- Use the approved architecture and contracts when a task needs detail; ask before changing scope or design.
- Do not introduce EC2, HA, multi-organization SaaS, extra IDEs, public sharing, MFA, runtime provider plugins, SSH Git, or workspace-disk backup.
- Keep one Go module, one active controller, one VM and primary devcontainer per workspace, and a static UI served by Go.

---

## Notes

- `[P]` marks different-file work that can proceed without an incomplete dependency.
- Story labels provide traceability to the four specification stories.
- Test tasks always precede implementation and require explanatory comments in every test.
- A task is complete only when its named tests pass and its file paths match the approved structure.
- Commit after each task or small coherent group; never hide unrelated scope inside a checklist item.

## Execution checkpoint — ready for requested compaction and restart

The user requested removal of the global context-window and compaction overrides,
context compaction, and notification of a safe point to pause/restart. Both global
overrides were removed and the remaining TOML configuration was verified unchanged.
The agent has no exposed compaction tool; the app composer supports `/compact`.
This checkpoint does not itself change the goal status.

- Completed: 22 tasks marked above. No user-story implementation has begun.
- Continue the remaining foundation tasks: T013 (including route/action coverage),
  T014 (including OpenAPI drift and service allowlisting), T016, T018, T023, T025,
  and T029. Authentication/store/policy and HTTP primitives are partly implemented;
  session routes, complete authorization integration, and the UI remain unfinished.
- Do not treat the initial foundation SQL as fully verified: T018 is still unchecked.
  Audit-required administrator access must commit an audit event before use; the
  current policy returns AuditRequired, but the consuming access flow is not built.
- Latest targeted race checks pass for auth, proxy, HTTP/SSE, and identity packages.
  Real SSE replay/revocation and actual Dex HTTPS discovery/start/stop passed.
  Earlier SQLite/PostgreSQL migration/transaction parity and internal mTLS tests passed.
- Recent commits before this checkpoint: `5b556a3` (session policy, HTTP boundaries,
  revocable SSE), `adc8fd0` (operation journal), `c6d0800` (database/crypto/config),
  and `cfd09bf` (setup). The identity work is saved with this checkpoint commit.
- Local tools: `.cache/toolchains/go/bin` (Go 1.27.4),
  `.cache/toolchains/node-v26.13.0-darwin-arm64/bin`, and `.cache/toolchains/dex`.
  Dex 2.46.1 was built from commit `75eb19238e10fc4750e7efb05a4031b254b04029`
  in `.cache/dex-source`; its Go module path prevents direct version download.
- Set GOPATH to `$PWD/.cache/gopath` and GOCACHE to `$PWD/.cache/go-build`.
  Set SHIFTBOX_TEST_DEX_BINARY to `$PWD/.cache/toolchains/dex` to enable the real
  native Dex test. Local listener/process tests need execution permissions.
- User authorized local prerequisites. PostgreSQL container `shiftbox-mvp-postgres`
  uses `127.0.0.1:55432`, verified TLS, and owned `shiftbox_test` schema. Read the
  ignored `.cache/test-postgres/uri` into SHIFTBOX_TEST_POSTGRES_URI without printing
  it. Its TLS fixture lasts seven days. UTM is not installed yet.
- UI dependencies/configuration and brand copies exist. No UI components or routes
  have been implemented; UI skills and Mantine MCP references have been consulted.
  npm reports 19 development-tool audit findings, none in production dependencies.
- User edits to spec.md and untracked contracts/plan/data-model/research/quickstart
  remain untouched. Architecture documents remain read-only. No subagents used.

### Resumed implementation — management cookie boundary

- Added test-first management cookie issuance, extraction, and deletion in
  `internal/web/session_routes.go` and `internal/web/session_routes_test.go`.
  Uses the contract's `__Host-shiftbox_session` name, host-only root path,
  Secure/HttpOnly/SameSite=Strict, and the session's expiry. Missing, duplicate,
  malformed, and noncanonical tokens are rejected without exposing credentials.
- Targeted tests failed before implementation and passed afterward. Race checks
  for web/auth/proxy and repository formatting/vet checks pass.
- T025 remains unchecked: session read/logout handlers and complete authorization
  integration still need implementation. Cookie helpers alone do not establish
  working sign-in, logout, or browser isolation validation.

### Session endpoint implementation

- Added the session GET/DELETE handler with current-principal lookup, token binding,
  policy gates, no-store responses, CSRF delivery, external-authorization deadline,
  and policy-derived navigation permissions. Resource-specific permissions are not
  advertised as blanket authority. Logout requires exact Origin and bound CSRF,
  waits for revocation/closure, and then clears the host-only cookie.
- Tests cover disabled membership, unknown-cookie versus storage-failure responses,
  restricted password-change permissions, hostile sibling Origin, wrong CSRF, and
  failed revocation. A real SQLite fixture verifies persisted invalidation before
  registered connection closure and rejection of the cookie after logout.
- The handler's lookup and revoke functions are explicit composition dependencies;
  production router/state wiring and general route/action authorization remain
  unfinished. T025 and its broader test tasks remain unchecked.

### Current resource-grant persistence

- Added transaction-scoped reads of active grants for an exact subject/resource
  type/resource ID and idempotent revocation preserving the first timestamp.
- Test-first SQLite coverage excludes revoked and unrelated grants, verifies
  rollback preserves permission, and proves committed revocation removes admin
  connection authority. Active admin connection decisions still require audit and
  retain the workspace owner's secret/Git context.
- Auth race tests, formatting, and vet pass. T023 remains unchecked: the consuming
  administrator connection flow must still commit audit evidence before access;
  this store does not itself grant access or close registered connections.

### Workflow Stepper foundation

- Consulted Mantine MCP Stepper documentation and added the reusable client
  component with server-derived active progress, disabled pending steps, completed
  step inspection, visible failure alerts, skipped labels, and polite live status.
  Inspection does not change authoritative progress or hide current-step details.
- Three component tests were written before implementation and pass with the
  installed Mantine provider. TypeScript validation passes.
- T016/T029 remain unchecked: SSE gap/refetch tests and wiring, branded shell,
  query provider, and actual Next/browser runtime verification remain unfinished.
  No runnable page has been added yet; component tests are not browser evidence.

### Workflow event revalidation

- Added a native EventSource subscription and TanStack Query operation hook.
  Every operation/step event invalidates the exact operation query, so sequence
  gaps refresh authoritative state without replaying partial event snapshots.
  Disconnect/reconnect also revalidates; heartbeat events do not trigger reads.
- Terminal operations close the stream. Explicit revocation cancels reads and
  removes the cached operation; denied refreshes hide cached progress and stop
  listening. Fetches use cancellation, same-origin credentials, and no-store.
- Nine UI tests across three suites and TypeScript validation pass. Tests include
  gap refetch, terminal closure, lost authorization, cache removal, and cleanup.
- Application-level provider/page wiring and real browser/SSE verification remain
  pending. T016/T029 are not marked complete based on isolated hook tests.

### Branded static shell and browser validation

- Added Mantine AppShell with approved light/dark logo assets, responsive navigation,
  session-mode/permission-aware links, and a persistent alert landmark. Added the
  Mantine and in-memory Query providers, App Router layout, and session-reading home.
- Tests first cover member/admin navigation, restricted sessions, persistent alerts,
  and unavailable session API. All 13 UI tests, TypeScript, and static export pass;
  `ui/out/index.html` is produced without a production Node server.
- Verified Next 16.4.6 Turbopack via MCP: no compilation issues/runtime errors, route
  `/`. Agent-browser 0.36.0 confirmed the rendered shell and React provider/component
  tree. Corrected an alert-landmark accessibility issue with a regression test;
  repeat WCAG A/AA scan reports zero violations and zero incomplete findings.
- The controller API is not running; browser validation intentionally observed the
  persistent connection-error state. Sign-in and navigation destination pages are
  not implemented. Full session/operation browser journeys remain pending, as does
  the reusable API problem view. T029 remains unchecked.
- Next generated `ui/AGENTS.md` and TypeScript development-route configuration;
  retained those framework instructions/settings. Local test browser and dev server
  were stopped before the successful production build.

### Shared API problem guidance

- Added validated problem decoding and a reusable Mantine persistent error view
  with recovery detail, field corrections, and request reference. Malformed JSON,
  HTML proxy responses, mismatched HTTP status, and arbitrary exceptions use fixed
  safe guidance. The session page now uses this shared path.
- All 18 UI tests and TypeScript checks pass. The browser rendered the fallback
  alert for the absent controller; React introspection confirmed APIProblemView.
  Next MCP reports no compilation/runtime errors; WCAG A/AA scan has zero violations
  and zero incomplete findings. Structured controller guidance is unit-tested;
  end-to-end controller integration remains pending.
- Test browser closed. Next dev remains on port 3127 (exec session 91329) for the
  next UI verification loop; inspect the live handle before assuming it remains up.

### Management route contract coverage

- Added a 52-operation management route catalog with exact method/path/operation
  identities, static action keys, and explicitly named contextual resolver rules.
- Test-first contract checks compare every catalog entry with OpenAPI and static
  permissions with the approved authorization matrix. Missing/duplicate/moved
  routes and changed contract layout fail the checks. Contextual operations have
  no blanket action key (retry, entry grants, operation views, scope/field updates).
- Targeted contract tests, formatting, and Go vet pass. The catalog is metadata,
  not authorization enforcement. Contextual resolution and guarded registration
  remain unfinished; T013/T014/T025 are still unchecked.

### Foundation authority constraints

- Tightened both initial foundation schemas to require canonical SHA-256 hex
  session/CSRF digests and enumerated grant actions matching their resource type.
- Test-first raw-SQL failure cases pass against SQLite and real PostgreSQL, using
  connection-local copies of the shipped definitions to avoid resetting existing
  PostgreSQL fixture state. Valid session and administrator-grant inserts pass.
- Full Go race suite passes with PostgreSQL/Dex fixtures enabled; formatting and
  vet pass. This completes only part of T018/T023. Initial migration edits affect
  newly created schemas; the existing development database was not upgraded or
  represented as upgraded. Installation/configuration activation constraints and
  remaining foundation authorization work are still pending.

### Shared UI foundation completed

- T016 is complete: component tests now exercise the real operation query hook and
  visible Mantine Stepper together through a missing event, verifying authoritative
  refresh, retained failure history, disabled future steps, and live status labels.
  Earlier tests were written failing before the Stepper and event/query code.
- T029 is complete at its shared-foundation scope: static layout/providers, branded
  AppShell, session-aware navigation, persistent alerts, API problem view, in-memory
  query client, and reusable workflow component are implemented and tested.
- All 19 UI tests pass; TypeScript and the production static export pass. Prior
  runtime MCP/browser/accessibility evidence covers the shell/error view. This is
  not completion of sign-in, destination pages, actual workspace workflows, or
  end-to-end Go/SSE browser validation; those remain in later user-story tasks.
- 24 tasks are now complete. Remaining foundation tasks: T013, T014, T018, T023,
  and T025. The development server (91329) was stopped before the final build.

### HTTP/proxy boundary test coverage completed

- T014 is complete: existing problem/request-ID/UUID/version/idempotency/body-limit/
  unknown-field/header tests, RFC 3339 SSE decoding, route/action contract checks,
  and new service-allowlist failure tests cover its shared boundary requirements.
- Added an immutable service allowlist bound to controller-derived workspace ID and
  generation. Only exact registered HTTP/WebSocket service keys are admitted;
  browser-selected network destinations are never converted to controller dials.
  Tests reject cross-workspace/stale-generation/protocol/key misuse and prove input
  mutation cannot change the captured allowlist.
- Proxy, web, and contract race checks plus formatting/vet pass. This is a boundary
  primitive, not a running tunnel: mTLS/Yamux registration and actual proxy stream
  enforcement remain T053 work. Runtime route authorization remains T013/T025 work.
- 25 tasks are complete. Remaining foundation tasks: T013, T018, T023, T025.

### Database-backed session authority

- Added a single-statement principal read joining session, current membership, and
  installation gates within the caller's transaction. Tests prove membership/role
  changes apply to an existing cookie; unknown cookies do not produce authority.
  Duration conversion rejects invalid/overflowing external authorization intervals.
- Added normal session-handler construction using the active database and live
  connection registry. The database-backed HTTP logout test now exercises this
  production wiring instead of hand-assembling store callbacks and still proves
  durable invalidation before connection closure and subsequent cookie rejection.
- Auth/web race checks, formatting, and vet pass. Bootstrap/recovery eligibility
  is not inferred from database state; protected flow wiring remains pending, as
  do controller route registration and general authorization enforcement.

### Atomic configuration-generation records

- Added the configuration-store activation-record primitive needed by T018's
  generation invariant and the later T042/T045 activation flow. It advances the
  expected generation by exactly one, retires the predecessor, records the new
  settings snapshot, and updates the installation in the caller's transaction.
- Test-first SQLite and PostgreSQL tests prove stale-generation rejection,
  rollback after a subsequent activation failure, one current generation, and
  retained history. Both profiles pass under the race detector; formatting/vet pass.
- This records already-validated non-secret settings; it does not perform profile,
  certificate, hostname, restore, or authorization validation. Those consuming
  gates and the remaining T018 constraints are still pending. No additional task
  is marked complete, and the architecture documents remain unchanged.

### Installation identity constraints

- Added explicit non-null UUID-format constraints to the initial installation
  table in both dialects. The test exposed SQLite accepting a null TEXT primary
  key; the new NOT NULL constraint closes that gap.
- Raw-SQL tests reject null/empty/malformed identities and verify that two valid,
  distinct UUIDs with distinct management origins still cannot bypass the singleton
  installation rule. Updated test fixtures to use valid installation UUIDs.
- Contract/config/auth/web race suites pass with the PostgreSQL fixture enabled;
  formatting and vet pass. These are initial-schema changes, not a migration of
  the existing development database. T018 remains incomplete for its other gates.

### Browser-certificate validation primitive

- Added test-first certificate/key and X.509 server-auth validation against an
  explicit trust pool and every supplied concrete service hostname. Rejects key
  mismatch, malformed input, unknown issuer, expired/future validity, client-only
  usage, missing hostname coverage, and wildcard depth mismatch with a safe error.
- Configuration race tests, formatting, and vet pass. The validator does not
  install/persist keys or select trust policy. Configured-pattern validation,
  private-CA/browser trust integration, protected storage, and atomic activation
  remain consuming T018/T045 work; neither task is marked complete.

### Approved service hostname syntax

- User approved `{workspace}.ide.example.com` and
  `{service}--{workspace}.preview.example.com`, with configurable DNS suffixes.
  Workspace labels use UUIDs; preview service keys share one label with the UUID.
- Added test-first fixed-template/DNS-suffix validation and tightened management
  origin checks to reject spellings that disagree with browser serialization
  (empty query/fragment/port, uppercase hosts, default or invalid ports).
- Configuration race tests and formatting/vet pass. Host routing, management
  collision checks, wildcard certificate coverage, and activation wiring remain
  pending; T018 is not marked complete. Architecture documents were not changed.

### Service identity routing and certificate pattern coverage

- Added construction/parsing of the approved hostname formats using canonical
  workspace UUIDs and DNS-safe service keys. Fixed-width UUID parsing preserves
  service keys containing consecutive hyphens; `ide` selects the IDE namespace.
  Preview keys occupy at most 25 bytes to keep the combined DNS label within 63.
- Rejects management hostnames that overlap a service hostname, including on
  different ports because host-scoped cookies are shared across ports.
- Added configured-certificate validation requiring both wildcard SANs as well
  as management coverage and existing key/chain/time/trust validation. A single
  workspace certificate cannot substitute for coverage of future workspaces.
- Test-first configuration race tests, formatting, and vet pass. These are
  routing/validation primitives; controller request dispatch and configuration
  activation still need to consume them. Trust-policy selection and protected
  key persistence remain pending. No additional task is marked complete.

### Validated installation settings records

- Added typed non-secret installation settings and transactional recording that
  consumes the canonical management-origin and service-pattern validation.
  The live installation row and its generation snapshot come from the same
  validated fields; caller-supplied snapshot JSON cannot disagree with them.
- Ordinary updates reject database-profile changes after a durable generation
  exists or the installation is active. Guarded restore remains a separate,
  unfinished flow; no boolean bypass or automatic authority switch was added.
- SQLite and PostgreSQL race tests verify invalid-host rejection, immutable
  profiles, rollback of both settings and generation, and matching durable
  snapshots. Formatting and vet pass.
- Certificate/key persistence, database prerequisite checks, guarded restore,
  listener activation, and runtime authority switching remain consuming flow
  requirements. T018 remains incomplete; these records alone do not activate
  an installation or claim a runnable product.

### Transactional management authorization handler

- Added a guarded handler that reads current session authority (database-backed
  by default), checks exact Origin/CSRF for mutations, resolves controller-owned
  resource context, evaluates policy, records any required access audit, and
  persists intent inside one transaction. Responses run only after commit.
- Fixed-action routes reject resolver/action disagreement. Missing/failed audit
  storage denies administrator connections; failed later intent rolls back the
  audit together with the intent. Resolution callbacks never grant authority by
  themselves. External effects are forbidden inside the transaction callbacks.
- Tests cover disabled membership, hostile origin, route mismatch, intent
  rollback, response-after-commit, and required audit failure/rollback/success.
  Web/auth race suites, formatting, and vet pass (HTTP tests require loopback).
- Route registration, concrete contextual resolvers, durable audit schema/store,
  and real entry-grant/proxy flows remain unfinished. T013/T023/T025 are not
  marked complete by this handler boundary alone.

### Reviewed management route registration

- Added management-router construction for all 52 catalog operations at their
  `/api/v1` paths. Missing, unknown, or incompletely wired implementations fail
  construction; handler-supplied route/action declarations cannot replace the
  reviewed catalog. Session/logout retain their existing dedicated handler.
- Tests dispatch every catalog operation through the actual HTTP mux and prove
  unauthenticated requests stop before contextual resolution or intent callbacks.
  Registration supplies controller request IDs and copies handler configuration.
- Web/contract race suites, formatting, and vet pass. This is registration proof,
  not implementation of the missing endpoint callbacks: contextual resource/action
  resolvers and concrete flows still prevent a complete runnable controller.
  T013/T025 remain unchecked until those consuming requirements are satisfied.

### Durable administrator-access audit prerequisite

- Introduced the shared AuditEvent table in the initial foundation schema because
  foundation authorization requires durable administrator-access evidence before
  the later sharing flows. T069 must reuse this table rather than create a second
  audit store; its remaining sharing/secret/Git migrations are still pending.
- Added an append-only store API with ordered database-generated IDs, explicit
  actor/action/resource/outcome, and bounded typed owner/grant metadata. Both
  database schemas reject unknown fields, nested values, null identifiers, and
  non-object metadata. There is no event-update API.
- The authorization handler now defaults to this writer for administrator
  workspace connections, recording the exact active grant and workspace owner
  alongside intent. Missing audit storage or a later intent failure prevents
  success; the test exercises real schema persistence as well as rollback.
- Audit/web/auth/config/contract race suites pass with PostgreSQL enabled;
  formatting and vet pass. Initial schema edits do not upgrade the existing
  fixture's durable tables. Retention (T071), concrete entry grants/proxy access,
  and broader sharing flows remain unfinished; no additional task is marked done.

### Contextual route permission bounds

- Added reviewed action sets for each contextual resolver and enforced them in
  the authorization handler. A workspace-entry resolver cannot substitute a
  session or template permission; unknown resolver categories fail closed.
- Workspace retries retain possible original launch/connection actions as well
  as lifecycle actions. Concrete resolvers must still select the exact action
  from persisted operation context, ownership, and requested fields; an allowed
  vocabulary is not a replacement for those checks.
- Catalog-wide action tests and an HTTP mismatch test pass. Web/auth/contract
  race suites pass with PostgreSQL enabled; formatting and vet pass.
- Foundation remains at 25 complete tasks. T013 contextual action selection,
  T018 certificate activation, and dependent T023/T025 completion evidence still
  require work; later user-story endpoint implementations are not claimed.

### Workspace contextual action selection

- Added lifecycle and entry action selection from controller-loaded workspace,
  template, grant, and failed-operation records. Retry requires a failed operation
  bound to the same workspace and preserves its lifecycle, launch, or connection
  permission; installation operations cannot enter the workspace retry path.
- Provision retries also require workspace ownership or administrator role,
  preventing shared-template launch permission from controlling another owner's
  existing failed workspace. Entry selection retains owner and grant context for
  the existing policy and durable audit checks.
- Test-first checks cover ordinary lifecycle mapping, cross-workspace/nonfailed
  retries, shared-template isolation, unsupported actions, and owner/admin entry
  selection. Web/auth/contract race suites, formatting, and vet pass.
- Concrete endpoint loaders must still bind these inputs to database records in
  the authorization transaction. Other contextual routes and certificate
  activation remain outstanding; no additional task is marked complete.


### Atomic protected browser-certificate activation

- Added protected-value storage and a certificate-key reference constraint.
  Certificate activation validates the matching pair, explicit trust roots,
  current validity, management hostname, and both wildcard service patterns.
- Private keys use the existing AES-256-GCM store with installation/certificate
  identity in authenticated context. Ciphertext, certificate replacement, and
  the typed configuration snapshot (including certificate ID) commit together.
  Failed replacement preserves the prior certificate, generation, and key count.
- Loading permits only the active certificate and rechecks current time, trust,
  and hostname coverage. Wrong installation/key, retired certificates, and
  expired active material fail without fallback.
- Both-profile configuration tests and auth/web/audit/contract race suites pass;
  formatting and vet pass. T018 is complete at its foundation schema/invariant
  scope. This does not complete T045's UI upload, trust-policy selection, notices,
  and listener integration, or the later guarded restore workflow. Ordinary
  configuration activation still has no profile-immutability bypass.
- 26 tasks are complete. Remaining foundation tasks are T013, T023, and T025.
  Initial schema changes still do not upgrade the existing fixture's durable
  tables; PostgreSQL tests exercise temporary copies of current shipped SQL.

### Field-dependent administrative action selection

- User explicitly approved `user.disable` for transitions back to `pending`;
  this removes active access. Recorded here without changing architecture docs.
- Added secret scope selection bound to the loaded resource type and ID, notice
  read/dismiss selection, template edit/archive selection, and membership
  approve/disable/change-role selection. Unknown and empty changes fail closed.
- Combined membership changes use the membership-state action after validating
  both fields; all membership actions require administrator authority in the MVP
  policy and cannot be delegated by resource grants. Archive plus rename uses
  template.archive; both template actions retain the loaded owner context.
- Failure-focused selection tests and web/auth/contract race suites pass;
  formatting and vet pass. Concrete endpoint loading/decoding and operation-view
  contextual action selection remain pending. No additional task is marked done.


### Operation-view authority and foundation checkpoint

- Added persisted, server-only originating action to operation intent. Idempotent
  replay reads the existing action and cannot replace it with a weaker action.
  It is not exposed in operation/event JSON. Missing installation-operation
  authority fails closed; concrete admission flows must supply their checked
  action when creating intent.
- Operation views bind workspace operations to the exact loaded workspace and
  use workspace.view. Installation-wide views use the persisted originating
  administrative action; session/template/workspace permissions cannot substitute.
- Reviewed foundation completion against shared-component scope: contextual
  action selection and route-contract tests complete T013; stored current users,
  identities, sessions, grants, policy and durable required access audit complete
  T023; session/logout, mutation/authorization middleware, request parsing,
  problems, redacted logging and reviewed registration complete T025.
- Full Go race suite passes with PostgreSQL and pinned Dex fixture variables;
  formatting/vet pass. UI component suite passes all 19 tests in 7 suites.
- 29/126 tasks are complete. Phase 2 foundation is complete; proceed to Phase 3
  tests-first setup/CLI and provider flows. Concrete endpoint loaders, protected
  bootstrap, listener composition, UTM provisioning and end-to-end journeys are
  still later tasks, not working behavior claimed by this checkpoint.

### Protected setup request boundary

- Began T030 with test-first setup API boundary cases, then added the shared
  T043 request guard. Only direct loopback HTTPS requests to the exact configured
  literal-IP host/port reach inner setup handlers. Remote peers, rebound hosts,
  wrong ports, plaintext requests, and malformed peer addresses fail closed.
- Origin configuration rejects public/wildcard addresses, DNS names, paths,
  empty query/fragment markers, and invalid/noncanonical ports. Forwarding
  headers cannot substitute for the actual peer or request authority.
- App/web/contract race tests, formatting, and vet pass. The listener still needs
  an explicit loopback bind; password issuance/change, resumable setup state,
  setup sessions, activation and the CLI remain unfinished. T030/T043 are not
  marked complete by this boundary alone.

### Resumable Dex credential creation prerequisite

- Added the official Dex administration API pinned to the same source revision as
  the verified Dex executable. The adapter uses CreatePassword and checks an
  existing email's stable user ID; it never updates a password during a bootstrap
  retry. A different identity at that email is a conflict, never an automatic merge.
- Added 32-byte random temporary-password generation and bcrypt hashing. The
  adapter accepts hashes only and redacts transport/API failures. Tests prove
  retry preserves a changed credential, identity collisions fail, and generated
  passwords are independent and verify against their hashes.
- Identity/auth/app/contract race suites pass with the pinned Dex fixture enabled;
  formatting and vet pass. Password adapter behavior is currently unit-tested
  against the API boundary; live authenticated gRPC administration, durable
  issuance/delivery state, forced change, and one-time output remain pending.
  T030/T043 remain incomplete.

### Live authenticated Dex administration

- Added a loopback-only gRPC administration client requiring explicit trust,
  server identity, a client key/certificate, and TLS 1.3. Insecure verification,
  missing credential material, and public endpoints are rejected.
- Extended the pinned native Dex test with a separate administrator client trust
  fixture. Real CreatePassword/UpdatePassword/VerifyPassword calls prove that a
  setup retry preserves the changed hash; an unrelated client certificate cannot
  list passwords. Client connections and the native process are closed afterward.
- Live testing exposed Dex's minimum bcrypt cost (10); corrected the test fixture
  from the fast unit-test cost (4). The production generator already used 10.
  One startup failed; fixed possible duplicate ephemeral-port selection by holding
  all reservations until distinct ports are chosen. Two later live runs pass.
- Identity race suite and formatting/vet pass. Durable administrator TLS material,
  bootstrap issuance/delivery state, forced change and controller composition
  remain pending; T030/T043 are not yet complete.

### Durable initial-administrator intent

- User approved explicit protected local reset if the temporary password is lost
  during delivery; ordinary setup retries must not redisplay or reset credentials.
  The reset flow is authorized but not yet implemented.
- Added one bootstrap-administrator record per installation, linked to the stable
  user and an encrypted temporary-password delivery value. Preparing intent
  atomically creates an active local administrator with forced password change,
  stores the bcrypt hash, and stores only ciphertext for the delivery copy.
- Retries return the same user/hash, even if a new candidate ID was generated;
  different owner email, existing unrelated users, and active installation state
  fail closed. Dex calls must occur after this transaction commits.
- SQLite/PostgreSQL tests prove rollback and stable retry behavior. App/config/
  contract race suites and formatting/vet pass. One-time delivery, Dex readiness
  reconciliation, explicit local reset, forced-change UI/session transitions,
  and CLI composition remain pending. T030/T043 remain incomplete.

### One-time temporary-password claim

- Added Dex-readiness recording fenced by the exact durable user/hash intent, so
  a stale reconciliation result cannot ready a reset credential.
- Added a protected-local-only delivery service that commits the delivery marker
  and deletes the encrypted temporary copy before returning plaintext. It checks
  setup state, administrator membership, forced-change status, and readiness;
  ordinary retries after delivery return no password.
- Both-profile tests cover pre-readiness refusal, stale readiness, wrong-key
  failure, successful hash-matching delivery, repeat refusal, and ciphertext
  removal. A foreign-key hold injects a delete failure and proves the whole claim
  rolls back without returning plaintext, then succeeds after the failure clears.
- App/contract race suites pass with PostgreSQL enabled; formatting/vet pass.
  Explicit local reset, credential reconciliation orchestration, session/password
  changes and protected HTTP/CLI delivery remain unfinished. T030/T043 stay open.

### Administrator credential reconciliation

- Connected committed administrator intent to idempotent Dex creation and fenced
  readiness recording. The external API runs outside the database transaction;
  an unavailable/ambiguous result leaves delivery blocked for a safe retry.
- Both-profile tests read the committed intent from inside the API callback to
  prove no database transaction remains held. Failure/retry uses identical user,
  email and hash; already-confirmed readiness skips repeat API calls.
- Extended native Dex validation through actual persisted setup intent,
  reconciliation, one-time claim and VerifyPassword. The delivered random
  credential authenticates in Dex, and a later reconciliation cannot redisplay it.
- App race tests pass on SQLite/PostgreSQL; native Dex integration and formatting/
  vet pass. Visible setup operations, protected HTTP/CLI composition, explicit
  local reset, and forced-password-change transitions remain unfinished.

### Journaled administrator setup workflow

- Added atomic admission linking the initial administrator intent to one durable
  setup operation. Even a retry with a different HTTP idempotency key returns the
  linked operation rather than creating another administrator workflow.
- Added the shared-worker executor for Record initial administrator and Configure
  local sign-in. It observes durable setup evidence before applying Dex effects;
  errors leave the current step waiting and retain the completed first step.
- Both-profile tests exercise failure, retry, stable operation identity, and final
  ordered completion. App/operation/identity/contract race suites pass with local
  PostgreSQL and pinned Dex fixtures; formatting/vet pass.
- This supplies authoritative Stepper/SSE data but does not yet mount protected
  setup endpoints or render setup forms. HTTP/CLI composition, explicit reset,
  forced password change, and the remaining setup steps remain incomplete.

### Protected setup HTTP status and administrator creation

- Mounted local setup status and initial-administrator endpoints behind the exact
  loopback HTTPS guard and controller request IDs. The unauthenticated first-owner
  mutation additionally requires exact Origin, a valid idempotency key, bounded
  strict JSON, and a normalized valid email.
- The administrator endpoint admits the durable workflow, advances its two
  reconciled steps with a bounded request context, and claims the temporary
  password only after credential readiness. Successful output includes the
  completed operation; subsequent requests cannot redisplay/reset the password.
- Status exposes only setup gate/step names. Tests cover foreign Origin, unknown
  fields, Dex outage followed by successful retry, correct delivered credential,
  completed operation, secret-free status, and repeat-delivery refusal.
- App/contract race suites and formatting/vet pass. The TLS listener/CLI, setup UI,
  sign-in/password change, explicit local reset and later setup endpoints remain
  unfinished. T030/T043 are still not complete.

### Persistent loopback setup certificate

- Added explicit-path creation/loading of a 24-hour ECDSA P-256 temporary setup
  certificate covering only IPv4/IPv6 loopback. The operator must explicitly
  trust it; callers must restrict use to unactivated local setup.
- Certificate and key are published as one complete private file without
  overwriting a competing/existing identity. File and directory sync preserve
  the published trust identity across restart. Existing invalid, expired,
  symlinked, or publicly readable material is rejected without regeneration.
- Tests prove certificate stability, loopback coverage, private permissions,
  expiry refusal without replacement, malformed-input refusal and explicit paths.
  App/contract race suites, formatting and vet pass.
- Listener/CLI composition and explicit local trust presentation remain pending;
  this helper is not a production TLS fallback or a claim of runnable setup.

### Real loopback bootstrap TLS listener

- Added a listener owner that accepts only explicit loopback IP binds, supports
  an OS-selected port, and builds handlers with the actual canonical HTTPS origin.
  It serves TLS 1.3 only and has no plaintext or public-interface fallback.
- Added cancellation-driven graceful shutdown with a bounded grace period and
  joined shutdown goroutine. Close also releases a reserved, not-yet-served
  listener. Raw HTTP server diagnostics are not emitted to process output.
- Real network tests verify trusted TLS requests, rejected rebound Host, rejected
  TLS 1.2/plaintext, public-bind refusal and clean cancellation. App/contract race
  suites, formatting and vet pass; test listeners are closed afterward.
- CLI/bootstrap installation composition and local trust presentation remain
  unfinished, as do explicit reset, sign-in/password change and remaining setup
  UI/endpoints. No additional task is marked complete.

### SQLite bootstrap startup

- Added the SQLite bootstrap opener for T044. It migrates the dedicated setup
  database, seeds the singleton installation on first start, and returns its
  existing identity when setup resumes.
- Added a restart test for preserved identity, origin, state, and version, plus
  refusal after activation. Updated the administrator HTTP contract test to
  start with the bootstrap opener instead of a hand-seeded installation.
- Database, app, and contract race tests pass, as do formatting and vet.
  CLI composition and PostgreSQL preflight and activation remain open.

### Runnable local setup command

- Wired `shiftbox setup` to the SQLite bootstrap database, protected installation
  key, authenticated native Dex, administrator journal, and loopback HTTPS
  listener. The operator gets the password once on stdout and the setup origin
  and certificate fingerprint on stderr.
- The command locates Dex beside the executable by default. The explicit
  `--dex-binary` path supports local validation against the pinned executable.
  Dex uses setup-local ports and its own retained SQLite file. The command holds
  an installation lock and joins both services during shutdown.
- The CLI restart test contacts the reported HTTPS endpoint on both runs and
  checks that the password is absent from the second run and both status streams.
  CLI, app, database and contract race tests pass, along with formatting and vet.
- Browser sign-in and forced change, setup UI, activation, certificate/key flags,
  JSON output, and `shiftbox serve` still require implementation.

### Local Dex sign-in during bootstrap

- Added browser authorization-code sign-in with PKCE, nonce and state binding,
  verified Dex TLS, and the signed local connector subject. The setup callback
  accepts only the ready, active initial administrator in the local installation.
- The callback issues the existing host-only bootstrap session. Session read and
  logout are available on the guarded setup listener. The permission matrix
  denies setup mutations while the account has a required password change.
- The native command test follows the Dex sign-in and approval forms using the
  one-time password, verifies the restricted session, and checks that an unbound
  callback is denied. The affected race suites, formatting and vet passed on
  verification. The existing Dex probe-cancellation test timed out on the first
  package run, then passed three isolated runs and a full identity rerun.
- Completing the password change, serving the setup UI and activating an
  installation remain open.

### Required first password change

- The loopback setup listener now handles the contracted password change with
  the bootstrap session, exact Origin, and session-bound CSRF token. Dex verifies
  the current password and receives a bcrypt hash for the replacement over
  its private mTLS administration API.
- Successful completion clears the password-change flag and revokes the
  owner's existing sessions in one transaction. Signing in with the new
  password grants the bootstrap setup permissions.
- If Dex stores the replacement before the controller commits the flag and
  revocations, the initial password and the exact replacement can reconcile
  the same change. The native CLI test injects that controller write failure and
  verifies retry, session revocation, and fresh Dex sign-in. It also checks
  wrong-origin and incorrect-password rejection.
- CLI, app, identity, web, and contract race suites, formatting, and vet pass.
  The remaining setup endpoints, form, and activation still need wiring.

### Authenticated SQLite profile selection

- The setup profile endpoint admits only the local bootstrap owner after the
  required password change. It checks the session, Origin and CSRF in the same
  admission transaction that records the selected SQLite profile.
- Selection commits the configuring state and completed journal operation
  together. A retry returns the same operation, and setup status advances to
  the certificate step.
- Native Dex coverage verifies the password gate, wrong-Origin rejection,
  idempotent operation replay, and the next setup step. CLI, app, operation, web,
  and contract race suites pass, as do formatting and vet.
- PostgreSQL preflight and profile activation remain pending under T044.

### Authenticated browser certificate setup

- The certificate upload uses the same local administrator authorization,
  Origin and CSRF checks as profile selection and requires a selected profile.
- It validates service patterns, the matching key and the certificate chain
  against controller trust, then commits encrypted key storage, the
  configuration generation and operation evidence in one transaction.
- The setup test verifies the profile gate, validation failure followed by
  retry, operation replay, and encrypted storage. CLI, app, config, web and
  contract race suites, formatting and vet passed.
- T120 reopens the setup database before and after profile and certificate
  commits. An interrupted PostgreSQL probe saves no credentials or operation.
  A failed certificate journal write rolls back the uploaded certificate and
  configuration generation. After SQLite activation, a new process refuses
  bootstrap and the previous bootstrap session remains revoked.
- Composition uses system certificate roots. Provider selection, full
  installation activation and production HTTPS listener wiring remain open.

### PostgreSQL setup target preflight

- Added production-target preflight for operator-provisioned, empty ShiftBox and
  Dex schemas. Fresh TLS connections verify hostnames and check the scoped roles,
  database-side search paths, ownership, privileges, and schema contents.
- A throwaway PostgreSQL database with two scoped login roles passed the race
  integration test. The test rejected bad trust, shared roles, URI overrides,
  and an occupied schema. The database and roles were removed afterward.
- T031/T044 remain open for API credential collection, optional provisioning,
  migration and owner transfer, and the durable activation switch.

### Bootstrap administrator credential transfer

- Added read-only extraction of the single administrator credential from the
  pinned Dex SQLite schema. The source lookup requires the expected email
  and user ID and rejects a populated source. The transfer sends the bcrypt
  hash directly to the destination Dex credential API.
- Native setup coverage transfers the changed credential twice into a second
  Dex instance, rejects a mismatched owner ID, verifies the changed password,
  and checks that the temporary password fails against the destination.
- CLI, identity and app race suites, formatting, and vet passed. This verifies
  the transfer with independent Dex SQLite stores. Target PostgreSQL Dex
  startup, application data transfer, and authority switching remain open.

### PostgreSQL-backed Dex credential import

- The setup Dex launcher now accepts explicit scoped PostgreSQL configuration
  for the destination and selects only that storage mode.
- The native CLI integration test imports the changed administrator credential
  into a PostgreSQL-backed Dex with verified TLS. It confirms the changed
  password works, the temporary password fails, and Dex's password table is
  placed in the configured role's `dex` schema rather than `public`.
- A missing PostgreSQL CA file blocks startup, and PostgreSQL startup does not
  create a fallback Dex SQLite file. The disposable PostgreSQL database and
  roles were cleaned up after the passing race test. Formatting and vet passed.
- PostgreSQL setup still needs authenticated target collection, application
  migrations and state transfer, and authority switching.

### PostgreSQL application setup staging

- Added the scoped PostgreSQL application opener, which rechecks role and
  schema isolation on every fresh connection.
- A single target transaction stages the installation, initial administrator,
  current configuration and encrypted browser TLS material. The source keeps
  bootstrap authority, the destination stays configuring, and sessions,
  temporary password data and unrelated ciphertext are excluded.
- Live PostgreSQL integration tests verify migrations, repeat staging,
  rejection of changed destination records and exclusion of bootstrap
  credentials and sessions. Formatting and vet passed.
- Durable authority switching, credential collection, and the activation
  route remain open.

### Provider contract and in-memory adapter

- Added the controller-facing provider interface, inventory and request types,
  semantic errors, and a synchronized in-memory implementation.
- The reusable contract tests exercise lost replies, inspection before retry,
  clone isolation, stale generations, partial deletion, shared-resource
  preservation, host unavailability, and stopped-only recovery and networking.
- Provider and contract race tests, formatting, and vet passed. Native UTM
  integration is still pending, as are contract cases for concurrent mutations
  and host permission/version mismatch.

### Read-only UTM inventory

- Added a bounded JXA inventory query through the installed UTM scripting
  dictionary. It requires an exact configured version and separates missing
  resources, ambiguous ownership, permission errors, and unavailable hosts.
- Normalization checks the installation/workspace ownership marker and
  generation, preserves drive identity, and reports unknown power states
  without inferring a stop or deletion.
- Race tests passed, including an opt-in read-only inspection of UTM 5.0.8
  for a synthetic workspace. That check does not validate tagged VM drive
  extraction, mutation, artifact boot, or full owned-resource deletion.
  The later native tests documented under T122 verified these provider
  behaviors, including recovery media and preservation of the source drives.

### Native tagged VM inventory

- Verified tagged VM and attached-disk inspection using a disposable stopped
  QEMU VM on UTM 5.0.8. The opt-in test creates a unique fixture through
  AppleScript, checks its ownership before cleanup, and confirms the VM is
  absent afterward.
- The installed UTM build exposes host allocation but omitted guest capacity
  for the test drive. Inventory now represents those values separately and
  exposes whether aggregate guest capacity is known.
- Native provider and contract race tests, formatting, and vet passed.
  Artifact import/boot, lifecycle mutations, and complete owned-resource
  deletion still require the release fixture.

### Native UTM power operations

- Added serialized start and stop with pre-effect inspection and ownership
  recheck at the Apple event. Post-effect inspection determines success,
  even after a lost reply. Unconfirmed stop retains an uncertain outcome.
- Start checks observed networking and refuses a pending enrollment delivery.
  Ordinary shutdown sends a guest request. Only the explicit retention
  deletion flag selects force stop.
- The separately opted-in UTM 5.0.8 test starts a disposable blank QEMU VM,
  repeats start, force-stops the fixture, and verifies cleanup. Unit tests
  cover an unconfirmed graceful request, lost replies and prerequisite gates.
  Provider and contract race tests, formatting and vet passed.
- Graceful shutdown with the release guest, enrolled start, full provider
  composition and remaining lifecycle operations are still pending.

### Native UTM recovery media and managed bundle deletion

- The stopped-VM recovery update submits every drive ID to UTM and replaces
  only the designated removable source. Two distinct ISO sizes were observed
  on UTM 5.0.8 with the persistent disk and drive order preserved. Guest
  consumption and enrollment remain to be verified.
- Managed VM deletion checks the exact ownership generation and stopped
  state, issues UTM's delete event, and checks both registry and bundle
  absence. The live fixture proved bundle removal and repeat deletion.
  Unit tests cover lost replies and orphaned bundles.
- Production provisioning must persist the managed bundle location before
  wiring this into the provider's delete method. External enrollment images
  have a separate cleanup lifecycle.

### Native UTM workspace artifact import

- Added stopped UTM import under a deterministic workspace and generation
  bundle name. Reconciliation checks the ownership tag, artifact identity,
  compute settings, shared network, disk and media drives, and managed bundle.
  An unowned destination blocks a second import after an interrupted attempt.
- UTM 5.0.8 imported a disposable export into an independent VM, applied
  its ownership and compute settings, reused it on retry, and deleted it
  without changing the digests of the exported source.
- Production catalog staging, disk-capacity verification and adjustment,
  boot of the pinned Ubuntu artifact, and enrollment remain pending.

### Verified platform artifact staging

- Added strict amd64 manifest validation and streamed SHA-256 verification
  of every file in a UTM export. The verifier rejects missing, undeclared,
  and non-regular files. Staging writes a separate copy and removes it on
  failure.
- The native UTM 5.0.8 import test now uses the verified staging path and
  proves import, retry, and deletion preserve the source bundle's digests.
  Release, provider, and contract race tests and Go vet passed.
- Durable catalog publication, default selection, release compatibility,
  and boot validation of the pinned Ubuntu artifact remain pending.

### Platform catalog metadata and default selection

- Added the platform artifact portion of the workspace migration and its
  store. Import moves from validating to available or rejected against the
  exact manifest digest. The schema preserves published metadata and allows
  at most one default. The store checks guest protocol compatibility before
  switching defaults in one transaction.
- Race tests passed against SQLite and a disposable TLS PostgreSQL database,
  including repeated migration, conflicting releases, wrong validation
  digest, incompatible defaults, and published metadata mutation.
- The catalog still needs managed artifact publication, provider and guest
  boot validation, and connection to the administrator import operation.

### Managed platform catalog files

- Catalog import stages and verifies a separate copy, records the validating
  release, then publishes the files under the exact manifest digest. Retrying
  the same import can complete an interrupted publication.
- Workspace staging is gated on availability and rechecks the pinned
  manifest and every bundle file. Tests cover reopening without the original
  import source and reject changes to the managed manifest or disk.
- The validation entry point checks managed content around a boot check and
  keeps uncertain outcomes in validating state. A real provider and guest
  boot check and the administrator import operation still need wiring.

### Workspace capacity configuration

- Added resource profiles and versioned capacity budgets to the in-progress
  workspace migration. The workspace store validates profile sizes against
  provider limits before writing and rejects stale budget updates.
- Admission checks report the first exceeded resource and count only new
  CPU and memory on restart when the workspace retains its disk reservation.
- Capacity, catalog, and migration race tests passed on SQLite and disposable
  TLS PostgreSQL. Durable reservations and lifecycle integration remain
  pending.

### Workspace launch state and capacity reservations

- Added workspace snapshots and reservation tables in both profiles. The
  schema freezes launch inputs and makes deletion a terminal desired state.
- Start admission can reserve capacity in the same transaction as its
  operation. It serializes budget checks across workspaces and reuses retained
  disk on restart. Provider observations are fenced by instance, ownership,
  generation, and reservation time before releasing resources.
- SQLite and TLS PostgreSQL race tests cover competing starts, transaction
  rollback, retained disk, stale observations, and partial deletion.
  The workspace launch API and provider reconciliation still need wiring.

### Atomic workspace launch admission

- The workspace launch transaction now pins authorized launch inputs,
  reserves capacity, and writes the provision operation and its ordered
  steps together. Desired running state starts with unknown observed state.
- Launch retries read the original actor-scoped intent, including the
  original retention choice even if retention changed after launch. SQLite
  and PostgreSQL tests verify replay and rollback on capacity refusal.
- Template authority and profile resolution still need to be connected to
  the HTTP launch endpoint. The provider and guest must execute and verify
  the recorded workflow before it can report readiness.

### Template revision and launch authorization records

- Added template, revision, and launch-grant migrations on both database
  profiles. The template owner and revision contents are immutable and
  the selected revision must exist.
- Launch lookup reads the requested revision alongside current grant and
  archive evidence. Repository URL and Dev Container path validation is
  shared with workspace admission. SQLite and PostgreSQL race tests cover
  historical revisions, grant revocation, and archival.

### Workspace launch HTTP handler

- The launch handler resolves the requested template revision and current
  launch grant, checks the profile, artifact and provider capabilities, and
  commits the workspace, reservation and provisioning operation together.
- The HTTP contract test covers CSRF, injected owner fields, launch grants,
  revocation, profile changes, request-key conflicts, retention limits and
  capacity refusal. Management and contract race tests passed.
- Controller composition and execution of provisioning, enrollment and
  readiness steps remain pending.

### Workspace VM provisioning reconciliation

- The provisioning worker reads the committed launch and reservation, then
  inspects before creating or retrying a VM. It records the exact instance,
  stopped state and owned/shared resource inventory in one transaction.
- The worker rejects mismatched ownership, pinned settings, unknown disk
  capacity and omitted tracked resources. A fake-provider test loses the
  create response and confirms retry uses the same VM before enrollment.
- Native UTM still needs effective disk-capacity evidence and provider
  composition. Enrollment and the later workflow steps remain pending.

### Validate staged VM disk capacity

- The UTM import path checks the digest-verified standalone QCOW2 image
  against the pinned allocation before issuing the import event. Backing
  images, external data and unsupported image formats are refused.
- The native UTM 5.0.8 test verifies the exported disk's capacity and
  confirms an oversized request leaves no VM or bundle.
- This proves requested capacity at the input boundary. Attached disk
  capacity still needs provider or authenticated guest evidence. The UTM
  scripting readback only exposes host allocation on this build.

### Verify attached UTM disk capacity

- Catalog import now measures capacity after import using a temporary export
  through the supported UTM command. It matches the exported UUID, ownership
  and drive IDs against native inventory before reading the QCOW2 header.
  Verification leaves the live bundle untouched.
- Native UTM 5.0.8 import and repeat import verified the attached 64 MiB
  fixture disk. Unit tests reject a different VM or drive. Provider,
  workspace, release and contract race tests and Go vet passed.
- Temporary export has storage and time costs on real platform images.
  Production provider composition, disk resizing and release boot validation
  remain pending.

### Compiled UTM provider

- Connected the native UTM operations to the controller's provider
  interface. Creation uses the managed catalog. Inspection reconciles
  stopped disk capacity and surfaces bundle remnants when the registry
  no longer lists the VM. Recovery selects exactly one owned removable drive.
- Capability discovery checks the running UTM version and inventory
  access. Shared networking is the verified default; restricted
  network policies remain unsupported on this adapter.
- Native tests exercise compiled-interface restart reconciliation, shared
  network verification and repeated deletion. Provider, workspace, release
  and contract race tests passed. Controller startup composition and the
  full Ubuntu release journey remain pending.

### Guest enrollment grants and certificate issuance

- Verified provisioning creates the initial eligible registration. The
  enrollment store binds grants to the stopped provider instance, current
  generation and active operation, and persists only the grant digest.
- Grant consumption and certificate issuance are atomic. Same-key retries
  recover the original certificate. Other keys, expiry, revoked
  registration and mismatched provider ownership are denied. Client leaves
  have controller-selected identity and expiry capped at the CA boundary.
- Guest, workspace and contract race tests and dual-dialect migration
  checks passed. Provider delivery orchestration, the enrollment HTTP
  endpoint, renewal, and authenticated guest boot remain pending.

### Internal guest enrollment endpoint

- Added the server-authenticated TLS 1.3 enrollment listener and a bounded
  JSON route for grant and CSR submission. The route validates protocol and
  current/previous guest release before consuming a grant.
- A live local HTTPS test verifies the server against the installation CA,
  submits a real grant and CSR, and receives the issued leaf. Contract tests
  cover plaintext, incompatible releases, and unknown input fields.
- Enrollment media and controller listener composition are still pending.
  The polling and tunnel listeners continue to require verified client
  certificates.

### Enrollment recovery-media handoff

- The enrollment service makes a private, per-attempt ISO containing a grant,
  HTTPS enrollment endpoint and public installation CA. It submits the exact
  stopped instance and operation to the provider and checks the returned
  attachment evidence.
- A failed attachment invalidates the grant before removing the image. A
  confirmed attachment retains the ISO for boot. The native macOS tests
  cover lost replies, private media and redemption of the ISO's grant over
  authenticated HTTPS.
- Durable delivery tracking, cleanup after guest consumption, and the actual
  Ubuntu boot-time reader remain pending.

### Durable enrollment media tracking

- Persist the private image directory against the grant before asking the
  provider to attach it. A restart reuses a recorded image and repeats
  attachment only when its confirmation was interrupted.
- Cleanup removes media for consumed grants after authenticated guest contact
  and for invalidated or expired attempts. Retrying cleanup after removal is
  safe. The native ISO test checks recovery and preservation before use.
- The workspace enrollment worker still needs to call these operations and
  confirm guest contact. A crash while building an ISO before the tracking
  write can leave an untracked file with an expiring grant.

### Authenticated guest contact

- The internal poll route derives workspace identity from the verified TLS
  leaf and rechecks the current registration, serial, key, generation and
  owner before recording contact. Unsupported versions and undispatched
  operation reports are refused.
- Recovery media now survives issuance and is eligible for cleanup after
  the current key proves possession over mTLS. A live TLS test covers
  contact, forged workspace input, revocation and media cleanup.
- Guest operation dispatch, poll replay and bounded execution evidence
  still need implementation.

### Workspace worker enrollment step

- Launch reconciliation now attaches the operation's enrollment media to
  the verified stopped VM, rechecks current launch authority, and starts
  it. A running VM is reconciled without replacing media or starting again.
- Enrollment completes only when the exact VM and generation have a
  consumed grant for this operation and current authenticated guest
  contact. Cleanup follows that evidence.
- Worker tests cover a lost start reply and contact arriving after worker
  restart. The Ubuntu agent, live boot and remaining guest workflow steps
  are still pending.

### Guest-side enrollment client

- The guest client uses the recovery seed's CA and controller HTTPS name,
  generates an ECDSA key locally, and stores a pending key bound to the
  grant digest across interrupted exchanges. It validates and atomically
  persists the issued identity without retaining the grant.
- The local TLS test drops the connection after issuance, proves the retry
  uses the same key, and reloads the certificate from disk without media.
  Guest, API, and contract race tests passed.
- Agent CLI and supervision, boot media discovery, polling and local
  operation execution still need wiring.

### Supervised guest agent entry point

- Added `shiftbox-agent serve` with supervisor-owned paths. It loads a
  valid identity or enrolls from mounted recovery media, then polls over
  mTLS on a jittered 30-second cadence.
- The systemd unit runs a launcher that mounts the SHIFTBOX ISO read-only
  inside the service's mount namespace. The guest command passed a Linux
  amd64 build, and local TLS and CLI race tests passed.
- The platform build still needs to install the unit, launcher, binary and
  controller poll endpoint configuration. Guest operation dispatch, local
  attempt journaling, renewal and validation inside Ubuntu remain pending.

### Installation-specific guest endpoints in recovery media

- Enrollment delivery now supplies both internal HTTPS endpoints and the
  public CA. The agent validates them and persists the mTLS poll endpoint
  with its identity, keeping the Ubuntu artifact installation-independent.
- The TLS test has separate enrollment and polling listeners and verifies
  that a restarted agent polls with its saved identity after removing the
  seed. Native media tests verify both endpoint values are present.

### Internal controller listeners

- Added paired internal HTTPS listeners with separate enrollment and mTLS
  polling routes. Their bound addresses and guest-reachable host produce
  the URLs for recovery media.
- Internal server certificates use fresh keys and exact DNS/IP names under
  the retained CA. New handshakes replace the leaf after four hours, and
  issuance observes the eight-hour lifetime and issuer expiry.
- Local TLS tests cover both listeners, certificate rotation and shutdown.
  Installation CA provisioning and `shiftbox serve` startup composition
  still need wiring.

### Installation guest certificate authority

- Setup provisions a stable installation guest CA before starting Dex.
  Its signing key is encrypted under the installation key, scoped to the
  installation ID, and stored with the public certificate in one private
  atomically published file.
- The runtime loader refuses missing, damaged, expired or mismatched trust
  material. The initial root lifetime is five years. Native Dex setup
  restart tests confirm the CA record is reused.
- Production serve and management backup/restore still need to load or
  preserve this retained authority.

### Controller active-database preflight

- Added a startup opener for the explicitly configured database. It
  requires an active installation and a matching committed configuration
  generation, profile, origin and service settings.
- Startup refuses a missing SQLite file before opening it. A failed
  PostgreSQL connection never reads or mutates the bootstrap SQLite file.
  Application, database and contract race tests passed.
- The setup activation endpoint, authority switch, and controller command
  composition still need wiring.

### SQLite setup activation

- The protected activation route checks owner credential readiness, the
  selected SQLite profile, browser TLS, a compatible default artifact and
  a capacity budget. It atomically records activation and retires restricted
  sessions, then clears the browser cookie after commit.
- Tests inject failure during session retirement and prove both the active
  state and operation roll back. A successful retry denies the retired
  session. CLI, app, configuration and contract race tests passed.
- The provider/artifact setup UI, PostgreSQL activation switch and fresh
  production sign-in still need wiring.

### Controller CLI preflight

- `shiftbox serve --config` now checks the selected active database, complete
  migration history, private installation directory, retained key and guest
  CA, and current browser certificate before listener startup.
- Tests cover missing databases, gaps in migration history and redacted CLI
  validation. Native setup and controller race tests passed.
- Successful preflight still stops at the runtime composition boundary.
  Dex, management and internal HTTPS, proxy and reconciliation need startup
  wiring before the command can serve an installation.

### Operation lookup and progress streaming

- Added authenticated operation detail and SSE routes using the current
  workspace owner or the operation's recorded installation authority.
  Unauthorized workspace operations use the same response as missing IDs.
- The launch contract test fetches the accepted operation and reconnects
  the stream from its last event ID. The stream uses the existing
  revocation registry and rechecks current session and scope.
- Web, operation and HTTP contract race tests passed. The production
  management router and workspace UI still need to mount these routes.

### Workspace management read endpoints

- Added owner-scoped workspace listing and detail, with administrator
  visibility, observed-state filtering, cursor pagination, and a version
  ETag for detail reads.
- The launch API test verifies the current operation and separate requested
  and observed states after launch. Member privacy and administrator reads
  are tested alongside operation replay and the contract suite.
- Controller startup composition and the workspace UI still need to mount
  and consume these endpoints.

### Workspace start and stop

- Management handlers check current authority and version, replay committed
  requests, and serialize workspace actions. Start commits requested state,
  compute reservation, and the operation together. Stop leaves accounting
  intact at admission.
- The power executor inspects the exact VM before applying an effect. It
  confirms graceful stop before releasing compute and waits for fresh guest
  contact after start. Both actions retain the disk and provider generation.
- Contract and worker race tests cover rollback, stale versions, exhausted
  capacity, duplicate requests, and lost provider replies. Startup wiring,
  the remaining lifecycle actions, and live guest readiness are still open.

### Active local sign-in

- Added the management HTTPS login and callback handler. It reuses the
  bootstrap flow's PKCE, state and nonce checks, then consults the active
  database using the verified Dex user ID. The active flow has its own
  host-only cookie and commits each session before issuing it.
- The pinned native Dex test verifies normal and password-change sessions,
  denial for inactive installations and ineligible users, and refusal on
  a foreign host or unbound callback. Setup and authentication race tests
  also passed.
- The controller command still needs production Dex and management router
  composition. Active password changes and upstream identity stay open.

### Managed Dex for the controller

- Added a stable management-origin Dex issuer and a browser proxy limited
  to the login flow. Discovery, token exchange, signing keys, and
  administration use the privately authenticated loopback listeners.
- Native Dex race tests sign in with retained credentials using both SQLite
  and PostgreSQL identity storage. They verify that the browser login form
  uses the management origin and that browser requests cannot reach
  private identity endpoints. PostgreSQL startup leaves SQLite absent.
- Production startup still needs to load scoped PostgreSQL credentials
  from protected configuration and mount the browser and management routes.

### Protected PostgreSQL identity configuration

- Target staging now commits the scoped Dex credentials alongside the
  copied setup records. The installation key encrypts the record and binds
  it to the installation. A retry accepts only matching credentials.
- Controller preflight loads the retained record for the PostgreSQL profile
  and verifies that Dex uses a separate role on the application database.
- Unit tests cover credential and ciphertext conflicts. Live PostgreSQL
  tests verify scoped staging, failed-transaction rollback, and no copying
  of unrelated protected records. The setup HTTP workflow and authority
  switch still need wiring.

### PostgreSQL authority switch

- A staged target records the activation intent before bootstrap retirement.
  The source transaction blocks local setup and revokes its sessions, then
  the target commits activation. A receipt on the source binds retries to
  the same target operation.
- A failure-injection test closes and reopens both stores after a target
  commit failure, verifies that ordinary local setup cannot reopen the
  source, and resumes the original activation. Live PostgreSQL tests cover
  readiness validation, replay, and selection by the runtime DB opener.
- T120 injects a failure in the live PostgreSQL activation transaction after
  SQLite retirement. With both stores reopened, protected recovery completes
  the original pending target operation from the encrypted selected profile
  without a browser session or a second Dex transfer.
- Protected setup still needs to orchestrate profile selection, Dex
  transfer, cutover, and restart recovery from the command and HTTP routes.

### PostgreSQL profile selection in local setup

- The protected profile endpoint validates scoped schema ownership and
  PostgreSQL TLS before saving encrypted pending credentials. It rechecks
  current session authority and the installation version before commit.
- The local command accepts the Dex connection through a private
  `--postgres-identity-config` JSON file with `dexUri` and optional
  `caFile`. The browser request carries the scoped application URI.
- The native setup HTTPS test selects PostgreSQL against disposable
  isolated schemas. Tests cover replay, redaction, and refusal of SQLite
  activation while PostgreSQL is selected.
- This path accepts pre-provisioned schemas and roles. The setup
  activation endpoint still needs to stage the target, transfer the
  Dex credential, and invoke and resume the authority switch.

### PostgreSQL activation through protected setup

- The protected activation endpoint opens the selected target, stages setup
  records, transfers the changed owner credential through managed Dex,
  and invokes the cutover. It rechecks the selected profile and the
  current bootstrap session before retiring the source.
- Local setup startup can resume the exact recorded cutover from the
  encrypted selection and retirement receipt before opening its listener.
- The live PostgreSQL HTTP test covers a failed credential transfer, retry,
  session retirement, recovery, and runtime selection. It uses an injected
  transfer callback and seeded provider readiness records. Native Dex
  transfer has its own integration coverage. Provider setup and the
  complete product startup remain open.

### Infrastructure capacity routes

- Added the reviewed administrator routes for budget updates and resource
  profile creation. Writes use the authorization transaction, budget
  changes check the current version, and profile resources use verified
  provider bounds.
- Contract coverage exercises denied members, CSRF, first-budget creation,
  stale budget updates, over-limit profiles, and duplicate profile names.
  The management router still needs to mount the handlers. Provider
  validation and capacity configuration in local setup remain open.

### Capacity in protected setup

- Local setup accepts the versioned capacity write after the browser
  certificate step. It reuses the management budget validation and uses
  the restricted setup session for authority.
- PostgreSQL staging copies the budget and its revision, and cutover
  checks that the source revision still matches before retiring setup.
  The live integration test edits the budget after a failed transfer
  and confirms the retry activates PostgreSQL with the newer revision.
- Default artifact import and real UTM guest validation remain open.

### Ubuntu Minimal guest build

- Pinned Packer 1.14.2, the upstream UTM 5-compatible plugin revision,
  and the Ubuntu 24.04 Minimal amd64 cloud-image checksum. The local build
  cross-compiles the guest agent and uses a temporary SSH key.
- UTM 5.0.8 booted and provisioned the Ubuntu image, enabled the systemd
  agent, shut down, exported the bundle, and removed the disposable VM.
  Supported UTM scripting then preserved the persistent drive, removed the
  build-only network, and added the empty removable recovery drive.
- The release candidate still needs browser IDE and container tooling,
  artifact manifest generation, and boot and enrollment validation of the
  final prepared export before catalog admission.

### Live Ubuntu enrollment verification

- Added an opt-in UTM release test that imports the prepared Ubuntu
  export, uses the normal launch worker and recovery media, and waits
  for the guest's authenticated poll. Its catalog and IDE metadata are
  temporary fixtures and cannot establish production artifact readiness.
- The live import exposed a rename to the embedded build name. The
  provider now explicitly sets the managed VM name and a workspace
  MAC address through UTM while preserving the network and drive layout.
- The first Ubuntu clone booted and started the agent, but its journal
  showed the agent could not open the enrollment file. The launcher now
  mounts ISO9660 media without Linux's lowercase filename mapping.
- Rebuilt with Packer and verified the corrected Ubuntu export on UTM
  5.0.8. The real guest consumed the grant and authenticated in 2m05s
  after provisioning. The race-enabled release test passed in 134s,
  including verified clone cleanup. Full release validation still needs
  the selected browser IDE, container tooling, and the remaining gates.
### Ubuntu guest toolchain

- Packer 1.14.2 and UTM 5.0.8 built and exported the pinned Ubuntu 24.04
  Minimal amd64 guest with Docker Engine 29.9.3, containerd 2.4.2, Buildx
  0.39.0, Node.js 24.22.3, Dev Container CLI 0.90.0 and Ubuntu Git 2.43.0.
  The build verified archive digests, the running Docker daemon and tool
  versions before sealing the VM.
- The race-enabled UTM enrollment gate imported a disposable copy of that
  export, authenticated the guest in 3m50s, and cleaned up the copy. The
  full test took 244.10s. Packer recipe validation and release race tests
  passed.

- T039 supplies the real Chrome journey for setup, SSE reconnection, IDE
  readiness, retained edits, and repair. Playwright discovery and a local
  Chrome editor smoke test with pinned code-server passed. The complete
  native journey remains T061.

### Native repair candidate

- The repair-enabled `development-repair` artifact built and exported on
  UTM 5.0.8. Native catalog validation passed in 56.53 seconds. The
  enrollment and offboarding test passed in 66.89 seconds and confirmed
  the stopped VM kept its owned disk and released compute capacity.
- T061's real Chrome journey completed protected setup and activation
  with the candidate. T060 currently stops management startup at the
  missing `importPlatformArtifact` handler. Full browser acceptance
  and the release manifest remain open.
- The disposable PostgreSQL schema was recreated from current
  migrations after its stale development schema failed verification.
  The PostgreSQL contract and release parity tests then passed.

### Active platform import

- T120 reopens the controller database after admission and during an
  interrupted import. The worker respects the outstanding lease and retries
  with the original operation ID and folder. After validation commits,
  catalog replay uses the verified managed files without booting again.

- T099 wires administrator platform import to the controller. The
  request contains an absolute local folder and an optional default
  selection. Its actor-scoped key binds both values to a durable
  operation, and the worker rechecks administrator membership before
  staging. Setup and active import share the manifest, compatibility,
  disposable boot, and catalog verification path.
- The Go race suite passed with the local PostgreSQL fixture. A real
  controller startup advanced past the platform import route and
  stopped at the next missing handler, `listMaintenanceCampaigns`.
  T060, T061, and the remaining T099 handlers stay open.

### Maintenance scheduling

- T095 and T099 add durable campaign and target tables plus the
  administrator list and schedule routes. Admission records the
  campaign operation and retained workspace generation and power states
  in one transaction. The contract test covers administrator and CSRF
  checks, deadlines, unknown releases, and replay after the deadline.
- T060 native startup passed both maintenance routes and stopped at
  `runMigrationPreflight`. T095 now presents the target release and
  deadline on the retained workspace and excludes deletions already
  committed when the campaign is scheduled. T101 lists and schedules
  campaigns from Infrastructure and follows the server operation.
  The shared Stepper keeps the waiting text and icon stable while the worker
  briefly reclaims the same active step. A regression test reproduced the
  flash, and all 89 UI unit tests and six Chrome operations cases passed
  with the fix.
  Due-campaign preparation now creates one durable operation for each
  eligible workspace generation after existing lifecycle work settles. It
  records the latest desired state and reuses the same operation after a
  restart. An already targeted workspace with deletion underway waits for
  verified removal without creating an update operation.
  The persisted deadline fences new lifecycle actions and retention deletion
  before the worker has claimed the target. Previously committed work still
  reconciles, and terminal update failures allow explicit retirement.
  The campaign worker waits until the deadline and aggregates the target
  operation outcomes. A failed target blocks campaign completion. The
  controller starts both workers beside the guest listeners and coordinates
  their writes with the backup checkpoint.
  Guest staging now verifies the release and protocol, Ed25519 signature,
  binary size, and SHA-256 digest before replacing a private staged file.
  Verification tests use disposable keys. The durable trial record selects
  the last confirmed agent after an interrupted trial. Confirmation requires
  the matching digest and attempt token, and an identical failed candidate
  requires an explicit retry. The platform launcher now selects the agent
  through its fixed supervisor entry point, checks the recorded binary digest
  before launch, and falls back when the candidate is missing or changed.
  The selected agent confirms its trial only after its authenticated poll
  succeeds. A rejected poll leaves the trial unconfirmed for supervisor
  rollback. The controller can load the pinned binary and detached signature
  from the private local release directory. Tests cover the shared signed
  metadata and rejection of changed or linked artifacts. The internal guest
  listener serves the download only during the authenticated workspace
  generation's staging step and rechecks that assignment before sending
  bytes. The guest downloader derives its endpoint from the poll origin,
  rejects redirects, and stages verified downloads by digest. The guest poll
  loop now handles the staging command and journals the digest for replay after
  restart. The controller dispatches staging only for the active workspace
  generation and persists its receipt. Identical receipt replays succeed and
  changed receipts are rejected. The image build now installs a supplied
  Ed25519 publisher public key in the guest's fixed trust path. The offline
  signing command matches the pinned manifest before publishing a detached
  signature. Guest activation now starts a supervisor trial only for the
  staged operation and release, then reports confirmation or rollback on
  redelivery. The controller now dispatches activation for the staged
  operation, stores the result separately from the staging receipt, and
  checks that successful activation reports the assigned release and staged
  digest. The target executor compares the staging receipt with the pinned
  artifact, fails on rollback, and requires the assigned release and
  protocol at verification. The maintenance power executor can reserve
  capacity and boot the exact retained VM while its desired state stays
  stopped, then release compute after a provider-confirmed graceful stop.
  A stopped target waits for its pinned release files before reserving
  compute or starting the VM. It resumes when the files become available.
  Failed staging or activation now preserves the failed step, skips
  remaining update steps, and still performs the graceful stop before
  settling the target as failed. When configured with the enrollment
  service, maintenance can deliver recovery media for an expired guest
  registration while keeping desired state stopped. The boot step waits
  for fresh authenticated contact before clearing the media. The runtime
  test drives a persisted campaign through signed download, staging, trial
  confirmation receipt, and completion over the real mutual TLS listeners.
  It uses a fixture guest client. The native UTM race run then proved
  signed rollback and successful replacement on a retained stopped guest,
  restoring its stopped state and keeping the same owned disk in both cases.
  Mutual TLS tests also prove a management-only guest can poll while
  development commands, tunnel and preview offers, secrets, and Git
  credentials are withheld. A lost graceful-stop reply keeps reservations
  until a fresh provider observation confirms shutdown.
  A blocked campaign remains in history after verified retirement of its
  failed targets, and it no longer prevents scheduling a new campaign.
  An administrator can schedule a replacement for retained failed targets.
  Migration stays blocked until the replacement succeeds for the exact
  workspace generation or the provider verifies removal.
  A replacement operation can retry the same signed digest after rollback.
  Redelivery of a failed operation only reports the recorded rollback.
  T101 refreshes the campaign list while work is scheduled or active and
  when a viewed workflow settles. The latest blocked campaign explains
  the administrator's replacement and retirement options.
  Before scheduling, Maintenance shows the current capacity budget and
  reviews retained workspaces by name across inventory pages. A failed
  inventory read leaves scheduling unavailable and can be retried.
  Offboarding uses workspace names in its preview and stop progress.
  All 88 UI unit tests, the production export, and 24 Chrome fixture tests
  passed. Live operations acceptance remains under T103.
  T088 has two Chromium fixture scenarios covering safe-boundary waiting,
  rollback display, destructive confirmation, and unresolved cleanup.
  Both passed using the exported UI and installed Chrome. The broader
  operations journey depends on the native workspace validation in T061.
  T088 expanded the installed-Chrome fixture run to six passing cases,
  including inactivity and recovery grace, refused long-stop recovery,
  workflow access revocation, and private preview navigation. Live proxy,
  sibling-origin and native recovery checks remain open.
  The combined T088 and T103 rerun passed all seven Chrome cases and the
  Go acceptance test with the race detector. The live case used the exact
  rc2 controller and an independently enrolled test guest tunnel. The
  native UTM preview rerun remains in T124 and T125.
  T120 reopens the controller database after staging and activation receipt
  writes with an outstanding worker lease. Both confirmed activation and
  rollback settle the same persisted target and campaign after restart.
  Verification waits for the assigned release and protocol even after the
  supervisor trial succeeds. The campaign worker also resumes across
  restart after committing completed or blocked state and before finishing
  its journal step.
  The restart suite also interrupts a management boot and graceful shutdown
  after each provider effect. The replacement worker honors the outstanding
  lease, requires fresh guest contact after boot, and verifies stopped power
  after either a verified update or a staging failure. The worker releases
  CPU and memory only after that verification, while disk stays reserved.
  The campaign waits for restoration before recording its final outcome.

### Migration preflight CLI

- T097 and T102 add a preflight-only CLI that reads a bounded pinned
  release manifest and reports stable blockers. It uses the existing
  active database and trust checks, current retained guest evidence,
  UTM capability checks, and the pinned Dex binary digest. PostgreSQL
  dump and restore tools must match the application server major.
- Unit tests cover manifest rejection and blocked preflight output. Preflight
  verifies that the application and Dex PostgreSQL roles still own their
  separate schemas and use the expected search paths. A disposable PostgreSQL
  test passed with scoped roles and verified refusal when the Dex login was
  missing or unusable, or the application search path had changed.
- The native PostgreSQL tool check passed with matching `pg_dump` and
  `pg_restore` against a disposable database. It blocked when `pg_dump`
  reported an incompatible major or `pg_restore` failed to run. The CLI
  returned exit 7 with `migration.installation_unverified` against the older
  SQLite acceptance snapshots, whose schema versions were 17 and 19 while
  the current executable expects 27.
  On a disposable copy at schema 27, the CLI returned ready after a
  verified SQLite backup with a matching test manifest. It refused the
  archived Dex pin and reported a synthetic stopped, previous-release
  workspace as a blocker until its record matched the deployed release.
  Details are in `documentation/validation/quickstart-results.md`.
  Irreversible migration acceptance remains open.

### Migration preflight management route

- T097 and T099 share the CLI migration checks with the administrator API.
  Place the pinned manifest at `releases/<releaseId>/release.json`
  beside the configuration passed to `shiftbox serve`, with private
  directories and file permissions. Admission persists the release ID
  for retries, and the worker reports blockers on the operation.
- Preflight confirms the private installation key on disk matches the key
  in use and verifies write access to both installation and UTM storage.
  Tests cover replaced, missing, and non-private keys and unavailable UTM
  storage.
- The check-only CLI returned ready on `mvp-rc1` with a verified backup
  and a current retained UTM workspace. A separate native maintenance run
  proved that a rollback blocks that guest gate until successful activation.

### Backup and recovery schema

- T109 creates the backup and recovery review tables in migration 18 for
  both database profiles. Migration 6 is already used by GitHub OAuth
  state. The schema requires archive verification metadata for completed
  backups and administrator approval metadata for activated reviews.
  Both profiles passed the constraint test under the race detector.
- T110 still owns verifying actual archive contents and committing the
  result. T112 owns recovery-mode authorization and activation checks.

### Management archive and checkpoint primitives

- T110 writes a canonical management manifest and an age passphrase
  archive assembled from a fixed list of staged files. Before publishing
  it, the code closes the encrypted file, decrypts the entire stream, and
  checks every recorded file digest. Archive publication refuses to
  replace an existing file.
- T104 exercises the checkpoint fence callback through success,
  cancellation, and acquisition failure. The SQLite checkpoint test
  uses a real WAL database and verifies that the snapshot includes
  committed changes and excludes later writes.
- T110 stages application and Dex SQLite snapshots with independent
  copies of the configuration, installation key, and guest CA. The
  capture requires private staging and rejects missing or uninitialized
  Dex stores, symlinked protected inputs, and files with public permissions.
- T110 exports PostgreSQL application and Dex schemas with separate
  schema owners using the same read-only snapshot. It releases the
  checkpoint fence before running the native dumps. The PostgreSQL 18
  fixture test checks that both dumps exclude writes after the cutoff
  and unrelated schema contents. Credentials stay in private temporary
  password files outside the archive allowlist.
- T110 records checkpoint progress and commits a verified archive's
  digests, size, and operation completion together. Failed captures
  commit a sanitized failure. T097 checks the completed evidence for
  the same installation, release, profile, and application schema.
- T104 and T110 add a cancellable checkpoint fence around controller
  requests and background writes. SQLite and PostgreSQL independently
  drain Dex database writers during capture. The controller leaves
  established workspace tunnels and operation event streams running.
- T113 connects the administrator backup route to the live checkpoint
  and archive service. Admission is durable and idempotent. The worker
  receives the passphrase in memory after commit, and startup marks any
  interrupted attempts failed so a new backup can be submitted.
- T120 covers restart while preparing, checkpointing, and encrypting,
  after an archive is published without a completion record, and after a
  completed backup. Startup preserves the published archive and accepts
  only committed digest and size evidence for migration preflight.
- T104 and T110 remain open for live workspace computation evidence.

### Restore validation

- T105 exercises full archive and digest checks, release and schema
  compatibility, the original Mac binding, and matching installation
  keys. The PostgreSQL fixture verifies that validation accepts empty
  separately owned schemas and preserves rows in a rejected occupied
  target. SQLite target checks reject existing files and sidecars.
- T111 adds read-only validation and binds new production archives
  to the current Mac.
- T111 persists private validation records bound to the verified archive
  and replacement target. Restore staging reauthenticates the archive
  with a freshly supplied passphrase, checks the target again, and
  removes staged files if verification fails.
- T111 can publish a verified SQLite stage to a new protected directory
  with an exclusive rename. T112 commits the recovered installation,
  initial review, old session and grant invalidation, GitHub reconnect
  requirement, and interrupted job records in one transaction before
  publication. The gate and rollback passed SQLite tests, and the gate
  passed a disposable PostgreSQL test.
- T120 reopens a saved SQLite validation after interrupting publication.
  The retry verifies the encrypted archive again and publishes a replacement
  with one identity review and revoked copied sessions. The source stays
  active and replay cannot overwrite the published replacement.
- T120 reopens the recovery database after each review decision and a final
  approval rolled back at its audit write. The installation stays in review
  until the subsequent approval commits the workspace observation, session
  retirement, operation, and audit event together.
- T111 verifies both PostgreSQL dumps before loading them into a
  replacement database. Each pre-provisioned owner restores only its
  schema in its own transaction. The local PostgreSQL 18 test confirmed
  new role ownership, damaged dump rejection before load, and refusal
  to retry against an occupied target. A forced failure of the second
  load left only the committed Dex schema. The CLI directs the operator
  to keep the replacement offline and prepare an empty target. The
  fixture revalidated and restored both schemas after it was reset.
  Full recovery acceptance remains open under T116.
- T120 terminates a disposable PostgreSQL restore process after the Dex
  schema commits and before application loading. A fresh process refuses
  the occupied target and preserves the committed Dex rows. After fixture
  reprovisioning both schemas restore successfully.
- T112 records ordered identity, provider, and authorization/retention
  decisions. Provider ownership conflicts block approval, and activation
  requires fresh matching evidence and a current administrator. Approval
  commits review history and reopens the installation with fresh sign-in.
  T112 and T113 allow the serving preflight to open a restored
  installation only with an unfinished, linked review. The restricted
  listener issues local administrator review sessions and serves review
  and activation routes without guest listeners or retention workers.
  Review activation closes that listener and reruns startup checks.
  T106 recovery tests verify that ready review still blocks stale workspace
  credentials and overdue deletion, provider mismatches block review, and
  explicit activation revokes the recovery session.
- T108 and T115 passed the recovery UI unit tests, static build, and both
  Chrome journeys with API fixtures. A native SQLite controller also
  completed browser backup and validation, protected CLI restore, and
  browser review and activation with a disposable empty workspace inventory.
  The November 14 populated `mvp-rc1` run passed browser backup with a live
  IDE, protected restore, matched native provider inventory, and explicit
  recovery activation. The Go journal verifier passed with `-race` on the
  same archive and the source and restored databases.
- T113 registers restore validation on the active router and keeps
  review and activation on the recovery listener. Validation uses
  the current installation identity and retained key, records the
  validation ID as operation evidence, and retains the passphrase
  and replacement database logins only in memory.
- T113 requires the write-only Dex login for PostgreSQL replacements.
  The route test rejects an omitted login and checks that neither database
  login nor the archive passphrase appears in the response or operation
  history. T111 PostgreSQL validation and activation tests passed with
  separately owned fixture schemas and verified restored Dex data.
- T060 starts the native controller with its embedded UI and full active
  route catalog. A disposable activated SQLite fixture served the UI
  over trusted HTTPS and returned 401 for a signed-out session. The CLI
  created a verified backup through the running controller and validated
  that archive without creating a replacement. Tagged race tests passed
  for embedded assets, native setup with pinned Dex, and agent CLI
  dispatch. T061 still requires the complete fresh UTM and Chrome journey.
- T061 repeated fresh protected setup and UTM image validation in Chrome,
  signed in again on the management origin, and reached the GitHub
  authorization page. The run timed out waiting for authorization.
  The pinned candidate manifest and read-only Go journal check are in place.
- T114 exposes `shiftbox restore validate` with terminal or explicit
  stdin passphrase input. It checks against the installed executable's
  schema and the operator-supplied installation and release IDs, then
  writes a private validation record. Validation does not require a
  working source database. SQLite activation reopens the saved validation,
  rereads the passphrase, requires the controller installation lock, and
  publishes a recovery-only replacement with a rebased configuration.
  PostgreSQL activation loads scoped dumps, seals the replacement Dex
  credentials in the recovery-entry transaction, and publishes a new
  controller configuration only after review is in place. The local TLS
  fixture verified a replacement in recovery and an unchanged source.
  The backup CLI uses the running controller's private Unix socket and
  shares the browser backup service. Its test verifies passphrase
  confirmation and waits for durable archive verification. T107 passed
  race-enabled API and CLI contract tests for passphrase handling, durable
  steps, stable JSON and exit codes, validation IDs, and explicit
  activation.
- T046 remains open for the selected browser IDE and release artifact
  validation. The UTM test ends at authenticated enrollment. It does not
  prove a real project checkout, Dev Container launch, or browser IDE.

### Workspace runtime scheduling

- The workspace runtime binds the guest endpoints and starts one durable
  worker for workspace launch, start, and stop. The worker filters pending
  operations by their recorded admission action before claiming them.
- The race-enabled UTM 5.0.8 enrollment test now queues the launch and
  lets the shared runtime pick it up without direct test-side reconciliation.
  The guest authenticated in 3m50s. The test passed in 237.68s and removed
  the disposable VM. The test still ends at authenticated enrollment.
- Production management composition and full workspace acceptance remain
  open under T060 and T061.

### Foundation schema preflight

- Startup checks that required foundation tables and operation authority
  columns exist after verifying migration history. Earlier development
  databases can have current history with an older foundation layout.
- Valid SQLite and fresh PostgreSQL schemas passed. A PostgreSQL fixture
  missing the operation authority column and SQLite fixtures missing
  foundation records were rejected before controller startup. The check
  leaves database repair to the reviewed recovery workflow.

### Embedded browser assets

- `make build` builds the Next.js static export and embeds it into the
  ShiftBox controller binary. CI builds the tagged binary and verifies
  the generated home page and a script referenced by it.
- The local setup listener serves the embedded export through its existing
  loopback HTTPS guard. A live run with the pinned Dex binary served the
  home page, JavaScript, and setup status API from the same origin.
- The dedicated setup flow and active controller composition remain open
  under T058 and T060.

### Local setup credentials UI

- The embedded setup page uses the server's setup status for its Mantine
  Stepper and supports one-time password acknowledgement and required
  password replacement. Dex returns to the setup page after sign-in.
- UI tests cover first-invalid focus, password acknowledgement, removal
  of credentials from the form, and the fresh sign-in prompt. The tagged
  Go test and a live Chrome run passed against pinned Dex and loopback TLS.
- The database, certificate, provider and activation screens remain open
  under T058.

### Database and certificate setup UI

- Local setup submits the SQLite or scoped PostgreSQL profile and browser
  certificate files over the restricted session. The certificate form
  builds the approved IDE and preview host patterns from DNS suffixes.
- UI tests cover rejected PostgreSQL configuration and retry, first-invalid
  focus, certificate submission, and advancement from server-reported
  progress. Chrome verified SQLite selection and certificate form validation
  against the embedded setup server. Provider and activation remain open.

### Capacity and activation in local setup

- The setup status reports the saved capacity revision and compatible
  default image prerequisite. The setup page uses the revision when
  saving capacity and advances after the server reports both prerequisites.
- Activation requires acknowledgement that the local session ends, then
  offers fresh sign-in at the configured management origin. Go tests cover
  the status transition and committed SQLite activation. UI tests cover the
  capacity request, acknowledgement, and management sign-in link.
- Live provider validation and importing the initial workspace artifact
  through setup remain open under T058. The controller still needs T060
  composition before the management sign-in journey can run end to end.

### Management services composition

- Assembled the implemented workspace, template, operation, capacity, and
  Git handlers over the shared journal, sessions, and connection registry.
  Production API registration still requires the full reviewed catalog.
- The management site mounts API requests, interactive Dex, and the
  embedded UI, with setup pages and internal guest endpoints excluded.
  A pinned Dex HTTPS test signs in through the composed site and reads the
  guarded workspace list.
- Outstanding management handlers still prevent production CLI startup
  under T060.

### Template editing and launch grants

- Owners and administrators can change template metadata and archive or
  restore the template with a version check. Archiving blocks new launches
  and preserves revisions, workspace provenance, and existing grants.
- Launch grants require explicit acknowledgement of project-secret access.
  Grant and revocation changes are audited. Replaying a grant request
  after revocation cannot restore access.
- The contract test verifies owner and administrator access, launch denial
  after archive, visibility for grantees, and revocation. Guest-side project
  secret resolution remains open.

### Protected application secrets

- Added exactly three scopes, context-bound encryption, explicit empty
  overrides, and workspace resolution from current owner and template
  grants. Management handlers return metadata, require fallback
  acknowledgement on deletion, and exclude values from audit entries.
- Service and HTTP contract race tests passed. The new migration passed
  on SQLite and fresh PostgreSQL. Wiring workspace resolution to the
  authenticated guest endpoint and surfacing fallback in the UI remain
  open.

### Guest secret delivery

- Wired the workspace secret endpoint on the internal mTLS listener.
  Authentication and resolution share one transaction, and the listener
  uses the application's encrypted secret store. The endpoint accepts
  the name without any caller-selected scope or owner and marks responses
  non-cacheable.
- The real TLS test covers an empty user override, organization fallback,
  absent names, scope injection, a stopped VM and a revoked registration.
  Guest, app, web, secret and contract race suites passed.
- The in-guest command and its exit codes remain open under T075.

### In-guest secret command

- Added `shiftbox secrets get-secret` with exact stdout bytes and stable
  success, missing, denied, and unavailable exits. Each call uses the
  agent's Unix socket and a new controller request, including reads after
  a prior success. The private guest identity remains outside the
  container mounts.
- The guest image provides the command on the VM and mounts a separate
  runtime copy and socket directory into the development container.
  Linux amd64 build and local socket, CLI, and Dev Container invocation
  tests passed. A rebuilt UTM image and live container check remain open.
- The standalone Git credential-helper command remains open under T075.

### Workspace Git credential helper

- The guest command implements Git's credential protocol over the agent
  socket. For HTTPS GitHub requests it sends the expected repository, which
  the controller compares with the workspace's recorded repository before
  resolving the current owner token. Store and erase do not persist tokens.
- Tests verify credential-only output, rejected repository context,
  reconnect-required handling and controller outage. A real Git invocation
  confirmed helper dispatch with `credential.useHttpPath=true`. The Linux
  guest binary builds. Live GitHub authorization and the rebuilt UTM
  container path remain unverified.

### Workspace list and detail UI

- Added the static workspace route, cursor-based listing, desired and
  observed state, resource details, and retained-data guidance. Versioned
  start and stop requests use the current session and the returned operation
  ID drives the shared WorkflowStepper.
- UI tests cover listing, paging, separate state, workflow failures, and
  start/stop request wiring. Chrome verified the exported route with
  fixture API responses.

### Workspace launch UI

- Added keyboard-selectable templates, review of the exact saved revision,
  and launch with CSRF and an idempotency key retained across retries for
  that revision. The operation response drives the shared WorkflowStepper.
- All 29 UI tests and the production build passed. Chrome verified review
  by keyboard and the accepted launch with fixture API responses. Template
  creation, repair and live controller validation remain open.

### Browser IDE entry

- Workspace detail returns an optional `canOpenIde` flag. The controller
  evaluates the same readiness and permission rules as grant issuance,
  and the browser refreshes the decision when the Stepper reaches IDE
  verification. Opening the IDE requests a fresh grant and navigates to
  the separate service origin.
- Go web and contract race tests, UI tests and the production build
  passed. Chrome verified keyboard entry and the exchange redirect with
  fixture API responses. A live UTM-backed IDE session remains to verify.

### Workspace provenance and retention details

- Workspace details display the pinned template revision and platform
  release, last activity, and any scheduled retention deadline. The platform
  release comes from the immutable artifact referenced by the workspace.
- Go web and contract race tests, 31 UI tests and the production build
  passed. Rebuild remains open pending verification of retained data.

### Workspace failure after reload

- Workspace detail identifies the latest failed workflow in the current
  generation when no operation is active. The Stepper renders the saved
  failure and its detail after a reload. A newer workflow supersedes it.
- Go contract and web race tests, 31 UI tests, and the production build
  passed. Recovery actions and operation history remain open.

### Infrastructure status API

- T099's workspace and administrator handlers are registered in the active
  management router. The rebuilt native controller opened HTTPS and served
  the management API with the complete route catalog. The full `internal/web`,
  `internal/app`, and `tests/contract` race suites passed. The live operations
  journey remains tracked by T103.
- The administrator status route reports provider identity and network modes,
  the saved capacity budget and version, and certificate validity warnings.
  The 30-day warning is computed from the active certificate's stored dates.
- Web, application, and contract race tests passed. Infrastructure UI,
  broader health conditions, and full controller startup remain open.

### Infrastructure page

- Added the administrator's Infrastructure page with provider details,
  active certificate warnings, and a versioned capacity budget form. A
  conflicting edit reloads the current budget for review without retrying.
- All 33 UI tests and the production build passed. Chrome verified
  keyboard navigation and a successful budget update using fixture
  responses. Certificate replacement and broader infrastructure controls
  remain open under T099 and T101.

### Disposable platform boot validation

- Catalog validation stages a checksum-verified copy for the boot checker.
  Writes during the check affect only the disposable copy. The catalog is
  checked again before publication and a failed check leaves it unavailable.
- The release race suite passed, including a boot-check fixture that modifies
  the staged disk. The import route and live VM boot verification remain open.

### Live enrollment recheck and controller shutdown

- The current controller imported and booted a disposable copy of the local
  Ubuntu toolchain export on UTM 5.0.8. The guest first authenticated after
  2m25s. The race-enabled enrollment test passed and verified VM cleanup.
  This export still uses fixture IDE metadata.
- Added shared shutdown for the browser listener and workspace runtime when
  either service fails or Dex exits. Application, CLI, and contract race
  suites passed. The `serve` command still needs to invoke the supervisor.

### Active management routing and membership list

- The active router requires all normal management handlers and keeps
  bootstrap APIs on the local setup listener. Missing normal handlers
  still prevent active router construction.
- Added the administrator membership list through the reviewed route.
  Contract tests cover pending and disabled accounts and live role changes.
  Web, application, CLI, and contract race suites passed. Account mutations
  and `serve` startup composition remain open.

### People membership view

- The People page displays current memberships, roles, and local or external
  sign-in modes. A failed read can be retried, and direct visits require the
  administrator's current list permission.
- All 36 UI tests and the production build passed. Chrome exercised mobile
  keyboard navigation and retry with fixture responses. Approval and
  offboarding actions remain open under T077 and T101.

### Pending member approval

- Administrators can approve a pending membership from People after
  selecting Member or Administrator. Approval checks the current version
  and records the change and its audit event in one transaction.
- Contract and UI tests cover explicit role selection, authorization,
  CSRF, and stale requests. Web, app and contract race suites and all
  37 UI tests passed. Offboarding and role-only changes remain open.

### Active membership role changes

- People now supports versioned role changes for active accounts. The
  controller serializes membership updates and prevents demoting the
  last active administrator with a completed password setup. Demotion
  closes tracked connections after the role and audit event commit.
- Contract tests cover a member's denial, rejected self-demotion, and
  post-commit closure. Web, app and contract race suites, all 38 UI tests,
  and the production build passed. Offboarding remains open.

### Offboarding stop execution

- Added transactional revocation for all sessions of one user. It preserves
  earlier revocations and rolls back if the surrounding transaction fails.
- The workspace worker can reconcile a committed offboarding stop directly
  through the provider, even when the member cannot authenticate a guest
  poll. Race tests verify recovery from a lost stop response and retention
  of the disk reservation. Membership admission must still atomically queue
  these operations and close the affected connections.

### Membership offboarding admission

- Changing membership to disabled or pending revokes sessions, invalidates
  outstanding enrollment grants, and queues provider stop operations in
  the membership transaction. Tracked user and owned-workspace connections
  close before a successful response. Conflicting workflows are cancelled.
- Contract and race tests cover commit-before-close and reconciliation of a
  stop when the VM was imported before its instance was recorded. The UI,
  native provider offboarding, and reconciliation when no VM is found remain
  open. Unverified workspace outcomes retain their reservations.

### People offboarding controls

- People previews owned workspaces before disabling an account and explains
  that sessions close and workspace files are preserved. After confirmation,
  the dialog shows the controller's stop operations and their Stepper state.
- The UI test covers a failed provider stop. All 39 UI tests, the production
  build, and Go web, workspace, app, and contract race tests passed. Per-session
  counts, live UTM offboarding, and post-dialog recovery remain open.

### Offboarding stop visibility after close

- Disabled accounts link to their workspace list. The owner filter stays
  in the URL and across pages, so administrators can return to pending
  and failed stop workflows after closing the offboarding dialog.
- Unconfirmed provider stops persist recovery guidance in the Stepper.
  Missing or mismatched VM inventory does not release reservations.
  All 40 UI tests, production build, and Go race tests passed.

### Native UTM offboarding verification

- The UTM 5.0.8 release harness now disables the disposable workspace owner
  through the membership handler after authenticating the Ubuntu guest. The
  normal worker ran the queued stop and UTM confirmed the same VM and owned
  disk remained. Compute capacity was released and the disk stayed reserved.
- The race-enabled native run passed in 248 seconds and removed the test VM.
  The full Go race suite passed afterward. Browser IDE, file-level retention,
  and simultaneous live-connection coverage remain separate release checks.

### Administrator audit history API

- The management audit route returns redacted events with exact action and
  resource filters. It returns at most 50 events at a time and uses the
  response Link header to carry the append-order cursor.
- Audit, web, app and contract race tests passed. The browser audit view and
  configurable retention remain open under T071 and T077.

### Administrator audit history page

- Added the Audit page with action and resource filters and server-driven
  pagination. The page checks current administrator permission before
  requesting history and displays the recorded actor, resource and outcome.
- All 42 UI tests and the production build passed. Chrome verified mobile
  navigation, filtering and pagination by keyboard using fixture responses.
  Configurable audit retention remains open under T071.

### Audit retention policy and pruning

- Controller configuration accepts `auditRetentionDays`, defaulting to 90.
  The pruning service deletes history strictly before the cutoff during an
  active installation and preserves membership, grants, unresolved operations
  and workspace provenance. The cutoff comparison supports existing rows
  with variable RFC3339 fractional-second precision.
- Audit, configuration, app, CLI and contract race tests passed. Scheduling
  pruning in the active controller and dismissed-notice retention remain open.

### Systemwide notice publication and receipts

- Added the notice and receipt schema and wired the reviewed management
  routes for publication, listing, first read and dismissal. Receipts are
  scoped to the signed-in user. Notices observe scheduled start and expiry.
- Web, application, database and contract race tests passed. Persistent
  operational conditions, notice retention, and the browser view remain open.

### Notices page

- Members can mark notices read, dismiss them, and browse their own history.
  Administrators can publish notices immediately or provide start and expiry
  times. The page follows server pagination.
- All 44 UI tests and the production build passed. Chrome verified the
  member keyboard flow using fixture responses. Operational conditions and
  automatic history retention remain open.

### Certificate warnings in Notices

- Notices uses the same authorized infrastructure status as the
  Infrastructure page. Active certificate warnings appear as persistent
  alerts for administrators with infrastructure access.
- All 45 UI tests and the production build passed. Chrome verified that
  the warning survives keyboard dismissal of a personal notice and clears
  after a refreshed status shows replacement. Other operational conditions
  and active-listener composition remain open under T096 and T101.

### Dismissed-notice retention

- `dismissedNoticeRetentionDays` defaults to 30. Pruning removes dismissal
  receipts strictly older than the cutoff once their notices expire.
  Unexpired notices retain their receipts so they stay dismissed for the
  original user. Read-only history and recovery state are preserved.
- Go vet and race-enabled configuration, application, and contract tests
  passed. The pruning test used SQLite. Scheduling both history pruners
  in the active controller remains open under T098.

### Controller history cleanup

- The controller supervisor now owns the cleanup service alongside HTTPS
  and reconciliation. It runs audit and dismissed-notice cleanup on startup
  and daily. A failed run is reported and retried on the next schedule.
- Retention and shutdown tests passed, with dismissal pruning verified on
  SQLite and the disposable PostgreSQL fixture. Go vet and the full Go race
  suite passed using a fresh PostgreSQL database and serial package execution.
  The `shiftbox serve` entry point still needs to start the supervisor under
  T060. Broader operational conditions remain open under T096 and T098.

### Active browser certificate replacement

- Wired the administrator replacement operation into the management
  composition. Certificate validation, encrypted storage, configuration
  generation and idempotent operation evidence commit in one transaction.
- New HTTPS connections use the latest committed replacement. Failed
  replacements preserve the prior certificate. Expiry refuses new TLS
  connections, with no plaintext or temporary certificate fallback.
- Go vet and race-enabled application, configuration, web, command and
  contract tests passed. A loopback TLS test verified replacement and
  expiry. The Infrastructure upload form and production CLI composition
  remain open under T101 and T060.

### Infrastructure certificate upload

- Administrators with infrastructure management permission can upload the
  replacement chain and private key. The form preserves the request key
  when retrying the same files, clears selected files after acceptance, and
  displays the committed operation in the existing Mantine Stepper.
- All 47 UI tests and the production build passed. Chrome verified keyboard
  validation, retry, file clearing and warning refresh against fixture
  responses. Production startup remains open under T060.

### Active local password changes

- Wired the active password-change route to Dex. The controller checks
  the current password and rechecks session authority, then revokes existing
  sessions and closes their connections before replacing the credential.
  A successful change clears the first-login requirement and requires a
  fresh sign-in. The audit event records only account identifiers.
- Go vet and race-enabled command, application, authorization, web and
  contract tests passed. The pinned Dex test verified changing a password
  from a restricted session and signing in with the replacement. The active
  browser form and administrator-created accounts remain open under T070.

### Browser password changes

- The active sign-in landing page shows the shared Mantine password form
  when a change is required. Active local users can also change their
  password from Account. Success clears the browser's session view and
  offers fresh sign-in.
- All 50 UI tests and the production build passed. Chrome verified first-login
  keyboard validation and the sign-in prompt using fixture responses. Account
  creation and production startup remain open.

### Administrator-created local accounts

- The active management API creates local accounts with an explicit role
  and a required first-login password change. It reserves the identity and
  an encrypted temporary credential before contacting Dex. A retry uses
  that reservation, and the membership and audit transaction consumes it
  before returning the password once.
- Go vet and relevant race tests passed. The creation fixture verified
  interrupted Dex writes and one-time delivery. Migration parity passed
  on SQLite and PostgreSQL. The People form and credential recovery remain
  open under T070 and T077.

### Local account creation in People

- Authorized administrators can enter an email address and explicitly
  choose Member or Administrator. People refreshes after the account is
  created and clears the one-time password when the administrator confirms
  they saved it for private delivery.
- All 51 UI tests and the production build passed. Chrome verified role
  selection and one-time password clearing by keyboard using fixture
  responses. Credential recovery and production startup remain open.

### Protected setup temporary-password reset

- `shiftbox setup --reset-temporary-password` uses the existing private
  installation directory and exclusive setup lock. It resets only a
  delivered bootstrap password that still requires its first change, revokes
  prior sessions, and writes the replacement only to standard output.
- Go vet and the relevant race tests passed. Native Dex rejected the old
  password and accepted the replacement. Ordinary setup resumed without
  redisplaying it, and explicit reset refused a completed password change.
  Recovery of an active installation's administrator remains open.

### Active local administrator recovery

- `shiftbox recover-admin --email <existing-email> [--config <path>]`
  defaults to `config.json` in the current directory. It requires an
  exclusive installation lock, the protected key and the active database.
  Only an existing active local administrator with a matching Dex subject
  receives a replacement. Prior sessions and browser origin grants are
  revoked, the next login requires a password change, and recovery is audited.
- Go vet and the relevant race tests passed. Native Dex verified the lock,
  refusal of the original password, and acceptance of the replacement on
  SQLite. PostgreSQL recovery and signed-in administrator resets remain open.

### PostgreSQL administrator recovery verification

- Ran the native recovery command test against SQLite and PostgreSQL.
  PostgreSQL used separate application and Dex schema owners with verified
  TLS and the encrypted Dex connection record. The test confirmed that
  Dex rejects the old credential and accepts the replacement, with no
  local Dex database created. The disposable database and Dex role were
  removed after the run.

### Network policy creation and launch

- The administrator API stores named unrestricted policies when the
  pinned provider advertises shared networking. Invalid allowlist rules
  are rejected, and UTM configurations that require unverified restrictions
  cannot be created through the API.
- Template revisions can reference an enabled policy. Launch pins the
  selected policy ID and effective network mode after checking current
  capabilities. Existing templates without a policy retain shared networking.
- Go vet, race-enabled application, template, web, workspace and contract
  tests, and SQLite/PostgreSQL migration parity passed. Stopped-workspace
  policy edits and browser selection remain open.

### Workspace policy edits

- The versioned workspace API saves network policies only when desired
  and observed state are stopped and no operation is active. The runtime
  reads the current policy at next start while retaining launch provenance.
- The API also saves or clears workspace retention settings within the
  configured limit. Automatic retention enforcement and browser controls
  remain open under T093 and T101.
- Go vet, relevant race tests and the focused start-policy test passed.

### Workspace activity observations

- The guest samples aggregate Linux CPU counters across a two-minute
  window. Authenticated poll exchanges use controller receipt time and
  accept CPU activity above 5% when its sample is at most 90 seconds old.
  Open authorized browser IDE requests also count on poll. Guest-reported
  IDE counts do not affect retention.
- Tests cover stale, idle and wrong-generation reports, monotonic activity,
  the CPU sampling window, and browser connection presence. Go vet and the
  relevant race tests passed. Monitor configuration, baseline deadlines,
  outage grace and automatic deletion remain open under T093.

### Baseline retention deadlines

- Launch records a deadline when an allowed retention interval is selected.
  Policy edits derive the deadline from the latest recorded activity, or
  creation time when the workspace has never reported activity. Clearing
  retention clears the deadline. A concurrent activity write fences an edit
  based on an older baseline.
- Go vet and focused race tests passed, including launch deadlines and
  migration parity on SQLite and PostgreSQL. Scheduling, reconciliation
  grace and automatic deletion remain open under T093.

### Retention eligibility after reconciliation

- Retention discovery requires an administrator limit and elapsed
  post-reconciliation grace. It derives overdue dates from controller-owned
  activity or creation time, checks the exact provider identity, and
  rechecks workspace state, membership, and activity after inspection.
- Tests cover disabled retention, grace, uncertain inventory, renewed
  activity and a policy edit between discovery and the final recheck.
  Go vet and the relevant race tests passed. Runtime reconciliation
  timestamps, scheduling and deletion commitment remain open under T093.

### Durable retention deletion commitment

- The retention admission transaction rechecks the verified candidate,
  current activity baseline, policy, membership, state and grace. It
  persists deletion intent, audit evidence and the shutdown, stop and
  deletion steps together. It leaves observed provider state untouched.
- Tests cover cancellation by a policy change, duplicate commitment and
  rollback when the audit write fails. Go vet and the relevant race tests
  passed. Scheduling and the provider executor remain open under T093.

### Retention deletion execution

- The workspace worker executes committed retention deletions. During the
  graceful phase, an authenticated guest can receive and acknowledge its
  shutdown command. Only retention operations can force a provider stop
  after the default 60-second graceful window.
- Confirmed stop releases compute. Partial cleanup and lost deletion replies
  keep disk capacity reserved until the adapter confirms owned resources
  were removed. Go vet and race-enabled workspace, guest, application and
  contract tests passed. Scheduling and native UTM end-to-end validation
  remain open under T093.

### Runtime retention scheduling

- Workspace cleanup is disabled unless `workspaceRetentionMaximumSeconds`
  is configured. The workspace runtime checks provider availability and
  deletion capability each minute. Startup, failed provider probes and
  missed cycles renew the grace window, which defaults to ten minutes and
  can be configured with `retentionOutageGraceSeconds`.
- An open IDE request blocks commitment under the same lock used to admit
  new IDE requests. Go vet and race-enabled application, configuration,
  proxy, workspace and contract tests passed.
- Workspace details now show whether automatic deletion is paused or in
  outage grace, the original deadline, and what deletion removes.
  Native UTM automatic cleanup, configurable activity monitoring, and
  production startup wiring remain open under T093.
- T093 is complete. The active runtime schedules retention with outage grace,
  and the worker tests verify graceful shutdown, retention-authorized
  escalation, partial cleanup, lost replies, and capacity release only after
  provider confirmation. T099 now admits confirmed explicit deletion and
  T101 shows typed confirmation with Cancel focused first and server-driven
  cleanup progress. Explicit deletion uses a graceful provider stop.

### Signed-in administrator password reset

- T127 implements the approved `user.reset_password` permission and
  `POST /api/v1/admin/users/{userId}/password-reset` contract. Reset revokes
  access before replacing the Dex credential and preserves a usable
  administrator. Version checks prevent a repeated request from issuing a
  second password.
- People shows the temporary password in the reset dialog until the
  administrator confirms it was saved. Race-enabled controller, auth, web
  and contract tests passed. The native Dex test confirmed denial of the
  old credential and a restricted password-change session with the
  replacement. UI tests, the static build and a mobile Chrome smoke test
  passed.

### Controller startup composition

- T060 composes the native UTM adapter, pinned Dex, approved management
  router, embedded UI, browser HTTPS, guest listeners, workspace worker,
  and history cleanup under the controller supervisor. Startup takes
  the installation lock before loading active state. Management and
  guests share one connection registry and, when configured, one GitHub
  App. An absent App leaves the guarded Git routes available to report
  their configuration requirement.
- Runtime bind addresses, the guest-reachable host, and UTM resource
  bounds are explicit. Startup reads the active and minimum guest releases
  from the authoritative database. The retained-workspace upgrade gate
  and backup utility checks still need their T097 implementation.
- Formatting, Go vet, the embedded build, and the full race suite run
  one package at a time with pinned Dex passed. The parallel race run
  timed out waiting for the second probe in an existing Dex test.
- T060 stays open. The real management router still rejects startup
  until the required handlers are wired. The next missing handler is
  external reauthorization, followed by other catalog operations for
  external identity, platform import, maintenance, migration, backup,
  and recovery. Live production startup and acceptance remain unverified.

### External membership and reauthorization

- T070 and T076 have the external identity admission and callback
  implementation. The browser attempt binds the selected Dex connector,
  nonce, PKCE verifier, expiry, and original ShiftBox session. The signed
  Dex authentication time must fall within that attempt.
- First successful external sign-in creates a separate pending membership
  from the stable issuer, subject, and connector. Reauthorization checks
  the same account, replaces its session, and revokes browser grants and
  established connections for that session.
- T062 covers unapproved and disabled membership, equal-email isolation,
  stale broker authentication, expired and forged attempts, CSRF, and
  renewal replay.
- The controller wires external connector management and reauthorization at
  startup. Connector activation commits the versioned application settings
  after the private Dex write. It retires the prior connector and revokes
  external sessions and browser grants. Disabling requires a usable local
  administrator. The activated database controls the authorization interval.
- The pinned Dex HTTPS test counted three upstream code exchanges for
  pending sign-in, approved sign-in, and renewal. Membership disable and
  password change tests cover browser grant revocation. The full serialized
  Go race suite, Go vet, and embedded build passed. Migration and audit
  checks passed on SQLite and a fresh disposable PostgreSQL database.
- T062, T070, and T076 are checked off. T077 still needs the external
  identity controls and T060 still needs the remaining management routes.

### Organizational sign-in and GitHub connection screens

- T077 adds organizational sign-in alongside local sign-in. The shared
  shell shows the upstream authorization deadline and gates the visible
  workspace and management content when it expires. The renewal stepper
  hands off to the controller's short-lived Dex URL.
- Account shows GitHub connection status and supports connect, reconnect,
  and confirmed disconnect. Authorization links must match the expected
  destination and expire in the UI at the server-provided time.
- UI tests cover an open page reaching its deadline, failed renewal,
  invalid destination, and confirmed GitHub disconnection. Chrome
  verified both authorization handoffs and the disconnect confirmation
  against local fixture responses. T077 remains open for template and
  secret controls.

### Template revisions, launch grants, and archive controls

- T077 adds template creation, immutable revisions, launch-grant
  acknowledgement and revocation, archival, and the administrator-only
  archive filter and restore action. The revision form uses the current
  template fields and the version the user reviewed. Creating a template
  uses resource and artifact IDs, and sharing uses a user ID.
- Template list responses include current grant membership only for
  owners and administrators. The UI keeps cached lists scoped to the
  signed-in user, role, and archive filter.
- Race-enabled template and contract tests, UI tests, and the embedded
  build passed. Chrome verified keyboard sharing through the warning and
  confirmation with local API fixtures. T067 and T077 remain open for the
  secret controls and tests.

### Secret management and restricted sign-out

- T067 and T077 are complete. Account, Templates, and Infrastructure
  expose the Personal, Project template, and Organization secret scopes
  using current user, template, and session authority. Only metadata is
  retained in the query cache. Writes start with a blank field, explicit
  empty is a separate choice, and deletion requires fallback confirmation.
- A personal fallback varies by workspace and current project access.
  The confirmation names those project-dependent results and any known
  Organization fallback. Project and Organization confirmations describe
  the resulting precedence for their scopes.
- Password-change and other authorized sessions can sign out through the
  shared shell. A successful sign-out clears cached management results.
- All 68 UI tests, Go vet, the embedded build, and race-enabled identity
  and API tests passed. Chrome verified keyboard-only empty override and
  fallback deletion with local fixtures.

### Prepared UTM artifact manifests

- T046 has a release build tool that generates an artifact manifest from
  the prepared UTM export and the build's component metadata. It hashes
  the exported configuration, firmware data and persistent disk. It
  refuses unexpected files and symlinks in the bundle.
- The generated manifest passed catalog verification in the release
  tests. The command also hashed the existing prepared Ubuntu bundle
  using fixture component metadata. That bundle has no approved browser
  IDE or release boot evidence.
- T033, T046 and T058 stay open for the licensed browser IDE selection,
  final image and native boot evidence, and setup import wiring.

### Browser IDE packaging

- T046 selects Coder code-server 4.145.2 for the VS Code Browser
  experience. Packer pins the published Linux amd64 archive and SHA-256,
  keeps its MIT license and third-party notices, and checks the installed
  version. Builds record the IDE archive and guest digests in the manifest
  alongside the final UTM export.
- The release tests use that exact manifest for the disposable UTM clone.
  Packer 1.14.2 and UTM 5.0.8 built the Ubuntu 24.04 Minimal amd64 image
  and verified code-server 4.145.2 on the guest. The race-enabled clone
  test authenticated in 45s and passed in 62.06s, including offboarding,
  retained disk verification and disposable VM cleanup. The development
  manifest SHA-256 is `56a64ae23b7926e255770229dab751057a057a019f02499869cc9d3c836febbe`.
- The catalog publication and default-selection test passed on SQLite
  and PostgreSQL. T046 stays open for production admission through setup.
  The approved MVP source is a local import folder containing the UTM
  bundle and its `manifest.json`.
- T033 is complete. Exact Packer pin and systemd supervision assertions
  and UTM capability tests passed with the race detector. Native UTM 5.0.8
  inventory and recovery-media tests verified preserved drive lists,
  unchanged source digests, separate VM and disk IDs, and clone cleanup.
- Workspace launch now rejects an image whose agent is outside the
  controller's supported release window, even if its protocol matches.
  The launch contract test verifies refusal and acceptance after the same
  image is declared compatible as the previous release.
  T052 stays open for starting the IDE inside the project Dev Container
  and verifying it through the ShiftBox proxy.

### Retained guest release evidence

- T097 records the release and protocol from the last accepted poll in
  the authenticated workspace generation. The upgrade check lists retained
  workspaces with missing or different versions, including stopped guests.
  The enrollment test confirms that a previous release can still poll but
  blocks advancement, an unsupported report leaves the last version intact,
  and a current-release poll clears that version blocker.
- T095 and T097 also keep migration blocked when a current-version guest
  still has unresolved maintenance. A completed update or verified removal
  clears that maintenance blocker.
- The native UTM test confirmed the stopped guest remained a blocker after
  rollback and cleared after an authenticated replacement succeeded.

### Local platform import and PostgreSQL cutover

- T046 reads `manifest.json` alongside the UTM bundle in a local folder.
  Reopening the catalog reuses the same release record. A changed manifest
  conflicts with that record, and workspace staging requires validation.
- The native UTM 5.0.8 enrollment test imported the built code-server image
  through the local folder path, authenticated in 4m05s, and passed in
  276.95s including offboarding, disk retention and disposable VM cleanup.
- PostgreSQL setup now transfers the exact validated default artifact record
  across profile activation. The digest-addressed files stay in place. A
  test against separately owned PostgreSQL schemas with verified TLS
  reproduced the missing record and passed after the fix.
- T046 and T058 remain open for connecting the setup screen to local import
  and the production boot-and-enrollment validator.

### Setup platform validation

- T046 and T058 are complete. The protected setup provider step validates
  the operator's local bundle and manifest, checks UTM capability and guest
  compatibility, and uses a disposable copy with separate enrollment trust
  and no project dispatch. The catalog is published and selected only after
  an authenticated poll and confirmed clone deletion.
- UTM 5.0.8 passed the native validation test in 148.75s with an 8 GiB
  configured disk minimum and the actual 32 GiB image. Regression tests
  covered a lost import reply, and Chrome exercised keyboard validation,
  progress, retry and activation acknowledgement with local fixtures.
- T052 remains open for starting code-server in the project Dev Container.
  The complete launch journey and release evidence remain T039 and T061.

### Browser IDE inside the Dev Container

- T052 starts packaged code-server after the declared `waitFor` gate, using
  the configured remote user and workspace directory. The guest tunnel uses
  the dedicated Unix socket. ShiftBox owns browser authorization, and the
  workbench's own preview proxy is disabled.
- The workbench probe checks the editor document after at most one relative
  project-folder redirect on the same authenticated tunnel. Tests cover
  unrelated destinations, startup failure, and the separate proxy gate.
- T052 records a successful code-server copy for the current container and
  packaged version. Regression tests cover restart reuse, replacement
  containers, interrupted copies, and separate copy and startup deadlines.
- Code-server 4.145.2 and Dev Container CLI 0.90.0 returned the actual VS Code
  workbench from a disposable amd64 Ubuntu Dev Container as `vscode`. The
  Go race suite passed serially. Packer rebuilt the Ubuntu 24.04 image
  with the modified service. The native UTM 5.0.8 catalog check enrolled
  the clone and confirmed removal in 49.91s. The development manifest SHA-256
  is `b6c2f15712a9b33c6fadd00ed6a8a339ae29e390f93373029675d9217d977818`.
- T052 still needs the full UTM workspace
  journey, project and home retention on rebuild, and repair entry and exit.
- T120 reopens the controller database at all five workspace launch
  steps with a simulated provider and guest receipts. Lost create and boot
  replies result in one VM and one enrollment delivery. The worker waits
  for authenticated guest contact, the connection-ready receipt, a successful
  IDE probe, and the separate final hook receipt.
- T120 also restarts the workspace worker with an outstanding lease after
  a rebuild reports a required setup failure, at both repair steps, and
  at all three exit-and-retry steps. After the lease expires, it consumes
  the persisted receipts and verifies that the VM identity and owned disks
  remain the same. The provider and guest evidence are simulated.
  The matrix now also restarts at both ordinary stop steps, all four start
  steps, and all three successful rebuild steps. Lost stop and start replies
  reconcile without duplicate calls, and reservations follow the verified
  power state. The expanded lifecycle and deletion tests passed with `-race`.
  The matrix also reconciles an offboarding stop after a lost provider
  reply and a controller restart, preserving the disk reservation. The full
  restart matrix, setup and platform restart tests, long-stop recovery,
  and live PostgreSQL setup restart passed with `-race`.
- T061 verified retained marker contents after native stop/start and rebuild,
  then passed the focused Chrome repair and retry journey on the same VM.
  The Go workspace journal check passed with the race detector in resumed
  mode. The November 14 run then passed the complete 24.1-minute Chrome
  workspace journey on `mvp-rc1` after the protected setup handoff. The
  strict Go journal passed with the race detector against the same SQLite
  installation. The pinned versions and digests are in the release manifest.
- T059's template form uses named profile and image choices. The production
  export and all 87 UI unit tests passed. The native Chrome runs exercised
  workspace launch progress, detail, stop/start, rebuild, and repair with
  server-derived workflow steps. T061 tracks the combined candidate run.

### Writable project checkout

- T052 now resolves the selected developer's UID and GID in the project
  container and assigns checkout ownership before the remote lifecycle
  hooks run. The ownership receipt stays in private guest state so a
  restart with the same identity preserves file ownership changes.
- Tests cover ordering, ownership failure, retry, and symlink handling.
  The pinned Dev Container CLI ran the required write hook as `vscode` on
  a disposable Linux volume and ran the later hook in a separate pass.

### Retained developer home

- T052 discovers the developer's home from the container account, seeds
  it from the image before remote hooks, and binds the retained directory
  from the VM disk. The guest checks the mount, home path, and owner before
  running hooks. A different home mount or developer identity stops setup.
- Tests cover discovery of a custom home, copying image files once, reuse
  on restart, and mismatched mounts and owners. A disposable Docker test
  with the pinned CLI preserved a home file through container replacement.
- T052 remains open for managed rebuild and repair, retained application
  mounts, and the full UTM browser journey.

### Guest container rebuild

- T052 accepts the fixed rebuild command and verifies the checkout,
  developer home, and declared named Docker volumes against the managed
  container before replacement. The guest verifies the new container ID
  and the same retained mounts before running required remote hooks or
  reporting readiness. The durable journal prevents a replay or agent
  restart from repeating an uncertain replacement.
- The pinned Dev Container CLI preserved files in the checkout, home, and
  a named application volume when replacing a stopped Docker container.
  The old writable-layer file was absent from the replacement. Automated
  guest tests cover ambiguous inventory, missing mounts, reused IDs, and
  operation replay.
- T055 admits rebuild on a running workspace with the existing VM and
  reservation. It dispatches through authenticated guest polling and gates
  IDE entry on the new configuration and proxy verification. The workspace
  page displays the durable rebuild steps in its existing Stepper.
- Go vet and the serial race suite passed. UI type checking, 70 tests, and
  the production build passed. Chrome verified keyboard rebuild, live
  progress, and IDE availability after completion using a local fixture.
- T052 and T055 stay open for repair using the approved preloaded Ubuntu
  container, separate repair home, and packaged code-server. The full
  UTM browser journey remains under T039 and T061.

### Preloaded repair image

- T052's Packer recipe now builds a local repair image from a digest-pinned
  Ubuntu 24.04 amd64 base and the verified code-server distribution.
  The release recipe test passed, and Docker verified code-server 4.145.2
  in the built image with image pulls and networking disabled. Guest
  repair commands and a UTM artifact containing the image remain pending.

### Guest repair entry and exit

- T052 handles the fixed `enter-repair` and `exit-repair` commands. It
  reads the immutable repair image ID recorded by Packer, quiesces the
  normal container, and starts code-server offline with a separate home
  and socket. It mounts the retained checkout and leaves the normal
  container and retained data on disk after repair exit.
- Guest tests cover initial entry, a lost container creation response,
  duplicate dispatches, and scoped deletion. A disposable amd64 Docker
  test verified repair HTTP health and an edited file after exit.
- T052 and T055 remain open for controller dispatch and access, repair
  workflow validation, and native UTM acceptance.

### Controller repair entry

- T055 admits repair after a failed development setup step and recent
  authenticated guest contact. The existing VM and reservation stay in
  place. The controller dispatches the fixed guest entry command and
  probes the repair workbench through its dedicated tunnel service.
- Repair browser access requires its own verified step, origin, and
  scoped entry grant. For an IDE suffix of `ide.example.com`, the
  browser certificate must also cover `*.repair.example.com`. The
  management UI displays the repair workflow and opens the verified IDE.
- The approved combined exit and retry action still needs controller
  dispatch, setup reconciliation, and UI wiring. Native UTM and browser
  repair acceptance remain open.

### Exit repair and retry setup

- T055 exposes the combined action when repair is active. Accepting it
  immediately closes admission to the repair origin. The guest stops
  the repair container before retrying the captured Dev Container
  configuration, preserving the checkout and retained mounts. The
  controller requires fresh development IDE verification before entry.
- Guest, authenticated polling, API, worker, and UI tests cover the
  combined action. A release artifact and full UTM browser journey
  remain to be exercised under T039 and T061.
- T055 passed the workspace, guest, API and restart tests under the race
  detector. The retained UTM fixture verified native stop/start, rebuild,
  failed setup, repair IDE readiness and authenticated retry on the same
  workspace generation. The frozen-candidate journey remains T061 work.

### Guest executor coverage

- T052's native retained-VM run verified repository-backed IDE readiness,
  graceful stop, rebuild, repair entry and exit, and preservation of the
  checkout marker. The pinned Debian and Ubuntu Dev Container fixtures
  also passed under the race detector, including the failed required hook,
  separate later hook, and named-volume retention checks. The complete
  frozen-candidate browser journey remains under T061.
- T037 adds executable tests with the pinned Dev Container CLI for a
  prebuilt image, a Dockerfile, a local Feature, and required hook failure.
  Tests reopen the operation journal to verify the `waitFor` milestone
  precedes the later hook. The fixtures passed against the Debian and
  amd64 Ubuntu images under the Go race detector.

- T038 verifies the repair explanation, the server-driven failure step,
  and the named data retained across rebuild. Both repair controls expose
  the explanation to assistive technology. UI type checking and 72 tests
  passed.

### Guest client certificate renewal

- T049 adds authenticated renewal at four hours. The controller validates a
  fresh CSR, issues an eight-hour replacement capped by the installation CA,
  and commits the new serial before responding. A guest can recover the same
  replacement with the same key while its predecessor is still valid.
- The guest replaces its stored key and certificate together and reopens its
  authenticated connections. Race-enabled guest and controller tests covered
  the lost-response retry and the first poll with the replacement. SQLite
  and PostgreSQL migration parity passed. Native UTM lifecycle acceptance
  remains under T061 and the recovery tasks.

- T050 passed the T034 contract over real mTLS. It verifies accepted current
  and previous releases, stable dispatch replay, stale generation and version
  denial, independent IDE verification, and the 30-second cadence advertised
  by the controller. Invalid and oversized evidence leave the dispatch able
  to accept its subsequent valid report. The agent polls with 27–33 second
  jitter.

- T053 passed the race-enabled tunnel and proxy tests twice. They exercise
  mutually authenticated Yamux registration, scoped HTTP streams, a browser
  WebSocket upgrade, and closure on revocation and expiry. Both sides use
  the pinned Yamux keepalive configuration.

### Setup and guest credential contracts

- T030 passed the setup and CLI contracts under the race detector with the
  pinned Dex binary. Tests cover the protected listener, temporary password,
  step resumption, validation, activation, retirement of setup sessions, fresh
  sign-in, safe command output, and startup refusal before activation or when
  the active database is missing.
- T043 uses the protected setup listener and durable setup journal exercised
  by T030. Native Chrome previously completed the same SQLite activation and
  signed in afresh against the active controller. The exact-stack workspace
  browser run still needs to continue from GitHub consent under T061.
- T031 passed against SQLite and a fresh local PostgreSQL fixture over TLS.
  It covers setup restart, swapped schema owners, the exact bootstrap data
  allowed through activation, and refusal to reopen the retired SQLite
  bootstrap after PostgreSQL cutover.
- T044 also passed the native setup command test against separately scoped
  PostgreSQL logins over verified TLS. Pinned Dex imported the changed
  administrator password, rejected the temporary password, and wrote only
  to its dedicated schema without creating a SQLite fallback.
- T065 passed the race-enabled guest, management, identity, and command
  contracts. The mTLS test distinguishes workspace owner secrets from
  template author secrets and verifies fallback on revocation. An archived
  template keeps project-secret access for existing grants. GitHub contract
  tests check PKCE, the exact callback address, and rejection of revoked
  state, with refresh and owner token checks using local provider fixtures.
- T074 and T075 passed the owner-bound identity, guest TLS, socket, and
  command race tests. Coverage includes encrypted token persistence,
  repository restrictions, concurrent refresh, reconnect-required state,
  token delivery after an authority recheck, and secret command exits.
  The Linux amd64 guest command built successfully. The single-owner
  GitHub consent and private clone remain part of T061 acceptance.

### Workspace schema and stores

- T040 and T042 passed migration, catalog, configuration, and template
  revision checks across SQLite and PostgreSQL. Both profiles passed
  capacity contention and provider-evidence release tests. The lost-create
  test verifies provider instance and resource records are persisted only
  after confirmed inventory. Guest enrollment and exact-origin grant tests
  verify their durable bindings and one-use exchanges.

- T056 passed the race-enabled management route and authorization matrix
  comparisons and the setup, template, workspace, event replay, and entry
  grant handler contracts. T057 still owns manually configured preview ports.

### Backup checkpoint continuity

- T104 verifies that an established guest workload can continue responding
  over mutual TLS and Yamux while new guest polls wait on the checkpoint
  fence. The race-enabled guest, controller, and backup tests passed with
  the age archive, checkpoint failure, and release-before-assembly cases.
  Native workload continuity during a backup remains part of T116.
- T110 passed the SQLite and PostgreSQL checkpoint and archive suites.
  PostgreSQL verified both scoped dumps share one cutoff and release the
  write fence before export. The verified age archive contains only the
  management data and protected files on its allowlist.

### Operations regression tests

- T080 verifies supported policy admission and rejects malformed or
  unsupported rules, including restricted policies with recorded exceptions.
  A journal and worker test confirms a mismatch prevents the provider from
  starting the stopped VM. T091 remains open for verification on policy edit.
- T087 covers capacity, maintenance, offboarding, retention, conditions,
  and keyboard interaction. The delete dialog now restores focus to its
  trigger after Escape and requires fresh confirmation each time it opens.
  TypeScript and 32 workspace, admin, and notices tests passed. T119 remains
  open for real browser keyboard and narrow viewport evidence.

### Manually configured private previews

- T057 adds versioned workspace preview ports. The guest advertises the
  controller-configured ports through its authenticated tunnel, and each
  port has a separate origin. Removing a port revokes existing grants and
  cancels active preview traffic. Adding it again requires a new grant.
- Contract tests passed for owner and audited administrator access, exact
  origin, cookie revocation, and wrong-port target rejection. A real local
  mTLS/Yamux tunnel forwarded the registered preview and stripped ShiftBox
  credentials. SQLite and PostgreSQL migrations, TypeScript, and 84 UI
  tests passed. Native UTM preview and browser isolation remain part of the
  release acceptance tests.

### Stopped-workspace network verification

- T091 verifies the exact stopped instance, generation, ownership, artifact,
  and effective network mode through the UTM provider before committing a
  network policy edit. A failed or mismatched observation leaves the
  stopped workspace and its settings intact. The only verified UTM mode
  remains shared outbound access, with no exception rules required.
- Race-enabled policy contracts and the native UTM 5.0.8 stopped fixture
  passed. The native fixture was removed after verification.

### Capacity and preflight regression tests

- T079 checks each capacity refusal without queuing or displacing the active VM.
  The first workload and its reservation survive lost replies, conflicting
  inventory, and controller reconstruction. Verified complete deletion
  frees capacity. The race-enabled integration run passed twice.
- T085 checks the actionable backup, key, certificate, provider, and tool
  prerequisites, exit statuses, N/N−1 handling, and refusal to fall back
  to plaintext after browser certificate expiry. The focused race suite
  passed. The native signed update run and check-only preflight supplied
  the controlled-host evidence for T097.

### Notice retention and workspace deletion confirmation

- T086 passed the admin contracts and the SQLite/PostgreSQL retention
  checks. Dismissal, expiry, restart, and pruning preserve current notices,
  certificate warnings, maintenance, live grants, and workspace provenance.
  T096 still owns the remaining operational conditions.
- T100 adds an entered workspace name to launch and uses it for exact
  deletion confirmation. Older unnamed workspaces display their ID.
  Cancel receives focus first and Escape restores focus. The modal
  explains the retained data loss and management backup exclusion, and
  requires acknowledging that needed work has been pushed.
- Workspace API, migration parity, TypeScript and all 84 UI tests passed.

### Workspace inventory conflict reporting

- T090 exposes missing or mismatched VM ownership and partial cleanup in
  operation steps. It keeps the reservation while provider evidence
  remains incomplete. The race-enabled worker, capacity and restart
  integration, and deletion API tests passed.

### Persistent infrastructure conditions and recovery CLI status

- T096 persists provider, key-file, storage, and unexpected controller
  failure conditions. The administrator views combine them with the
  browser certificate warnings. Admission measures the UTM volume before
  allocating a new disk, while stopping and inspecting existing workspaces
  remain available. SQLite and PostgreSQL migration checks and the
  fail-closed browser certificate tests passed.
- T098 passed notice publication, dismissal, history, active condition
  visibility, and protected history pruning tests. The history service
  starts with the controller, with SQLite and PostgreSQL receipt coverage.
- T102 adds stable recovery exit statuses and versioned JSON status on
  stderr. The temporary password is delivered separately on stdout. The
  pinned Dex recovery test verifies password replacement and excludes the
  password from JSON. Migration preflight retains its stable result and
  exit contract.

### Security audit retention

- T071 passed the configured retention startup test. Pruning keeps events
  at the cutoff and preserves current authority, in-flight operations,
  scheduled maintenance, and workspace provenance. SQLite and PostgreSQL
  both passed the transactional write, rollback, and metadata checks.

### OpenAPI type generation

- T117 generates TypeScript declarations from OpenAPI with a pinned npm
  generator. The UI CI job checks for drift. The initial failing check,
  clean generation check, intentional drift and regeneration, full Go
  contract suite, and UI typecheck passed.

### Long-stop recovery

- T084 reproduced power-on without fresh enrollment for an expired leaf.
  T094 verifies the stopped VM's exact identity, delivers a one-use grant,
  and waits for authenticated contact. An interrupted start resumes without
  another grant or power request. The UTM adapter drive-inventory checks,
  recovery integration tests, and affected race suites passed. Live UTM
  acceptance remains pending under the release gates.
- T120 interrupts the controller after verified recovery media attachment
  and again after the provider boots the retained VM. Each restart opens a
  fresh database connection and enrollment service. The test verifies that
  the same grant is consumed, the seed is cleared, and no duplicate media
  attachment or boot is needed.

### Offboarding connection closure

- T081 added local TLS integration coverage for management SSE, IDE,
  preview, WebSocket, poll, and the guest tunnel, plus the durable stop.
  T092 now closes registered poll transports and waits for browser
  forwarding to finish before offboarding returns success.
- T092 adds administrator-only listing and revocation of existing workspace
  connection grants. Revocation commits both the permission and its derived
  browser grants with one audit event, then closes connections matching the
  exact grant. Closure failure supports retry after commit. Go contract tests
  and the administrator workspace UI tests passed.
- Explicit and retention-driven deletion retire browser grants and outstanding
  enrollment offers in the deletion transaction. Browser forwarding and
  guest tunnels close before cleanup. The guest poll closes after the graceful
  shutdown step, and verified provider deletion retires the registration
  alongside reservation release. Race-enabled contract, worker, and TLS
  integration suites passed.
- T120 restarts the deletion worker after the shutdown receipt, a lost stop
  reply, partial removal, and a lost delete reply. Verified stop releases
  CPU and memory. Verified removal releases disk and retires the guest
  registration. The restarted worker makes no duplicate provider call after
  an effect has been verified.
- The retention case also checks outage grace before admission and reopens
  the database at 59 seconds into workload shutdown. The worker waits until
  the original minute expires before requesting one forced stop.

### Sharing persistence and Git refresh fencing

- T069 uses the existing dual-profile template, secret, and audit tables.
  Migration 00024 adds the GitHub-only provider constraint and a durable
  refresh generation. An older refresh response cannot complete a newer
  claim after revoke and reconnect. SQLite and PostgreSQL passed the refresh,
  audit schema, template, secret API, and migration checks.

### Operations and activity persistence

- T089 uses the ordered network policy, maintenance, and notices migrations.
  Migration 00025 retains the latest valid receipt per selected IDE or CPU
  signal and guest generation. Idle observations leave the deadline intact.
  The controller configuration accepts retentionMonitorIDE and
  retentionMonitorCPU, both enabled by default. Both database profiles passed
  schema and activity checks. Maintenance admission, notice retention, and
  network-policy tests passed. Pinned UTM continues to reject restrictions
  without an enforceable provider exception set.

### Redaction release regressions

- T118 verifies API and audit responses, request logging, scoped service
  entry and forwarding, browser storage and URLs, the Dex child process,
  and a real encrypted checkpoint archive with synthetic values. All three
  race-enabled Go tests and the production-export Chrome check passed.

### Local release integration gates

- T121 adds an HTTPS membership mutation check that denies a sibling preview
  despite valid management credentials and leaves membership unchanged. Its
  management-origin control succeeds. Race-enabled SQLite/PostgreSQL parity,
  live mTLS revocation, pinned Dex/upstream OIDC exchange, scoped service
  entry, and corrupted encrypted archive checks passed. The exact UTM stack
  and full release gate matrix remain under T061 and T124.

### Notices UI review — Carbon application notifications

- [X] T101: Added one dismissible application notice across normal
  authenticated pages, with a link to the Notices inbox. Per-user
  receipts persist dismissal across navigation; the existing read receipt
  requirement is fulfilled before dismissal. Failed saves retain the message.
- [X] T101: Added Active/All notice views, severity and receipt labels, dates,
  refresh, cursor paging, empty/error states, and a focused administrator
  publishing dialog with scheduling guidance and expiry validation. Existing
  certificate and operational warnings remain non-dismissible.
- [X] T101: Verified 119 UI unit tests, TypeScript/production export, and Chrome
  journeys at 1280px and 390px covering keyboard focus, publishing, dismissal,
  reload persistence, history, and overflow. Rebuilt the embedded controller
  and updated the running local demo for review.

- [X] T101: Changed the application notice to Carbon's actionable toast pattern
  after review: fixed at the top right below the header, outside page flow,
  with a persistent View notices action and explicit dismissal. Verified 19
  targeted UI tests, production build, and desktop/phone Chrome journeys,
  including overlay positioning and viewport bounds. Updated the live demo.

### Audit UI review — Carbon data table guidance

- [X] T077: Added an Audit history card with a toolbar, refresh, clear cursor
  pagination, local timestamps, readable action labels, outcome badges, and
  permission-gated current user names. Expandable rows retain original event,
  actor, and resource identifiers, UTC time, action and reason codes without
  rendering arbitrary metadata.
- [X] T077: Added full-history action/resource filters with validation and
  event-detail shortcuts, plus explicitly scoped current-page search and
  outcome filtering. Empty, loading, error/retry, and unavailable-name states
  preserve read-only audit evidence and administrator authorization.
- [X] T077: Verified six Audit unit tests, TypeScript/production export, and
  Chrome journeys at 1280px and 390px covering keyboard expansion, filters,
  paging, viewport bounds, and visible outcome labels. Updated and visually
  checked the running embedded demo against its real audit history.

### Administration navigation review — Carbon UI shell

- [X] T029: Grouped Notices, Infrastructure, Users, Audit, and Backup & Recovery
  under one expandable Administration section in the left navigation, with
  permission-gated destinations, selected-page indication, and a button that
  exposes its expanded state to assistive technology. Workspaces and Templates
  remain the everyday destinations. Members can reach their personal notice
  history from the avatar menu.
- [X] T029: Verified 13 shell unit tests, the updated Notices tests, production
  export, and desktop/mobile Chrome journeys for role separation, keyboard
  expansion, and selected-page state. Rebuilt and visually checked the running
  demo.


## ARM64 platform support

- [X] T128 Add regression coverage for ARM64 artifact validation, provider inventory, catalog migration, and the native build inputs.
- [X] T129 Build and admit ARM64 UTM platform images alongside AMD64.
- [X] T130 Build the native ARM64 export on Apple Silicon and verify UTM boot, authenticated enrollment, a native devcontainer, and the offline repair IDE.


- [X] T131 Apply the catalog default to new template forms while preserving the image selected by existing template revisions.
- [X] T132 Import the compatible ARM64 image into the local installation, select it as default, and verify a native workspace launched from the UI.


## ARM64 workspace launch latency

- [X] T133 Shorten authenticated guest handoffs during launch while preserving idle polling and replay safety.
- [X] T134 Record redacted durations for guest setup phases and the underlying Dev Container CLI Docker operations.
- [X] T135 Preload the pinned ARM64 base image and verify the launch on a disposable native UTM workspace.
- [X] T136 Roll out the faster controller and a uniquely versioned ARM64 image to the local installation and select the new default.
- [X] T137 Time a real UI launch with the updated image and identify the remaining work in the one-minute budget.

## Workspace console

- [X] T138 Open the workspace IDE in a new tab while keeping the ShiftBox console open.
- [X] T139 Open the repair IDE in a new tab and refresh the local controller build.
- [X] T140 Open workspace previews in a new tab and refresh the local controller build.
- [X] T141 Default local UTM guest communication to the private bridge and support startup before the bridge is available.
