---
layout: default
title: Private connectivity and browser trust
permalink: /private-access/
---

# Configure private access

Your browser needs a private route to the ShiftBox service subnet, DNS for the installation suffix, and trust in the HTTPS certificate. The management console, IDE, previews and repair pages all use TCP 8443. The temporary setup wizard uses a separate local Session Manager tunnel on TCP 8444.

These steps match the published RC3 template. It creates private service and workspace subnets and configures the workload security groups and private DNS records.

## Connect with Tailscale

1. Choose `AccessMode=tailscale` when installing. After the `database` phase, find `TailscaleRouterInstanceId`, `TailscaleAdvertisedRoute` and `DnsSuffix` on the stack's **Outputs** tab.
2. Open a Session Manager shell on the router with an AWS identity authorized to start that session. Replace `ROUTER_INSTANCE_ID` with the output value and `AWS_REGION` with the stack's region.

   ```sh
   aws ssm start-session --target ROUTER_INSTANCE_ID --region AWS_REGION
   ```

3. Run the following on the router. Replace `SERVICE_SUBNET_CIDR` with `TailscaleAdvertisedRoute`. Use the authorization link to join the router to your tailnet.

   ```sh
   sudo tailscale up --accept-dns=false --advertise-routes=SERVICE_SUBNET_CIDR
   ```

4. Approve the advertised service subnet route in the Tailscale admin console. Make sure your client accepts subnet routes. Limit access to the intended users and allow them TCP 8443 to the service subnet and TCP and UDP 53 to the router. [Tailscale subnet routing](https://tailscale.com/docs/features/subnet-routers)
5. Under **DNS**, add the router's Tailscale IPv4 address as a nameserver restricted to the stack's `DnsSuffix`. This forwards queries for ShiftBox to the VPC while your client keeps its normal settings for other names. [Tailscale DNS](https://tailscale.com/docs/reference/dns-in-tailscale)

## Use an existing private connection

Choose `AccessMode=private-link` and set `OperatorCidr` to the approved IPv4 source range the service sees. Arrange forward and return routes between your clients and the new service subnet through your VPN, Direct Connect or other private connection. Forward your installation suffix to a reachable Route 53 Resolver inbound endpoint or equivalent VPC-side resolver. This covers the management hostname and the per-workspace hostnames. [Route 53 inbound DNS forwarding](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resolver-forwarding-inbound-queries.html)

## Trust the browser certificate

Use a certificate that covers `shiftbox.<suffix>`, `*.ide.<suffix>`, `*.preview.<suffix>` and `*.repair.<suffix>`. For a private suffix you can use an installation-specific certificate authority (CA). The guided RC3 installer creates one for Tailscale under `browser-tls/`. For a console install, use the release's certificate commands or certificates from your organization. [RC3 browser certificate commands](https://github.com/jflatten/ShiftBox/blob/v1.0.0-rc.3/platform/cloudformation/aws/README.md#connect-privately-and-complete-the-wizard)

1. Compare the public CA certificate's SHA-256 fingerprint with the fingerprint from your installer or certificate administrator.
2. Import the public browser CA certificate, `ca.pem` on the guided path, into the OS or browser trust store on each participating device.
3. Upload the browser leaf certificate and key through the protected setup wizard. Keep the issuer key in protected storage for renewal. PostgreSQL uses a separate CA, and the loopback wizard uses its own fingerprint-verified certificate.

When the controller is active, open `ManagementOrigin` from the same client. The name should resolve to a private address and your browser should show the ShiftBox sign-in page over trusted HTTPS. [Troubleshoot access]({{ '/troubleshooting/' | relative_url }})
