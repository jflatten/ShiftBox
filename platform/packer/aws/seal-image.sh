#!/bin/sh
set -eu

verify_public_root() {
    root=$1
    for path in /root/.aws /root/.ssh /root/.docker/config.json /root/.config/gh/hosts.yml \
        /root/.git-credentials /root/.netrc /root/.bash_history \
        /home/ubuntu/.aws /home/ubuntu/.ssh /home/ubuntu/.docker/config.json /home/ubuntu/.config/gh/hosts.yml \
        /home/ubuntu/.git-credentials /home/ubuntu/.netrc /home/ubuntu/.bash_history \
        /var/lib/cloud/instance /var/lib/shiftbox-agent /var/lib/amazon/ssm; do
        if [ -e "$root$path" ] || [ -L "$root$path" ]; then
            echo "Unexpected builder state at $path. Stop and investigate before image capture." >&2
            return 1
        fi
    done
    for path in "$root"/etc/ssh/ssh_host_*; do
        if [ -e "$path" ] || [ -L "$path" ]; then
            echo 'Builder SSH host keys are still present.' >&2
            return 1
        fi
    done
    for directory in /var/lib/cloud/instances /var/lib/cloud/seed; do
        if [ -d "$root$directory" ]; then
            contents=$(find "$root$directory" -mindepth 1 -print -quit) || return
            if [ -n "$contents" ]; then
                echo 'Cloud-init builder state remains on the image.' >&2
                return 1
            fi
        fi
    done
    if [ -s "$root/etc/machine-id" ] && [ "$(cat "$root/etc/machine-id")" != uninitialized ]; then
        echo 'Builder machine identity remains on the image.' >&2
        return 1
    fi
    if ! awk -F: '$1 == "root" && $2 ~ /^[!*]/ { root = 1 }
        $1 == "ubuntu" && $2 ~ /^[!*]/ { ubuntu = 1 }
        END { exit !(root && ubuntu) }' "$root/etc/shadow"; then
        echo 'The builder accounts must be locked before public capture.' >&2
        return 1
    fi
}

mode=${1:-private}
case "$mode" in
    verify-public)
        [ "$#" -eq 2 ] || exit 2
        case "$2" in /*) ;; *) exit 2 ;; esac
        verify_public_root "$2"
        exit
        ;;
    private|public) [ "$#" -le 1 ] || exit 2 ;;
    *) echo 'Choose private, public, or verify-public sealing mode.' >&2; exit 2 ;;
esac

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

if [ "$mode" = public ]; then
    # The ephemeral builder writes only its public authorization key here.
    for directory in /home/ubuntu/.ssh /root/.ssh; do
        if [ -d "$directory" ]; then
            rmdir "$directory"
        fi
    done
    usermod -L root
    verify_public_root ''
fi

# Packer stops the instance before taking the AMI snapshots.
sync
