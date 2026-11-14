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
- [ ] T013 [P] Write failing session, authorization-matrix, Dex, and OIDC tests for opaque token digests, membership/session gates, every route-to-action mapping in `contracts/authorization-matrix.md`, owner/non-owner/admin exact-grant decisions, CSRF, exact Origin, stable upstream identity, supervisor failure, and revoke-before-close ordering in `internal/auth/session_test.go`, `internal/auth/policy_test.go`, and `internal/identity/dex_test.go`
- [X] T014 [P] Write failing HTTP/proxy boundary tests for RFC 3339 timestamps, UUIDs, stable problems, request IDs, conditional versions, idempotency keys, body limits, unknown-field rejection, management-header stripping, service allowlisting, and OpenAPI route/action drift in `internal/web/problem_test.go`, `internal/web/middleware_test.go`, `internal/proxy/sanitize_test.go`, and `tests/contract/openapi_routes_test.go`
- [X] T015 [P] Write failing SSE tests for ordered event IDs, Last-Event-ID replay, sequence-gap recovery, authorization recheck, and immediate closure on revocation in `internal/web/events_test.go`
- [X] T016 [P] Write failing UI component tests for server-derived Mantine Stepper state, disabled future steps, retained failure history, accessible labels, live status semantics, and refetch on SSE gaps in `ui/components/workflow-stepper.test.tsx`

### Shared implementation

- [X] T017 Implement the embedded Goose runner, explicit SQLite/PostgreSQL openers, transaction helpers, and schema ownership checks proven by T010 in `internal/database/database.go`, `internal/database/migrate.go`, and `internal/database/transaction.go`
- [ ] T018 Implement base installation/configuration/session/operation migrations in `migrations/sqlite/00001_foundation.sql` and `migrations/postgres/00001_foundation.sql`, preserving these verbatim constraints: Installation `id` is a "Stable opaque UUID; exactly one active installation record", `state` is "`bootstrap`, `configuring`, `active`, `recovery`, or `blocked`", `organization_name` is a "Non-empty display name", `data_profile` is "`sqlite` or `postgres`; immutable after active configuration except guarded restore activation", `management_origin` is a "Canonical HTTPS origin; unique and distinct from IDE/preview patterns", `ide_host_pattern` "maps exactly one workspace ID", `preview_host_pattern` maps workspace/service identity, `external_auth_interval_seconds` is a "Positive finite value; initial default 14,400 seconds", and `config_generation` "Increments on durable configuration activation"; ActivatedConfiguration generation "increases exactly by one" and "the last active generation is unique"; BrowserCertificate chain "covers every configured service hostname", fingerprint is unique, and state is "`pending`, `active`, `replaced`, or `invalid`"; Session `token_digest` is a "Unique digest of a random cookie value; raw token is never stored" and mode is "`bootstrap_restricted`, `password_change`, `normal`, or `recovery_review`"; Operation `(requested_by, request_key)` is unique, type is "`provision`, `start`, `open_ide`, `rebuild`, `repair`, `stop`, `delete`, `recover`, `update`, `backup`, `restore`, or setup operation", status is "`pending`, `running`, `waiting`, `succeeded`, `failed`, or `cancelled`", step status is "`pending`, `active`, `succeeded`, `failed`, or `skipped`", and `event_id` is "Monotonic per operation; used for SSE replay"
- [X] T019 Implement authenticated encryption and protected installation-key file loading proven by T011 in `internal/cryptostore/store.go` and `internal/cryptostore/keyfile.go`
- [X] T020 [P] Implement typed configuration loading with explicit paths, no environment reads below composition, and redacted validation errors in `internal/config/config.go` and `internal/config/validate.go`
- [X] T021 Implement the durable operation journal, leases, ordered steps/events, generation checks, and idempotent request lookup proven by T012 in `internal/operation/model.go`, `internal/operation/store.go`, and `internal/operation/journal.go`
- [X] T022 Implement reusable reconciler scheduling that commits intent before external calls and commits observed evidence afterward in `internal/operation/reconciler.go` and `internal/operation/worker.go`
- [ ] T023 Implement User, IdentityBinding, Session, and ResourceGrant stores and policy evaluation proven by T013 in `internal/auth/model.go`, `internal/auth/store.go`, `internal/auth/session.go`, and `internal/auth/policy.go`, preserving these verbatim constraints: User `primary_email` is a "Normalized email for display/local login; not a cross-provider merge key", `display_name` is optional, `role` is "`member` or `administrator`", `membership_state` is "`pending`, `active`, or `disabled`", and `credential_mode` is "`local` or `external`"; IdentityBinding `issuer` is an "Exact canonical issuer string", `subject` is an "Exact non-empty provider subject", `(issuer, subject, connector_id)` is unique, and "Email never creates a binding automatically"; Session is usable only when membership, mode, time bounds, and installation gate permit it; ResourceGrant `action` is an enumerated permission, only active grants participate, and administrator connection requires a distinct active grant plus audit event
- [X] T024 Implement managed Dex child-process configuration, health supervision, OIDC relying-party verification, and clean shutdown in `internal/identity/dex.go`, `internal/identity/oidc.go`, and `internal/identity/supervisor.go`
- [ ] T025 Implement `/session` read/logout handlers, management session cookies, CSRF/Origin middleware, authorization middleware, version/idempotency parsing, stable problems, and redacted structured request logging proven by T013-T014 in `internal/web/session_routes.go`, `internal/web/middleware.go`, `internal/web/problem.go`, and `internal/web/logging.go`
- [X] T026 Implement authorized operation SSE replay and connection closure proven by T015 in `internal/web/events.go` and `internal/auth/revocation.go`
- [X] T027 [P] Implement installation ECDSA P-256 CA loading plus TLS 1.3 server/client certificate verification primitives without guest enrollment behavior in `internal/guest/ca.go` and `internal/guest/tls.go`
- [X] T028 Implement management/IDE/preview connection registries and a reverse-proxy sanitizer that strips ShiftBox cookies, CSRF tokens, origin grants, and hop-by-hop headers in `internal/proxy/registry.go` and `internal/proxy/sanitize.go`
- [X] T029 Implement the static Mantine AppShell, ShiftBox theme/branding, role-aware navigation, persistent Alert region, TanStack Query client with no persistence, API problem view, and reusable WorkflowStepper proven by T016 in `ui/app/layout.tsx`, `ui/app/providers.tsx`, `ui/components/app-shell.tsx`, `ui/components/workflow-stepper.tsx`, and `ui/lib/query-client.ts`

