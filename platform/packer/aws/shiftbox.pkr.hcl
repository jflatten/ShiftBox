packer {
  required_version = "= 1.14.2"
  required_plugins {
    amazon = {
      source  = "github.com/hashicorp/amazon"
      version = "= 1.8.4"
    }
  }
}

variable "region" {
  type = string
}

variable "source_ami" {
  type = string
  validation {
    condition     = can(regex("^ami-([0-9a-f]{8}|[0-9a-f]{17})$", var.source_ami))
    error_message = "The source_ami must be a pinned AMI ID."
  }
}

variable "public_build" {
  type    = bool
  default = false
}

variable "publisher_account_id" {
  type    = string
  default = ""
  validation {
    condition     = var.publisher_account_id == "" || can(regex("^[0-9]{12}$", var.publisher_account_id))
    error_message = "The publisher_account_id must be an approved 12-digit AWS account ID."
  }
}

variable "build_subnet_id" {
  type = string
  validation {
    condition     = can(regex("^subnet-([0-9a-f]{8}|[0-9a-f]{17})$", var.build_subnet_id))
    error_message = "The build_subnet_id must be an explicit subnet ID."
  }
}

variable "build_security_group_id" {
  type = string
  validation {
    condition     = can(regex("^sg-([0-9a-f]{8}|[0-9a-f]{17})$", var.build_security_group_id))
    error_message = "The build_security_group_id must be an explicit security group ID."
  }
}

variable "cluster_name" {
  type = string
}

variable "environment" {
  type    = string
  default = "private"
}

variable "publisher_attempt" {
  type    = string
  default = ""
  validation {
    condition     = var.publisher_attempt == "" || can(regex("^[a-z0-9][a-z0-9-]{0,17}$", var.publisher_attempt))
    error_message = "A publisher attempt must be a 1–18 character lower-case attempt ID."
  }
}

variable "build_key_pair" {
  type = string
}

variable "build_private_key_file" {
  type      = string
  sensitive = true
}

variable "build_ssh_proxy_host" {
  type    = string
  default = ""
}

variable "build_ssh_proxy_port" {
  type    = number
  default = 1080
}

variable "release_id" {
  type = string
  validation {
    condition     = can(regex("^[0-9A-Za-z._-]{1,96}$", var.release_id))
    error_message = "The release_id must contain 1 to 96 letters, digits, dots, underscores or hyphens."
  }
}

variable "ami_name" {
  type    = string
  default = null
}

variable "guest_agent_release" {
  type    = string
  default = null
  validation {
    condition     = var.guest_agent_release == null || can(regex("^[0-9A-Za-z._-]{1,96}$", var.guest_agent_release))
    error_message = "The guest_agent_release must contain 1 to 96 letters, digits, dots, underscores or hyphens."
  }
}

variable "source_revision" {
  type = string
}

variable "agent_binary" {
  type = string
}

variable "agent_sha256" {
  type = string
}

variable "guest_update_public_key" {
  type = string
}

variable "guest_update_key_sha256" {
  type = string
}

variable "manifest_path" {
  type = string
}

locals {
  canonical_account = "099720109477"
  root_disk_gib     = 32
  rehearsal         = startswith(var.publisher_attempt, "rehearsal-")
  tags = merge({
    project      = "shiftbox"
    cluster_name = var.cluster_name
    environment  = var.environment
  }, var.publisher_attempt == "" ? {} : { publisher_attempt = var.publisher_attempt })
}

source "amazon-ebs" "shiftbox" {
  region                       = var.region
  source_ami                   = var.source_ami
  ami_name                     = local.rehearsal ? "shiftbox-${var.publisher_attempt}-amd64" : coalesce(var.ami_name, "shiftbox-${var.release_id}-amd64")
  ami_description              = local.rehearsal ? "ShiftBox Packer rehearsal ${var.publisher_attempt}" : "ShiftBox workspace platform ${var.release_id} on Ubuntu 24.04 Minimal amd64"
  instance_type                = "t3.medium"
  subnet_id                    = var.build_subnet_id
  security_group_id            = var.build_security_group_id
  associate_public_ip_address  = false
  ssh_interface                = "private_ip"
  ssh_username                 = "ubuntu"
  ssh_keypair_name             = var.build_key_pair
  ssh_private_key_file         = var.build_private_key_file
  ssh_proxy_host               = var.build_ssh_proxy_host
  ssh_proxy_port               = var.build_ssh_proxy_port
  ssh_disable_agent_forwarding = var.public_build
  ssh_timeout                  = "20m"
  imds_support                 = "v2.0"
  user_data_file               = var.public_build ? "${path.root}/apt-https.cloud-config.yaml" : ""

  # The pinned regional AMI must also satisfy the publisher and platform filters.
  source_ami_filter {
    owners = [local.canonical_account]
    filters = {
      "architecture"        = "x86_64"
      "name"                = "ubuntu-minimal/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-minimal-*"
      "root-device-name"    = "/dev/sda1"
      "root-device-type"    = "ebs"
      "state"               = "available"
      "virtualization-type" = "hvm"
    }
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  launch_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = local.root_disk_gib
    volume_type           = "gp3"
    encrypted             = !var.public_build
    delete_on_termination = true
  }

  run_tags        = local.tags
  run_volume_tags = local.tags
  snapshot_tags   = local.tags
  tags = merge(local.tags, {
    ShiftBoxArtifact     = local.rehearsal ? "packer-rehearsal" : "workspace-ami"
    ShiftBoxRelease      = local.rehearsal ? var.publisher_attempt : var.release_id
    ShiftBoxSourceAMI    = var.source_ami
    ShiftBoxSourceCommit = var.source_revision
  })
}

