# Feature Specification: First Public AWS Distribution

**Feature Branch**: Worktree based on `feature/mvp` at `48f4d92`

**Created**: 2026-11-21

**Status**: Draft

**Input**: Ship a supported, prebuilt AWS distribution that a new operator can install privately in their own account. Publish a public candidate, qualify its exact artifacts, and promote those artifacts to stable.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Install privately and open a workspace (Priority: P1)

A new operator with authenticated AWS access opens a regional AWS Console launch link or uses the guided CLI to install ShiftBox in their account, completes the protected browser setup, and opens a working workspace IDE on a private connection. The operator can use the guided installer with their existing tailnet even if they have no domain or TLS certificate. Operators with existing private connectivity can supply their own connection and browser trust.

**Why this priority**: Installation through the first useful workspace is the customer outcome that defines the distribution.

**Independent Test**: Give an operator a versioned release fixture and an eligible AWS account. Use the quick-create link to inspect and deploy the versioned CloudFormation template, then verify protected setup and a successful IDE launch. Exercise the guided CLI and manual template inspection against the same artifacts.

**Acceptance Scenarios**:

1. **Given** an eligible account and a tailnet with no domain or certificate, **when** the operator follows the guided install, trusts their installation's HTTPS issuer once, and signs in after protected setup, **then** they can use the IDE through private DNS and HTTPS without installing Terraform, building artifacts or authenticating to GitHub for downloads.
2. **Given** an operator with Direct Connect or an operator-managed VPN, **when** they configure the documented DNS, routes, access rules and browser trust, **then** the management and IDE origins are reachable through their private connection and unavailable from the public internet.
3. **Given** a released stable version and a newer public candidate, **when** the operator uses the default install, explicitly selects the candidate, or opens a pinned console link or downloads the checked template for manual deployment, **then** each path uses the intended version and its exact regional assets.
4. **Given** rejected changes or an interrupted deployment, **when** the operator exits or resumes, **then** the installer preserves existing state, identifies the next safe step, and obtains fresh approval for any changed plan.
5. **Given** a signed-in administrator, **when** they complete the documented first-workspace setup, **then** the official regional image and required launch configuration are available through the product's authorized controls and remain usable after reload.

### User Story 2 - Publish and qualify an exact release (Priority: P2)

A maintainer makes a public candidate available from a protected version tag, validates it using a separate AWS account, and approves stable by pushing a protected stable tag. A release consumer can inspect the release notes and authoritative artifact identities.

**Why this priority**: Stable support depends on evidence for the same payload customers will install.

**Independent Test**: Exercise an RC and promotion in a rehearsal repository and approved disposable AWS environments. Check that a valid candidate can qualify and that changed, missing, unverified or mismatched artifacts prevent promotion.

**Acceptance Scenarios**:

1. **Given** reviewed source and a protected RC tag, **when** baseline checks and all four regional image publications succeed, **then** the public prerelease exposes complete, versioned install assets and their provenance.
2. **Given** a public RC, **when** the official image boots from a separate account in each supported region and a fresh operator completes the full private install through the IDE in Ohio, **then** the results are recorded against the exact candidate and its artifacts.
3. **Given** a qualified RC and a stable tag on its source commit, **when** promotion independently verifies its evidence and public assets, **then** stable offers the identical controller digest, CloudFormation template bytes and four regional image IDs.
4. **Given** an invalid or unauthorized tag, missing evidence, incomplete regions, mismatched payload or interrupted publication, **when** publication is attempted or retried, **then** stable stays at its previous eligible release and a retry only completes or accepts an identical publication.
5. **Given** a commit, branch push or pull request, **when** normal CI completes, **then** it leaves release payloads and default stable selection unchanged.

### User Story 3 - Find the right public instructions (Priority: P3)

An operator arrives from the repository or a release page and finds setup, first workspace, connectivity and troubleshooting instructions for the intended version. A contributor can build the reviewed source on main.

**Why this priority**: Users need an entry point that leads directly to the supported install and its requirements.

**Independent Test**: Render the public site with stable, RC and pre-stable fixtures. Follow links from the repository and release notes, resolve each install route, and confirm a documentation update leaves release artifacts and stable selection unchanged.

**Acceptance Scenarios**:

1. **Given** a stable release and public RCs, **when** an operator opens the site, **then** the default instructions point to stable and the explicit release references identify the selected version and its regional console launch links.
2. **Given** a documentation-only change, **when** the site updates, **then** published packages, release versions and default stable selection remain unchanged.
3. **Given** a new contributor or a private connectivity failure, **when** they follow the relevant guide, **then** they can find source build instructions or an actionable diagnosis for their install, with private operator records kept out of public content.

