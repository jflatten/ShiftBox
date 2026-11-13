# Technology decisions

**Status:** Architecture dependency register for the greenfield ShiftBox project. Based on accepted decisions Q1–Q155 and the subsequent Mantine selection. This records design choices, not an inventory of installed packages or proof of release compatibility. [Design readback](design-readback.md) · [Full decision record](shiftbox-design-decisions.md)

## Scope and decision status

This document is the central reference for direct product dependencies, build tools, platform prerequisites, external services and protocols. Transitive packages and their exact versions belong in implementation dependency manifests and release inventories once those exist.

- **Selected:** the technology or capability is part of the accepted architecture; exact versions may still require selection and validation.
- **Engineering baseline:** a documented implementation direction within the agreed contract, subject to validation.
- **Candidate / unresolved:** researched or required, but the concrete implementation has not been selected.
- **Phase II:** a future direction, not a phase-one prerequisite.

The detailed architecture contracts remain authoritative. A version mentioned in historical research is not automatically the supported release version.

## Controller and browser application

| Technology | Status | Role and reason for the decision | Deployment / version boundary |
| --- | --- | --- | --- |
| Go | Selected — Q67 | Implements controller and guest services; supports the chosen native delivery and common service-language approach. | Native macOS controller and Linux amd64 guest builds. Toolchain version and native dependency requirements remain to be pinned. |
| Go HTTPS and static asset embedding | Selected — Q106 | Serves the UI and live APIs from the ShiftBox distribution; keeps runtime authentication and authorization in Go. | Next.js assets are built before packaging. A production Node UI server is not required. |
| React | Selected — Q80 | Browser application component model. | Build dependency and shipped browser code; select a version compatible with Next.js and Mantine. |
| TypeScript | Selected — Q80 | Typed browser application implementation. | Build-time toolchain; compiled output is shipped to browsers. |
| Next.js static export | Selected — Q80/Q106 | Produces the React application's static files for Go to serve. | Export-compatible routes and live Go APIs; request-time Next.js server features are outside the deployment contract. Version remains to be pinned. |
| Mantine | Selected — subsequent explicit user decision | Shared UI components and theming for setup, workspace management and administration. | Integrate `MantineProvider` and required package styles. Pin compatible packages; additional Mantine modules are selected as needed, not all required by default. |
| Node.js and frontend package/build tooling | Required tooling; exact versions/tools unresolved | Builds the React/Next.js/TypeScript application. | Build environment dependency. Package-manager selection remains open; it is not an operator requirement for serving the compiled UI. Guest-side Node tooling is listed separately below. |
| Browser | Required client | Runs the management UI, browser IDE and private application previews. | Supported browser/version matrix remains to be validated, including WebSockets, origin isolation and accessibility. |