build {
  sources = ["source.amazon-ebs.shiftbox"]

  provisioner "file" {
    source      = var.agent_binary
    destination = "/tmp/shiftbox-agent"
  }

  provisioner "file" {
    source      = "${path.root}/../../guest/shiftbox-agent.service"
    destination = "/tmp/shiftbox-agent.service"
  }

  provisioner "file" {
    source      = "${path.root}/../../guest/shiftbox-start-agent"
    destination = "/tmp/shiftbox-start-agent"
  }

  provisioner "file" {
    source      = "${path.root}/../install-toolchain.sh"
    destination = "/tmp/shiftbox-install-tools"
  }

  provisioner "file" {
    source      = "${path.root}/../extend-devcontainer-cli.cjs"
    destination = "/tmp/shiftbox-extend-devcontainer-cli.cjs"
  }

  provisioner "file" {
    source      = "${path.root}/../code-server.env"
    destination = "/tmp/shiftbox-code-server.env"
  }

  provisioner "file" {
    source      = "${path.root}/seal-image.sh"
    destination = "/tmp/shiftbox-seal-aws-image"
  }

  provisioner "file" {
    source      = var.guest_update_public_key
    destination = "/tmp/shiftbox-guest-update.pub"
  }

  provisioner "shell" {
    inline = compact([
      var.public_build ? "echo 'ShiftBox Packer checkpoint: guest-bootstrap'" : "",
      "cloud-init status --wait",
      var.public_build ? "echo 'ShiftBox Packer checkpoint: guest-toolchain'" : "",
      "sudo /bin/sh /tmp/shiftbox-install-tools amd64",
      "sudo apt-get install -y --no-install-recommends nftables",
      var.public_build ? "echo 'ShiftBox Packer checkpoint: guest-agent'" : "",
      "sudo install -m 0755 /tmp/shiftbox-agent /usr/local/bin/shiftbox-agent",
      "sudo install -D -o root -g root -m 0644 /tmp/shiftbox-guest-update.pub /usr/local/share/shiftbox/guest-update.pub",
      "sudo ln -s /usr/local/bin/shiftbox-agent /usr/local/bin/shiftbox",
      "sudo install -D -m 0755 /tmp/shiftbox-start-agent /usr/local/libexec/shiftbox-start-agent",
      "sudo install -m 0644 /tmp/shiftbox-agent.service /etc/systemd/system/shiftbox-agent.service",
      "sudo systemctl enable shiftbox-agent.service",
      var.public_build ? "echo 'ShiftBox Packer checkpoint: guest-seal'" : "",
      "sudo /bin/sh /tmp/shiftbox-seal-aws-image ${var.public_build ? "public" : "private"}",
      var.public_build ? "echo 'ShiftBox Packer checkpoint: ami-creation'" : "",
    ])
  }

  post-processor "manifest" {
    output = var.manifest_path
    custom_data = {
      provider             = "ec2"
      releaseId            = var.release_id
      region               = var.region
      sourceRegion         = var.region
      publicBuild          = var.public_build ? "true" : "false"
      publisherAccountId   = var.publisher_account_id
      sourceAmiId          = var.source_ami
      sourceAmiOwnerId     = local.canonical_account
      sourceRevision       = var.source_revision
      architecture         = "amd64"
      platformVersion      = "24.04"
      protocolVersion      = "1"
      minimumRootDiskGiB   = format("%d", local.root_disk_gib)
      guestAgentRelease    = coalesce(var.guest_agent_release, var.release_id)
      guestAgentSHA256     = var.agent_sha256
      guestUpdateKeySHA256 = var.guest_update_key_sha256
      toolchainRecipeSHA256 = sha256(join(":", [
        sha256(file("${path.root}/../install-toolchain.sh")),
        sha256(file("${path.root}/../extend-devcontainer-cli.cjs")),
      ]))
      codeServerRecipeSHA256 = sha256(file("${path.root}/../code-server.env"))
    }
  }
}
