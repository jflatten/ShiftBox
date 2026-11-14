#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

case "${1:-}" in
    "") set -- --check ;;
    --write) shift ;;
    *) echo "Usage: $0 [--write]" >&2; exit 2 ;;
esac

generator=ui/node_modules/.bin/openapi-typescript
if [ ! -x "$generator" ]; then
    echo "Install the UI toolchain with npm --prefix ui ci." >&2
    exit 1
fi

if ! "$generator" specs/001-shiftbox-mvp/contracts/openapi.yaml \
    --output ui/lib/api-types.ts --default-non-nullable false "$@"; then
    echo "To regenerate the API types, run sh scripts/check-openapi.sh --write." >&2
    exit 1
fi
