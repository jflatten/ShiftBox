locals {
  release_region       = "us-east-2"
  scope                = "${local.release_region}:${var.publisher_account_id}"
  ec2                  = "arn:aws:ec2:${local.scope}"
  name_scope           = "shiftbox-publisher-rel-??????????????"
  expiry_function      = "arn:aws:lambda:${local.scope}:function:${local.name_scope}-expiry"
  expiry_group         = "arn:aws:scheduler:${local.scope}:schedule-group/${local.name_scope}-expiry-group"
  expiry_schedules     = "arn:aws:scheduler:${local.scope}:schedule/${local.name_scope}-expiry-group/*"
  expiry_log_group     = "arn:aws:logs:${local.scope}:log-group:/aws/lambda/${local.name_scope}-expiry"
  expiry_role          = "arn:aws:iam::${var.publisher_account_id}:role/${local.name_scope}-expiry"
  expiry_role_invoke   = "${local.expiry_role}-invoke"
  expiry_role_arns     = [local.expiry_role, local.expiry_role_invoke]
  owned_network_tags   = { project = "shiftbox", cluster_name = "shiftbox-publisher" }
  release_network_tags = { environment = "release-v*-rc.*", publisher_attempt = "rel-??????????????" }

  region_condition = { StringEquals = { "aws:RequestedRegion" = local.release_region } }
  owned_condition = {
    StringEquals = { for tag, value in local.owned_network_tags : "aws:ResourceTag/${tag}" => value }
    StringLike   = { for tag, value in local.release_network_tags : "aws:ResourceTag/${tag}" => value }
  }
  request_condition = {
    StringEquals = { for tag, value in local.owned_network_tags : "aws:RequestTag/${tag}" => value }
    StringLike   = { for tag, value in local.release_network_tags : "aws:RequestTag/${tag}" => value }
  }
  owned_region = merge(local.owned_condition, {
    StringEquals = merge(local.owned_condition.StringEquals, local.region_condition.StringEquals)
  })
  request_region = merge(local.request_condition, {
    StringEquals = merge(local.request_condition.StringEquals, local.region_condition.StringEquals)
  })

  # EC2 evaluates new resources and any already-existing resources separately.
  # Require release ownership tags in the appropriate context for each ARN.
  network_creates = {
    CreateVpc                     = "vpc"
    CreateSubnet                  = "subnet"
    CreateInternetGateway         = "internet-gateway"
    AllocateAddress               = "elastic-ip"
    CreateNatGateway              = "natgateway"
    CreateRouteTable              = "route-table"
    CreateSecurityGroup           = "security-group"
    AuthorizeSecurityGroupEgress  = "security-group-rule"
    AuthorizeSecurityGroupIngress = "security-group-rule"
    CreateInstanceConnectEndpoint = "instance-connect-endpoint"
    ImportKeyPair                 = "key-pair"
  }
  create_dependencies = {
    vpc          = ["CreateSubnet", "CreateNatGateway", "CreateRouteTable", "CreateSecurityGroup", "AttachInternetGateway"]
    subnet       = ["CreateNatGateway", "CreateInstanceConnectEndpoint", "CreateNetworkInterface", "AssociateRouteTable"]
    "elastic-ip" = ["CreateNatGateway"]
    "security-group" = [
      "CreateInstanceConnectEndpoint", "CreateNetworkInterface", "AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress",
    ]
    "internet-gateway" = ["AttachInternetGateway"]
    "route-table"      = ["CreateRoute", "AssociateRouteTable"]
  }
  network_deletes = {
    vpc                         = ["DeleteVpc", "DetachInternetGateway"]
    subnet                      = ["DeleteSubnet", "DisassociateRouteTable"]
    "internet-gateway"          = ["DeleteInternetGateway", "DetachInternetGateway"]
    "elastic-ip"                = ["ReleaseAddress"]
    natgateway                  = ["DeleteNatGateway"]
    "route-table"               = ["DeleteRouteTable", "DeleteRoute", "DisassociateRouteTable"]
    "security-group"            = ["DeleteSecurityGroup", "RevokeSecurityGroupIngress", "RevokeSecurityGroupEgress"]
    "instance-connect-endpoint" = ["DeleteInstanceConnectEndpoint"]
    "key-pair"                  = ["DeleteKeyPair"]
    "network-interface"         = ["DeleteNetworkInterface"]
    volume                      = ["DeleteVolume"]
  }

  network_inventory = [
    {
      Sid    = "ReadOhioBuildInventory"
      Effect = "Allow"
      Action = [
        "ec2:DescribeAddresses", "ec2:DescribeAddressesAttribute", "ec2:DescribeAvailabilityZones", "ec2:DescribeImages",
        "ec2:DescribeInstanceConnectEndpoints", "ec2:DescribeInstances", "ec2:DescribeInstanceTypeOfferings",
        "ec2:DescribeInternetGateways", "ec2:DescribeKeyPairs", "ec2:DescribeNatGateways",
        "ec2:DescribeNetworkAcls", "ec2:DescribeNetworkInterfaces", "ec2:DescribeRouteTables",
        "ec2:DescribeSecurityGroups", "ec2:DescribeSecurityGroupRules", "ec2:DescribeSnapshots",
        "ec2:DescribeSubnets", "ec2:DescribeTags", "ec2:DescribeVolumes", "ec2:DescribeVpcs",
        "logs:DescribeLogGroups",
      ]
      Resource  = "*"
      Condition = local.region_condition
    },
    {
      Sid       = "ReadBuildVpcAttributes"
      Effect    = "Allow"
      Action    = ["ec2:DescribeVpcAttribute"]
      Resource  = "${local.ec2}:vpc/*"
      Condition = local.owned_region
    },
    {
      Sid    = "ReadExpiryRoleState"
      Effect = "Allow"
      Action = [
        "iam:GetRole", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies", "iam:ListInstanceProfilesForRole",
        "iam:ListRolePolicies", "iam:ListRoleTags",
      ]
      Resource = local.expiry_role_arns
    },
    {
      Sid    = "ReadExpiryFunctionState"
      Effect = "Allow"
      # The pinned provider reads the latest version and ZIP code signing
      # configuration even though this function publishes no versions.
      Action = [
        "lambda:GetFunction", "lambda:GetFunctionCodeSigningConfig", "lambda:GetFunctionConfiguration",
        "lambda:GetFunctionEventInvokeConfig", "lambda:ListTags", "lambda:ListVersionsByFunction",
      ]
      Resource  = local.expiry_function
      Condition = local.region_condition
    },
    {
      Sid       = "ReadExpiryLogGroupState"
      Effect    = "Allow"
      Action    = ["logs:ListTagsForResource", "logs:ListTagsLogGroup"]
      Resource  = [local.expiry_log_group, "${local.expiry_log_group}:*"]
      Condition = local.region_condition
    },
    {
      Sid       = "ReadExpiryScheduleGroupState"
      Effect    = "Allow"
      Action    = ["scheduler:GetScheduleGroup", "scheduler:ListTagsForResource"]
      Resource  = local.expiry_group
      Condition = local.region_condition
    },
    {
      Sid       = "ReadNamedExpirySchedules"
      Effect    = "Allow"
      Action    = ["scheduler:GetSchedule"]
      Resource  = local.expiry_schedules
      Condition = local.region_condition
    },
  ]

  network_retire = concat(
    [{
      Effect    = "Allow"
      Action    = [for action in distinct(flatten(values(local.network_deletes))) : "ec2:${action}"]
      Resource  = [for kind in keys(local.network_deletes) : "${local.ec2}:${kind}/*"]
      Condition = local.owned_region
    }],
    [
      {
        Sid       = "TerminateOnlyOwnedBuilders"
        Effect    = "Allow"
        Action    = ["ec2:TerminateInstances"]
        Resource  = "${local.ec2}:instance/*"
        Condition = merge(local.owned_region, { StringEquals = merge(local.owned_region.StringEquals, { "ec2:InstanceType" = "t3.medium" }) })
      },
      {
        Sid       = "RemoveOnlyBoundedExpiryRoles"
        Effect    = "Allow"
        Action    = ["iam:DeleteRole", "iam:DeleteRolePolicy"]
        Resource  = local.expiry_role_arns
        Condition = merge(local.owned_condition, { StringEquals = merge(local.owned_condition.StringEquals, { "iam:PermissionsBoundary" = local.expiry_boundary_arn }) })
      },
      {
        Sid       = "RemoveExpiryFunctions"
        Effect    = "Allow"
        Action    = ["lambda:DeleteFunction", "lambda:DeleteFunctionEventInvokeConfig"]
        Resource  = local.expiry_function
        Condition = local.owned_region
      },
      {
        Sid       = "RemoveExpiryLogs"
        Effect    = "Allow"
        Action    = ["logs:DeleteLogGroup", "logs:DeleteRetentionPolicy"]
        Resource  = [local.expiry_log_group, "${local.expiry_log_group}:*"]
        Condition = local.owned_region
      },
      {
        Sid       = "RemoveExpiryScheduleGroups"
        Effect    = "Allow"
        Action    = ["scheduler:DeleteScheduleGroup"]
        Resource  = local.expiry_group
        Condition = local.owned_region
      },
      {
        Sid       = "RemoveNamedExpirySchedules"
        Effect    = "Allow"
        Action    = ["scheduler:DeleteSchedule"]
        Resource  = local.expiry_schedules
        Condition = local.region_condition
      },
    ]
  )

  network_provision_ec2_new = [
    for action, kind in local.network_creates : {
      Effect    = "Allow"
      Action    = "ec2:${action}"
      Resource  = "${local.ec2}:${kind}/*"
      Condition = local.request_region
    }
  ]
  network_provision_ec2_existing = concat(
    [for kind, actions in local.create_dependencies : {
      Effect    = "Allow"
      Action    = [for action in actions : "ec2:${action}"]
      Resource  = "${local.ec2}:${kind}/*"
      Condition = local.owned_region
    }],
    [
      {
        Sid      = "PermitEndpointServiceInterface"
        Effect   = "Allow"
        Action   = ["ec2:CreateNetworkInterface"]
        Resource = "${local.ec2}:network-interface/*"
        # EICE creates its own interface without the caller's request tags.
        # EC2 separately requires the tagged subnet grant above.
        Condition = local.region_condition
      },
      {
        Sid      = "TagOnlyOnNetworkCreate"
        Effect   = "Allow"
        Action   = "ec2:CreateTags"
        Resource = [for kind in distinct(values(local.network_creates)) : "${local.ec2}:${kind}/*"]
        Condition = merge(local.request_region, {
          StringEquals = merge(local.request_region.StringEquals, { "ec2:CreateAction" = keys(local.network_creates) })
        })
      },
      {
        Sid      = "TagOnlyNewEndpointInterface"
        Effect   = "Allow"
        Action   = "ec2:CreateTags"
        Resource = "${local.ec2}:network-interface/*"
        # EC2 supplies this service-owned tag on the EICE interface instead of
        # forwarding the endpoint's caller-supplied ownership tags.
        Condition = {
          StringEquals                = merge(local.region_condition.StringEquals, { "ec2:CreateAction" = "CreateNetworkInterface" })
          "ForAllValues:StringEquals" = { "aws:TagKeys" = ["InstanceConnectEndpointId"] }
          StringLike                  = { "aws:RequestTag/InstanceConnectEndpointId" = "eice-*" }
        }
      },
      {
        Sid       = "ConfigureOwnedVpc"
        Effect    = "Allow"
        Action    = ["ec2:ModifyVpcAttribute"]
        Resource  = "${local.ec2}:vpc/*"
        Condition = local.owned_region
      },
      {
        Sid       = "ConfigureOwnedSubnets"
        Effect    = "Allow"
        Action    = ["ec2:ModifySubnetAttribute"]
        Resource  = "${local.ec2}:subnet/*"
        Condition = local.owned_region
      },
    ],
  )

  network_provision_expiry = concat(
    [
      {
        Sid       = "CreateTaggedExpiryFunction"
        Effect    = "Allow"
        Action    = ["lambda:CreateFunction", "lambda:TagResource"]
        Resource  = local.expiry_function
        Condition = local.request_region
      },
      {
        Sid       = "ConfigureExpiryDelivery"
        Effect    = "Allow"
        Action    = ["lambda:PutFunctionEventInvokeConfig"]
        Resource  = local.expiry_function
        Condition = local.owned_region
      },
      {
        Sid       = "CreateTaggedExpiryLogGroup"
        Effect    = "Allow"
        Action    = ["logs:CreateLogGroup", "logs:TagResource", "logs:TagLogGroup"]
        Resource  = [local.expiry_log_group, "${local.expiry_log_group}:*"]
        Condition = local.request_region
      },
      {
        Sid      = "LimitExpiryLogRetention"
        Effect   = "Allow"
        Action   = ["logs:PutRetentionPolicy"]
        Resource = [local.expiry_log_group, "${local.expiry_log_group}:*"]
        # The provider sets retention immediately after creation. The reserved
        # attempt log group ARN scopes this call during tag propagation.
        Condition = local.region_condition
      },
      {
        Sid       = "CreateTaggedExpiryScheduleGroup"
        Effect    = "Allow"
        Action    = ["scheduler:CreateScheduleGroup", "scheduler:TagResource"]
        Resource  = local.expiry_group
        Condition = local.request_region
      },
      {
        Sid       = "CreateNamedExpirySchedules"
        Effect    = "Allow"
        Action    = ["scheduler:CreateSchedule"]
        Resource  = local.expiry_schedules
        Condition = local.region_condition
      },
      local.iam_expiry_create,
      local.iam_expiry_tag,
      local.iam_expiry_policy,
    ],
    local.iam_expiry_pass,
  )
}
