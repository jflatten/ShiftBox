# Private ShiftBox on AWS contracts

## Terraform deployment

The Terraform root configures a private VPC and service and workspace subnets in the target Availability Zone. It provisions ECR, encrypted persistent EBS, an ECS cluster and host, task roles, security groups, logging, and private service discovery. It exposes nonsecret values needed to publish an image, connect over the deployer-managed private network and run setup. Normal traffic and guest trust terminate at ShiftBox. PostgreSQL is reachable only from the setup and controller tasks.

The data volume is independent of host lifetime. The host registers with ECS after the expected volume is mounted and role-specific state directories are ready. The controller and PostgreSQL tasks use distinct paths and replace one at a time. The operator handles controlled host replacement within the volume's Availability Zone.

## Operator initialization

1. The deployer supplies VPC DNS reachability, browser names and their trusted certificate, a pinned controller image digest and scoped authorization for setup.
2. With PostgreSQL ready and the normal controller stopped, the deployer starts a one-off task and forwards a local port through a scoped AWS Systems Manager session. The session document forwards only the fixed port 8444. The wizard advertises matching loopback HTTPS and delivers the one-time password to the browser. ECS logs contain the setup URL and certificate fingerprint. Cloud setup status exposes a `cloud` object with `managementOrigin`, `ideHostPattern` and `previewHostPattern`. The database step accepts `profile: postgres` with `tlsMode: verify-full`, using the deployed scoped connection on the server.
3. The deployer changes the password, configures scoped PostgreSQL access for ShiftBox and Dex, and activates TLS and application state. Setup exits and releases the shared installation lock before the controller starts.
4. An authenticated administrator registers the Packer release and the controller holds launch access until it verifies boot, authenticated guest contact and cleanup.

## Runtime AWS identity

The controller loads AWS credentials explicitly from its ECS task role for the configured account and region. The task execution role separately pulls images and emits logs. The setup task has a separate role and PostgreSQL and workspace instances receive no controller permissions. The controller's EC2 mutation policy uses workspace ownership tags that are distinct from Terraform's shared deployment tags. Provider calls validate the configured target and verify owned-resource identity before mutations. Failures preserve the workspace binding and leave the corresponding operations pending or visibly failed.

## Platform and workspace contract

An EC2 artifact manifest identifies the provider, release, Ubuntu series and architecture, AMI owner, region and AMI ID, source AMI publisher and ID, root snapshot and minimum disk size, agent version/digest, protocol and tool versions, and build provenance. Admission compares those facts to AWS, verifies compatibility and completes a disposable instance boot, authenticated poll and cleanup. AWS artifact selection reuses the project template's platform field. The launch response and workspace details expose a provider label and release while using existing workspace operation and event formats.

An EC2 resource profile selects an on-demand, non-bare-metal Nitro instance type that supports HVM and EBS, is offered in the workspace subnet's Availability Zone, and matches an architecture supported by the platform. The current AMI contract supports amd64. The controller reads AWS compute metadata and offerings at startup, derives vCPU and memory from the selected type, and accepts a separate encrypted gp3 root volume in whole GiB from 32 through 2,047. The configured size applies to the EBS volume. The existing sealed AMI disables cloud-init and does not expand its root filesystem at boot.

Admission requires the template's selected AMI to match the profile's architecture and be deployment-approved, available, and within the supported guest release and protocol window. If the selection is incompatible, admission returns a conflict before reserving capacity and directs the administrator to select a verified AMI in a new template revision. The selected type, AMI and disk size are pinned for the new workspace. Legacy EC2 profiles and workspaces resolve to the original t3.medium shape and 32 GiB disk.

| Provider action | Observable evidence |
| --- | --- |
| Create | Frozen request and token for one verified owned instance. An approved single-boot AMI receives a public launch reference and waits for enrollment while running. Legacy images boot unseeded and reconcile to stopped. Both paths verify exact allocation and owned resource inventory before provision succeeds. |
| Inspect | Per-workspace account, region and ownership checks apply before reporting state. Conflicting ownership, failed reads and uncertain eventual-consistency outcomes block mutation and keep reservations. |
| Start and recovery | Verify current target and stopped inventory. When trust is missing or expired, deliver a fresh scoped seed and verify the write before starting the exact instance. Authenticate the agent and reach existing readiness checks before opening the IDE. |
| Stop | Graceful stop under existing operation authority. Confirm EC2 stopped and the encrypted EBS disk retained before releasing CPU and memory. |
| Delete | Recheck current authority and ownership. Confirm instance termination and removal of the primary ENI, root volume and any known owned leftovers before releasing disk capacity. Preserve all shared resources. |
| Network | Use only the verified Terraform-deployed default outbound mode. Reject incompatible policies at admission. Workspace security groups and routing stay under Terraform authority. |

AWS user data is the proposed enrollment transport and requires exact-stack proof. Use IMDSv2 with hop limit one, a bounded opaque seed, root-only tmpfs and no AWS SDK request or response body logging. The seed carries only the existing scoped grant, trust material and endpoint information. Keep workload startup behind authenticated enrollment and verify isolation of the live seed from ordinary workload access, including container restart during recovery. A guest with host root remains untrusted under the existing architecture. Require that cloud-init caches and diagnostics, workspace disks and snapshots, CloudTrail and other logs contain no usable grant. Invalidate an issued grant if delivery or start cannot be reconciled within its validity window. Clear any spent value at an authorized stop, or replace it on the next verified recovery. A running instance with expired trust follows the existing recovery-required flow.

For single-boot initial enrollment, the user data contains only a version, launch reference, endpoints and public CA. The controller independently resolves the exact instance and owns the enrollment decision. The guest proves its certificate key and uses its built-in EC2 instance identity credentials to sign an STS `GetCallerIdentity` request covering the same short-lived challenge. Only the matched AWS account and exact instance may authorize issuance. The controller consumes the nonce and issues the key-bound grant in one transaction. The grant travels over the private TLS listener and the guest must authenticate before any workspace workload starts.
