# Feature Specification for Private ShiftBox on AWS

**Feature Branch** `feature/mvp`  
**Updated** 2026-11-16  
**Status** Approved for implementation  
**Input** Build a fresh, privately hosted ShiftBox installation on AWS with Terraform, containerized ShiftBox and PostgreSQL, and an EC2 workspace provider with a Packer-built Ubuntu amd64 image.

## User Scenarios & Testing

### User Story 1 - Deploy and initialize a private installation (Priority P1)

An infrastructure operator provisions a private AWS deployment, then completes ShiftBox setup through a protected first-run session. The operator supplies private network access, a DNS suffix, and a browser-trusted certificate for management, IDE, preview and repair names.

**Independent Test.** Deploy the stack, use a one-off setup task and Session Manager forwarding to initialize the administrator, PostgreSQL and TLS, then start the normal controller and sign in through the existing private connection.

**Acceptance Scenarios**

1. Terraform creates the VPC resources for ShiftBox and workspace VMs, the ECS cluster and two private services, persistent storage, workload IAM and private DNS. The operator supplies the client VPN, routing and DNS resolution from their network.
2. One private EC2 host runs separate controller and PostgreSQL services in the same ECS cluster. ShiftBox serves its embedded UI and supervises Dex. PostgreSQL accepts traffic only from the controller and authorized setup task, with distinct scoped ShiftBox and Dex database roles and schemas.
3. The deployer opens the existing loopback HTTPS wizard through a scoped Systems Manager session to a one-off setup task. The initial password reaches the authorized deployer without appearing in ECS logs, and the deployer must change it. The normal controller starts after setup ends, from the same persistent installation directory.
4. Replacing a task or the ECS host preserves PostgreSQL data, installed keys and certificates, and the initialized administrator. The deployment allows downtime and runs at most one active controller. Missing persistent storage blocks startup.
5. The ECS task role supplies the controller's AWS provider permissions. The setup role is separately scoped and PostgreSQL and workspace VMs receive no controller permissions.

### User Story 2 - Publish and select an AWS platform image (Priority P1)

A release operator builds and publishes a reusable private Ubuntu 24.04 LTS Minimal amd64 AMI with Packer. An administrator registers the release and configures a template with an EC2 resource profile selected from compatible instance types in the workspace Availability Zone and a separate EBS size.

**Independent Test.** Register the published AMI on the running controller and verify a disposable boot, authenticated guest contact and cleanup before the platform can be selected.

**Acceptance Scenarios**

1. ShiftBox validates the regional AMI identity, provenance and guest compatibility. AWS workspace launch stays unavailable until the disposable instance boots, authenticates and is removed with its owned resources.
2. A release with mismatched identity or compatibility is rejected with an actionable reason. Publishing a newer valid release leaves an existing workspace's image and disk pinned.

### User Story 3 - Launch and use a private AWS workspace (Priority P1)

An authorized member launches a workspace from an AWS template, follows live progress, and opens the private browser IDE.

**Independent Test.** Launch the reference workspace, open its IDE over the existing private connection, and verify project files, developer home and declared persistent data across reboot, stop/start and container rebuild.

**Acceptance Scenarios**

1. The workspace launches in the private workspace subnet without a public IP, inbound access or controller IAM credentials. It can reach the controller's private guest endpoints and the advertised outbound destinations.
2. The member sees the provider, platform release and ordered workflow steps. The IDE and previews use existing private authorization and readiness checks.
3. Retained data survives reboot, stop/start and rebuild. An eligible project failure uses the existing offline repair environment.
4. The fresh AWS installation starts without UTM. The existing native Mac installation continues to use its local UTM provider independently.

### User Story 4 - Recover or remove an AWS workspace (Priority P2)

An authorized owner can recover access after a long stop and remove a workspace when its data is no longer needed. Existing maintenance and retention rules apply.

**Independent Test.** Recover the same stopped instance after its guest trust expires, then delete it and confirm owned resources are gone and shared AWS infrastructure remains.

**Acceptance Scenarios**

1. An authorized start reconciles the verified stopped instance before issuing a single-use enrollment grant. Revoked, deleted or mismatched identities remain denied.
2. A lost provisioning response reconciles the same logical instance. A partial delete keeps capacity reserved until the instance and owned attachments are confirmed removed.
3. Manual deletion displays the data-loss warning and preserves shared resources. Retention and maintenance continue to use their existing authorization and safety rules.

