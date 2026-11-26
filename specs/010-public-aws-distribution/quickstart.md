# Public AWS Distribution Validation Quickstart

Use these steps after implementation in authorized settings. The illustrative RC tag stands for the exact candidate under review.

## Validation environments and cleanup ownership

| Environment | Scope and owner |
| --- | --- |
| Local fixtures and normal CI | Implementers run unit, contract and template checks and remove local fixtures. Record skipped external cases with their required inputs. |
| Private ShiftBox development stack | Use the approved CloudFormation stack in `shiftbox-testing` and review its change set before rollout. Clean up the disposable records and resources created for the check. Follow the [AWS development guide](../../platform/development/README.md). |
| Public publisher and rehearsal context | The release maintainer owns the staged release assets and temporary publisher fixtures. Preserve any AMI, snapshot or S3 object version referenced by a release. Use an approved rehearsal context for test writes. |
| Separate disposable customer accounts | The qualification operator confirms account separation from the publisher and demo, creates the test stacks and workspaces, and verifies cleanup of owned compute, application security groups, retained EBS and secrets. Keep account IDs and inventory privately. |

The November 21, 2026 inventory reviewed the repository's recorded evidence. A read-only retry verified the default AWS profile in the approved demo account and both ECS services reported one running task and a completed rollout. This worktree has no local demo deployment files. The disposable customer accounts and public RC remain unverified. Before changing an environment, verify the approved account and region, the required state or stack inputs, and who owns cleanup. A release maintainer reviews any missing or failed gate against the exact candidate. Keep that gate pending until the required evidence is recorded.

## 1. Verify source and run local checks

From the reviewed RC source, verify baseline CI for the peeled commit. Use the candidate commit’s checked-in `platform/cloudformation/aws/template.yaml` as `release_template`.

```sh
git rev-parse 'v1.0.0-rc.1^{commit}'
make fmt-check vet test race
make ui-test build
go test -tags shiftbox_ui ./cmd/shiftbox -run '^TestEmbeddedBrowserAssets$'
cfn-lint --regions us-east-1 us-east-2 us-west-1 us-west-2 -- "$release_template"
cfn-guard test --rules-file platform/cloudformation/aws/shiftbox.guard --test-data platform/cloudformation/aws/tests/guard/template-tests.yaml
cfn-guard validate --rules platform/cloudformation/aws/shiftbox.guard --data "$release_template"
packer init platform/packer/aws
platform/packer/aws/test-recipe.sh
bash scripts/install-aws.test.sh
bash platform/release/release.test.sh
```

Verify standalone template layout, encrypted retained resources, restricted first-login bootstrap, least-privilege IAM, private access by default and no secret values and archive/privacy exclusions. Verify the release package contains the checked-in YAML byte for byte.

## 2. Rehearse publication and promotion

Use an approved rehearsal repository and publisher context. Observe ordinary branch/PR CI and verify zero package or release changes. Exercise an invalid version tag and a disallowed tag push. For a valid RC, inspect the public release ID, all four immutable assets, exact source and build identities, and anonymous OCI pull.

Exercise missing S3 or AMI regions, a failed asset upload, an already complete rerun and conflicting staged provenance. Rehearse promotion with the wrong tag SHA, wrong or absent evidence, wrong digest/asset IDs, wrong S3 object version or bytes, foreign/withdrawn/deprecated AMI, missing region, and an older rerun. In each negative case verify prior stable selection or its absence.

## 3. Validate four console links and launch four exact AMIs

For each region, decode the quick-create link and compare the console region, S3 endpoint and version ID to the manifest. Compare the decoded **Release inputs** with the regional pins supplied by the installer. Fetch that exact S3 version anonymously and compare SHA-256 against the GitHub inspection copy. Verify unsigned list/write and reads of a nonrelease object or unversioned alias are denied. In each cross-account context open the link, inspect the template and region, and create a disposable CREATE change set with reviewed inputs. Inspect the change set and CloudFormation validation events.

```sh
aws cloudformation validate-template --region "$release_region" --template-url "$versioned_template_url"
aws cloudformation describe-change-set --region "$release_region" --change-set-name "$change_set_arn"
aws cloudformation describe-events --region "$release_region" --change-set-name "$change_set_arn"
```

Exercise wrong-region, malformed encoding and changed object version cases. Verify the release tooling refuses mismatched links and the CLI never substitutes a different template. Discard unexecuted change sets and their review stacks.

From a separate account without a private image grant, launch the exact manifest AMI for each region using the approved disposable fixture, private networking and explicitly encrypted root. Observe actual boot and the volume.

```sh
aws sts get-caller-identity
aws ec2 describe-images --region "$release_region" --image-ids "$release_ami"
aws ec2 wait instance-status-ok --region "$release_region" --instance-ids "$validation_instance"
aws ec2 describe-volumes --region "$release_region" \
  --filters "Name=attachment.instance-id,Values=$validation_instance"
```

Verify approved owner, ID, snapshot, architecture, visibility, encrypted volume, no public IP and cleanup of instances, volumes and ENIs. Keep published AMIs, backing snapshots and S3 release object versions. Report a missing region or unresolved host/template validation as a qualification failure.

