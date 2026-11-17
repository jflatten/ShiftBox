resource "aws_vpc" "shiftbox" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = var.name }
}

locals {
  vpc_generated_resources = {
    default_acl   = aws_vpc.shiftbox.default_network_acl_id
    default_group = aws_vpc.shiftbox.default_security_group_id
    default_route = aws_vpc.shiftbox.default_route_table_id
  }
}

# Apply deployment tags without adopting or changing the AWS-generated defaults.
resource "aws_ec2_tag" "vpc_generated" {
  for_each = {
    for pair in setproduct(keys(local.vpc_generated_resources), ["project", "cluster_name"]) :
    "${pair[0]}:${pair[1]}" => pair
  }
  resource_id = local.vpc_generated_resources[each.value[0]]
  key         = each.value[1]
  value       = each.value[1] == "project" ? "shiftbox" : var.name
}

resource "aws_ec2_tag" "nat_interface" {
  for_each    = var.create_nat_gateway ? { project = "shiftbox", cluster_name = var.name } : {}
  resource_id = aws_nat_gateway.egress[0].network_interface_id
  key         = each.key
  value       = each.value
}

resource "aws_subnet" "services" {
  vpc_id                  = aws_vpc.shiftbox.id
  availability_zone       = var.availability_zone
  cidr_block              = cidrsubnet(var.vpc_cidr, 4, 0)
  map_public_ip_on_launch = false

  tags = { Name = "${var.name}-services" }
}

resource "aws_subnet" "workspaces" {
  vpc_id                  = aws_vpc.shiftbox.id
  availability_zone       = var.availability_zone
  cidr_block              = cidrsubnet(var.vpc_cidr, 4, 1)
  map_public_ip_on_launch = false

  tags = { Name = "${var.name}-workspaces" }
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.shiftbox.id

  tags = { Name = "${var.name}-private" }
}

resource "aws_route_table_association" "services" {
  subnet_id      = aws_subnet.services.id
  route_table_id = aws_route_table.private.id
}

resource "aws_route_table_association" "workspaces" {
  subnet_id      = aws_subnet.workspaces.id
  route_table_id = aws_route_table.private.id
}

# The public subnet only hosts the NAT gateway. Both private subnets use one gateway to share its cost.
resource "aws_subnet" "egress" {
  count = var.create_nat_gateway ? 1 : 0

  vpc_id                  = aws_vpc.shiftbox.id
  availability_zone       = var.availability_zone
  cidr_block              = cidrsubnet(var.vpc_cidr, 4, 2)
  map_public_ip_on_launch = false

  tags = { Name = "${var.name}-egress" }
}

resource "aws_internet_gateway" "egress" {
  count = var.create_nat_gateway ? 1 : 0

  vpc_id = aws_vpc.shiftbox.id

  tags = { Name = "${var.name}-egress" }
}

resource "aws_route_table" "public" {
  count = var.create_nat_gateway ? 1 : 0

  vpc_id = aws_vpc.shiftbox.id

  tags = { Name = "${var.name}-egress" }
}

resource "aws_route" "public_internet" {
  count = var.create_nat_gateway ? 1 : 0

  route_table_id         = aws_route_table.public[0].id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.egress[0].id
}

resource "aws_route_table_association" "egress" {
  count = var.create_nat_gateway ? 1 : 0

  subnet_id      = aws_subnet.egress[0].id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_eip" "nat" {
  count = var.create_nat_gateway ? 1 : 0

  domain = "vpc"

  tags = { Name = "${var.name}-nat" }
}

resource "aws_nat_gateway" "egress" {
  count = var.create_nat_gateway ? 1 : 0

  connectivity_type = "public"
  subnet_id         = aws_subnet.egress[0].id
  allocation_id     = aws_eip.nat[0].id

  depends_on = [aws_route.public_internet, aws_route_table_association.egress]

  tags = { Name = "${var.name}-nat" }
}

# With deployer-managed egress, the deployer adds the outbound route to this private table.
resource "aws_route" "private_internet" {
  count = var.create_nat_gateway ? 1 : 0

  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.egress[0].id
}
