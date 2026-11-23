terraform {
  required_version = ">= 1.12.0, < 2.0.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

provider "aws" {
  region              = local.region
  allowed_account_ids = [local.publisher_account_id]
  default_tags {
    tags = local.tags
  }
}

variable "publisher_account_id" {
  type        = string
  description = "Independently verified AWS publisher account for this attempt."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.publisher_account_id))
    error_message = "Provide the 12-digit publisher account ID."
  }
}

variable "vpc_cidr" {
  type        = string
  description = "Reviewed, nonoverlapping private IPv4 /24 for the disposable build VPC."
  validation {
    condition = (
      can(cidrnetmask(var.vpc_cidr)) &&
      can(regex("^(10[.][0-9]+[.][0-9]+|172[.](1[6-9]|2[0-9]|3[01])[.][0-9]+|192[.]168[.][0-9]+)[.]0/24$", var.vpc_cidr))
    )
    error_message = "Use a network-aligned RFC 1918 IPv4 /24."
  }
}

variable "attempt_id" {
  type        = string
  description = "Unique lower-case ID for the disposable build attempt."
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,17}$", var.attempt_id))
    error_message = "Give the attempt a unique 1–18 character lower-case ID."
  }
}

variable "candidate_tag" {
  type        = string
  description = "Protected candidate tag reviewed for this publisher."
  validation {
    condition     = length(var.candidate_tag) <= 96 && can(regex("^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)-rc\\.[1-9][0-9]*$", var.candidate_tag))
    error_message = "Specify a canonical release candidate tag, such as v1.0.0-rc.1."
  }
}

variable "availability_zone_id" {
  type        = string
  description = "Verified Ohio zone ID offering the Packer builder's t3.medium instance type."
  validation {
    condition     = contains(["use2-az1", "use2-az2", "use2-az3"], var.availability_zone_id)
    error_message = "Choose a verified Ohio availability-zone ID."
  }
}

variable "packer_public_key" {
  type        = string
  description = "One attempt's OpenSSH public key. Keep the private key outside Terraform."
  validation {
    condition     = can(regex("^ssh-ed25519 [A-Za-z0-9+/=]+( [^\r\n]+)?$", var.packer_public_key))
    error_message = "Supply only a one-line Ed25519 OpenSSH public key."
  }
}

variable "expires_at" {
  type        = string
  description = "Absolute UTC deadline for the disposable build resources."
  validation {
    condition     = can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$", var.expires_at)) && can(timeadd(var.expires_at, "0s"))
    error_message = "Use a complete UTC RFC3339 deadline."
  }
}

variable "allow_builder_http" {
  type        = bool
  default     = false
  description = "Allow TCP 80 only on the Packer build guest when signed Ubuntu APT sources require it."
}

locals {
  publisher_account_id = var.publisher_account_id
  region               = "us-east-2"
  name                 = "shiftbox-publisher-${var.attempt_id}"
  vpc_cidr             = var.vpc_cidr
  tags = {
    project           = "shiftbox"
    cluster_name      = "shiftbox-publisher"
    environment       = "release-${var.candidate_tag}"
    publisher_attempt = var.attempt_id
  }
}

resource "terraform_data" "attempt_window" {
  input = var.expires_at
  lifecycle {
    precondition {
      condition = (
        timecmp(var.expires_at, timeadd(plantimestamp(), "15m")) > 0 &&
        timecmp(var.expires_at, timeadd(plantimestamp(), "2h")) <= 0
      )
      error_message = "The reviewed deadline must leave at least 15 minutes and at most two hours from the planning time."
    }
    precondition {
      condition     = timecmp(var.expires_at, timeadd(timestamp(), "15m")) > 0
      error_message = "The reviewed attempt is stale. Generate a fresh plan before provisioning."
    }
  }
}

resource "aws_vpc" "build" {
  cidr_block           = local.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.name }
  depends_on           = [terraform_data.attempt_window]
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.build.id
  cidr_block              = cidrsubnet(local.vpc_cidr, 4, 0)
  availability_zone_id    = var.availability_zone_id
  map_public_ip_on_launch = false
  tags                    = { Name = "${local.name}-egress" }
}

resource "aws_subnet" "private" {
  vpc_id                  = aws_vpc.build.id
  cidr_block              = cidrsubnet(local.vpc_cidr, 2, 1)
  availability_zone_id    = var.availability_zone_id
  map_public_ip_on_launch = false
  tags                    = { Name = "${local.name}-build" }
}

