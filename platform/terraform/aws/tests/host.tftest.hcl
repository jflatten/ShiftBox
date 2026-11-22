mock_provider "aws" {
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
}

variables {
  name                = "shiftbox-host-test"
  aws_region          = "us-east-1"
  availability_zone   = "us-east-1a"
  vpc_cidr            = "10.76.0.0/16"
  operator_ipv4_cidrs = ["10.20.0.0/24"]
  ecs_host_ami_id     = "ami-00112233445566778"
}

# Verify the single ECS host is private and a separate attachment stops the host before detaching data.
run "private_host_with_durable_data" {
  command = plan

  assert {
    condition = (
      length(aws_instance.ecs_host) == 1 &&
      !aws_instance.ecs_host[0].associate_public_ip_address &&
      aws_instance.ecs_host[0].metadata_options[0].http_tokens == "required" &&
      !var.initialize_data_volume &&
      length(aws_volume_attachment.data) == 1 &&
      aws_volume_attachment.data[0].stop_instance_before_detaching
    )
    error_message = "The ECS host must be private, require IMDSv2, and stop before its independent data disk is detached."
  }
  # Tag the root device independently of the persistent attachment.
  assert {
    condition     = one(aws_instance.ecs_host[0].root_block_device).tags["cluster_name"] == var.name && one(aws_instance.ecs_host[0].root_block_device).tags["project"] == "shiftbox"
    error_message = "The root disk must carry the deployment tags."
  }
  # The ECS registration is a separate taggable resource from the EC2 host.
  assert {
    condition = strcontains(templatefile("${path.module}/host/user-data.sh.tftpl", {
      cluster_name = var.name, volume_serial = "fixture", initialize = false, mount_script = ""
    }), "ECS_CONTAINER_INSTANCE_TAGS=${jsonencode({ cluster_name = var.name, project = "shiftbox" })}")
    error_message = "The ECS agent must tag its container instance when it registers."
  }
}
