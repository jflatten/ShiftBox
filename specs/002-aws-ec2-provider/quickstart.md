# Private ShiftBox on AWS validation quickstart

Use an isolated AWS deployment with a deployer-managed VPN or routed private network. For live development verification, select the approved testing stack and profile using the [repository instructions](../../AGENTS.md).

## Deploy and initialize

1. Render the CloudFormation template from the current checkout. Use the pinned controller image and workspace AMI, or build a private artifact for the feature under test. Follow the [AWS development guide](../../platform/development/README.md).
2. Select the private service and workspace subnets. Review the stack change set for workload networking, storage and IAM before executing it. Finish setup and activation at the management URL.
3. Sign in over the private connection. Confirm the ECS task role supplies AWS permissions, the workspace network can reach the guest listeners, and browser and database access remain scoped. Verify credentials stay out of ECS logs.
4. Observe validation launch, authenticated poll and verified cleanup when admitting a workspace AMI. Confirm a mismatched candidate remains unavailable.

## Validate the private workspace

1. Launch one workspace from the validated platform and supported profile. Open the private IDE and verify the instance has no public IP, inbound access or controller role. Write files under the repository, developer home and an explicitly persistent application mount.
2. Check each file after reboot, stop/start and rebuild. Exercise the offline repair path with a supported project failure. Stop the VM through certificate expiry and confirm authenticated recovery on the same instance. Validate the enrollment transport with a synthetic marker and test that usable grant data stays out of workload access and logs or disk caches.
3. Replace the ECS tasks and then the host within the selected Availability Zone. Confirm the original PostgreSQL data and controller identity persist, and that an absent mount blocks clean startup. Confirm the EC2 workspace remains bound to the same controller installation and expected image.
4. Confirm the user-facing deletion warning, delete the AWS workspace through ShiftBox and verify owned disk and network resources are gone. Confirm the stack and shared network are intact.

## Focused automated checks

Cover blocked startup without durable state, unauthorized credential or role access, one lost launch response and incomplete owned-resource cleanup. Reuse existing authorization, maintenance, retention and backup tests for shared behavior. Run a standalone UTM smoke test against the native Mac installation. Run the browser, guest trust and storage checks once on the exact AWS stack before marking it supported.
