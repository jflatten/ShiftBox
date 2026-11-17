variable "verified_enrollment_ami_id" {
  type        = string
  default     = null
  description = "Exact AMI whose stopped-instance enrollment confidentiality and recovery have been verified."
  validation {
    condition     = var.verified_enrollment_ami_id == null || can(regex("^ami-([a-f0-9]{8}|[a-f0-9]{17})$", var.verified_enrollment_ami_id))
    error_message = "The verified enrollment image must be an AMI ID."
  }
}

variable "controller_image_digest" {
  description = "Digest of the published controller image in this stack's ECR repository. Omit while provisioning the base stack."
  type        = string
  default     = null
  validation {
    condition     = var.controller_image_digest == null || can(regex("^sha256:[a-f0-9]{64}$", var.controller_image_digest))
    error_message = "Use the published controller SHA-256 image digest."
  }
}

variable "postgres_image" {
  description = "Pin of the official PostgreSQL 17 Debian-based image, including its registry digest."
  type        = string
  default     = null
  validation {
    condition     = var.postgres_image == null || can(regex("@sha256:[a-f0-9]{64}$", var.postgres_image))
    error_message = "Pin the PostgreSQL image to its SHA-256 digest."
  }
}

variable "postgres_credentials_arn" {
  description = "Existing Secrets Manager JSON secret with admin_password, app_password, and dex_password."
  type        = string
  default     = null
}

variable "postgres_tls_arn" {
  description = "Existing Secrets Manager JSON secret with certificate and private_key for the private PostgreSQL DNS name."
  type        = string
  default     = null
}

variable "postgres_ca_pem" {
  description = "Public CA PEM used to verify PostgreSQL TLS."
  type        = string
  default     = null
}

variable "controller_enabled" {
  description = "Start the normal controller after the deployer completes and stops protected setup."
  type        = bool
  default     = false
}

variable "github_app_credentials_arn" {
  description = "Optional Secrets Manager ARN for GitHub App credentials used by the controller."
  type        = string
  default     = null
}

locals {
  services_enabled = var.controller_image_digest != null
  service_groups   = toset(["controller", "postgres", "setup"])
  browser_port     = 8443
  setup_port       = 8444
  guest_ports      = toset(["19443", "19444", "19445", "19446", "19447"])
  controller_image = local.services_enabled ? "${aws_ecr_repository.controller.repository_url}@${var.controller_image_digest}" : null
  task_logs = {
    logDriver = "awslogs"
    options = {
      awslogs-group         = aws_cloudwatch_log_group.services.name
      awslogs-region        = var.aws_region
      awslogs-stream-prefix = "ecs"
    }
  }
  controller_environment = local.services_enabled ? [
    { name = "SHIFTBOX_DB_HOST", value = "postgres.${var.dns_suffix}" },
    { name = "SHIFTBOX_DB_CA", value = var.postgres_ca_pem },
    { name = "SHIFTBOX_CLOUD_CONFIG", value = jsonencode({
      installationDir  = "/var/lib/shiftbox"
      dataProfile      = "postgres"
      managementOrigin = "https://shiftbox.${var.dns_suffix}:${local.browser_port}"
      runtime = {
        provider = "ec2"
        ec2 = {
          accountId             = local.account_id
          region                = var.aws_region
          dataVolumeId          = aws_ebs_volume.data.id
          subnetId              = aws_subnet.workspaces.id
          securityGroupId       = aws_security_group.workspace.id
          deployment            = var.name
          environment           = var.environment
          verifiedEnrollmentAmi = var.verified_enrollment_ami_id
        }
        browserAddress    = ":${local.browser_port}"
        guestHost         = "guest.${var.dns_suffix}"
        enrollmentAddress = ":19443"
        pollAddress       = ":19444"
        tunnelAddress     = ":19445"
      }
    }) },
  ] : []
  app_db_secret      = local.services_enabled ? [{ name = "SHIFTBOX_APP_PASSWORD", valueFrom = "${var.postgres_credentials_arn}:app_password::" }] : []
  github_app_enabled = local.services_enabled && var.github_app_credentials_arn != null
  controller_secrets = concat(local.app_db_secret, local.github_app_enabled ? [{ name = "SHIFTBOX_GITHUB_APP", valueFrom = var.github_app_credentials_arn }] : [])
}

resource "terraform_data" "service_inputs" {
  lifecycle {
    precondition {
      condition = !local.services_enabled || alltrue([
        var.ecs_host_ami_id != null, var.dns_suffix != null, var.postgres_image != null,
        var.postgres_credentials_arn != null, var.postgres_tls_arn != null, var.postgres_ca_pem != null,
      ])
      error_message = "The ECS host AMI, DNS suffix, PostgreSQL image, secret ARNs, and database CA are required to deploy the services."
    }
    precondition {
      condition     = !var.controller_enabled || local.services_enabled
      error_message = "Publish a controller image and complete protected setup before enabling the controller."
    }
  }
}

resource "aws_cloudwatch_log_group" "services" {
  name              = "/shiftbox/${var.name}/ecs"
  retention_in_days = 14
}

resource "aws_security_group" "task" {
  for_each    = local.service_groups
  name        = "${var.name}-${each.key}"
  description = "Private ShiftBox ${each.key} task"
  vpc_id      = aws_vpc.shiftbox.id
}

resource "aws_security_group" "workspace" {
  name        = "${var.name}-workspace"
  description = "Private ShiftBox workspaces with outbound access only"
  vpc_id      = aws_vpc.shiftbox.id
}