**Checkpoint**: Database profiles, protected values, authentication primitives, durable operations, HTTP boundaries, SSE, connection revocation, and the visible workflow shell are testable shared foundations.

---

## Phase 3: User Story 1 — Launch and Use a Development Workspace (Priority: P1) — MVP

**Goal**: An operator activates a protected installation, creates an approved template, and a member launches, opens, stops/starts, rebuilds, and repairs one owner-private UTM workspace while designated files persist.

**Independent Test**: From a new supported installation, complete setup, create one template, launch one workspace, open the IDE, edit retained files, stop/start, rebuild, and enter repair; verify ordered progress, no duplicate provider effects, and retained data.

### Tests first

- [ ] T030 [P] [US1] Write failing setup and controller CLI contract tests for protected loopback access, one-time temporary password, forced change, resumable steps, certificate/profile validation, activation, bootstrap-session retirement, fresh sign-in, `shiftbox serve`, and secret-safe command output in `tests/contract/setup_api_test.go` and `cmd/shiftbox/main_test.go`
- [ ] T031 [P] [US1] Write failing setup integration tests for SQLite bootstrap, PostgreSQL ownership/TLS/schema preflight, activation copy of only initial administrator/setup state, and prohibition on SQLite fallback in `tests/integration/setup_activation_test.go`
- [ ] T032 [P] [US1] Write the reusable failing provider contract suite for inspect-before-retry, idempotent create/start, complete inventory, partial deletion, shared-resource preservation, generation fencing, and typed failures in `tests/contract/provider_suite_test.go`
- [ ] T033 [P] [US1] Write failing platform-image and UTM adapter tests for pinned Packer inputs, Ubuntu 24.04 Minimal amd64, supervised guest agent, VS Code Browser, immutable artifact manifest/digests, import/boot health, normalized complete inventory, preserved drive lists, supported capabilities, lost responses, and no direct bundle edits in `tests/release/platform_image_test.go` and `internal/provider/utm/provider_test.go`
- [ ] T034 [P] [US1] Write failing guest protocol tests for mTLS-derived identity, N/N−1 compatibility, poll replay, operation fencing, enrollment replay/alternate-key denial, and readiness evidence in `tests/contract/guest_protocol_test.go`
- [ ] T035 [P] [US1] Write failing workspace-domain tests for pinned launch snapshots, capacity reservation timing, desired/observed separation, owner privacy, lifecycle transitions, repair automation suppression, and retained-data contracts in `internal/workspace/service_test.go`
- [ ] T036 [P] [US1] Write failing workspace API tests for `/workspaces`, lifecycle actions, operation lookup/events, conditional versions, idempotency, capacity problems, and short-lived entry grants in `tests/contract/workspace_api_test.go`
- [ ] T037 [P] [US1] Write failing guest executor tests for persisted operation attempts, repository preparation, prebuilt-image and Dockerfile/build-definition Dev Containers, declared features, lifecycle-hook ordering/failure, separate declared `waitFor` and proxy readiness, rebuild retention checks, and repair without project automation in `internal/guest/executor_test.go`
- [ ] T038 [P] [US1] Write failing UI tests for setup forms, first-invalid focus, launch review, capacity refusal, server-driven progress, desired/observed state, retained-data copy, and repair explanation in `ui/features/setup/setup.test.tsx` and `ui/features/workspaces/workspaces.test.tsx`
- [ ] T039 [P] [US1] Write failing Playwright MVP journey for keyboard-only setup, launch through SSE reconnect, delayed IDE readiness, file persistence across stop/start/rebuild, and repair access in `ui/tests/e2e/mvp-workspace.spec.ts`

### Models and stores

