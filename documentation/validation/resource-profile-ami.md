# Resource profile AMI ownership validation

T025 was validated on November 17, 2026, against the private `shiftbox-demo` installation in account `524941137496`, region `us-east-2`, using the `default` profile.

## Result

AWS resource profiles now own the approved platform AMI, offered EC2 instance type, and independent EBS disk size. Selecting an image filters instance types by its architecture. AWS templates select a profile without an independent AMI control. Admission captures the current profile image for each new workspace; existing workspaces and repeated launch requests retain their original image.

## Deployment

- Verified encrypted management backup operation `f72c7745-2b26-49c7-97ae-30279962f0fd` before stopping the controller.
- The one-off migration verified the backup archive checksum and upgraded PostgreSQL schema 32 to 33.
- Terraform stopped only the controller for migration, then replaced the controller and setup task definitions. PostgreSQL, storage, and networking were unchanged. The setup policy was recomputed because it references the setup task family.
- Running controller: `shiftbox-demo-controller:19`.
- Verified image digest: `sha256:9a9005bce9bb414803022231088e7fad8fde4eeab2aba8ca1ad6494fa83bebf9`.
- Both controller and PostgreSQL reached `COMPLETED`, with one desired and running task each.
- Terraform removed both temporary backup/migration task definitions. The temporary helper image was deleted from ECR. The encrypted backup remains protected on the installation volume.

Review URL: <https://shiftbox.demo.shiftbox.internal:8443/infrastructure/>.

## Checks

The Go regression suite passed with `go test -p 1 ./...`; `go vet ./...` passed. The initial concurrent full run exposed existing Dex subprocess timing failures. The isolated timeout test and subsequent full run with package concurrency limited passed. The 146 UI tests, TypeScript check, production static export, OpenAPI type check, embedded browser asset test, and Linux container executable smoke check passed.

Tests cover unavailable/unverified images, architecture mismatch, insufficient root disk, image selection persistence, incompatible type reset, UTM behavior, template profile ownership, profile AMI changes between launches, launch replay, and immutable existing workspace image bindings. SQLite upgrade/backfill was tested. The PostgreSQL fixture test is environment-gated and was not run locally; the actual demo PostgreSQL upgrade completed successfully.

Live browser verification used a disposable `AMI profile review` profile:

1. Selected `ec2-single-boot-20261117T164639Z` (`ami-0eea624c1e5701eb3`), selected `t3.medium`, and verified 2 vCPUs, 4,096 MiB memory, and 32 GiB EBS.
2. Created the profile and reloaded to confirm persistence.
3. Reopened it and verified the saved AMI, instance type, and disk. Renamed it and disabled it. One initial edit/list request returned a generic failure; retry succeeded, and subsequent update, reload, and delete checks passed.
4. Re-enabled the profile, switched its AMI to the approved `development` image, kept the compatible instance type, changed EBS independently to 64 GiB, and verified all values after reload.
5. Opened the AWS template form, selected the disposable profile, and confirmed the form has no independent AMI field. Canceled without creating a template.
6. Deleted the disposable profile and reloaded to confirm it remained absent. Existing profiles were preserved.

The demo currently exposes amd64 images; the two-architecture filtering/reset behavior is covered by UI tests. No workspace was launched for this UI verification.

## Existing profiles

Schema 33 assigned the `development` image to `EC2 Large` from its unambiguous historical reference. `AWS demo · t3.medium` had multiple historical image references, so migration left its AMI unassigned. In T026, the user explicitly requested the verified single-boot AMI. The profile was updated to `ec2-single-boot-20261117T164639Z` (`ami-0eea624c1e5701eb3`) and verified after reload, retaining `t3.medium`, 2 vCPUs, 4,096 MiB memory, 32 GiB EBS, and enabled state. Existing workspaces retain their captured image.

Local rollout plans, logs, and screenshot evidence are under `.cache/aws-demo/ami-profile-schema33/`; state, deployment inputs, and registry credentials remain local.

In T027, the user also requested the verified single-boot AMI for `EC2 Large`. Updated it to `ec2-single-boot-20261117T164639Z` (`ami-0eea624c1e5701eb3`) and verified persistence after reload, retaining `m6i.xlarge`, 4 vCPUs, 16,384 MiB memory, 32 GiB EBS, and enabled state. Both demo resource profiles now use the verified single-boot AMI.

## T028: Live launch from the updated profile

At the user's request, launched `groundtruth-single-boot` from `groundtruth - large`, revision 3. The launch review and persisted workspace configuration both showed `EC2 Large` and `ec2-single-boot-20261117T164639Z`. The template revision predates the profile update, exercising current profile ownership at launch.

- Workspace: `baa31673-6496-4b13-985b-4d5ba20ea77f`.
- EC2 instance: `i-0b589ed3f65ba6b26`, launched at 19:13:36 UTC on November 17, 2026, in `us-east-2a`.
- EC2 `DescribeInstances` independently confirmed `ImageId=ami-0eea624c1e5701eb3` and `InstanceType=m6i.xlarge`, matched to the workspace ownership tag.
- EC2 `DescribeImages` confirmed the available account-owned image `shiftbox-ec2-single-boot-20261117T164639Z-amd64`.
- EBS volume `vol-0bc7e3e85f2f51764` is encrypted gp3, 32 GiB, and in use.
- Provisioning, authenticated guest enrollment, container setup, and IDE readiness completed. The workspace displayed Running and a healthy code-server probe, with Open IDE enabled.

The requested workspace was retained for the user's IDE review, then removed in T030 after the user confirmed verification. No disposable diagnostic resources were created. This check confirms the selected AMI and readiness; it does not independently recount kernel boots or replay the enrollment proof.

Workspace URL: <https://shiftbox.demo.shiftbox.internal:8443/workspaces/?id=baa31673-6496-4b13-985b-4d5ba20ea77f>.

Redacted EC2 evidence and the ready-state screenshot are under `.cache/aws-demo/ami-profile-schema33/groundtruth-launch-instance.json` and `.cache/aws-demo/ami-profile-schema33/groundtruth-single-boot-workspace.jpg`.

## T030: Cleanup after human IDE verification

On November 17, 2026, the user confirmed IDE verification and requested cleanup. Deleted `groundtruth-single-boot` through ShiftBox's normal deletion workflow. The UI reported Deleted and completed graceful shutdown, stopped-state verification, and retained-file deletion.

Independent AWS checks confirmed:

- Instance `i-0b589ed3f65ba6b26` is terminated.
- Root EBS volume `vol-0bc7e3e85f2f51764` is absent.
- Network interface `eni-0de3ea8d71fd87299` is absent.
- Both schema 33 backup/migration task definitions are inactive; their Terraform state contains no managed resources.
- Temporary migration image `sha256:786bcbfdc69204b28a8a96bd943105537c3cd1a07b81bcd087d2388d802d610d` is absent from ECR.
- The existing `groundtruth` instance `i-0ec0e862c0ed5a3ac` remains stopped.
- Controller and PostgreSQL each have one desired and running task and report `COMPLETED`.

The shared verified AMI, updated resource profiles, templates, deployed controller image, and protected management backup remain in place. The disposable profile used in T025 was already deleted. No temporary change-specific compute, volume, network interface, active helper task definition, or helper image remains.

Cleanup evidence is local under `.cache/aws-demo/ami-profile-schema33/cleanup-final.json` and `review-workspace-deleted.jpg`.
