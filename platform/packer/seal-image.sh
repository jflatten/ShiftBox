#!/bin/sh
set -eu

# No build credentials or machine identity may survive into a workspace clone.
rm -f /home/shiftbox-build/.ssh/authorized_keys
passwd -l shiftbox-build
rm -f /etc/sudoers.d/90-cloud-init-users /etc/ssh/ssh_host_* /etc/ssh/sshd_config.d/50-cloud-init.conf
systemctl disable ssh.service ssh.socket
cloud-init clean --logs --machine-id --seed
rm -f /tmp/shiftbox-agent /tmp/shiftbox-start-agent /tmp/shiftbox-agent.service /tmp/shiftbox-seal-image /tmp/shiftbox-install-toolchain
rm -f /tmp/shiftbox-guest-update.pub
rm -f /tmp/shiftbox-code-server.env
sync
systemctl poweroff
