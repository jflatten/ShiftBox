variable "tailscale_router_enabled" {
  description = "Deploy an opt-in Tailscale subnet router for private browser access to ShiftBox."
  type        = bool
  default     = false
  nullable    = false
}

locals {
  tailscale_router_name  = "${var.name}-tailscale"
  tailscale_router_route = cidrsubnet(var.vpc_cidr, 4, 0)
}

data "aws_ssm_parameter" "tailscale_router_ami" {
  count = var.tailscale_router_enabled ? 1 : 0
  name  = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_iam_role" "tailscale_router" {
  count = var.tailscale_router_enabled ? 1 : 0
  name  = local.tailscale_router_name
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "tailscale_router" {
  count      = var.tailscale_router_enabled ? 1 : 0
  role       = one(aws_iam_role.tailscale_router).name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "tailscale_router" {
  count = var.tailscale_router_enabled ? 1 : 0
  name  = local.tailscale_router_name
  role  = one(aws_iam_role.tailscale_router).name
}

resource "aws_security_group" "tailscale_router" {
  count       = var.tailscale_router_enabled ? 1 : 0
  name        = local.tailscale_router_name
  description = "Private Tailscale subnet router with no VPC ingress"
  vpc_id      = aws_vpc.shiftbox.id
  tags        = { Name = local.tailscale_router_name }
}

resource "aws_vpc_security_group_egress_rule" "tailscale_https" {
  count             = var.tailscale_router_enabled ? 1 : 0
  security_group_id = one(aws_security_group.tailscale_router).id
  description       = "HTTPS for Tailscale, Systems Manager and package repositories"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "tailscale_stun" {
  count             = var.tailscale_router_enabled ? 1 : 0
  security_group_id = one(aws_security_group.tailscale_router).id
  description       = "Tailscale STUN"
  ip_protocol       = "udp"
  from_port         = 3478
  to_port           = 3478
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "tailscale_controller" {
  count                        = var.tailscale_router_enabled ? 1 : 0
  security_group_id            = one(aws_security_group.tailscale_router).id
  referenced_security_group_id = aws_security_group.task["controller"].id
  ip_protocol                  = "tcp"
  from_port                    = local.browser_port
  to_port                      = local.browser_port
}

resource "aws_vpc_security_group_ingress_rule" "tailscale_controller" {
  count                        = var.tailscale_router_enabled ? 1 : 0
  security_group_id            = aws_security_group.task["controller"].id
  referenced_security_group_id = one(aws_security_group.tailscale_router).id
  ip_protocol                  = "tcp"
  from_port                    = local.browser_port
  to_port                      = local.browser_port
}

resource "aws_instance" "tailscale_router" {
  count                       = var.tailscale_router_enabled ? 1 : 0
  ami                         = nonsensitive(one(data.aws_ssm_parameter.tailscale_router_ami).value)
  instance_type               = "t4g.micro"
  subnet_id                   = aws_subnet.services.id
  vpc_security_group_ids      = [one(aws_security_group.tailscale_router).id]
  associate_public_ip_address = false
  source_dest_check           = false
  iam_instance_profile        = one(aws_iam_instance_profile.tailscale_router).name

  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    encrypted   = true
    volume_type = "gp3"
    volume_size = 8
    tags        = { Name = "${local.tailscale_router_name}-root", project = "shiftbox", environment = var.environment, cluster_name = var.name }
  }

  user_data = templatefile("${path.module}/tailscale/user-data.sh.tftpl", {
    dns_suffix   = coalesce(var.dns_suffix, "invalid.example")
    subnet_cidr  = local.tailscale_router_route
    browser_port = local.browser_port
  })

  lifecycle {
    precondition {
      condition     = local.services_enabled && var.dns_suffix != null
      error_message = "The Tailscale router requires the controller service and its private DNS suffix."
    }
    # Refreshing the public AMI lookup must not replace a router enrolled in a tailnet.
    ignore_changes = [ami]
  }

  tags = { Name = local.tailscale_router_name }

  depends_on = [
    aws_iam_role_policy_attachment.tailscale_router,
    aws_route_table_association.services,
    aws_route.private_internet,
    aws_vpc_security_group_egress_rule.tailscale_https,
  ]
}

resource "aws_ec2_tag" "tailscale_router_interface" {
  for_each    = var.tailscale_router_enabled ? { project = "shiftbox", cluster_name = var.name } : {}
  resource_id = one(aws_instance.tailscale_router).primary_network_interface_id
  key         = each.key
  value       = each.value
}

output "tailscale_router_instance_id" {
  description = "Connect to this instance via Systems Manager to authorize it in your tailnet."
  value       = one(aws_instance.tailscale_router[*].id)
}

output "tailscale_advertised_route" {
  description = "Advertise and approve this services subnet in your tailnet."
  value       = var.tailscale_router_enabled ? local.tailscale_router_route : null
}
