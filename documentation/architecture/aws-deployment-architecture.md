# ShiftBox AWS deployment architecture

The release maintainer builds and publishes a candidate from GitHub Actions using the ShiftBox publisher account. A separate AWS testing account qualifies that exact candidate. Customers then deploy the pinned release into their own AWS accounts using CloudFormation and reach ShiftBox over a private connection.

This is the public AWS distribution design for review. The hosted Packer network and tunnel are a proposed change. The four publisher template buckets are private, qualification is disabled, and no public candidate has been published.

## Release system

![Components, artifact flow, and account boundaries for the ShiftBox public AWS release](diagrams/aws-release-system.svg)

[Open full-size diagram](diagrams/aws-release-system.svg)

<details>
<summary>Mermaid source</summary>

```mermaid
%% Maintainer actions, release artifacts, and AWS account boundaries
flowchart TB
    accTitle: ShiftBox public AWS release system
    accDescr: Protected tags start a publisher workflow that builds and pins artifacts. Independent testing qualifies an exact candidate and returns evidence for stable promotion.
    Maintainer["Release maintainer"]
    subgraph GitHub["GitHub repository and public services"]
        Checks["Checks<br/>Exact source commit"]
        ReleaseWorkflow["Release workflow<br/>Protected RC or stable job"]
        Release["GitHub Releases<br/>Immutable assets and manifest"]
        Registry["Public GHCR<br/>Controller OCI digest"]
        Pages["GitHub Pages<br/>Docs and regional launch links"]
        Qualification["Qualification workflow<br/>Explicit approval and dispatch"]
        Evidence["Reviewed main<br/>Exact RC qualification record"]
    end

    subgraph Publisher["AWS publisher account"]
        Lease["Ohio build network<br/>Terraform, NAT, expiry cleanup"]
        Endpoint["EC2 Instance Connect Endpoint<br/>IAM-scoped SSH over TCP 22"]
        Guest["Private Packer guest<br/>Builds Ohio AMI"]
        Amis["Publisher-owned workspace AMIs<br/>Four supported regions"]
        Buckets["Regional S3 template buckets<br/>Exact object versions"]
    end

    subgraph Testing["Independent AWS testing account"]
        Tests["Disposable qualification<br/>Four AMI probes and Ohio install"]
    end

    Maintainer -->|"Protected version tag"| ReleaseWorkflow
    Checks -.->|"Required passing result"| ReleaseWorkflow
    Maintainer -->|"Review build lease and IAM"| Lease
    Lease -.->|"Provision endpoint"| Endpoint
    ReleaseWorkflow -->|"Hosted Packer via scoped OpenTunnel"| Endpoint
    Endpoint -->|"SSH to private builder"| Guest
    Guest -->|"Verified Ohio artifact"| Amis
    ReleaseWorkflow -->|"Separate copy and share approvals"| Amis
    ReleaseWorkflow -->|"Stage exact template bytes"| Buckets
    ReleaseWorkflow -->|"Build or verify exact digest"| Registry
    ReleaseWorkflow -->|"Publish release assets"| Release
    Pages -->|"Read canonical release and manifest"| Release
    Release -->|"Select exact public RC"| Qualification
    Qualification -->|"Scoped deploy and cleanup roles"| Tests
    Tests -->|"Launch exact regional images"| Amis
    Tests -->|"Fetch pinned Ohio template"| Buckets
    Tests -->|"Evidence review"| Evidence
    Evidence -.->|"Stable promotion gate"| ReleaseWorkflow
```

</details>

