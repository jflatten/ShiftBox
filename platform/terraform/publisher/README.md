# ShiftBox public release publisher

This Terraform root manages the four dedicated S3 launch-template buckets and
two GitHub OIDC roles in an approved commercial AWS publisher account. Configure
`publisher_account_id` and a distinct `release_buckets` name for each of
`us-east-1`, `us-east-2`, `us-west-1`, and `us-west-2`. The account must
already have the GitHub Actions OIDC provider for
`token.actions.githubusercontent.com` with audience `sts.amazonaws.com`.

Configure each GitHub environment to admit only approved release tags and
maintainers. Use `candidate_template_role_arn` in `aws-release-candidate`,
`stable_verify_role_arn` in `aws-release-stable`, and the regional
`template_buckets` result in the template stager. An approved, separately scoped
AMI build and publication role supplies AMI-specific permissions.

The buckets deny insecure transport, overwrites and ordinary deletion of
launch object versions. Public access is limited to reading versioned launch
templates over HTTPS. Verify the publisher account and Organizations allow
this bucket-policy exception before applying a reviewed publication plan.
Store internal files and qualification records in separate private storage.
Review any intentional removal of an unreferenced public version separately
from ordinary release operations.

Run the tests without a live publisher account:

```sh
terraform init -backend=false -lockfile=readonly
terraform fmt -check -recursive
terraform validate
terraform test
```

See the [release operations guide](../../release/README.md) and
[publication contract](../../../specs/010-public-aws-distribution/contracts/release-publication.md).