Decision detail: [UI component architecture](architecture-and-communication.md#ui-component-architecture). Mantine implementation references: [user-supplied documentation](https://mantine.dev/llms-full.txt) and [Next.js integration guide](https://mantine.dev/guides/next/).

## VM platform, containers and developer environment

| Technology | Status | Role and reason for the decision | Deployment / version boundary |
| --- | --- | --- | --- |
| macOS host | Selected phase-one platform — Q19/Q53/Q116 | Runs the controller natively beside UTM. | Logged-in, awake Mac with UTM available; initial operation is manual. Supported macOS versions remain to be defined. |
| UTM | Selected first VM provider — Q17/Q19 | Owns workspace VM execution and infrastructure through the ShiftBox adapter. | Operator-side host prerequisite. Exact supported version and automation capabilities require validation; UTM 5.0.8 was a research reference, not a completed compatibility certification. |
| QEMU through UTM | Selected execution mode — Q37/Q66 | Emulates amd64 guests on the initial ARM64 Mac. | Delivered through the selected UTM stack; ShiftBox is not introducing a separate direct-QEMU provider. |
| Ubuntu 24.04 LTS Minimal amd64 | Selected platform OS — Q66 | Baseline VM image for guest services and development tooling. | Pin exact image revision/digest. Existing workspaces retain their platform image; new images apply to new workspaces. |
| Docker container tooling | Engineering baseline | Supplies the container execution/build environment used by the Dev Container CLI. | Runs inside the platform VM. Pin and validate engine/tool versions and maintenance-start behavior. Host Docker Desktop is not a phase-one ShiftBox requirement. |
| Dev Containers specification (`devcontainer.json`) | Selected — Q47/Q71/Q77 | Defines repository environment configuration, Features, lifecycle hooks and readiness behavior. | One primary image/Dockerfile-based devcontainer initially; platform-managed Compose is deferred. |
| Dev Container CLI | Selected | Executes project-container setup and lifecycle operations under the guest service. | Included in the platform tooling; version and supported configuration coverage require validation. |
| Node.js for guest tooling | Engineering baseline | Supports the selected Dev Container CLI/tooling path. | Guest runtime dependency distinct from frontend build Node.js; versions may differ and must be pinned independently. |
| glibc-based amd64 Linux project containers | Selected compatibility scope — Q71 | Initial project-container userspace supported by the browser workspace design. | Debian/Ubuntu reference cases. This is separate from the Ubuntu platform VM; Alpine/musl support is deferred. |
| VS Code Browser experience | Selected capability — Q23 | Initial interactive IDE accessed through the ShiftBox proxy. | Exact browser IDE distribution, version, extension source and license-compatible packaging remain unresolved. Historical code-server research does not select that distribution. |
| Git / Git HTTPS | Selected integration | Repository operations under the workspace owner's GitHub authority. | Validate the Git client and credential-delivery path used by platform tooling. Other developer tools remain developer-managed. |
| Guest service supervision | Required capability; systemd is a researched candidate | Starts the guest service and supports recovery after service/update failure. | Independent upgrade recovery is required; selecting a supervisor does not by itself implement binary rollback. |
| Boot-time enrollment media / reader | Required capability; delivery implementation under validation | Supplies a fresh per-VM enrollment grant and current trust without embedding reusable credentials in images. | Stopped-VM UTM media replacement is the candidate path. Cloud-init/NoCloud and QEMU guest-agent channels are researched mechanisms, not blanket required dependencies or proven recovery implementations. |

Decision detail: [Workspace lifecycle and providers](workspace-lifecycle-and-providers.md) and [certificates and guest trust](certificates-and-guest-trust.md).

## Image production and release artifacts

| Technology | Status | Role and ownership | Version / validation boundary |
| --- | --- | --- | --- |
| HashiCorp Packer | Selected | Release-time construction of reusable platform artifacts. | Pin the build tool; it does not run for each workspace launch. |
| `naveenrajm7/packer-plugin-utm` compatible fork | Selected starting point / fork direction — Q40–Q41 | UTM image builder maintained as a pinned compatible fork for the release pipeline. | Fork implementation and exact revision remain future work. Prove build, shutdown, export, import and boot with the selected Ubuntu/UTM combination. |
| Versioned platform catalog and release manifests | Selected product mechanism | Records artifact architecture, versions, digests, owned resources and provenance; administrators explicitly import approved artifacts. | First-party ShiftBox functionality, not a selected external artifact-registry service. |
| Authenticated guest update artifacts | Selected contract — Q87 | Supports verified guest self-updates with previous-version recovery. | Signing library/format and release tooling remain implementation choices. N/N−1 product compatibility is required. |
| Mermaid and SVG | Existing documentation formats | Editable diagram sources and rendered architecture illustrations. | Documentation dependencies only; not shipped application services. |

The selected builder source is recorded in [the lifecycle design](workspace-lifecycle-and-providers.md#artifacts-and-runtime-baseline). Image digests and compatibility evidence belong in each release's artifact manifest.

## Identity, data and recovery

| Technology | Status | Role and reason for the decision | Ownership / version boundary |
| --- | --- | --- | --- |
| Embedded/product-managed Dex | Selected — Q68/Q73/Q84 | Owns local credentials and OIDC authentication/brokering; avoids a separately administered identity installation in the default product experience. | ShiftBox manages integration and lifecycle. Exact version, embedding/process arrangement and account-management integration require validation. ShiftBox retains membership/authorization ownership. |
| OpenID Connect (OIDC) | Selected authentication boundary | Connects browser sign-in to Dex and optionally an external identity provider. | Connector support must be validated. External provider operation remains with the organization. |
| SQLite | Selected demo/small-installation profile — Q115/Q117 | Embedded application and Dex state, including restricted first-run bootstrap. | Separate owned stores; driver/backup implementation remains unresolved. Not the selected production profile. |
| PostgreSQL | Selected production profile — Q115/Q117/Q123 | Application and Dex persistence in one database with separate schemas and scoped accounts. | Operator deploys/runs the server and encrypted storage. ShiftBox manages its owned schema/seed operations. Supported server/client versions remain to be pinned. |
| PostgreSQL native dump/restore utilities | Engineering baseline — Q153 | Coordinated management backup and replacement-target restore. | Bundle compatible utilities or validate them as explicit prerequisites. Exact utility packaging remains open. |
| Authenticated application-value encryption | Selected security contract — Q92/Q119/Q148 | Protects application secrets and persisted OAuth material using installation-specific keys outside the database. | Concrete algorithm/library remains to be selected; live Dex records are not implicitly field-encrypted. |
| Protected persistent key file / mounted key file | Selected custody model — Q119/Q134 | Portable installation key storage and recovery. | Product-managed persistent file or operator-supplied mount; mandatory macOS Keychain or external KMS is not assumed. |
| Password-encrypted management archive | Selected capability — Q69/Q75 | Independently recoverable backup of owned controller/identity state, configuration and keys. | Excludes VM disks. Archive format is versioned; the operator retains the password separately. |
| Go `age` library | Engineering proposal | Candidate authenticated streaming archive encryption implementation. | Not a finalized dependency/version. Validate archive authentication, passphrase handling and restore before selection. |

Decision detail: [Identity and access](identity-access-and-browser-security.md) and [operations, state and recovery](operations-state-and-recovery.md).

## External integrations and communication standards

| Technology / protocol | Status | Dependency contract |
| --- | --- | --- |
| GitHub.com GitHub App and user OAuth | Selected phase-one Git integration — Q93/Q107–Q110/Q148 | Administrator owns app registration/installation. Expose owner-authorized user access tokens; encrypted access/refresh persistence and renewal stay in the controller. This does not add product PR management or a managed GitHub CLI. |
| HTTPS, TLS and X.509 certificates | Selected | Browser traffic uses uploaded certificates; internal guest traffic uses a separate installation CA and per-VM mTLS. Leaf/key rotation every 4h, validity 8h; revoked identities do not automatically recover. |
| HTTP and WebSocket browser proxying | Selected access requirement | Carries authenticated IDE/preview traffic through the proxy. Guest-originated tunnels are required; tunnel framing, multiplexing and library selection remain open. |
| DNS and distinct browser origins | Selected operator configuration | Supports management, workspace IDE and preview names. Operator supplies naming and valid web certificates; no public DNS vendor or ACME service has been selected. |
| Destination CIDR / TCP / UDP / port policy | Selected network policy model | Provider adapter exposes only enforceable restrictions and required exceptions. No particular firewall product is selected. |
| AWS EC2 | Phase II — Q17/Q116 | Second VM provider; Linux/container-hosted production controllers arrive in this phase. AWS SDK, AMI build details and infrastructure dependencies remain future implementation decisions. |

Decision detail: [Communication paths](architecture-and-communication.md#communication-paths), [Git integration](templates-secrets-and-git.md) and [certificate lifecycle](certificates-and-guest-trust.md).

## Choices still to record

The following are deliberately unresolved, rather than implicit technology decisions:

- Go HTTP routing, database drivers/query layer, schema migration tooling, OAuth/OIDC client integration, tunnel libraries and concrete cryptographic implementations.
- Browser IDE distribution and packaging, frontend package manager, optional Mantine modules, icon library and application-specific styling/build additions.
- Guest supervisor/update helper implementation, boot-time recovery reader, final UTM seed delivery and cloud-init/QEMU guest-agent involvement.
- Test frameworks, CI service, release signing tools and dependency inventory tooling.
- Exact supported versions and platform matrix for all selected technologies except the explicitly chosen Ubuntu 24.04 LTS Minimal amd64 baseline.

ShiftBox's reconciler, provider adapter, proxy, permission checks, template/secret services and guest agent are first-party application components. Their presence does not imply an external scheduler, message broker, authorization engine, secrets server or reverse-proxy product.

## Superseded choices and scope exclusions

Kubernetes is not the selected workspace substrate. The initial Alpine platform proposal was superseded by Ubuntu Minimal. A separately managed Keycloak installation was superseded by product-managed Dex. Mantine settles the earlier open UI-library choice; the reference CloudBox UI stack is not inherited by this greenfield design.

SPIRE, Vault and Smallstep appear as research precedents, not selected services. SQLCipher is not selected by the application-value encryption decision. A production Next.js/Node UI server, host Docker Desktop dependency, platform-managed Compose, other IDE integrations and mandatory external KMS are not part of the phase-one stack. Tools developers independently add inside their containers are outside this platform dependency register.

## Maintaining this register

When a choice is finalized, update its status, rationale, exact version or source revision, execution location and linked decision. Record compatibility evidence against the [release validation gates](validation-and-engineering-defaults.md#mandatory-release-validation), especially UTM/Packer, Dex/database integration, browser export and guest recovery. Retain superseded decisions for provenance.

Implementation manifests and release inventories should enumerate actual direct/transitive versions, artifact digests and required license notices. This register explains why a dependency exists; it does not replace those inventories. Changes that alter supported scope, operator burden, trust or data preservation return for a product decision under the existing design authority.