- [ ] T040 [US1] Add platform/workspace/provider/guest/origin migrations in `migrations/sqlite/00002_workspace.sql` and `migrations/postgres/00002_workspace.sql`, preserving these verbatim constraints: PlatformArtifact `release_id` is a "Unique human-readable release identity", `architecture` "Must be `amd64` for MVP", `is_default` is "At most one compatible default", state is "`validating`, `available`, or `rejected`", and available content/manifest fields are immutable; ResourceProfile `name` is "Unique, non-empty name" and resource values are "Positive values inside provider capability bounds"; CapacityBudget limits are "Positive configured totals"; Workspace `owner_user_id` is "Immutable owner context", resource values are "Pinned resolved resources", desired state is "`stopped`, `running`, or `deleted`", observed state is "`unknown`, `provisioning`, `stopped`, `starting`, `running`, `unreachable`, `stopping`, `deleting`, `deleted`, or `failed`", `provider_instance_id` is "Null until reconciled creation; unique while live", and deleted has "no transition"; Reservation has "Unique active reservation per workspace", CPU/memory release requires confirmed stop/deletion, disk release requires verified complete deletion, and unknown outcomes retain reservations; ProviderResource type is "`vm`, `disk`, `media`, `network`, or adapter-defined owned type" and deletion succeeds only when all owned resources are absent; GuestRegistration state is "`eligible`, `active`, `expired`, `revoked`, or `deleted`" and revoked/deleted registrations cannot automatically re-enroll; EnrollmentGrant ID is a "Random high-entropy identifier stored as a digest", state is "`issued`, `consumed`, `expired`, or `invalidated`", expires "Initially 15 minutes after minting", and is single use; OriginGrant has opaque token digest and exact user/resource/service/origin binding
- [ ] T041 [US1] Add ProjectTemplate and TemplateRevision migrations needed for single-owner launch in `migrations/sqlite/00003_templates.sql` and `migrations/postgres/00003_templates.sql`, preserving these verbatim constraints: ProjectTemplate `owner_user_id` means "Owner; administrator management does not silently transfer ownership", `name` is "Non-empty display name", `state` is "`active` or `archived`", and `current_revision` is an "Existing immutable revision number"; TemplateRevision `(template_id, revision)` is a "Composite immutable identity", `repository_url` is an "HTTPS GitHub.com URL in MVP", `devcontainer_path` is a "Normalized repository-relative path", and `secret_names_json` contains "References only; never plaintext values"
- [ ] T042 [US1] Implement installation, activated configuration, browser certificate, platform artifact, resource profile, capacity, template revision, workspace, reservation, provider resource, guest registration, enrollment grant, and origin grant stores in `internal/config/store.go`, `internal/release/store.go`, `internal/template/store.go`, `internal/workspace/store.go`, and `internal/guest/store.go`

### Setup and provider implementation

- [ ] T043 [US1] Implement resumable protected setup state, first-administrator creation, one-time password output, password-change gate, and exact activation transitions proven by T030 in `internal/app/setup.go` and `cmd/shiftbox/setup.go`
- [ ] T044 [US1] Implement SQLite bootstrap and PostgreSQL profile preflight/activation with separate ShiftBox/Dex ownership and no fallback after activation proven by T031 in `internal/database/profile.go` and `internal/app/activate.go`
- [ ] T045 [US1] Implement browser certificate chain/key/hostname validation and atomic configuration activation in `internal/config/certificate.go`
- [ ] T046 [US1] Build the pinned Packer Ubuntu 24.04 Minimal amd64 artifact with supervised guest agent and VS Code Browser, then implement immutable artifact import, manifest/digest/provenance validation, default selection, resource profiles, capacity budget, and provider capability admission proven by T033 in `platform/packer/shiftbox.pkr.hcl`, `platform/guest/`, `internal/release/catalog.go`, and `internal/workspace/capacity.go`
- [ ] T047 [US1] Define the consumer-owned Provider interface, complete inventory/types, semantic errors, fake provider, and shared suite adapter proven by T032 in `internal/provider/provider.go`, `internal/provider/types.go`, `internal/provider/errors.go`, and `internal/provider/fake/fake.go`
- [ ] T048 [US1] Implement the compiled UTM provider using only the supported local scripting surface, complete inventory comparison, serialized mutations, inspect-after-effect, and preserved drive lists proven by T033 in `internal/provider/utm/provider.go`, `internal/provider/utm/inventory.go`, and `internal/provider/utm/script.go`

### Guest, workspace, API, and UI implementation

