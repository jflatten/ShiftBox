---
layout: default
title: Install ShiftBox
permalink: /install/
show_release_links: true
---

# Install ShiftBox and open a workspace

You need an authenticated AWS account with permission to deploy CloudFormation and acknowledge IAM creation, one of the four supported commercial regions, and an approved private route or a Tailscale tailnet. Use AWS CLI v2 and the local Session Manager plugin for the protected setup tunnel. Review EC2, volumes, NAT, DNS and workspace costs before launching.

## Launch from the AWS Console

Choose the region on the official release. Open the quick-create link, confirm your AWS account, and verify the region and exact versioned S3 template URL. Inspect the template using **View template**, or download the YAML and compare it with the SHA-256 published in the release manifest.

1. Create the foundation with a new deployment name, eligible Availability Zone, private DNS suffix and nonoverlapping VPC CIDR. Review IAM, enable termination protection, and use the stack policy from the release's support archive. Keep your deployment name, zone and retained EBS volume.
2. Add the PostgreSQL TLS certificate and key to the scoped Secrets Manager secret named by the stack output. Preview a change set for the database phase with the public DB CA. Review it and wait for PostgreSQL while the normal controller stays stopped.
3. Configure your [private network and browser trust]({{ '/private-access/' | relative_url }}). Run the release archive's `setup.sh` from your local terminal with the exact stack ID, region and expected AWS account. Verify the loopback setup fingerprint before opening the wizard.
4. Create and secure the first administrator, select the verified database, configure the browser leaf certificate and key, select workspace capacity and activate. After the helper verifies activation and setup task stop, review the active-phase change set on the same stack and start the controller.

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

Download and inspect `install.sh` before execution if you prefer a local copy. Verify it against the distribution manifest. The CLI confirms AWS identity and artifacts before creating a change set and asks you to review and approve each execution. Continue an interrupted CLI install with the same `--work-dir` and `--resume`. Before the first stable release, choose an explicitly announced RC from [Releases](https://github.com/jflatten/ShiftBox/releases).

## Open the first workspace

Sign in over your private connection using the `ManagementOrigin` stack output. In **Administration → Infrastructure → Platform**, import the selected region's official catalog file from the verified support archive. Wait for the validation canary to complete and its resources to be cleaned up.

Create a named resource profile pinned to the validated image. Choose it in an authorized project template. For a repository-backed workspace, configure your operator-owned GitHub App in Secrets Manager and connect the workspace owner's GitHub account. Select a repository and devcontainer they are authorized to use, launch the workspace and open the IDE. Public release assets remain available anonymously.
