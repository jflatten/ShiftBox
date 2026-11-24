terraform {
  required_version = ">= 1.12.0, < 2.0.0"
  backend "local" {}

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }
}

provider "aws" {
  region              = "us-east-2"
  allowed_account_ids = ["888293065132"]
}

resource "aws_ecr_repository" "development" {
  name                 = "shiftbox-development"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false

  encryption_configuration {
    encryption_type = "AES256"
  }

  tags = {
    project      = "shiftbox"
    cluster_name = "shiftbox-development"
  }
}
