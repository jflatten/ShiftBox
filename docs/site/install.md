---
layout: default
title: Install ShiftBox on AWS
permalink: /install/
show_release_links: true
---

# Install ShiftBox and open a workspace

Deploy ShiftBox in your AWS account, create the first administrator, and sign in over private HTTPS.

## Choose your deployment

* **Current AWS template from source.** Supply an existing VPC and two private subnets in that VPC, one for ShiftBox services and one for workspaces. Use the service subnet's Availability Zone for the installation volume. Provide outbound connectivity, private browser routing and DNS forwarding. The stack adds workload security groups and private DNS records. [Current CloudFormation operator guide](https://github.com/jflatten/ShiftBox/tree/main/platform/cloudformation/aws)
* **Published `v1.0.0-rc.3` release candidate.** The RC3 template provisions its own network. Use the steps below with the RC3 installer, template and support archive. For another release, use that version's assets and guide. [ShiftBox releases](https://github.com/jflatten/ShiftBox/releases)

## Before you begin with RC3

* **AWS account and region.** Choose `us-east-1` (N. Virginia), `us-east-2` (Ohio), `us-west-1` (N. California), or `us-west-2` (Oregon). You need permission to deploy the CloudFormation stack and its named IAM resources, and to use a scoped policy for the setup session.
* **Private access.** For a first install, use the release's Tailscale router if you can approve subnet routes and configure DNS in your tailnet. You can also bring a private connection such as a VPN or Direct Connect. Choose a VPC address range that does not overlap networks your clients already use. [Private access options]({{ '/private-access/' | relative_url }})
* **Local tools.** Install AWS CLI v2 and the Session Manager plugin. The guided installer also needs Bash, `curl`, `jq`, OpenSSL and `tar`. Use a local terminal on the computer where you'll open setup. [AWS CLI installation](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html), [Session Manager plugin installation](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
* **AWS costs.** Plan for an EC2 host for the ShiftBox controller and PostgreSQL, encrypted EBS storage, private DNS, and a separate EC2 instance and disk for each running workspace. The RC3 template creates a VPC with private subnets and an outbound NAT gateway by default. Selecting Tailscale on RC3 adds a router instance.

Confirm your terminal is using the account you intend to deploy into. If you use a named AWS CLI profile, set `AWS_PROFILE` and sign in with it first.

```sh
aws sts get-caller-identity --query Account --output text
aws --version
session-manager-plugin --version
```

## Use the guided CLI

Use this path to review each CloudFormation change set in your terminal. The installer verifies the release assets, advances the same stack through its setup phases, and prepares the database TLS certificate.

1. Download the pinned installer and its manifest into a directory you control. Compare the two SHA-256 values printed below, then inspect `install.sh`.

   ```sh
   tag=v1.0.0-rc.3
   release_url="https://github.com/jflatten/ShiftBox/releases/download/$tag"
   curl -fLS "$release_url/install.sh" -o install.sh
   curl -fLS "$release_url/distribution-manifest.json" -o distribution-manifest.json
   jq -r '.installer.sha256' distribution-manifest.json
   openssl dgst -sha256 install.sh
   ```

2. Run the installer with the same release tag and a new work directory. Keep this directory private. It holds the pinned artifacts, install state, and generated TLS material.

   ```sh
   bash ./install.sh --version "$tag" --work-dir ./shiftbox-aws-install
   ```

