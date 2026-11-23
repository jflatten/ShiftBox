locals {
  phase_names = ["build", "copy", "share"]
  phase_policy_arns = {
    for phase in local.phase_names :
    phase => "arn:aws:iam::${var.publisher_account_id}:policy/shiftbox-publisher-release-${phase}-boundary"
  }
  phase_role_arns = {
    for phase in local.phase_names :
    phase => "arn:aws:iam::${var.publisher_account_id}:role/shiftbox-release/shiftbox-publisher-release-${phase}-*"
  }
  phase_tags = {
    "aws:ResourceTag/project"      = "shiftbox"
    "aws:ResourceTag/cluster_name" = "shiftbox-publisher"
  }
  phase_request_tags = {
    "aws:RequestTag/project"      = "shiftbox"
    "aws:RequestTag/cluster_name" = "shiftbox-publisher"
  }
  phase_role_tag_keys = concat(keys(local.tags), ["environment", "release_run_id", "release_run_attempt", "release_phase"])

  # These identities can only create new bounded roles. They cannot pass a
  # phase role to an AWS service or edit its trust or permissions boundary.
  candidate_provision_access = concat(
    [for phase, boundary in local.phase_policy_arns : {
      Sid      = "Create${title(phase)}PhaseIdentity"
      Effect   = "Allow"
      Action   = ["iam:CreateRole"]
      Resource = local.phase_role_arns[phase]
      Condition = {
        StringEquals = merge(local.phase_request_tags, {
          "iam:PermissionsBoundary"      = boundary
          "aws:RequestTag/release_phase" = phase
        })
      }
    }],
    [for phase, boundary in local.phase_policy_arns : {
      Sid      = "WriteOnly${title(phase)}PhaseInlinePolicy"
      Effect   = "Allow"
      Action   = ["iam:PutRolePolicy"]
      Resource = local.phase_role_arns[phase]
      Condition = {
        StringEquals = merge(local.phase_tags, { "iam:PermissionsBoundary" = boundary })
      }
    }],
    [{
      Sid      = "TagOnlyPublisherPhaseIdentities"
      Effect   = "Allow"
      Action   = ["iam:TagRole"]
      Resource = values(local.phase_role_arns)
      Condition = {
        StringEquals = local.phase_request_tags
        # CreateRole also authorizes TagRole with the incoming tags as
        # resource tags. An existing ownership value must match the request.
        StringEqualsIfExists = {
          for tag in local.phase_role_tag_keys : "aws:ResourceTag/${tag}" => "$${aws:RequestTag/${tag}}"
        }
        "ForAllValues:StringEquals" = { "aws:TagKeys" = local.phase_role_tag_keys }
        Null = merge({ "aws:TagKeys" = "false" }, {
          for tag in local.phase_role_tag_keys : "aws:RequestTag/${tag}" => "false"
        })
      }
    }],
    local.candidate_cleanup_access
  )
  candidate_cleanup_access = [
    {
      # IAM authorizes GetRole by its pathless name while a role is absent.
      # Real role reads and all mutations still require the reserved path.
      Sid      = "CheckUncreatedPhaseRoles"
      Effect   = "Allow"
      Action   = ["iam:GetRole"]
      Resource = [for phase in local.phase_names : "arn:aws:iam::${var.publisher_account_id}:role/shiftbox-publisher-release-${phase}-????????????????"]
    },
    {
      Sid    = "ReadPhaseRoleForTerraform"
      Effect = "Allow"
      Action = [
        "iam:GetRole", "iam:GetRolePolicy", "iam:ListRolePolicies", "iam:ListRoleTags",
        "iam:ListAttachedRolePolicies", "iam:ListInstanceProfilesForRole"
      ]
      Resource = values(local.phase_role_arns)
    },
    {
      Sid      = "RemoveOwnedPhaseRoleAndInlinePolicy"
      Effect   = "Allow"
      Action   = ["iam:DeleteRole", "iam:DeleteRolePolicy"]
      Resource = values(local.phase_role_arns)
      Condition = {
        StringEquals = local.phase_tags
      }
    }
  ]

  phase_regions = ["us-east-1", "us-east-2", "us-west-1", "us-west-2"]
  phase_ohio    = "arn:aws:ec2:us-east-2:${var.publisher_account_id}"
  phase_image   = "arn:aws:ec2:us-east-2::image/*"
  phase_snap    = "arn:aws:ec2:us-east-2::snapshot/*"
  image_arns    = [for region in local.phase_regions : "arn:aws:ec2:${region}::image/*"]
  copy_regions  = [for region in local.phase_regions : region if region != "us-east-2"]
  copy_arns     = flatten([for region in local.copy_regions : ["arn:aws:ec2:${region}::image/*", "arn:aws:ec2:${region}::snapshot/*"]])

  phase_reads = {
    Effect = "Allow"
    Action = [
      "ec2:DescribeImages", "ec2:DescribeSnapshots",
      "ec2:DescribeImageAttribute", "ec2:DescribeSnapshotAttribute",
      "ec2:GetEbsEncryptionByDefault", "ec2:GetImageBlockPublicAccessState"
    ]
    Resource = "*"
    Condition = {
      StringEquals = { "aws:RequestedRegion" = local.phase_regions }
    }
  }

  build_boundary = [
    {
      Effect = "Allow"
      Action = [
        "ec2:DescribeAvailabilityZones", "ec2:DescribeInstanceConnectEndpoints",
        "ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:DescribeKeyPairs",
        "ec2:DescribeRegions", "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeSecurityGroups",
        "ec2:DescribeSubnets", "ec2:DescribeVolumes", "ec2:DescribeVpcs"
      ]
      Resource  = "*"
      Condition = { StringEquals = { "aws:RequestedRegion" = "us-east-2" } }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2-instance-connect:OpenTunnel"]
      Resource = "${local.phase_ohio}:instance-connect-endpoint/*"
      Condition = {
        StringEquals          = local.phase_tags
        StringLike            = { "aws:ResourceTag/publisher_attempt" = "rel-*" }
        NumericEquals         = { "ec2-instance-connect:remotePort" = 22 }
        NumericLessThanEquals = { "ec2-instance-connect:maxTunnelDuration" = 2700 }
      }
    },
    {
      Effect = "Allow"
      Action = ["ec2:RunInstances"]
      Resource = concat([local.phase_image, local.phase_snap], [
        for type in ["instance", "network-interface", "subnet", "security-group", "key-pair", "volume"] :
        "${local.phase_ohio}:${type}/*"
      ])
      Condition = { StringEquals = { "aws:RequestedRegion" = "us-east-2" } }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:CreateImage", "ec2:TerminateInstances", "ec2:StopInstances"]
      Resource = "${local.phase_ohio}:instance/*"
      Condition = {
        StringEquals = local.phase_tags
        StringLike   = { "aws:ResourceTag/publisher_attempt" = "rel-*" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:CreateImage"]
      Resource = [local.phase_image, local.phase_snap]
      Condition = {
        StringEquals = local.phase_request_tags
        StringLike   = { "aws:RequestTag/publisher_attempt" = "rel-*" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:CreateTags"]
      Resource = [local.phase_image, local.phase_snap, "${local.phase_ohio}:instance/*", "${local.phase_ohio}:network-interface/*", "${local.phase_ohio}:volume/*"]
      Condition = {
        StringEquals = local.phase_request_tags
        StringLike   = { "aws:RequestTag/publisher_attempt" = "rel-*" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:ModifyImageAttribute"]
      Resource = local.phase_image
      Condition = {
        StringEquals = merge(local.phase_tags, { "ec2:Owner" = var.publisher_account_id })
        StringLike = {
          "ec2:Attribute/Description"         = "ShiftBox workspace platform v*-rc.* on Ubuntu 24.04 Minimal amd64"
          "aws:ResourceTag/publisher_attempt" = "rel-*"
        }
        Bool = { "ec2:Public" = "false" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:ModifyImageAttribute"]
      Resource = local.phase_image
      Condition = {
        StringEquals = merge(local.phase_tags, { "ec2:Owner" = var.publisher_account_id, "ec2:Attribute/ImdsSupport" = "v2.0" })
        StringLike   = { "aws:ResourceTag/publisher_attempt" = "rel-*" }
        Bool         = { "ec2:Public" = "false" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:DeregisterImage"]
      Resource = local.phase_image
      Condition = {
        StringEquals = merge(local.phase_tags, { "ec2:Owner" = var.publisher_account_id })
        StringLike   = { "aws:ResourceTag/publisher_attempt" = "rel-*" }
        Null         = { "aws:ResourceTag/ShiftBoxArtifact" = "true" }
        Bool         = { "ec2:Public" = "false" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:DeleteSnapshot"]
      Resource = local.phase_snap
      Condition = {
        StringEquals = merge(local.phase_tags, { "ec2:Owner" = var.publisher_account_id })
        StringLike   = { "aws:ResourceTag/publisher_attempt" = "rel-*" }
        Null         = { "aws:ResourceTag/ShiftBoxArtifact" = "true" }
      }
    },
  ]

  copy_boundary = [
    {
      Effect   = "Allow"
      Action   = ["ec2:CopyImage"]
      Resource = local.phase_image
      Condition = {
        StringEquals = { "ec2:Owner" = var.publisher_account_id, "aws:RequestedRegion" = local.copy_regions }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:CopyImage"]
      Resource = local.copy_arns
      Condition = {
        StringEquals = merge(local.phase_request_tags, { "aws:RequestedRegion" = local.copy_regions })
        StringLike   = { "aws:RequestTag/ShiftBoxRelease" = "v*-rc.*" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:CreateTags"]
      Resource = local.copy_arns
      Condition = {
        StringEquals = merge(local.phase_request_tags, { "aws:RequestedRegion" = local.copy_regions, "ec2:CreateAction" = "CopyImage" })
        StringLike   = { "aws:RequestTag/ShiftBoxRelease" = "v*-rc.*" }
      }
    },
  ]

  share_boundary = [{
    Effect   = "Allow"
    Action   = ["ec2:ModifyImageAttribute"]
    Resource = local.image_arns
    Condition = {
      StringEquals = merge(local.phase_tags, {
        "ec2:Owner"                        = var.publisher_account_id
        "aws:RequestedRegion"              = local.phase_regions
        "aws:ResourceTag/ShiftBoxArtifact" = "workspace-ami"
      })
      StringLike             = { "aws:ResourceTag/ShiftBoxRelease" = "v*-rc.*" }
      StringEqualsIgnoreCase = { "ec2:Attribute" = "launchPermission" }
      Bool                   = { "ec2:Public" = "false" }
    }
  }]

  candidate_boundaries = {
    build = concat([local.phase_reads], local.build_boundary)
    copy  = concat([local.phase_reads], local.copy_boundary)
    share = concat([local.phase_reads], local.share_boundary)
  }
}

resource "aws_iam_policy" "candidate_boundary" {
  for_each    = local.candidate_boundaries
  name        = "shiftbox-publisher-release-${each.key}-boundary"
  description = "Maximum authority for one temporary ShiftBox ${each.key} role."
  tags        = local.tags
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = each.value
  })
}
