#!/usr/bin/env bash
# Run one CI shard of the full Go race suite.
set -euo pipefail

shard=${1:-}
case "$shard" in
  app|web|acceptance|core) ;;
  *) printf 'Usage: %s <app|web|acceptance|core>\n' "$0" >&2; exit 2 ;;
esac

go_command=${GO:-go}
package_list=$("$go_command" list -race ./...)
packages=()
module=github.com/jflatten/ShiftBox
while IFS= read -r package; do
  [[ -n "$package" ]] || continue
  # Pair the slowest measured packages and include every other package in core.
  case "$package" in
    "$module/internal/app"|"$module/tests/contract") bucket=app ;;
    "$module/internal/web"|"$module/internal/workspace") bucket=web ;;
    "$module/tests/acceptance"|"$module/internal/backup") bucket=acceptance ;;
    *) bucket=core ;;
  esac
  if [[ "$bucket" == "$shard" ]]; then
    packages+=("$package")
  fi
done <<< "$package_list"

if (( ${#packages[@]} == 0 )); then
  printf 'No Go packages matched race shard %s.\n' "$shard" >&2
  exit 1
fi
exec "$go_command" test -race "${packages[@]}"
