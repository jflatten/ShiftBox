packer {
  required_version = "= 1.14.2"
  required_plugins {
    utm = {
      source  = "github.com/naveenrajm7/utm"
      version = "= 4.0.0"
    }
  }
}

variable "build_name" {
  type = string
}

variable "output_directory" {
  type = string
}

variable "build_public_key" {
  type = string
}

variable "build_private_key" {
  type      = string
  sensitive = true
}

variable "agent_binary" {
  type = string
}

source "utm-cloud" "shiftbox" {
  vm_name            = var.build_name
  vm_arch            = "x86_64"
  vm_backend         = "qemu"
  hypervisor         = false
  uefi_boot          = true
  cpus               = 2
  memory             = 4096
  disk_size          = 32768
  resize_cloud_image = true

  iso_url      = "https://cloud-images.ubuntu.com/minimal/releases/noble/release-20260716/ubuntu-24.04-minimal-cloudimg-amd64.img"
  iso_checksum = "sha256:d99d1abe3284e568161b3b7dabfbd6cf67956a7f4274b13842f10aa9c7807a2c"
  use_cd       = true
  cd_label     = "cidata"
  cd_content = {
    "meta-data"      = "instance-id: ${var.build_name}\nlocal-hostname: shiftbox-build\n"
    "network-config" = <<-NETWORK
      version: 2
      ethernets:
        build:
          match:
            name: "en*"
          dhcp4: true
          optional: true
    NETWORK
    "user-data"      = <<-SEED
      #cloud-config
      users:
        - name: shiftbox-build
          lock_passwd: true
          sudo: "ALL=(ALL) NOPASSWD:ALL"
          shell: /bin/bash
          ssh_authorized_keys:
            - ${jsonencode(var.build_public_key)}
      ssh_pwauth: false
    SEED
  }

  ssh_username         = "shiftbox-build"
  ssh_private_key_file = var.build_private_key
  ssh_timeout          = "20m"
  shutdown_command     = "sudo /bin/sh /tmp/shiftbox-seal-image"
  shutdown_timeout     = "5m"
  display_nopause      = true
  boot_nopause         = true
  export_nopause       = true
  output_directory     = var.output_directory
  output_filename      = "shiftbox"
}

build {
  sources = ["source.utm-cloud.shiftbox"]

  provisioner "file" {
    source      = var.agent_binary
    destination = "/tmp/shiftbox-agent"
  }

  provisioner "file" {
    source      = "${path.root}/../guest/shiftbox-agent.service"
    destination = "/tmp/shiftbox-agent.service"
  }

  provisioner "file" {
    source      = "${path.root}/../guest/shiftbox-start-agent"
    destination = "/tmp/shiftbox-start-agent"
  }

  provisioner "file" {
    source      = "${path.root}/seal-image.sh"
    destination = "/tmp/shiftbox-seal-image"
  }

  provisioner "file" {
    source      = "${path.root}/install-toolchain.sh"
    destination = "/tmp/shiftbox-install-toolchain"
  }

  provisioner "shell" {
    inline = [
      "cloud-init status --wait",
      "sudo /bin/sh /tmp/shiftbox-install-toolchain",
      "sudo install -m 0755 /tmp/shiftbox-agent /usr/local/bin/shiftbox-agent",
      "sudo ln -s /usr/local/bin/shiftbox-agent /usr/local/bin/shiftbox",
      "sudo install -D -m 0755 /tmp/shiftbox-start-agent /usr/local/libexec/shiftbox-start-agent",
      "sudo install -m 0644 /tmp/shiftbox-agent.service /etc/systemd/system/shiftbox-agent.service",
      "sudo systemctl enable shiftbox-agent.service",
    ]
  }
}
