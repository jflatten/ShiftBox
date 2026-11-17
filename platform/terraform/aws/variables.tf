variable "name" {
  description = "Name prefix that distinguishes this ShiftBox deployment from other resources."
  type        = string
  default     = "shiftbox"
  nullable    = false

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,30}[a-z0-9]$", var.name))
    error_message = "The name must have 2–32 lowercase letters, digits, or hyphens, start with a letter, and end with a letter or digit."
  }
}

variable "aws_region" {
  description = "AWS Region for all deployment resources."
  type        = string
  nullable    = false
}

variable "availability_zone" {
  description = "Availability Zone for the single host, its data volume, and workspace instances."
  type        = string
  nullable    = false

  validation {
    condition     = startswith(var.availability_zone, var.aws_region) && var.availability_zone != var.aws_region
    error_message = "Choose an Availability Zone within aws_region."
  }
}

variable "vpc_cidr" {
  description = "IPv4 CIDR for the deployment VPC. Choose a range that does not overlap the deployer's routed networks."
  type        = string
  nullable    = false

  validation {
    condition = try(
      can(cidrnetmask(var.vpc_cidr)) &&
      tonumber(split("/", var.vpc_cidr)[1]) >= 16 &&
      tonumber(split("/", var.vpc_cidr)[1]) <= 24,
      false,
    )
    error_message = "The VPC CIDR must be an IPv4 CIDR with a prefix length between /16 and /24."
  }
}

variable "operator_ipv4_cidrs" {
  description = "Source IPv4 CIDRs of browser traffic routed privately into the VPC by the deployer."
  type        = set(string)
  nullable    = false

  validation {
    condition = length(var.operator_ipv4_cidrs) > 0 && alltrue([
      for cidr in var.operator_ipv4_cidrs : try(can(cidrnetmask(cidr)) && tonumber(split("/", cidr)[1]) > 0, false)
    ])
    error_message = "Provide at least one specific IPv4 source CIDR for private browser access."
  }
}

variable "create_nat_gateway" {
  description = "Create one public NAT gateway for both private subnets. Set to false if the deployer manages outbound routes."
  type        = bool
  default     = true
  nullable    = false
}

variable "data_volume_size_gib" {
  description = "Size of the encrypted installation and PostgreSQL data volume, in GiB."
  type        = number
  default     = 20
  nullable    = false

  validation {
    condition     = var.data_volume_size_gib > 0 && floor(var.data_volume_size_gib) == var.data_volume_size_gib
    error_message = "Specify a positive whole number of GiB."
  }
}

variable "aws_account_id" {
  description = "Expected AWS account. Set this to protect deployments against an unintended provider account."
  type        = string
  default     = null
  validation {
    condition     = var.aws_account_id == null || can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "Use a 12-digit AWS account ID."
  }
}

variable "environment" {
  description = "Environment label applied to AWS resources."
  type        = string
  default     = "private"
}