| Boundary | Responsibility |
| --- | --- |
| GitHub repository | Normal pushes and pull requests run Checks. A protected version tag starts release preflight, which verifies the maintainer, tag, source commit, and passing Checks before granting write credentials. GitHub Pages publishes documentation from reviewed main through a separate workflow. |
| Publisher build in Ohio (`us-east-2`) | A standard GitHub-hosted job assumes the build role through GitHub OIDC. Packer launches one private EC2 guest. Packer's local SOCKS bridge opens an IAM-scoped EC2 Instance Connect tunnel to that guest on TCP 22. A temporary NAT gateway provides package egress. An attempt-scoped Scheduler and Lambda provide expiry cleanup, and the operator reconciles the Terraform state afterward. |
| Publisher artifacts in four regions | Separate approved roles build, copy, and share the workspace AMIs. A separate template role stages identical CloudFormation bytes in the versioned S3 buckets in `us-east-1`, `us-east-2`, `us-west-1`, and `us-west-2`. The planned public S3 policy grants read access only to published launch artifacts. The AMI backing snapshots are shareable. Customer-launched storage is encrypted in the customer account. |
| Public distribution | GitHub Releases holds the Bash installer, inspectable CloudFormation YAML, support archive, and distribution manifest. The manifest binds the controller's public GHCR digest, each regional AMI and publisher owner, each exact S3 object version, and the regional console launch links. Pages points readers to the canonical release and its launch links. |
| Independent qualification | A maintainer dispatches the workflow for an exact public RC. Separate deployment and cleanup roles operate on run-owned resources in the testing account. Cross-account launches check all four AMIs. A staffed, fresh Ohio console installation verifies the private browser path through the IDE. Teardown is verified before the evidence is approved on main. A separate cleanup path covers cancelled or lost runs when explicitly enabled. |

The publisher also fences the build by account, region, attempt tags, reviewed subnet, endpoint, and a temporary SSH key. The key's public half is imported into EC2. The private half comes from the protected GitHub environment and is removed from the job's temporary directory on exit. The guest receives no instance profile and accepts SSH only from the endpoint's security group. [Publisher build plan](../../platform/packer/aws/publisher-runner-plan.md) · [Release workflow](../../.github/workflows/release.yml) · [Qualification runbook](../../platform/qualification/README.md)

## Release sequence

![Maintainer approvals, private AMI build, candidate publication, independent testing, and stable promotion](diagrams/aws-release-sequence.svg)

[Open full-size diagram](diagrams/aws-release-sequence.svg)

<details>
<summary>Mermaid source</summary>

```mermaid
%% Protected candidate build, qualification, and stable promotion
sequenceDiagram
    accTitle: Release candidate to stable
    accDescr: The maintainer approves each AMI phase. An independent testing account qualifies the immutable release candidate before a stable tag can promote the same artifacts.
    actor Maintainer as Release maintainer
    participant Actions as Release Actions
    participant Publisher as AWS publisher
    participant Public as GitHub release and GHCR
    participant Testing as AWS qualification
    participant Main as Reviewed main

    Maintainer->>Actions: Push protected RC tag
    Actions->>Main: Verify peeled commit and passing Checks
    Actions->>Public: Reserve candidate draft
    Maintainer->>Publisher: Review Ohio network lease, build role and key
    Maintainer->>Actions: Approve private AMI build
    Actions->>Publisher: Assume build role by OIDC, then tunnel SSH via EICE
    Publisher-->>Actions: Sealed Ohio AMI and verified Packer manifest
    Note over Actions,Publisher: Freeze build handoff. Reconcile and tear down build network.
    Maintainer->>Actions: Approve exact Ohio AMI for copy
    Actions->>Publisher: Copy and verify private AMIs in the other three regions
    Publisher-->>Actions: Freeze four AMI IDs and catalogs
    Maintainer->>Actions: Approve exact regional AMIs for sharing
    Actions->>Publisher: DryRun every remaining share, then share and verify
    Actions->>Public: Stage controller at exact GHCR digest
    Actions->>Publisher: Validate and stage identical template bytes in regional S3
    Actions->>Public: Verify anonymous retrieval and publish immutable RC

    Maintainer->>Testing: Dispatch and approve qualification of this exact RC
    Testing->>Public: Verify immutable release assets and digest
    Testing->>Publisher: Consume exact public S3 versions and AMIs
    Testing->>Testing: Four regional boots, staffed Ohio install to browser IDE, cleanup
    Testing-->>Main: Submit exact-RC evidence for review

    Maintainer->>Actions: Push annotated stable tag on the same source commit
    Actions->>Main: Verify named RC and reviewed evidence commit
    Actions->>Publisher: Read back the same AMI IDs and S3 versions
    Actions->>Public: Verify exact RC payload, then publish same bytes as stable
```

</details>

After the Ohio build, the workflow freezes the verified Packer output before regional copy. It then freezes the four regional AMI identities before sharing. The public prerelease becomes the canonical candidate when its complete assets and anonymous downloads pass verification. The first release uses the selected tags `v1.0.0-rc.1` and `v1.0.0`.

