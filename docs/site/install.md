---
layout: default
title: Install ShiftBox on AWS
permalink: /install/
---

# Install ShiftBox in an existing AWS VPC

Create one CloudFormation stack, then finish setup in your browser. The stack runs the controller and PostgreSQL database on a shared host. When a developer launches a workspace later, the controller starts a separate private EC2 virtual machine for it.

## Get the installation files

Open the release you intend to install and choose its CloudFormation link for your Region. The link loads `shiftbox-aws-cloudformation.yaml` and fills in that release’s regional image pins. The same template supports both private and public browser access. Keep `shiftbox-aws-support.tar.gz` for the regional workspace image catalog and stack policy. [ShiftBox downloads](https://github.com/jflatten/ShiftBox/releases)

If you are using an earlier release whose template asks for **Phase**, follow the instructions included with that release. [Legacy setup helper]({{ '/install/setup-helper/' | relative_url }})

| How will your team connect? | Browser access | What to expect |
| --- | --- | --- |
| Over an existing private route | **Private** (default) | Let’s Encrypt verifies your authoritative public DNS zone. Users reach the controller privately on port 443. |
| Over the internet from approved addresses | **Public** | A Network Load Balancer listens on port 443 at a fixed public IPv4 address. The controller, database and workspace machines stay in private subnets. |

Prepare the network, choose the stack settings, then finish first login. [Network](#prepare-your-vpc-and-subnets) · [Stack settings](#create-the-cloudformation-stack) · [First login](#finish-setup-in-shiftbox)

## Prepare your VPC and subnets

- **AWS account and Region.** Use a Region covered by the release catalog (`us-east-1`, `us-east-2`, `us-west-1` or `us-west-2`). Your AWS identity needs permission to deploy the stack and its named IAM roles, upload the template and read the setup secret in Secrets Manager.
- **Service and workspace networks.** Identify the existing VPC and private subnets for the controller and workspaces. Enable VPC DNS resolution and DNS hostnames. Both subnets need an outbound path to AWS services and container registries. Workspaces also need to reach GitHub and any package sources the project uses. [AWS VPC DNS settings](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-dns-updating.html)
- **Browser connection.** For private access, arrange a route, DNS forwarding and the source IPv4 range your browser will use. For public access, identify the public IPv4 range your team connects from. The form discovers the load balancer subnet in the service subnet’s Availability Zone. Use a service subnet whose zone has exactly one eligible internet-routed subnet with at least eight free IPv4 addresses. [Private routing and DNS]({{ '/private-access/' | relative_url }})
- **Domain for Let’s Encrypt.** Create or select a public Route 53 hosted zone in the deployment account. A hosted zone holds the DNS records for a domain. Your registrar (or the parent DNS zone for a subdomain) must delegate to its assigned name servers. For an existing domain in use, preserve its other DNS records when moving DNS service. [Set up Route 53 for an existing domain](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/MigratingDNS.html)
- **Costs.** Budget for the always-on service host, disks, DNS, secrets and network traffic, plus compute and storage for each workspace. Public access adds the load balancer and its Elastic IP. Stopping a workspace releases its compute while its disk continues to incur charges. [Load balancer pricing](https://aws.amazon.com/elasticloadbalancing/pricing/) · [Public IPv4 pricing](https://aws.amazon.com/vpc/pricing/)

## Create the CloudFormation stack

1. Open the release’s CloudFormation link for the Region containing your subnets. If you upload `shiftbox-aws-cloudformation.yaml` yourself, copy **Release inputs** from that same Region’s release link. [Create a stack in the AWS Console](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stack.html)
2. Enter the stack name and select your subnets. Use the tables below to complete the access and storage fields.
3. Acknowledge the named IAM resources. Review the change set, then execute it to create the stack. Wait for **CREATE_COMPLETE**.
4. Open the stack’s **Outputs** tab. **ManagementOrigin** is the address you will use for both setup and regular sign-in. On a public installation, **PublicIPv4** shows the Elastic IP you can compare against your public DNS answer.

| Field | How to choose |
| --- | --- |
| **Release inputs** | The release link fills in the controller and PostgreSQL digests and the exact AMIs for the selected Region. Keep that value together with the template from the same release. |
| **Stack name** | Choose a lasting deployment name, 2–32 lowercase letters, digits or hyphens. Start with a letter and end with a letter or digit. With a managed certificate, this name becomes the first part of the browser address. |
| **Service subnet** | Pick the private subnet for the controller, database and their host. ShiftBox derives the VPC and Availability Zone from it. The installation disk lives in that zone. |
| **Workspace subnet** | Pick the private subnet in the same VPC where developer machines should run. Check its available addresses, outbound connectivity and EC2 instance types. |
| **Private browser source IPv4 range** | For private access, enter the source range AWS sees from your VPN or other private route. For example, `10.24.8.0/24` covers that network. For one client address use `/32`. In public mode this field is optional and also allows a private route when filled. |

### Browser access with Let’s Encrypt

For example, selecting the authoritative zone `example.com` and naming the stack `team` gives you `https://team.example.com`.

| Field | How to choose |
| --- | --- |
| **Browser access** | Choose **Public** to create the internet entry point. **Private** keeps browser traffic on your private route and still issues a publicly trusted certificate. |
| **Public Route 53 hosted zone** | Select your domain from the zones in this AWS account. Check that its public name servers match the delegation. The stack gets the domain name from the zone. |
| **Public browser source IPv4 range** | Required for public access. Use your office or VPN’s public exit range, or one public IP with `/32`. `0.0.0.0/0` lets any IPv4 address reach the sign-in page. |
| **Let's Encrypt contact email** | Enter a monitored address for the certificate account. An address on another domain works. DNS proves domain control. |
| **Accept Let's Encrypt subscriber agreement** | Select **true** if authorized to accept the agreement. Both managed-certificate options require acceptance. [Let’s Encrypt subscriber agreement](https://letsencrypt.org/repository/) |
| **DNS placement** (Advanced) | Keep **Use selected zone** to add the deployment’s records to your existing zone. **Delegate a child zone** creates a separate zone for the deployment name and adds its name servers to the selected parent. Choose this if you want that deployment’s public records grouped separately. |

On first boot, Certbot in the controller writes temporary DNS TXT records, obtains the certificate and later renews it automatically. The stack grants the required Route 53 permissions. The same certificate covers management, workspace IDEs, previews and repair pages. [How DNS certificate validation works](https://letsencrypt.org/docs/challenge-types/#dns-01-challenge)

To move a working public deployment behind a VPN later, keep the same hostname and test private access before removing its public address. [Switch a public endpoint to private access]({{ '/private-access/' | relative_url }}#switch-an-existing-public-endpoint-to-private-access)

The stack also reuses or creates a **private** browser zone for the selected base domain. If that domain already serves other applications, check how those names resolve inside this VPC before deployment. [Understand public and private DNS answers]({{ '/private-access/' | relative_url }}#route-and-resolve-shiftbox)

### Advanced storage and DNS options

| Field | Default | When to change it |
| --- | --- | --- |
| **Private DNS suffix** | Stack name plus `.internal` | Choose an unused lowercase suffix if your private DNS already uses that name. Keep it distinct from the public zone. Arrange private DNS forwarding for the names your clients use. |
| **DataVolumeSizeGiB** | 32 GiB | This encrypted disk holds installation state and the database and survives stack deletion. Choose more space if needed for that data. The allowed range is 20–16,384 GiB. Each workspace has a separate disk. |
| **OCI image cache size (GiB)** | 20 GiB | This separate disk holds reusable container images. Suggested starting sizes are 20 GiB for up to 5 active users, 50 for 6–20, or 100 for 21–50. Large or varied images need more space. The allowed range is 1–16,384 GiB. Review the usable cache budget in ShiftBox after activation. |

### Using the guided installer

If you prefer a terminal, use an authenticated AWS CLI v2 with CloudFormation event support, Bash, curl, jq, OpenSSL and tar. Download and inspect `install.sh` from the release. Run `bash ./install.sh --version RELEASE_TAG` for private access or add `--public` for public HTTPS. Replace `RELEASE_TAG` with the published tag. The installer supplies the same release inputs to the same template, lists eligible subnets and Route 53 zones, and can suggest your current public IP for confirmation. Review the change set, then complete the same browser setup below.

## Retrieve the certificate and admin claim

1. In **Outputs**, find **SetupClaimSecretArn**. This is the AWS identifier of a secret. Open **Secrets Manager** in the same account and Region, open that secret and choose **Retrieve secret value**. Copy the claim code. It authorizes creation of the first administrator, so handle it like a password. [Retrieve a secret in the AWS Console](https://docs.aws.amazon.com/secretsmanager/latest/userguide/retrieving-secrets-console.html)
2. If **BrowserCaSecretArn** appears, retrieve that separate secret. Save the entire public certificate, including its `BEGIN CERTIFICATE` and `END CERTIFICATE` lines, as `shiftbox-ca.crt`. Trust it on the computer you will use for setup. Browsers already trust the Let’s Encrypt option. [Trust the browser certificate]({{ '/private-access/' | relative_url }}#trust-the-browser-certificate)

## Finish setup in ShiftBox

1. Open **ManagementOrigin** from an allowed network. On **Set up ShiftBox**, enter the claim, your administrator email and a new password twice. Use at least 12 characters and at most 72 UTF-8 bytes. Select **Create administrator**.

   ![First administrator form showing the setup claim, email and password fields.]({{ '/assets/setup-administrator.png' | relative_url }})

2. Sign in with the account you created. Select **Validate database**. ShiftBox checks the encrypted connection and the separate database logins for the application and its sign-in service, Dex.
3. Select **Validate certificate** to check that the installed certificate covers the displayed browser addresses.
4. In **Workspace capacity**, set total vCPU, memory and disk limits across workspaces, or leave any field blank for unlimited. Memory and disk use MiB (1 GiB = 1,024 MiB). Select **Save capacity**. You can revise these limits later. [Choose a capacity budget]({{ '/configure/' | relative_url }}#workspace-sizes-and-capacity)
5. On **Activate installation**, confirm that you are ready to sign in again, then select **Activate installation**.

   ![Activation screen with the sign-in confirmation checkbox and Activate installation button.]({{ '/assets/setup-activate.png' | relative_url }})

6. Wait for **ShiftBox is online** and sign in again at the same address. You are ready to connect GitHub and launch a project. [Launch your first workspace]({{ '/first-workspace/' | relative_url }})

## Clean up a trial installation

Push work you want to keep and delete the trial workspaces in ShiftBox. Make and verify a management backup if you need the installation settings. Record the installation volume ID and secret identifiers from the stack before you delete it. [Backup and recovery]({{ '/upgrade-recovery/' | relative_url }})

Confirm the account, Region and stack name, turn off termination protection if enabled, then delete the trial stack. After **DELETE_COMPLETE**, check EC2 for its retained installation volume and any remaining Elastic IP, and check Secrets Manager for its retained secrets. Remove only resources you have matched to the discarded installation. A fresh deployment with the same name may conflict with retained secrets. Keep shared DNS zones and remove the trial CA from your device if you trusted it.
