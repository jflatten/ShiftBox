terraform {
  required_version = ">= 1.12.0, < 2.0.0"
}

variable "testing_account_id" {
  type        = string
  description = "Independently verified qualification account."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.testing_account_id)) && var.testing_account_id != var.publisher_account_id && var.testing_account_id != "524941137496"
    error_message = "Supply the dedicated testing account, distinct from demo and publisher."
  }
}

variable "publisher_account_id" {
  type        = string
  description = "Independently verified official image publisher."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.publisher_account_id)) && var.publisher_account_id != "524941137496"
    error_message = "Supply the official publisher account, distinct from demo."
  }
}

variable "run_name" {
  type        = string
  description = "Exact qualification stack and cluster name for one GitHub run attempt."
  validation {
    condition     = can(regex("^sbq-[1-9][0-9]{0,15}-[1-9][0-9]{0,3}$", var.run_name))
    error_message = "Use sbq-<GitHub run ID>-<attempt> with numeric IDs."
  }
}

variable "host_ami_id" {
  type        = string
  description = "Exact Ohio Amazon ECS host AMI verified against the reviewed template default."
  validation {
    condition     = can(regex("^ami-[0-9a-f]{17}$", var.host_ami_id))
    error_message = "Supply the resolved Ohio ECS host AMI ID."
  }
}

variable "router_ami_id" {
  type        = string
  description = "Exact Ohio Arm AL2023 router AMI verified against the reviewed template default."
  validation {
    condition     = can(regex("^ami-[0-9a-f]{17}$", var.router_ami_id)) && var.router_ami_id != var.host_ami_id
    error_message = "Supply an exact router AMI ID distinct from the ECS host."
  }
}

variable "verified_vpc_id" {
  type        = string
  default     = null
  description = "Optional physical VPC ID verified to belong to this run before enabling DNS creation."
  validation {
    condition     = var.verified_vpc_id == null || can(regex("^vpc-[0-9a-f]{17}$", var.verified_vpc_id))
    error_message = "Supply a single verified Ohio VPC ID."
  }
}

variable "verified_private_zone_id" {
  type        = string
  default     = null
  description = "Optional ShiftBox application hosted-zone ID verified against the run VPC and exact DNS suffix."
  validation {
    condition = var.verified_private_zone_id == null || (
      var.verified_vpc_id != null && can(regex("^Z[A-Z0-9]{5,32}$", var.verified_private_zone_id))
    )
    error_message = "Supply an exact private zone ID only with its verified VPC."
  }
}

variable "verified_discovery_zone_id" {
  type        = string
  default     = null
  description = "Optional Cloud Map hosted-zone ID verified against the run VPC and exact discovery namespace."
  validation {
    condition = var.verified_discovery_zone_id == null || (
      var.verified_vpc_id != null && var.verified_discovery_zone_id != var.verified_private_zone_id &&
      can(regex("^Z[A-Z0-9]{5,32}$", var.verified_discovery_zone_id))
    )
    error_message = "Supply the distinct, exact Cloud Map zone ID only with its verified VPC."
  }
}

variable "verified_namespace_id" {
  type        = string
  default     = null
  description = "Optional exact Cloud Map namespace ID verified against the run VPC and discovery DNS suffix."
  validation {
    condition = var.verified_namespace_id == null || (
      var.verified_discovery_zone_id != null && can(regex("^ns-[a-z0-9]{16}$", var.verified_namespace_id))
    )
    error_message = "Supply the exact discovery namespace only with its verified VPC and hosted zone."
  }
}

variable "verified_discovery_service_ids" {
  type        = set(string)
  default     = []
  description = "The two exact Cloud Map service IDs, independently verified against the run namespace and names."
  validation {
    condition = length(var.verified_discovery_service_ids) == 0 || (
      var.verified_namespace_id != null && length(var.verified_discovery_service_ids) == 2 &&
      alltrue([for id in var.verified_discovery_service_ids : can(regex("^srv-[a-z0-9]{16}$", id))])
    )
    error_message = "Supply both verified discovery service IDs with their own verified namespace."
  }
}

locals {
  region       = "us-east-2"
  account      = var.testing_account_id
  run          = var.run_name
  regional     = "arn:aws:%s:${local.region}:${local.account}:%s"
  region_check = { "aws:RequestedRegion" = local.region }
  request_tags = {
    "aws:RequestTag/project"      = "shiftbox"
    "aws:RequestTag/cluster_name" = local.run
  }
  resource_tags = {
    "aws:ResourceTag/project"      = "shiftbox"
    "aws:ResourceTag/cluster_name" = local.run
  }
  regional_reads = { StringEquals = local.region_check }
  tagged_create  = { StringEquals = merge(local.region_check, local.request_tags) }
  tagged_run     = { StringEquals = merge(local.region_check, local.resource_tags) }
  core_statements = {
    identity         = [for s in local.identity_statements : s if !contains(s.Action, "iam:PutRolePolicy")]
    inline           = [for s in local.identity_statements : s if contains(s.Action, "iam:PutRolePolicy")]
    roles            = local.identity_lifecycle_statements
    network_create   = [for s in local.lifecycle_statements : s if anytrue([for a in s.Action : startswith(a, "ec2:Create") || a == "ec2:AllocateAddress"])]
    network_existing = [for s in local.lifecycle_statements : s if !anytrue([for a in s.Action : startswith(a, "ec2:Create") || a == "ec2:AllocateAddress"])]
    instances        = local.instance_statements
    services         = [for s in local.service_statements : s if startswith(s.Action[0], "ecs:")]
    service_support  = [for s in local.service_statements : s if !startswith(s.Action[0], "ecs:")]
  }
  policy_statements = merge(
    {
      for name, statements in local.core_statements : name => jsonencode({ Version = "2012-10-17", Statement = statements })
    },
    var.verified_vpc_id == null ? {} : {
      dns       = jsonencode({ Version = "2012-10-17", Statement = local.dns_statements })
      discovery = jsonencode({ Version = "2012-10-17", Statement = local.discovery_statements })
    }
  )
}

output "policy_documents" {
  description = "Unattached and unvalidated-in-cloud IAM execution policy documents for one reviewed qualification run."
  value       = local.policy_statements
}

output "required_role_boundaries" {
  description = "Prerequisite per-role boundaries to review and provision separately."
  value       = local.boundary_arns
}
