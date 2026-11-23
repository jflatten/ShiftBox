mock_provider "aws" {}

variables {
  publisher_account_id = "111122223333"
}

# Only the protected candidate workflow and maintainer may federate as the
# network provisioner. Stable tags and ordinary branch events never match.
run "protected_candidate_federation" {
  command = plan

  assert {
    condition = jsondecode(aws_iam_role.provision.assume_role_policy).Statement == [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = { Federated = "arn:aws:iam::111122223333:oidc-provider/token.actions.githubusercontent.com" }
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud"      = "sts.amazonaws.com"
          "token.actions.githubusercontent.com:sub"      = "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-release-candidate"
          "token.actions.githubusercontent.com:actor_id" = "11561330"
          "token.actions.githubusercontent.com:workflow" = "Public AWS release"
        }
        StringLike = {
          "token.actions.githubusercontent.com:ref" = "refs/tags/v*"
          "sts:RoleSessionName"                     = "shiftbox-release-network-*"
        }
      }
    }]
    error_message = "The network provisioner must trust only the approved protected candidate workflow."
  }
}

# Recovery runs on protected main and cannot create network infrastructure,
# run instances, change IAM trust, or touch release AMIs and snapshots.
run "cleanup_only" {
  command = plan

  assert {
    condition = (
      jsondecode(aws_iam_role.cleanup.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-release-cleanup" &&
      jsondecode(aws_iam_role.cleanup.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:ref"] == "refs/heads/main" &&
      jsondecode(aws_iam_role.cleanup.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:workflow"] == "Clean expired publisher build networks" &&
      jsondecode(aws_iam_role.cleanup.assume_role_policy).Statement[0].Condition.StringLike["sts:RoleSessionName"] == "shiftbox-release-cleanup-*"
    )
    error_message = "The recovery identity must use a distinct protected workflow and environment on main."
  }

  assert {
    condition = alltrue([
      for action in flatten([for grant in jsondecode(aws_iam_role_policy.cleanup.policy).Statement : grant.Action]) :
      !can(regex(":(Create|Put|Update|Run|Register|Deregister|Copy|ModifyImage|DeleteSnapshot|Assume|PassRole)", action)) ||
      action == "s3:PutObject"
    ])
    error_message = "The cleanup role must not create resources or mutate release artifacts."
  }

  assert {
    condition = (
      contains(flatten([for grant in jsondecode(aws_iam_role_policy.cleanup.policy).Statement : grant.Action]), "ec2:DeleteVpc") &&
      contains(flatten([for grant in jsondecode(aws_iam_role_policy.cleanup.policy).Statement : grant.Action]), "iam:DeleteRolePolicy")
    )
    error_message = "Recovery must be able to remove the disposable network and expiry policies."
  }
}

# The provisioner may install policies only on its own bounded expiry roles
# and may pass each role only to the intended service.
run "expiry_cannot_escalate" {
  command = plan

  assert {
    condition = (
      local.iam_expiry_create.Condition.StringEquals["iam:PermissionsBoundary"] == "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-expiry-boundary" &&
      local.iam_expiry_policy.Condition.StringEquals["iam:PermissionsBoundary"] == local.iam_expiry_create.Condition.StringEquals["iam:PermissionsBoundary"] &&
      toset(flatten([for grant in local.iam_expiry_pass : grant.Resource])) == toset([
        "arn:aws:iam::111122223333:role/shiftbox-publisher-rel-??????????????-expiry",
        "arn:aws:iam::111122223333:role/shiftbox-publisher-rel-??????????????-expiry-invoke"
      ]) &&
      alltrue([for grant in local.iam_expiry_pass :
        endswith(grant.Resource, "-invoke") ?
        grant.Condition.StringEquals["iam:PassedToService"] == "scheduler.amazonaws.com" :
        grant.Condition.StringEquals["iam:PassedToService"] == "lambda.amazonaws.com"
      ])
    )
    error_message = "Only the two attempt expiry roles may be passed, and policy updates require the bootstrap boundary."
  }

  assert {
    condition = alltrue([
      for action in flatten([for grant in jsondecode(aws_iam_policy.expiry_boundary.policy).Statement : grant.Action]) :
      contains([
        "logs:CreateLogStream", "logs:PutLogEvents", "ec2:DescribeAddresses", "ec2:DescribeInstances",
        "ec2:DescribeNatGateways", "ec2:DescribeNetworkInterfaces", "ec2:DescribeSubnets", "ec2:DescribeVolumes",
        "ec2:DescribeVpcs", "ec2:TerminateInstances", "ec2:DeleteNatGateway", "ec2:ReleaseAddress",
        "ec2:DeleteNetworkInterface", "ec2:DeleteVolume", "lambda:InvokeFunction"
      ], action)
    ])
    error_message = "The expiry boundary must cap inline permissions at the expected cleanup APIs."
  }
}

# Tagging can describe creation but cannot adopt an existing unrelated VPC.
# The policy set must also fit AWS's limits before it is reviewed for apply.
run "network_mutation_scope_and_policy_limits" {
  command = plan

  assert {
    condition = (
      one([for grant in local.network_provision_ec2_existing : grant if try(grant.Sid, "") == "TagOnlyOnNetworkCreate"]).Condition.StringEquals["ec2:CreateAction"] == keys(local.network_creates) &&
      !contains(flatten([for grant in local.network_provision_ec2_existing : grant.Action]), "ec2:DeleteTags") &&
      !contains(flatten([for grant in local.network_provision_ec2_new : grant.Action]), "ec2:RunInstances") &&
      alltrue([
        for grant in local.network_retire : try(grant.Condition.StringLike["aws:ResourceTag/publisher_attempt"], "") == "rel-??????????????"
        if anytrue([for action in grant.Action : startswith(action, "ec2:")])
      ])
    )
    error_message = "Network mutation must have an ownership check and cannot start instances or retag existing resources."
  }

  assert {
    condition = (
      length(aws_iam_role_policy.provision.policy) <= 10240 &&
      length(aws_iam_role_policy.cleanup.policy) <= 10240 &&
      length(aws_iam_policy.provision_ec2_new.policy) <= 6144 &&
      length(aws_iam_policy.provision_ec2_existing.policy) <= 6144 &&
      length(aws_iam_policy.provision_expiry.policy) <= 6144 &&
      length(aws_iam_policy.expiry_boundary.policy) <= 6144
    )
    error_message = "The generated policy documents must fit the AWS IAM role inline and managed policy size limits."
  }
}
