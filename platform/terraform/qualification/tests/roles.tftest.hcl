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

# The Ohio DNS update may be previewed and discarded. It cannot be executed
# by the qualification GitHub identity or use the staffed service role.
run "dns_staging_is_preview_only" {
  command = plan

  assert {
    condition = toset([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      s.Condition.StringLike["cloudformation:ChangeSetName"] if s.Sid == "CreateReviewedChangeSet" || s.Sid == "CreateOhioDnsPreview"
      ]) == toset(["sbq-*-*-foundation", "sbq-*-*-dns-zones"]) && one([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      s.Condition.StringLike["cloudformation:TemplateUrl"] if s.Sid == "CreateOhioDnsPreview"
    ]) == "https://shiftbox-template-fixture-oh.s3.us-east-2.amazonaws.com/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml?versionId=*"
    error_message = "Only the named foundation and first DNS preview are allowed."
  }

  assert {
    condition = one([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      s.Condition.StringLike["cloudformation:ChangeSetName"]
      if try(contains(s.Action, "cloudformation:ExecuteChangeSet"), s.Action == "cloudformation:ExecuteChangeSet")
    ]) == "sbq-*-*-foundation"
    error_message = "Only the reviewed foundation change set can be executed."
  }

  assert {
    condition     = !strcontains(aws_iam_role_policy.deploy.policy, "staffed-cloudformation")
    error_message = "No GitHub role can pass the staffed operator service role."
  }

  assert {
    condition = one([
      for s in jsondecode(aws_iam_role_policy.deploy.policy).Statement :
      [for arn in s.Resource : arn if endswith(arn, "-dns-zones/*")] if s.Sid == "ReadOperationEvents"
    ])[0] == "arn:aws:cloudformation:us-east-2:111111111111:changeSet/sbq-*-*-dns-zones/*"
    error_message = "The preview can read pre-deployment events only for the Ohio DNS change set."
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
