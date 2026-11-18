#!/bin/sh
set -eu

guard="$(cd -- "$(dirname -- "$0")" && pwd)/mount-registry-cache.sh"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
mkdir "$scratch/bin"
export TEST_DEVICE="$scratch/cache-device" TEST_MOUNT="$scratch/mounted" TEST_LOG="$scratch/mount-log"
export PATH="$scratch/bin:$PATH" TEST_FORMATTED="$scratch/formatted"

cat > "$scratch/bin/lsblk" <<'STUB'
#!/bin/sh
case "$*" in
    "-dnro MOUNTPOINTS $TEST_DEVICE") printf '%s' "${TEST_OTHER_MOUNT:-}"; exit 0 ;;
    "-nrpo TYPE $TEST_DEVICE") printf 'disk\n'; exit 0 ;;
    "-dnpro NAME,SERIAL") ;;
    *) exit 2 ;;
esac
if [ -e "$TEST_DEVICE" ]; then printf '%s %s\n' "$TEST_DEVICE" vol0123456789abcdef0; fi
STUB
cat > "$scratch/bin/findmnt" <<'STUB'
#!/bin/sh
test "$*" = "-nro SOURCE --mountpoint $TEST_MOUNT" || exit 2
test -f "$TEST_LOG" && cat "$TEST_LOG"
STUB
cat > "$scratch/bin/blkid" <<'STUB'
#!/bin/sh
if [ "${TEST_BLANK:-false}" = true ] && [ ! -e "$TEST_FORMATTED" ]; then exit 2; fi
printf 'xfs\n'
STUB
cat > "$scratch/bin/mount" <<'STUB'
#!/bin/sh
test "$*" = "-t xfs $TEST_DEVICE $TEST_MOUNT" || exit 2
printf '%s\n' "$TEST_DEVICE" > "$TEST_LOG"
STUB
cat > "$scratch/bin/mkfs.xfs" <<'STUB'
#!/bin/sh
test "${TEST_BLANK:-false}" = true
test "$*" = "$TEST_DEVICE"
test ! -e "$TEST_FORMATTED"
touch "$TEST_FORMATTED"
STUB
cat > "$scratch/bin/wipefs" <<'STUB'
#!/bin/sh
test "${TEST_PROBE_ERROR:-false}" = false
STUB
cat > "$scratch/bin/install" <<'STUB'
#!/bin/sh
for argument do previous=${last-}; last="$argument"; done
if [ "$1" = "-d" ]; then mkdir -p "$last"; else cp "$previous" "$last"; fi
STUB
chmod +x "$scratch"/bin/*

# An absent serial must not create a writable fallback directory.
if sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" false 0; then
    echo 'cache mount accepted an absent volume' >&2
    exit 1
fi
test ! -e "$TEST_MOUNT"

# An existing expected filesystem mounts twice without formatting.
touch "$TEST_DEVICE"
sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" false 0
sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" false 0
test -d "$TEST_MOUNT/cache"
test "$(cat "$TEST_MOUNT/.shiftbox-registry-cache")" = vol-0123456789abcdef0

# A different filesystem at the target is never accepted as the cache disk.
printf '%s\n' "$scratch/not-the-cache" > "$TEST_LOG"
if sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" false 0; then
    echo 'cache mount accepted a foreign volume' >&2
    exit 1
fi

# Boot initialization requires the exact disk to be blank and unmounted.
rm "$TEST_LOG"
if sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" true 0; then
    echo 'initial setup accepted a nonblank volume' >&2
    exit 1
fi
if TEST_BLANK=true TEST_OTHER_MOUNT=/other sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" true 0; then
    echo 'initial setup accepted a disk mounted elsewhere' >&2
    exit 1
fi
if TEST_BLANK=true TEST_PROBE_ERROR=true sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" true 0; then
    echo 'initial setup accepted an unreadable signature probe' >&2
    exit 1
fi
test ! -e "$TEST_FORMATTED"
TEST_BLANK=true sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" true 0
test -e "$TEST_FORMATTED"
TEST_BLANK=true sh "$guard" vol-0123456789abcdef0 "$TEST_MOUNT" false 0
