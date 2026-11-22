variables {
  testing_account_id   = "111111111111"
  publisher_account_id = "222222222222"
  run_name             = "sbq-123456789-2"
  host_ami_id          = "ami-0123456789abcdef0"
  router_ami_id        = "ami-abcdef01234567890"
}

# The draft must stay runnable without an AWS login and fit IAM attachment limits.
run "review_artifacts_fit_iam_limits" {
  command = plan

  assert {
    condition     = length(output.policy_documents) <= 10 && alltrue([for policy in values(output.policy_documents) : length(policy) <= 6144])
    error_message = "The separate policies must fit IAM per-policy and default per-role attachment quotas."
  }

  assert {
    condition = alltrue(flatten([
      for policy in values(output.policy_documents) : [
        for statement in jsondecode(policy).Statement : alltrue([
          for action in statement.Action : !strcontains(action, "*") && !contains([
            "iam:UpdateAssumeRolePolicy", "iam:DeleteRolePermissionsBoundary", "iam:PutRolePermissionsBoundary",
            "iam:CreatePolicy", "iam:CreatePolicyVersion", "iam:SetDefaultPolicyVersion",
            "secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue", "secretsmanager:DeleteSecret"
          ], action)
        ])
      ]
    ]))
    error_message = "Execution cannot widen role trust or boundaries, mutate customer-managed policies, or access retained secret values."
  }

  assert {
    condition     = !contains(keys(output.policy_documents), "dns") && !contains(keys(output.policy_documents), "discovery")
    error_message = "DNS and discovery must stay closed until matching physical resources are independently verified."
  }
}

# Each role must carry its own fixed boundary, even when an inline policy is replaced.
run "identity_roles_have_fixed_bounds" {
  command = plan

  assert {
    condition = alltrue([
      for statement in concat(jsondecode(output.policy_documents.identity).Statement, jsondecode(output.policy_documents.inline).Statement) :
      length(statement.Resource) == 1 &&
      endswith(statement.Condition.StringEquals["iam:PermissionsBoundary"], split("/", statement.Resource[0])[3])
      if contains(statement.Action, "iam:CreateRole") || contains(statement.Action, "iam:PutRolePolicy")
      ]) && length([
      for statement in jsondecode(output.policy_documents.identity).Statement : statement
      if contains(statement.Action, "iam:CreateRole")
    ]) == 7
    error_message = "Creation and inline writes require reviewed, exact per-role boundaries."
  }

  assert {
    condition = toset(flatten([
      for policy in values(output.policy_documents) : [
        for statement in jsondecode(policy).Statement : statement.Condition.StringEquals["iam:PassedToService"]
        if contains(statement.Action, "iam:PassRole")
      ]
    ])) == toset(["ec2.amazonaws.com", "ecs-tasks.amazonaws.com"])
    error_message = "Instance and task PassRole permissions must stay separated by destination service."
  }
}

# Independently verified DNS resources must never activate wildcard record writes or zone deletion.
run "verified_dns_targets_stay_exact" {
  command = plan

  variables {
    verified_vpc_id                = "vpc-0123456789abcdef0"
    verified_private_zone_id       = "Z12345ABCDE"
    verified_discovery_zone_id     = "Z98765ZYXWV"
    verified_namespace_id          = "ns-abcdefghijklmnop"
    verified_discovery_service_ids = ["srv-abcdefghijklmnop", "srv-0123456789abcdef"]
  }

  assert {
    condition = alltrue([
      for statement in jsondecode(output.policy_documents.dns).Statement :
      statement.Resource == ["arn:aws:route53:::hostedzone/Z12345ABCDE"] &&
      length(statement.Condition["ForAllValues:StringEquals"]["route53:ChangeResourceRecordSetsNormalizedRecordNames"]) == 6
      if contains(statement.Action, "route53:ChangeResourceRecordSets")
    ])
    error_message = "Record writes must target the verified private zone and only the six approved names."
  }

  assert {
    condition = alltrue([
      for statement in jsondecode(output.policy_documents.dns).Statement :
      statement.Condition.StringEquals["route53:VPCs"] == "VPCId=${var.verified_vpc_id},VPCRegion=us-east-2"
      if contains(statement.Action, "route53:CreateHostedZone")
      ]) && alltrue([
      for statement in jsondecode(output.policy_documents.discovery).Statement :
      alltrue([for resource in statement.Resource : !strcontains(resource, "*")])
      if anytrue([for action in statement.Action : contains([
        "servicediscovery:ListTagsForResource", "servicediscovery:TagResource", "servicediscovery:UpdateService", "servicediscovery:DeleteService"
      ], action)])
    ])
    error_message = "Zone creation must require the exact VPC, and discovery tagging and service mutations require exact physical IDs."
  }

  assert {
    condition = var.verified_private_zone_id != var.verified_discovery_zone_id && alltrue([
      for statement in jsondecode(output.policy_documents.dns).Statement : !contains(statement.Resource, "*")
      if contains(statement.Action, "route53:DeleteHostedZone")
    ])
    error_message = "DNS zone deletes must target verified zone IDs."
  }
}

# A second run's resources, a publisher account and arbitrary AMI selectors cannot become policy inputs.
run "rejects_wrong_account" {
  command = plan

  variables {
    testing_account_id = "222222222222"
  }

  expect_failures = [var.testing_account_id]
}

# The ownership tag and generated names must name a specific GitHub run attempt.
run "rejects_broad_run_selector" {
  command = plan

  variables {
    run_name = "sbq-*-*"
  }

  expect_failures = [var.run_name]
}

# Both EC2 launch images must be exact, including the router's resolved public parameter.
run "rejects_unpinned_ami" {
  command = plan

  variables {
    router_ami_id = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
  }

  expect_failures = [var.router_ami_id]
}

# Policy size enforcement must also hold for the longest permitted run name and the optional DNS documents.
run "maximum_input_sizes_fit_iam_limits" {
  command = plan

  variables {
    run_name                       = "sbq-1234567890123456-9999"
    verified_vpc_id                = "vpc-0123456789abcdef0"
    verified_private_zone_id       = "ZABCDEFGHIJKLMNOPQRSTUVWXY1234567"
    verified_discovery_zone_id     = "ZABCDEFGHIJKLMNOPQRSTUVWXY1234568"
    verified_namespace_id          = "ns-abcdefghijklmnop"
    verified_discovery_service_ids = ["srv-abcdefghijklmnop", "srv-0123456789abcdef"]
  }

  assert {
    condition     = length(output.policy_documents) == 10 && alltrue([for policy in values(output.policy_documents) : length(policy) <= 6144])
    error_message = "Fully rendered documents fit IAM quotas even with the maximum input lengths."
  }
}
