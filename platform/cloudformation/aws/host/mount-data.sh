#!/bin/sh
set -eu

volume_serial=$1
destination=$2
initialize=$3
attempts=${4:-120}

find_data_device() {
    lsblk -dnpro NAME,SERIAL | awk -v serial="$volume_serial" '$2 == serial { print $1 }'
}

# Nitro device names can change after replacement. Wait for this EBS serial.
device="$(find_data_device)"
while [ -z "$device" ] && [ "$attempts" -gt 0 ]; do
    sleep 2
    attempts=$((attempts - 1))
    device="$(find_data_device)"
done
if [ -z "$device" ] || [ ! -e "$device" ]; then
    echo 'ShiftBox data volume is absent' >&2
    exit 1
fi

mounted="$(findmnt -nro SOURCE --mountpoint "$destination" || true)"
if [ -z "$mounted" ]; then
    if filesystem="$(blkid -p -s TYPE -o value "$device")"; then
        if [ "$filesystem" != xfs ]; then
            echo 'ShiftBox data volume has an unexpected filesystem' >&2
            exit 1
        fi
    else
        status=$?
        if [ "$status" -ne 2 ] || [ "$initialize" != true ]; then
            echo 'ShiftBox data filesystem is unavailable' >&2
            exit 1
        fi
        # Formatting requires explicit authorization for a newly created volume.
        mkfs.xfs "$device"
    fi
    mkdir -p "$destination"
    mount -t xfs "$device" "$destination"
    mounted="$(findmnt -nro SOURCE --mountpoint "$destination")"
fi

expected_device="$(readlink -f "$device")"
if ! mounted_device="$(readlink -f "$mounted")" || [ "$mounted_device" != "$expected_device" ]; then
    echo 'ShiftBox data path belongs to a different volume' >&2
    exit 1
fi

install -d -m 0700 -o 10001 -g 10001 "$destination/shiftbox"
install -d -m 0700 -o 999 -g 999 "$destination/postgres"
# The controller checks this marker before using its installation directory.
install -m 0400 -o 10001 -g 10001 /dev/null "$destination/shiftbox/.persistent-volume"
