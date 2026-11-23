locals {
  state_bucket = "shiftbox-release-state-${var.publisher_account_id}-us-east-2"
  state_arn    = "arn:aws:s3:::${local.state_bucket}"
  state_prefix = "release-builds/"
  tags         = { project = "shiftbox", cluster_name = "shiftbox-publisher" }

  state_access = [
    {
      Sid    = "CheckStateBucket"
      Effect = "Allow"
      Action = [
        "s3:GetBucketLocation", "s3:GetBucketOwnershipControls", "s3:GetBucketPolicyStatus",
        "s3:GetBucketPublicAccessBlock", "s3:GetBucketVersioning", "s3:GetEncryptionConfiguration",
      ]
      Resource = local.state_arn
    },
    {
      Sid      = "ListOnlyReleaseBuilds"
      Effect   = "Allow"
      Action   = ["s3:ListBucket"]
      Resource = local.state_arn
      Condition = {
        StringLike = { "s3:prefix" = ["release-builds", "${local.state_prefix}*"] }
      }
    },
    {
      Sid    = "ReadAndWriteAttemptState"
      Effect = "Allow"
      Action = ["s3:GetObject", "s3:PutObject"]
      Resource = [
        "${local.state_arn}/${local.state_prefix}*/terraform.tfstate",
        "${local.state_arn}/${local.state_prefix}*/terraform.tfstate.tflock",
      ]
    },
    {
      Sid    = "ReadImmutableAttemptRecords"
      Effect = "Allow"
      Action = ["s3:GetObject"]
      Resource = [
        "${local.state_arn}/${local.state_prefix}*/lease.json",
        "${local.state_arn}/${local.state_prefix}*/retired.json",
        "${local.state_arn}/${local.state_prefix}*/phases/*/phase.json",
        "${local.state_arn}/${local.state_prefix}*/phases/*/phase-retired.json",
      ]
    },
    {
      Sid    = "WriteNewRetirementRecord"
      Effect = "Allow"
      Action = ["s3:PutObject"]
      Resource = [
        "${local.state_arn}/${local.state_prefix}*/retired.json",
        "${local.state_arn}/${local.state_prefix}*/phases/*/phase-retired.json",
      ]
      Condition = { StringEquals = { "s3:if-none-match" = "*" } }
    },
    {
      Sid      = "ReleaseOnlyTerraformStateLocks"
      Effect   = "Allow"
      Action   = ["s3:DeleteObject"]
      Resource = "${local.state_arn}/${local.state_prefix}*/terraform.tfstate.tflock"
    },
  ]

  state_lease_create = {
    Sid    = "WriteNewBuildLease"
    Effect = "Allow"
    Action = ["s3:PutObject"]
    Resource = [
      "${local.state_arn}/${local.state_prefix}*/lease.json",
      "${local.state_arn}/${local.state_prefix}*/phases/*/phase.json",
    ]
    Condition = { StringEquals = { "s3:if-none-match" = "*" } }
  }
}

resource "aws_s3_bucket" "state" {
  bucket        = local.state_bucket
  force_destroy = false
  tags          = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyInsecureTransport"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource  = [local.state_arn, "${local.state_arn}/*"]
        Condition = { Bool = { "aws:SecureTransport" = "false" } }
      },
      {
        Sid       = "RequireEncryptedWrites"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${local.state_arn}/${local.state_prefix}*"
        Condition = { StringNotEquals = { "s3:x-amz-server-side-encryption" = "AES256" } }
      },
      {
        Sid       = "DenyOverwritingAttemptRecords"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource = [
          "${local.state_arn}/${local.state_prefix}*/lease.json",
          "${local.state_arn}/${local.state_prefix}*/retired.json",
          "${local.state_arn}/${local.state_prefix}*/phases/*/phase.json",
          "${local.state_arn}/${local.state_prefix}*/phases/*/phase-retired.json",
        ]
        Condition = { StringNotEquals = { "s3:if-none-match" = "*" } }
      },
    ]
  })

  depends_on = [
    aws_s3_bucket_public_access_block.state,
    aws_s3_bucket_ownership_controls.state,
    aws_s3_bucket_versioning.state,
    aws_s3_bucket_server_side_encryption_configuration.state,
  ]
}

output "release_state_bucket" {
  description = "Dedicated encrypted private state backend in the publisher account."
  value       = aws_s3_bucket.state.bucket
}
