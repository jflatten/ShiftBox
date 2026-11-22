#!/bin/bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
cd /workspace/ShiftBox
# Expose the checkout's Codex configuration to the cloud harness at its workspace root.
# -T prevents existing directories from receiving nested links or being replaced.
for entry in .codex .agents AGENTS.md; do
  ln -sfnT "/workspace/ShiftBox/$entry" "/workspace/$entry"
done
go version
node --version
go mod download
npm --prefix ui ci
make build
cd /workspace/.shiftbox-tools/dex-src
go build -p 4 -mod=readonly -ldflags '-w -X main.version=v2.46.1' -o /workspace/.shiftbox-tools/dex ./cmd/dex
