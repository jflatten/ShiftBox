resource "aws_ecs_task_definition" "setup" {
  count                    = local.services_enabled ? 1 : 0
  family                   = "${var.name}-setup"
  requires_compatibilities = ["EC2"]
  network_mode             = "awsvpc"
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.setup.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  volume {
    name      = "installation"
    host_path = "/var/lib/shiftbox-data/shiftbox"
  }

  container_definitions = jsonencode([{
    name              = "setup"
    image             = local.controller_image
    essential         = true
    memoryReservation = 512
    entryPoint        = ["/usr/local/bin/shiftbox-cloud"]
    command           = ["setup"]
    linuxParameters   = { initProcessEnabled = true }
    mountPoints       = [{ sourceVolume = "installation", containerPath = "/var/lib/shiftbox" }]
    environment       = local.controller_environment
    secrets = concat(local.app_db_secret, [
      { name = "SHIFTBOX_DEX_PASSWORD", valueFrom = "${var.postgres_credentials_arn}:dex_password::" },
    ])
    logConfiguration = local.task_logs
  }])
  tags       = { ShiftBoxBootstrap = var.name }
  depends_on = [terraform_data.service_inputs]
}

resource "aws_ssm_document" "setup" {
  count           = local.services_enabled ? 1 : 0
  name            = "${var.name}-SetupPort"
  document_type   = "Session"
  document_format = "JSON"
  content = jsonencode({
    schemaVersion = "1.0"
    description   = "Connect only to the protected ShiftBox setup listener"
    sessionType   = "Port"
    properties = {
      portNumber      = tostring(local.setup_port)
      localPortNumber = tostring(local.setup_port)
      type            = "LocalPortForwarding"
    }
  })
}

output "setup" {
  description = "Nonsecret inputs for the protected one-off setup task and private port forward."
  value = local.services_enabled ? {
    cluster         = aws_ecs_cluster.shiftbox.name
    region          = var.aws_region
    task_definition = aws_ecs_task_definition.setup[0].arn
    subnet          = aws_subnet.services.id
    security_group  = aws_security_group.task["setup"].id
    document        = aws_ssm_document.setup[0].name
    port            = local.setup_port
    log_group       = aws_cloudwatch_log_group.services.name
    tag             = var.name
  } : null
}

resource "aws_iam_policy" "setup_operator" {
  count       = local.services_enabled ? 1 : 0
  name        = "${var.name}-setup-operator"
  description = "Allow a deployer to start the one-off setup task and forward its fixed local HTTPS port"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect    = "Allow"
        Action    = "ecs:RunTask"
        Resource  = "${aws_ecs_task_definition.setup[0].arn_without_revision}:*"
        Condition = { ArnEquals = { "ecs:cluster" = aws_ecs_cluster.shiftbox.arn } }
      },
      {
        Effect    = "Allow"
        Action    = "ecs:TagResource"
        Resource  = "arn:${local.partition}:ecs:${var.aws_region}:${local.account_id}:task/${aws_ecs_cluster.shiftbox.name}/*"
        Condition = { StringEquals = { "ecs:CreateAction" = "RunTask", "aws:RequestTag/ShiftBoxBootstrap" = var.name } }
      },
      {
        Effect    = "Allow"
        Action    = "iam:PassRole"
        Resource  = [aws_iam_role.execution.arn, aws_iam_role.setup.arn]
        Condition = { StringEquals = { "iam:PassedToService" = "ecs-tasks.amazonaws.com" } }
      },
      {
        Effect   = "Allow"
        Action   = ["ecs:DescribeTasks", "ecs:StopTask", "ssm:StartSession"]
        Resource = "arn:${local.partition}:ecs:${var.aws_region}:${local.account_id}:task/${aws_ecs_cluster.shiftbox.name}/*"
        Condition = {
          StringEquals = { "aws:ResourceTag/ShiftBoxBootstrap" = var.name }
          BoolIfExists = { "ssm:SessionDocumentAccessCheck" = "true" }
        }
      },
      {
        Effect   = "Allow"
        Action   = ["ssm:StartSession", "ssm:GetDocument"]
        Resource = aws_ssm_document.setup[0].arn
      },
      {
        Effect   = "Allow"
        Action   = "ecs:DescribeServices"
        Resource = [aws_ecs_service.controller[0].arn, aws_ecs_service.postgres[0].arn]
      },
      {
        Effect   = "Allow"
        Action   = "logs:DescribeLogStreams"
        Resource = "${aws_cloudwatch_log_group.services.arn}:*"
      },
      {
        Effect   = "Allow"
        Action   = "logs:GetLogEvents"
        Resource = "${aws_cloudwatch_log_group.services.arn}:log-stream:ecs/setup/*"
      }
    ]
  })
}

output "setup_operator_policy_arn" {
  description = "Attach to the deployer identity authorized to run protected setup."
  value       = local.services_enabled ? aws_iam_policy.setup_operator[0].arn : null
}
