# Public Installation Contract

**Serves** US1, US3 and US4. Commands and paths in this contract are implementation targets.

## Regional console quick-create

GitHub Releases and Pages render four launch links from the selected public release's verified manifest. The version-pinned links use this shape, with the complete regional HTTPS S3 URL percent-encoded inside the console fragment:

```text
https://<region>.console.aws.amazon.com/cloudformation/home?region=<region>#/stacks/create/review?templateURL=<encoded-versioned-template-url>
```

The decoded template is `https://<bucket>.s3.<region>.amazonaws.com/<key>?versionId=<pinned-version>`. The console and object regions must match the selected entry. The inspected template specifies encrypted installation storage and selects the exact matching host and workspace pins. AWS Console shows the exact template for inspection, the caller's account and region, stack name, network and AZ parameters, IAM capability and stack options. Provide the matching template and checksum in the GitHub Release. A manual operator may use the versioned URL or upload the verified download. Links never carry credentials, secret values or private endpoints and must default to the foundation phase.

Choose the designated account and region, expand **View template**, confirm parameters and IAM capabilities, upload the release's stack policy to protect retained resources, enable termination protection and review the initial stack or preview change set. After launch, use the same stack's Outputs, Secrets Manager and documented local AWS CLI/Session Manager helper to complete setup. Use preview change sets for each update. Console users can complete the flow without Terraform or the guided installer. They still use the local Session Manager client for the protected browser tunnel.

AWS references: [Quick-create links](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stacks-quick-create-links.html), [versioned S3 templates](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stack.html).

## CLI version selection and downloads

From the canonical ShiftBox release repository:

```bash
# Default stable
curl -fsSL https://github.com/jflatten/ShiftBox/releases/latest/download/install.sh | bash -s --

# An explicit public candidate
curl -fsSL https://github.com/jflatten/ShiftBox/releases/download/v1.0.0-rc.1/install.sh | bash -s -- --version v1.0.0-rc.1
```

The tag in the second command is illustrative. Use the same explicit invocation for a pinned stable. The startup script supports `--version <public-tag>`, `--work-dir <path>`, `--resume` and `--help`. It asks for account, region, deployment name, AZ and private access choices through the operator terminal. If an interactive terminal is unavailable, stop with the download/inspect and manual instructions. The piped body must be received and parsed before it can reach deployment side effects. No options bypass CLI change-set review or IAM acknowledgement.

No version means the eligible published stable indicated by GitHub Releases. Confirm that it is a complete public stable with the expected tag, manifest and assets. If none exists, report that the operator can select an explicitly published RC. The stable download itself can return 404 before the first stable, so the docs give the RC entry before launch and explain the missing stable. Explicit selection resolves the independently verified public release by tag and checks the release's draft, prerelease and immutable status against the requested channel. No fallback between channels, packages or versions.

The canonical HTTPS repository and its approved release assets are the bootstrap trust root. Fetch expected assets by exact resolved tag, require the allowed HTTPS redirect destinations, validate manifest shape, check the downloaded template and support asset hashes, the exact S3 object version bytes and the selected catalog hash before use, and verify exact OCI and AWS publisher/region identities. Reject checksum mismatch, unsafe archive paths or links, unexpected publisher/package identity, or missing required inputs before provisioning. Record the resolved tag and digests once. The download/inspect path verifies the raw installer, template and support archive against manifest checksums before execution. Public package and release retrieval use anonymous access. The pinned manual route consumes the same manifest, template, validated stack changes and setup steps.

## Preflight, review and resume

