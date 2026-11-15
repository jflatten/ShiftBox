# Implementation Plan: ShiftBox MVP

**Branch**: `feature/mvp` | **Date**: 2026-11-13 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/001-shiftbox-mvp/spec.md`

## Summary

Build the first ShiftBox release as a self-hosted control plane on an awake, logged-in Mac beside UTM. A native Go controller serves a statically exported React/Next.js/Mantine management UI, owns identity authorization and durable reconciliation, proxies authenticated IDE and preview traffic, and manages SQLite or PostgreSQL state. A separate Go guest service runs inside each Ubuntu VM, polls desired state, executes approved Dev Container operations, reports activity, rotates its identity, and creates an outbound multiplexed mTLS tunnel. The plan preserves owner-private workspaces, retained VM data, explicit provider authority, visible Stepper workflows, and guarded backup and recovery.

## Technical Context

**Language/Version**: Go 1.27.4 for controller and guest services; TypeScript 5.x, React 19.3.x, Next.js 16.x Active LTS, and Node.js 26.13.x LTS for the build-only UI toolchain

**Primary Dependencies**: Go standard library (`net/http`, `crypto/*`, `database/sql`, `embed`, `log/slog`); Dex 2.46.1 as a product-managed child process; `coreos/go-oidc/v3` 3.21.x and `golang.org/x/oauth2`; `pgx/v5` 5.13.x; `modernc.org/sqlite` 1.62.x; `pressly/goose/v3` 3.30.x; `filippo.io/age` 1.3.x; `hashicorp/yamux` 0.1.2; React Query 5.x; Mantine 9.x core, hooks, form, notifications, and modals packages

**Storage**: SQLite files for demo/very-small installations; one operator-managed PostgreSQL database with separate ShiftBox and Dex schemas/roles for production; protected installation key and configuration files outside the database; VM-owned workspace disks remain provider storage and are excluded from management backups

**Testing**: Go `testing`, `httptest`, `go test -race`, and `go-cmp`; scoped fakes and real HTTP/mTLS round trips; dialect acceptance suites for SQLite and PostgreSQL; Jest plus React Testing Library for UI units; Playwright for browser, origin, keyboard, and end-to-end workflows; opt-in exact-stack UTM/Packer/VM validation suites for release gates

**Target Platform**: Native macOS controller on a logged-in, awake ARM Mac with UTM/QEMU; Ubuntu 24.04 LTS Minimal amd64 guest VMs; glibc-based amd64 project containers; modern private-network browsers; production UI is static assets served by Go with no Node.js runtime

**Project Type**: Greenfield web control plane, native controller/CLI, VM guest service, browser proxy, and platform-image pipeline in one repository and one Go module

**Performance Goals**: No unvalidated workspace-count or throughput claim. A mutating request returns only after its durable operation and initial step commit, before any provider effect; the UI renders that persisted step from the response and subsequently applies ordered SSE updates. Backup write coordination lasts only through acquisition of the application and identity checkpoints, resumes before archive assembly/encryption, and also resumes after any checkpoint failure. Duplicate provider effects remain prohibited under retry.

**Constraints**: One active controller; one VM and one primary devcontainer per workspace; compiled UTM provider only in MVP; management/IDE/preview origins separated; management authority never enters guests; all external effects reconciled before retry; CPU/RAM/disk reservations retained through uncertain outcomes; project/home/designated data retained across reboot/start/rebuild but lost at verified VM deletion; no production Next.js server; no workspace-data backup; no weak authentication or TLS fallback

**Scale/Scope**: One organization with administrators and members; capacity is administrator configured rather than hard-coded; functional and security gates V1–V14 define release readiness; EC2, HA, multi-organization SaaS, Compose projects, public sharing, extra IDEs, MFA, application-key rotation, and load testing are deferred

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-research gate

| Principle | Evidence | Result |
| --- | --- | --- |
| KISS | One Go module, one controller process plus only the separately required Dex/guest roles, standard-library-first HTTP/crypto/logging, compiled UTM adapter, no broker or scheduler | PASS |
| Explicit ownership and trust | Controller owns intent and authorization; UTM owns VM facts; guest owns in-VM operations; guest input never establishes identity or provider truth | PASS |
| Preserve work and reconcile | Durable operation journal, desired/observed separation, provider inventory reconciliation, retained reservations, explicit deletion warning, VM data excluded from management backup | PASS |
| Test first and real failures | Every implementation task must begin with commented unit/contract tests; plan includes lost responses, replay, revocation, disk pressure, database loss, corrupted backup, and hostile-origin tests | PASS |
| Visible workflows | Setup, lifecycle, maintenance, backup, and recovery use Mantine Stepper fed by durable operation steps and live events; actionable errors and keyboard/focus checks are contractual | PASS |
| Architecture constraints | Native Mac/UTM MVP, Ubuntu amd64 VM, one primary devcontainer, VS Code Browser, Dex, SQLite/PostgreSQL, static Next.js/Mantine assets served by Go | PASS |

No gate violation requires a complexity exception.

### Post-design gate

Phase 1 preserves every pre-research result. The data model makes authority, generations, reservations, and desired/observed state explicit. The API, guest, provider, CLI, and UI contracts keep trust boundaries separate. The quickstart validates real failure paths before supported-release claims. No deferred product scope or additional infrastructure service was introduced.

## Technical Decisions

### Process and communication model

- `shiftbox serve` starts the management HTTPS server, controller/reconciler, authorized proxy, UTM adapter, background maintenance, and supervised Dex process from explicit configuration.
- `shiftbox-agent` runs inside each workspace VM under independent supervision. It polls the internal mTLS endpoint every 30 seconds with jitter, deduplicates operations by ID and generation, and reports steps and activity.
- Browser management uses versioned JSON REST under `/api/v1`. Operation updates use server-sent events because the data is controller-to-browser and reconnectable by event ID. IDE and preview traffic uses HTTP/WebSocket proxying through the guest tunnel.
- Each guest opens one persistent mTLS connection to a distinct internal listener. Yamux provides per-request streams over that connection. Go's reverse proxy sends sanitized HTTP/1.1 and WebSocket traffic through streams.
- UTM calls are isolated behind a consumer-defined provider interface. The adapter uses the supported local scripting surface and always re-inspects complete owned-resource inventory before deciding whether an effect remains.

### Data and transaction model

- `database/sql` is the shared store boundary. PostgreSQL uses `pgx/v5/stdlib`; SQLite uses the pure-Go `modernc.org/sqlite` driver so native Mac builds do not depend on CGO.
- Handwritten SQL repositories keep queries explicit. Embedded Goose SQL migrations are split by dialect only where syntax differs; no ORM or generic repository layer is added.
- A transaction records desired state, operation identity, generation, and reservations before an external effect. A reconciler claims due work with a renewable lease, performs or observes the provider/guest effect, then commits observed evidence and the next step.
- Application secrets and OAuth tokens use AES-256-GCM with a fresh random nonce, key version, and entity/scope/name metadata as additional authenticated data. The installation key is a protected file outside the database or an operator-supplied mounted file.
- Management backups stream a versioned manifest plus coordinated application/Dex dumps and protected files into passphrase-based age encryption. Validation completes before restore activation.

### Identity and browser model

- Dex runs as a pinned managed child process. ShiftBox configures its local connector and optional upstream OIDC connector, manages local passwords through Dex's authenticated API, and remains the sole membership/permission authority.
- ShiftBox is an OIDC relying party through `go-oidc`; successful authentication creates a random opaque ShiftBox session whose digest is stored server-side. Restricted bootstrap and forced-password-change state are ShiftBox session properties.
- Every management and proxy entry point maps to one stable action in `contracts/authorization-matrix.md`; role defaults, ownership, exact-resource grants, session modes, and installation gates are evaluated from current controller records before durable intent is created.
- Management cookies are Secure, HttpOnly, host-only, and SameSite=Strict. Mutations also require a session-bound CSRF token and exact allowed Origin. IDE/preview entry exchanges a short-lived single-origin grant for a separate host-only cookie so management credentials are never forwarded to guest applications.
- Revocation publishes an in-process connection-close event, invalidates sessions/grants transactionally, and closes active management event streams, IDE/preview proxy connections, and matching guest access without waiting for polling or certificate expiry.

### UI model

- Next.js uses App Router with `output: 'export'`; every dynamic management request is client-side against Go APIs. Unsupported runtime-only features, server actions, and request-dependent route handlers are prohibited.
- Mantine `AppShell` provides responsive navigation. Mantine `Stepper` renders durable workflow steps; upcoming steps are not selectable, completed steps remain inspectable, and each step has a visible label/description for keyboard and screen-reader use.
- Mantine Form handles field state and focuses the first invalid field. Core Modal or the modals manager handles destructive confirmation with initial focus on the safe action. Notifications supplement persistent inline Alert/Error state and never carry the only failure explanation.
- TanStack Query owns browser server-state with hierarchical key factories, targeted invalidation, explicit stale times, and online network mode. Its cache is not persisted, contains no secret values, and is refreshed from operation SSE events. Mutations are not queued offline or optimistically shown as committed for destructive actions.
- Approved ShiftBox assets and theme tokens are copied from `branding/` into `ui/public/brand/`; the shipped UI uses the exact ShiftBox spelling and supplied light/dark assets.

### Error and test model

- Domain packages return semantic sentinel or typed errors where callers must branch. Package boundaries wrap with context; HTTP/guest/provider boundaries translate errors into stable contract codes and do not expose internal strings.
- Only `main` exits. Every service receives explicit dependencies, contexts, and clocks; environment and filesystem reads stay at composition boundaries.
- Unit tests are written before implementation and each test includes a comment describing the real behavior under test. Fakes replace mocks; real transports and real database dialects are used for integration contracts.
- Exact UTM, seed-media, Packer, certificate, tunnel, browser-origin, backup/restore, and N/N−1 guest paths remain release-gated even when unit and local integration suites pass.

## Project Structure

### Documentation (this feature)

```text
specs/001-shiftbox-mvp/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── openapi.yaml
│   ├── guest-protocol.md
│   ├── provider-interface.md
│   ├── authorization-matrix.md
│   ├── cli.md
│   └── ui-workflows.md
└── tasks.md                  # Created by $speckit-tasks, not this phase
```

### Source Code (repository root)

```text
cmd/
├── shiftbox/                 # controller, operator CLI, and run() composition
└── shiftbox-agent/           # guest service and in-container secret command

internal/
├── app/                      # controller composition and background workers
├── auth/                     # sessions, membership, permissions, CSRF, grants
├── backup/                   # checkpoint, archive, restore, recovery gate
├── config/                   # explicit config loading and activation records
├── cryptostore/              # contextual protected-value encryption
├── database/                 # dialect opening, transactions, migrations
├── guest/                    # polling, devcontainer work, activity, self-update
├── identity/                 # managed Dex and external reauthorization
├── operation/                # journal, leases, steps, reconciliation scheduler
├── provider/                 # provider contract, capability and inventory types
│   └── utm/                  # local UTM implementation
├── proxy/                    # host grants, HTTP/WebSocket forwarding, tunnels
├── release/                  # platform/guest manifests and compatibility gates
├── secret/                   # three-scope lookup and authorized delivery
├── template/                 # revisions, grants, archival, launch snapshots
├── web/                      # REST/SSE handlers and embedded static UI
└── workspace/                # lifecycle policy, capacity, retention, maintenance

migrations/
├── sqlite/                   # embedded ShiftBox application migrations
└── postgres/                 # embedded ShiftBox application migrations

ui/
├── app/                      # statically exportable routes and layouts
├── components/               # Mantine workflow and domain components
├── features/                 # setup, workspaces, templates, admin, recovery
├── lib/                      # API client, query keys, events, formatting
├── public/brand/             # generated/copy-only approved brand assets
└── tests/                    # Jest/RTL units and Playwright browser flows

platform/
├── packer/                   # pinned Ubuntu/UTM image definitions
├── guest/                    # supervisor, repair, enrollment, retained mounts
└── manifests/                # artifact and guest-release schemas/examples

tests/
├── acceptance/               # cross-package controller and guest journeys
├── contract/                 # store/provider/guest/proxy contract suites
├── integration/              # SQLite, PostgreSQL, Dex, OAuth, backup, browser
└── release/                  # opt-in exact UTM/Packer/VM release gates
```

**Structure Decision**: Use one Go module and one repository organized by product responsibility. The controller and guest have separate commands but share narrowly scoped internal contracts. The UI and platform pipeline remain build inputs to the Go release, not production services. Packages are split only at ownership or trust boundaries; no generic `util`, `common`, infrastructure abstraction layer, or public plugin SDK is introduced.

## Complexity Tracking

No constitution violations require justification.
