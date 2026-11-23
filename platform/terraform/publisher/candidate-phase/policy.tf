variable "phase" {
  description = "Approved permission phase for the first candidate AMI."
  type        = string

  validation {
    condition     = contains(["build", "copy", "share"], var.phase)
    error_message = "Select build, copy or share for the approved candidate job."
  }
}

variable "candidate_tag" {
  description = "Exact reviewed candidate tag from the protected release run."
  type        = string

  validation {
    condition     = length(var.candidate_tag) <= 96 && can(regex("^v(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)-rc[.][1-9][0-9]*$", var.candidate_tag))
    error_message = "An ephemeral AMI role requires a canonical RC tag."
  }
}

variable "execution" {
  description = "Exact approved GitHub run, source pins and absolute deadline for one phase."
  type = object({
    run_id        = string
    run_attempt   = string
    expires_at    = string
    source_commit = string
    source_ami    = string
  })
  validation {
    condition = try(
      can(regex("^[1-9][0-9]{0,19}$", var.execution.run_id)) &&
      can(regex("^[1-9][0-9]{0,7}$", var.execution.run_attempt)) &&
      can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$", var.execution.expires_at)) &&
      can(timeadd(var.execution.expires_at, "0s")) &&
      can(regex("^[0-9a-f]{40}$", var.execution.source_commit)) &&
      can(regex("^ami-[0-9a-f]{17}$", var.execution.source_ami)), false
    )
    error_message = "An active phase requires the exact workflow run, attempt, source commit, source AMI and deadline."
  }
}

variable "build" {
  description = "Exact reviewed Ohio private network, existing SSH key-pair name, and verified Canonical AMI and snapshot."
  type = object({
    attempt_id      = string
    vpc_id          = string
    subnet_id       = string
    subnet_cidr     = string
    security_group  = string
    endpoint_id     = string
    source_snapshot = string
  })
  default = null

  validation {
    condition = var.phase != "build" ? true : try(
      var.build.attempt_id == "rel-${substr(sha256("${var.execution.run_id}/${var.execution.run_attempt}"), 0, 14)}" &&
      can(regex("^vpc-[0-9a-f]{17}$", var.build.vpc_id)) &&
      can(regex("^subnet-[0-9a-f]{17}$", var.build.subnet_id)) &&
      can(regex("^sg-[0-9a-f]{17}$", var.build.security_group)) &&
      can(regex("^eice-[0-9a-f]{17}$", var.build.endpoint_id)) &&
      can(cidrnetmask(var.build.subnet_cidr)) &&
      can(regex("^(10[.][0-9]+[.][0-9]+|172[.](1[6-9]|2[0-9]|3[01])[.][0-9]+|192[.]168[.][0-9]+)[.]64/26$", var.build.subnet_cidr)) &&
      can(regex("^snap-[0-9a-f]{17}$", var.build.source_snapshot)), false
    )
    error_message = "Provide the exact Ohio candidate attempt, private network, EICE endpoint and Canonical root snapshot."
  }
}

variable "built_ami_id" {
  description = "The exact private, verified Ohio output captured from this candidate build."
  type        = string
  default     = null

  validation {
    condition     = !contains(["copy", "share"], var.phase) || can(regex("^ami-[0-9a-f]{17}$", var.built_ami_id))
    error_message = "Copy and share require the exact verified Ohio output AMI ID."
  }
}

variable "regional_amis" {
  description = "Exact four frozen publisher-owned regional AMI IDs from the candidate manifest."
  type        = map(string)
  default     = {}

  validation {
    condition = var.phase != "share" || (
      toset(keys(var.regional_amis)) == toset(["us-east-1", "us-east-2", "us-west-1", "us-west-2"]) &&
      alltrue([for id in values(var.regional_amis) : can(regex("^ami-[0-9a-f]{17}$", id))]) &&
      lookup(var.regional_amis, "us-east-2", "") == var.built_ami_id
    )
    error_message = "Share requires approved pins in all four regions, with Ohio matching the built AMI."
  }
}

