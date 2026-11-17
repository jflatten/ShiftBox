# Private ShiftBox on AWS validation quickstart

Use an isolated AWS deployment and a deployer-managed VPN or routed private network. Supply a private DNS suffix and browser-trusted certificate covering management, IDE, preview and repair names. Use separate Terraform, Packer and bootstrap permissions. The staged steps below describe release acceptance once the implementation is complete.

## Deploy and initialize

Follow the [Terraform deployment instructions](../../platform/terraform/aws/README.md) for the exact phased inputs, secret references and protected setup command.

1. Initialize the Terraform root against the locked AWS provider. Run formatting, validation and the mocked foundation tests. Review the private network, storage, IAM and image repository before applying the approved plan.
2. Build and publish the Linux amd64 controller image by digest. Build the pinned Ubuntu 24.04 LTS Minimal amd64 AMI on a Packer runner that can reach its disposable builder. Confirm the image and provenance, then supply the controller image digest to the deployment.
3. Start PostgreSQL with its mounted data directory and scoped initialization inputs. Confirm the normal controller is stopped. Launch the one-off setup task, forward the HTTPS wizard through Session Manager, complete the first administrator password change, PostgreSQL cutover and browser TLS setup. Verify the initial password is absent from ECS logs.
4. Stop the setup task and start the controller with the same persisted installation path. Sign in over the existing private connection. Confirm the ECS role supplies AWS permissions, the workspace network can reach private guest listeners and both browser and database access are scoped as specified.
5. Register the AMI and observe validation launch, authenticated poll and verified cleanup. Confirm a mismatched candidate remains unavailable.

## Validate the private workspace

1. Launch one workspace from the validated platform and supported profile. Open the private IDE and verify the instance has no public IP, inbound access or controller role. Write files under the repository, developer home and an explicitly persistent application mount.
2. Check each file after reboot, stop/start and rebuild. Exercise the offline repair path with a supported project failure. Stop the VM through certificate expiry and confirm authenticated recovery on the same instance. Validate the enrollment transport with a synthetic marker and test that usable grant data stays out of workload access and logs or disk caches.
3. Replace the ECS tasks and then the host within the selected Availability Zone. Confirm the original PostgreSQL data and controller identity persist, and that an absent mount blocks clean startup. Confirm the EC2 workspace remains bound to the same controller installation and expected image.
4. Confirm the user-facing deletion warning, delete the AWS workspace through ShiftBox and verify owned disk and network resources are gone. Shared Terraform and Packer resources remain. Re-plan Terraform to confirm workspace activity produces no infrastructure drift.

## Focused automated checks

Cover blocked startup without durable state, unauthorized credential or role access, one lost launch response and incomplete owned-resource cleanup. Reuse existing authorization, maintenance, retention and backup tests for shared behavior. Run a standalone UTM smoke test against the native Mac installation. Run the browser, guest trust and storage checks once on the exact AWS stack before marking it supported.
