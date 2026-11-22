# Disposable Ohio Packer build infrastructure

This separate Terraform root defines the private builder network and expiry
cleanup for one ShiftBox publisher attempt in `us-east-2`. Use it with the
[hosted Packer review plan](../../../packer/aws/publisher-runner-plan.md).
The GitHub-hosted job receives attempt outputs after an independently approved
deployment. CI exercises this root with a mock AWS provider.

## Prepare a plan for review

Verify the publisher account using the intended AWS CLI profile before
planning. Use a new private state path per attempt. Supply the verified
`publisher_account_id`, a nonoverlapping private `vpc_cidr` in Ohio,
`availability_zone_id`, the protected `candidate_tag`, a unique `attempt_id`,
the one-attempt `packer_public_key` and a UTC `expires_at` in a private inputs
file. Set the deadline within two hours and reserve enough time for the
50-minute build. Use only the SSH public key in the input file.

Set `attempt_dir` to an absolute, owner-only directory with the reviewed
`attempt.tfvars.json`. From the repository root, package the expiry source
once and keep that archive with the reviewed plan. Use the same archive for
any later, separately approved operation on the plan.

```sh
umask 077
root=platform/terraform/publisher/ohio-build
(cd "$root" && zip -q -X expiry.zip expiry.py)
terraform -chdir="$root" init -backend=false -lockfile=readonly
terraform -chdir="$root" validate
terraform -chdir="$root" plan -input=false \
  -state="$attempt_dir/terraform.tfstate" \
  -var-file="$attempt_dir/attempt.tfvars.json" \
  -out="$attempt_dir/ohio-build.tfplan"
terraform -chdir="$root" show -no-color "$attempt_dir/ohio-build.tfplan" \
  > "$attempt_dir/ohio-build-plan.txt"
```

The HTTPS-only baseline plans 32 AWS creates and one local deadline guard.
The optional builder TCP 80 rule adds one AWS create. Review the private
subnet, NAT, endpoint-to-builder SSH path, ownership tags, imported public
key, and five bounded expiry invocations. Review first-use EC2 Instance
Connect service-linked IAM behavior separately. Keep saved plans, inputs,
state, and account inventory in protected operator storage.

The `attempt` output supplies the protected workflow inputs after a later
deployment. The `build_connection_policy` output renders the exact endpoint,
attempt tag, builder CIDR, TCP port and bounded duration for review in the
separate build-role plan. It does not attach the grant. Cleanup independently
reconciles attempt-owned billable compute, detached volumes, NAT and its
address at expiry. Staffed teardown verifies and removes the remaining
network and attempt inputs.
