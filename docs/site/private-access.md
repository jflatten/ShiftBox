---
layout: default
title: Private connectivity and browser trust
permalink: /private-access/
---

# Configure private access

Your browser needs a route to the existing service subnet, DNS for the ShiftBox installation suffix, and trust in the browser certificate. The management console, IDE, previews, and repair pages all use HTTPS on TCP 8443. The staged setup wizard uses a separate local Session Manager tunnel on TCP 8444.

## Route and resolve ShiftBox

1. Connect your computer to the VPC through your VPN, Direct Connect connection, or existing Tailscale subnet router. Confirm the service subnet has a return path to the client. For Tailscale, advertise and approve the service subnet's route on your existing router. [Tailscale subnet routing](https://tailscale.com/docs/features/subnet-routers)
2. Set `OperatorCidr` to the IPv4 source CIDR AWS sees for those browser connections. Limit access over the private connection to the clients that need TCP 8443. Check that network ACLs allow the traffic and its return path.
3. Forward queries for `DnsSuffix` to a VPC-side resolver your clients can reach. You can use a Route 53 Resolver inbound endpoint or an existing DNS forwarder in the VPC. With Tailscale, use a restricted nameserver for the installation suffix on a reachable DNS forwarder. [Route 53 inbound DNS forwarding](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resolver-forwarding-inbound-queries.html), [Tailscale DNS](https://tailscale.com/docs/reference/dns-in-tailscale)
4. After activation, look up the hostname from `ManagementOrigin` on your client. It should resolve to a private address. The same DNS forwarding serves the per-workspace IDE, preview, and repair names.

## Trust the browser certificate

For staged setup, use a certificate covering `shiftbox.<suffix>`, `*.ide.<suffix>`, `*.preview.<suffix>`, and `*.repair.<suffix>`. Use a certificate trusted by your clients or issue one from a private browser CA. Upload the browser certificate and leaf key in the protected setup wizard. PostgreSQL uses separate trust material. [Certificate commands](https://github.com/jflatten/ShiftBox/blob/main/platform/cloudformation/aws/README.md#connect-privately-and-complete-the-wizard)

For the one-pass development template, use the public CA in the secret identified by `BrowserCaSecretArn`. Verify the public CA certificate's SHA-256 fingerprint against the value from your trusted operator before importing it. Add the CA to the OS or browser trust store of each participating device.

Open `ManagementOrigin` from the connected client. Verify that the browser shows the ShiftBox sign-in page over trusted HTTPS. [Troubleshoot access]({{ '/troubleshooting/' | relative_url }})
