variable "ecs_host_ami_id" {
  description = "Pinned ECS-optimized Amazon Linux 2023 amd64 AMI in the deployment region. Omit for foundation-only deployment."
  type        = string
  default     = null

  validation {
    condition     = var.ecs_host_ami_id == null || can(regex("^ami-[0-9a-f]{17}$", var.ecs_host_ami_id))
    error_message = "The ECS host AMI must be an explicit regional AMI ID."
  }
}

variable "ecs_host_instance_type" {
  description = "Nitro-based amd64 instance type for the shared ECS host."
  type        = string
  default     = "t3.medium"
}

variable "initialize_data_volume" {
  description = "Allow filesystem creation on the new blank data volume on its first deployment. Leave disabled when replacing a host or recovering data."
  type        = bool
  default     = false
}

resource "aws_ecs_cluster" "shiftbox" {
  name = var.name
}

resource "aws_security_group" "ecs_host" {
  name        = "${var.name}-ecs-host"
  description = "Private ECS host with outbound access and no ingress"
  vpc_id      = aws_vpc.shiftbox.id
}

resource "aws_vpc_security_group_egress_rule" "ecs_host" {
  security_group_id = aws_security_group.ecs_host.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

locals {
  ecs_host_first_boot = templatefile("${path.module}/host/user-data.sh.tftpl", {
    cluster_name  = aws_ecs_cluster.shiftbox.name
    volume_serial = replace(aws_ebs_volume.data.id, "-", "")
    initialize    = var.initialize_data_volume
    mount_script  = file("${path.module}/host/mount-data.sh")
  })
}

resource "aws_instance" "ecs_host" {
  count                       = var.ecs_host_ami_id == null ? 0 : 1
  ami                         = var.ecs_host_ami_id
  instance_type               = var.ecs_host_instance_type
  subnet_id                   = aws_subnet.services.id
  vpc_security_group_ids      = [aws_security_group.ecs_host.id]
  associate_public_ip_address = false
  iam_instance_profile        = aws_iam_instance_profile.ecs_host.name

  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    encrypted   = true
    volume_type = "gp3"
    volume_size = 30
    tags        = { Name = "${var.name}-ecs-host-root", project = "shiftbox", environment = var.environment, cluster_name = var.name }
  }

  user_data_replace_on_change = false

  lifecycle {
    prevent_destroy = true
  }

  user_data = var.registry_cache_bootstrap ? templatefile("${path.module}/host/registry-cache-user-data.mime.tftpl", {
    first_boot   = local.ecs_host_first_boot
    volume_id    = one(aws_ebs_volume.registry_cache[*].id)
    data_serial  = replace(aws_ebs_volume.data.id, "-", "")
    mount_script = file("${path.module}/host/mount-registry-cache.sh")
  }) : local.ecs_host_first_boot

  tags = { Name = "${var.name}-ecs-host" }

  depends_on = [
    aws_iam_role_policy_attachment.ecs_host,
    aws_route_table_association.services,
    aws_route.private_internet,
    aws_vpc_security_group_egress_rule.ecs_host,
  ]
}

resource "aws_volume_attachment" "data" {
  count                          = var.ecs_host_ami_id == null ? 0 : 1
  device_name                    = "/dev/sdf"
  volume_id                      = aws_ebs_volume.data.id
  instance_id                    = aws_instance.ecs_host[0].id
  stop_instance_before_detaching = true
}

resource "aws_ec2_tag" "host_interface" {
  for_each    = var.ecs_host_ami_id == null ? {} : { project = "shiftbox", cluster_name = var.name }
  resource_id = aws_instance.ecs_host[0].primary_network_interface_id
  key         = each.key
  value       = each.value
}

output "ecs_cluster_name" {
  description = "ECS cluster used for setup and deployed services."
  value       = aws_ecs_cluster.shiftbox.name
}