locals {
  build_region = "us-east-2"
  copy_regions = ["us-east-1", "us-west-1", "us-west-2"]
  ami_regions  = concat([local.build_region], local.copy_regions)
  release_tag  = var.candidate_tag
  ami_tags = {
    project      = "shiftbox"
    cluster_name = "shiftbox-publisher"
    environment  = "release-${local.release_tag}"
  }

  build_tags          = merge(local.ami_tags, { publisher_attempt = try(var.build.attempt_id, "") })
  build_request_tags  = { for key, value in local.build_tags : "aws:RequestTag/${key}" => value }
  build_resource_tags = { for key, value in local.build_tags : "aws:ResourceTag/${key}" => value }
  session_name        = "shiftbox-ami-${var.phase}-${var.execution.run_id}-${var.execution.run_attempt}"
  expires_at          = var.execution.expires_at
  role_name           = "shiftbox-publisher-release-${var.phase}-${substr(sha256("${var.execution.run_id}/${var.execution.run_attempt}"), 0, 16)}"
  role_tags = merge(local.ami_tags, {
    release_run_id      = var.execution.run_id
    release_run_attempt = var.execution.run_attempt
    release_phase       = var.phase
  })
  ohio     = "arn:aws:ec2:${local.build_region}"
  owned    = "${local.ohio}:${var.publisher_account_id}"
  subnet   = "${local.owned}:subnet/${try(var.build.subnet_id, "")}"
  instance = "${local.owned}:instance/i-*"
  image    = "${local.ohio}::image/ami-*"
  snapshot = "${local.ohio}::snapshot/snap-*"
  # EC2 authorizes creation before it assigns a resource ID.
  new_instance  = "${local.owned}:instance/*"
  new_interface = "${local.owned}:network-interface/*"
  new_volume    = "${local.owned}:volume/*"
  new_image     = "${local.ohio}::image/*"
  new_snapshot  = "${local.ohio}::snapshot/*"
  request_tags = {
    for key, value in local.ami_tags : "aws:RequestTag/${key}" => value
  }
  resource_tags = {
    for key, value in local.ami_tags : "aws:ResourceTag/${key}" => value
  }
  artifact_tags = {
    ShiftBoxArtifact = "workspace-ami"
    ShiftBoxRelease  = local.release_tag
  }
  provenance = {
    ShiftBoxSourceAMI    = try(var.execution.source_ami, "")
    ShiftBoxSourceCommit = try(var.execution.source_commit, "")
  }
  artifact_request_tags = {
    for key, value in merge(local.artifact_tags, local.provenance) : "aws:RequestTag/${key}" => value
  }
  artifact_resource_tags = {
    for key, value in merge(local.artifact_tags, local.provenance) : "aws:ResourceTag/${key}" => value
  }
  provenance_keys = keys(local.provenance)
  copy_keys       = ["ShiftBoxBuildAMI", "ShiftBoxBuildRegion"]

  trust = {
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = "sts:AssumeRoleWithWebIdentity"
      Principal = {
        Federated = "arn:aws:iam::${var.publisher_account_id}:oidc-provider/token.actions.githubusercontent.com"
      }
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud"      = "sts.amazonaws.com"
          "token.actions.githubusercontent.com:sub"      = "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-release-candidate"
          "token.actions.githubusercontent.com:actor_id" = "11561330"
          "token.actions.githubusercontent.com:ref"      = "refs/tags/${local.release_tag}"
          "token.actions.githubusercontent.com:workflow" = "Public AWS release"
          "sts:RoleSessionName"                          = local.session_name
        }
        DateLessThan = { "aws:CurrentTime" = local.expires_at }
      }
    }]
  }

  regional_reads = {
    ReadRegionalSettingsAndInventory = {
      Action   = ["ec2:DescribeImages", "ec2:DescribeSnapshots", "ec2:GetEbsEncryptionByDefault", "ec2:GetImageBlockPublicAccessState"]
      Resource = "*"
      Condition = {
        StringEquals = { "aws:RequestedRegion" = local.ami_regions }
      }
    }
  }

  build_statements = {
    ReadOhioBuildInventory = {
      Action = [
        "ec2:DescribeAvailabilityZones", "ec2:DescribeImageAttribute", "ec2:DescribeInstanceConnectEndpoints",
        "ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:DescribeKeyPairs", "ec2:DescribeRegions",
        "ec2:DescribeSnapshotAttribute",
        "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeSecurityGroups", "ec2:DescribeSubnets",
        "ec2:DescribeVolumes", "ec2:DescribeVpcs"
      ]
      Resource  = "*"
      Condition = { StringEquals = { "aws:RequestedRegion" = local.build_region } }
    }

    OpenReviewedTunnel = {
      Action   = "ec2-instance-connect:OpenTunnel"
      Resource = "${local.owned}:instance-connect-endpoint/${try(var.build.endpoint_id, "")}"
      Condition = {
        StringEquals          = local.build_resource_tags
        IpAddress             = { "ec2-instance-connect:privateIpAddress" = try(var.build.subnet_cidr, "") }
        NumericEquals         = { "ec2-instance-connect:remotePort" = 22 }
        NumericLessThanEquals = { "ec2-instance-connect:maxTunnelDuration" = 2700 }
      }
    }

    PinnedLaunchInputs = {
      Action = "ec2:RunInstances"
      Resource = [
        "${local.ohio}::image/${try(var.execution.source_ami, "")}",
        "${local.ohio}::snapshot/${try(var.build.source_snapshot, "")}",
        local.subnet,
        "${local.owned}:security-group/${try(var.build.security_group, "")}",
        "${local.owned}:key-pair/shiftbox-publisher-${try(var.build.attempt_id, "")}-packer"
      ]
    }

    PrivateBuildInterface = {
      Action   = "ec2:RunInstances"
      Resource = local.new_interface
      Condition = {
        ArnEquals = {
          "ec2:Subnet" = local.subnet
          "ec2:Vpc"    = "${local.owned}:vpc/${try(var.build.vpc_id, "")}"
        }
        Bool         = { "ec2:AssociatePublicIpAddress" = "false" }
        StringEquals = local.build_request_tags
      }
    }

    TaggedBuildInstance = {
      Action   = "ec2:RunInstances"
      Resource = local.new_instance
      Condition = {
        StringEquals = merge(local.build_request_tags, {
          "ec2:InstanceType"       = "t3.medium"
          "ec2:InstanceMarketType" = "on-demand"
          "ec2:MetadataHttpTokens" = "required"
        })
      }
    }

    TaggedBuildRoot = {
      Action   = "ec2:RunInstances"
      Resource = local.new_volume
      Condition = {
        StringEquals  = merge(local.build_request_tags, { "ec2:VolumeType" = "gp3" })
        NumericEquals = { "ec2:VolumeSize" = 32 }
        Bool          = { "ec2:Encrypted" = "false" }
      }
    }

    CaptureTaggedInstance = {
      Action    = "ec2:CreateImage"
      Resource  = local.instance
      Condition = { StringEquals = local.build_resource_tags }
    }

    CreateTaggedImageAndSnapshot = {
      Action    = "ec2:CreateImage"
      Resource  = [local.new_image, local.new_snapshot]
      Condition = { StringEquals = local.build_request_tags }
    }

    TagWhenBuilding = {
      Action   = "ec2:CreateTags"
      Resource = [local.new_instance, local.new_interface, local.new_volume, local.new_image, local.new_snapshot]
      Condition = {
        StringEquals = merge(local.build_request_tags, { "ec2:CreateAction" = ["RunInstances", "CreateImage"] })
        "ForAllValues:StringEquals" = {
          "aws:TagKeys" = keys(local.build_tags)
        }
      }
    }

    SetBuildImageDescription = {
      Action   = "ec2:ModifyImageAttribute"
      Resource = local.image
      Condition = {
        StringEquals = merge(local.build_resource_tags, {
          "ec2:Owner"                 = var.publisher_account_id
          "ec2:Attribute/Description" = "ShiftBox workspace platform ${local.release_tag} on Ubuntu 24.04 Minimal amd64"
        })
        Bool = { "ec2:Public" = "false" }
      }
    }

    RequireBuildImageIMDSv2 = {
      Action   = "ec2:ModifyImageAttribute"
      Resource = local.image
      Condition = {
        StringEquals = merge(local.build_resource_tags, {
          "ec2:Owner"                 = var.publisher_account_id
          "ec2:Attribute/ImdsSupport" = "v2.0"
        })
        Bool = { "ec2:Public" = "false" }
      }
    }

    AddPackerOutputTags = {
      Action   = "ec2:CreateTags"
      Resource = [local.image, local.snapshot]
      Condition = {
        StringEquals = merge(local.build_resource_tags, local.build_request_tags, local.artifact_request_tags, { "ec2:Owner" = var.publisher_account_id })
        "ForAllValues:StringEquals" = {
          "aws:TagKeys" = concat(keys(local.build_tags), keys(local.artifact_tags), local.provenance_keys)
        }
      }
    }

    ReapplySnapshotAttemptTags = {
      Action   = "ec2:CreateTags"
      Resource = local.snapshot
      Condition = {
        StringEquals                = merge(local.build_resource_tags, local.build_request_tags, { "ec2:Owner" = var.publisher_account_id })
        "ForAllValues:StringEquals" = { "aws:TagKeys" = keys(local.build_tags) }
      }
    }

    StopAndTerminateBuild = {
      Action    = ["ec2:StopInstances", "ec2:TerminateInstances"]
      Resource  = local.instance
      Condition = { StringEquals = local.build_resource_tags }
    }

    CleanupUnfinalizedImage = {
      Action   = "ec2:DeregisterImage"
      Resource = local.image
      Condition = {
        StringEquals = merge(local.build_resource_tags, { "ec2:Owner" = var.publisher_account_id })
        Bool         = { "ec2:Public" = "false" }
        Null         = { "aws:ResourceTag/ShiftBoxArtifact" = "true" }
      }
    }

    CleanupUnfinalizedSnapshot = {
      Action   = "ec2:DeleteSnapshot"
      Resource = local.snapshot
      Condition = {
        StringEquals = merge(local.build_resource_tags, { "ec2:Owner" = var.publisher_account_id })
        Null         = { "aws:ResourceTag/ShiftBoxArtifact" = "true" }
      }
    }
  }

  copy_destinations = flatten([
    for region in local.copy_regions : ["arn:aws:ec2:${region}::image/*", "arn:aws:ec2:${region}::snapshot/*"]
  ])
  copy_request_tags = merge(local.request_tags, local.artifact_request_tags, {
    for key, value in merge(local.artifact_tags, {
      ShiftBoxBuildAMI    = coalesce(var.built_ami_id, "missing")
      ShiftBoxBuildRegion = local.build_region
    }) : "aws:RequestTag/${key}" => value
  })
  copy_statements = {
    CopyPinnedOhioBuild = {
      Action   = "ec2:CopyImage"
      Resource = "${local.ohio}::image/${coalesce(var.built_ami_id, "missing")}"
      Condition = {
        StringEquals = {
          "ec2:Owner"           = var.publisher_account_id
          "aws:RequestedRegion" = local.copy_regions
        }
      }
    }

    CopyTaggedDestinations = {
      Action    = "ec2:CopyImage"
      Resource  = local.copy_destinations
      Condition = { StringEquals = merge(local.copy_request_tags, { "aws:RequestedRegion" = local.copy_regions }) }
    }

    TagOnlyDuringCopy = {
      Action   = "ec2:CreateTags"
      Resource = local.copy_destinations
      Condition = {
        StringEquals = merge(local.copy_request_tags, { "ec2:CreateAction" = "CopyImage", "aws:RequestedRegion" = local.copy_regions })
        "ForAllValues:StringEquals" = {
          "aws:TagKeys" = concat(keys(local.ami_tags), keys(local.artifact_tags), local.provenance_keys, local.copy_keys)
        }
      }
    }

    InspectCandidateAttributes = {
      Action   = ["ec2:DescribeImageAttribute", "ec2:DescribeSnapshotAttribute"]
      Resource = flatten([for region in local.ami_regions : ["arn:aws:ec2:${region}::image/ami-*", "arn:aws:ec2:${region}::snapshot/snap-*"]])
      Condition = {
        StringEquals = merge(local.resource_tags, { "ec2:Owner" = var.publisher_account_id })
      }
    }
  }

  share_statements = {
    InspectCandidateAttributes = local.copy_statements.InspectCandidateAttributes

    PublishPinnedCandidates = {
      Action   = "ec2:ModifyImageAttribute"
      Resource = [for region, ami in var.regional_amis : "arn:aws:ec2:${region}::image/${ami}"]
      Condition = {
        StringEquals = merge(local.resource_tags, local.artifact_resource_tags, {
          "ec2:Owner"           = var.publisher_account_id
          "aws:RequestedRegion" = local.ami_regions
        })
        StringEqualsIgnoreCase = { "ec2:Attribute" = "launchPermission" }
        Bool                   = { "ec2:Public" = "false" }
      }
    }
  }

  active_statements = merge(
    local.regional_reads,
    { for sid, statement in local.build_statements : sid => statement if var.phase == "build" },
    { for sid, statement in local.copy_statements : sid => statement if var.phase == "copy" },
    { for sid, statement in local.share_statements : sid => statement if var.phase == "share" },
  )
}

