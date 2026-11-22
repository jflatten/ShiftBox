mock_provider "aws" {
  override_during = plan
}

override_resource {
  target          = aws_security_group.builder
  override_during = plan
  values          = { id = "sg-00000000000000001" }
}

override_resource {
  target          = aws_security_group.endpoint
  override_during = plan
  values          = { id = "sg-00000000000000002" }
}

override_resource {
  target          = aws_ec2_instance_connect_endpoint.builder
  override_during = plan
  values = {
    arn = "arn:aws:ec2:us-east-2:111122223333:instance-connect-endpoint/eice-00000000000000003"
  }
}

override_resource {
  target          = aws_subnet.private
  override_during = plan
  values = {
    id                = "subnet-00000000000000004"
    availability_zone = "us-east-2a"
  }
}

override_resource {
  target          = aws_nat_gateway.build
  override_during = plan
  values          = { id = "nat-00000000000000005" }
}

override_resource {
  target          = aws_eip.nat
  override_during = plan
  values          = { id = "eipalloc-00000000000000006" }
}

override_resource {
  target          = aws_vpc.build
  override_during = plan
  values          = { id = "vpc-00000000000000007" }
}

override_resource {
  target          = aws_cloudwatch_log_group.expiry
  override_during = plan
  values          = { arn = "arn:aws:logs:us-east-2:111122223333:log-group:/aws/lambda/shiftbox-publisher-fixture-expiry" }
}

variables {
  publisher_account_id = "111122223333"
  vpc_cidr             = "10.42.0.0/24"
  attempt_id           = "fixture"
  candidate_tag        = "v1.0.0-rc.1"
  availability_zone_id = "use2-az1"
  packer_public_key    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA fixture"
}

# Reject changes to the selected candidate or an Ohio-only availability zone.
run "wrong_target" {
  command = plan
  variables {
    candidate_tag        = "v1.0.0"
    availability_zone_id = "use1-az1"
  }
  expect_failures = [var.candidate_tag, var.availability_zone_id]
}

# A stale deadline cannot justify any build-network resource.
run "past_deadline" {
  command = plan
  variables {
    expires_at = "2020-01-01T00:00:00Z"
  }
  expect_failures = [terraform_data.attempt_window]
}

# Addressing remains private and outbound traffic uses the dedicated guest NAT.
run "private_egress" {
  command = plan
  assert {
    condition = (
      local.vpc_cidr == "10.42.0.0/24" &&
      aws_subnet.private.cidr_block == "10.42.0.64/26" &&
      !aws_subnet.private.map_public_ip_on_launch &&
      !aws_subnet.public.map_public_ip_on_launch &&
      aws_route.private_default.destination_cidr_block == "0.0.0.0/0" &&
      aws_route.private_default.nat_gateway_id == aws_nat_gateway.build.id
    )
    error_message = "Only the disposable NAT may supply the private subnet's default route."
  }
  assert {
    condition = (
      aws_vpc_security_group_egress_rule.builder_https.from_port == 443 &&
      aws_vpc_security_group_egress_rule.builder_https.to_port == 443 &&
      length(aws_vpc_security_group_egress_rule.builder_http) == 0
    )
    error_message = "The builder's default package egress must be HTTPS-only."
  }
}

