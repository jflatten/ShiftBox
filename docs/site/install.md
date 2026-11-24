---
layout: default
title: Install ShiftBox on AWS
permalink: /install/
---

# Install ShiftBox in an existing AWS VPC

Use the AWS Console to create ShiftBox in your private network, then finish setup in your browser. When you’re done, you can sign in as the first administrator and launch a workspace.

## Before you begin

* **AWS access.** You need permission to create a CloudFormation stack, let it create the roles it needs, and read the two setup values in AWS Secrets Manager. If someone else manages your AWS account or network, ask them to help with these parts.
* **An existing network.** Choose a virtual private cloud (VPC) and two private subnets in it. One subnet runs the ShiftBox application and its database. The other runs your workspaces. You’ll connect to ShiftBox through your VPN, Tailscale, or another private connection.
* **AWS costs.** The stack adds an EC2 server, disks, and private DNS records. Each workspace adds its own server and disk.

## Prepare your VPC and subnets

1. In the AWS Console, open **VPC → Your VPCs**. Select your VPC and confirm **DNS resolution** and **DNS hostnames** are enabled. Note its VPC ID. [AWS VPC DNS settings](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-dns-updating.html)
2. Open **Subnets** and select two private subnets in that VPC. Note both subnet IDs and the **Availability Zone** of the subnet you’ll use for the ShiftBox application.
3. Ask your network administrator to confirm that both subnets can reach AWS services and download container images. The workspace subnet also needs access to GitHub and any package sources your project uses.
4. Choose a private name ending for this installation, such as `shiftbox-team.internal`. Ask your network administrator to set up DNS for it and give you the IPv4 address range allowed to reach ShiftBox from your private connection. [Private routing and DNS]({{ '/private-access/' | relative_url }})

## Get the installation files

Open the newest package on the ShiftBox downloads page and choose **Launch in** for the AWS Region that contains your VPC. This opens CloudFormation with the template already selected. If your organization requires you to upload it yourself, download `shiftbox-aws-cloudformation.yaml` from **Assets** on the same page. [ShiftBox downloads](https://github.com/jflatten/ShiftBox/releases)

## Create the CloudFormation stack

1. Confirm that the AWS Console shows the account and Region where your VPC lives. If you downloaded the template, open **CloudFormation → Create stack → With new resources (standard)** and choose **Upload a template file**. [Create a stack in the AWS Console](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stack.html)
2. Name the stack and fill in the installation and network fields.

   | Field | What to enter |
   | --- | --- |
   | **DeploymentName** | A unique name in lowercase, such as `shiftbox-team`. |
   | **VpcId** | The ID of your existing VPC. |
   | **ServiceSubnet** | The subnet ID for the ShiftBox application and database. |
   | **WorkspaceSubnet** | The subnet ID for workspaces. |
   | **AvailabilityZone** | The Availability Zone of the service subnet. |
   | **DnsSuffix** | The private name ending you chose. |
   | **Inbound security group rule for UI access** | The IPv4 address range from your network administrator. AWS uses it to allow browser access on port 8443. |
   | **OCI image cache size (GiB)** | Start with the default of 20 GiB for a small team. The disk stores copies of container images so workspaces can reuse them. |

3. Review the stack details and acknowledge the named IAM resources. Create the stack and wait for **CREATE_COMPLETE**. To protect it from accidental deletion, turn on termination protection under **Stack actions**.

## Retrieve the certificate and admin claim

1. Open the stack’s **Outputs** tab. Keep it open so you can refer to **BrowserCaSecretArn**, **SetupClaimSecretArn**, and **ManagementOrigin**.
2. Open **Secrets Manager** in the same AWS Region. Find the secret whose ARN matches **BrowserCaSecretArn** and select **Retrieve secret value**. Copy the full certificate, including the `BEGIN CERTIFICATE` and `END CERTIFICATE` lines. Save it as a plain-text file named `shiftbox-ca.crt`, then [trust this certificate on your computer]({{ '/private-access/' | relative_url }}#trust-the-browser-certificate).
3. Find the secret whose ARN matches **SetupClaimSecretArn** and select **Retrieve secret value**. Copy the claim code for the next step. Treat it like a password and keep it out of URLs, chat, and screenshots. [View a secret in the AWS Console](https://docs.aws.amazon.com/secretsmanager/latest/userguide/retrieving-secrets-console.html)

## Finish setup in ShiftBox

1. Connect to your private network and open the address listed under **ManagementOrigin**. On **Set up ShiftBox**, paste the claim code, enter your email, and choose a password. Select **Create administrator**.

   ![The ShiftBox setup form with fields for the setup claim code, administrator email, and new password.]({{ '/assets/setup-administrator.png' | relative_url }})

2. Sign in with the email and password you just entered.
3. Select **Validate database**, then **Validate certificate** on the next screen. ShiftBox checks the database and certificate it already set up.
4. Select **Save capacity** after reviewing the workspace limits. On **Activate installation**, confirm that you’re ready to sign in again, then select **Activate installation**.

   ![The final ShiftBox setup screen with the sign-in confirmation checkbox and Activate installation button.]({{ '/assets/setup-activate.png' | relative_url }})

5. Wait for **ShiftBox is online**, then sign in again at the same address. [Launch your first workspace]({{ '/first-workspace/' | relative_url }})

## Clean up a trial installation

Push any workspace changes you want to keep. Delete the trial workspaces in ShiftBox first, then back up any settings or data you need. In CloudFormation, verify that you selected the trial stack before turning off termination protection and deleting it. Review its retained volume and secrets separately, and remove the trial certificate from your computer’s trust settings. [Backup and recovery]({{ '/upgrade-recovery/' | relative_url }})

For templates that ask for an installation phase, see [Set up ShiftBox with the setup helper]({{ '/install/setup-helper/' | relative_url }}).
