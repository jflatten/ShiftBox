#!/bin/bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
cd /workspace/ShiftBox
exec npm --prefix ui run dev -- --hostname 0.0.0.0 --port 3000
