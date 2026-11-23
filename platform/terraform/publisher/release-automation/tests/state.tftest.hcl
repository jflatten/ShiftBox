mock_provider "aws" {}

variables {
  publisher_account_id = "111122223333"
}

# The recovery backend must stay in the publisher account with no public
# access, no ACLs and recoverable encrypted versions.
run "protected_versioned_backend" {
  command = plan
  assert {
    condition = (
      aws_s3_bucket.state.bucket == "shiftbox-release-state-111122223333-us-east-2" &&
      !aws_s3_bucket.state.force_destroy &&
      aws_s3_bucket_public_access_block.state.block_public_acls &&
      aws_s3_bucket_public_access_block.state.ignore_public_acls &&
      aws_s3_bucket_public_access_block.state.block_public_policy &&
      aws_s3_bucket_public_access_block.state.restrict_public_buckets &&
      aws_s3_bucket_versioning.state.versioning_configuration[0].status == "Enabled" &&
      aws_s3_bucket_ownership_controls.state.rule[0].object_ownership == "BucketOwnerEnforced" &&
      one(aws_s3_bucket_server_side_encryption_configuration.state.rule).apply_server_side_encryption_by_default[0].sse_algorithm == "AES256"
    )
    error_message = "The state backend must be dedicated, private, encrypted, versioned and protected against accidental removal."
  }

  assert {
    condition = alltrue([
      for statement in jsondecode(aws_s3_bucket_policy.state.policy).Statement :
      statement.Effect == "Deny"
    ])
    error_message = "The state bucket resource policy must never grant anonymous or cross-account access."
  }
}

# The shared development demo and customer qualification account must never
# become the publisher's durable state backend.
run "reject_qualification_account" {
  command = plan
  variables {
    publisher_account_id = "888293065132"
  }
  expect_failures = [var.publisher_account_id]
}

# Each network lifecycle identity must be limited to the private state prefix.
run "state_scope" {
  command = plan
  assert {
    condition = (
      local.state_prefix == "release-builds/" &&
      alltrue([for statement in local.state_access :
        alltrue([for resource in flatten([statement.Resource]) :
          startswith(resource, "arn:aws:s3:::shiftbox-release-state-111122223333-us-east-2")
        ])
      ]) &&
      alltrue([for grant in local.state_access :
        !contains(flatten([grant.Action]), "s3:DeleteObjectVersion")
      ])
    )
    error_message = "State permissions escaped the exact encrypted bucket or allowed deleting historical state versions."
  }

  assert {
    condition = (
      local.state_lease_create.Condition.StringEquals["s3:if-none-match"] == "*" &&
      alltrue([
        for grant in local.state_access :
        alltrue([for resource in flatten([grant.Resource]) :
          !endswith(resource, "/lease.json") && !endswith(resource, "/phase.json")
        ])
        if contains(flatten([grant.Action]), "s3:PutObject")
      ]) &&
      one([for statement in jsondecode(aws_s3_bucket_policy.state.policy).Statement :
      statement if statement.Sid == "DenyOverwritingAttemptRecords"]).Condition.StringNotEquals["s3:if-none-match"] == "*"
    )
    error_message = "The lease must be immutable even if network recovery can write Terraform state and a retirement record."
  }
}
