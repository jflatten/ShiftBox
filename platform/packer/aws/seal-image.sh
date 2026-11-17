#!/bin/sh
set -eu

# Canonical's temporary SSH login is only used while Packer provisions the image.
rm -f /home/ubuntu/.ssh/authorized_keys /root/.ssh/authorized_keys
usermod -L ubuntu
rm -f /etc/sudoers.d/90-cloud-init-users /etc/ssh/ssh_host_* /etc/ssh/sshd_config.d/50-cloud-init.conf
systemctl mask ssh.service ssh.socket
cloud-init clean --logs --machine-id --seed

# Runtime never processes EC2 user data through cloud-init. A generic DHCP
# configuration replaces the builder-specific NIC configuration at next boot.
touch /etc/cloud/cloud-init.disabled
rm -f /etc/netplan/50-cloud-init.yaml
cat > /etc/netplan/90-shiftbox-ec2.yaml <<'NETPLAN'
network:
  version: 2
  renderer: networkd
  ethernets:
    workspace:
      match:
        name: "e*"
      dhcp4: true
NETPLAN
chmod 0600 /etc/netplan/90-shiftbox-ec2.yaml

# Block metadata before workloads start. The root boot agent is permitted;
# forwarded traffic and ordinary host users cannot request metadata tokens.
cat > /etc/nftables.conf <<'RULES'
table inet shiftbox_metadata {
    chain output {
        type filter hook output priority -150; policy accept;
        ip daddr 169.254.169.254 meta skuid != 0 drop
    }
    chain forward {
        type filter hook forward priority -150; policy accept;
        ip daddr 169.254.169.254 drop
    }
}
RULES
systemctl enable nftables.service
install -d -m 0755 /etc/systemd/system/shiftbox-agent.service.d /etc/systemd/system/docker.service.d
cat > /etc/systemd/system/shiftbox-agent.service.d/ec2.conf <<'AGENT'
[Unit]
Requires=nftables.service
After=nftables.service
[Service]
Environment=SHIFTBOX_EC2_ENROLLMENT=1
AGENT
cat > /etc/systemd/system/docker.service.d/metadata.conf <<'DOCKER'
[Unit]
Requires=nftables.service
After=nftables.service
DOCKER

rm -f /tmp/shiftbox-agent /tmp/shiftbox-agent.service /tmp/shiftbox-start-agent
rm -f /tmp/shiftbox-install-tools /tmp/shiftbox-code-server.env /tmp/shiftbox-guest-update.pub /tmp/shiftbox-seal-aws-image

# Packer stops the instance before taking the AMI snapshots.
sync