- [ ] T049 [US1] Implement provider-mediated one-use enrollment, CSR key-proof retry, controller-owned certificate fields, and four-hour replacement/eight-hour validity in `internal/guest/enrollment.go` and `internal/guest/certificates.go`
- [ ] T050 [US1] Implement authenticated guest poll handling with derived identity, one active operation, generation fencing, bounded evidence, 30-second jittered cadence, and N/N−1 checks proven by T034 in `internal/guest/protocol.go` and `internal/web/guest_routes.go`
- [ ] T051 [US1] Implement `shiftbox-agent serve` supervision and persistent operation attempt/step journal in `cmd/shiftbox-agent/main.go`, `internal/guest/agent.go`, and `internal/guest/local_journal.go`
- [ ] T052 [US1] Implement the fixed approved guest command executor for repository preparation, prebuilt-image and Dockerfile/build-definition Dev Containers, declared features, lifecycle hooks, declared connection-ready stage, rebuild, IDE verification, repair entry/exit, and graceful shutdown proven by T037 in `internal/guest/executor.go`, `internal/guest/devcontainer.go`, and `internal/guest/repair.go`
- [ ] T053 [US1] Implement guest-initiated mTLS Yamux registration, one stream per HTTP/WebSocket request, service allowlisting, heartbeats, and expiry/revocation closure in `internal/guest/tunnel.go` and `internal/proxy/tunnel.go`
- [ ] T054 [US1] Implement template creation/revision validation and workspace admission that snapshots owner, template revision, repository, resources, platform, network policy, and secret references before reserving capacity in `internal/template/service.go` and `internal/workspace/service.go`
- [ ] T055 [US1] Implement provision/start/stop/rebuild/repair/open-IDE reconciliation with inspect-before-retry, desired/observed separation, durable steps, readiness evidence, and reservation release rules in `internal/workspace/reconciler.go` and `internal/workspace/lifecycle.go`
- [ ] T056 [US1] Implement the setup, template, workspace, operation, event, and entry-grant OpenAPI routes proven by T030 and T036 in `internal/web/setup_routes.go`, `internal/web/template_routes.go`, `internal/web/workspace_routes.go`, and `internal/web/operation_routes.go`
- [ ] T057 [US1] Implement owner/audited-admin IDE and preview routing that exchanges a short-lived exact-origin grant for a separate host-only cookie and never forwards management authority in `internal/proxy/grants.go`, `internal/proxy/router.go`, and `internal/web/proxy_routes.go`
- [ ] T058 [US1] Implement the keyboard-accessible Mantine setup Stepper and forms, including one-time password acknowledgement, profile/certificate/provider validation, activation warning, and fresh sign-in in `ui/features/setup/setup-flow.tsx` and `ui/app/setup/page.tsx`
- [ ] T059 [US1] Implement template creation plus workspace launch/detail/lifecycle/repair screens with Mantine controls and server-derived WorkflowStepper state in `ui/features/templates/template-form.tsx`, `ui/features/workspaces/launch-flow.tsx`, `ui/features/workspaces/workspace-detail.tsx`, and `ui/app/workspaces/page.tsx`
- [ ] T060 [US1] Wire static UI embedding and controller/agent CLI composition so `shiftbox serve`, `shiftbox setup`, and `shiftbox-agent serve` satisfy their contracts in `internal/web/static.go`, `cmd/shiftbox/main.go`, and `cmd/shiftbox-agent/main.go`
- [ ] T061 [US1] Add and run the exact-stack Go acceptance test together with the T039 Playwright journey, then record separate test results and pinned release evidence without broadening supported versions in `tests/release/mvp_workspace_test.go`, `documentation/validation/mvp-workspace.md`, and `platform/manifests/release.json`

**Checkpoint**: User Story 1 is a demonstrable MVP slice and must pass independently before later stories are required.

---

## Phase 4: User Story 2 — Share Reusable Projects Safely (Priority: P2)

**Goal**: Administrators and template owners manage members, immutable revisions, launch grants, three-scope secrets, and owner-bound GitHub HTTPS authority without weakening workspace privacy.

**Independent Test**: Create two members, share a template, configure all three secret scopes including an explicit empty value, connect private GitHub authorization, launch as the grantee, then revoke/archive and verify privacy, precedence, denial, and audit evidence.

### Tests first

- [ ] T062 [P] [US2] Write failing membership, identity, password, and audit API/store tests for local users, pending external users, stable issuer/subject/connector binding, explicit role approval, self password change, actual upstream reauthorization, protected recovery boundaries, disable revocation, append-only redacted events, and protected retention in `tests/contract/users_api_test.go` and `internal/audit/store_test.go`
- [ ] T063 [P] [US2] Write failing template service/API tests for immutable revisions, owner/admin edit rules, update/new-revision routes, acknowledged launch grants, owner-private launches, revocation, archival, and preserved provenance in `internal/template/service_test.go` and `tests/contract/templates_api_test.go`
- [ ] T064 [P] [US2] Write failing secret service/API tests for metadata-only listing, exactly three scopes, User → Project Template → Organization precedence, explicit empty override, fallback preview on delete, ciphertext context binding, and management/audit omission in `internal/secret/service_test.go` and `tests/contract/secrets_api_test.go`
- [ ] T065 [P] [US2] Write failing management and guest secret/Git contract tests for metadata-only secret listing, mTLS-derived owner/template context, denied guest-supplied scope, exit codes 0/3/4/5, no persistent cache, authorization initiation/state-bound callback/revocation, current owner token, coordinated refresh, and reconnect-required failure in `tests/contract/credentials_test.go` and `tests/contract/git_authorization_api_test.go`
- [ ] T066 [P] [US2] Write failing GitHub authorization tests for repository coverage, stable provider identity, fenced concurrent renewal, app/refresh credential exclusion, revocation, and permanent renewal failure in `internal/identity/github_test.go`
- [ ] T067 [P] [US2] Write failing UI tests for people states, template grant exposure warning, secret metadata-only views, empty override, fallback deletion warning, archival filters, and no secret value rendering in `ui/features/admin/people.test.tsx`, `ui/features/templates/sharing.test.tsx`, and `ui/features/secrets/secrets.test.tsx`
- [ ] T068 [P] [US2] Write failing Playwright sharing journey covering two users, owner privacy, project-secret warning, precedence, administrator connection owner context, Git authority, revoke, and archive in `ui/tests/e2e/template-sharing.spec.ts`

