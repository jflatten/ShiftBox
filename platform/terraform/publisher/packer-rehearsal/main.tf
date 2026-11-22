terraform {
  required_version = ">= 1.12.0, < 2.0.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

provider "aws" {
  region              = local.region
  allowed_account_ids = [var.publisher_account_id]
}

variable "publisher_account_id" {
  type        = string
  description = "The independently verified ShiftBox publisher AWS account."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.publisher_account_id)) && var.publisher_account_id != "524941137496"
    error_message = "Supply the verified 12-digit publisher account separate from testing and demo."
  }
}

variable "attempt" {
  description = "Reviewed Ohio network outputs, immutable Canonical source and one approved workflow run. Null closes the role."
  type = object({
    id              = string
    run_id          = string
    source_commit   = string
    expires_at      = string
    vpc_id          = string
    subnet_id       = string
    subnet_cidr     = string
    security_group  = string
    endpoint_id     = string
    source_ami      = string
    source_snapshot = string
  })
  default = null

  validation {
    condition = var.attempt == null ? true : (
      can(regex("^rehearsal-[a-z0-9]{1,8}$", var.attempt.id)) &&
      can(regex("^[1-9][0-9]{0,19}$", var.attempt.run_id)) &&
      can(regex("^[0-9a-f]{40}$", var.attempt.source_commit)) &&
      can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$", var.attempt.expires_at)) &&
      can(timeadd(var.attempt.expires_at, "0s")) &&
      can(regex("^vpc-[0-9a-f]{17}$", var.attempt.vpc_id)) &&
      can(regex("^subnet-[0-9a-f]{17}$", var.attempt.subnet_id)) &&
      can(cidrnetmask(var.attempt.subnet_cidr)) &&
      can(regex("^(10[.][0-9]+[.][0-9]+|172[.](1[6-9]|2[0-9]|3[01])[.][0-9]+|192[.]168[.][0-9]+)[.]64/26$", var.attempt.subnet_cidr)) &&
      can(regex("^sg-[0-9a-f]{17}$", var.attempt.security_group)) &&
      can(regex("^eice-[0-9a-f]{17}$", var.attempt.endpoint_id)) &&
      can(regex("^ami-[0-9a-f]{17}$", var.attempt.source_ami)) &&
      can(regex("^snap-[0-9a-f]{17}$", var.attempt.source_snapshot))
    )
    error_message = "Bind a rehearsal-prefixed attempt to the exact verified Ohio inputs and approved run."
  }
}

