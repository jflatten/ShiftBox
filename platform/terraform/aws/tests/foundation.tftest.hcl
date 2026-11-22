mock_provider "aws" {
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
}

variables {
  name                = "shiftbox-test"
  aws_region          = "us-east-1"
  availability_zone   = "us-east-1a"
  vpc_cidr            = "10.76.0.0/16"
  operator_ipv4_cidrs = ["10.20.0.0/24"]
}

# Verify that the controller and guests stay private while sharing one outbound path.
run "private_subnets_with_managed_egress" {
  command = plan

  assert {
    condition = (
      !aws_subnet.services.map_public_ip_on_launch &&
      !aws_subnet.workspaces.map_public_ip_on_launch &&
      aws_subnet.services.availability_zone == var.availability_zone &&
      aws_subnet.workspaces.availability_zone == var.availability_zone &&
      aws_subnet.services.cidr_block != aws_subnet.workspaces.cidr_block
    )
    error_message = "Controller and workspace subnets must be distinct private subnets in the selected zone."
  }

  assert {
    condition = (
      length(aws_nat_gateway.egress) == 1 &&
      aws_route.private_internet[0].destination_cidr_block == "0.0.0.0/0" &&
      aws_route.public_internet[0].destination_cidr_block == "0.0.0.0/0"
    )
    error_message = "The private subnets must share one NAT path to the Internet gateway."
  }

  assert {
    condition = (
      aws_ebs_volume.data.encrypted &&
      aws_ebs_volume.data.type == "gp3" &&
      aws_ebs_volume.data.availability_zone == var.availability_zone &&
      aws_ecr_repository.controller.image_tag_mutability == "IMMUTABLE"
    )
    error_message = "Installation data must be encrypted in the selected zone and the controller repository must use immutable tags."
  }
}

# Verify that deployers can use their own outbound routing without paying for a second NAT gateway.
run "deployer_managed_egress" {
  command = plan

  variables {
    create_nat_gateway = false
  }

  assert {
    condition = (
      length(aws_nat_gateway.egress) == 0 &&
      length(aws_eip.nat) == 0 &&
      length(aws_internet_gateway.egress) == 0 &&
      length(aws_route.private_internet) == 0
    )
    error_message = "Disabling managed egress must leave private routing available without provisioning public infrastructure."
  }
}