### Models and implementation

- [ ] T069 [US2] Add sharing/secret/Git/audit migrations in `migrations/sqlite/00004_sharing.sql` and `migrations/postgres/00004_sharing.sql`, preserving these verbatim constraints: TemplateGrant `(template_id, user_id)` is a "Unique active grant pair" and records `project_secret_warning_ack_at`; SecretValue `scope_type` is "`user`, `template`, or `organization` only", `name` is a "Normalized non-empty key; unique within active scope", `is_explicit_empty` "Distinguishes empty override from absence", and values never appear in audit/read models; GitAuthorization has "One record per user and app registration", provider "`github.com` in MVP", state "`connected`, `refreshing`, `reconnect_required`, or `revoked`", and `refresh_generation` fences concurrent results; AuditEvent metadata is "Redacted bounded context; schema rejects known secret fields"
- [ ] T070 [US2] Implement administrator local-user creation, self password change, external connector configuration, external identity approval by stable binding, actual upstream reauthorization within the configured positive interval (initially four hours, never token-refresh-only), role/membership updates, protected local recovery of an existing administrator, and transactional session/grant revocation proven by T062 in `internal/identity/users.go`, `internal/identity/password.go`, `internal/identity/external.go`, `internal/identity/recovery.go`, and `internal/auth/membership.go`
- [ ] T071 [US2] Implement append-only security audit writes and configurable retention that never removes current authority, operations, maintenance, or workspace provenance in `internal/audit/store.go` and `internal/audit/service.go`
- [ ] T072 [US2] Implement immutable template revisions, launch grants with required project-secret acknowledgement, current-state launch authorization, revocation, archival, and administrator archive filtering proven by T063 in `internal/template/sharing.go` and `internal/template/archive.go`
- [ ] T073 [US2] Implement protected three-scope secret storage/resolution, explicit empty handling, authorized fallback preview, and metadata-only management reads proven by T064 in `internal/secret/store.go`, `internal/secret/resolver.go`, and `internal/secret/service.go`
- [ ] T074 [US2] Implement owner-bound GitHub.com App authorization initiation, state-bound callback, repository coverage, encrypted token storage, single-flight fenced refresh, revoke, and reconnect-required state proven by T065-T066 in `internal/identity/github.go`, `internal/identity/github_oauth.go`, and `internal/identity/github_store.go`
- [ ] T075 [US2] Implement guest secret resolution and Git token endpoints plus `shiftbox secrets get-secret` and internal credential-helper exit behavior proven by T065 in `internal/web/credential_routes.go`, `internal/guest/credentials.go`, and `cmd/shiftbox-agent/credentials.go`
- [ ] T076 [US2] Implement self password/external reauthorization, users/membership/external connector, template update/new revision/grant/revoke/archive, secret metadata/set/delete, Git authorization/callback/revoke, and audit read handlers from the OpenAPI and authorization-matrix contracts in `internal/web/session_routes.go`, `internal/web/user_routes.go`, `internal/web/template_grant_routes.go`, `internal/web/secret_routes.go`, `internal/web/git_routes.go`, and `internal/web/audit_routes.go`
- [ ] T077 [US2] Implement Mantine password change, external reauthorization, People, template revision/sharing/archive, secret metadata/edit/delete, Git connection, and audit screens with the required warnings in `ui/features/session/password.tsx`, `ui/features/session/external-reauthorization.tsx`, `ui/features/admin/people.tsx`, `ui/features/templates/sharing.tsx`, `ui/features/secrets/secrets.tsx`, `ui/features/git/connection.tsx`, and `ui/app/audit/page.tsx`
- [ ] T078 [US2] Add and run the Go sharing acceptance test together with the T068 Playwright journey, then record separate evidence for privacy, revocation, secret non-disclosure, and owner-bound Git behavior in `tests/acceptance/template_sharing_test.go` and `documentation/validation/template-sharing.md`

**Checkpoint**: User Story 2 works against the User Story 1 launch path but is independently testable with its own users, template, secrets, and Git authorization.

---

## Phase 5: User Story 3 — Operate Workspaces Without Losing Control (Priority: P3)

**Goal**: Administrators safely operate capacity, access revocation, networking, deletion/retention, maintenance, certificates, notices, release compatibility, and uncertain provider outcomes with visible recovery states.

**Independent Test**: Exercise capacity refusal, provider timeouts, disable/revoke, idle deletion, certificate expiry, long-stop recovery, and maintenance with hung work; verify reservations, authority boundaries, warnings, safe boundaries, and actionable progress.

### Tests first

