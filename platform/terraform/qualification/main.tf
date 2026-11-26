terraform {
  required_version = ">= 1.12.0, < 2.0.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

variable "testing_account_id" {
  type        = string
  description = "Confirmed dedicated ShiftBox qualification account."

  validation {
    condition = (
      can(regex("^[0-9]{12}$", var.testing_account_id)) &&
      var.testing_account_id != "524941137496" && var.testing_account_id != var.publisher_account_id
    )
    error_message = "Use the dedicated testing account, separate from the demo and publisher."
  }
}

variable "publisher_account_id" {
  type        = string
  description = "Independently confirmed ShiftBox publisher account."

  validation {
    condition     = can(regex("^[0-9]{12}$", var.publisher_account_id)) && var.publisher_account_id != "524941137496"
    error_message = "Supply the approved publisher account, separate from the demo."
  }
}

variable "release_template_buckets" {
  type        = map(string)
  description = "Approved dedicated publisher bucket in each release region."

  validation {
    condition = (
      toset(keys(var.release_template_buckets)) == toset(["us-east-1", "us-east-2", "us-west-1", "us-west-2"]) &&
      length(distinct(values(var.release_template_buckets))) == 4 &&
      alltrue([for bucket in values(var.release_template_buckets) : can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", bucket))])
    )
    error_message = "Supply four distinct approved release buckets keyed by supported region."
  }
}

variable "create_github_oidc_provider" {
  type        = bool
  default     = false
  description = "Create the non-billable provider only if absent and explicitly approved."
}

variable "cloudformation_execution_policy_arn" {
  type        = string
  default     = null
  description = "Reviewed customer-managed policy for the exact RC stack. Omit when the dedicated service role is managed separately."

  validation {
    condition = var.cloudformation_execution_policy_arn == null ? true : can(regex(
      "^arn:aws:iam::${var.testing_account_id}:policy/shiftbox-qualification-cloudformation[-A-Za-z0-9]*$",
      var.cloudformation_execution_policy_arn
    ))
    error_message = "Supply a dedicated qualification policy in the testing account."
  }
}

provider "aws" {
  region              = "us-east-2"
  allowed_account_ids = [var.testing_account_id]
}

locals {
  account = var.testing_account_id
  regions = ["us-east-1", "us-east-2", "us-west-1", "us-west-2"]
  environments = {
    deploy  = "aws-qualification"
    cleanup = "aws-qualification-cleanup"
  }
  # Match the repository's verified immutable GitHub OIDC subject mode.
  github_subject = "repo:jflatten@11561330/ShiftBox@1457179716"
  oidc           = "arn:aws:iam::${local.account}:oidc-provider/token.actions.githubusercontent.com"
  service        = "arn:aws:iam::${local.account}:role/shiftbox-qualification-cloudformation"
  stacks         = [for region in local.regions : "arn:aws:cloudformation:${region}:${local.account}:stack/sbq-*-*/*"]
  changes = flatten([
    for region in local.regions : [
      for phase in ["foundation", "one-pass"] :
      "arn:aws:cloudformation:${region}:${local.account}:changeSet/sbq-*-*-${phase}/*"
    ]
  ])
  tags = { project = "shiftbox", cluster_name = "shiftbox-qualification-access" }
  ec2_read = [
    "ec2:DescribeAddresses", "ec2:DescribeAvailabilityZones", "ec2:DescribeImages",
    "ec2:DescribeInstanceAttribute", "ec2:DescribeInstanceCreditSpecifications",
    "ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:DescribeInstanceTypeOfferings",
    "ec2:DescribeInstanceTypes", "ec2:DescribeNatGateways", "ec2:DescribeNetworkInterfaces",
    "ec2:DescribeRouteTables", "ec2:DescribeSecurityGroups", "ec2:DescribeSecurityGroupRules",
    "ec2:DescribeSubnets", "ec2:DescribeTags", "ec2:DescribeVolumes", "ec2:DescribeVolumeAttribute",
    "ec2:DescribeVpcAttribute", "ec2:DescribeVpcs"
  ]
  region_guard = { StringEquals = { "aws:RequestedRegion" = local.regions } }
  run_tags = {
    StringEquals = { "aws:ResourceTag/project" = "shiftbox" }
    StringLike   = { "aws:ResourceTag/cluster_name" = "sbq-*-*" }
  }
  stack_tags = {
    StringEquals = { "aws:ResourceTag/project" = "shiftbox" }
    StringLike = {
      "aws:ResourceTag/cluster_name"               = "sbq-*-*"
      "aws:ResourceTag/shiftbox_qualification_run" = "sbq-*-*"
    }
  }
  probe_tags = {
    StringEquals = { "aws:RequestTag/project" = "shiftbox", "aws:RequestedRegion" = local.regions }
    StringLike   = { "aws:RequestTag/cluster_name" = "sbq-*-*-p" }
  }
  probe_resources = flatten([
    for region in local.regions : [
      for kind in ["instance", "volume", "network-interface", "vpc", "subnet", "security-group", "elastic-ip"] :
      "arn:aws:ec2:${region}:${local.account}:${kind}/*"
    ]
  ])
  probe_cleanup = [
    "ec2:TerminateInstances", "ec2:DeleteVolume", "ec2:DeleteNetworkInterface",
    "ec2:ReleaseAddress", "ec2:DeleteSubnet", "ec2:DeleteSecurityGroup", "ec2:DeleteVpc"
  ]
  cfn_read = [
    "cloudformation:DescribeStacks", "cloudformation:DescribeChangeSet", "cloudformation:GetTemplate",
    "cloudformation:GetStackPolicy", "cloudformation:ListStackResources"
  ]
  template_urls = {
    for region, bucket in var.release_template_buckets :
    region => "https://${bucket}.s3.${region}.amazonaws.com/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml?versionId=*"
  }
  urls = values(local.template_urls)
}

resource "aws_iam_openid_connect_provider" "github" {
  count          = var.create_github_oidc_provider ? 1 : 0
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  tags           = local.tags
}

resource "aws_iam_role" "actions" {
  for_each             = local.environments
  name                 = "shiftbox-qualification-${each.key}"
  max_session_duration = each.key == "deploy" ? 7200 : 3600
  tags                 = local.tags
  depends_on           = [aws_iam_openid_connect_provider.github]

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = { Federated = local.oidc }
      Condition = { StringEquals = {
        "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
        "token.actions.githubusercontent.com:sub" = "${local.github_subject}:environment:${each.value}"
      } }
    }]
  })
}

