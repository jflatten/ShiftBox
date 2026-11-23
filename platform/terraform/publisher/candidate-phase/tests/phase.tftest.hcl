mock_provider "aws" {
  mock_resource "aws_iam_role" {
    defaults = { unique_id = "AROAEXAMPLE0123456789" }
  }
}

variables {
  publisher_account_id = "111122223333"
  candidate_tag        = "v1.2.0-rc.3"
  execution = {
    run_id        = "12345678"
    run_attempt   = "1"
    expires_at    = "2030-01-01T01:00:00Z"
    source_commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    source_ami    = "ami-00000000000000004"
  }
}

# A protected job gets a new boundary-constrained role for its exact run,
# candidate tag, phase session and lease.
run "scoped_build_role" {
  command = apply
  variables {
    phase = "build"
    build = {
      attempt_id      = "rel-b4e6ccddd9fe60"
      vpc_id          = "vpc-00000000000000001"
      subnet_id       = "subnet-00000000000000002"
      subnet_cidr     = "10.0.0.64/26"
      security_group  = "sg-00000000000000003"
      endpoint_id     = "eice-00000000000000006"
      source_snapshot = "snap-00000000000000005"
    }
  }
  assert {
    condition = (
      aws_iam_role.ami_candidate.path == "/shiftbox-release/" &&
      startswith(aws_iam_role.ami_candidate.name, "shiftbox-publisher-release-build-") &&
      aws_iam_role.ami_candidate.permissions_boundary == "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-build-boundary" &&
      local.trust.Statement[0].Condition.StringEquals["sts:RoleSessionName"] == "shiftbox-ami-build-12345678-1" &&
      local.trust.Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:ref"] == "refs/tags/v1.2.0-rc.3" &&
      local.active_statements.OpenReviewedTunnel.Resource == "arn:aws:ec2:us-east-2:111122223333:instance-connect-endpoint/eice-00000000000000006" &&
      local.active_statements.PrivateBuildInterface.Condition.StringEquals["aws:RequestTag/publisher_attempt"] == "rel-b4e6ccddd9fe60" &&
      !contains(keys(local.active_statements), "CopyPinnedOhioBuild") &&
      !contains(keys(local.active_statements), "PublishPinnedCandidates") &&
      length(aws_iam_role_policy.ami_candidate.policy) <= 10240
    )
    error_message = "Build must stay in its own temporary role, exact network, GitHub job session, and publisher scope."
  }
}

# Copy authorization must use only the verified Ohio product of the build
# and prohibit publication under that ephemeral identity.
run "private_regional_copy" {
  command = plan
  variables {
    phase        = "copy"
    built_ami_id = "ami-00000000000000006"
  }
  assert {
    condition = (
      aws_iam_role.ami_candidate.permissions_boundary == "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-copy-boundary" &&
      local.active_statements.CopyPinnedOhioBuild.Resource == "arn:aws:ec2:us-east-2::image/ami-00000000000000006" &&
      !contains(keys(local.active_statements), "OpenReviewedTunnel") &&
      !contains(keys(local.active_statements), "PublishPinnedCandidates")
    )
    error_message = "Copy is pinned to the approved private AMI and has no build or share permissions."
  }
}

# A successful private build does not justify sharing until all four images
# have been frozen in the same candidate handoff.
run "refuse_incomplete_share" {
  command = plan
  variables {
    phase         = "share"
    built_ami_id  = "ami-00000000000000006"
    regional_amis = { us-east-2 = "ami-00000000000000006" }
  }
  expect_failures = [var.regional_amis]
}

# A stable tag can only promote existing artifacts. It cannot provision
# an ephemeral candidate role, even if supplied with an AMI to copy.
run "refuse_stable_tag" {
  command = plan
  variables {
    candidate_tag = "v1.2.0"
    phase         = "copy"
    built_ami_id  = "ami-00000000000000006"
  }
  expect_failures = [var.candidate_tag]
}
