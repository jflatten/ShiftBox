#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
export RACE_FIXTURE="$scratch/packages"
cat > "$RACE_FIXTURE" <<'EOF'
github.com/jflatten/ShiftBox/internal/app
github.com/jflatten/ShiftBox/internal/backup
github.com/jflatten/ShiftBox/internal/guest
github.com/jflatten/ShiftBox/internal/web
github.com/jflatten/ShiftBox/internal/workspace
github.com/jflatten/ShiftBox/tests/acceptance
github.com/jflatten/ShiftBox/tests/contract
github.com/jflatten/ShiftBox/tests/future-fixture
EOF
cat > "$scratch/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == 'list -race ./...' ]]; then
  cat "$RACE_FIXTURE"
  exit 0
fi
[[ "$1" == test && "$2" == -race ]]
shift 2
[[ $# -gt 0 ]]
printf '%s\n' "$@"
EOF
chmod +x "$scratch/go"

# Every race-enabled package must be tested exactly once, including new packages.
for shard in app web acceptance core; do
  GO="$scratch/go" bash "$root/scripts/go-race-shard.sh" "$shard" > "$scratch/$shard"
done
sort "$RACE_FIXTURE" > "$scratch/all"
sort "$scratch"/{app,web,acceptance,core} | diff -u "$scratch/all" -

# Keep the six measured slow packages in balanced pairs and send new packages to core.
grep -Fxq github.com/jflatten/ShiftBox/internal/app "$scratch/app"
grep -Fxq github.com/jflatten/ShiftBox/tests/contract "$scratch/app"
grep -Fxq github.com/jflatten/ShiftBox/internal/web "$scratch/web"
grep -Fxq github.com/jflatten/ShiftBox/internal/workspace "$scratch/web"
grep -Fxq github.com/jflatten/ShiftBox/tests/acceptance "$scratch/acceptance"
grep -Fxq github.com/jflatten/ShiftBox/internal/backup "$scratch/acceptance"
grep -Fxq github.com/jflatten/ShiftBox/tests/future-fixture "$scratch/core"

# A typo in the workflow matrix must fail instead of passing with zero tests.
if GO="$scratch/go" bash "$root/scripts/go-race-shard.sh" invalid >/dev/null 2>&1; then
  printf '%s\n' 'race shard accepted an unknown name' >&2
  exit 1
fi
