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
  region              = "us-east-2"
  allowed_account_ids = ["888293065132"]
}

variable "build_network" {
  description = "Existing private Ohio Packer subnet, endpoint, security group, and pinned Canonical source."
  type = object({
    vpc_id              = string
    private_subnet_id   = string
    private_subnet_cidr = string
    endpoint_id         = string
    builder_group_id    = string
    source_ami          = string
    source_snapshot     = string
  })
  validation {
    condition = alltrue([
      can(regex("^vpc-[a-f0-9]{17}$", var.build_network.vpc_id)),
      can(regex("^subnet-[a-f0-9]{17}$", var.build_network.private_subnet_id)),
      can(cidrnetmask(var.build_network.private_subnet_cidr)),
      can(regex("^eice-[a-f0-9]{17}$", var.build_network.endpoint_id)),
      can(regex("^sg-[a-f0-9]{17}$", var.build_network.builder_group_id)),
      can(regex("^ami-[a-f0-9]{17}$", var.build_network.source_ami)),
      can(regex("^snap-[a-f0-9]{17}$", var.build_network.source_snapshot)),
    ])
    error_message = "Supply the existing Ohio builder network and source AMI and snapshot IDs."
  }
}

variable "cloudformation_execution_policy_arn" {
  description = "Approved customer-managed policy for CloudFormation to operate only the sbtest-main stack."
  type        = string
  validation {
    condition     = var.cloudformation_execution_policy_arn == "arn:aws:iam::888293065132:policy/shiftbox-main-test-cloudformation"
    error_message = "Use the reviewed Ohio testing policy named shiftbox-main-test-cloudformation."
  }
}

locals {
  account    = "888293065132"
  region     = "us-east-2"
  stack      = "sbtest-main"
  oidc       = "token.actions.githubusercontent.com"
  subject    = "repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-testing"
  cfn        = "arn:aws:cloudformation:${local.region}:${local.account}"
  ec2        = "arn:aws:ec2:${local.region}"
  owned      = "${local.ec2}:${local.account}"
  stack_arn  = "${local.cfn}:stack/${local.stack}/*"
  change_arn = "${local.cfn}:changeSet/main-test-*/*"

  tags = {
    shiftbox-project      = "shiftbox"
    shiftbox-cluster-name = local.stack
  }
  request_tags  = { for key, value in local.tags : "aws:RequestTag/${key}" => value }
  resource_tags = { for key, value in local.tags : "aws:ResourceTag/${key}" => value }

  bucket_read = {
    Effect   = "Allow"
    Action   = ["s3:GetBucketLocation", "s3:GetBucketPublicAccessBlock", "s3:GetBucketVersioning"]
    Resource = aws_s3_bucket.artifacts.arn
  }
  helper_object   = "${aws_s3_bucket.artifacts.arn}/main-test/*/*/ShiftBox-Local-Helper.dmg"
  template_object = "${aws_s3_bucket.artifacts.arn}/main-test/*/*/template.yaml"
  template_url    = "https://${aws_s3_bucket.artifacts.bucket}.s3.${local.region}.amazonaws.com/main-test/*/*/template.yaml?versionId=*"
  subnets         = "${local.owned}:subnet/${var.build_network.private_subnet_id}"
}

resource "aws_s3_bucket" "artifacts" {
  bucket_prefix = "shiftbox-main-test-"
  tags          = local.tags
}

resource "aws_s3_bucket_ownership_controls" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket     = aws_s3_bucket.artifacts.id
  depends_on = [aws_s3_bucket_versioning.artifacts]
  rule {
    id     = "expire-disposable-workflow-objects"
    status = "Enabled"
    filter {
      prefix = "main-test/"
    }
    expiration {
      days = 7
    }
    noncurrent_version_expiration {
      noncurrent_days = 1
    }
  }
}

resource "aws_iam_role" "actions" {
  for_each             = toset(["stage", "deploy"])
  name                 = "shiftbox-main-test-${each.key}"
  max_session_duration = each.key == "deploy" ? 7200 : 3600
  tags                 = local.tags
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = { Federated = "arn:aws:iam::${local.account}:oidc-provider/${local.oidc}" }
      Condition = { StringEquals = { "${local.oidc}:aud" = "sts.amazonaws.com", "${local.oidc}:sub" = local.subject } }
    }]
  })
}

resource "aws_iam_role_policy" "stage" {
  name = "StagePrivateHelper"
  role = aws_iam_role.actions["stage"].name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [local.bucket_read, {
      Effect    = "Allow", Action = "s3:PutObject", Resource = local.helper_object
      Condition = { StringEquals = { "s3:x-amz-server-side-encryption" = "AES256" } }
    }]
  })
}

resource "aws_iam_role" "cloudformation" {
  name = "shiftbox-main-test-cfn"
  tags = local.tags
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow", Action = "sts:AssumeRole", Principal = { Service = "cloudformation.amazonaws.com" }
      Condition = {
        StringEqualsIfExists = { "aws:SourceAccount" = local.account }
        ArnLikeIfExists      = { "aws:SourceArn" = local.stack_arn }
      }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "cloudformation_execution" {
  role       = aws_iam_role.cloudformation.name
  policy_arn = var.cloudformation_execution_policy_arn
}

resource "aws_iam_role_policy" "cloudformation_template" {
  name = "ReadPrivateTestTemplate"
  role = aws_iam_role.cloudformation.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow", Action = ["s3:GetObject", "s3:GetObjectVersion"], Resource = local.template_object
    }]
  })
}

resource "aws_iam_role_policy" "deploy" {
  name = "DeployReviewedMainTest"
  role = aws_iam_role.actions["deploy"].name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [local.bucket_read,
      { Effect = "Allow", Action = ["s3:GetObject", "s3:GetObjectVersion"], Resource = [local.helper_object, local.template_object] },
      {
        Effect    = "Allow", Action = "s3:PutObject", Resource = local.template_object
        Condition = { StringEquals = { "s3:x-amz-server-side-encryption" = "AES256" } }
      },
      {
        Effect   = "Allow", Action = "secretsmanager:DescribeSecret"
        Resource = "arn:aws:secretsmanager:${local.region}:${local.account}:secret:shiftbox-main-test-ghcr-??????"
      },
      {
        Effect = "Allow", Action = "cloudformation:CreateChangeSet", Resource = [local.stack_arn, local.change_arn]
        Condition = {
          StringEquals = merge(local.request_tags, { "cloudformation:RoleArn" = aws_iam_role.cloudformation.arn })
          StringLike   = { "cloudformation:TemplateUrl" = local.template_url }
        }
      },
      {
        Effect = "Allow", Resource = [local.stack_arn, local.change_arn]
        Action = ["cloudformation:DescribeStacks", "cloudformation:DescribeChangeSet", "cloudformation:GetTemplate",
        "cloudformation:ExecuteChangeSet", "cloudformation:DeleteChangeSet"]
      },
      {
        Effect    = "Allow", Action = "iam:PassRole", Resource = aws_iam_role.cloudformation.arn
        Condition = { StringEquals = { "iam:PassedToService" = "cloudformation.amazonaws.com" } }
      },
    ]
  })
}

output "staging_bucket" {
  value = aws_s3_bucket.artifacts.bucket
}

output "workflow_build_network" {
  value = jsonencode({ for key, value in var.build_network : key => value if key != "source_snapshot" })
}
