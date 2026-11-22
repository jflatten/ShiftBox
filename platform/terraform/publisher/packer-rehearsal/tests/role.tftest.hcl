mock_provider "aws" {
  override_during = plan
}

variables {
  publisher_account_id = "111122223333"
}

# The initial review can create only an inert role and cannot assume it from GitHub.
run "closed" {
  command = plan

  assert {
    condition     = length(aws_iam_role_policy.attempt) == 0 && jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Effect == "Deny"
    error_message = "The role must have no assumption or AWS access before an attempt is bound."
  }
}

# A live attempt may scope one run to exact AWS resources and no publication actions.
run "bound" {
  command = plan

  variables {
    attempt = {
      id              = "rehearsal-fixture"
      run_id          = "101"
      source_commit   = "0123456789abcdef0123456789abcdef01234567"
      expires_at      = "2037-11-22T16:00:00Z"
      vpc_id          = "vpc-00000000000000001"
      subnet_id       = "subnet-00000000000000002"
      subnet_cidr     = "10.42.0.64/26"
      security_group  = "sg-00000000000000003"
      endpoint_id     = "eice-00000000000000004"
      source_ami      = "ami-00000000000000005"
      source_snapshot = "snap-00000000000000006"
    }
  }

  assert {
    condition = (
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-packer-rehearsal" &&
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:actor_id"] == "11561330" &&
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:ref"] == "refs/heads/main" &&
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:workflow"] == "Rehearse private Ohio Packer build" &&
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringEquals["sts:RoleSessionName"] == "shiftbox-packer-rehearsal-101-1"
    )
    error_message = "The trust must pin the immutable environment, branch, actor, workflow and session name."
  }

  assert {
    condition = alltrue([
      for statement in jsondecode(aws_iam_role_policy.attempt[0].policy).Statement :
      statement.Condition.DateLessThan["aws:CurrentTime"] == var.attempt.expires_at &&
      statement.Condition.StringEquals["aws:RequestedRegion"] == "us-east-2" &&
      alltrue([for action in statement.Action : !strcontains(action, "*") && !can(regex("^(s3:|iam:|sts:)|CopyImage|ModifySnapshotAttribute", action))])
    ])
    error_message = "Every grant must expire, stay in Ohio and avoid publication or delegation."
  }

  # The pinned Packer plugin enumerates regions before it can launch a builder.
  assert {
    condition = [
      for statement in jsondecode(aws_iam_role_policy.attempt[0].policy).Statement : statement.Sid
      if contains(statement.Action, "ec2:DescribeRegions")
    ] == ["ReadOhioBuildInventory"]
    error_message = "Packer requires an Ohio-scoped DescribeRegions grant before launching."
  }

  # EC2 checks the resources it will create before their IDs are allocated.
  # Existing builders remain scoped to real instance IDs and attempt tags.
  assert {
    condition = {
      for statement in jsondecode(aws_iam_role_policy.attempt[0].policy).Statement : statement.Sid => statement.Resource
      if contains(["TaggedT3Builder", "TaggedPrivateInterface", "TaggedBuildRoot", "CreateOwnedImageAndSnapshot", "TagAttemptOnCreation", "ImageOnlyOwnedAttemptBuilder"], statement.Sid)
      } == {
      TaggedT3Builder             = ["arn:aws:ec2:us-east-2:111122223333:instance/*"]
      TaggedPrivateInterface      = ["arn:aws:ec2:us-east-2:111122223333:network-interface/*"]
      TaggedBuildRoot             = ["arn:aws:ec2:us-east-2:111122223333:volume/*"]
      CreateOwnedImageAndSnapshot = ["arn:aws:ec2:us-east-2::image/*", "arn:aws:ec2:us-east-2::snapshot/*"]
      TagAttemptOnCreation = [
        "arn:aws:ec2:us-east-2:111122223333:instance/*", "arn:aws:ec2:us-east-2:111122223333:network-interface/*",
        "arn:aws:ec2:us-east-2:111122223333:volume/*", "arn:aws:ec2:us-east-2::image/*", "arn:aws:ec2:us-east-2::snapshot/*",
      ]
      ImageOnlyOwnedAttemptBuilder = ["arn:aws:ec2:us-east-2:111122223333:instance/i-*"]
    }
    error_message = "Creation grants must match unassigned EC2 resource IDs, while existing instance operations retain their ID fence."
  }

  assert {
    condition = one([
      for statement in jsondecode(aws_iam_role_policy.attempt[0].policy).Statement : statement
      if contains(statement.Action, "ec2-instance-connect:OpenTunnel")
    ]).Resource == ["arn:aws:ec2:us-east-2:111122223333:instance-connect-endpoint/eice-00000000000000004"]
    error_message = "A new endpoint is not covered by the rehearsal tunnel grant."
  }

  assert {
    condition = alltrue([
      for statement in jsondecode(aws_iam_role_policy.attempt[0].policy).Statement :
      can(statement.Condition.StringEquals["ec2:Attribute/Description"]) || can(statement.Condition.StringEquals["ec2:Attribute/ImdsSupport"])
      if contains(statement.Action, "ec2:ModifyImageAttribute")
    ])
    error_message = "Image attribute writes must be limited to description and IMDSv2."
  }
}
