---
layout: default
title: Install ShiftBox
permalink: /install/
show_release_links: true
---

# Install ShiftBox and open a workspace

You need an authenticated AWS account with permission to deploy CloudFormation and acknowledge named IAM resources, one of the four supported commercial regions, existing private service and workspace subnets in one VPC, and a private route for browser access. Use AWS CLI v2 and the local Session Manager plugin for the protected setup tunnel. Review EC2, volumes, outbound data, DNS and workspace costs before launching.

## Launch from the AWS Console

Choose the region on the official release. Open the quick-create link, confirm your AWS account, and verify the region and exact versioned S3 template URL. Inspect the template using **View template**, or download the YAML and compare it with the SHA-256 published in the release manifest.

1. Create the foundation with a new deployment name and private DNS suffix. Select your existing VPC, service subnet and workspace subnet. Select the service subnet’s Availability Zone for the retained volume. Supply an approved browser source CIDR when the private route is ready. Keep `RoleBoundaryMode=managed` for quick-create. Review and acknowledge the named IAM roles and their permissions boundaries. The role and profile names include your deployment name and region. Enable termination protection and apply the stack policy from the support archive. Keep your deployment name, zone and retained EBS volume.
2. On the same stack, review and execute `Phase=dns-zones`. Verify the private hosted zones belong to the installation VPC and the Cloud Map namespace uses your private suffix. Review and execute `Phase=dns-services`, then verify the `postgres` and `shiftbox` services are in that namespace. The support archive's operator guide identifies the outputs to check at each phase.
3. Add the PostgreSQL TLS certificate and key to the scoped Secrets Manager secret named by the stack output. Preview a change set for `Phase=database` with the public DB CA. Review it and wait for PostgreSQL while the normal controller stays stopped.
4. Configure your [private network and browser trust]({{ '/private-access/' | relative_url }}). Run the release archive's `setup.sh` from your local terminal with the exact stack ID, region and expected AWS account. Verify the loopback setup fingerprint before opening the wizard.
5. Create and secure the first administrator, select the verified database, configure the browser leaf certificate and key, select workspace capacity and activate. After the helper verifies activation and setup task stop, review `Phase=active` on the same stack and start the controller.

The version-matched support archive includes the stack policy, setup helper and operator guide. The download-and-inspect route uses the same template, release checksum and change-set review.

## Use the guided CLI

For stable, use the installer linked from the [latest stable release](https://github.com/jflatten/ShiftBox/releases/latest).

```sh
curl -fsSL https://github.com/jflatten/ShiftBox/releases/latest/download/install.sh | bash -s --
```

For a specific release, replace the example tag in both places with the exact published stable or RC tag.

```sh
curl -fsSL https://github.com/jflatten/ShiftBox/releases/download/v1.0.0-rc.1/install.sh |
  bash -s -- --version v1.0.0-rc.1
```

Download and inspect `install.sh` before execution if you prefer a local copy. Verify it against the distribution manifest. The CLI confirms AWS identity and artifacts, asks for `CAPABILITY_NAMED_IAM`, and asks you to approve each change set after review. Continue an interrupted CLI install with the same `--work-dir` and `--resume`. Before the first stable release, choose an explicitly announced RC from [Releases](https://github.com/jflatten/ShiftBox/releases).

## Open the first workspace

Sign in over your private connection using the `ManagementOrigin` stack output. In **Administration → Infrastructure → Platform**, import the selected region's official catalog file from the verified support archive. Wait for the validation canary to complete and its resources to be cleaned up.

Create a named resource profile pinned to the validated image. Choose it in an authorized project template. For a repository-backed workspace, configure your operator-owned GitHub App separately and connect the workspace owner's GitHub account. Select a repository and devcontainer they are authorized to use, launch the workspace and open the IDE. Public release assets remain available anonymously.
