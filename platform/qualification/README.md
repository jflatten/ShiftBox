# On-demand AWS RC qualification

Use this workflow in the dedicated `shiftbox-testing` account. It validates
an already public immutable ShiftBox RC. The publisher owns the release
assets and the shared demo has its own deployment. Workflow credentials must
resolve to the independently confirmed testing account. None of these jobs
publish releases or use demo credentials.

## Budget and runtime

The phase-zero estimate runs locally with no AWS credentials or billing API.

```sh
python3 platform/qualification/cost.py
python3 platform/qualification/cost.py --mode staffed --headroom-usd 6.00
```

The second command demonstrates the admission boundary with an example
amount. A maintainer must substitute verified aggregate monthly headroom
under the **$30 AWS cap**. Deduct the month-to-date spend, expected remaining
demo and other recurring spend, the operator's permanent router and App secret,
and existing or planned publisher retention
before admitting a qualification window. Check payer coverage and delayed
charges. Actual aggregate headroom has not been verified.

| Scenario | Estimated incremental AWS cost | Planning ceiling |
| --- | ---: | ---: |
| Four isolated regional AMI boots and hosted change sets plus Ohio foundation only | $0.49 | $5.00 |
| Same regional probes plus one staffed Ohio console-to-IDE install and scoped recovery target | $1.01 | $6.00 |

The full model allocates 15 minutes to each standalone private probe, up to
two hours for Ohio resources including teardown, and one hour for a separate
private recovery target. The workflow limits automated deployment and probes
to 45 minutes combined, the staffed operator window to 60 minutes, and the
immediate cleanup step to 15 minutes. The price estimate includes $0.15 for
all four probes, Ohio ECS host, workspace and restore compute, paid
gp3 storage, CPU credit allowance, 5 GiB of processing through existing egress, CloudWatch, private DNS, Cloud Map, and Secrets Manager. It uses
public list rates without relying on credits. The ceiling adds two hours of
running orphan allowance, 24 hours of retained storage and secrets, increased
traffic and logs, and hosted zone charges if teardown misses the AWS grace
period. It is an admission reserve. Billing may exceed it if a resource
cannot be deleted, data transfer exceeds the assumptions, or a run is
repeated. Reconcile the change after AWS posts the bill.

The Ohio stack uses existing outbound connectivity. The separate AMI probes
use their own disposable private VPC with no NAT and no public IP. The operator owns the
permanent router and GitHub App secret outside this estimate and workflow. The separate
publisher model estimates up to $6.57 per month per retained RC, assuming
four 32 GiB of billed standard snapshot data and four small versioned
template objects. Its estimated build and copy allowance is $3.03 before
runner, network, download, and retry costs. Stable promotion reuses the RC
assets. The cost calculator contains dated catalog links and line items.

