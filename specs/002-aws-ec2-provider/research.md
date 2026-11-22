# Private ShiftBox on AWS research

## Cloud deployment

**Decision.** Run separate single-task ShiftBox and PostgreSQL services on one private ECS-on-EC2 host. Both use distinct host paths on an independently managed encrypted gp3 volume. Register host capacity only after the volume mounts. Keep Dex supervised in the existing ShiftBox service and keep separate PostgreSQL ownership for the application and Dex. Use the host replacement procedure within the selected Availability Zone.

**Rationale.** The user chose a small deployment and containerized PostgreSQL. The two services share host capacity and retain their state across replacement. ECS service-managed EBS volumes have a task-bound lifetime, so persistent data belongs on a Terraform-owned host volume.

Sources [ECS service EBS lifetime](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ebs-volumes.html), [ECS host bind mounts](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/bind-mount-examples.html).

## Networking and authority

**Decision.** The full Terraform stack provides private service and workspace subnets, outbound connectivity and private service discovery. The deployer provides browser network access and DNS resolution through their existing VPN, an agreed DNS suffix and a browser-trusted TLS certificate. PostgreSQL accepts connections only from the controller and authorized setup task. The controller uses explicit credentials from its ECS task role with EC2 mutations scoped to required resources and ownership tags. ECS host and execution roles serve separate infrastructure purposes. Guests and PostgreSQL receive no EC2 provider authority.

**Rationale.** All application ingress stays private and existing browser and guest TLS remain end-to-end. ECS task credentials remove the need to place a provider key pair in this deployment's configuration, Terraform state or guest. A separate setup role grants narrowly scoped Systems Manager channels.

Sources [ECS private service discovery](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service-discovery.html), [ECS task roles](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html), [EC2 service authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html).

## Protected initialization

**Decision.** Use a deployer-operated Session Manager port forward to the one-off ECS task's loopback HTTPS setup. Share the installation volume and lock with the normal controller. Keep the bootstrap password out of task logs. Reuse initial password change, scoped PostgreSQL cutover, and trusted browser certificate activation. Start the normal controller after setup, then use its VPC-reachable guest listeners to validate the first platform before allowing launches.

**Rationale.** The current setup binds a protected browser origin on loopback. A scoped tunnel preserves that boundary. Performing the AMI canary after normal service startup reuses private guest listener routing instead of adding a second cloud listener topology to bootstrap.

Sources [Session Manager forwarding to ECS tasks](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html), [existing setup command](../../cmd/shiftbox/setup.go).

## Ubuntu and Packer

**Decision.** Use Packer's `amazon-ebs` builder in its own recipe directory. Use the repository's Packer 1.14.2 pin and validate the Amazon plugin 1.8.4 pin. Resolve Canonical's Ubuntu 24.04 LTS Minimal Noble amd64 EBS source AMI once per release in the target region and pin its ID and publisher. Reuse guest provisioning assets and common sealing steps. Let the AWS builder perform the final stop and capture. Record the private built AMI and root snapshot, source image, source revision, architecture, guest/protocol/tool versions and binary digests.

**Rationale.** A regional AMI pins launch inputs, and the existing guest, toolchain, signed update verification, and offline repair baseline preserve the workspace contract. An AMI ID identifies a regional image. Manifest and release binary digests provide separate integrity evidence. Validate the SSH build user's cleanup, cloud-init and machine identity reset, sealed image boot, and retained root filesystem growth.

**Alternatives considered.** Importing a UTM export would add conversion and a separate boot-support path. A moving AMI alias would permit release inputs to change between builds.

Sources [Canonical AMI discovery](https://ubuntu.com/aws/docs/aws-how-to/instances/find-ubuntu-images/), [Packer Amazon EBS builder](https://developer.hashicorp.com/packer/integrations/hashicorp/amazon/latest/components/builder/ebs), [Amazon plugin pin](https://github.com/hashicorp/packer-plugin-amazon/releases/tag/v1.8.4), [Packer manifest](https://developer.hashicorp.com/packer/docs/post-processors/manifest).

## Provisioning and inventory

**Decision.** Pin the target, instance type, AMI and launch parameters before the side effect. Use a stable launch token derived from the journaled operation. Tag the instance, encrypted root EBS volume and primary ENI and verify account, location, operation and ownership before taking action. Reconcile EC2's initial boot through an operation-scoped graceful stop of the inert instance. Record stopped completion only after observing the required inventory and allocation. For deletion, verify the terminated instance and disappearance of each recorded workspace-owned resource before releasing disk capacity.

**Rationale.** EC2 launch starts the instance and reads can lag mutations. Existing `internal/workspace/provision.go` completes only on verified stopped inventory, and a stopped instance preserves EBS data. Reconciliation needs to recognize the owned intermediate EC2 state and durably track partial inventory while preserving existing completion rules.

**Alternatives considered.** Accepting the initial running instance as provisioned would invalidate the existing enrollment gate. Retrying with a new token after an uncertain response risks duplicate resources. Relying on termination alone cannot prove all owned attachments were removed.

Sources [RunInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_RunInstances.html), [EC2 idempotency](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html), [eventual consistency](https://docs.aws.amazon.com/ec2/latest/devguide/eventual-consistency.html), [volume persistence](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/preserving-volumes-on-termination.html).

## Enrollment delivery

**Decision.** Validate stopped-instance EC2 user data as the first delivery transport. Write scoped, short-lived, single-use enrollment material after verifying the stopped instance. Read back and compare a digest before start. A dedicated root boot reader fetches an opaque, bounded seed over IMDSv2 when enrollment is needed. Keep the seed in restrictive tmpfs and remove it after use. Verify that cloud-init does not process or persist it. A spent or expired payload can remain configured in the AWS attribute until the next authorized stop, when it can be cleared. Preserve controller-side grant expiry, operation binding, revocation and replay rules.

**Rationale.** EC2 restricts EBS instance user-data changes to the stopped state, and the boot reader can reread it after a later stop/start. EC2 user data can be read by principals with applicable access and by instance software. The release gate therefore requires evidence that live grants stay within authorized provider/guest access, that workload containers cannot access the transport, and that grants do not persist in disk caches or telemetry. Refuse release if these checks fail.

**Alternatives considered.** Ordinary cloud-init user-data scripts do not provide a reliable repeatable recovery receiver and can cache payloads. An encrypted EBS seed disk may be considered if the first transport fails validation. Its temporary snapshots, attachments, AWS permissions, grant timing, and verified cleanup would require a design review.

Sources [EC2 instance metadata retrieval](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html), [metadata access and user data](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-metadata.html), [IMDS configuration](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-instance-metadata-options.html), [cloud-init internal files](https://docs.cloud-init.io/en/24.2/development/internal_files.html).
