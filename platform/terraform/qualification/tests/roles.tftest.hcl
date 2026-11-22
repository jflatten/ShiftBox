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
    condition     = jsondecode(aws_iam_role.actions["deploy"].assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten/ShiftBox:environment:aws-qualification"
    error_message = "Deployment trust must bind the exact repository and environment."
  }

  assert {
    condition     = jsondecode(aws_iam_role.actions["cleanup"].assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten/ShiftBox:environment:aws-qualification-cleanup"
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
