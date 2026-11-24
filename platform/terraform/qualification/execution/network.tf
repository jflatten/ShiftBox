locals {
  ec2 = {
    for type in ["security-group", "volume", "instance", "network-interface"] :
    type => format(local.regional, "ec2", "${type}/*")
  }
  service_subnet_arn = var.verified_service_subnet_id == null ? null : format(local.regional, "ec2", "subnet/${var.verified_service_subnet_id}")
  lifecycle_statements = concat(
    [
      {
        Effect    = "Allow"
        Action    = ["ec2:CreateSecurityGroup"]
        Resource  = [local.ec2["security-group"]]
        Condition = local.tagged_create
      },
      {
        Effect    = "Allow"
        Action    = ["ec2:CreateTags"]
        Resource  = [local.ec2["security-group"]]
        Condition = { StringEquals = merge(local.region_check, local.request_tags, { "ec2:CreateAction" = "CreateSecurityGroup" }) }
      },
      {
        Effect = "Allow"
        Action = [
          "ec2:DeleteSecurityGroup", "ec2:AuthorizeSecurityGroupIngress", "ec2:AuthorizeSecurityGroupEgress",
          "ec2:RevokeSecurityGroupIngress", "ec2:RevokeSecurityGroupEgress"
        ]
        Resource  = [local.ec2["security-group"]]
        Condition = local.tagged_run
      },
      {
        Effect = "Allow"
        Action = [
          "ec2:DescribeAvailabilityZones", "ec2:DescribeImages", "ec2:DescribeNetworkInterfaces",
          "ec2:DescribeSecurityGroups", "ec2:DescribeSecurityGroupRules", "ec2:DescribeSubnets", "ec2:DescribeTags",
          "ec2:DescribeVpcAttribute", "ec2:DescribeVpcs"
        ]
        Resource  = ["*"]
        Condition = local.regional_reads
      }
    ],
    var.verified_vpc_id == null ? [] : [{
      Effect    = "Allow"
      Action    = ["ec2:CreateSecurityGroup"]
      Resource  = [format(local.regional, "ec2", "vpc/${var.verified_vpc_id}")]
      Condition = local.regional_reads
    }]
  )
  instance_statements = concat([
    {
      Effect   = "Allow"
      Action   = ["ec2:RunInstances"]
      Resource = [local.ec2.instance]
      Condition = {
        StringEquals = merge(local.region_check, local.request_tags, {
          "ec2:InstanceType"       = "t3.medium"
          "ec2:MetadataHttpTokens" = "required"
        })
        ArnEquals     = { "ec2:InstanceProfile" = local.host_profile }
        NumericEquals = { "ec2:MetadataHttpPutResponseHopLimit" = 1 }
      }
    },
    {
      Effect    = "Allow"
      Action    = ["ec2:RunInstances"]
      Resource  = ["arn:aws:ec2:${local.region}::image/${var.host_ami_id}"]
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["ec2:RunInstances"]
      Resource  = [local.ec2["security-group"]]
      Condition = local.tagged_run
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:RunInstances", "ec2:CreateVolume"]
      Resource = [local.ec2.volume]
      Condition = {
        StringEquals          = merge(local.region_check, local.request_tags, { "ec2:VolumeType" = "gp3" })
        Bool                  = { "ec2:Encrypted" = "true" }
        NumericLessThanEquals = { "ec2:VolumeSize" = 32 }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:CreateTags"]
      Resource = [local.ec2.instance, local.ec2.volume]
      Condition = {
        StringEquals = merge(local.region_check, local.request_tags, {
          "ec2:CreateAction" = ["CreateVolume", "RunInstances"]
        })
      }
    },
    {
      Effect    = "Allow"
      Action    = ["ec2:AttachVolume", "ec2:DetachVolume"]
      Resource  = [local.ec2.instance, local.ec2.volume]
      Condition = local.tagged_run
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:DeleteVolume"]
      Resource = [local.ec2.volume]
      Condition = {
        StringEquals = merge(local.region_check, local.resource_tags, {
          "aws:ResourceTag/shiftbox_qualification_run" = local.run
          "aws:ResourceTag/shiftbox-volume-purpose"    = "oci-image-cache"
        })
      }
    },
    {
      Effect    = "Allow"
      Action    = ["ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:ModifyInstanceAttribute"]
      Resource  = [local.ec2.instance]
      Condition = local.tagged_run
    },
    {
      Effect    = "Allow"
      Action    = ["ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:DescribeInstanceAttribute", "ec2:DescribeVolumes", "ec2:DescribeVolumeAttribute", "ec2:DescribeInstanceCreditSpecifications", "ec2:DescribeAccountAttributes"]
      Resource  = ["*"]
      Condition = local.regional_reads
    }
    ], var.verified_service_subnet_id == null ? [] : [
    {
      Effect    = "Allow"
      Action    = ["ec2:RunInstances"]
      Resource  = [local.service_subnet_arn]
      Condition = local.regional_reads
    },
    {
      Effect   = "Allow"
      Action   = ["ec2:RunInstances"]
      Resource = [local.ec2["network-interface"]]
      Condition = {
        StringEquals = local.region_check
        Bool         = { "ec2:AssociatePublicIpAddress" = "false" }
        ArnEquals    = { "ec2:Subnet" = local.service_subnet_arn }
      }
    }
  ])
}
