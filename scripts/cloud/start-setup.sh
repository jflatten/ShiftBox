#!/bin/bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
cd /workspace/ShiftBox
umask 077
mkdir -p /workspace/shiftbox-env/private
exec ./bin/shiftbox setup --installation-dir /workspace/ShiftBox/.shiftbox/demo --administrator-email demo@shiftbox.test --dex-binary /workspace/.shiftbox-tools/dex >> /workspace/shiftbox-env/private/initial-password 2> /workspace/shiftbox-env/setup.log