locals {
  region         = "us-east-2"
  oidc_url       = "token.actions.githubusercontent.com"
  github_subject = "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-packer-rehearsal"
  trust_keys = {
    "${local.oidc_url}:actor_id" = "11561330"
    "${local.oidc_url}:aud"      = "sts.amazonaws.com"
    "${local.oidc_url}:ref"      = "refs/heads/main"
    "${local.oidc_url}:sub"      = local.github_subject
    "${local.oidc_url}:workflow" = "Rehearse private Ohio Packer build"
  }
  scope_tags = {
    project           = "shiftbox"
    cluster_name      = "shiftbox-publisher"
    environment       = "release-v1.0.0-rc.1"
    publisher_attempt = try(var.attempt.id, "")
  }
  request_tags  = { for key, value in local.scope_tags : "aws:RequestTag/${key}" => value }
  resource_tags = { for key, value in local.scope_tags : "aws:ResourceTag/${key}" => value }
  artifacts = {
    ShiftBoxArtifact     = "packer-rehearsal"
    ShiftBoxRelease      = try(var.attempt.id, "")
    ShiftBoxSourceAMI    = try(var.attempt.source_ami, "")
    ShiftBoxSourceCommit = try(var.attempt.source_commit, "")
  }
  artifact_request_tags = { for key, value in local.artifacts : "aws:RequestTag/${key}" => value }
  ohio                  = "arn:aws:ec2:${local.region}"
  owned                 = "${local.ohio}:${var.publisher_account_id}"
  subnet                = "${local.owned}:subnet/${try(var.attempt.subnet_id, "")}"
  vpc                   = "${local.owned}:vpc/${try(var.attempt.vpc_id, "")}"
  instance              = "${local.owned}:instance/i-*"
  interface             = "${local.owned}:network-interface/eni-*"
  volume                = "${local.owned}:volume/vol-*"
  image                 = "${local.ohio}::image/ami-*"
  snapshot              = "${local.ohio}::snapshot/snap-*"
  trusted_principal     = { Federated = "arn:aws:iam::${var.publisher_account_id}:oidc-provider/${local.oidc_url}" }

  # EC2 authorizes RunInstances against its inputs and all the resources it creates.
  grants = {
    ReadOhioBuildInventory = {
      Action = [
        "ec2:DescribeAvailabilityZones", "ec2:DescribeImageAttribute", "ec2:DescribeImages", "ec2:DescribeInstanceConnectEndpoints",
        "ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:DescribeInstanceTypeOfferings",
        "ec2:DescribeKeyPairs", "ec2:DescribeSecurityGroups", "ec2:DescribeSnapshotAttribute", "ec2:DescribeSnapshots",
        "ec2:DescribeSubnets", "ec2:DescribeVolumes", "ec2:DescribeVpcs", "ec2:GetEbsEncryptionByDefault",
      ]
      Resource  = ["*"]
      Condition = {}
    }
    OpenReviewedTunnel = {
      Action   = ["ec2-instance-connect:OpenTunnel"]
      Resource = ["${local.owned}:instance-connect-endpoint/${try(var.attempt.endpoint_id, "")}"]
      Condition = {
        StringEquals          = local.resource_tags
        IpAddress             = { "ec2-instance-connect:privateIpAddress" = try(var.attempt.subnet_cidr, "") }
        NumericEquals         = { "ec2-instance-connect:remotePort" = 22 }
        NumericLessThanEquals = { "ec2-instance-connect:maxTunnelDuration" = 2700 }
      }
    }
    ReviewedLaunchInputs = {
      Action = ["ec2:RunInstances"]
      Resource = [
        "${local.ohio}::image/${try(var.attempt.source_ami, "")}",
        "${local.ohio}::snapshot/${try(var.attempt.source_snapshot, "")}",
        local.subnet,
        "${local.owned}:security-group/${try(var.attempt.security_group, "")}",
        "${local.owned}:key-pair/shiftbox-publisher-${try(var.attempt.id, "")}-packer",
      ]
      Condition = {}
    }
    TaggedPrivateInterface = {
      Action   = ["ec2:RunInstances"]
      Resource = [local.interface]
      Condition = {
        StringEquals = local.request_tags
        ArnEquals    = { "ec2:Subnet" = local.subnet, "ec2:Vpc" = local.vpc }
        Bool         = { "ec2:AssociatePublicIpAddress" = "false" }
      }
    }
    TaggedT3Builder = {
      Action   = ["ec2:RunInstances"]
      Resource = [local.instance]
      Condition = {
        StringEquals = merge(local.request_tags, {
          "ec2:InstanceType"       = "t3.medium"
          "ec2:InstanceMarketType" = "on-demand"
          "ec2:MetadataHttpTokens" = "required"
        })
      }
    }
    TaggedBuildRoot = {
      Action   = ["ec2:RunInstances"]
      Resource = [local.volume]
      Condition = {
        StringEquals  = merge(local.request_tags, { "ec2:VolumeType" = "gp3" })
        NumericEquals = { "ec2:VolumeSize" = 32 }
        Bool          = { "ec2:Encrypted" = "false" }
      }
    }
    ImageOnlyOwnedAttemptBuilder = {
      Action    = ["ec2:CreateImage", "ec2:StopInstances", "ec2:TerminateInstances"]
      Resource  = [local.instance]
      Condition = { StringEquals = local.resource_tags }
    }
    CreateOwnedImageAndSnapshot = {
      Action    = ["ec2:CreateImage"]
      Resource  = [local.image, local.snapshot]
      Condition = { StringEquals = local.request_tags }
    }
    TagAttemptOnCreation = {
      Action   = ["ec2:CreateTags"]
      Resource = [local.instance, local.interface, local.volume, local.image, local.snapshot]
      Condition = {
        StringEquals                = merge(local.request_tags, { "ec2:CreateAction" = ["RunInstances", "CreateImage"] })
        "ForAllValues:StringEquals" = { "aws:TagKeys" = keys(local.scope_tags) }
      }
    }
    StampRehearsalProvenance = {
      Action   = ["ec2:CreateTags"]
      Resource = [local.image, local.snapshot]
      Condition = {
        StringEquals                = merge(local.resource_tags, local.request_tags, local.artifact_request_tags, { "ec2:Owner" = var.publisher_account_id })
        "ForAllValues:StringEquals" = { "aws:TagKeys" = concat(keys(local.scope_tags), keys(local.artifacts)) }
      }
    }
    ReapplySnapshotAttemptTags = {
      Action   = ["ec2:CreateTags"]
      Resource = [local.snapshot]
      Condition = {
        StringEquals                = merge(local.resource_tags, local.request_tags, { "ec2:Owner" = var.publisher_account_id })
        "ForAllValues:StringEquals" = { "aws:TagKeys" = keys(local.scope_tags) }
      }
    }
    SetOnlyReviewedImageDescription = {
      Action   = ["ec2:ModifyImageAttribute"]
      Resource = [local.image]
      Condition = {
        StringEquals = merge(local.resource_tags, {
          "ec2:Owner"                 = var.publisher_account_id
          "ec2:Attribute/Description" = "ShiftBox Packer rehearsal ${try(var.attempt.id, "")}"
        })
        Bool = { "ec2:Public" = "false" }
      }
    }
    RequireImageIMDSv2 = {
      Action   = ["ec2:ModifyImageAttribute"]
      Resource = [local.image]
      Condition = {
        StringEquals = merge(local.resource_tags, {
          "ec2:Owner"                 = var.publisher_account_id
          "ec2:Attribute/ImdsSupport" = "v2.0"
        })
        Bool = { "ec2:Public" = "false" }
      }
    }
    RemoveOnlyPrivateRehearsalImages = {
      Action   = ["ec2:DeregisterImage"]
      Resource = [local.image]
      Condition = {
        StringEquals         = merge(local.resource_tags, { "ec2:Owner" = var.publisher_account_id })
        StringEqualsIfExists = { "aws:ResourceTag/ShiftBoxArtifact" = "packer-rehearsal" }
        Bool                 = { "ec2:Public" = "false" }
      }
    }
    RemoveOnlyRehearsalSnapshots = {
      Action   = ["ec2:DeleteSnapshot"]
      Resource = [local.snapshot]
      Condition = {
        StringEquals         = merge(local.resource_tags, { "ec2:Owner" = var.publisher_account_id })
        StringEqualsIfExists = { "aws:ResourceTag/ShiftBoxArtifact" = "packer-rehearsal" }
      }
    }
  }

  # AWS requires these inventory calls to use *, so their read access is still time and region bounded.
  permissions = jsonencode({
    Version = "2012-10-17"
    Statement = [
      for sid, grant in local.grants : merge(grant, {
        Sid    = sid
        Effect = "Allow"
        Condition = merge(grant.Condition, {
          StringEquals = merge(try(grant.Condition.StringEquals, {}), { "aws:RequestedRegion" = local.region })
          DateLessThan = { "aws:CurrentTime" = try(var.attempt.expires_at, "2000-01-01T00:00:00Z") }
        })
      })
    ]
  })
}

resource "aws_iam_role" "rehearsal" {
  name                 = "shiftbox-publisher-packer-rehearsal"
  description          = "Build one temporary private Ohio Packer AMI through a separately approved rehearsal."
  max_session_duration = 3600
  tags                 = { project = "shiftbox", cluster_name = "shiftbox-publisher" }
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = var.attempt == null ? "Deny" : "Allow"
      Principal = local.trusted_principal
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = merge(
        { StringEquals = merge(local.trust_keys, var.attempt == null ? {} : {
          "sts:RoleSessionName" = "shiftbox-packer-rehearsal-${var.attempt.run_id}-1"
        }) },
        var.attempt == null ? {} : { DateLessThan = { "aws:CurrentTime" = var.attempt.expires_at } }
      )
    }]
  })
}

resource "aws_iam_role_policy" "attempt" {
  count  = var.attempt == null ? 0 : 1
  name   = "OneOhioPackerRehearsal"
  role   = aws_iam_role.rehearsal.name
  policy = local.permissions
}

output "role_arn" {
  value = aws_iam_role.rehearsal.arn
}