resource "aws_iam_role" "cloudformation" {
  count = var.cloudformation_execution_policy_arn == null ? 0 : 1
  name  = "shiftbox-qualification-cloudformation"
  tags  = local.tags
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "cloudformation.amazonaws.com" }
      Condition = {
        StringEqualsIfExists = { "aws:SourceAccount" = local.account }
        ArnLikeIfExists      = { "aws:SourceArn" = local.stacks }
      }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "cloudformation" {
  count      = length(aws_iam_role.cloudformation)
  role       = aws_iam_role.cloudformation[count.index].name
  policy_arn = var.cloudformation_execution_policy_arn
}

resource "aws_iam_role_policy" "deploy" {
  name = "QualificationReviewedChangeSetsAndProbes"
  role = aws_iam_role.actions["deploy"].name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "ReadRegionalProbeInventory", Effect = "Allow", Action = local.ec2_read, Resource = "*", Condition = local.region_guard },
      {
        Sid = "ReadDeploymentQuotas", Effect = "Allow", Action = "servicequotas:GetServiceQuota"
        Resource = flatten([
          for region in local.regions : [
            for service in ["ec2", "vpc", "iam"] : "arn:aws:servicequotas:${region}:${local.account}:${service}/L-*"
          ]
        ])
      },
      {
        Sid    = "ReadQuotaValidationInputs", Effect = "Allow", Resource = "*", Condition = local.region_guard
        Action = ["cloudwatch:GetMetricData", "iam:GetAccountSummary"]
      },
      {
        Sid       = "ValidateReviewedTemplate", Effect = "Allow", Action = "cloudformation:ValidateTemplate", Resource = "*"
        Condition = { StringLike = { "cloudformation:TemplateUrl" = local.urls } }
      },
      { Sid = "ReadQualificationChangeSets", Effect = "Allow", Action = local.cfn_read, Resource = local.stacks },
      { Sid = "ReadOperationEvents", Effect = "Allow", Action = "cloudformation:DescribeEvents", Resource = concat(local.stacks, local.changes) },
      {
        Sid = "CreateReviewedChangeSet", Effect = "Allow", Action = "cloudformation:CreateChangeSet", Resource = local.stacks
        Condition = {
          StringEquals = {
            "cloudformation:RoleArn" = local.service
            "aws:RequestTag/project" = "shiftbox"
          }
          StringLike = {
            "cloudformation:TemplateUrl"                = local.urls
            "cloudformation:ChangeSetName"              = ["sbq-*-*-foundation", "sbq-*-*-one-pass"]
            "aws:RequestTag/cluster_name"               = "sbq-*-*"
            "aws:RequestTag/shiftbox_qualification_run" = "sbq-*-*"
          }
        }
      },
      { Sid = "RemoveOwnedReviewStacks", Effect = "Allow", Action = ["cloudformation:DeleteChangeSet", "cloudformation:DeleteStack"], Resource = local.stacks, Condition = local.stack_tags },
      {
        Sid       = "PassReviewedCloudFormationRole", Effect = "Allow", Resource = local.service, Action = "iam:PassRole"
        Condition = { StringEquals = { "iam:PassedToService" = "cloudformation.amazonaws.com" } }
      },
      {
        Sid    = "CreateTaggedProbeNetwork", Effect = "Allow", Resource = local.probe_resources, Condition = local.probe_tags
        Action = ["ec2:CreateVpc", "ec2:CreateSubnet", "ec2:CreateSecurityGroup"]
      },
      {
        Sid = "TagProbesAtCreation", Effect = "Allow", Resource = local.probe_resources, Action = "ec2:CreateTags"
        Condition = merge(local.probe_tags, {
          StringEquals = merge(local.probe_tags.StringEquals, {
            "ec2:CreateAction" = ["CreateVpc", "CreateSubnet", "CreateSecurityGroup", "RunInstances"]
          })
        })
      },
      {
        Sid = "ManageTaggedProbeNetwork", Effect = "Allow", Resource = local.probe_resources, Condition = merge(local.run_tags, {
          StringLike = { "aws:ResourceTag/cluster_name" = "sbq-*-*-p" }
        })
        Action = concat(local.probe_cleanup, [
          "ec2:ModifyVpcAttribute", "ec2:ModifySubnetAttribute", "ec2:AuthorizeSecurityGroupEgress",
          "ec2:RevokeSecurityGroupEgress", "ec2:ModifyInstanceAttribute"
        ])
      },
      {
        Sid      = "LaunchTaggedProbeCompute", Effect = "Allow", Action = "ec2:RunInstances"
        Resource = [for arn in local.probe_resources : arn if strcontains(arn, ":instance/")]
        Condition = merge(local.probe_tags, {
          StringEquals = merge(local.probe_tags.StringEquals, { "ec2:InstanceType" = "t3.medium", "ec2:MetadataHttpTokens" = "required" })
        })
      },
      {
        Sid       = "LaunchEncryptedProbeStorage", Effect = "Allow", Action = "ec2:RunInstances"
        Resource  = [for arn in local.probe_resources : arn if strcontains(arn, ":volume/")]
        Condition = merge(local.probe_tags, { Bool = { "ec2:Encrypted" = "true" } })
      },
      {
        Sid       = "UseOnlyTaggedProbeSubnetsAndGroups", Effect = "Allow", Action = "ec2:RunInstances"
        Resource  = [for arn in local.probe_resources : arn if strcontains(arn, ":subnet/") || strcontains(arn, ":security-group/")]
        Condition = merge(local.run_tags, { StringLike = { "aws:ResourceTag/cluster_name" = "sbq-*-*-p" } })
      },
      {
        Sid      = "UseOnlyPublicPublisherImages", Effect = "Allow", Action = "ec2:RunInstances"
        Resource = [for region in local.regions : "arn:aws:ec2:${region}::image/*"]
        Condition = {
          StringEquals = { "aws:RequestedRegion" = local.regions, "ec2:Owner" = var.publisher_account_id }
          Bool         = { "ec2:Public" = "true" }
        }
      },
      {
        Sid       = "UseOnlyPrivateProbeInterfaces", Effect = "Allow", Action = "ec2:RunInstances"
        Resource  = [for arn in local.probe_resources : arn if strcontains(arn, ":network-interface/")]
        Condition = merge(local.region_guard, { BoolIfExists = { "ec2:AssociatePublicIpAddress" = "false" } })
      }
    ]
  })
}

