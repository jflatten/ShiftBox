# ShiftBox MVP Research

This phase resolves the technical choices left open by the confirmed architecture. Versions are the planning baseline on 2026-11-13; implementation commits exact direct and transitive versions in module and package lockfiles and reruns compatibility gates before release.

## Go and process baseline

**Decision**: Use Go 1.27.4 for the native controller and Linux amd64 guest service. Keep one Go module, separate `shiftbox` and `shiftbox-agent` commands, and supervise pinned Dex 2.46.1 as a product-managed child process.

**Rationale**: Go 1.27.4 is the current security-patched stable toolchain and matches the architecture's native delivery and shared service-language decision. A separate Dex executable preserves Dex's supported configuration, migrations, and gRPC API instead of importing server internals, while still presenting one ShiftBox product lifecycle.

**Alternatives considered**:

- Importing Dex server packages: rejected because its server internals are not a stable embedding contract.
- Reimplementing local credentials/OIDC: rejected because Dex owns authentication in the confirmed design.
- Multiple Go modules: rejected because controller and guest share release/protocol types and do not yet need independent dependency graphs.

**Sources**: [Go release history](https://go.dev/doc/devel/release), [Dex 2.46.1 release](https://github.com/dexidp/dex/releases/tag/v2.46.1), [Dex API](https://dexidp.io/docs/configuration/api/)

## HTTP routing and management API

**Decision**: Use Go's standard `net/http` router and middleware with a versioned JSON REST API under `/api/v1`. Use server-sent events for durable operation updates and standard HTTP/WebSocket reverse proxying for IDE and preview traffic.

**Rationale**: Go 1.27's method-aware `ServeMux` is sufficient for a bounded API and avoids a router dependency. Server-sent events match one-way, ordered, reconnectable controller progress. IDE traffic still requires bidirectional WebSocket support and is handled by the proxy rather than the management event channel.

**Alternatives considered**:

- GraphQL: rejected because the product has command-oriented workflows and no requirement for arbitrary client-shaped queries.
- WebSockets for all live UI state: rejected because operation events are one-way and SSE has simpler reconnection semantics.
- Production Next.js API routes/server actions: rejected because the confirmed runtime has no Node server.

## UI runtime and component selection

**Decision**: Build with Node.js 26 LTS, npm lockfiles, Next.js 16 Active LTS static export, React 19.3, TypeScript 5.x, Mantine 9.x, and TanStack Query 5.x. Use Mantine AppShell, Stepper, Form, Modal/modals, Alert, Table, Tabs, Badge, Progress, and Notifications. Serve the exported assets from Go.

**Rationale**: These are supported stable lines on the planning date. Static export enforces the architecture's no-Node-runtime boundary. Mantine Stepper has keyboard navigation and labeled button semantics; AppShell supplies the responsive management frame; Modal supplies focus trapping and safe initial focus. TanStack Query provides explicit client server-state and targeted refresh for a data-heavy static application.

**Alternatives considered**:

- Raw `fetch` state per component: rejected because duplicated caching, cancellation, error, and invalidation logic would grow across the management application.
- Persisted/offline query cache: rejected because the management UI must not present stale authority or retain sensitive management state in browser storage.
- A custom component system: rejected because Mantine and the ShiftBox theme are confirmed architecture.

**Sources**: [Node.js 26 LTS](https://nodejs.org/en/blog/release/v26.13.0), [Next.js support policy](https://nextjs.org/support-policy), [Next.js static export](https://nextjs.org/docs/app/guides/static-exports), [React versions](https://react.dev/versions), [Mantine Next.js guide](https://mantine.dev/guides/next/), [Mantine Stepper](https://mantine.dev/core/stepper)

## Browser state and live workflows

**Decision**: Use online-only TanStack Query caches with hierarchical key factories, explicit stale times by resource volatility, targeted invalidation, and no persistence. Apply SSE events to the operation/detail cache, then refetch authoritative state after reconnect or sequence gaps.

**Rationale**: The UI remains responsive without turning browser state into authority. Operation events carry monotonic event IDs and resource versions, so replay and gap recovery are explicit. Mutations create durable server operations and never remain queued in the browser while offline.

**Alternatives considered**:

- Optimistically committing lifecycle/destructive mutations: rejected because provider effects are asynchronous and uncertain.
- Persisting cache to local storage: rejected because sessions, authorization, and operational state must be revalidated after reload.
- Polling every screen: rejected because it adds redundant load and delays visible workflow steps.

## Database access and migrations

**Decision**: Use `database/sql` as the narrow application store interface, `pgx/v5/stdlib` 5.13.x for PostgreSQL, `modernc.org/sqlite` 1.62.x for pure-Go SQLite, handwritten SQL repositories, and embedded Goose 3.30.x migrations with dialect-specific files only where necessary.

**Rationale**: Both profiles share transaction semantics without hiding database behavior behind an ORM. Pure-Go SQLite keeps native distribution independent of CGO. Goose supports embedded SQL and both selected databases. PostgreSQL-specific setup, scoped schemas, search paths, and backup behavior remain explicit integration code.

**Alternatives considered**:

- ORM: rejected because lifecycle transactions, leases, reservations, and dialect setup need visible SQL and exact locking behavior.
- PostgreSQL-native pgx APIs everywhere: rejected because the demo profile also needs SQLite and the common subset fits `database/sql`.
- Custom migration runner: rejected because migration ordering, locking, and status are solved problems and a small established dependency is safer.

**Sources**: [pgx v5](https://pkg.go.dev/github.com/jackc/pgx/v5), [modernc SQLite](https://pkg.go.dev/modernc.org/sqlite), [Goose](https://pkg.go.dev/github.com/pressly/goose/v3)

## Durable reconciliation

**Decision**: Store desired state, workspace generation, operation identity, ordered steps, reservations, and a renewable worker lease in the application database before external effects. Workers reconcile provider inventory or guest-reported evidence before issuing or retrying a command.

**Rationale**: Database transactions cannot include UTM, guest, GitHub, or identity-provider effects. Durable intent plus observed evidence makes timeout and restart behavior safe and supports real-time workflow display from the same journal.

**Alternatives considered**:

- In-memory job queue: rejected because controller restarts would lose authority and progress.
- Message broker: rejected because one controller does not need an additional service and the database already owns durable coordination.
- Treating timeouts as failures: rejected because a timeout does not prove the provider omitted an effect.

## UTM provider integration

**Decision**: Define a small compiled provider interface around inspect, create, start, stop, delete, network capabilities, and stopped-VM enrollment delivery. Implement UTM through its supported local scripting interface, preserving and comparing the complete owned-resource inventory on every mutation.

**Rationale**: The provider owns VM truth. A consumer-defined interface allows fake and acceptance implementations without promising runtime plugins. Full inventory comparison prevents partial drive-list updates from detaching persistent disks.

**Alternatives considered**:

- `utmctl attach` for recovery media: rejected because the documented command concerns serial attachment rather than seed-media replacement.
- Direct manipulation of UTM configuration files: rejected because it bypasses supported persistence and ownership behavior.
- Generic remote provider protocol: deferred with remote workers and EC2.

## Guest polling and reverse tunnel

**Decision**: Use a dedicated internal TLS listener with per-VM mTLS. The guest polls desired operations over HTTPS and establishes one guest-initiated TCP/TLS connection multiplexed with Yamux 0.1.2. Each proxied HTTP or WebSocket request receives an independent stream.

**Rationale**: Polling keeps controller intent durable and avoids a general remote shell. A guest-initiated tunnel works without public VM ports. Yamux provides bounded stream multiplexing, half-close behavior, and cancellation over standard connections while allowing Go's HTTP reverse proxy to handle application semantics.

**Alternatives considered**:

- One tunnel TCP connection per browser request: rejected because it increases reconnect and authentication overhead.
- Custom multiplexing: rejected because framing, flow control, and half-close behavior are subtle.
- HTTP/2 CONNECT as the first implementation: deferred because Go's low-level extended-connect and reverse-dial integration would add complexity without a product benefit.

**Sources**: [Yamux releases](https://github.com/hashicorp/yamux/releases)

## Identity, sessions, and CSRF

**Decision**: Manage local credentials through Dex's authenticated API and authenticate ShiftBox itself as an OIDC relying party with `go-oidc` 3.21.x. Store only digests of random opaque ShiftBox sessions. Use Secure, HttpOnly, host-only, SameSite=Strict cookies, exact Origin checks, and a session-bound CSRF token for mutations.

**Rationale**: Authentication and application authorization remain separate. Server-side opaque sessions support immediate revocation and restricted bootstrap state. Host-only cookies plus distinct origins keep management authority out of previews; CSRF tokens and Origin checks avoid relying on SameSite alone.

**Alternatives considered**:

- Browser-held JWT sessions: rejected because revocation and recovery gating would require an additional denylist and risk stale authority.
- Email-based automatic identity merging: rejected by the product contract.
- Sharing management cookies across subdomains: rejected because sibling guest origins are untrusted.

**Sources**: [go-oidc](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc), [Dex local connector](https://dexidp.io/docs/connectors/local/)

## Application encryption and certificate authority

**Decision**: Encrypt application secrets and OAuth token material with AES-256-GCM using a random nonce and contextual additional authenticated data containing key version, record type, owner/scope, record ID, and logical name. Use a protected 32-byte installation key file. Use an installation ECDSA P-256 CA and fresh ECDSA leaf keys for internal mTLS.

**Rationale**: Both constructions are available in Go's standard library, have broad platform support, and avoid inventing formats. Context binding prevents ciphertext movement between records. Key versioning preserves future migration metadata even though rotation is deferred.

**Alternatives considered**:

- SQLCipher: rejected because only protected values, not the entire SQLite database, are in the accepted contract.
- Mac Keychain-only custody: rejected because controller portability is required.
- CA signing material in guests: rejected because the whole guest is untrusted.

## Backup and restore format

**Decision**: Stream a canonical versioned manifest, scoped database dumps, configuration, installation keys, CA/OIDC material, and checksums into passphrase-based age 1.3.x encryption. Keep the password out of persistent state. Use database-native consistent backup mechanisms and validate the whole archive before activation.

**Rationale**: Age provides authenticated streaming passphrase encryption and stable v1 decryption compatibility. Native SQLite/PostgreSQL backup tools preserve database consistency. A manifest makes format, release, schema, and key compatibility reviewable before mutation.

**Alternatives considered**:

- Plain archive plus external encryption instruction: rejected because built-in encryption is required.
- Copying live database files: rejected because it cannot establish a coordinated checkpoint.
- Backing up VM disks: explicitly outside the management-backup contract.

**Sources**: [age package](https://pkg.go.dev/filippo.io/age), [PostgreSQL pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html)

## API contract and identifiers

**Decision**: Use OpenAPI 3.1 as the reviewed management API contract, JSON with RFC 3339 UTC timestamps, opaque UUID identifiers, resource `version` fields for conditional mutation, and stable machine-readable error codes. Keep Go transport/domain mapping explicit and generate only TypeScript contract types during the UI build.

**Rationale**: The API is external to the static UI and benefits from a language-neutral contract. Conditional versions prevent stale browser actions. Avoiding generated Go handlers keeps authorization and transaction boundaries visible; generating UI types reduces drift without creating a runtime dependency.

**Alternatives considered**:

- Sharing handwritten TypeScript types with Go structs: rejected because no compiler verifies cross-language drift.
- Full server/client generation: rejected because generated handler abstractions would obscure authorization and error translation at the main trust boundary.

## Testing strategy

**Decision**: Use Go's standard test runner with `go-cmp`, fakes at consumer-owned interfaces, real HTTP/mTLS transports, shared store/provider acceptance suites, and race testing. Use Jest/React Testing Library for focused UI behavior and Playwright for browser, origins, keyboard, focus, and end-to-end workflows. Keep exact UTM/Packer/VM gates opt-in and release-blocking.

**Rationale**: The constitution requires tests first, comments on every test, and realistic failures. Standard tools minimize dependencies; real transports catch contract drift; exact-stack tests are necessary because fake providers cannot establish UTM support.

**Alternatives considered**:

- Mock-heavy unit tests: rejected because they duplicate implementations and miss protocol behavior.
- Snapshot-first UI tests: rejected because accessible role and user-action assertions better express requirements.
- Running UTM tests in the ordinary unit suite: rejected because they are host-specific, slow, and need controlled destructive fixtures.

**Sources**: [Next.js testing guide](https://nextjs.org/docs/app/guides/testing), [Playwright installation and supported platforms](https://playwright.dev/docs/intro)

## Resolved unknowns

All Technical Context items are resolved for planning. Exact patch pins, browser-version matrix, selected PostgreSQL major versions, UTM revision, Packer fork commit, Ubuntu image digest, and browser IDE distribution are release-manifest inputs. They do not change the design and must be fixed and validated before a supported release rather than treated as unresolved planning decisions.