### Edge Cases

- A missing data volume blocks both services, so a host replacement cannot initialize empty PostgreSQL or rotate the installation identity accidentally.
- An uncertain AWS result retains inventory and reservations. The controller checks ownership and actual state before retrying.
- An image or guest trust failure blocks workspace launch or access and identifies the failed step.

## Requirements

- **FR-001** Terraform MUST deploy a fresh private installation with a private ECS-on-EC2 host, separate controller and PostgreSQL services, and a private EC2 workspace network in the same VPC. The deployer owns client VPN and external routing into the VPC.
- **FR-002** ECS MUST use separately scoped task roles. The controller MUST use its task role for EC2 operations. Databases, workspaces, and setup MUST receive only their required permissions.
- **FR-003** Terraform MUST provision encrypted persistent EBS storage with distinct PostgreSQL and controller paths that survive task and host replacement. Startup MUST fail closed without the mounted persistent state. Run one controller and one PostgreSQL instance.
- **FR-004** Provide operator-guided setup through a scoped Systems Manager port forward to the one-off loopback HTTPS task. Preserve first-administrator change, separate application and Dex DB ownership, TLS activation, and password confidentiality. Terraform MUST publish private DNS; the deployer supplies suffix and browser-trusted certificate for management, IDE and previews.
- **FR-005** Packer MUST build a reusable private Ubuntu 24.04 LTS Minimal amd64 AMI with ShiftBox tooling and offline repair. Accept the pinned release only after metadata checks and a disposable guest canary. Support one reference AWS workspace size initially.
- **FR-006** ShiftBox MUST provide an EC2-only startup path and retain the standalone Mac/UTM path. AWS workspaces MUST retain their provider, target and image identity. Use the existing private IDE, repair, rebuild and real-time workflow behavior.
- **FR-007** Preserve retained workspace files through ordinary lifecycle operations. Reconcile uncertain AWS outcomes and release capacity only after verified stop or removal. Remove owned resources and preserve shared ones.
- **FR-008** Recover an eligible stopped EC2 workspace using a fresh single-use grant and preserve the existing maintenance, retention and management backup boundaries.
- **FR-009** AWS resource profiles MUST own the approved platform AMI, compatible offered EC2 instance type, and independent EBS disk size. Selecting an AMI narrows the instance types to its architecture. AWS templates select a resource profile without selecting an AMI. Future launches resolve the profile's current AMI and pin it on the new workspace; existing workspaces retain their captured image. This ownership change was explicitly requested by the user on November 17, 2026.

### Key Entities

- **Cloud deployment** describes private network, ECS services, task identities, DNS and persistent host storage.
- **Installation** holds setup status, administrator and database bindings, browser TLS and guest trust.
- **Platform release** records compatible AMI identity, provenance and validation state.
- **Workspace binding** identifies the admitted provider, AWS target, image and verified resource inventory.
- **Enrollment grant** authorizes one eligible registration for the current verified instance and operation.

## Success Criteria

- **SC-001** One Terraform deployment creates the private control plane and workspace network. One deployer completes protected setup and signs in through the existing private connection using the deployed DNS and TLS.
- **SC-002** Restarting or replacing the ECS tasks and replacing the host with its retained volume preserves the installation identity and database. An absent mount blocks startup.
- **SC-003** One verified Packer release yields a private AWS workspace and IDE. Retained files survive ordinary lifecycle operations and the same stopped instance recovers after trust expiry.
- **SC-004** The tested lost launch creates one logical instance. Verified deletion leaves zero workspace-owned resources and leaves the shared stack intact.
- **SC-005** The AWS deployment uses task-role credentials, and the existing local UTM smoke test still passes.

## Assumptions

- One AWS account, region, Availability Zone, private ECS host and reference workspace size. Host replacement has downtime.
- This deployment initializes a new installation. Existing Mac/UTM data stays in its separate installation.
- The deployer supplies external private routing and DNS resolution, trusted browser TLS files, and authorized release and bootstrap identities.
- The AWS stack uses ECS task roles. The earlier UI-managed AWS key pair proposal for a controller outside AWS is outside this deployment milestone.
