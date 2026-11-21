terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

variable "bucket_name" {
  description = "S3 bucket used exclusively for published, versioned release templates."
  type        = string
  nullable    = false
}

variable "cluster_name" {
  description = "Deployment label for this publisher's Terraform resources."
  type        = string
}

locals {
  bucket_arn   = "arn:aws:s3:::${var.bucket_name}"
  template_arn = "${local.bucket_arn}/public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml"
}

resource "aws_s3_bucket" "templates" {
  bucket        = var.bucket_name
  force_destroy = false
  tags          = { project = "shiftbox", cluster_name = var.cluster_name }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "templates" {
  bucket = aws_s3_bucket.templates.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "templates" {
  bucket = aws_s3_bucket.templates.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_ownership_controls" "templates" {
  bucket = aws_s3_bucket.templates.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "templates" {
  bucket             = aws_s3_bucket.templates.id
  block_public_acls  = true
  ignore_public_acls = true
  # This dedicated bucket permits only the policy's public versioned reads.
  block_public_policy     = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_policy" "templates" {
  bucket = aws_s3_bucket.templates.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "ReadVersionedLaunchTemplates"
        Effect    = "Allow"
        Action    = ["s3:GetObjectVersion"]
        Principal = "*"
        Resource  = [local.template_arn]
      },
      {
        Sid       = "DenyInsecureTransport"
        Effect    = "Deny"
        Action    = ["s3:*"]
        Principal = "*"
        Resource  = [local.bucket_arn, "${local.bucket_arn}/*"]
        Condition = {
          Bool = { "aws:SecureTransport" = "false" }
        }
      },
      {
        Sid       = "DenyNonConditionalTemplateWrites"
        Effect    = "Deny"
        Action    = ["s3:PutObject"]
        Principal = "*"
        Resource  = [local.template_arn]
        Condition = {
          StringNotEquals = { "s3:if-none-match" = "*" }
        }
      },
      {
        Sid       = "RetainPublishedTemplateVersions"
        Effect    = "Deny"
        Action    = ["s3:DeleteObject", "s3:DeleteObjectVersion"]
        Principal = "*"
        Resource  = [local.template_arn]
      },
    ]
  })

  depends_on = [
    aws_s3_bucket_ownership_controls.templates,
    aws_s3_bucket_public_access_block.templates,
    aws_s3_bucket_server_side_encryption_configuration.templates,
    aws_s3_bucket_versioning.templates,
  ]
}

output "bucket_name" {
  description = "Bucket name in the region configured by the calling provider."
  value       = aws_s3_bucket.templates.bucket
}
