#!/bin/sh
set -eu

guard="$(cd -- "$(dirname -- "$0")" && pwd)/mount-data.sh"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
mkdir "$scratch/bin"
export TEST_LOG="$scratch/commands"
export TEST_DEVICE="$scratch/expected-volume"
export TEST_MOUNT="$scratch/mounted"
export PATH="$scratch/bin:$PATH"

cat > "$scratch/bin/lsblk" <<'STUB'
#!/bin/sh
test "$*" = "-dnpro NAME,SERIAL" || exit 2
printf '%s %s\n' "$TEST_DEVICE" volunrelated
if [ -e "$TEST_DEVICE" ]; then
    printf '%s %s\n' "$TEST_DEVICE" volexpected
fi
STUB
cat > "$scratch/bin/findmnt" <<'STUB'
#!/bin/sh
test "$*" = "-nro SOURCE --mountpoint $TEST_MOUNT" || exit 2
test -f "$TEST_LOG" && cat "$TEST_LOG"
STUB
cat > "$scratch/bin/blkid" <<'STUB'
#!/bin/sh
test "$*" = "-p -s TYPE -o value $TEST_DEVICE" || exit 4
printf 'xfs\n'
STUB
cat > "$scratch/bin/mount" <<'STUB'
#!/bin/sh
test "$*" = "-t xfs $TEST_DEVICE $TEST_MOUNT" || exit 2
printf '%s\n' "$TEST_DEVICE" > "$TEST_LOG"
STUB
cat > "$scratch/bin/mkfs.xfs" <<'STUB'
#!/bin/sh
printf 'unexpected attempt to format retained media\n' >&2
exit 2
STUB
cat > "$scratch/bin/install" <<'STUB'
#!/bin/sh
for argument do last="$argument"; done
if [ "$1" = "-d" ]; then mkdir -p "$last"; else touch "$last"; fi
STUB
chmod +x "$scratch"/bin/*

# An absent volume blocks startup before any state directory can be created.
if sh "$guard" volexpected "$TEST_MOUNT" false 0; then
    echo 'mount guard accepted missing persistent storage' >&2
    exit 1
fi
test ! -e "$TEST_MOUNT"

# A replacement host mounts retained data and never formats the existing filesystem.
touch "$TEST_DEVICE"
sh "$guard" volexpected "$TEST_MOUNT" false 0
test -d "$TEST_MOUNT/shiftbox"
test -d "$TEST_MOUNT/postgres"
test -f "$TEST_MOUNT/shiftbox/.persistent-volume"
test "$(cat "$TEST_LOG")" = "$TEST_DEVICE"

# Refuse to use another attached volume even if it is already mounted at the expected path.
printf '%s\n' "$scratch/foreign-volume" > "$TEST_LOG"
if sh "$guard" volexpected "$TEST_MOUNT" false 0; then
    echo 'mount guard accepted a mismatched volume' >&2
    exit 1
fi
