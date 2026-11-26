locals {
  build_instance = "${local.owned}:instance/*"
  build_volume   = "${local.owned}:volume/*"
  build_eni      = "${local.owned}:network-interface/*"
  build_image    = "${local.ec2}::image/*"
  build_snapshot = "${local.ec2}::snapshot/*"
  build_key      = "${local.owned}:key-pair/sbtest-main-*"

  build_grants = {
    ReadBuilderInventory = {
      Action = ["ec2:DescribeAvailabilityZones", "ec2:DescribeImageAttribute", "ec2:DescribeImages",
        "ec2:DescribeInstanceConnectEndpoints", "ec2:DescribeInstances", "ec2:DescribeInstanceStatus",
        "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeKeyPairs", "ec2:DescribeRegions",
      "ec2:DescribeSecurityGroups", "ec2:DescribeSnapshots", "ec2:DescribeSubnets", "ec2:DescribeVolumes", "ec2:DescribeVpcs"]
      Resource  = "*"
      Condition = {}
    }
    OpenPrivateBuilderTunnel = {
      Action   = "ec2-instance-connect:OpenTunnel"
      Resource = "${local.owned}:instance-connect-endpoint/${var.build_network.endpoint_id}"
      Condition = {
        StringEquals          = local.resource_tags
        IpAddress             = { "ec2-instance-connect:privateIpAddress" = var.build_network.private_subnet_cidr }
        NumericEquals         = { "ec2-instance-connect:remotePort" = 22 }
        NumericLessThanEquals = { "ec2-instance-connect:maxTunnelDuration" = 2700 }
      }
    }
    ImportDisposableKey = {
      Action    = "ec2:ImportKeyPair"
      Resource  = local.build_key
      Condition = { StringEquals = local.request_tags }
    }
    DeleteDisposableKey = {
      Action    = "ec2:DeleteKeyPair"
      Resource  = local.build_key
      Condition = { StringEquals = local.resource_tags }
    }
    LaunchFromReviewedInputs = {
      Action = "ec2:RunInstances"
      Resource = ["${local.ec2}::image/${var.build_network.source_ami}", "${local.ec2}::snapshot/${var.build_network.source_snapshot}",
      local.subnets, "${local.owned}:security-group/${var.build_network.builder_group_id}", local.build_key]
      Condition = {}
    }
    LaunchTaggedBuilder = {
      Action   = "ec2:RunInstances"
      Resource = local.build_instance
      Condition = {
        StringEquals = merge(local.request_tags, {
          "ec2:InstanceType" = "t3.medium", "ec2:InstanceMarketType" = "on-demand", "ec2:MetadataHttpTokens" = "required"
        })
      }
    }
    LaunchPrivateInterface = {
      Action   = "ec2:RunInstances"
      Resource = local.build_eni
      Condition = {
        StringEquals = local.request_tags
        ArnEquals    = { "ec2:Subnet" = local.subnets, "ec2:Vpc" = "${local.owned}:vpc/${var.build_network.vpc_id}" }
        Bool         = { "ec2:AssociatePublicIpAddress" = "false" }
      }
    }
    LaunchEncryptedRoot = {
      Action   = "ec2:RunInstances"
      Resource = local.build_volume
      Condition = {
        StringEquals  = merge(local.request_tags, { "ec2:VolumeType" = "gp3" })
        NumericEquals = { "ec2:VolumeSize" = 32 }
        Bool          = { "ec2:Encrypted" = "true" }
      }
    }
    ImageAndStopOwnedBuilder = {
      Action    = ["ec2:CreateImage", "ec2:StopInstances", "ec2:TerminateInstances"]
      Resource  = local.build_instance
      Condition = { StringEquals = local.resource_tags }
    }
    CreateTaggedPrivateImage = {
      Action    = "ec2:CreateImage"
      Resource  = [local.build_image, local.build_snapshot]
      Condition = { StringEquals = local.request_tags }
    }
    TagBuildResourcesOnCreation = {
      Action    = "ec2:CreateTags"
      Resource  = [local.build_instance, local.build_volume, local.build_eni, local.build_image, local.build_snapshot, local.build_key]
      Condition = { StringEquals = merge(local.request_tags, { "ec2:CreateAction" = ["RunInstances", "CreateImage", "ImportKeyPair"] }) }
    }
    StampOwnedImageProvenance = {
      Action   = "ec2:CreateTags"
      Resource = [local.build_image, local.build_snapshot]
      Condition = {
        StringEquals = merge(local.resource_tags, local.request_tags, { "ec2:Owner" = local.account })
        "ForAllValues:StringEquals" = { "aws:TagKeys" = concat(keys(local.tags), [
          "shiftbox-environment", "ShiftBoxArtifact", "ShiftBoxRelease", "ShiftBoxSourceAMI", "ShiftBoxSourceCommit"
        ]) }
      }
    }
    SetPrivateImageIMDSv2 = {
      Action   = "ec2:ModifyImageAttribute"
      Resource = local.build_image
      Condition = {
        StringEquals = merge(local.resource_tags, { "ec2:Owner" = local.account, "ec2:Attribute/ImdsSupport" = "v2.0" })
        Bool         = { "ec2:Public" = "false" }
      }
    }
    SetPrivateImageDescription = {
      Action   = "ec2:ModifyImageAttribute"
      Resource = local.build_image
      Condition = {
        StringEquals = merge(local.resource_tags, { "ec2:Owner" = local.account })
        StringLike   = { "ec2:Attribute/Description" = "ShiftBox workspace platform v0.0.0-rc.* on Ubuntu 24.04 Minimal amd64" }
        Bool         = { "ec2:Public" = "false" }
      }
    }
  }
}

resource "aws_iam_role_policy" "builder" {
  name = "BuildEncryptedOhioTestImage"
  role = aws_iam_role.actions["deploy"].name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [for name, grant in local.build_grants : merge(grant, {
      Sid    = name
      Effect = "Allow"
      Condition = merge(grant.Condition, {
        StringEquals = merge(try(grant.Condition.StringEquals, {}), { "aws:RequestedRegion" = local.region })
      })
    })]
  })
}
