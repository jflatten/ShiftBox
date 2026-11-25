---
layout: default
title: Network access and browser trust
permalink: /private-access/
---

# Network access and browser trust

A browser connects to the controller for the management UI, workspace IDEs, previews and repair pages. ShiftBox checks the user’s session and permissions at those endpoints. Developers can use a workspace through the browser without a direct network route to its virtual machine.

## Route and resolve ShiftBox

| Connection | What DNS should return | What the network must allow |
| --- | --- | --- |
| Public HTTPS | From outside the VPC, the deployment name and its IDE, preview and repair names resolve to the load balancer’s Elastic IP. | TCP 443 from the **Public browser source IPv4 range**. Use the hostname in **ManagementOrigin** so the certificate matches. |
| Private with Let’s Encrypt | A resolver inside the VPC returns the private controller destination for the same browser names. | A private route to the service subnet and TCP 443 from the **Private browser source IPv4 range**. |
| Private with the installation CA | The management name is `shiftbox` under your private DNS suffix. Workspace browser names are under `ide`, `preview` and `repair` within that suffix. | A private route to the service subnet and TCP 8443 from the **Private browser source IPv4 range**. Trust the installation CA on each client. |

For either private option, connect your existing VPN or subnet router and arrange forwarding of DNS queries to a resolver that can answer for the VPC. With Let’s Encrypt, forward the selected browser domain and ensure the resolver can follow its records to the internal service names. With the installation CA, forward the private suffix. Keep the connection active when GitHub redirects your browser back to ShiftBox. [AWS private DNS forwarding](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resolver-forwarding-inbound-queries.html) · [Using an existing Tailscale subnet router](https://tailscale.com/docs/features/subnet-routers)

**If you already use the domain inside this VPC**, check its other hostnames too. The managed-certificate forms create or reuse a private zone for the base domain, even when public records go in a delegated child zone. The VPC resolver uses that private zone for matching names. If a record exists only in the public zone, clients using this resolver can get a name lookup failure. Use a dedicated delegated domain for ShiftBox or maintain the other records needed by private clients. [How Route 53 selects a hosted zone](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/hosted-zone-private-considerations.html)

Workspace machines contact the controller privately to enroll and connect their IDEs. The stack allows this traffic from the workspace security group on TCP 19443–19447 and limits PostgreSQL access to the service network. Outbound access from the workspace subnet determines which repositories, registries and package sources a project can use.

## Switch an existing public endpoint to private access

1. Arrange private routing and DNS for all browser names. Update the same stack to allow the **Private browser source IPv4 range** while **Public** access is still enabled.
2. From a private client, confirm that **ManagementOrigin** resolves to the private destination. Sign in and open a workspace IDE on that route.
3. Review and execute a change set on the same stack with **Browser access** set to **Private**. Keep the public DNS zone and certificate account settings for automatic renewal. The change removes the public browser records, load balancer and Elastic IP.
4. Reopen **ManagementOrigin** from the private client. The same hostname should work over the private connection.

## Trust the browser certificate

If your installation uses Let’s Encrypt, open **ManagementOrigin** using your browser’s normal trust store. The following steps apply to deployments that issue their own browser CA.

Use the `shiftbox-ca.crt` file you saved from the **BrowserCaSecretArn** secret for your stack. A certificate authority (CA) certificate lets your browser verify the identity of your private ShiftBox site. Import it on each computer that will use ShiftBox, or ask your IT team to distribute it.

* **Mac.** Open **Keychain Access** and import the file into your login keychain. Open the imported certificate, expand **Trust**, and set **When using this certificate** to **Always Trust**. [Change certificate trust in Keychain Access](https://support.apple.com/en-ca/guide/keychain-access/kyca11871/mac)
* **Windows.** Open the file and select **Install Certificate**. Choose **Current User**, then **Place all certificates in the following store** and select **Trusted Root Certification Authorities**. [Import a trusted root certificate on Windows](https://learn.microsoft.com/en-us/skype-sdk/sdn/articles/installing-the-trusted-root-certificate)
* **Other devices or managed computers.** Ask your IT team how to add the certificate to the trust store used by your browser.

Open **ManagementOrigin** from the CloudFormation stack’s **Outputs** tab. Your browser should show ShiftBox over a trusted HTTPS connection. [Troubleshoot access]({{ '/troubleshooting/' | relative_url }})
