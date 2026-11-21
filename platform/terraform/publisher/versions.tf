terraform {
  required_version = ">= 1.12.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

# Every alias must authenticate to the same explicitly approved publisher account.
provider "aws" {
  region              = "us-east-2"
  allowed_account_ids = [var.publisher_account_id]
}

provider "aws" {
  alias               = "us_east_1"
  region              = "us-east-1"
  allowed_account_ids = [var.publisher_account_id]
}

provider "aws" {
  alias               = "us_west_1"
  region              = "us-west-1"
  allowed_account_ids = [var.publisher_account_id]
}

provider "aws" {
  alias               = "us_west_2"
  region              = "us-west-2"
  allowed_account_ids = [var.publisher_account_id]
}
