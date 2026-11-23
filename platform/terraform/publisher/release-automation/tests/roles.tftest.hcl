mock_provider "aws" {}

variables {
  publisher_account_id = "111122223333"
}

# IAM role retirement needs to inventory the expiry role's instance profiles.
# A broad Elastic IP disassociation grant would let a failed cleanup reach
# outside this attempt, so the network roles must keep that action unavailable.
run "expiry_role_cleanup_reads_are_scoped" {
  command = plan

  assert {
    condition = alltrue([
      for scope, document in {
        cleanup   = aws_iam_role_policy.cleanup.policy
        provision = aws_iam_role_policy.provision.policy
        rehearsal = aws_iam_role_policy.rehearsal.policy
      } :
      try(one([for grant in jsondecode(document).Statement : grant if contains(flatten([grant.Action]), "iam:ListInstanceProfilesForRole")]).Resource, []) == (
        scope == "rehearsal" ? [for arn in local.expiry_role_arns : replace(arn, "rel-??????????????", "rel-r?????????????")] : local.expiry_role_arns
      ) && !contains(flatten([for grant in jsondecode(document).Statement : grant.Action]), "ec2:DisassociateAddress")
    ])
    error_message = "Network roles may inspect only their own expiry profiles and must release detached addresses."
  }
}

# Live Terraform TAG-on-create and resource refresh need these narrow grants.
run "network_provider_api_dependencies" {
  command = plan

  assert {
    condition = (
      contains(flatten([for grant in local.network_inventory : grant.Action]), "ec2:DescribeAddressesAttribute") &&
      local.iam_expiry_tag.Condition.StringEqualsIfExists["aws:ResourceTag/publisher_attempt"] == "$${aws:RequestTag/publisher_attempt}" &&
      local.iam_expiry_tag.Condition.StringEqualsIfExists["aws:ResourceTag/environment"] == "$${aws:RequestTag/environment}" &&
      local.iam_expiry_tag.Resource == local.expiry_role_arns &&
      one([for grant in local.network_provision_expiry : grant if try(grant.Sid, "") == "LimitExpiryLogRetention"]).Condition == local.region_condition
    )
    error_message = "Elastic IP refresh, expiry role creation and named log retention must work without unscoped write access."
  }

  assert {
    condition = one([for grant in local.network_provision_ec2_existing : grant if try(grant.Sid, "") == "TagOnlyNewEndpointInterface"]) == {
      Sid      = "TagOnlyNewEndpointInterface"
      Effect   = "Allow"
      Action   = "ec2:CreateTags"
      Resource = "arn:aws:ec2:us-east-2:111122223333:network-interface/*"
      Condition = {
        StringEquals                = { "aws:RequestedRegion" = "us-east-2", "ec2:CreateAction" = "CreateNetworkInterface" }
        "ForAllValues:StringEquals" = { "aws:TagKeys" = ["InstanceConnectEndpointId"] }
        StringLike                  = { "aws:RequestTag/InstanceConnectEndpointId" = "eice-*" }
      }
    }
    error_message = "The EICE service-created interface may receive only its endpoint ID tag on creation in Ohio."
  }
}

# The pinned provider reads versions and code signing when refreshing the
# unqualified ZIP expiry function, including when it prepares a destroy plan.
run "refresh_zip_expiry_function" {
  command = plan

  assert {
    condition = alltrue([
      for scope, document in {
        provision = aws_iam_role_policy.provision.policy
        cleanup   = aws_iam_role_policy.cleanup.policy
        rehearsal = aws_iam_role_policy.rehearsal.policy
      } :
      alltrue([
        for action in ["lambda:GetFunctionCodeSigningConfig", "lambda:ListVersionsByFunction"] :
        try(one([for grant in jsondecode(document).Statement : grant if contains(flatten([grant.Action]), action)]).Resource, "") == (
          scope == "rehearsal" ? replace(local.expiry_function, "rel-??????????????", "rel-r?????????????") : local.expiry_function
        )
      ])
    ])
    error_message = "All network roles must refresh the bounded ZIP expiry function without broader Lambda access."
  }
}

# The manual workflow identity cannot reach release journals, expiry names,
# image mutation or the temporary identities that publish candidate artifacts.
run "isolated_network_rehearsal" {
  command = plan

  assert {
    condition = (
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringEquals == {
        "token.actions.githubusercontent.com:aud"      = "sts.amazonaws.com"
        "token.actions.githubusercontent.com:sub"      = "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-release-network-rehearsal"
        "token.actions.githubusercontent.com:actor_id" = "11561330"
        "token.actions.githubusercontent.com:ref"      = "refs/heads/main"
        "token.actions.githubusercontent.com:workflow" = "Rehearse publisher build network"
      } &&
      jsondecode(aws_iam_role.rehearsal.assume_role_policy).Statement[0].Condition.StringLike["sts:RoleSessionName"] == "shiftbox-release-rehearsal-*-1"
    )
    error_message = "Only the named main rehearsal and maintainer may federate."
  }

  assert {
    condition = (
      length(aws_iam_policy.rehearsal) == 3 &&
      length(aws_iam_role_policy.rehearsal.policy) <= 10240 &&
      alltrue([for policy in aws_iam_policy.rehearsal : length(policy.policy) <= 6144]) &&
      alltrue([for statement in jsondecode(aws_iam_role_policy.rehearsal.policy).Statement :
        alltrue([for resource in flatten([statement.Resource]) : strcontains(resource, "/release-builds/rehearsals/")])
        if contains(["ReadAndWriteAttemptState", "ReleaseOnlyTerraformStateLocks", "ReadRehearsalRecords", "WriteRehearsalRecords"], try(statement.Sid, ""))
      ]) &&
      alltrue([for document in values(local.rehearsal_documents) :
        !strcontains(document, "rel-??????????????") && !strcontains(document, "release-v*-rc.*") &&
        !can(regex("(RunInstances|TerminateInstances|ModifyImageAttribute|CopyImage|CreateImage|RegisterImage|DeregisterImage|DeleteSnapshot|/phases/)", document))
      ])
    )
    error_message = "The rehearsal must be confined to isolated network state and resources within the IAM size limits."
  }
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
