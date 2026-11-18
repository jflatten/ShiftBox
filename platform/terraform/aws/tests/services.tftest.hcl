mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
}

variables {
  aws_region               = "us-east-1"
  availability_zone        = "us-east-1a"
  vpc_cidr                 = "10.76.0.0/16"
  operator_ipv4_cidrs      = ["10.20.0.0/24"]
  ecs_host_ami_id          = "ami-00112233445566778"
  dns_suffix               = "dev.example.test"
  controller_image_digest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  postgres_image           = "postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  postgres_credentials_arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:postgres-credentials"
  postgres_tls_arn         = "arn:aws:secretsmanager:us-east-1:123456789012:secret:postgres-tls"
  postgres_ca_pem          = "test-ca"
}

# Setup runs on demand while the controller is stopped and has its own scoped role.
run "private_bootstrap_services" {
  command = plan

  assert {
    condition     = aws_ecs_service.controller[0].desired_count == 0 && aws_ecs_service.postgres[0].desired_count == 1
    error_message = "Bootstrap must start with PostgreSQL online and the normal controller stopped."
  }
  assert {
    condition     = !aws_ecs_service.controller[0].network_configuration[0].assign_public_ip && !aws_ecs_service.postgres[0].network_configuration[0].assign_public_ip
    error_message = "Neither ECS service can receive a public address."
  }
  assert {
    condition     = one(aws_ecs_task_definition.controller[0].volume).host_path == "/var/lib/shiftbox-data/shiftbox" && one(aws_ecs_task_definition.postgres[0].volume).host_path == "/var/lib/shiftbox-data/postgres"
    error_message = "The services must use separate paths on the persistent host volume."
  }
  assert {
    condition     = aws_iam_role.setup.name != aws_iam_role.controller.name && !strcontains(aws_iam_role_policy.setup.policy, "ec2:") && strcontains(aws_iam_role_policy.setup.policy, "ssmmessages:OpenDataChannel")
    error_message = "The setup role must be separate from EC2 lifecycle authority."
  }
  # ECS discovery and writable DNS records use separate private zones.
  assert {
    condition     = aws_service_discovery_private_dns_namespace.deployment[0].name == "services.dev.example.test" && aws_route53_zone.deployment[0].name == "dev.example.test" && aws_route53_record.controller_names["postgres"].records == toset(["postgres.services.dev.example.test"])
    error_message = "User-facing DNS names must resolve through the dedicated ECS discovery zone."
  }
  assert {
    condition     = length(terraform_data.discovery_zone_tags) == 1 && terraform_data.discovery_zone_tags[0].triggers_replace.cluster == var.name
    error_message = "The Cloud Map-managed Route 53 zone must receive the deployment tags."
  }
  # Session Manager uses the task's own loopback when no remote host is requested.
  assert {
    condition     = !contains(keys(jsondecode(aws_ssm_document.setup[0].content).properties), "host") && jsondecode(aws_ssm_document.setup[0].content).properties.portNumber == "8444"
    error_message = "The setup session must forward only the fixed local task port."
  }
  # EC2 canonicalizes the IAM attribute name independently of the API spelling.
  assert {
    condition     = local.workspace_enrollment_conditions.StringEquals["ec2:Attribute"] == "UserData"
    error_message = "The controller must be allowed to modify only the EC2 UserData attribute."
  }
}

# Optional private access leaves foundation-only and standard deployments untouched.
run "tailscale_router_disabled" {
  command = plan

  assert {
    condition     = length(aws_instance.tailscale_router) == 0
    error_message = "The router must be opt-in."
  }
}

# The router has no public address and forwards only the controller port and private DNS zone.
run "tailscale_router_private" {
  command = plan

  variables {
    tailscale_router_enabled = true
  }

  assert {
    condition = (
      !one(aws_instance.tailscale_router).associate_public_ip_address &&
      one(one(aws_instance.tailscale_router).root_block_device).encrypted &&
      one(one(aws_instance.tailscale_router).root_block_device).tags["cluster_name"] == var.name &&
      one(aws_vpc_security_group_egress_rule.tailscale_controller).from_port == 8443 &&
      length(aws_vpc_security_group_ingress_rule.tailscale_controller) == 1 &&
      strcontains(one(aws_instance.tailscale_router).user_data, "server=/dev.example.test/169.254.169.253") &&
      output.tailscale_advertised_route == "10.76.0.0/20"
    )
    error_message = "The router must be private, encrypted, tagged and scoped to the controller port and deployment DNS."
  }
}

# GitHub App credentials belong only to the normal controller, with an exact secret ARN.
run "controller_github_app" {
  command = plan

  variables {
    github_app_credentials_arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:shiftbox-demo/github-app-123abc"
  }

  assert {
    condition = (
      contains([for secret in local.controller_secrets : secret.valueFrom], var.github_app_credentials_arn) &&
      !contains([for secret in local.app_db_secret : secret.valueFrom], var.github_app_credentials_arn) &&
      contains(tolist(jsondecode(one(aws_iam_role_policy.controller_execution_secrets).policy).Statement[0].Resource), var.github_app_credentials_arn) &&
      !strcontains(one(aws_iam_role_policy.execution_secrets).policy, var.github_app_credentials_arn)
    )
    error_message = "Only the controller task and its dedicated execution role may receive the GitHub App secret."
  }
}

# A dedicated cache disk can be attached without replacing tasks on the host.
run "registry_cache_attach_without_service_mount" {
  command = plan

  variables {
    registry_cache_volume_size_gib = 100
  }

  assert {
    condition = (
      one(aws_ebs_volume.registry_cache).size == 100 &&
      one(aws_ebs_volume.registry_cache).encrypted &&
      one(aws_ebs_volume.registry_cache).type == "gp3" &&
      one(aws_volume_attachment.registry_cache).device_name == "/dev/sdg" &&
      length(one(aws_ecs_task_definition.controller).volume) == 1 &&
      length(one(aws_ecs_task_definition.postgres).volume) == 1
    )
    error_message = "Preparing cache storage must leave both existing service mounts intact."
  }
}

# Only the controller receives the cache bind after the host mount is verified.
run "registry_cache_verified_controller_bind" {
  command = plan

  variables {
    registry_cache_volume_size_gib = 100
    registry_cache_mount_ready     = true
  }

  assert {
    condition = (
      one(local.registry_cache_task_mounts).containerPath == "/var/lib/shiftbox-registry-cache" &&
      one(local.registry_cache_task_mounts).sourceVolume == "registry-cache" &&
      length(one(aws_ecs_task_definition.postgres).volume) == 1 &&
      length(one(aws_ecs_task_definition.setup).volume) == 1
    )
    error_message = "The cache disk must be bound only into the controller."
  }
}
