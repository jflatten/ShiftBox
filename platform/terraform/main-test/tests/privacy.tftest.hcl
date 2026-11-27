mock_provider "aws" {}

variables {
  build_network = {
    vpc_id              = "vpc-00000000000000001"
    private_subnet_id   = "subnet-00000000000000002"
    private_subnet_cidr = "10.42.0.64/26"
    endpoint_id         = "eice-00000000000000003"
    builder_group_id    = "sg-00000000000000004"
    source_ami          = "ami-00000000000000005"
    source_snapshot     = "snap-00000000000000006"
  }
  cloudformation_execution_policy_arn = "arn:aws:iam::888293065132:policy/shiftbox-main-test-cloudformation"
}

# The hosted workflow can build encrypted private images and use private staging.
run "private_testing_artifacts" {
  command = plan

  assert {
    condition = (
      aws_s3_bucket_public_access_block.artifacts.block_public_acls &&
      aws_s3_bucket_public_access_block.artifacts.block_public_policy &&
      aws_s3_bucket_public_access_block.artifacts.ignore_public_acls &&
      aws_s3_bucket_public_access_block.artifacts.restrict_public_buckets
    )
    error_message = "Every S3 public access block must remain enabled."
  }

  assert {
    condition = alltrue([for role in aws_iam_role.actions :
      jsondecode(role.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] ==
      "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-testing"
    ])
    error_message = "The AWS roles must use only the protected testing environment."
  }

  assert {
    condition = (
      local.build_grants.LaunchEncryptedRoot.Condition.Bool["ec2:Encrypted"] == "true" &&
      local.build_grants.SetPrivateImageIMDSv2.Condition.Bool["ec2:Public"] == "false" &&
      local.build_grants.SetPrivateImageDescription.Condition.Bool["ec2:Public"] == "false"
    )
    error_message = "The builder must use an encrypted root and modify only private images."
  }
}
