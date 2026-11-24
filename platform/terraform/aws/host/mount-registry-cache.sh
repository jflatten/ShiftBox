#!/bin/sh
set -eu

volume_id=$1
destination=$2
initialize=$3
attempts=${4:-120}
case "$volume_id" in vol-*) ;; *) echo 'Expected an EBS volume ID' >&2; exit 1 ;; esac
case "$initialize" in
    true|false|auto) ;;
    *) echo 'Expected cache initialization mode true, false, or auto' >&2; exit 1 ;;
esac
serial="vol${volume_id#vol-}"

device_for_serial() {
    lsblk -dnpro NAME,SERIAL | awk -v serial="$serial" '$2 == serial { print $1 }'
}

device="$(device_for_serial)"
while [ -z "$device" ] && [ "$attempts" -gt 0 ]; do
    sleep 2
    attempts=$((attempts - 1))
    device="$(device_for_serial)"
done
if [ -z "$device" ] || [ ! -e "$device" ]; then
    echo 'Expected registry cache volume is absent' >&2
    exit 1
fi

mounted="$(findmnt -nro SOURCE --mountpoint "$destination" || true)"
if [ "$initialize" = true ] && [ -n "$mounted" ]; then
    echo 'Initial cache preparation requires an unmounted volume' >&2
    exit 1
fi
if [ -z "$mounted" ]; then
    existing_targets="$(lsblk -dnro MOUNTPOINTS "$device")"
    if [ -n "$existing_targets" ]; then
        echo 'Registry cache volume is already mounted at a different path' >&2
        exit 1
    fi
    if filesystem="$(blkid -p -s TYPE -o value "$device")"; then
        if [ "$initialize" = true ]; then
            echo 'Initial cache preparation requires a blank volume' >&2
            exit 1
        fi
        if [ "$filesystem" != xfs ]; then
            echo 'Registry cache volume has an unexpected filesystem' >&2
            exit 1
        fi
    else
        status=$?
        if [ "$status" -ne 2 ] || [ "$initialize" = false ] ||
            [ "$(lsblk -nrpo TYPE "$device")" != disk ]; then
            echo 'Registry cache filesystem is unavailable or not confirmed blank' >&2
            exit 1
        fi
        if ! signatures="$(wipefs --noheadings "$device")" || [ -n "$signatures" ]; then
            echo 'Registry cache filesystem is unavailable or not confirmed blank' >&2
            exit 1
        fi
        mkfs.xfs "$device"
    fi

    install -d -m 0755 "$destination"
    mount -t xfs "$device" "$destination"
    mounted="$(findmnt -nro SOURCE --mountpoint "$destination")"
fi
if [ "$(readlink -f "$mounted")" != "$(readlink -f "$device")" ]; then
    echo 'Registry cache mount belongs to a different volume' >&2
    exit 1
fi

# CloudFormation owns this disk and can increase its size between host boots.
# Grow only after verifying the mounted device so the change exposes the new capacity.
if [ "$initialize" = auto ]; then
    xfs_growfs "$destination"
fi

install -d -m 0700 -o 10001 -g 10001 "$destination/cache"
marker="$(mktemp)"
trap 'rm -f "$marker"' EXIT
printf '%s\n' "$volume_id" > "$marker"
install -m 0444 -o 0 -g 0 "$marker" "$destination/.shiftbox-registry-cache"
