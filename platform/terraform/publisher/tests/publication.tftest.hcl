mock_provider "aws" {}

mock_provider "aws" {
  alias = "us_east_1"
}

mock_provider "aws" {
  alias = "us_west_1"
}

mock_provider "aws" {
  alias = "us_west_2"
}

variables {
  publisher_account_id = "111122223333"
  release_buckets = {
    us-east-1 = "fixture-release-us-east-1"
    us-east-2 = "fixture-release-us-east-2"
    us-west-1 = "fixture-release-us-west-1"
    us-west-2 = "fixture-release-us-west-2"
  }
}

run "reject_missing_publisher_region" {
  command = plan

  # A release cannot silently omit one AWS region.
  variables {
    release_buckets = {
      us-east-1 = "fixture-release-us-east-1"
      us-east-2 = "fixture-release-us-east-2"
      us-west-1 = "fixture-release-us-west-1"
    }
  }

  expect_failures = [var.release_buckets]
}

run "reject_demo_as_publisher" {
  command = plan

  # A release configuration must never modify the private demo account.
  variables {
    publisher_account_id = "524941137496"
  }

  expect_failures = [var.publisher_account_id]
}

run "distinct_publisher_roles_and_buckets" {
  command = plan

  # Security is scoped to four distinct, explicitly named regional buckets.
  assert {
    condition     = output.template_buckets == tomap(var.release_buckets)
    error_message = "The template bucket map must match the explicit four-region input."
  }

  # GitHub's environment-based OIDC claims must match exact role subjects.
  assert {
    condition = alltrue([
      for name, role in { candidate = aws_iam_role.candidate, stable = aws_iam_role.stable } :
      jsondecode(role.assume_role_policy).Statement[0].Principal.Federated == "arn:aws:iam::111122223333:oidc-provider/token.actions.githubusercontent.com" &&
      jsondecode(role.assume_role_policy).Statement[0].Action == "sts:AssumeRoleWithWebIdentity" &&
      jsondecode(role.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:aud"] == "sts.amazonaws.com" &&
      jsondecode(role.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten/ShiftBox:environment:aws-release-${name}"
    ])
    error_message = "OIDC trusts an unexpected identity, repository or environment."
  }

  # The candidate only stages immutable templates. It cannot change bucket policy, delete, or publish AMIs.
  assert {
    condition = toset(flatten([
      for statement in jsondecode(aws_iam_role_policy.candidate.policy).Statement : statement.Action
      ])) == toset([
      "s3:GetBucketLocation", "s3:GetBucketVersioning", "s3:ListBucketVersions",
      "s3:PutObject", "s3:PutObjectTagging"
    ])
    error_message = "The candidate template role gained out-of-scope permissions."
  }

  # The stable job inspects final facts and cannot publish or modify AWS assets.
  assert {
    condition = toset(flatten([
      for statement in jsondecode(aws_iam_role_policy.stable.policy).Statement : statement.Action
      ])) == toset([
      "s3:GetBucketLocation", "s3:GetBucketVersioning", "s3:GetObjectVersion",
      "ec2:DescribeImages", "ec2:DescribeImageAttribute", "ec2:DescribeSnapshots", "ec2:DescribeSnapshotAttribute"
    ])
    error_message = "The stable AWS role has write or unexpected read permissions."
  }

  # Unscoped enumeration or uploading an unrelated S3 key is a policy regression.
  assert {
    condition = alltrue([
      for statement in jsondecode(aws_iam_role_policy.candidate.policy).Statement :
      statement.Sid != "ListTemplateVersions" ||
      statement.Condition.StringLike["s3:prefix"] == "public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml"
      ]) && alltrue([
      for statement in jsondecode(aws_iam_role_policy.candidate.policy).Statement :
      !contains(statement.Action, "s3:PutObject") ||
      alltrue([for arn in statement.Resource : endswith(arn, "/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml")])
    ])
    error_message = "The candidate S3 policy permits an unrelated key or unscoped enumeration."
  }
}

run "release_bucket_security" {
  command = plan

  module {
    source = "./modules/template-bucket"
  }

  variables {
    bucket_name  = "fixture-region-templates"
    cluster_name = "shiftbox-publisher"
  }

  # Regional stores are versioned and encrypted with S3-managed keys for anonymous read.
  assert {
    condition     = one(aws_s3_bucket_versioning.templates.versioning_configuration).status == "Enabled"
    error_message = "The regional S3 template URL must use an enabled object version."
  }

  assert {
    condition = (
      one(one(aws_s3_bucket_server_side_encryption_configuration.templates.rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256" &&
      !aws_s3_bucket.templates.force_destroy
    )
    error_message = "Templates must use SSE-S3 and bucket destruction must not force-delete objects."
  }

  # Only the deliberately constrained bucket policy can grant public reads.
  assert {
    condition = (
      one(aws_s3_bucket_ownership_controls.templates.rule).object_ownership == "BucketOwnerEnforced" &&
      aws_s3_bucket_public_access_block.templates.block_public_acls &&
      aws_s3_bucket_public_access_block.templates.ignore_public_acls &&
      !aws_s3_bucket_public_access_block.templates.block_public_policy &&
      !aws_s3_bucket_public_access_block.templates.restrict_public_buckets
    )
    error_message = "Anonymous template reads must use the public version policy without ACLs."
  }

  # Any allow accessible by a public principal is restricted to versioned release objects.
  assert {
    condition = (
      length([for statement in jsondecode(aws_s3_bucket_policy.templates.policy).Statement : statement if statement.Effect == "Allow"]) == 1 &&
      alltrue([
        for statement in jsondecode(aws_s3_bucket_policy.templates.policy).Statement :
        statement.Effect != "Allow" || (
          try(statement.Principal == "*", false) && toset(statement.Action) == toset(["s3:GetObjectVersion"]) &&
          toset(statement.Resource) == toset(["arn:aws:s3:::fixture-region-templates/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml"])
        )
      ])
    )
    error_message = "Public policy permits something beyond versioned launch-template reads."
  }

  # Deny creation with no If-None-Match header so a released object key cannot be overwritten.
  assert {
    condition = anytrue([
      for statement in jsondecode(aws_s3_bucket_policy.templates.policy).Statement :
      statement.Effect == "Deny" && try(statement.Condition.StringNotEquals["s3:if-none-match"] == "*", false) &&
      contains(statement.Action, "s3:PutObject")
    ])
    error_message = "The bucket policy is missing the immutable first-write condition."
  }

  # The pinned S3 object cannot be deleted or hidden by a delete marker.
  assert {
    condition = anytrue([
      for statement in jsondecode(aws_s3_bucket_policy.templates.policy).Statement :
      statement.Effect == "Deny" && statement.Principal == "*" &&
      toset(statement.Action) == toset(["s3:DeleteObject", "s3:DeleteObjectVersion"]) &&
      toset(statement.Resource) == toset(["arn:aws:s3:::fixture-region-templates/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml"])
    ])
    error_message = "Approved launch object versions must survive ordinary delete requests."
  }
}
