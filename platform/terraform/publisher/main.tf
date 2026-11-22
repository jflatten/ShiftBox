variable "publisher_account_id" {
  description = "Explicitly approved dedicated public publisher AWS account ID."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.publisher_account_id)) && var.publisher_account_id != "524941137496"
    error_message = "Pass an approved 12-digit publisher account ID separate from the private ShiftBox demo."
  }
}

variable "release_buckets" {
  description = "Four dedicated, globally unique S3 bucket names, keyed by supported commercial AWS region."
  type        = map(string)

  validation {
    condition = toset(keys(var.release_buckets)) == toset(["us-east-1", "us-east-2", "us-west-1", "us-west-2"]) && alltrue([
      for name in values(var.release_buckets) : can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", name))
    ]) && length(distinct(values(var.release_buckets))) == 4
    error_message = "Supply a different DNS-compatible template bucket for each supported region."
  }
}

variable "enable_public_template_reads" {
  description = "Allow anonymous versioned launch template reads only for an approved publication."
  type        = bool
  default     = false
  nullable    = false
}

locals {
  cluster_name = "shiftbox-publisher"
  tags         = { project = "shiftbox", cluster_name = local.cluster_name }
  regions      = ["us-east-1", "us-east-2", "us-west-1", "us-west-2"]

  buckets         = { for region in local.regions : region => lookup(var.release_buckets, region, null) }
  bucket_arns     = [for bucket in values(local.buckets) : "arn:aws:s3:::${bucket}" if bucket != null]
  release_key     = "public/templates/v*-rc.*/*/shiftbox-aws-cloudformation.yaml"
  template_arns   = [for arn in local.bucket_arns : "${arn}/${local.release_key}"]
  github_oidc_arn = "arn:aws:iam::${var.publisher_account_id}:oidc-provider/token.actions.githubusercontent.com"
  # This repository uses immutable GitHub OIDC subjects.
  github_subject = "repo:jflatten@11561330/ShiftBox@1457179716"

  oidc_trust = {
    for environment in ["candidate", "stable"] : environment => jsonencode({
      Version = "2012-10-17"
      Statement = [{
        Effect    = "Allow"
        Action    = "sts:AssumeRoleWithWebIdentity"
        Principal = { Federated = local.github_oidc_arn }
        Condition = {
          StringEquals = {
            "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
            "token.actions.githubusercontent.com:sub" = "${local.github_subject}:environment:aws-release-${environment}"
          }
        }
      }]
    })
  }
}

module "us_east_1" {
  source                       = "./modules/template-bucket"
  providers                    = { aws = aws.us_east_1 }
  bucket_name                  = local.buckets["us-east-1"]
  cluster_name                 = local.cluster_name
  enable_public_template_reads = var.enable_public_template_reads
}

module "us_east_2" {
  source                       = "./modules/template-bucket"
  bucket_name                  = local.buckets["us-east-2"]
  cluster_name                 = local.cluster_name
  enable_public_template_reads = var.enable_public_template_reads
}

module "us_west_1" {
  source                       = "./modules/template-bucket"
  providers                    = { aws = aws.us_west_1 }
  bucket_name                  = local.buckets["us-west-1"]
  cluster_name                 = local.cluster_name
  enable_public_template_reads = var.enable_public_template_reads
}

module "us_west_2" {
  source                       = "./modules/template-bucket"
  providers                    = { aws = aws.us_west_2 }
  bucket_name                  = local.buckets["us-west-2"]
  cluster_name                 = local.cluster_name
  enable_public_template_reads = var.enable_public_template_reads
}

resource "aws_iam_role" "candidate" {
  name                 = "shiftbox-publisher-templates-candidate"
  description          = "Stage checked ShiftBox candidate template bytes in the dedicated publication buckets."
  assume_role_policy   = local.oidc_trust.candidate
  max_session_duration = 3600
  tags                 = local.tags
}

resource "aws_iam_role_policy" "candidate" {
  name = "StageVersionedLaunchTemplates"
  role = aws_iam_role.candidate.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "VerifyTemplateBuckets"
        Effect   = "Allow"
        Action   = ["s3:GetBucketLocation", "s3:GetBucketVersioning"]
        Resource = local.bucket_arns
      },
      {
        Sid      = "ListTemplateVersions"
        Effect   = "Allow"
        Action   = ["s3:ListBucketVersions"]
        Resource = local.bucket_arns
        Condition = {
          StringLike = { "s3:prefix" = local.release_key }
        }
      },
      {
        Sid      = "WriteFirstTemplateVersion"
        Effect   = "Allow"
        Action   = ["s3:PutObject"]
        Resource = local.template_arns
        Condition = {
          StringEquals = {
            "s3:if-none-match"                = "*"
            "s3:x-amz-server-side-encryption" = "AES256"
            "s3:RequestObjectTag/project"     = "shiftbox"
          }
          StringLike = { "s3:RequestObjectTag/cluster_name" = "v*-rc.*" }
        }
      },
      {
        Sid      = "TagLaunchTemplates"
        Effect   = "Allow"
        Action   = ["s3:PutObjectTagging"]
        Resource = local.template_arns
        Condition = {
          StringEquals = { "s3:RequestObjectTag/project" = "shiftbox" }
          StringLike   = { "s3:RequestObjectTag/cluster_name" = "v*-rc.*" }
          "ForAllValues:StringEquals" = {
            "s3:RequestObjectTagKeys" = ["project", "cluster_name"]
          }
          Null = { "s3:RequestObjectTagKeys" = "false" }
        }
      },
    ]
  })
}

resource "aws_iam_role" "stable" {
  name                 = "shiftbox-publisher-stable-verify"
  description          = "Independently verify existing public templates and AMI metadata for stable."
  assume_role_policy   = local.oidc_trust.stable
  max_session_duration = 3600
  tags                 = local.tags
}

resource "aws_iam_role_policy" "stable" {
  name = "VerifyPublicAWSArtifacts"
  role = aws_iam_role.stable.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "VerifyTemplateBuckets"
        Effect   = "Allow"
        Action   = ["s3:GetBucketLocation", "s3:GetBucketVersioning"]
        Resource = local.bucket_arns
      },
      {
        Sid      = "VerifyTemplateVersions"
        Effect   = "Allow"
        Action   = ["s3:GetObjectVersion"]
        Resource = local.template_arns
      },
      {
        Sid      = "InspectPublicImageMetadata"
        Effect   = "Allow"
        Action   = ["ec2:DescribeImages", "ec2:DescribeImageAttribute", "ec2:DescribeSnapshots", "ec2:DescribeSnapshotAttribute"]
        Resource = "*"
        Condition = {
          StringEquals = { "aws:RequestedRegion" = local.regions }
        }
      },
    ]
  })
}

output "template_buckets" {
  description = "Regional map passed to the publication workflow after account and bucket approval."
  value = tomap({
    us-east-1 = module.us_east_1.bucket_name
    us-east-2 = module.us_east_2.bucket_name
    us-west-1 = module.us_west_1.bucket_name
    us-west-2 = module.us_west_2.bucket_name
  })
}

output "candidate_template_role_arn" {
  description = "OIDC role that may stage versioned S3 templates in the RC workflow."
  value       = aws_iam_role.candidate.arn
}

output "stable_verify_role_arn" {
  description = "Read-only AWS OIDC role for stable promotion."
  value       = aws_iam_role.stable.arn
}