## 4. Run a fresh Ohio console install and exercise the guided CLI

The [on-demand qualification workflow](../../platform/qualification/README.md)
prices the run, verifies the public RC and regional probes, and provides
an independently cleaned Ohio window in the dedicated testing account.
Its automation evidence keeps the manual browser, CLI and recovery gates
pending. Confirm actual aggregate budget and Ohio capacity before dispatch.
For the staffed workflow, use its runbook to select existing Ohio subnets
and arrange operator-managed private routing, DNS and optional GitHub App
credentials.

Use the dedicated testing account, an authoritative Route 53 public zone,
an approved private route and DNS forwarding, and a known-good repository.
Start at the candidate’s Ohio Console link.

1. Check the AWS account, Region and prefilled release inputs. Select the
   admitted service and workspace subnets and **Private** browser access.
   Enter the approved source IPv4 range and Let’s Encrypt settings.
2. Review the single CREATE change set and named IAM acknowledgement.
   Confirm the plan uses the existing VPC and subnets. Execute it with the
   staffed role. Install the stack policy and enable termination protection.
3. After `CREATE_COMPLETE`, verify the exact controller and PostgreSQL
   images, encrypted storage, private DNS and Cloud Map outputs. Retrieve
   the setup claim from Secrets Manager and finish first login at
   `ManagementOrigin`. Confirm database and browser TLS, durable activation,
   and denial from outside the approved private route.
4. Validate the official image and observe canary cleanup. Save and reload
   the pinned profile and project template. Authorize repository access and
   open the IDE. Confirm the exact AMI and encrypted root.
5. Exercise the guided installer against the same YAML and release inputs.
   Decline execution once, then resume the same reviewed change set.
   Verify rejection of wrong account, wrong Region, changed pins, changed
   template bytes and unexpected resources.

### Historical staged fixture evidence

The local T021 rehearsal assembled the actual installer from synthetic release assets and ran it in an interactive terminal with mocked AWS and artifact endpoints. The installer submitted a CREATE preview, kept the same review stack ID after the operator declined execution, and rejected substituted CloudFormation template bytes on resume. After explicit approval it executed only the original change set, recorded the protected stack and retained volume, and stopped when the operator declined the TLS secret write. Later resumes rejected account and volume drift.

A second terminal fixture exercised the CLI stage runner from a synthetic foundation through activation. It kept the reviewed DNS service change set after rejection, recovered from a lost response to the TLS secret write without a duplicate write, and withheld controller activation after an interrupted setup handoff. It reused the stack, volume, VPC and DNS IDs on resume, and repeated execution issued no further change sets after the stack was active. CloudFormation and the protected setup handoff were simulated.

An offline check ran the [legacy setup helper](../../docs/site/setup-helper.md) with a synthetic DNS suffix. OpenSSL verified the management, IDE, preview, repair and PostgreSQL hostnames under their intended issuers. It rejected unrelated hostnames, a name beyond the wildcard depth, and each leaf under the other issuer. The fixture keys were removed after the check.

A Chrome fixture exercised the freshly built UI against local responses. It uploaded the exact catalog bytes, reloaded a validated release and saved a resource profile pinned to that artifact. It reloaded the project template and verified the inherited AMI in the launch review. The canary and persisted API responses were simulated. The live canary, catalog persistence and IDE, Console launch, the remaining manual journey, and live DNS and browser trust remain pending.

```sh
bash ./install.sh --version "$release_tag" --work-dir "$validation_dir"
```

Keep evidence privately. Delete disposable workspaces through normal product controls. In the authorized test account, disable termination protection, delete the disposable stack and verify dependent services, application security groups and DNS cleanup. Verify retained EBS and secrets survive as designed, then remove only the identified test remnants after review. Verify the selected subnets, routes and router remain available. Keep publisher assets and demo state.

## 5. Reuse the applicable product gates