The stable tag names the validated RC and the reviewed evidence commit, and resolves to the RC's source commit. Stable publication independently rechecks the evidence, the controller digest, and the four regional AMIs and S3 versions. It copies identical GitHub asset bytes and reuses those existing AWS and GHCR artifacts. A changed payload requires a new RC. Stable is the default installer selection once it exists. [Release publication contract](../../specs/010-public-aws-distribution/contracts/release-publication.md)

## Customer installation

![CloudFormation, private networking, data storage, and workspace communications in a customer's AWS account](diagrams/aws-installation-system.svg)

[Open full-size diagram](diagrams/aws-installation-system.svg)

<details>
<summary>Mermaid source</summary>

```mermaid
%% Customer installation, private access, and runtime boundaries
flowchart TB
    accTitle: ShiftBox customer AWS installation
    accDescr: The operator deploys the pinned stack in their AWS account. The private controller brokers workspace IDE traffic and stores data with PostgreSQL on retained encrypted storage.
    Operator["Operator and workspace user"]
    Artifacts["Published release assets<br/>Regional S3 YAML, GHCR digest, AMI"]
    subgraph Customer["Customer AWS account and selected region"]
        CloudFormation["CloudFormation stack<br/>Console or reviewed CLI change sets"]
        Access["Private route<br/>Tailscale router or existing VPN / Direct Connect"]
        SSM["AWS Systems Manager<br/>Scoped setup session"]
        DNS["Private DNS<br/>Route 53 aliases and Cloud Map"]
        Controls["AWS IAM and Secrets Manager<br/>Scoped roles, TLS and DB secrets"]
        subgraph ECS["Private ECS on EC2"]
            Setup["Temporary ECS setup task<br/>Protected browser wizard"]
            Controller["ShiftBox controller<br/>Embedded UI, Dex, API and IDE proxy"]
            Postgres["PostgreSQL ECS service<br/>ShiftBox and Dex schemas"]
        end
        Storage["Retained encrypted EBS<br/>Installation state and PostgreSQL data"]
        Workspace["Private EC2 workspace<br/>Guest agent, devcontainer and browser IDE"]
        Egress["NAT or operator egress<br/>Outbound dependencies"]
    end

    Operator -->|"Inspect and approve stack phases"| CloudFormation
    Artifacts -->|"Selected regional template version"| CloudFormation
    CloudFormation -->|"Stages foundation and workloads"| ECS
    CloudFormation -->|"Creates scoped AWS resources"| Controls
    CloudFormation -->|"Stages private records"| DNS
    Operator -->|"Scoped AWS session"| SSM
    SSM -->|"Loopback HTTPS 8444"| Setup
    Setup -->|"Bootstrap and verify DB TLS"| Postgres
    Controls -->|"Execution roles and secret references"| ECS
    Artifacts -->|"ECS pulls exact image digest"| Controller
    Operator -->|"Private HTTPS and browser trust"| Access
    Access -->|"Split DNS"| DNS
    Access -->|"HTTPS 8443"| Controller
    Controller -->|"TLS on TCP 5432"| Postgres
    Postgres -->|"Database files"| Storage
    Setup -->|"Persists installation configuration"| Storage
    Storage -->|"Loads installation configuration"| Controller
    Controller -->|"Launch pinned AMI with encrypted root"| Workspace
    Workspace -->|"Guest-initiated mTLS and IDE tunnel"| Controller
    ECS -.->|"Outbound HTTPS"| Egress
    Workspace -.->|"Outbound package and repo access"| Egress
```

</details>

CloudFormation provisions the private VPC, service and workspace subnets, ECS host, named roles with permissions boundaries, retained encrypted volume, DNS, and workloads in the selected customer region. The ECS host runs the setup task, PostgreSQL, and the controller at their respective stages. Each workspace has its own private EC2 instance and uses the regional workspace AMI pinned by the release. The setup task configures the installation, and the controller brokers authenticated IDE traffic after activation. CloudWatch receives workload logs. [Customer CloudFormation template](../../platform/cloudformation/aws/template.yaml) · [Workspace lifecycle and providers](workspace-lifecycle-and-providers.md)

| Traffic | Private path and trust |
| --- | --- |
| Initial browser setup, TCP 8444 | An authorized operator starts a temporary ECS task and uses AWS Systems Manager to forward a local HTTPS port. The operator verifies the setup certificate fingerprint before opening the wizard. |
| Console, IDE, previews, and repair, TCP 8443 | Clients route through the optional Tailscale subnet router or an operator-managed VPN or Direct Connect path. Route 53 and Cloud Map provide private resolution. The guided no-domain path uses tailnet split DNS (TCP and UDP 53 to the router) and one-time client trust in an installation-specific browser CA. |
| PostgreSQL, TCP 5432 | Setup and controller connect to the database using its own TLS trust. Scoped Secrets Manager references supply database credentials and key material. |
| Workspace registration and tunneling, TCP 19443–19447 | The workspace agent initiates enrollment and mTLS connections from the workspace security group to the controller. The controller proxies IDE traffic to the workspace over that channel. |
| Dependency egress | Private workloads use the stack's optional NAT gateway or operator-provided egress to reach required AWS APIs, registries, package sources, and authorized repository services. |

[Private networking and browser trust](../../docs/site/private-access.md) · [Certificate and guest trust design](certificates-and-guest-trust.md)

## Customer installation sequence

![Version selection, reviewed CloudFormation stages, SSM-protected setup, and the first working workspace IDE](diagrams/aws-installation-sequence.svg)

[Open full-size diagram](diagrams/aws-installation-sequence.svg)

<details>
<summary>Mermaid source</summary>

```mermaid
%% Version selection, reviewed stack phases, and the first workspace
sequenceDiagram
    accTitle: Customer install to first browser workspace
    accDescr: The operator verifies a versioned release, deploys the customer stack in phases, completes SSM-protected setup, and opens an EC2 workspace through the private controller.
    actor Operator as AWS operator
    participant Assets as Release and regional S3
    participant Stack as CloudFormation
    participant AWS as Secrets Manager and SSM
    participant ECS as Private ECS services
    participant Guest as EC2 workspace

    Operator->>Assets: Choose stable, explicit RC, or pinned release
    alt Guided CLI
        Assets-->>Operator: Installer, manifest and checked release assets
        Operator->>Stack: Confirm account and region, validate and approve change sets
    else Console or inspect-and-deploy
        Assets-->>Operator: Regional quick-create link and inspection copy
        Operator->>Stack: Review exact template, parameters and IAM capabilities
    end

    Operator->>Stack: Create foundation, protect stack and retained resources
    Operator->>Stack: Review dns-zones, then dns-services on the same stack
    Stack-->>Operator: Confirm private zone, namespace and service IDs
    Operator->>AWS: Supply scoped database TLS secret
    Operator->>Stack: Review database stage
    Stack->>ECS: Start PostgreSQL and define temporary setup task
    Operator->>ECS: Start temporary setup task using the helper
    Operator->>AWS: Open scoped SSM session and verify setup fingerprint
    AWS->>ECS: Forward loopback HTTPS on TCP 8444
    Operator->>ECS: Create first admin, configure TLS and capacity, activate
    Operator->>ECS: Verify activation and stop setup task using the helper
    Operator->>Stack: Review active stage
    Stack->>ECS: Start the normal controller

    Operator->>ECS: Sign in over private DNS and HTTPS
    Operator->>ECS: Import pinned regional catalog and verify canary cleanup
    Operator->>ECS: Launch from an authorized project template
    ECS->>Guest: Launch pinned AMI with encrypted workspace storage
    Guest->>ECS: Enroll and open guest-initiated mTLS tunnel
    ECS-->>Operator: Serve authenticated workspace IDE on private HTTPS
```

</details>

The Console uses the regional version-pinned launch link. The guided shell installer verifies the release payload and AWS account and region, shows CloudFormation change sets for approval, and supports continuation using the same working directory and stack identity. The inspection path uses the same template and checksum. The customer deployment uses CloudFormation throughout.

The operator advances one stack through foundation, DNS zones and namespace, DNS services, database, and active. The setup wizard runs while the normal controller is stopped. After signing in over private HTTPS, the operator imports the pinned catalog, waits for the workspace canary and its cleanup, and selects the validated image in a resource profile and project template. Repository-backed workspaces additionally use an operator-owned GitHub App and the workspace owner's authorization. [Install and open a workspace](../../docs/site/install.md) · [Public install contract](../../specs/010-public-aws-distribution/contracts/public-install.md)