resource "aws_internet_gateway" "build" {
  vpc_id = aws_vpc.build.id
  tags   = { Name = local.name }
}

resource "aws_eip" "nat" {
  domain     = "vpc"
  tags       = { Name = "${local.name}-nat" }
  depends_on = [terraform_data.attempt_window]
}

resource "aws_nat_gateway" "build" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public.id
  tags          = { Name = local.name }
  depends_on    = [aws_internet_gateway.build]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.build.id
  tags   = { Name = "${local.name}-egress" }
}

resource "aws_route" "public_default" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.build.id
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.build.id
  tags   = { Name = "${local.name}-build" }
}

resource "aws_route" "private_default" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.build.id
}

resource "aws_route_table_association" "private" {
  subnet_id      = aws_subnet.private.id
  route_table_id = aws_route_table.private.id
}

resource "aws_security_group" "endpoint" {
  name        = "${local.name}-endpoint"
  description = "Dedicated SSH endpoint for the private Packer builder"
  vpc_id      = aws_vpc.build.id
}

resource "aws_security_group" "builder" {
  name        = "${local.name}-builder"
  description = "Private Packer guest"
  vpc_id      = aws_vpc.build.id
}

resource "aws_vpc_security_group_egress_rule" "endpoint_ssh" {
  security_group_id            = aws_security_group.endpoint.id
  referenced_security_group_id = aws_security_group.builder.id
  ip_protocol                  = "tcp"
  from_port                    = 22
  to_port                      = 22
}

resource "aws_vpc_security_group_ingress_rule" "builder_ssh" {
  security_group_id            = aws_security_group.builder.id
  referenced_security_group_id = aws_security_group.endpoint.id
  ip_protocol                  = "tcp"
  from_port                    = 22
  to_port                      = 22
}

resource "aws_vpc_security_group_egress_rule" "builder_https" {
  security_group_id = aws_security_group.builder.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_vpc_security_group_egress_rule" "builder_http" {
  count             = var.allow_builder_http ? 1 : 0
  security_group_id = aws_security_group.builder.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
}

resource "aws_ec2_instance_connect_endpoint" "builder" {
  subnet_id          = aws_subnet.private.id
  security_group_ids = [aws_security_group.endpoint.id]
  ip_address_type    = "ipv4"
  preserve_client_ip = false
  tags               = { Name = local.name }
}

resource "aws_key_pair" "build" {
  key_name   = "${local.name}-packer"
  public_key = var.packer_public_key
  depends_on = [terraform_data.attempt_window]
}

output "attempt" {
  value = {
    account                     = local.publisher_account_id
    region                      = local.region
    attempt_id                  = var.attempt_id
    candidate_tag               = var.candidate_tag
    availability_zone_id        = var.availability_zone_id
    vpc_id                      = aws_vpc.build.id
    vpc_cidr                    = aws_vpc.build.cidr_block
    private_subnet_id           = aws_subnet.private.id
    private_subnet_cidr         = aws_subnet.private.cidr_block
    egress_subnet_id            = aws_subnet.public.id
    builder_group_id            = aws_security_group.builder.id
    endpoint_group_id           = aws_security_group.endpoint.id
    endpoint_id                 = aws_ec2_instance_connect_endpoint.builder.id
    endpoint_arn                = aws_ec2_instance_connect_endpoint.builder.arn
    nat_gateway_id              = aws_nat_gateway.build.id
    eip_allocation_id           = aws_eip.nat.id
    packer_key_pair             = aws_key_pair.build.key_name
    packer_public_key_sha256    = sha256(split(" ", var.packer_public_key)[1])
    deadline                    = var.expires_at
    cleanup_function_arn        = aws_lambda_function.expiry.arn
    cleanup_schedule_group_name = aws_scheduler_schedule_group.expiry.name
    cleanup_window_ends         = local.expiry_deadline
  }
}

# Render the hosted job's build-phase tunnel statement for review only.
# Attaching it belongs to the separately planned publisher build IAM policy.
output "build_connection_policy" {
  value = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "ec2-instance-connect:OpenTunnel"
      Resource = aws_ec2_instance_connect_endpoint.builder.arn
      Condition = {
        StringEquals          = { "aws:ResourceTag/publisher_attempt" = var.attempt_id }
        IpAddress             = { "ec2-instance-connect:privateIpAddress" = aws_subnet.private.cidr_block }
        NumericEquals         = { "ec2-instance-connect:remotePort" = 22 }
        NumericLessThanEquals = { "ec2-instance-connect:maxTunnelDuration" = 2700 }
      }
    }]
  })
}
