output "vpc_id" {
  description = "VPC ID for deployer-managed private access."
  value       = aws_vpc.shiftbox.id
}

output "service_subnet_id" {
  description = "Private subnet for the ECS host and services."
  value       = aws_subnet.services.id
}

output "workspace_subnet_id" {
  description = "Private subnet for ShiftBox-managed EC2 workspace instances."
  value       = aws_subnet.workspaces.id
}

output "private_route_table_id" {
  description = "Shared private route table for deployer-managed private routing or outbound access."
  value       = aws_route_table.private.id
}

output "controller_repository_url" {
  description = "Private ECR repository URL for publishing the controller image."
  value       = aws_ecr_repository.controller.repository_url
}

output "data_volume_id" {
  description = "Encrypted persistent EBS volume ID for the controller and PostgreSQL."
  value       = aws_ebs_volume.data.id
}

output "workspace_security_group_id" {
  description = "Security group for the private EC2 workspace instances."
  value       = aws_security_group.workspace.id
}
