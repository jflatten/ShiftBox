mock_provider "aws" {}

variables {
  testing_account_id   = "111111111111"
  publisher_account_id = "222222222222"
  release_template_buckets = {
    us-east-1 = "shiftbox-template-fixture-va"
    us-east-2 = "shiftbox-template-fixture-oh"
    us-west-1 = "shiftbox-template-fixture-ca"
    us-west-2 = "shiftbox-template-fixture-or"
  }
}

# Each environment has its own OIDC subject. Cleanup cannot launch instances
# or execute a change set, even though it can delete approved test resources.
run "separates_deployment_and_cleanup" {
  command = plan

  assert {
    condition     = jsondecode(aws_iam_role.actions["deploy"].assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-qualification"
    error_message = "Deployment trust must bind the immutable repository and owner IDs and exact environment."
  }

  assert {
    condition     = jsondecode(aws_iam_role.actions["cleanup"].assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-qualification-cleanup"
    error_message = "Cleanup trust must differ from deployment trust."
  }

  assert {
    condition     = !can(regex("CreateChangeSet|ExecuteChangeSet|RunInstances|GetSecretValue|ModifyImageAttribute|iam:PassRole", aws_iam_role_policy.cleanup.policy))
    error_message = "Cleanup must not deploy, read secret values, mutate AMIs, or pass a role."
  }

  assert {
    condition     = length([for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement : s if s.Sid == "CreateReviewedChangeSet" && length(s.Condition.StringLike["cloudformation:TemplateUrl"]) == 4]) == 1
    error_message = "Change set creation must be limited to the four reviewed artifact origins."
  }

  assert {
    condition     = can(regex("aws:ResourceTag/cluster_name", aws_iam_role_policy.cleanup.policy)) && can(regex("stack/sbq-\\*[-]\\*/\\*", aws_iam_role_policy.cleanup.policy))
    error_message = "Cleanup must be constrained to owned resources and qualification stack names."
  }
}

# The same YAML can be reviewed in every region. Only the staffed operator
# can execute a controller stack change set.
run "qualification_is_preview_only" {
  command = plan

  assert {
    condition = toset(flatten([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      s.Condition.StringLike["cloudformation:ChangeSetName"] if s.Sid == "CreateReviewedChangeSet"
      ])) == toset(["sbq-*-*-foundation", "sbq-*-*-one-pass"]) && contains(one([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      s.Condition.StringLike["cloudformation:TemplateUrl"] if s.Sid == "CreateReviewedChangeSet"
    ]), "https://shiftbox-template-fixture-oh.s3.us-east-2.amazonaws.com/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml?versionId=*")
    error_message = "Review may create the one-pass and legacy previews using pinned template URLs."
  }

  assert {
    condition = length([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      s if try(contains(s.Action, "cloudformation:ExecuteChangeSet"), s.Action == "cloudformation:ExecuteChangeSet")
    ]) == 0
    error_message = "Automation cannot execute a change set regardless of name."
  }

  # The new one-pass name grants event readback without changing execution authority.
  assert {
    condition = length([
      for arn in one([
        for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement : s.Resource if s.Sid == "ReadOperationEvents"
      ]) : arn if endswith(arn, "-one-pass/*")
    ]) == 4
    error_message = "One-pass validation must read change-set events in each supported region."
  }

  assert {
    condition     = !strcontains(aws_iam_role_policy.deploy.policy, "staffed-cloudformation")
    error_message = "No GitHub role can pass the staffed operator service role."
  }

}

# The demo and publisher cannot be selected as the qualification account.
run "rejects_demo" {
  command = plan

  variables {
    testing_account_id = "524941137496"
  }

  expect_failures = [var.testing_account_id]
}

run "rejects_publisher" {
  command = plan

  variables {
    testing_account_id = "222222222222"
  }

  expect_failures = [var.testing_account_id]
}
