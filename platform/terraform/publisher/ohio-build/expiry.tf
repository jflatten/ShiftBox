locals {
  expiry_name         = "${local.name}-expiry"
  expiry_group_name   = "${local.expiry_name}-group"
  expiry_function_arn = "arn:aws:lambda:${local.region}:${local.publisher_account_id}:function:${local.expiry_name}"
  expiry_ec2_arn      = "arn:aws:ec2:${local.region}:${local.publisher_account_id}"
  expiry_deadline     = timeadd(var.expires_at, "35m")
  expiry_offsets      = { first = "0m", second = "4m", third = "12m", fourth = "24m", final = "32m" }

  expiry_exact_tags = {
    for key in ["project", "cluster_name", "environment", "publisher_attempt"] :
    "aws:ResourceTag/${key}" => local.tags[key]
  }
  expiry_time_condition = {
    ArnEquals             = { "lambda:SourceFunctionArn" = local.expiry_function_arn }
    DateGreaterThanEquals = { "aws:CurrentTime" = var.expires_at }
    DateLessThan          = { "aws:CurrentTime" = local.expiry_deadline }
  }
}

resource "aws_cloudwatch_log_group" "expiry" {
  name              = "/aws/lambda/${local.expiry_name}"
  retention_in_days = 7
  tags              = local.tags
}

resource "aws_iam_role" "expiry" {
  name = local.expiry_name
  tags = local.tags

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "expiry" {
  name = "ExpireSinglePublisherAttempt"
  role = aws_iam_role.expiry.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "WriteOnlyExpiryLogs"
        Effect    = "Allow"
        Action    = ["logs:CreateLogStream", "logs:PutLogEvents"]
        Resource  = "${aws_cloudwatch_log_group.expiry.arn}:log-stream:*"
        Condition = { ArnEquals = { "lambda:SourceFunctionArn" = local.expiry_function_arn } }
      },
      {
        Sid    = "ReadExpiryInventoryInOhio"
        Effect = "Allow"
        Action = [
          "ec2:DescribeAddresses", "ec2:DescribeInstances", "ec2:DescribeNatGateways",
          "ec2:DescribeNetworkInterfaces", "ec2:DescribeSubnets", "ec2:DescribeVolumes", "ec2:DescribeVpcs"
        ]
        Resource = "*"
        Condition = merge(local.expiry_time_condition, {
          StringEquals = { "aws:RequestedRegion" = local.region }
        })
      },
      {
        Sid      = "TerminateTaggedPackerInstances"
        Effect   = "Allow"
        Action   = "ec2:TerminateInstances"
        Resource = "${local.expiry_ec2_arn}:instance/*"
        Condition = merge(local.expiry_time_condition, {
          StringEquals = merge(local.expiry_exact_tags, {
            "aws:RequestedRegion"  = local.region
            "ec2:AvailabilityZone" = aws_subnet.private.availability_zone
            "ec2:InstanceType"     = "t3.medium"
          })
        })
      },
      {
        Sid    = "ReleaseExactEgress"
        Effect = "Allow"
        Action = ["ec2:DeleteNatGateway", "ec2:ReleaseAddress"]
        Resource = [
          "${local.expiry_ec2_arn}:natgateway/${aws_nat_gateway.build.id}",
          "${local.expiry_ec2_arn}:elastic-ip/${aws_eip.nat.id}",
        ]
        Condition = merge(local.expiry_time_condition, {
          StringEquals = merge(local.expiry_exact_tags, { "aws:RequestedRegion" = local.region })
        })
      },
      {
        Sid      = "DeleteTaggedPrivateDetachedInterfaces"
        Effect   = "Allow"
        Action   = "ec2:DeleteNetworkInterface"
        Resource = "${local.expiry_ec2_arn}:network-interface/*"
        Condition = merge(local.expiry_time_condition, {
          ArnEquals = merge(local.expiry_time_condition.ArnEquals, {
            "ec2:Subnet" = "${local.expiry_ec2_arn}:subnet/${aws_subnet.private.id}"
            "ec2:Vpc"    = "${local.expiry_ec2_arn}:vpc/${aws_vpc.build.id}"
          })
          StringEquals = merge(local.expiry_exact_tags, { "aws:RequestedRegion" = local.region })
        })
      },
      {
        Sid      = "DeleteOnlyExplicitlyAttemptTaggedVolumes"
        Effect   = "Allow"
        Action   = "ec2:DeleteVolume"
        Resource = "${local.expiry_ec2_arn}:volume/*"
        Condition = merge(local.expiry_time_condition, {
          StringEquals = merge(local.expiry_exact_tags, {
            "aws:RequestedRegion"  = local.region
            "ec2:AvailabilityZone" = aws_subnet.private.availability_zone
          })
        })
      }
    ]
  })
}

