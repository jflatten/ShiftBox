---
layout: default
title: Troubleshoot an AWS installation
permalink: /troubleshooting/
---

# Troubleshoot ShiftBox on AWS

Start at the first step that failed. For a stack failure, open **CloudFormation → Stacks → Events** and read the first failed resource’s reason. Later rollback messages usually describe cleanup.

## Stack and setup problems

| What you see | What to check |
| --- | --- |
| The archive has no `launch/` folder | Check the release’s template. If it starts at the foundation phase, use the instructions for that release. The revised public deployment requires the managed-certificate launch form. [Identify your installation files]({{ '/install/' | relative_url }}#get-the-installation-files) |
| CloudFormation rejects the stack name or subnets | Use 2–32 lowercase letters, digits or hyphens for the name, starting with a letter and ending with a letter or digit. Select service and workspace subnets from the same VPC. Check the account and Region. |
| Public subnet discovery fails | In the service subnet’s VPC and Availability Zone, look for a subnet with an active default IPv4 route to an internet gateway and at least eight free IPv4 addresses. If several qualify, use the explicit-placement form to pick one. |
| The certificate cannot be issued | Check the selected **public** hosted zone, its delegation at the registrar or parent, and the controller’s outbound HTTPS. A DNS-validated certificate needs publicly resolvable challenge records even if browser access is private. Check controller startup logs in CloudWatch for the failed challenge or Route 53 permission. |
| Secrets Manager denies access or a setup secret is empty | Wait for **CREATE_COMPLETE**. Check the account and Region against the stack and ask for read access to the specific secret identified in **Outputs**. If empty after completion, check the service host and controller events. |
| ShiftBox rejects the setup claim | Retrieve the complete value from **SetupClaimSecretArn**. If account creation was interrupted, retry with the same email and password. If the administrator already exists, sign in with those credentials. |
| **Validate database** fails | Record the request reference on screen. Check that PostgreSQL is healthy in Amazon ECS, then look up that request in the controller logs. Validation checks the database hostname and TLS trust as well as the separate ShiftBox and Dex schema logins. Use the matching current template and controller build for a fresh installation. |
| Activation appears to stall | Keep the setup tab open. Check controller service events and logs in ECS or CloudWatch. When **ShiftBox is online** appears, sign in again at **ManagementOrigin**. |

## Browser and workspace problems

| What you see | What to check |
| --- | --- |
| The browser cannot find ShiftBox | Open the exact **ManagementOrigin**. For public access check public DNS from an external client. For private access check VPN connectivity and DNS forwarding for the browser domain. Check a workspace hostname too if only the IDE fails. [Network access and browser trust]({{ '/private-access/' | relative_url }}) |
| The browser finds the address but times out | For public HTTPS, check the load balancer’s target health, port 443 and your current public exit IP against the allowed range. For private access, check the route and private source range. Use port 443 with Let’s Encrypt or 8443 with the installation CA. |
| The browser reports an untrusted certificate | Use the hostname, including the port shown in **ManagementOrigin**. For an installation CA, import the certificate for this stack on the affected device. For Let’s Encrypt, check the served hostname, certificate expiry and last renewal result under **Settings → Certificates**. [Trust the browser certificate]({{ '/private-access/' | relative_url }}#trust-the-browser-certificate) |
| The project form offers no resource profiles | An administrator can validate the release image for the installation’s Region and enable a profile for it. [Create the first resource profile]({{ '/configure/' | relative_url }}#when-the-resource-profile-list-is-empty) |
| A repository is unavailable or launch asks you to reconnect | Check that the installation’s GitHub App can access the repository. In **Account**, connect or reconnect the GitHub user who will own the workspace. Check the project’s branch and Dev Container file path. [Connect GitHub and launch]({{ '/first-workspace/' | relative_url }}) |
| The IDE is slow to open or a workspace fails to start | Open the workspace’s **Performance** tab to see which step took time on this launch or start. Use **Logs** for the error and verify that the workspace subnet can reach its source, image and package registries. Missing guest samples can indicate an older guest agent. |
| Public images appear in the cache but a built image is missing | In **Infrastructure → Cache**, check **Built devcontainer images** is on and available, and that the disk and cache budget have space. A fresh launch or an explicit rebuild can publish a finished image after the IDE becomes ready. Check that the Dev Container configuration still matches the project’s configured Git source. An ordinary stop/start reuses the existing workspace. [Understand the two cache settings]({{ '/configure/' | relative_url }}#image-caching) |

When reporting a problem, include the ShiftBox version, AWS Region, failed step, request reference if displayed and the error message. Remove credentials, private URLs and account or resource identifiers from public reports. [ShiftBox issues](https://github.com/jflatten/ShiftBox/issues)