- [ ] T079 [P] [US3] Write failing capacity/reconciliation tests for CPU, memory, and disk refusal; no queue/eviction/resize; lost response; controller restart; conflicting inventory; and reservation retention until confirmed evidence in `tests/integration/capacity_reconciliation_test.go`
- [ ] T080 [P] [US3] Write failing network-policy tests for only provider-supported modes, stopped-only changes, required controller/DNS/repository exceptions, allowlist validation, verification failure, and remain-stopped behavior in `internal/workspace/network_test.go`
- [ ] T081 [P] [US3] Write failing revocation integration tests that close management SSE, IDE, preview, WebSocket, poll, and tunnel connections before reporting success and request stops for disabled users in `tests/integration/revocation_test.go`
- [ ] T082 [P] [US3] Write failing retention tests for disabled-by-default deletion, selected activity signals, outage grace, final transaction recheck, graceful timeout, explicit deletion authority, partial cleanup, and disk reservation retention in `internal/workspace/retention_test.go`
- [ ] T083 [P] [US3] Write failing maintenance tests for deadlines, warnings, capacity, safe activation boundaries, no maintenance force-kill, verification/rollback evidence, prior stopped-state restoration, and compatibility blocking in `internal/workspace/maintenance_test.go`
- [ ] T084 [P] [US3] Write failing long-stop recovery tests for exact provider instance/ownership/generation, one-use delivery, preserved disks/drive lists, and denial of revoked/deleted/unknown/cloned/mismatched identities in `tests/integration/long_stop_recovery_test.go`
- [ ] T085 [P] [US3] Write failing certificate/release/preflight API and CLI tests for 30-day browser warnings, independent internal PKI, invalid replacement rejection, no plaintext fallback, N/N−1 guest support, retained-workspace gate, backup/key/certificate/provider/tool checks, stable exits, and actionable blocks in `internal/release/preflight_test.go` and `cmd/shiftbox/migrate_test.go`
- [ ] T086 [P] [US3] Write failing notice, infrastructure, maintenance, and audit-retention API tests for persistent conditions, publish/list/read/dismiss/history semantics, maintenance independence, provider/capacity/artifact/profile/policy management, and protected active records in `internal/app/notices_test.go` and `tests/contract/admin_operations_api_test.go`
- [ ] T087 [P] [US3] Write failing UI tests for capacity errors, desired/observed state, destructive modal safe focus/typed name/checkbox, retention warnings, offboarding stop failures, maintenance steps, certificate/storage alerts, and narrow keyboard navigation in `ui/features/workspaces/operations.test.tsx` and `ui/features/admin/maintenance.test.tsx`
- [ ] T088 [P] [US3] Write failing Playwright operations journey for hostile preview isolation, cross-origin mutation denial, live revocation, deletion focus/copy, partial cleanup reservation, inactivity, long-stop recovery, and maintenance in `ui/tests/e2e/operations.spec.ts`

### Models and implementation