# No CIDR ingress or operator/runner hops are allowed between EICE and Packer.
run "direct_tunnel" {
  command = plan
  assert {
    condition = (
      aws_ec2_instance_connect_endpoint.builder.ip_address_type == "ipv4" &&
      !aws_ec2_instance_connect_endpoint.builder.preserve_client_ip &&
      aws_ec2_instance_connect_endpoint.builder.subnet_id == aws_subnet.private.id &&
      contains(aws_ec2_instance_connect_endpoint.builder.security_group_ids, aws_security_group.endpoint.id) &&
      aws_security_group.endpoint.id != aws_security_group.builder.id
    )
    error_message = "EICE must terminate inside the build subnet and preserve its own security-group identity."
  }
  assert {
    condition = (
      aws_vpc_security_group_egress_rule.endpoint_ssh.security_group_id == aws_security_group.endpoint.id &&
      aws_vpc_security_group_egress_rule.endpoint_ssh.referenced_security_group_id == aws_security_group.builder.id &&
      aws_vpc_security_group_egress_rule.endpoint_ssh.from_port == 22 &&
      aws_vpc_security_group_egress_rule.endpoint_ssh.to_port == 22 &&
      aws_vpc_security_group_egress_rule.endpoint_ssh.cidr_ipv4 == null &&
      aws_vpc_security_group_ingress_rule.builder_ssh.security_group_id == aws_security_group.builder.id &&
      aws_vpc_security_group_ingress_rule.builder_ssh.referenced_security_group_id == aws_security_group.endpoint.id &&
      aws_vpc_security_group_ingress_rule.builder_ssh.from_port == 22 &&
      aws_vpc_security_group_ingress_rule.builder_ssh.to_port == 22 &&
      aws_vpc_security_group_ingress_rule.builder_ssh.cidr_ipv4 == null
    )
    error_message = "SSH egress and ingress must reference only each other on TCP 22."
  }
  assert {
    condition = (
      jsondecode(output.build_connection_policy).Statement[0].Action == "ec2-instance-connect:OpenTunnel" &&
      jsondecode(output.build_connection_policy).Statement[0].Resource == aws_ec2_instance_connect_endpoint.builder.arn &&
      jsondecode(output.build_connection_policy).Statement[0].Condition.IpAddress["ec2-instance-connect:privateIpAddress"] == "10.42.0.64/26" &&
      jsondecode(output.build_connection_policy).Statement[0].Condition.StringEquals["aws:ResourceTag/publisher_attempt"] == "fixture" &&
      jsondecode(output.build_connection_policy).Statement[0].Condition.NumericEquals["ec2-instance-connect:remotePort"] == 22 &&
      jsondecode(output.build_connection_policy).Statement[0].Condition.NumericLessThanEquals["ec2-instance-connect:maxTunnelDuration"] == 2700 &&
      length(jsondecode(output.build_connection_policy).Statement) == 1
    )
    error_message = "Only the reviewed endpoint, builder CIDR, SSH port and bounded tunnel may be rendered."
  }
}

# Signed Ubuntu APT using HTTP requires an explicit builder-only compatibility input.
run "opt_in_apt_egress" {
  command = plan
  variables {
    allow_builder_http = true
  }
  assert {
    condition = (
      aws_vpc_security_group_egress_rule.builder_http[0].security_group_id == aws_security_group.builder.id &&
      aws_vpc_security_group_egress_rule.builder_http[0].from_port == 80 &&
      aws_vpc_security_group_egress_rule.builder_http[0].to_port == 80
    )
    error_message = "Only the documented builder APT exception can open outbound TCP 80."
  }
}

# Expiry can mutate only attempt-tagged transient resources within the review's time window.
run "scoped_expiry" {
  command = plan
  assert {
    condition = toset(flatten([
      for statement in jsondecode(aws_iam_role_policy.expiry.policy).Statement : [statement.Action]
      if startswith(statement.Sid, "Terminate") ||
      startswith(statement.Sid, "Delete") || startswith(statement.Sid, "Release")
      ])) == toset([
      "ec2:TerminateInstances", "ec2:DeleteNatGateway", "ec2:ReleaseAddress",
      "ec2:DeleteNetworkInterface", "ec2:DeleteVolume",
    ])
    error_message = "Keep the expiry mutation list limited to disposable compute and egress."
  }
  assert {
    condition = alltrue([
      for statement in jsondecode(aws_iam_role_policy.expiry.policy).Statement :
      statement.Condition.StringEquals["aws:ResourceTag/publisher_attempt"] == "fixture" &&
      statement.Condition.DateGreaterThanEquals["aws:CurrentTime"] == var.expires_at &&
      statement.Condition.DateLessThan["aws:CurrentTime"] == timeadd(var.expires_at, "35m")
      if startswith(statement.Sid, "Terminate") ||
      startswith(statement.Sid, "Delete") || startswith(statement.Sid, "Release")
    ])
    error_message = "Every expiry mutation requires the exact attempt ownership tag and date fences."
  }
}

# Reject malformed account IDs and networks outside the reviewed private IPv4 shape.
run "invalid_scope" {
  command = plan
  variables {
    publisher_account_id = "unconfirmed"
    vpc_cidr             = "203.0.113.0/24"
  }
  expect_failures = [var.publisher_account_id, var.vpc_cidr]
}
