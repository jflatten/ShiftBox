locals {
  oidc                = "token.actions.githubusercontent.com"
  github_subject      = "repo:jflatten@11561330/ShiftBox@1457179716"
  oidc_principal      = { Federated = "arn:aws:iam::${var.publisher_account_id}:oidc-provider/${local.oidc}" }
  expiry_policy_name  = "shiftbox-publisher-release-expiry-boundary"
  expiry_boundary_arn = "arn:aws:iam::${var.publisher_account_id}:policy/${local.expiry_policy_name}"

  iam_expiry_create = {
    Sid      = "CreateOnlyBoundedTaggedExpiryRoles"
    Effect   = "Allow"
    Action   = ["iam:CreateRole"]
    Resource = local.expiry_role_arns
    Condition = merge(local.request_condition, {
      StringEquals = merge(local.request_condition.StringEquals, { "iam:PermissionsBoundary" = local.expiry_boundary_arn })
    })
  }
  iam_expiry_tag = {
    Sid      = "TagOwnedExpiryRolesOnly"
    Effect   = "Allow"
    Action   = ["iam:TagRole"]
    Resource = local.expiry_role_arns
    Condition = merge(local.request_condition, {
      # IAM evaluates TagRole during CreateRole with the incoming tags already
      # available as resource tags. Existing roles must keep all ownership tags.
      StringEqualsIfExists = {
        for tag in concat(keys(local.owned_network_tags), keys(local.release_network_tags)) :
        "aws:ResourceTag/${tag}" => "$${aws:RequestTag/${tag}}"
      }
    })
  }
  iam_expiry_policy = {
    Sid      = "ConfigureOnlyBoundedExpiryRoles"
    Effect   = "Allow"
    Action   = ["iam:PutRolePolicy"]
    Resource = local.expiry_role_arns
    Condition = merge(local.owned_condition, {
      StringEquals = merge(local.owned_condition.StringEquals, { "iam:PermissionsBoundary" = local.expiry_boundary_arn })
    })
  }
  iam_expiry_pass = [
    for role, service in {
      (local.expiry_role)        = "lambda.amazonaws.com"
      (local.expiry_role_invoke) = "scheduler.amazonaws.com"
      } : {
      Effect    = "Allow"
      Action    = ["iam:PassRole"]
      Resource  = role
      Condition = { StringEquals = { "iam:PassedToService" = service } }
    }
  ]
  network_read_and_retire = concat(local.state_access, local.network_inventory, local.network_retire)
}

resource "aws_iam_role" "provision" {
  name                 = "shiftbox-publisher-release-network"
  description          = "Provision and retire the approved release build network in Ohio."
  max_session_duration = 3600
  tags                 = local.tags
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = local.oidc_principal
      Condition = {
        StringEquals = {
          "${local.oidc}:aud"      = "sts.amazonaws.com"
          "${local.oidc}:sub"      = "${local.github_subject}:environment:aws-release-candidate"
          "${local.oidc}:actor_id" = "11561330"
          "${local.oidc}:workflow" = "Public AWS release"
        }
        # GitHub admits candidate tags to this environment. AWS additionally
        # restricts the persistent role to version tag refs. The temporary
        # build, copy and share identities pin the exact candidate ref.
        StringLike = {
          "${local.oidc}:ref"   = "refs/tags/v*"
          "sts:RoleSessionName" = "shiftbox-release-network-*"
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "provision" {
  name   = "ReadAndRetireShiftBoxReleaseNetwork"
  role   = aws_iam_role.provision.name
  policy = jsonencode({ Version = "2012-10-17", Statement = concat(local.network_read_and_retire, [local.state_lease_create]) })
}

resource "aws_iam_policy" "provision_ec2_new" {
  name   = "shiftbox-publisher-release-network-create-ec2"
  tags   = local.tags
  policy = jsonencode({ Version = "2012-10-17", Statement = local.network_provision_ec2_new })
}

resource "aws_iam_policy" "provision_ec2_existing" {
  name   = "shiftbox-publisher-release-network-configure-ec2"
  tags   = local.tags
  policy = jsonencode({ Version = "2012-10-17", Statement = local.network_provision_ec2_existing })
}

resource "aws_iam_policy" "provision_expiry" {
  name   = "shiftbox-publisher-release-network-create-expiry"
  tags   = local.tags
  policy = jsonencode({ Version = "2012-10-17", Statement = local.network_provision_expiry })
}

resource "aws_iam_role_policy_attachment" "provision_ec2_new" {
  role       = aws_iam_role.provision.name
  policy_arn = aws_iam_policy.provision_ec2_new.arn
}

resource "aws_iam_role_policy_attachment" "provision_ec2_existing" {
  role       = aws_iam_role.provision.name
  policy_arn = aws_iam_policy.provision_ec2_existing.arn
}

resource "aws_iam_role_policy_attachment" "provision_expiry" {
  role       = aws_iam_role.provision.name
  policy_arn = aws_iam_policy.provision_expiry.arn
}

resource "aws_iam_policy" "provision_candidate" {
  name   = "shiftbox-publisher-release-network-candidate-access"
  tags   = local.tags
  policy = jsonencode({ Version = "2012-10-17", Statement = local.candidate_provision_access })
}

resource "aws_iam_role_policy_attachment" "provision_candidate" {
  role       = aws_iam_role.provision.name
  policy_arn = aws_iam_policy.provision_candidate.arn
}

resource "aws_iam_role" "cleanup" {
  name                 = "shiftbox-publisher-release-network-cleanup"
  description          = "Recover expired release build networks from the private state backend."
  max_session_duration = 3600
  tags                 = local.tags
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = local.oidc_principal
      Condition = {
        StringEquals = {
          "${local.oidc}:aud"      = "sts.amazonaws.com"
          "${local.oidc}:sub"      = "${local.github_subject}:environment:aws-release-cleanup"
          "${local.oidc}:ref"      = "refs/heads/main"
          "${local.oidc}:workflow" = "Clean expired publisher build networks"
        }
        StringLike = { "sts:RoleSessionName" = "shiftbox-release-cleanup-*" }
      }
    }]
  })
}

resource "aws_iam_role_policy" "cleanup" {
  name   = "RecoverExpiredShiftBoxReleaseNetwork"
  role   = aws_iam_role.cleanup.name
  policy = jsonencode({ Version = "2012-10-17", Statement = local.network_read_and_retire })
}

resource "aws_iam_policy" "cleanup_candidate" {
  name   = "shiftbox-publisher-release-network-cleanup-candidate-access"
  tags   = local.tags
  policy = jsonencode({ Version = "2012-10-17", Statement = local.candidate_cleanup_access })
}

resource "aws_iam_role_policy_attachment" "cleanup_candidate" {
  role       = aws_iam_role.cleanup.name
  policy_arn = aws_iam_policy.cleanup_candidate.arn
}

output "release_network_role_arn" {
  description = "Protected candidate OIDC role for the one-build Terraform network lifecycle."
  value       = aws_iam_role.provision.arn
}

output "release_network_cleanup_role_arn" {
  description = "Protected main OIDC role for independent expired network recovery."
  value       = aws_iam_role.cleanup.arn
}
