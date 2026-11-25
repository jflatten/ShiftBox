---
layout: default
title: Private connectivity and browser trust
permalink: /private-access/
---

# Connect to ShiftBox privately

Connect your computer to the AWS network before opening ShiftBox. Your organization may use a VPN, an existing Tailscale router, or another private connection.

## Route and resolve ShiftBox

Ask your network administrator to confirm these settings.

1. Your computer can reach the subnet that runs ShiftBox over HTTPS. Use port 443 for installations configured with Let’s Encrypt and port 8443 for the private setup with operator-managed certificates.
2. The source IPv4 address range that AWS sees for that connection matches the range entered in **Inbound security group rule for UI access**.
3. Your network sends requests for the browser domain to a DNS resolver that can look up private names in the VPC. Use `BaseDomain` for Let’s Encrypt deployments or the private `DnsSuffix` for operator-managed deployments. The same setup lets your browser find ShiftBox, workspace IDEs, and previews. [AWS private DNS forwarding](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resolver-forwarding-inbound-queries.html), [Tailscale subnet routing](https://tailscale.com/docs/features/subnet-routers)

## Trust the browser certificate

If your installation uses Let’s Encrypt, open **ManagementOrigin** using your browser’s normal trust store. The following steps apply to deployments that issue their own browser CA.

Use the `shiftbox-ca.crt` file you saved from the **BrowserCaSecretArn** secret for your stack. A certificate authority (CA) certificate lets your browser verify the identity of your private ShiftBox site. Import it on each computer that will use ShiftBox, or ask your IT team to distribute it.

* **Mac.** Open **Keychain Access** and import the file into your login keychain. Open the imported certificate, expand **Trust**, and set **When using this certificate** to **Always Trust**. [Change certificate trust in Keychain Access](https://support.apple.com/en-ca/guide/keychain-access/kyca11871/mac)
* **Windows.** Open the file and select **Install Certificate**. Choose **Current User**, then **Place all certificates in the following store** and select **Trusted Root Certification Authorities**. [Import a trusted root certificate on Windows](https://learn.microsoft.com/en-us/skype-sdk/sdn/articles/installing-the-trusted-root-certificate)
* **Other devices or managed computers.** Ask your IT team how to add the certificate to the trust store used by your browser.

Open **ManagementOrigin** from the CloudFormation stack’s **Outputs** tab. Your browser should show ShiftBox over a trusted HTTPS connection. [Troubleshoot access]({{ '/troubleshooting/' | relative_url }})
