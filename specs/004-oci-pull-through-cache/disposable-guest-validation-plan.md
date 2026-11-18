# Disposable guest plan for live OCI cache validation

## Current launch blocker

ShiftBox originally pinned one exact AMI for stopped-instance enrollment. The single-boot rollout added a second exact AMI for challenge-based enrollment on the first boot. This gives the demo two approvals because the controller configuration has one field per enrollment flow. AWS itself can register additional private AMIs. ShiftBox currently checks those two IDs independently at import, resource profile selection, workspace launch and provider resolution. Its existing signed update campaign targets all retained workspaces of the matching architecture.

- [Controller import and provider checks](../../cmd/shiftbox/cloud_provider.go)
- [Workspace launch check](../../internal/web/ec2_launch_image.go)
- [Maintenance campaign target selection](../../internal/workspace/maintenance.go)
- [Single-boot validation record](../../documentation/validation/ec2-single-boot.md)

## AMI family workflow

The approved direction is for administrators to publish validated releases to a named AMI family and for resource profiles to follow a family. A new launch picks the newest available validated member compatible with the selected instance type, disk size and guest release. Order members by AWS creation time. The launch persists the exact image so a retry or restart keeps it. Publishing leaves existing pinned profiles, workspace bindings and the current default in place.

Keep the existing provenance and compatibility checks. Bind approval to the AMI, account, region, architecture and verified artifact identity. Use a single-boot enrollment canary for new family members. Show live validation and failure in the admin UI and retain progress on reload. Retire an unused temporary image from future selection before deregistering it.

## Private builder preview

Terraform validation and a read-only plan succeeded using the demo account, region and private workspace subnet. The preview has 14 creates, zero updates and zero deletes. They cover one temporary Systems Manager relay, its dedicated role and instance profile, a temporary SSH key registration, two isolated security groups, five rules and two relay network-interface tags. The relay has no public IP or inbound rules. The builder accepts SSH only from the relay security group. The private subnet has automatic public IP assignment disabled and an active NAT route for package access. The pinned x86-64 Canonical source AMI is available.

The local preview uses a reference public SSH key and is review material. Generate a fresh ephemeral build key and a new saved Terraform plan for an actual run. Keep the builder state separate from the demo services. Run the existing [private Packer recipe](../../platform/packer/aws/README.md) with a unique artifact identity, the cache-capable Linux amd64 binary, and the existing guest-update verification key. Retain the existing sealing checks, encrypted build volume and artifact provenance. The preview created no AWS resources or guest AMI.

## Live test and cleanup

1. Publish a sealed image into a disposable family. Launch a workspace using a profile and template for that family. Publish a newer image into the same family and confirm the second workspace selects it while the first binding remains pinned. Explicitly verify the original AMIs, approvals, profiles and default still match the baseline.
2. Run one direct, one cold-cache and one warm-cache workspace launch on fresh VMs with matching architecture, project files and pinned upstream digest. Choose an IDE-capable fixture with an image absent from the sealed guest's preloads. Verify the direct test's authorized egress and the cold test's empty fixture cache, retain the cold content for the warm run, and record each run's path and IDE readiness.
3. Capture controller upstream and locally served payload bytes, metadata, stream-through or guest-local reuse where observable, and time to the proxied IDE. Capture counters before changing cache settings because activation resets their observation window. Mark unmeasured quantities as unknown. Restore the original Cache settings after the comparison.
4. Delete the disposable workspaces and disks, archive the disposable template, disable its referenced profile, and retire the two test images. Verify no retained workspace or enabled profile can launch a test AMI before deregistering it and removing its exclusive snapshot. Destroy the isolated builder through its Terraform state and verify the demo services and original approvals remain in place.

The live measurements and cleanup sign-off in [T023 and T024](tasks.md) remain open.
