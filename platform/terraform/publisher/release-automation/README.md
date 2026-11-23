# Release automation bootstrap

This Terraform root belongs to the ShiftBox production publisher account in
Ohio. It creates a dedicated private S3 bucket for release-build state and the
OIDC roles and permissions boundaries used by the release and cleanup
workflows. It provisions no VPC or compute.

Check the approved AWS account, the existing GitHub OIDC provider, and the
EC2 Instance Connect service-linked role before preparing a saved plan. Use
the publisher's private Terraform state and protect it with owner-only
permissions. Keep it separate from the release-attempt state.

```sh
umask 077
export AWS_PROFILE=shiftbox-production AWS_REGION=us-east-2
root=platform/terraform/publisher/release-automation
account=$(aws sts get-caller-identity --query Account --output text)
test "$account" = "$approved_publisher_account"
terraform -chdir="$root" init -lockfile=readonly
terraform -chdir="$root" plan -input=false \
  -var="publisher_account_id=$account" -out="$private_review_dir/bootstrap.tfplan"
terraform -chdir="$root" show "$private_review_dir/bootstrap.tfplan"
```

After approval, apply the exact saved plan and verify the IAM trust, the state
bucket's owner, versioning, default encryption, and four public-access blocks.
Configure the output role ARNs and bucket in the
[protected GitHub environments](../../../release/README.md#bootstrap-release-automation).
Keep the cleanup flag off until the protected main cleanup job is configured
and verified.

Every build attempt and AMI phase uses a separate state key. Terraform uses
S3 native locking and SSE-S3. Lease and retirement records use conditional
creation. The build runner holds the SSH private key locally. The durable
backend contains only the public key and deployment metadata.

The persistent network role may create only boundary-constrained temporary
roles under the reserved release scope. The persistent cleanup role has
read and teardown permissions for expired attempts. Only the temporary share
role can change launch permission on the approved private AMI IDs. Neither
persistent role can change the publisher's public artifact controls.

Run the mock plans without a live account.

```sh
terraform -chdir="$root" init -backend=false -lockfile=readonly
terraform -chdir="$root" test
```