resource "aws_lambda_function" "expiry" {
  function_name    = local.expiry_name
  role             = aws_iam_role.expiry.arn
  runtime          = "python3.13"
  handler          = "expiry.handler"
  filename         = "${path.module}/expiry.zip"
  source_code_hash = filebase64sha256("${path.module}/expiry.zip")
  timeout          = 90
  tags             = local.tags

  environment {
    variables = {
      PUBLISHER_ACCOUNT         = local.publisher_account_id
      ATTEMPT_ID                = var.attempt_id
      CANDIDATE_TAG             = var.candidate_tag
      EXPIRES_AT                = var.expires_at
      EXPIRY_FUNCTION_ARN       = local.expiry_function_arn
      VPC_ID                    = aws_vpc.build.id
      PRIVATE_SUBNET_ID         = aws_subnet.private.id
      BUILDER_SECURITY_GROUP_ID = aws_security_group.builder.id
      EGRESS_SUBNET_ID          = aws_subnet.public.id
      AVAILABILITY_ZONE         = aws_subnet.private.availability_zone
      NAT_ID                    = aws_nat_gateway.build.id
      ALLOCATION_ID             = aws_eip.nat.id
    }
  }

  depends_on = [aws_iam_role_policy.expiry]
}

resource "aws_lambda_function_event_invoke_config" "expiry" {
  function_name                = aws_lambda_function.expiry.function_name
  maximum_event_age_in_seconds = 60
  maximum_retry_attempts       = 0
}

resource "aws_scheduler_schedule_group" "expiry" {
  name = local.expiry_group_name
  tags = local.tags
}

resource "aws_iam_role" "expiry_scheduler" {
  name = "${local.expiry_name}-invoke"
  tags = local.tags

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "scheduler.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = local.publisher_account_id }
        ArnEquals    = { "aws:SourceArn" = aws_scheduler_schedule_group.expiry.arn }
      }
    }]
  })
}

resource "aws_iam_role_policy" "expiry_scheduler" {
  name = "InvokeExactPublisherExpiry"
  role = aws_iam_role.expiry_scheduler.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "lambda:InvokeFunction"
      Resource = aws_lambda_function.expiry.arn
    }]
  })
}

resource "aws_scheduler_schedule" "expiry" {
  for_each = local.expiry_offsets

  name                         = each.key
  group_name                   = aws_scheduler_schedule_group.expiry.name
  schedule_expression          = "at(${trimsuffix(timeadd(var.expires_at, each.value), "Z")})"
  schedule_expression_timezone = "UTC"
  action_after_completion      = "DELETE"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.expiry.arn
    role_arn = aws_iam_role.expiry_scheduler.arn
    input    = jsonencode({ attempt = var.attempt_id, final = each.key == "final" })

    retry_policy {
      maximum_event_age_in_seconds = 60
      maximum_retry_attempts       = 1
    }
  }

  depends_on = [aws_iam_role_policy.expiry_scheduler, aws_lambda_function_event_invoke_config.expiry]
}
