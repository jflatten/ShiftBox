locals {
  rehearsal_records = [
    for file in ["lease.json", "retired.json"] : "${local.state_arn}/${local.state_prefix}*/${file}"
  ]
  rehearsal_state_access = concat(
    [for grant in local.state_access : grant if !contains([
      "ReadImmutableAttemptRecords", "WriteNewRetirementRecord"
    ], grant.Sid)],
    [
      {
        Sid      = "ReadRehearsalRecords"
        Effect   = "Allow"
        Action   = ["s3:GetObject"]
        Resource = local.rehearsal_records
      },
      {
        Sid       = "WriteRehearsalRecords"
        Effect    = "Allow"
        Action    = ["s3:PutObject"]
        Resource  = local.rehearsal_records
        Condition = { StringEquals = { "s3:if-none-match" = "*" } }
      },
    ]
  )
  # Use the publisher's network grants with a disjoint resource name, tag and
  # journal scope. The rehearsal never needs candidate phase permissions.
  rehearsal_documents = {
    for name, statements in {
      ec2-create = local.network_provision_ec2_new
      ec2-config = local.network_provision_ec2_existing
      expiry     = local.network_provision_expiry
      state = concat(local.rehearsal_state_access, local.network_inventory, [
        for grant in local.network_retire : grant if try(grant.Sid, "") != "TerminateOnlyOwnedBuilders"
      ])
      } : name => replace(replace(replace(
        jsonencode({ Version = "2012-10-17", Statement = statements }),
        "release-builds", "release-builds/rehearsals"),
      "rel-??????????????", "rel-r?????????????"),
    "release-v*-rc.*", "release-v0.0.0-rc.1")
  }
}

resource "aws_iam_role" "rehearsal" {
  name                 = "shiftbox-publisher-release-network-rehearsal"
  description          = "Exercise disposable release networking from protected main."
  max_session_duration = 3600
  tags                 = local.tags
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = local.oidc_principal
      Condition = {
        StringEquals = {
          "${local.oidc}:aud"      = "sts.amazonaws.com"
          "${local.oidc}:sub"      = "${local.github_subject}:environment:aws-release-network-rehearsal"
          "${local.oidc}:actor_id" = "11561330"
          "${local.oidc}:ref"      = "refs/heads/main"
          "${local.oidc}:workflow" = "Rehearse publisher build network"
        }
        StringLike = { "sts:RoleSessionName" = "shiftbox-release-rehearsal-*-1" }
      }
    }]
  })
}

resource "aws_iam_role_policy" "rehearsal" {
  name   = "ManageShiftBoxNetworkRehearsalState"
  role   = aws_iam_role.rehearsal.name
  policy = local.rehearsal_documents.state
}

resource "aws_iam_policy" "rehearsal" {
  for_each = { for name, document in local.rehearsal_documents : name => document if name != "state" }
  name     = "shiftbox-publisher-network-rehearsal-${each.key}"
  tags     = local.tags
  policy   = each.value
}

resource "aws_iam_role_policy_attachment" "rehearsal" {
  for_each   = aws_iam_policy.rehearsal
  role       = aws_iam_role.rehearsal.name
  policy_arn = each.value.arn
}

output "release_network_rehearsal_role_arn" {
  description = "Protected main OIDC role for provisioning and retiring a rehearsal network."
  value       = aws_iam_role.rehearsal.arn
}
