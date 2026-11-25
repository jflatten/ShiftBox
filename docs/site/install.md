---
layout: default
title: Install ShiftBox on AWS
permalink: /install/
---

# Install ShiftBox in an existing AWS VPC

Use the AWS Console to create ShiftBox in your private network, then finish setup in your browser. When you’re done, you can sign in as the first administrator and launch a workspace.

For a public browser URL with automatic HTTPS, use the [public access settings](#public-browser-access-with-lets-encrypt).

## Before you begin

* **AWS access.** You need permission to create a CloudFormation stack, let it create the roles it needs, and read the setup claim from AWS Secrets Manager. Private installations that use the generated browser CA also need access to that certificate.
* **An existing network.** Choose a virtual private cloud (VPC) and two private subnets in it. One subnet runs the ShiftBox application and its database. The other runs your workspaces. You’ll connect to ShiftBox through your VPN, Tailscale, or another private connection.
* **AWS costs.** The stack adds an EC2 server, disks, and private DNS records. Each workspace adds its own server and disk.

## Prepare your VPC and subnets

1. In **VPC → Your VPCs**, confirm that the VPC has DNS resolution and DNS hostnames enabled. [AWS VPC DNS settings](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-dns-updating.html)
2. Identify the service and workspace subnets in that VPC. Both need outbound access to AWS and container registries. The workspace subnet also needs access to your source code and package registries.
3. For private access, arrange private routing and DNS forwarding. Get the IPv4 source range your connection uses. Your default internal DNS suffix is the stack name followed by `.internal`. [Private routing and DNS]({{ '/private-access/' | relative_url }})

## Get the installation files

Download and extract `shiftbox-aws-support.tar.gz` from the release. In its `launch` directory, choose `shiftbox-aws-private.json` for private access or `shiftbox-aws-public.json` for a domain with Let’s Encrypt. If several public subnets are eligible, choose `shiftbox-aws-public-placement.json` to select one explicitly. [ShiftBox downloads](https://github.com/jflatten/ShiftBox/releases)

## Create the CloudFormation stack

1. In the AWS Console for your VPC’s Region, open **CloudFormation → Create stack → With new resources (standard)**. Choose **Upload a template file** and select your launch file. [Create a stack in the AWS Console](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stack.html)
2. Enter your deployment name as the **Stack name**. Use 2–32 lowercase letters, numbers, or hyphens. Start with a letter and end with a letter or number. Choose the service and workspace subnets from the menus. The stack looks up their VPC and uses the service subnet’s Availability Zone.
3. For private access, enter the **Private browser source IPv4 range**. For public access, complete the [public browser options](#public-browser-access-with-lets-encrypt) in the same form. Leave the storage and DNS suffix defaults in **Advanced** unless your installation needs different settings.
4. Review the change set, acknowledge the named IAM resources, and create the stack. Wait for **CREATE_COMPLETE**. You can enable termination protection under **Stack actions**.

## Retrieve the certificate and admin claim

1. Open the stack’s **Outputs** tab. Keep it open so you can refer to **SetupClaimSecretArn** and **ManagementOrigin**.
2. If the stack publishes **BrowserCaSecretArn**, open **Secrets Manager** in the same AWS Region. Find the secret with that ARN and select **Retrieve secret value**. Copy the full certificate, including the `BEGIN CERTIFICATE` and `END CERTIFICATE` lines. Save it as a plain-text file named `shiftbox-ca.crt` and trust it on your computer. [Trust the browser certificate]({{ '/private-access/' | relative_url }}#trust-the-browser-certificate)
3. Find the secret whose ARN matches **SetupClaimSecretArn** and select **Retrieve secret value**. Copy the claim code for the next step. Treat it like a password and keep it out of URLs, chat, and screenshots. [View a secret in the AWS Console](https://docs.aws.amazon.com/secretsmanager/latest/userguide/retrieving-secrets-console.html)

## Finish setup in ShiftBox

1. Open the address listed under **ManagementOrigin** from an allowed network. On **Set up ShiftBox**, paste the claim code, enter your email, and choose a password. Select **Create administrator**.

   ![The ShiftBox setup form with fields for the setup claim code, administrator email, and new password.]({{ '/assets/setup-administrator.png' | relative_url }})

2. Sign in with the email and password you just entered.
3. Select **Validate database**, then **Validate certificate** on the next screen. ShiftBox checks the database and certificate it already set up.
4. Select **Save capacity** after reviewing the workspace limits. On **Activate installation**, confirm that you’re ready to sign in again, then select **Activate installation**.

   ![The final ShiftBox setup screen with the sign-in confirmation checkbox and Activate installation button.]({{ '/assets/setup-activate.png' | relative_url }})

5. Wait for **ShiftBox is online**, then sign in again at the same address. [Launch your first workspace]({{ '/first-workspace/' | relative_url }})

## Clean up a trial installation

Push any workspace changes you want to keep. Delete the trial workspaces in ShiftBox first, then back up any settings or data you need. In CloudFormation, verify that you selected the trial stack before turning off termination protection and deleting it. Review its retained volume and secrets separately, and remove the trial certificate from your computer’s trust settings. [Backup and recovery]({{ '/upgrade-recovery/' | relative_url }})

For older templates that use the separate setup task, use the release-matched setup helper. [Legacy setup helper]({{ '/install/setup-helper/' | relative_url }})

## Public browser access with Let’s Encrypt

Use these settings on a fresh CloudFormation installation. The controller, PostgreSQL, and workspaces run in private subnets. An internet-facing Network Load Balancer adds a static IPv4 address and forwards HTTPS to the controller. The load balancer and its address add AWS charges. DNS validation and certificate renewal run inside the controller. [Network Load Balancer pricing](https://aws.amazon.com/elasticloadbalancing/pricing/) · [Public IPv4 pricing](https://aws.amazon.com/vpc/pricing/)

| Field | What to select |
| --- | --- |
| **Browser access** | Choose **Public**. Choose **Private** if you want a Let’s Encrypt certificate for a VPC-only browser URL. |
| **Public Route 53 hosted zone** | Select the authoritative public zone in this account. For stack `rc3` and zone `shiftbox.dev`, the browser URL is `https://rc3.shiftbox.dev`. |
| **Public browser source IPv4 range** | Enter the source range allowed through the load balancer. Use `/32` for one IPv4 address. |
| **Let’s Encrypt contact email** and **Accept Let’s Encrypt subscriber agreement** | Enter your contact email and explicitly accept the subscriber agreement. [Let’s Encrypt legal repository](https://letsencrypt.org/repository/) |

The stack uses the only eligible internet-routed subnet in the service subnet’s VPC and Availability Zone. If several qualify, use the explicit-placement launch file to pick one. In **Advanced**, use **DNS placement** to delegate a child zone instead of creating records directly in the selected zone. ShiftBox reuses a matching private zone associated with the VPC, or creates one when needed.

Verify that the public zone is authoritative before creating the stack. Wait for **CREATE_COMPLETE** while the controller obtains its certificate. Then retrieve the setup claim, open **ManagementOrigin** from an allowed IPv4 address, and finish setup in the browser. [First-login setup](#finish-setup-in-shiftbox)

The release installer also accepts `--public`. It filters the subnet choices, lists same-account public zones by name, and can propose this computer’s IPv4 address for you to confirm. It creates one reviewed CloudFormation change set.

Check the active certificate and last renewal result under **Administration → Settings → Certificates**. The controller retries failed renewals. Browsers use their normal trust stores for Let’s Encrypt certificates.

To switch to private access, first allow your private IPv4 source range and verify the same management URL from a client that uses VPC DNS and can reach TCP 443. Then review a stack update with **Browser access** set to **Private**. Keep the selected public hosted zone for certificate renewal. The update removes the public browser records, load balancer, and Elastic IP. [Private connectivity]({{ '/private-access/' | relative_url }})