resource "aws_iam_role" "ami_candidate" {
  name                 = local.role_name
  path                 = "/shiftbox-release/"
  description          = "Temporary ShiftBox candidate AMI phase for one protected workflow run."
  assume_role_policy   = jsonencode(local.trust)
  max_session_duration = 3600
  permissions_boundary = "arn:aws:iam::${var.publisher_account_id}:policy/shiftbox-publisher-release-${var.phase}-boundary"
  tags                 = local.role_tags
}

resource "aws_iam_role_policy" "ami_candidate" {
  name = "CandidateWorkspaceAMI"
  role = aws_iam_role.ami_candidate.name
  # Bind both grants and the role's trust to one reviewed job session.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat([
      {
        Sid       = "DenyEveryOtherSession"
        Effect    = "Deny"
        Action    = ["ec2:*", "ec2-instance-connect:*"]
        Resource  = "*"
        Condition = { StringNotEquals = { "aws:userid" = "${aws_iam_role.ami_candidate.unique_id}:${local.session_name}" } }
      },
      {
        Sid       = "DenyAtPhaseDeadline"
        Effect    = "Deny"
        Action    = ["ec2:*", "ec2-instance-connect:*"]
        Resource  = "*"
        Condition = { DateGreaterThanEquals = { "aws:CurrentTime" = local.expires_at } }
      }
      ], [
      for sid, statement in local.active_statements : merge(statement, {
        Sid    = sid
        Effect = "Allow"
        Condition = merge(try(statement.Condition, {}), {
          StringEquals = merge(try(statement.Condition.StringEquals, {}),
          var.phase == "build" ? { "aws:RequestedRegion" = local.build_region } : {})
        })
      })
    ])
  })
}

output "ami_candidate_role_arn" {
  value = aws_iam_role.ami_candidate.arn
}