Use the [V1–V14 validation register](../../documentation/architecture/validation-and-engineering-defaults.md#mandatory-release-validation), [native MVP evidence](../../documentation/validation/mvp-v1-v14.md#gate-results) and [private AWS runbook](../002-aws-ec2-provider/quickstart.md). The following inventory distinguishes recorded runs and available fixtures from the pending public RC evidence. Run the native UTM smoke separately. Record exact source, artifact IDs, environment, commands, outcomes and cleanup privately.

| Gate | Existing evidence or fixture | Pending for the public RC |
| --- | --- | --- |
| V1 Platform and provider | The [demo AMI launch](../../documentation/validation/resource-profile-ami.md#t028-live-launch-from-the-updated-profile) verified the selected private image, encrypted EBS and workspace identity. | Verify official image and template provenance, four cross-account boots, and isolated workspace and disk ownership through deletion. |
| V2 Enrollment delivery | [Demo single-boot acceptance](../../documentation/validation/ec2-single-boot.md) observed one boot and exact-proof replay rejection. The long-stop integration suite covers grant binding and ineligible targets. | Run initial and short/long-stop renewal on the official AMI. Verify the same retained instance and disk, replay refusal, and grant cleanup. |
| V3 Certificates | [Native certificate and revocation evidence](../../documentation/validation/mvp-v1-v14.md#gate-results) and the integration revocation suite cover the trust lifecycle and connection closure. | Verify AWS guest leaf rotation, bad clock and CA, restart and long-offline recovery, revoked or cloned identity and tunnel/resumption denial. |
| V4 Guest compatibility | The [native compatibility record](../../documentation/validation/mvp-v1-v14.md#gate-results) covers a previous signed agent, blocked migration, signed rollback and upgrade. | Verify unchanged RC build identity in the promotion rehearsal and exercise the supported prior-agent recovery, retained-workspace blockers and rollback fixture. A separate public-stable-to-public-stable rehearsal waits for a prior public stable. |
| V5 Provider networking | [Demo enrollment](../../documentation/validation/ec2-single-boot.md#live-workspace) observed a private instance without a public IP or instance profile. The CloudFormation fixtures cover the selected subnets and service security groups. | Verify the actual public stack's IAM, scoped routes, split DNS and egress for Tailscale and an existing private link, with failed access from outside the permitted route. |
| V6 Work and data | [Demo IDE validation](../../documentation/validation/ide-websocket.md#live-acceptance) verified a live rebuild/reload. The [demo cache validation](../009-devcontainer-image-cache/validation-report.md) records retained-home and rebuild coverage. | Record project, home and persistent mount data after reboot, stop/start and rebuild. Exercise trusted repair and same-volume controller/host recovery. |
| V7 Reconciliation and deletion | The EC2 provider fixtures cover lost create and enrollment replies and partial cleanup. [Demo cleanup](../../documentation/validation/resource-profile-ami.md#t030-cleanup-after-human-ide-verification) verified instance, volume and interface removal. | Exercise operation interruption, duplicate and competing actions, capacity and disk refusals, and independent AWS cleanup observations without premature reservation release. |
| V8 Retention and maintenance | The [native retention record](../../documentation/validation/mvp-v1-v14.md#gate-results) and maintenance admission contract cover activity, outage grace, maintenance and verified deletion. | Check shared regression suites and exercise the changed AWS path with fresh activity, a missing poll and safe maintenance of retained work. |
| V9 Identity and bootstrap | [Native setup results](../../documentation/validation/quickstart-results.md) cover owner preservation, scoped PostgreSQL and fresh sign-in. Demo records show protected use of the private stack. | Verify AWS Console → SSM → wizard, denied setup access, no leaked password, one preserved owner after restart, no stale fallback, and local/external identity checks. |
| V10 Backup and restore | [Recovery validation history](../../documentation/validation/backup-recovery.md) records native PostgreSQL replacement and retained-workspace review, plus separate private AWS backup and migration checks. | Complete the disposable same-installation AWS PostgreSQL recovery, authority revocation, retained disk continuity and teardown described in the recovery record. |
| V11 Application authorization | [Template sharing evidence](../../documentation/validation/template-sharing.md) and template/secret contracts cover grant and ownership rules with native or fixture inputs. | Recheck current contracts and verify public stack admin catalog permissions, save/reload, owner scope and the private launch acceptance flow. |
| V12 Browser and tunnel | [Native browser results](../../documentation/validation/workspace-operations.md) cover hostile preview origins and revocation. [Demo IDE readiness](../../documentation/validation/ide-websocket.md) verified the real EC2 browser tunnel. | Verify exact-RC IDE/preview origin isolation, copied credential rejection, authenticated WebSocket and revoked-stream closure on the private endpoint. |
| V13 OAuth | [Template sharing validation](../../documentation/validation/template-sharing.md) and the Git authorization contract cover owner-specific token context, refresh and reconnect. | Verify operator-owned App authorization and private checkout, encrypted token persistence across restart, and revoked permission denial and reconnect. |
| V14 Accessibility and operability | [Accessibility checks](../../documentation/validation/accessibility.md) exercise the exported UI with fixtures. [Demo admin validation](../../documentation/validation/ui-bundle.md) records a real browser review. | Exercise production embedded assets, keyboard/focus and accessible errors in AWS setup and catalog, browser readiness, notices and the reviewed upgrade/recovery instructions. |

Check coordinated backup before irreversible migrations, retained-workspaces-current, compatibility, change-set review, startup fencing, matching-release archive and recovery. Verify same-release recovery for the first stable. Rehearse an upgrade from a prior public stable when one exists.

## 6. Record qualification and verify docs

Verify exact RC, template asset and all four versioned S3 bytes, controller digest, regional AMIs, launch results and cleanup. Submit the sanitized record specified in the [release contract](contracts/release-publication.md). Rehearse stable using the exact annotation. Compare asset bytes and S3 version IDs and verify Pages-only publication leaves the release unchanged.

Open repository, release and Pages links with pre-stable, stable and explicit RC fixtures. Confirm all four quick-create links for each published version, the inspected template and checksum, installer, first-workspace, connectivity, troubleshooting, upgrade/recovery and build-main guides.
