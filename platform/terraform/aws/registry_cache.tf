variable "registry_cache_volume_size_gib" {
  description = "Optional size of an independent encrypted registry cache disk. Omit to use the existing controller storage layout."
  type        = number
  default     = null

  validation {
    condition     = var.registry_cache_volume_size_gib == null || try(var.registry_cache_volume_size_gib > 0 && floor(var.registry_cache_volume_size_gib) == var.registry_cache_volume_size_gib && var.ecs_host_ami_id != null, false)
    error_message = "The cache disk requires an ECS host and a positive whole GiB size."
  }
}

variable "registry_cache_mount_ready" {
  description = "Bind the dedicated disk into the controller after the operator has mounted and verified the expected EBS serial on the host."
  type        = bool
  default     = false

  validation {
    condition     = !var.registry_cache_mount_ready || (var.registry_cache_volume_size_gib != null && var.controller_image_digest != null)
    error_message = "Verify the optional cache disk host mount and publish the controller before enabling its cache bind."
  }
}

locals {
  registry_cache_mount_path = "/var/lib/shiftbox-registry-cache"
  registry_cache_task_mounts = var.registry_cache_mount_ready ? [
    { sourceVolume = "registry-cache", containerPath = local.registry_cache_mount_path },
  ] : []
}

resource "aws_ebs_volume" "registry_cache" {
  count             = var.registry_cache_volume_size_gib == null ? 0 : 1
  availability_zone = var.availability_zone
  size              = var.registry_cache_volume_size_gib
  type              = "gp3"
  encrypted         = true
  tags              = { Name = "${var.name}-registry-cache" }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_volume_attachment" "registry_cache" {
  count       = var.registry_cache_volume_size_gib == null ? 0 : 1
  device_name = "/dev/sdg"
  volume_id   = one(aws_ebs_volume.registry_cache).id
  instance_id = one(aws_instance.ecs_host).id

  # Detach only in planned maintenance after stopping cache writes and
  # unmounting the dedicated filesystem. Never force-detach a mounted disk.
  force_detach = false
}

output "registry_cache_volume_id" {
  description = "Independent cache disk ID to verify when preparing the ECS host mount."
  value       = one(aws_ebs_volume.registry_cache[*].id)
}

variable "registry_cache_bootstrap" {
  description = "Deliver the sealed-host boot-time cache mount for the dedicated volume."
  type        = bool
  default     = false

  validation {
    condition     = !var.registry_cache_bootstrap || (var.registry_cache_volume_size_gib != null && !var.initialize_data_volume)
    error_message = "Cache bootstrap requires a dedicated volume and must not initialize the shared application data disk."
  }
}

variable "registry_cache_postgres_enabled" {
  description = "Quiesce the existing PostgreSQL ECS service during approved host maintenance."
  type        = bool
  default     = true
}
