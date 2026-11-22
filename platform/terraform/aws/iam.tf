resource "aws_iam_role" "ecs_host" {
  name = "${var.name}-ecs-host"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ecs_host" {
  role       = aws_iam_role.ecs_host.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role"
}

resource "aws_iam_instance_profile" "ecs_host" {
  name = aws_iam_role.ecs_host.name
  role = aws_iam_role.ecs_host.name
}

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition
  ec2_arn    = "arn:${local.partition}:ec2:${var.aws_region}:${local.account_id}"
  workspace_resources = [
    "${local.ec2_arn}:instance/*",
    "${local.ec2_arn}:volume/*",
    "${local.ec2_arn}:network-interface/*",
  ]
  workspace_tag = "ShiftBoxWorkspaceDeployment"
  workspace_enrollment_conditions = {
    StringEquals = {
      "aws:ResourceTag/${local.workspace_tag}" = var.name
      "ec2:Attribute"                          = "UserData"
    }
  }
  task_trust = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = local.account_id }
        ArnLike      = { "aws:SourceArn" = "arn:${local.partition}:ecs:${var.aws_region}:${local.account_id}:*" }
      }
    }]
  })
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-ecs-execution"
  assume_role_policy = local.task_trust
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "execution_secrets" {
  count = local.services_enabled ? 1 : 0
  name  = "read-postgres-task-secrets"
  role  = aws_iam_role.execution.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "secretsmanager:GetSecretValue"
      Resource = [var.postgres_credentials_arn, var.postgres_tls_arn]
    }]
  })
}

# Keep the GitHub secret beyond the execution role passed to protected setup.
resource "aws_iam_role" "controller_execution" {
  count              = local.github_app_enabled ? 1 : 0
  name               = "${var.name}-controller-execution"
  assume_role_policy = local.task_trust
}

resource "aws_iam_role_policy_attachment" "controller_execution" {
  count      = local.github_app_enabled ? 1 : 0
  role       = one(aws_iam_role.controller_execution).name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "controller_execution_secrets" {
  count = local.github_app_enabled ? 1 : 0
  name  = "read-controller-task-secrets"
  role  = one(aws_iam_role.controller_execution).name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "secretsmanager:GetSecretValue"
      Resource = [var.postgres_credentials_arn, var.github_app_credentials_arn]
    }]
  })
}

resource "aws_iam_role" "controller" {
  name               = "${var.name}-controller"
  assume_role_policy = local.task_trust
}

resource "aws_iam_role_policy" "controller" {
  name = "workspace-ec2"
  role = aws_iam_role.controller.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ObserveRegionalResources"
        Effect = "Allow"
        Action = [
          "ec2:DescribeImages", "ec2:DescribeInstances", "ec2:DescribeInstanceTypes",
          "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeNetworkInterfaces",
          "ec2:DescribeSecurityGroups", "ec2:DescribeSubnets", "ec2:DescribeVolumes", "ec2:DescribeVpcs",
        ]
        Resource  = "*"
        Condition = { StringEquals = { "aws:RequestedRegion" = var.aws_region } }
      },
      {
        Sid       = "LaunchFromAccountImages"
        Effect    = "Allow"
        Action    = "ec2:RunInstances"
        Resource  = "arn:${local.partition}:ec2:${var.aws_region}::image/*"
        Condition = { StringEquals = { "ec2:Owner" = local.account_id } }
      },
      {
        Sid      = "LaunchInWorkspaceNetwork"
        Effect   = "Allow"
        Action   = "ec2:RunInstances"
        Resource = [aws_subnet.workspaces.arn, aws_security_group.workspace.arn]
      },
      {
        Sid       = "LaunchOwnedInstances"
        Effect    = "Allow"
        Action    = "ec2:RunInstances"
        Resource  = "${local.ec2_arn}:instance/*"
        Condition = { StringEquals = { "aws:RequestTag/${local.workspace_tag}" = var.name } }
      },
      {
        Sid      = "LaunchOwnedEncryptedVolumes"
        Effect   = "Allow"
        Action   = "ec2:RunInstances"
        Resource = "${local.ec2_arn}:volume/*"
        Condition = {
          StringEquals = { "aws:RequestTag/${local.workspace_tag}" = var.name }
          Bool         = { "ec2:Encrypted" = "true" }
        }
      },
      {
        Sid      = "LaunchOwnedPrivateInterfaces"
        Effect   = "Allow"
        Action   = "ec2:RunInstances"
        Resource = "${local.ec2_arn}:network-interface/*"
        Condition = {
          StringEquals = { "aws:RequestTag/${local.workspace_tag}" = var.name }
          BoolIfExists = { "ec2:AssociatePublicIpAddress" = "false" }
        }
      },
      {
        Sid      = "TagOnlyAtLaunch"
        Effect   = "Allow"
        Action   = "ec2:CreateTags"
        Resource = local.workspace_resources
        Condition = {
          StringEquals = {
            "ec2:CreateAction"                      = "RunInstances"
            "aws:RequestTag/${local.workspace_tag}" = var.name
          }
        }
      },
      {
        Sid       = "ManageOwnedInstances"
        Effect    = "Allow"
        Action    = ["ec2:StartInstances", "ec2:StopInstances", "ec2:RebootInstances", "ec2:TerminateInstances", "ec2:DescribeInstanceAttribute"]
        Resource  = "${local.ec2_arn}:instance/*"
        Condition = { StringEquals = { "aws:ResourceTag/${local.workspace_tag}" = var.name } }
      },
      {
        Sid       = "DeliverEnrollment"
        Effect    = "Allow"
        Action    = "ec2:ModifyInstanceAttribute"
        Resource  = "${local.ec2_arn}:instance/*"
        Condition = local.workspace_enrollment_conditions
      },
      {
        Sid       = "RemoveOwnedVolumes"
        Effect    = "Allow"
        Action    = "ec2:DeleteVolume"
        Resource  = "${local.ec2_arn}:volume/*"
        Condition = { StringEquals = { "aws:ResourceTag/${local.workspace_tag}" = var.name } }
      },
      {
        Sid       = "RemoveOwnedInterfaces"
        Effect    = "Allow"
        Action    = "ec2:DeleteNetworkInterface"
        Resource  = "${local.ec2_arn}:network-interface/*"
        Condition = { StringEquals = { "aws:ResourceTag/${local.workspace_tag}" = var.name } }
      },
    ]
  })
}

resource "aws_iam_role" "setup" {
  name               = "${var.name}-setup"
  assume_role_policy = local.task_trust
}

resource "aws_iam_role_policy" "setup" {
  name = "setup-session-channels"
  role = aws_iam_role.setup.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ssmmessages:CreateControlChannel", "ssmmessages:OpenControlChannel",
        "ssmmessages:CreateDataChannel", "ssmmessages:OpenDataChannel",
      ]
      Resource = "*"
    }]
  })
}