### User Story 4 - Plan a safe upgrade or recovery (Priority: P4)

An existing operator can identify what to preserve, which checks to run before updating, and how to recover if an irreversible migration fails.

**Why this priority**: Operators need a recovery path before committing their data to the first supported release.

**Independent Test**: Walk the published procedure against existing backup and recovery validation for the release and review the first-release guidance for future transitions.

**Acceptance Scenarios**:

1. **Given** an existing deployment, **when** an operator prepares a change, **then** the guide identifies how to confirm compatibility and retained-workspace readiness, verify a coordinated backup, and review the plan against the existing installation.
2. **Given** a failed irreversible migration or restored installation, **when** the operator follows the recovery guide, **then** they can identify the matching release and archive and the administrator review and reconciliation steps before access resumes.
3. **Given** the first public stable release, **when** it is qualified, **then** the existing release recovery gates still apply and the guide correctly describes which public-to-public upgrade can be tested once a prior public stable exists.

### Edge Cases

- No public stable exists, a selected version is a draft or missing, or a release asset fails integrity checks.
- AWS credentials expire, resolve to a different account, or lack a required operation. An unsupported region or wrong publisher/image pair blocks installation before infrastructure changes.
- A mismatched S3 version or quick-create region blocks launch. A private DNS, routing, outbound registry or certificate problem blocks the next step with a diagnosis and leaves the installation private.
- A run loses the result of an AWS action or finds an existing setup task, volume or installation. The operator can reconcile before proceeding.
- An RC has publicly visible assets from a failed run, a stable tag refers to another commit, or multiple RCs share a commit. Promotion needs one explicitly identified, qualified RC and verifies the exact payload.
- An image loses public launch access or has reached deprecation before promotion. Qualification evidence alone cannot substitute for checking current availability.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: A versioned GitHub Release MUST be the canonical entry for install artifacts, release notes and instructions. It MUST expose a digest-pinned, anonymously pullable full controller on public GHCR and a downloadable, checksummed CloudFormation template matching the hosted launch template.
- **FR-002**: The manifest MUST identify one official publisher-owned workspace AMI for each of `us-east-1`, `us-east-2`, `us-west-1` and `us-west-2`. Deployment MUST select the entry matching the configured region and reject unavailable, mismatched or unverifiable images.
- **FR-003**: Customer deployment MUST use CloudFormation to launch the existing persistent AWS topology: ECS services for ShiftBox and PostgreSQL, one EC2 VM per workspace, and a protected ECS/SSM browser setup. The publisher MUST host identical template bytes at immutable, version-specific S3 locations. Anonymous access MUST be limited to published launch artifacts. Keep public AMI contents reusable and deployed data encrypted.
- **FR-004**: Operators MUST be able to inspect and launch the exact release through regional CloudFormation Console quick-create links in GitHub Releases and Pages. They MUST also be able to pipe the published Bash installer from curl, choose an explicit public RC or stable, or download and inspect the script or template for manual deployment. Only an eligible public stable can resolve by default.
- **FR-005**: The guided CLI MUST preflight tools, AWS identity, account and region, show CloudFormation validation and the exact change set, obtain explicit approval before execution, and resume the same stack safely. Both install paths MUST present required IAM acknowledgement, protect persistent storage, stage controller startup behind the protected browser setup, and keep secret values out of stack inputs, outputs and logs. Review change sets before stack updates.
- **FR-006**: Public CloudFormation deployment MUST use existing service and workspace subnets in an operator-selected VPC and preserve the service subnet's Availability Zone for the retained EBS volume. The operator MUST own routes, internet egress, DNS forwarding and any Tailscale, VPN or Direct Connect infrastructure. The deployment MUST keep its application security groups and installation-specific DNS scoped to the selected VPC and leave browser ingress closed unless the operator specifies an approved source. Document private access and verified browser trust without requiring a registered domain or pre-existing certificate. Browser HTTPS, database TLS and internal guest trust MUST remain distinct.
- **FR-007**: The guided first-workspace path MUST give the authorized operator discoverable setup instructions and controls to make the exact regional image and required workspace configuration usable. Existing image validation, enrollment, permissions and save/reload behavior MUST apply.
- **FR-008**: Package publication MUST be triggered only by protected maintainer version tags. A valid RC tag MUST publish a public prerelease after baseline checks. Branches and pull requests MUST retain normal CI. Reviewed buildable source MUST be available on main by launch with contributor instructions.
- **FR-009**: Stable tag creation MUST serve as maintainer approval. The workflow MUST independently resolve the tag commit and one qualified public RC, verify its evidence and live artifact identities, and reuse the controller digest, CloudFormation template bytes, version-specific regional URLs and four AMI IDs without rebuilding. A changed payload MUST return through a new RC.
- **FR-010**: Publication MUST reject invalid or unauthorized tags, missing or mismatched evidence, and partial region coverage. Interrupted or duplicate publication MUST preserve stable selection until a complete release is visible and ensure a version cannot point to two payloads.
- **FR-011**: Before stable promotion, qualification MUST record successful cross-account boots of the exact public AMI and successful hosted-template validation in all four regions, and a full fresh-account CloudFormation Console install to the browser IDE in `us-east-2`. Qualify the guided CLI and existing product gates, including failure paths and disposable stack cleanup. The private AWS demo serves as the baseline and shared testbed. Public qualification requires separate evidence.
- **FR-012**: Public GitHub Pages documentation MUST cover install and first workspace, regional quick-create links, private connectivity and browser trust, troubleshooting, upgrades and recovery, and contributing/building main. Stable and explicit version links MUST agree with the respective GitHub Releases. Documentation publication MUST be independent of package publication and protect private evidence and operator details.
- **FR-013**: Publish operator-driven upgrade and recovery instructions covering persistent state, compatibility, all-retained-workspaces-current checks, verified coordinated backup before irreversible changes, matching-release recovery and administrator review. Existing recovery gates apply to the first stable. A public-to-public upgrade rehearsal requires a prior public stable.
- **FR-014**: After one-time publisher setup, the protected release workflow MUST provision and destroy its disposable Ohio Packer network through Terraform. The workflow MUST use run-scoped authority and ephemeral SSH credentials. Terraform state MUST persist in a dedicated, encrypted, private, locked and versioned S3 backend in the production publisher account. Successful and failed builds MUST tear down their network, with independent recovery after cancellation or runner loss. Teardown MUST preserve release AMIs and snapshots, and no build network should remain between releases.

