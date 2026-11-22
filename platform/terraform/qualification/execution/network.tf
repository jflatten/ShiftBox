locals {
  ec2 = {
    for type in ["vpc", "subnet", "route-table", "internet-gateway", "elastic-ip", "natgateway", "security-group", "security-group-rule", "volume", "instance", "network-interface"] :
    type => format(local.regional, "ec2", "${type}/*")
  }
  network_types = {
    vpc              = ["CreateVpc"]
    subnet           = ["CreateSubnet"]
    route-table      = ["CreateRouteTable"]
    internet-gateway = ["CreateInternetGateway"]
    elastic-ip       = ["AllocateAddress"]
    natgateway       = ["CreateNatGateway"]
    security-group   = ["CreateSecurityGroup"]
  }
  network_changes = {
    vpc              = ["ModifyVpcAttribute", "DeleteVpc", "AttachInternetGateway", "DetachInternetGateway"]
    subnet           = ["ModifySubnetAttribute", "DeleteSubnet", "AssociateRouteTable", "ReplaceRouteTableAssociation", "DisassociateRouteTable"]
    route-table      = ["DeleteRouteTable", "AssociateRouteTable", "ReplaceRouteTableAssociation", "DisassociateRouteTable", "CreateRoute", "ReplaceRoute", "DeleteRoute"]
    internet-gateway = ["DeleteInternetGateway", "AttachInternetGateway", "DetachInternetGateway"]
    elastic-ip       = ["ReleaseAddress"]
    natgateway       = ["DeleteNatGateway"]
    security-group   = ["DeleteSecurityGroup", "AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress", "RevokeSecurityGroupIngress", "RevokeSecurityGroupEgress"]
  }
  lifecycle_statements = concat(
    [
      for type, actions in local.network_types : {
        Effect    = "Allow"
        Action    = [for action in actions : "ec2:${action}"]
        Resource  = [local.ec2[type]]
        Condition = local.tagged_create
      }
    ],
    [
      for type, actions in local.network_changes : {
        Effect    = "Allow"
        Action    = [for action in actions : "ec2:${action}"]
        Resource  = [local.ec2[type]]
        Condition = local.tagged_run
      }
    ],
    [
      {
        Effect    = "Allow"
        Action    = ["ec2:CreateSubnet", "ec2:CreateRouteTable", "ec2:CreateSecurityGroup"]
        Resource  = [local.ec2.vpc]
        Condition = local.tagged_run
      },
      {
        Effect    = "Allow"
        Action    = ["ec2:CreateNatGateway"]
        Resource  = [local.ec2.subnet, local.ec2.elastic-ip]
        Condition = local.tagged_run
      },
      {
        Effect   = "Allow"
        Action   = ["ec2:CreateTags"]
        Resource = [for type in keys(local.network_types) : local.ec2[type]]
        Condition = {
          StringEquals = merge(local.region_check, local.request_tags, {
            "ec2:CreateAction" = flatten(values(local.network_types))
          })
        }
      },
      {
        Effect = "Allow"
        Action = [
          "ec2:DescribeAddresses", "ec2:DescribeAvailabilityZones", "ec2:DescribeImages", "ec2:DescribeInternetGateways",
          "ec2:DescribeNatGateways", "ec2:DescribeNetworkInterfaces", "ec2:DescribeRouteTables", "ec2:DescribeSecurityGroups",
          "ec2:DescribeSecurityGroupRules", "ec2:DescribeSubnets", "ec2:DescribeTags", "ec2:DescribeVpcAttribute", "ec2:DescribeVpcs"
        ]
        Resource  = ["*"]
        Condition = local.regional_reads
      }
    ]
  )
  instance_statements = concat(
    [
      for boot in [
        { type = "t3.medium", profile = local.instance_profiles[0] },
        { type = "t4g.micro", profile = local.instance_profiles[1] }
        ] : {
        Effect   = "Allow"
        Action   = ["ec2:RunInstances"]
        Resource = [local.ec2.instance]
        Condition = {
          StringEquals = merge(local.region_check, local.request_tags, {
            "ec2:InstanceType"       = boot.type
            "ec2:MetadataHttpTokens" = "required"
          })
          ArnEquals     = { "ec2:InstanceProfile" = boot.profile }
          NumericEquals = { "ec2:MetadataHttpPutResponseHopLimit" = 1 }
        }
      }
    ],
    [
      {
        Effect    = "Allow"
        Action    = ["ec2:RunInstances"]
        Resource  = [for image in [var.host_ami_id, var.router_ami_id] : "arn:aws:ec2:${local.region}::image/${image}"]
        Condition = local.regional_reads
      },
      {
        Effect    = "Allow"
        Action    = ["ec2:RunInstances"]
        Resource  = [local.ec2.subnet, local.ec2.security-group]
        Condition = local.tagged_run
      },
      {
        Effect   = "Allow"
        Action   = ["ec2:RunInstances"]
        Resource = [local.ec2.network-interface]
        Condition = {
          StringEquals = local.region_check
          Bool         = { "ec2:AssociatePublicIpAddress" = "false" }
          ArnLike      = { "ec2:Subnet" = local.ec2.subnet }
        }
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
      },
      {
        Effect    = "Allow"
        Action    = ["ssm:GetParameter", "ssm:GetParameters"]
        Resource  = ["arn:aws:ssm:${local.region}::parameter/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"]
        Condition = local.regional_reads
      }
    ]
  )
}