- [ ] T089 [US3] Add network/activity/maintenance/notice migrations in `migrations/sqlite/00005_operations.sql` and `migrations/postgres/00005_operations.sql`, preserving these verbatim constraints: NetworkPolicy `name` is a "Unique display name", mode is "`all`, `no_network`, or `allowlist`", and required exceptions include controller/proxy, DNS, Git, and approved auxiliary destinations; any selected active ActivityPolicy signal resets the deadline and an idle report does not; MaintenanceCampaign state is "`scheduled`, `active`, `blocked`, `completed`, `cancelled`" and MaintenanceTarget state is "`pending`, `waiting_safe`, `updating`, `verified`, `restoring_state`, `failed`, `completed`"; persistent operational notices remain visible while their condition is active regardless of dismissal history
- [ ] T090 [US3] Complete admission/reconciliation behavior for explicit limiting-resource errors, no queue/eviction/resize, retained reservations, orphan/conflict visibility, and restart-safe inspection proven by T079 in `internal/workspace/capacity.go`, `internal/workspace/reconciler.go`, and `internal/provider/reconcile.go`
- [ ] T091 [US3] Implement provider-capability-derived network policies, stopped-only application, explicit required exceptions, post-change verification, and remain-stopped failure behavior proven by T080 in `internal/workspace/network.go` and `internal/provider/utm/network.go`
- [ ] T092 [US3] Implement transactional user/permission/session/origin/guest revocation publication and closure of all established connection types proven by T081 in `internal/auth/revocation.go`, `internal/proxy/registry.go`, and `internal/guest/tunnel.go`
- [ ] T093 [US3] Implement activity observations, retention deadlines, host-outage grace, precommit authority/activity/state recheck, graceful then provider-enforced deletion, and verified reservation release proven by T082 in `internal/workspace/activity.go`, `internal/workspace/retention.go`, and `internal/workspace/delete.go`
- [ ] T094 [US3] Implement long-stop eligibility verification and provider-mediated enrollment recovery without disk/identity reset proven by T084 in `internal/workspace/recovery.go` and `internal/provider/utm/enrollment.go`
- [ ] T095 [US3] Implement maintenance campaign scheduling, warnings, per-target durable operations, safe-boundary activation, signed update staging, verification/rollback evidence, prior-state restoration, and blocked completion proven by T083 in `internal/workspace/maintenance.go` and `internal/guest/update.go`
- [ ] T096 [US3] Implement browser-certificate expiry/invalid notices, storage/provider/controller/key persistent conditions, and no unprotected listener fallback in `internal/app/conditions.go` and `internal/config/certificate.go`
- [ ] T097 [US3] Implement current/N−1 compatibility evaluation and irreversible migration preflight requiring verified backup, guest gate, tools, keys, provider, database ownership, certificate, and CA lifetime in `internal/release/compatibility.go`, `internal/release/preflight.go`, and `cmd/shiftbox/migrate.go`
- [ ] T098 [US3] Implement system notices, receipts, condition persistence, dismissal/history, and protected retention pruning proven by T086 in `internal/app/notices.go` and `internal/app/retention.go`
- [ ] T099 [US3] Implement workspace policy/action, user disable, notice publish/list/receipt, infrastructure status, browser-certificate replacement, platform artifact, resource profile, capacity, network policy, maintenance, and migration-preflight handlers from the OpenAPI and authorization-matrix contracts in `internal/web/workspace_policy_routes.go`, `internal/web/admin_routes.go`, `internal/web/notice_routes.go`, `internal/web/infrastructure_routes.go`, and `internal/web/maintenance_routes.go`
- [ ] T100 [US3] Implement the accessible destructive workspace Modal with initial focus on Cancel, typed workspace name, “I have pushed work I need to keep” acknowledgement, retained-path list, and management-backup exclusion in `ui/features/workspaces/delete-workspace-modal.tsx`
- [ ] T101 [US3] Implement Mantine Infrastructure, People offboarding, Notices, Retention, and Maintenance screens with server-driven Steppers and actionable persistent conditions in `ui/features/admin/infrastructure.tsx`, `ui/features/admin/offboarding.tsx`, `ui/features/notices/notices.tsx`, `ui/features/workspaces/retention.tsx`, and `ui/features/admin/maintenance.tsx`
- [ ] T102 [US3] Implement `shiftbox recover-admin` and `shiftbox migrate preflight` human/JSON output, stable exit codes, secret-safe arguments, and audit behavior in `cmd/shiftbox/recover_admin.go`, `cmd/shiftbox/migrate.go`, and `cmd/shiftbox/output.go`
- [ ] T103 [US3] Add and run the Go operations acceptance test together with the T088 Playwright journey, then record separate evidence for capacity, revocation, destructive clarity, recovery denial, and maintenance authority in `tests/acceptance/workspace_operations_test.go` and `documentation/validation/workspace-operations.md`

**Checkpoint**: User Story 3 exposes every operational workflow and failure state without weakening ownership, reservations, or retained user work.

---

## Phase 6: User Story 4 — Back Up and Recover Management State (Priority: P4)

**Goal**: An administrator creates an independently encrypted, verified management backup and restores it transactionally into a reviewed recovery mode without claiming to back up VM disks or reviving stale authority.

**Independent Test**: Back up a running installation, validate and restore to an empty replacement target on the original host, reject wrong-password/truncated/missing-key/incompatible/unowned targets, reconcile provider state, review authority, and explicitly reactivate.

### Tests first

- [ ] T104 [P] [US4] Write failing backup tests proving management writes pause only while application/Dex checkpoints are acquired, resume before archive assembly/encryption and after checkpoint failure, workspace computation remains uninterrupted, and the canonical age-encrypted verified archive explicitly excludes VM disks in `internal/backup/backup_test.go`
- [ ] T105 [P] [US4] Write failing restore validation tests for wrong password, truncation, digest mismatch, incompatible release/schema, mismatched key, wrong host, non-empty/unowned target, and zero mutation before validation in `internal/backup/restore_test.go`
- [ ] T106 [P] [US4] Write failing recovery tests for invalidated sessions/origin/enrollment grants/OAuth state, provider inventory matches/conflicts, gated workspace entry/destructive actions, identity/authorization review, and explicit activation in `internal/backup/recovery_test.go`
- [ ] T107 [P] [US4] Write failing backup/restore API and CLI contract tests for password handling, stable JSON/exit codes, operation steps, validation IDs, separate activation, and no protected data in diagnostics in `tests/contract/backup_restore_test.go`
- [ ] T108 [P] [US4] Write failing UI and Playwright tests for backup scope copy, cleared password state, ordered Stepper, validation findings, recovery gating, invalidated authority, provider conflicts, and keyboard focus in `ui/features/recovery/recovery.test.tsx` and `ui/tests/e2e/backup-recovery.spec.ts`

### Models and implementation