### Key Entities

- **Public release** — version, channel, source commit and canonical release assets.
- **Distribution manifest** — exact controller and CloudFormation template identities, publisher identity, four hosted template locations and four workspace image entries.
- **Qualification record** — candidate identity, verified artifact identities, regional launch results, fresh-install result, release gate evidence and cleanup disposition.
- **Deployment state** — operator-owned account, region, deployment identity, selected payload, stack lifecycle, and retained resources.
- **Installation trust material** — browser trust scoped to one installation, separately managed from database TLS and guest identity.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: One fresh Ohio account reaches a working signed-in private browser IDE from the candidate console quick-create link, with zero local builds, zero customer Terraform steps and zero GitHub sign-ins for artifact retrieval.
- **SC-002**: Four of four supported regions successfully launch the listed official workspace image from a separate customer-like account, with expected owner and encrypted launched storage verified.
- **SC-003**: Stable promotion matches the controller digest, template checksum and four AMI IDs from one qualified candidate, with all four versioned template downloads matching the inspection copy. Every exercised negative promotion case preserves previous default stable selection or its absence.
- **SC-004**: All four regional console links resolve the intended version and region. Default CLI, explicit RC and downloaded manual paths each resolve the intended artifacts. A no-stable fixture presents an actionable outcome without selecting a prerelease.
- **SC-005**: One no-domain tailnet install and one existing-private-route validation demonstrate private DNS, verified browser trust, successful sign-in and no public browser access.
- **SC-006**: An interrupted CLI or console sequence resumes on the same stack and retains its persistent resources. Rejected changes and wrong account, region, image or template perform zero unintended changes.
- **SC-007**: The public site exposes all five operator and contributor guide areas and resolves repository, stable and explicit version references. One docs-only publication rehearsal changes zero release payloads.
- **SC-008**: Every applicable existing release gate is linked to passing evidence for the qualified distribution before promotion, and disposable account inventory confirms stack, retained test storage and workspace resource cleanup.

## Assumptions

- AWS-first is the approved public distribution sequencing. Existing UTM capability and the underlying product security and data contracts continue.
- The user approved CloudFormation for customer stacks. Demo and publisher infrastructure continue to use existing internal Terraform practices.
- The one-controller persistent topology is the baseline. An operator supplies legitimate AWS access, deployment capacity and either a tailnet or an existing private connection. The install must surface the current database TLS, credentials, host image, setup permissions and catalog prerequisites.
- The team will verify the official publisher AWS account, GitHub package and version-tag controls, and CI permissions before enabling publication. The demo account is a separate operational dependency.
- Unit tests with descriptive comments precede each implementation change. Existing AWS and product acceptance checks are reused for release qualification.
- This work produces the specification and design through task generation. The launch itself requires subsequent implementation and operations work.
