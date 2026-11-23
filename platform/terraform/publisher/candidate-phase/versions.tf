terraform {
  required_version = ">= 1.12.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

variable "publisher_account_id" {
  description = "The verified ShiftBox production publisher account."
  type        = string

  validation {
    condition = (
      can(regex("^[0-9]{12}$", var.publisher_account_id)) &&
      !contains(["524941137496", "888293065132"], var.publisher_account_id)
    )
    error_message = "Use the verified publisher account, separate from demo and qualification."
  }
}

provider "aws" {
  region              = "us-east-2"
  allowed_account_ids = [var.publisher_account_id]
}