- [ ] T109 [US4] Add BackupRecord and RecoveryReview migrations in `migrations/sqlite/00006_recovery.sql` and `migrations/postgres/00006_recovery.sql`, preserving these verbatim constraints: BackupRecord state is "`preparing`, `checkpointing`, `encrypting`, `completed`, or `failed`", the password/archive plaintext are never stored, and completion occurs "only after archive close and integrity verification"; RecoveryReview state is "`identity_review`, `provider_reconcile`, `authorization_review`, `ready`, `activated`, or `blocked`" and normal mutations, workspace access, and overdue destructive retention remain gated until `activated`
- [ ] T110 [US4] Implement canonical backup manifest/checkpoint orchestration, scoped SQLite/PostgreSQL and Dex export, protected-file collection, passphrase age encryption, close/digest verification, and workspace-disk exclusion proven by T104 in `internal/backup/manifest.go`, `internal/backup/checkpoint.go`, and `internal/backup/backup.go`
- [ ] T111 [US4] Implement whole-archive validation, compatibility/key/host/target ownership checks, empty replacement activation, and no merge/overwrite behavior proven by T105 in `internal/backup/validate.go` and `internal/backup/restore.go`
- [ ] T112 [US4] Implement recovery-mode gate, stale-authority invalidation, identity review, provider reconciliation, workspace conflict decisions, authorization/retention review, and explicit normal-service activation proven by T106 in `internal/backup/recovery.go` and `internal/auth/recovery_gate.go`
- [ ] T113 [US4] Implement backup, restore-validation, and recovery-activation OpenAPI handlers with durable Operation steps in `internal/web/backup_routes.go` and `internal/web/recovery_routes.go`
- [ ] T114 [US4] Implement `shiftbox backup`, `shiftbox restore validate`, and `shiftbox restore activate` with TTY/stdin password handling, human/JSON output, and stable exits proven by T107 in `cmd/shiftbox/backup.go` and `cmd/shiftbox/restore.go`
- [ ] T115 [US4] Implement Mantine Backup & Recovery forms, scope warnings, cleared password fields, operation Stepper, validation findings, and gated recovery review in `ui/features/recovery/backup.tsx`, `ui/features/recovery/restore.tsx`, `ui/features/recovery/review.tsx`, and `ui/app/recovery/page.tsx`
- [ ] T116 [US4] Add and run the Go recovery acceptance test together with the T108 Playwright journey, then record separate recovery evidence without workspace-disk claims in `tests/acceptance/backup_recovery_test.go` and `documentation/validation/backup-recovery.md`

**Checkpoint**: User Story 4 safely restores owned management state while stale authority and destructive actions remain blocked until reviewed.

---

## Phase 7: Polish and Cross-Cutting Release Gates

**Purpose**: Verify the integrated MVP against the approved architecture without adding deferred product scope.

- [ ] T117 [P] First add a failing OpenAPI-to-TypeScript drift test, then generate UI contract types without generated Go handlers and make the drift test pass in `tests/contract/openapi_types_test.go`, `ui/lib/api-types.ts`, and `scripts/check-openapi.sh`
- [ ] T118 [P] Add bounded redaction and secret-leak tests across logs, URLs, API responses, browser storage, process arguments, audit metadata, and archives in `tests/integration/secret_leak_test.go` and `ui/tests/e2e/browser-storage.spec.ts`
- [ ] T119 [P] Add automated accessibility checks plus manual keyboard/focus evidence for every Mantine Stepper workflow in `ui/tests/e2e/accessibility.spec.ts` and `documentation/validation/accessibility.md`
- [ ] T120 Add end-to-end controller restart injection at every persisted setup, lifecycle, maintenance, backup, restore, and recovery step in `tests/acceptance/restart_matrix_test.go`
- [ ] T121 Add SQLite/PostgreSQL parity, race, real HTTP/mTLS, Dex/OIDC, hostile-origin, and corrupted-backup integration gates in `tests/integration/release_gate_test.go`
- [ ] T122 Run the already-defined exact UTM/Packer/import/boot/recovery/drive-preservation harness under the `utm_release` build tag and record controlled-host evidence in `tests/release/utm_stack_test.go` and `documentation/validation/utm-stack.md`
- [ ] T123 Add Debian and Ubuntu glibc/amd64 fixtures that separately exercise prebuilt-image and Dockerfile/build-definition Dev Containers, declared features, lifecycle-hook success/failure, declared connection-ready stages, and retained-data checks in `tests/release/fixtures/debian/` and `tests/release/fixtures/ubuntu/`
- [ ] T124 Run V1–V14 and a moderated first-use study with at least 10 eligible participants and at least 9 first-attempt successes, recording protocol, eligibility, failures, results, release/browser/database/UTM/platform/guest/IDE/schema/certificate versions, and artifact digests in `documentation/validation/mvp-v1-v14.md`, `documentation/validation/first-use-study.md`, and `platform/manifests/release.json`
- [ ] T125 Re-run every command and scenario in `specs/001-shiftbox-mvp/quickstart.md`, record failures, route each implementation defect back through its earlier failing test task before correction, and record final results in `documentation/validation/quickstart-results.md`
- [ ] T126 Review all exported Go APIs and non-obvious trust/reconciliation code for concise application-context comments, run formatting/lint/race/UI/browser checks, and record the clean result in `documentation/validation/final-checks.md`

---

## Dependencies and Execution Order

### Phase dependencies

- **Phase 1 — Setup**: starts immediately.
- **Phase 2 — Foundational**: depends on Phase 1 and blocks every user story.
- **Phase 3 — User Story 1**: depends on Phase 2 and is the first independently demonstrable MVP slice.
- **Phase 4 — User Story 2**: domain tests and services may start after Phase 2, but T068 and T078 depend on the completed User Story 1 launch path through T061.
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
