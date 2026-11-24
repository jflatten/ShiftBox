---
layout: default
title: Set up ShiftBox with the setup helper
permalink: /install/setup-helper/
---

# Set up ShiftBox with the setup helper

Use this guide if your CloudFormation template has a `Phase` field. For the template that starts ShiftBox in one operation, follow the [AWS installation guide]({{ '/install/' | relative_url }}).

## Before you begin

* Use an AWS identity that can create CloudFormation stacks, acknowledge named IAM resources, and work with the stack's Secrets Manager entries. For protected setup, install AWS CLI v2, `jq`, OpenSSL, and the Session Manager plugin on the computer where you will open the setup wizard. [AWS CLI installation](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html), [Session Manager plugin installation](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
* Obtain the phased CloudFormation template and the matching support archive for your installation. Extract `setup.sh` and `stack-policy.json` from the archive. The setup helper supports `us-east-1`, `us-east-2`, `us-west-1`, and `us-west-2`.
* Budget for the EC2 service host, encrypted installation and image cache disks, private DNS, outbound traffic, and each workspace's EC2 instance and disk.

Confirm the account shown by your local AWS CLI matches the account selected in the console.

```sh
aws sts get-caller-identity --query Account --output text
```

## Prepare your VPC and subnets

1. In the VPC console, select your existing VPC. Confirm **DNS resolution** and **DNS hostnames** are enabled so services in the VPC can use the private DNS records. Copy the VPC ID. [AWS VPC DNS settings](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-dns-updating.html)
2. Select a private subnet for services and a private subnet for workspaces in that VPC. Record both subnet IDs and the service subnet's Availability Zone. The installation's EBS volume must be in the same Availability Zone as the service host.
3. Check the route table for each subnet. Provide outbound access to the AWS services the workloads use and to their container registries. Workspaces also need access to GitHub and any package sources used by the devcontainer. Use your existing egress path, such as a NAT gateway.
4. Arrange a private connection from your browser to the service subnet, such as a VPN or an existing Tailscale subnet router. Record the IPv4 source CIDR the service will see over that connection. Choose an unused private DNS suffix, such as `shiftbox-team.internal`, and arrange DNS forwarding for it. [Private routing and DNS]({{ '/private-access/' | relative_url }})

## Create the CloudFormation stack

1. In the AWS console for your Region, open **CloudFormation → Create stack → With new resources (standard)**. Choose **Upload a template file** and select your rendered template, or enter the approved S3 template URL. [Create a stack in the AWS console](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stack.html)
2. Name the stack and enter these parameters.

   | Parameter | Value |
   | --- | --- |
   | `DeploymentName` | A unique lowercase name for this installation. |
   | `Phase` | `foundation`. |
   | `VpcId` | Your existing VPC ID. |
   | `ServiceSubnet` | The subnet ID for the ECS host, controller and database. |
   | `WorkspaceSubnet` | The subnet ID for workspace instances. |
   | `AvailabilityZone` | The service subnet's Availability Zone. |
   | `DnsSuffix` | The private suffix you chose. |
   | `OperatorCidr` | The approved IPv4 source CIDR for browser access on TCP 8443. Leave it blank to keep browser access closed during setup. |
   | `RoleBoundaryMode` | `managed`. |
   | `OciImageCacheSizeGiB` | The shared image cache disk size. The default is 20 GiB. |

3. Enable termination protection and apply the supplied `stack-policy.json` to protect the installation storage and secrets. Review the changes and acknowledge named IAM resources. Submit the stack and wait for **CREATE_COMPLETE**. Record its stack ID, VPC and subnet IDs, and `InstallationVolumeId`.

## Start private DNS and PostgreSQL

Create the following update change sets on the same stack using the same template. Review each change set before execution and preserve the installation volume, Availability Zone, and existing network inputs. [AWS change sets](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-cfn-updating-stacks-changesets.html)

1. Set `Phase=dns-zones`. After the update, check that the Route 53 private hosted zones belong to your VPC. In AWS Cloud Map, verify the namespace is `services.<suffix>`.
2. Set `Phase=dns-services`. Verify that the `postgres` and `shiftbox` discovery services are in that namespace.
3. Read `PostgresHostname` and `PostgresTlsSecretArn` in the stack's **Outputs**. Issue a database TLS certificate for that hostname. In Secrets Manager, store the PEM certificate and private key in the named secret under the JSON keys `certificate` and `private_key`. Prepare a separate browser certificate and key for the setup wizard. [Certificate commands and private trust](https://github.com/jflatten/ShiftBox/blob/main/platform/cloudformation/aws/README.md#connect-privately-and-complete-the-wizard)
4. Set `Phase=database` and put the public database CA certificate in `PostgresCaPem`. For a single-line console entry, separate PEM lines with `|`. Wait for the PostgreSQL ECS service to be healthy. Have an authorized AWS administrator grant your bootstrap identity the policy in `SetupOperatorPolicyArn` and read access to the stack.

## Complete the protected browser setup

1. Run the matching setup helper in a local terminal. Replace the placeholders with the exact stack ID, Region, and expected AWS account ID.

   ```sh
   sh ./setup.sh STACK_ID AWS_REGION AWS_ACCOUNT_ID
   ```

2. Keep the helper running. In a second terminal, compare the following SHA-256 fingerprint with the fingerprint the helper prints. Compare the hexadecimal digits, ignoring case and colons. Open the helper's loopback HTTPS link when they match.

   ```sh
   openssl s_client -connect 127.0.0.1:8444 </dev/null 2>/dev/null |
     openssl x509 -noout -sha256 -fingerprint
   ```

3. Create the first administrator. Save the temporary password, sign in, change it when prompted, then sign in again with your new password.
4. Select **Validate database** for the deployed PostgreSQL profile. Upload your browser **Certificate chain** and **Private key** and select **Validate certificate**. Save workspace capacity and select **Activate installation**.
5. Close the setup tunnel after activation. Wait for the helper to confirm the setup task stopped. Review and execute a change set with `Phase=active` and your approved `OperatorCidr`. After the update, CloudFormation shows `ManagementOrigin` in **Outputs**.
6. Connect over your private route. If you use a private browser CA, import the verified public certificate from the authority that issued your browser certificate. Open `ManagementOrigin` and sign in with the administrator's new password. [Private network checks]({{ '/private-access/' | relative_url }}#route-and-resolve-shiftbox)

## Open the first workspace

In ShiftBox, open **Administration → Infrastructure → Platform**. Select the JSON file for your Region from the support archive's `catalog/` folder under **Official AMI catalog manifest**, then select **Validate official AMI**. Wait for validation to finish and clean up its temporary EC2 instance.

In **Administration → Infrastructure → Resources**, select **New resource profile**. Choose the validated **AMI image**, an EC2 instance type available in your workspace subnet, and a disk size at or above the displayed minimum. Save the profile, then follow the workspace guide to connect GitHub and open the IDE. [Launch your first workspace]({{ '/first-workspace/' | relative_url }}#configure-the-github-app)
