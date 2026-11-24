---
layout: default
title: Install ShiftBox on AWS
permalink: /install/
---

# Install ShiftBox in an existing AWS VPC

Deploy the ShiftBox configuration from `main` into your AWS network. The service subnet hosts the ShiftBox controller and PostgreSQL. The workspace subnet hosts a separate EC2 instance for each workspace. You provide routing and outbound connectivity. ShiftBox creates workload security groups and private DNS records.

## Before you begin

* Use an AWS identity that can create CloudFormation stacks, acknowledge named IAM resources, and work with the stack's Secrets Manager entries. For protected setup, install AWS CLI v2, `jq`, OpenSSL, and the Session Manager plugin on the computer where you will open the setup wizard. [AWS CLI installation](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html), [Session Manager plugin installation](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
* Obtain a rendered CloudFormation template from `main` for your account. Choose a Region supported by your template and the setup helper. The staged helper supports `us-east-1`, `us-east-2`, `us-west-1`, and `us-west-2`. Keep the matching stack policy, setup helper, and regional workspace image catalog. For the Ohio testing environment, the source runbook shows how to generate those files. [Preparing deployment files from source](https://github.com/jflatten/ShiftBox/blob/main/platform/development/README.md#render-a-baseline)
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

Use the steps below for a template with a `Phase` parameter. For the Ohio testing template that starts the service in one stack operation, use [Set up from the private browser](#set-up-from-the-private-browser).

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
6. Connect over your private route. If you use a private browser CA, import its verified public certificate. Open `ManagementOrigin` and sign in with the administrator's new password. [Browser trust and connectivity checks]({{ '/private-access/' | relative_url }})

## Set up from the private browser

Use this path if you're deploying the one-pass development template in the authorized Ohio testing account. Generate its CloudFormation file and stack policy from `main`. [Build and render the one-pass template](https://github.com/jflatten/ShiftBox/blob/main/platform/development/README.md#install-once-and-finish-in-the-private-ui)

1. Upload the rendered template in CloudFormation and enter your existing VPC, subnet IDs, service subnet Availability Zone, DNS suffix, and approved browser source CIDR. Apply the generated stack policy, enable termination protection, and review the changes and named IAM capabilities before creating the stack.
2. After **CREATE_COMPLETE**, use `BrowserCaSecretArn` from **Outputs** to retrieve the public browser CA from Secrets Manager. Trust that CA on your client. Retrieve the separate claim from `SetupClaimSecretArn` and keep it in protected storage.
3. Open `ManagementOrigin` over your private route. Enter the claim, your administrator email, and your new password. Sign in with that account.
4. Validate the preconfigured database and browser certificate, save workspace capacity, and activate. When ShiftBox reports it is online, sign in again at the same URL.

## Open the first workspace

Use the workspace catalog and a resource profile to launch a project in the browser IDE. The one-pass development setup supplies starter profiles on an empty installation. [Launch your first workspace]({{ '/first-workspace/' | relative_url }})

## Clean up a trial installation

Push any workspace changes you want to keep. Delete the trial workspaces from ShiftBox first so the controller can clean up their EC2 resources. Back up any management data you need. Verify the trial stack before disabling termination protection and deleting it. Review the retained EBS volume and database secrets separately. For one-pass setup, also review the retained setup claim and browser CA secrets. Remove the trial CA from your client trust store. [Backup and recovery]({{ '/upgrade-recovery/' | relative_url }})
