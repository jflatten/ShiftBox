terraform {
  required_version = ">= 1.12.0, < 2.0.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

variable "customer_account_id" {
  type        = string
  description = "Approved disposable AWS customer account, separate from the publisher."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.customer_account_id))
    error_message = "Supply the approved 12-digit customer AWS account ID."
  }
}

variable "publisher_account_id" {
  type        = string
  description = "Approved ShiftBox public image publisher account ID."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.publisher_account_id))
    error_message = "Supply the approved 12-digit publisher AWS account ID."
  }
}

variable "aws_region" {
  type        = string
  description = "Release region for the exact official image."
  validation {
    condition     = contains(["us-east-1", "us-east-2", "us-west-1", "us-west-2"], var.aws_region)
    error_message = "Choose one of the four published ShiftBox regions."
  }
}

variable "availability_zone" {
  type        = string
  description = "Available zone in the selected region supporting the validation instance type."
}

variable "ami_id" {
  type        = string
  description = "Exact official AMI ID for this region from the immutable release manifest."
  validation {
    condition     = can(regex("^ami-[0-9a-f]{17}$", var.ami_id))
    error_message = "Specify the exact published regional AMI ID."
  }
}

variable "name" {
  type        = string
  description = "Unique name for this disposable validation deployment."
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,30}[a-z0-9]$", var.name))
    error_message = "Choose a 2–32 character name using lowercase letters, digits and hyphens."
  }
}

provider "aws" {
  region              = var.aws_region
  allowed_account_ids = [var.customer_account_id]
  default_tags {
    tags = {
      project      = "shiftbox"
      cluster_name = var.name
    }
  }
}

data "aws_caller_identity" "customer" {}

data "aws_ami" "official" {
  owners = [var.publisher_account_id]
  filter {
    name   = "image-id"
    values = [var.ami_id]
  }
}

resource "aws_vpc" "validation" {
  cidr_block           = "10.208.0.0/24"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name }
}

resource "aws_subnet" "validation" {
  vpc_id                  = aws_vpc.validation.id
  availability_zone       = var.availability_zone
  cidr_block              = "10.208.0.0/25"
  map_public_ip_on_launch = false
  tags                    = { Name = var.name }
}

resource "aws_security_group" "validation" {
  vpc_id      = aws_vpc.validation.id
  description = "Isolated public AMI validation without inbound or internet access"
  tags        = { Name = var.name }
}

resource "aws_instance" "validation" {
  ami                         = var.ami_id
  instance_type               = "t3.medium"
  subnet_id                   = aws_subnet.validation.id
  vpc_security_group_ids      = [aws_security_group.validation.id]
  associate_public_ip_address = false

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  root_block_device {
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = true
  }

  tags        = { Name = var.name }
  volume_tags = { project = "shiftbox", cluster_name = var.name }

  lifecycle {
    precondition {
      condition     = data.aws_caller_identity.customer.account_id == var.customer_account_id && var.customer_account_id != var.publisher_account_id
      error_message = "Run the public launch probe in the approved nonpublisher customer account."
    }
    precondition {
      condition = (
        data.aws_ami.official.id == var.ami_id && data.aws_ami.official.owner_id == var.publisher_account_id &&
        data.aws_ami.official.public && data.aws_ami.official.architecture == "x86_64" &&
        data.aws_ami.official.root_device_type == "ebs" && data.aws_ami.official.virtualization_type == "hvm"
      )
      error_message = "The exact public publisher-owned amd64 EBS AMI must be available to the validation account."
    }
  }
}

output "instance_id" {
  description = "Disposable test instance, used to prove guest boot and check teardown."
  value       = aws_instance.validation.id
}

output "root_volume_id" {
  description = "Disposable encrypted EBS root."
  value       = aws_instance.validation.root_block_device[0].volume_id
}
