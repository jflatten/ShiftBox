---
layout: default
title: Private connectivity and browser trust
permalink: /private-access/
---

# Configure private access

Management, IDE, preview and repair origins share the selected private service subnet. ShiftBox creates its installation security groups and private DNS records in the selected VPC. The operator provides routing, outbound connectivity and DNS forwarding.

Configure forward and return routes to the selected service subnet through your existing Tailscale subnet router, VPN, Direct Connect or equivalent. Verify the source IPv4 CIDR that the service sees and supply it as `OperatorCidr` to allow TCP 8443. Leave the parameter blank while browser ingress should be closed. For Tailscale, approve the service subnet route on your existing router and scope the grant to intended clients.

Forward the installation suffix to a reachable VPC-side resolver through your private connection. With Tailscale, the operator can configure split DNS using a restricted nameserver reachable through that connection. Confirm that the management origin and IDE, preview and repair names resolve to private addresses.

The temporary setup page uses local SSM forwarding on TCP 8444. PostgreSQL uses TCP 5432 inside the service network. Guest registration and tunnels use TCP 19443–19447 from the workspace security group. Preserve each path's scoped source rule.

## Browser and database trust

For a no-domain installation, create a private browser CA for this installation. Verify its fingerprint and import it once into each participating client's trust store. The browser certificate needs SANs for `shiftbox.<suffix>`, `*.ide.<suffix>`, `*.preview.<suffix>` and `*.repair.<suffix>`. The support archive's operator guide includes commands for a no-domain certificate. Supply the leaf certificate and key through the protected wizard. Keep the issuer key in protected operator custody for renewal and remove trust when you retire the installation.

Use a separate certificate and CA for `postgres.<suffix>`. Put database TLS private material in its scoped Secrets Manager secret. The guest trust authority and the fingerprint-verified temporary wizard certificate have separate lifecycles.