resource "aws_vpc_security_group_egress_rule" "task" {
  for_each          = toset(["controller", "setup"])
  security_group_id = aws_security_group.task[each.key].id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "workspace" {
  security_group_id = aws_security_group.workspace.id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_ingress_rule" "browser" {
  for_each          = var.operator_ipv4_cidrs
  security_group_id = aws_security_group.task["controller"].id
  ip_protocol       = "tcp"
  from_port         = local.browser_port
  to_port           = local.browser_port
  cidr_ipv4         = each.value
}

resource "aws_vpc_security_group_ingress_rule" "guest" {
  for_each                     = local.guest_ports
  security_group_id            = aws_security_group.task["controller"].id
  referenced_security_group_id = aws_security_group.workspace.id
  ip_protocol                  = "tcp"
  from_port                    = tonumber(each.key)
  to_port                      = tonumber(each.key)
}

resource "aws_vpc_security_group_ingress_rule" "postgres" {
  for_each                     = toset(["controller", "setup"])
  security_group_id            = aws_security_group.task["postgres"].id
  referenced_security_group_id = aws_security_group.task[each.key].id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_ecs_task_definition" "postgres" {
  count                    = local.services_enabled ? 1 : 0
  family                   = "${var.name}-postgres"
  requires_compatibilities = ["EC2"]
  network_mode             = "awsvpc"
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  volume {
    name      = "data"
    host_path = "/var/lib/shiftbox-data/postgres"
  }

  container_definitions = jsonencode([{
    name              = "postgres"
    image             = var.postgres_image
    essential         = true
    memoryReservation = 512
    entryPoint        = ["/bin/sh", "-ec"]
    command           = [file("${path.module}/postgres/start.sh")]
    mountPoints       = [{ sourceVolume = "data", containerPath = "/var/lib/postgresql/data" }]
    environment = [
      { name = "PGDATA", value = "/var/lib/postgresql/data/pgdata" },
      { name = "POSTGRES_DB", value = "shiftbox" },
    ]
    secrets = [
      { name = "POSTGRES_PASSWORD", valueFrom = "${var.postgres_credentials_arn}:admin_password::" },
      { name = "SHIFTBOX_APP_PASSWORD", valueFrom = "${var.postgres_credentials_arn}:app_password::" },
      { name = "SHIFTBOX_DEX_PASSWORD", valueFrom = "${var.postgres_credentials_arn}:dex_password::" },
      { name = "POSTGRES_TLS_CERT", valueFrom = "${var.postgres_tls_arn}:certificate::" },
      { name = "POSTGRES_TLS_KEY", valueFrom = "${var.postgres_tls_arn}:private_key::" },
    ]
    logConfiguration = local.task_logs
    portMappings     = [{ containerPort = 5432, protocol = "tcp" }]
  }])
  depends_on = [terraform_data.service_inputs]
}

resource "aws_ecs_task_definition" "controller" {
  count                    = local.services_enabled ? 1 : 0
  family                   = "${var.name}-controller"
  requires_compatibilities = ["EC2"]
  network_mode             = "awsvpc"
  execution_role_arn       = local.github_app_enabled ? one(aws_iam_role.controller_execution).arn : aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.controller.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  volume {
    name      = "installation"
    host_path = "/var/lib/shiftbox-data/shiftbox"
  }

  container_definitions = jsonencode([{
    name              = "controller"
    image             = local.controller_image
    essential         = true
    memoryReservation = 512
    entryPoint        = ["/usr/local/bin/shiftbox-cloud"]
    command           = ["serve"]
    mountPoints       = [{ sourceVolume = "installation", containerPath = "/var/lib/shiftbox" }]
    environment       = local.controller_environment
    secrets           = local.controller_secrets
    logConfiguration  = local.task_logs
    portMappings = [for port in concat([local.browser_port], [for guest in local.guest_ports : tonumber(guest)]) :
      { containerPort = port, protocol = "tcp" }
    ]
  }])
  depends_on = [
    terraform_data.service_inputs,
    aws_iam_role_policy_attachment.controller_execution,
    aws_iam_role_policy.controller_execution_secrets,
  ]
}

resource "aws_ecs_service" "postgres" {
  count                              = local.services_enabled ? 1 : 0
  name                               = "postgres"
  cluster                            = aws_ecs_cluster.shiftbox.id
  launch_type                        = "EC2"
  task_definition                    = aws_ecs_task_definition.postgres[0].arn
  desired_count                      = 1
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  propagate_tags                     = "TASK_DEFINITION"

  network_configuration {
    subnets          = [aws_subnet.services.id]
    security_groups  = [aws_security_group.task["postgres"].id]
    assign_public_ip = false
  }
  service_registries {
    registry_arn = aws_service_discovery_service.service["postgres"].arn
  }
  depends_on = [aws_volume_attachment.data, aws_iam_role_policy.execution_secrets]
}

resource "aws_ecs_service" "controller" {
  count                              = local.services_enabled ? 1 : 0
  name                               = "controller"
  cluster                            = aws_ecs_cluster.shiftbox.id
  launch_type                        = "EC2"
  task_definition                    = aws_ecs_task_definition.controller[0].arn
  desired_count                      = var.controller_enabled ? 1 : 0
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  propagate_tags                     = "TASK_DEFINITION"

  network_configuration {
    subnets          = [aws_subnet.services.id]
    security_groups  = [aws_security_group.task["controller"].id]
    assign_public_ip = false
  }
  service_registries {
    registry_arn = aws_service_discovery_service.service["shiftbox"].arn
  }
  depends_on = [aws_ecs_service.postgres, aws_iam_role_policy.controller]
}
