locals {
  # Keep a delegated inline policy inside the exact attempt namespace even if
  # its author attempted to omit the narrower ARN and time conditions.
  expiry_attempt_variable    = "$${aws:PrincipalTag/publisher_attempt}"
  expiry_lambda_principal    = "arn:aws:iam::${var.publisher_account_id}:role/shiftbox-publisher-${local.expiry_attempt_variable}-expiry"
  expiry_scheduler_principal = "${local.expiry_lambda_principal}-invoke"
  expiry_attempt_function    = "arn:aws:lambda:${local.scope}:function:shiftbox-publisher-${local.expiry_attempt_variable}-expiry"
  expiry_attempt_log         = "arn:aws:logs:${local.scope}:log-group:/aws/lambda/shiftbox-publisher-${local.expiry_attempt_variable}-expiry:log-stream:*"
  expiry_lambda_caller = {
    ArnEquals = {
      "aws:PrincipalArn"         = local.expiry_lambda_principal
      "lambda:SourceFunctionArn" = local.expiry_attempt_function
    }
    StringEquals = local.region_condition.StringEquals
  }
  expiry_owned_resource = merge(local.expiry_lambda_caller, {
    StringEquals = merge(local.expiry_lambda_caller.StringEquals, {
      "aws:ResourceTag/project"           = local.owned_network_tags.project
      "aws:ResourceTag/cluster_name"      = local.owned_network_tags.cluster_name
      "aws:ResourceTag/environment"       = "$${aws:PrincipalTag/environment}"
      "aws:ResourceTag/publisher_attempt" = local.expiry_attempt_variable
    })
  })
  expiry_boundary_statements = [
    {
      Sid       = "WriteOnlyOwnExpiryLogs"
      Effect    = "Allow"
      Action    = ["logs:CreateLogStream", "logs:PutLogEvents"]
      Resource  = local.expiry_attempt_log
      Condition = local.expiry_lambda_caller
    },
    {
      Sid    = "ReadOhioBillableResourceInventory"
      Effect = "Allow"
      Action = [
        "ec2:DescribeAddresses", "ec2:DescribeInstances", "ec2:DescribeNatGateways",
        "ec2:DescribeNetworkInterfaces", "ec2:DescribeSubnets", "ec2:DescribeVolumes", "ec2:DescribeVpcs",
      ]
      Resource  = "*"
      Condition = local.expiry_lambda_caller
    },
    {
      Sid       = "TerminateOnlyOwnTaggedBuilder"
      Effect    = "Allow"
      Action    = ["ec2:TerminateInstances"]
      Resource  = "${local.ec2}:instance/*"
      Condition = merge(local.expiry_owned_resource, { StringEquals = merge(local.expiry_owned_resource.StringEquals, { "ec2:InstanceType" = "t3.medium" }) })
    },
    {
      Sid       = "DeleteOnlyOwnBillableEgressAndStorage"
      Effect    = "Allow"
      Action    = ["ec2:DeleteNatGateway", "ec2:ReleaseAddress", "ec2:DeleteNetworkInterface", "ec2:DeleteVolume"]
      Resource  = [for kind in ["natgateway", "elastic-ip", "network-interface", "volume"] : "${local.ec2}:${kind}/*"]
      Condition = local.expiry_owned_resource
    },
    {
      Sid      = "SchedulerInvokesOnlyOwnExpiryFunction"
      Effect   = "Allow"
      Action   = ["lambda:InvokeFunction"]
      Resource = local.expiry_attempt_function
      Condition = {
        ArnEquals    = { "aws:PrincipalArn" = local.expiry_scheduler_principal }
        StringEquals = local.region_condition.StringEquals
      }
    },
  ]
}

resource "aws_iam_policy" "expiry_boundary" {
  name        = local.expiry_policy_name
  description = "Maximum authority for an automated ShiftBox one-attempt network expiry role."
  tags        = local.tags
  policy      = jsonencode({ Version = "2012-10-17", Statement = local.expiry_boundary_statements })
}

output "release_expiry_permissions_boundary_arn" {
  description = "Required authorization ceiling on disposable expiry identities."
  value       = aws_iam_policy.expiry_boundary.arn
}
