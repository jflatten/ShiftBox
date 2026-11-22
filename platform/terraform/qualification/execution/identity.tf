locals {
  role_prefix   = "arn:aws:iam::${local.account}:role/shiftbox-qualification/${local.run}"
  policy_prefix = "arn:aws:iam::${local.account}:policy/shiftbox-qualification-boundaries/${local.run}"
  host_roles    = ["HostRole", "RouterRole"]
  task_roles    = ["PostgresExecutionRole", "SetupExecutionRole", "ControllerExecutionRole", "SetupTaskRole", "ControllerTaskRole"]
  roles         = concat(local.host_roles, local.task_roles)
  role_arns     = { for role in local.roles : role => "${local.role_prefix}/${local.run}-${role}" }
  boundary_arns = { for role in local.roles : role => "${local.policy_prefix}-${role}" }
  managed_attachments = {
    HostRole   = "arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role"
    RouterRole = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
  }
  instance_profiles = [
    for name in ["HostProfile", "RouterProfile"] :
    "arn:aws:iam::${local.account}:instance-profile/shiftbox-qualification/${local.run}/${local.run}-${name}"
  ]
  identity_statements = concat(
    [
      for role in local.roles : {
        Effect   = "Allow"
        Action   = ["iam:CreateRole"]
        Resource = [local.role_arns[role]]
        Condition = {
          StringEquals = merge(local.request_tags, { "iam:PermissionsBoundary" = local.boundary_arns[role] })
        }
      }
    ],
    [
      for role in local.task_roles : {
        Effect   = "Allow"
        Action   = ["iam:PutRolePolicy"]
        Resource = [local.role_arns[role]]
        Condition = {
          StringEquals = merge(local.resource_tags, { "iam:PermissionsBoundary" = local.boundary_arns[role] })
        }
      }
    ],
    [
      for role, arn in local.managed_attachments : {
        Effect   = "Allow"
        Action   = ["iam:AttachRolePolicy", "iam:DetachRolePolicy"]
        Resource = [local.role_arns[role]]
        Condition = {
          StringEquals = merge(local.resource_tags, { "iam:PermissionsBoundary" = local.boundary_arns[role] })
          ArnEquals    = { "iam:PolicyARN" = arn }
        }
      }
    ]
  )
  identity_lifecycle_statements = [
    {
      Effect   = "Allow"
      Action   = ["iam:TagRole"]
      Resource = values(local.role_arns)
      Condition = {
        StringEquals = local.request_tags
      }
    },
    {
      Effect   = "Allow"
      Action   = ["iam:GetRole", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies", "iam:ListRolePolicies", "iam:ListRoleTags"]
      Resource = values(local.role_arns)
    },
    {
      Effect    = "Allow"
      Action    = ["iam:DeleteRole", "iam:DeleteRolePolicy"]
      Resource  = values(local.role_arns)
      Condition = { StringEquals = local.resource_tags }
    },
    {
      Effect   = "Allow"
      Action   = ["iam:CreateInstanceProfile", "iam:GetInstanceProfile", "iam:DeleteInstanceProfile", "iam:RemoveRoleFromInstanceProfile", "iam:AddRoleToInstanceProfile"]
      Resource = local.instance_profiles
    },
    {
      Effect   = "Allow"
      Action   = ["iam:PassRole"]
      Resource = [for role in local.host_roles : local.role_arns[role]]
      Condition = {
        StringEquals = { "iam:PassedToService" = "ec2.amazonaws.com" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["iam:PassRole"]
      Resource = [for role in local.task_roles : local.role_arns[role]]
      Condition = {
        StringEquals = { "iam:PassedToService" = "ecs-tasks.amazonaws.com" }
      }
    }
  ]
}