3. Enter the AWS account, region, deployment name and Availability Zone when prompted. For the default Tailscale path, the installer chooses a private DNS suffix and prints it. You can leave the optional GitHub App secret blank and connect a repository after the stack is active.
4. Acknowledge the creation of named IAM resources with `CAPABILITY_NAMED_IAM` when prompted. Review each change set before typing its `EXECUTE <phase>` confirmation. When the installer prints `SetupOperatorPolicyArn`, have an authorized AWS administrator grant that policy to your bootstrap identity. For Tailscale, follow the router and browser CA instructions the installer prints. [Connect with Tailscale]({{ '/private-access/' | relative_url }}#connect-with-tailscale)
5. Complete the [protected browser setup](#complete-the-protected-browser-setup). After the helper confirms the setup task stopped, review and approve the final `active` change set. The installer prints your private management URL.

If the installer is interrupted, resume with the same AWS identity and work directory. The installer reads the pinned version from its state.

```sh
bash ./install.sh --work-dir ./shiftbox-aws-install --resume
```

## Launch from the AWS Console

Use this path if you want to configure and review each stage in the browser. The final setup still uses a local terminal.

1. Open the [regional launch links](#releases-heading) for your selected release. In CloudFormation, confirm the account, region and versioned template. Download `shiftbox-aws-cloudformation.yaml`, `shiftbox-aws-support.tar.gz` and `distribution-manifest.json` from the same release and compare the template and archive SHA-256 values with the manifest. Extract the verified support archive.
2. Create the stack with `Phase=foundation`, a unique deployment name and private DNS suffix, an available Availability Zone, and a nonoverlapping VPC CIDR. Keep `RoleBoundaryMode=managed`. For Tailscale, set `AccessMode=tailscale` and keep `OperatorCidr` at its default. For your own connection, set `AccessMode=private-link` and supply the approved source CIDR as seen from AWS. Review and acknowledge named IAM resources. Enable termination protection and apply `stack-policy.json` from the archive after creation.
3. Create and review two separate update change sets on the same stack with the same template. Set `Phase=dns-zones` first and check that the private zones belong to the stack's VPC. Next set `Phase=dns-services` and check that the `postgres` and `shiftbox` discovery services appear in the `services.<suffix>` namespace. [AWS change sets](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-cfn-updating-stacks-changesets.html)
4. Issue a database certificate for the `PostgresHostname` output. In Secrets Manager, put its PEM certificate and key in the secret identified by `PostgresTlsSecretArn` using the fields `certificate` and `private_key`. Prepare the browser certificate with a separate issuer. [RC3 certificate commands and output checks](https://github.com/jflatten/ShiftBox/blob/v1.0.0-rc.3/platform/cloudformation/aws/README.md#prepare-database-tls-and-the-setup-task)
5. Review and execute the change set with `Phase=database` and the public database CA in `PostgresCaPem`. Confirm the change keeps the installation volume and Availability Zone, and wait for PostgreSQL. Grant the bootstrap identity the policy in `SetupOperatorPolicyArn`. Configure [private access and browser trust]({{ '/private-access/' | relative_url }}).
6. Run the archive's `setup.sh` with the exact stack ID, region and expected AWS account ID. Complete the [protected browser setup](#complete-the-protected-browser-setup).

   ```sh
   sh ./setup.sh STACK_ID AWS_REGION AWS_ACCOUNT_ID
   ```

7. Review and execute the `Phase=active` change set after the helper confirms the setup task stopped. When CloudFormation finishes, **Outputs** shows `ManagementOrigin`.

## Complete the protected browser setup

1. Keep the setup helper running. From a second local terminal, check the setup certificate against the SHA-256 fingerprint the helper prints.

   ```sh
   openssl s_client -connect 127.0.0.1:8444 </dev/null 2>/dev/null |
     openssl x509 -noout -sha256 -fingerprint
   ```

   Compare the hexadecimal digits, ignoring case and colons. Open the helper's loopback HTTPS link once they match. This temporary certificate is separate from the certificate used at `ManagementOrigin`.
2. Create the first administrator. Save the temporary password, sign in, change it when prompted, then sign in again with the new password.
3. Use **Validate database** for the deployed PostgreSQL profile. Upload your browser **Certificate chain** and **Private key** and select **Validate certificate**. On the guided Tailscale path, use `browser-tls/leaf.pem` and `browser-tls/leaf.key` from the work directory.
4. Set and save workspace capacity, then select **Activate installation**. Close the tunnel after activation. Wait for the helper to confirm that the protected setup task is stopped before approving the `active` change set.

## Open the first workspace

Connect through your private network, open `ManagementOrigin`, and sign in with your administrator email and new password. A working installation shows the ShiftBox console over trusted HTTPS. [Validate the workspace image, connect GitHub and open the browser IDE]({{ '/first-workspace/' | relative_url }})

## Clean up a trial installation

When you're done with a disposable install, push any workspace changes you want to keep. Delete trial workspaces from ShiftBox before deleting the stack so the controller can clean up their EC2 resources. Back up any management data you want to keep. After verifying that you selected the trial stack, disable termination protection and delete it. Check the retained EBS installation volume and database secrets separately. Delete those only after confirming their data is no longer needed. Remove any trust-store entry for the trial's private browser CA. [Backup and recovery]({{ '/upgrade-recovery/' | relative_url }})
