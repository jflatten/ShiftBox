#!/bin/bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
cd /workspace/ShiftBox
go version
node --version
go mod download
npm --prefix ui ci
make build
cd /workspace/.shiftbox-tools/dex-src
go build -p 4 -mod=readonly -ldflags '-w -X main.version=v2.46.1' -o /workspace/.shiftbox-tools/dex ./cmd/dex