[AWS on-demand rates](https://aws.amazon.com/ec2/pricing/on-demand/) · [NAT and IPv4](https://aws.amazon.com/vpc/pricing/) · [EBS and snapshots](https://aws.amazon.com/ebs/pricing/) · [Private DNS](https://aws.amazon.com/route53/pricing/) · [Secrets Manager](https://aws.amazon.com/secrets-manager/pricing/)

## Activate after review

1. Land the reviewed workflow on default `main` through the normal PR.
   GitHub requires the workflow on the default branch for dispatch and
   schedule. The workflow also verifies `jflatten/ShiftBox`, `main`, and
   the approved maintainer for manual operations. The scheduled sweeper
   uses the default branch.
2. In the verified testing account, review the Terraform in
   [qualification access](../terraform/qualification/main.tf) using
   `shiftbox-testing` and private inputs. It creates GitHub OIDC, when
   opted in, and two environment-bound OIDC roles. Input the four approved
   publisher bucket names and the confirmed publisher/account IDs. Supply
   a dedicated `shiftbox-qualification-cloudformation` service role with
   the least-privilege execution permissions reviewed against the exact RC
   template and AWS service actions. The module can attach a reviewed
   testing-account managed policy to that role. Review CloudFormation,
   ECS, EC2, Secrets Manager, Route 53, Cloud Map and Logs dependencies,
   named/scoped IAM and instance profiles, service-linked roles, and the
   ability to delete the stack with retained resources. The OIDC role can
   pass only this service role to CloudFormation. The cleanup OIDC role
   has neither creation permissions nor secret-value reads. Validate the
   live permission matrix and Organizations / Allowed AMIs settings before
   using either role.
3. Configure the GitHub environment `aws-qualification` for the deploy
   role and `aws-qualification-cleanup` for the cleanup role. Enforce
   `main` in both deployment branch policies. Require the approved
   maintainer's release-readiness and spend review before deployment.
   Keep unattended cleanup available without a human approval. The IAM
   audiences are `sts.amazonaws.com` and subjects are the exact repository
   and each named environment. Review who can edit workflows on `main`.
4. Set the protected account and role variables
   `SHIFTBOX_QUALIFICATION_ACCOUNT_ID`,
   `SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID`,
   `SHIFTBOX_QUALIFICATION_DEPLOY_ROLE_ARN`,
   `SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN`, and
   `SHIFTBOX_QUALIFICATION_CLEANUP_ROLE_ARN`. The account values used by
   scheduled admission must be available to the applicable jobs. Use
   role variables scoped to their environments. Keep AWS access keys,
   Identity Center tokens, private CA keys, and browser state out of
   GitHub variables and artifacts.
5. Configure `SHIFTBOX_QUALIFICATION_SUBNETS` in the qualification environment
   as a JSON object keyed by the four release regions. Set each entry's
   `service` and `workspace` members to existing private subnet IDs owned
   by the testing account and in the same VPC. For example, one entry has the
   shape `"us-east-2":{"service":"subnet-0123456789abcdef0","workspace":"subnet-0123456789abcdef1"}`.
   The workflow verifies the IDs and derives the VPC and service AZ before
   creating any change sets. The automated review holds browser ingress closed.
   For staffed runs, prepare the operator-managed private route and DNS,
   local browser/SSM client, GitHub App and permanent secret, private test
   repository, and testing-account SSO permission set. The workflow needs
   four available Standard On-Demand vCPUs in Ohio under quota `L-1216C47A`,
   after allowing for existing EC2 instances' default vCPU counts. Verify
   headroom under that quota before paying. Complete the catalog canary before
   starting the first workspace. Stop the
   workspace during the separate two-vCPU recovery target's residency and
   remove the restore target before restarting the workspace. The standalone
   regional probes are sequential and require two available vCPUs in each region.
   Keep the permanent router, secret and their dependent resources outside the
   reserved `sbq-<run-id>-<attempt>` names and ownership tags.
6. Keep `SHIFTBOX_QUALIFICATION_ENABLED` off until the exact RC, quota,
   verified budget headroom, and private fixtures are ready. Configure
   `SHIFTBOX_QUALIFICATION_SWEEP_ENABLED` independently so halting new runs
   does not halt cleanup. An occupied, stale, or unverifiable ownership
   namespace blocks a new paid run. Enable qualification only after review.

[GitHub workflow dispatch](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_dispatch) · [AWS OIDC environments](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws) · [CloudFormation service role](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-iam-servicerole.html)

## Operator workflow

Select **AWS RC qualification** on `main`. The default operation is
`estimate`, which has no AWS identity. For a separately approved paid run,
select `qualify`, enter the public RC tag and verified headroom, and select
whether a staffed operator is available. The workflow independently verifies
the prerelease, peeled Git tag, manifest, immutable GitHub asset digests,
versioned S3 template bytes and anonymous controller digest. The same group
serializes qualification and either cleanup path. GitHub can replace a queued
run with a newer pending run.

The workflow validates the exact hosted change set in all four regions,
executes each isolated Terraform AMI probe sequentially, verifies its
instance status and exact encrypted root, and destroys its state. It uses
one name per attempt of the form `sbq-<run-id>-<attempt>`. Probes append
`-p` to the name. The four regional change sets use `CAPABILITY_NAMED_IAM`
and `RoleBoundaryMode=external`. The runtime role ARNs and boundary ARNs are
bound to this attempt. Automated mode creates only the Ohio foundation and
verifies the VPC, encrypted volume and absence of DNS and application
services. It previews an Ohio `dns-zones` UPDATE, verifies the exact release
template and two expected additions, and discards the change set. The
GitHub deploy role can execute only a foundation change set in Ohio.

Before a separately approved run, prepare the exact host role boundary and
the limited execution role permissions for that attempt. Apply the reviewed
deploy IAM update for the new Ohio preview and verify the live permissions.
The [execution policy draft](../terraform/qualification/execution/README.md)
records the required name, boundary and resource handler checks.

Staffed mode instead discards the Ohio review stack and opens a 60-minute
window for the operator. Use the confirmed testing account through the
existing local `shiftbox-testing` SSO profile and the exact Ohio Console
template. Set both the stack name and `DeploymentName` to the job summary's
name. Use that name plus `.internal` as the DNS suffix and leave
`RoleBoundaryMode=managed` to exercise the public installation path.
Use the verified Ohio VPC and subnet choices from admission. Set
`OperatorCidr` to the verified source the service sees if private browser
access is ready. The operator manages routing
to the disposable VPC, split DNS for the run suffix, and private browser
access on TCP 8443. Supply the operator-managed GitHub App secret ARN directly
on the staffed database change set. The controller execution role may read
that exact secret for the install. GitHub Actions takes no App or Tailscale
credential inputs.
Before enabling the staffed lane, configure
`SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN` in the qualification
environment with the separately reviewed
`shiftbox-qualification-staffed-cloudformation` role in the testing
account. The approved operator passes that role to CloudFormation; the
GitHub deploy identity has no authority to pass it. Verify the role supports
deletion of the entire tagged stack before opening the operator window.
Set the stack tags `project=shiftbox`, `cluster_name=<name>`, and
`shiftbox_qualification_run=<name>`. Keep the tags and role on updates.

Review the foundation and protect the stack and storage using the published
policy. Review and execute `dns-zones` on that stack, then inspect the
`PrivateZoneId`, `DiscoveryNamespaceId`, and `DiscoveryHostedZoneId` outputs.
Verify both zones are private, use the exact suffixes, and are associated only
with the run VPC; independently verify the namespace and its tags. Review
and execute `dns-services` next. Verify the two service output IDs refer to
`postgres` and `shiftbox` in the verified namespace with the correct run
tags. Verify the six aliases during the subsequent `database` change set.
Record each change-set ID and these results in private evidence. Follow the
[validation quickstart](../../specs/010-public-aws-distribution/quickstart.md)
for PostgreSQL TLS, protected SSM bootstrap, browser trust and the first IDE.
Use a disposable browser profile on the approved private route.
Run the reviewed recovery within the same stack's budget and window with
a separate empty scoped TLS PostgreSQL target. Tag its exact-owned private
EC2 instance, encrypted disk and attached resources with `project=shiftbox`,
`cluster_name=<name>`, and `shiftbox_qualification_component=restore`.
Use the existing private route and avoid a persistent recovery database. At the end of the window,
remove this run's external DNS forwarding and any temporary access entries
you added for it. Stop work so the workflow
can read back the exact staffed stack and check its
DNS outputs against the CloudFormation inventory. The independent cleanup
job then deletes the disposable stack. Verify teardown even if a gate remains
incomplete.

The automation artifact is sanitized foundation or console-handoff evidence.
Staffed DNS readback confirms CloudFormation inventory and the candidate
template. The operator evidence covers the DNS stage order and live service
configuration, the browser, OIDC and Git checkout, operator-managed DNS and
private access, CLI resume, recovery, or completed teardown. Keep those
verified receipts in private operator evidence and combine them with the
selected candidate only after the approved review. A staffed session can
need another separately budgeted run for remaining acceptance gates, such as
an independent full CLI resume rehearsal. Do not qualify a payload from the
automation artifact alone.

## Teardown and recovery

The immediate job uses independent cleanup credentials even when validation
fails. An authenticated `cleanup` dispatch and scheduled sweeper share the
same concurrency group. The scheduled sweep uses GitHub run status,
original attempt, authorized workflow and a 30-minute terminal grace. It
skips active or recently finished runs and fails closed on unfamiliar run
evidence. Schedules may be delayed or disabled by GitHub. Use the manual
cleanup dispatch and a scoped operator inspection if a sweep is missed or
fails. An operator watches teardown rather than assuming the timer is an
AWS lease.

The cleanup script starts from verified account, resource tags, run name,
stack ARN and owned nondefault VPC. It drains owned workload instances,
deletes CloudFormation stacks, inspects regular resource outcomes, checks
native NAT state, and separately removes retained installation volumes and
disposable database secrets. Regional cleanup can reconcile exact run-tagged
probe resources when Terraform state died with a runner. Unresolved regular
resources, unexpected tags/names, orphaned dependencies, or inability to
prove ownership fail the check. Resolve the reported resource on its original
stack or approved private test scope. Cleanup does not select the default
VPC, shared resources, GitHub App secret, public AMIs, or release buckets.

The staffed operator records removal of run-specific routes, DNS and client
trust, then verifies the permanent router and App secret remain available.
Record private route, split-DNS, browser trust and denied public-access evidence
for the operator-managed connection. Release qualification remains open until
the exact release's acceptance checks and teardown are complete.