1. In the CLI, check Bash, curl, jq, OpenSSL, AWS CLI v2 with `describe-events`, and the Session Manager plugin. Confirm normal AWS caller identity, expected account, commercial region, eligible AZ, unique stack name, routing/egress constraints and required role permissions. Verify exact image owner and pins and hosted template digest. If the operator delegates stack execution to an existing service role, show its scope and require appropriate `iam:PassRole`. Require `CAPABILITY_NAMED_IAM` acknowledgement for the named roles with per-role permissions boundaries.
2. Record nonsecret metadata in an operator-only directory with a local lock. Bind to account, region, stack name and verified CloudFormation stack ID, release, template version and checksum. CloudFormation's stack/operation/change-set IDs and current resource state are authoritative. Distinguish an unexecuted CREATE review stack, pending operation and real prior installation. Reconcile existing change sets, client tokens and stack outputs on timeout or interruption. A mismatched stack, in-progress transition or uncertain result blocks new side effects.
3. Create or inspect the foundation stage with the installed controller and database services stopped or absent. CLI creates a CREATE change set, checks `describe-change-set` and `describe-events` validation, and obtains explicit terminal approval before executing that ID. Enable termination protection and verify the protected logical resources under the supplied stack policy before proceeding. Preserve the stable encrypted EBS volume in its selected AZ, and set both `DeletionPolicy: Retain` and `UpdateReplacePolicy: Retain` for the installation volume and managed secrets. Hold replacement/removal of protected physical resources. Guide operator review of network/IAM scope and costs. If an optional stack service role is chosen, document that CloudFormation reuses it for later operations and restrict stack update permissions accordingly.
4. Review separate UPDATE change sets for `dns-zones` and `dns-services`. Verify and record the run VPC, private hosted zone and Cloud Map namespace before creating the services. Verify both services belong to that namespace. Preserve their physical identities on resume. CloudFormation can generate independent database passwords in scoped Secrets Manager resources. Provision the matching PostgreSQL certificate and key into the scoped TLS secret through the AWS Console or approved AWS CLI helper and supply only public DB CA and ARN references in stack inputs. Never put private key or password values into stack inputs, outputs, UserData, change sets or routine logs. Retain matching secrets and values on resume. Guide operator-owned GitHub App setup and any secret references required for repository checkout.
5. Create and review a validated UPDATE change set that adds the private DNS aliases and starts PostgreSQL with the matching retained volume and setup/task definitions available, leaving the normal controller stopped. Confirm service readiness. Grant only authorized setup access and start or reconcile the tagged ECS setup task for this stack. Forward HTTPS over SSM to local TCP 8444, verify the temporary fingerprint, complete initial administrator/password setup, select verified PostgreSQL, configure production browser TLS and capacity and activate. Verify durable activation and setup task termination before startup. A resumed setup preserves the same owner and installation.
6. Create and review the next validated UPDATE change set to start the normal controller. Recheck credentials, stack operation and exact change set before execution. The controller must fail closed if persisted bootstrap is incomplete. Wait for service readiness and print its private origin. On unexpected changes or validation failure, leave stack state intact and explain the next safe action. Both paths require review for later updates. Reject accidental stack/volume replacement and create a new change set after stale approval, changed inputs or partial operation.

## Private DNS, network and authority

| Path | Current deployment port/source | Expected boundary |
| --- | --- | --- |
| Setup HTTPS | TCP 8444 via a loopback SSM port forward | Scoped setup principal/session document and verified temporary fingerprint |
| Browser HTTPS | TCP 8443 from permitted private operator CIDRs or router security group | No public ingress, private task endpoint and validated browser certificate |
| Tailscale DNS | TCP and UDP 53 on the router's tailnet address | Restricted forwarder for the chosen suffix, permitted tailnet clients only |
| Tailscale router outbound | TCP 443 and UDP 3478 in current Terraform | Auth/control/relay and discovery according to the tailnet configuration |
| Guest paths | TCP 19443–19447 from the workspace security group | Separate enrollment, polling and tunnel trust |
| PostgreSQL | TCP 5432 from the controller/setup security group | Verified database TLS and scoped roles |
| Deployment egress | DNS and HTTPS through the existing private subnets' outbound path | AWS APIs, SSM, public image pulls and required dependencies, with no inbound internet access |

For Tailscale, register the router through the authorized tailnet UI, approve the service subnet route, configure the restricted nameserver and client grants and verify no conflicting routes. Tailscale-only deployments can leave direct browser CIDRs empty; the dedicated router rule is always scoped. Generate a persistent installation-specific `sb-<random>.internal` suffix and a browser leaf covering `shiftbox.<suffix>`, `*.ide.<suffix>`, `*.preview.<suffix>` and `*.repair.<suffix>`. The operator compares the browser CA fingerprint and trusts it once per appropriate client store. Verify exact names and expiry with ordinary TLS checks. Document safe key storage, renewal and trust removal. Browser trust doesn't grant access to database or guest signing keys.

For existing private routing, the operator configures forward and return routes, the correct source CIDRs and conditional DNS to a reachable VPC-side resolver or equivalent, including the private wildcard names. Use operator-provided browser TLS or the documented scoped CA path. Describe route overlap, missing egress, public-origin rejection, DNS rebinding handling and certificate diagnosis without suggesting disabled verification or internet ingress.

## First workspace and existing installations

Use the normal signed-in admin interface to verify or import the configured official release image, watch validation and select a named pinned resource profile and a valid workspace template. Show actionable status on failed import. Reload to verify persistence and launch the known-good repository devcontainer. Document how to create and authorize an operator-owned GitHub App and connect the workspace owner's account for repository checkout. The required repository access is separate from downloading the distribution. Verify the workspace IDE over private HTTPS.

Manual installation reviews the exact template and change sets, retains the same stack and uses the same ECS/SSM and catalog flow. An existing installation uses the upgrade and recovery guide for payload changes and keeps its installation and backup lineage.
