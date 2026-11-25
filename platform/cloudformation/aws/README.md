# Install ShiftBox on AWS

Download the release support archive and select a template in its `launch/` directory. Compare the archive SHA-256 with the distribution manifest before extracting it. [ShiftBox releases](https://github.com/jflatten/ShiftBox/releases)

ShiftBox runs the ECS controller and PostgreSQL on a private EC2 host with retained encrypted storage. Workspaces use separate private EC2 instances. A public installation adds a Network Load Balancer and an Elastic IP for browser HTTPS.

## Create the stack

1. In the intended AWS account and Region, upload the private or public launch template. Enter the deployment name as the stack name. Select the service and workspace subnets. CloudFormation resolves the VPC, service Availability Zone, and default internal DNS suffix during the create.
2. Supply the approved browser source IPv4 range. For public access, select **Public**, select the authoritative Route 53 zone, and enter the Let’s Encrypt inputs. Use the explicit-placement form when several eligible public subnets exist. [Public access inputs](https://jflatten.github.io/ShiftBox/install/#public-browser-access-with-lets-encrypt)
3. Review and execute the CREATE change set. Acknowledge named IAM resources and keep the default installation settings for a fresh stack. Wait for **CREATE_COMPLETE**. The stack configures discovery and database TLS before starting the controller. You can enable termination protection and use the release’s `stack-policy.json` to protect retained resources during future updates.

## Finish first-login setup

Retrieve the claim from **SetupClaimSecretArn** in Secrets Manager. If the stack supplies **BrowserCaSecretArn**, trust that CA on the operator’s device. Open **ManagementOrigin** and use the claim to create the first administrator. Sign in, validate database and browser TLS, save capacity, and activate. Sign in again at the same URL after activation. [Browser setup](https://jflatten.github.io/ShiftBox/install/#finish-setup-in-shiftbox)

Then import and validate the official AMI using its regional catalog from the support archive if the installation needs a workspace image. Select a pinned resource profile in the project template before launching a workspace.

The guided CLI accepts `--public` for discovery and selection before the same single CREATE. Resume an interrupted install with `--resume --work-dir <same-directory>`. Use the release-matched setup helper for an older stack that uses a separate setup task. [Legacy setup helper](https://jflatten.github.io/ShiftBox/install/setup-helper/)

## Size the OCI image cache

Set **OCI image cache size (GiB)** when creating the stack. The default is 20 GiB
for up to five active users. Start with 50 GiB for 6 to 20 users or 100 GiB for
21 to 50. Increase the starting size when projects use many large, distinct
images. Shared layers reduce disk usage. Review the storage budget under
**Administration → Infrastructure → Cache** when choosing a larger disk.

The controller uses a separate encrypted gp3 volume that is deleted with the
stack. To increase the size on a stack that already has that volume, review a
size-only change set and wait for the EBS modification to complete. Restart the
ECS host during planned maintenance to grow the filesystem. EBS volumes cannot
be reduced in place. [Amazon EBS volume modification](https://docs.aws.amazon.com/ebs/latest/userguide/ebs-modify-volume.html)

An installation created before this option needs a separate host bootstrap
change before it can use a dedicated cache disk.

## Diagnose and recover

Check CloudFormation operation validation and failure events before retrying. Confirm your console or AWS CLI identity, stack region, exact template version, ECS service and task health, mounted installation volume, secret references, private DNS/routes and trust chain. An untrusted certificate or private DNS error calls for correcting access or trust. Keep the original stack, AWS resources and protected setup identity during an interrupted install. Follow the version-matched backup, migration and recovery guide before changing the release or deliberately removing retained resources.
