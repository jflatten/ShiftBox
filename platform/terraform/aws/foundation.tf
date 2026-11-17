resource "aws_ecr_repository" "controller" {
  name                 = "${var.name}/controller"
  image_tag_mutability = "IMMUTABLE"
}

# The ECS host can be replaced without replacing the installation or PostgreSQL data.
resource "aws_ebs_volume" "data" {
  availability_zone = var.availability_zone
  encrypted         = true
  size              = var.data_volume_size_gib
  type              = "gp3"

  lifecycle {
    prevent_destroy = true
  }

  tags = { Name = "${var.name}-data" }
}