resource "aws_iam_role_policy" "cleanup" {
  name = "QualificationOwnedResourcesTeardown"
  role = aws_iam_role.actions["cleanup"].name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ReadRegionalInventory", Effect = "Allow", Resource = "*", Condition = local.region_guard
        Action = concat(local.ec2_read, ["tag:GetResources", "cloudformation:ListStacks"])
      },
      {
        Sid = "ReadQualificationStacks", Effect = "Allow", Resource = local.stacks, Action = local.cfn_read
      },
      {
        Sid    = "DeleteOwnedStacks", Effect = "Allow", Resource = local.stacks, Condition = local.stack_tags
        Action = ["cloudformation:DeleteStack", "cloudformation:UpdateTerminationProtection"]
      },
      {
        Sid    = "DeleteOwnedEC2Remnants", Effect = "Allow", Resource = local.probe_resources, Condition = local.run_tags
        Action = local.probe_cleanup
      },
      {
        Sid      = "DeleteOwnedRetainedPostgresSecrets", Effect = "Allow", Condition = local.run_tags
        Resource = "arn:aws:secretsmanager:us-east-2:${local.account}:secret:sbq-*-*-Postgres*"
        Action   = ["secretsmanager:DescribeSecret", "secretsmanager:DeleteSecret"]
      }
    ]
  })
}

output "deploy_role_arn" {
  value = aws_iam_role.actions["deploy"].arn
}

output "cleanup_role_arn" {
  value = aws_iam_role.actions["cleanup"].arn
}

output "reviewed_cloudformation_role_arn" {
  description = "Required separately reviewed execution role with trust and permissions for the exact release template."
  value       = local.service
}
