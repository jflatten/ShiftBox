---
layout: default
title: Private connectivity and browser trust
permalink: /private-access/
---

# Configure private access

Management, IDE, preview and repair origins share the private service network. Use the Tailscale subnet router or your organization's existing private routing and DNS.

## Tailscale

From the authorized AWS session, use the SSM client to connect to the `TailscaleRouterInstanceId` stack output. On that instance run `sudo tailscale up --accept-dns=false --advertise-routes=<service-cidr>` using the `TailscaleAdvertisedRoute` output. Authorize the device, approve the advertised route, and grant the intended clients access. Allow TCP and UDP 53 to the router and TCP 8443 to the service subnet. Keep the router's outbound HTTPS and discovery connectivity available to the tailnet.

Use the router's Tailscale IP as a restricted DNS nameserver for the chosen suffix. Enable acceptance of the service route and split DNS on participating clients. A suffix such as `sb-<random>.internal` works without a registered domain.

## Existing private routes

For a VPN or Direct Connect path, configure forward and return routes to the VPC and specify the approved client source CIDR for TCP 8443. Forward queries for the deployment suffix to a reachable VPC-side resolver, such as a Route 53 Resolver inbound endpoint configured by your operator. Confirm that the management origin and IDE, preview and repair names resolve to private addresses.

The temporary setup page uses local SSM forwarding on TCP 8444. PostgreSQL uses TCP 5432 inside the service network. Guest registration and tunnels use TCP 19443–19447 from the workspace security group. Preserve each path's scoped source rule.

## Browser and database trust

For a no-domain installation, create a private browser CA for this installation. Verify its fingerprint and import it once into each participating client's trust store. The browser certificate needs SANs for `shiftbox.<suffix>`, `*.ide.<suffix>`, `*.preview.<suffix>` and `*.repair.<suffix>`. The support archive's operator guide includes commands for a no-domain certificate. Supply the leaf certificate and key through the protected wizard. Keep the issuer key in protected operator custody for renewal and remove trust when you retire the installation.

Use a separate certificate and CA for `postgres.<suffix>`. Put database TLS private material in its scoped Secrets Manager secret. The guest trust authority and the fingerprint-verified temporary wizard certificate have separate lifecycles.
