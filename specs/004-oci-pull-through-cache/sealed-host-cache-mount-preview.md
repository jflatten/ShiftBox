# ShiftBox demo boot-time cache mount preview

T023 draft for review. The previews used the existing deployment inputs, AWS provider 6.73.0, and a private copy of the current Terraform state. AWS refreshed the plans against the demo. The canonical state matched the copy afterward. No live resources were changed.

## What Terraform proposes

| Preview | Changes against the current demo |
| --- | --- |
| Controller quiesce | Controller desired count from 1 to 0. No other change. |
| Both services quiesced | Controller and PostgreSQL desired counts from 1 to 0. In execution, stop and verify the controller before changing PostgreSQL. |
| Boot-time cache mount | One in-place update to the EC2 host. The only changed field is `user_data`. No replacements or volume detachments. |
| Controller bind | Against today's state, the bind preview contains the host update above, a new controller task definition and a controller service update. Generate a fresh rollout plan after host verification using the new controller image digest. The bind preview uses the existing image digest for shape review only. |

The provider performs a stop and start for a user-data update with replacement disabled. The draft also prevents Terraform from destroying the instance. AWS dry runs authorized stop, start and user-data modification, and the read-only EC2 console output API returned output. [Terraform AWS instance lifecycle](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/instance) · [EC2 stop and start](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/how-ec2-instance-stop-start-works.html)

| Disk | In-place stop and start | If the host were terminated |
| --- | --- | --- |
| Root `vol-051b614f9fdbc7283` | Retains the actual root disk and operating system | AWS reports delete-on-termination enabled. Host replacement is excluded. |
| Shared installation and PostgreSQL `vol-09cc035be176053cd` | Retains the same volume and data. The draft explicitly disables shared-volume initialization. | AWS reports delete-on-termination disabled. An independent volume would require deliberate reattachment. |
| Cache `vol-06b675eca82009f4f` | Reuses the attached encrypted 20 GiB volume | AWS reports delete-on-termination disabled. |

The private EC2 network interface and IP stay with the instance across the stop and start. AWS can place the instance on a different underlying machine. [EC2 persistence across stop and start](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/how-ec2-instance-stop-start-works.html)

## How the sealed host prepares and reports the mount

The MIME user data retains the existing first-boot script with shared-disk initialization disabled. A separate cloud-init boothook installs a temporary systemd unit and starts it at boot. The unit selects the cache device by the exact EBS serial, requires it to be blank and unmounted for first-time initialization, and refuses an unreadable signature probe. It formats only that device as XFS, checks that the mounted source is the same device and verifies the cache directory owner and volume marker. A root-owned stamp disables formatting on later boots. If first preparation is interrupted after formatting, the next attempt fails safely for review. [Cloud-init user-data formats](https://docs.cloud-init.io/en/latest/explanation/format/index.html) · [Run updated EC2 user data on boot](https://repost.aws/knowledge-center/execute-user-data-ec2)

Before enabling the controller bind, require a fresh EC2 console readiness line with the expected volume ID, the new boot identifier, XFS and the measured capacity. Verify the instance and all three disk IDs again. Read-only console output works without enabling interactive serial access. The cache unit is ordered before ECS and leaves the database's existing mount dependency in place. The controller independently checks the expected volume marker before retaining content. [EC2 console output](https://docs.aws.amazon.com/cli/latest/reference/ec2/get-console-output.html) · [ECS host-directory bind mounts](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/specify-bind-mount-config.html)

Local checks passed for a blank disk, subsequent mounting without format, a missing serial, the wrong mount, a disk mounted elsewhere, a nonblank disk and an unreadable probe. The rendered MIME has two valid shell parts and is 7,373 bytes against EC2's 16 KiB raw limit. The actual AL2023 boot and console readiness line remain unverified until the maintenance window.

## Maintenance and recovery sequence

1. Verify the existing deployment, rollback image digest and live volume mappings. Create and verify an encrypted management backup while the original controller and database are healthy. Retain a recoverable copy independent of this operation.
2. Scale the existing controller to zero through Terraform and wait for the task to stop. Scale PostgreSQL to zero and verify clean shutdown. Generate and apply a fresh host-only plan against the canonical state. Abort if it proposes any replacement, disposal, attachment, network or unrelated change. Both services will be unavailable during this step and recovery. No outage duration has been measured.
3. Wait for EC2 status and the new console readiness evidence. Restore PostgreSQL and verify it is healthy. If the mount fails, keep the controller bind disabled, restore the original boot configuration with shared-disk initialization disabled, and restore the existing PostgreSQL and controller revisions. Restoring boot configuration can require another stop and start. The cache unit files live under `/run` and the disk remains attached. Do not erase or reformat an unexpectedly initialized disk.
4. After a successful mount, complete the approved application schema upgrade against the verified backup before deploying the new controller. Verify the new image digest, health and effective cache path. If rollback crosses a schema version, use the reviewed backup and recovery flow. Leave the root and shared disks attached throughout.
5. After the signed guest release is available, finish both signed-in Cache journeys, the outage/recovery journey and the fresh-guest direct, cold and warm comparison. The local image smoke test passed. The live backup, mount, schema upgrade, service deployment and signed-in checks are still pending.

The source patch and sanitized plan summaries are in the local sealed-host review directory. Saved plans, provider credentials and deployment inputs remain local.
