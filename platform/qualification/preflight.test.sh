#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
export GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_REF=refs/heads/main GITHUB_EVENT_NAME=workflow_dispatch
export GITHUB_ACTOR=jflatten GITHUB_TRIGGERING_ACTOR=jflatten
export GITHUB_RUN_ID=11122233344 GITHUB_RUN_ATTEMPT=3
export SHIFTBOX_QUALIFICATION_ACCOUNT_ID=111111111111 SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=222222222222
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

cat >"$scratch/aws" <<'AWS'
#!/usr/bin/env bash
set -eu
case "$*" in
  '--region us-east-2 sts get-caller-identity --query Account --output text')
    printf '%s\n' "${FIXTURE_ACCOUNT:-111111111111}" ;;
  *'service-quotas get-service-quota '*)
    printf '%s\n' "${FIXTURE_QUOTA:-5}" ;;
  *'ec2 describe-instances '*)
    printf '%s\n' "${FIXTURE_RUNNING:-0}" ;;
  *) exit 2 ;;
esac
AWS
chmod +x "$scratch/aws"
export PATH="$scratch:$PATH"

reject() {
  if "$@" >"$scratch/rejected" 2>&1; then
    printf 'Expected rejection for %s\n' "$*" >&2
    exit 1
  fi
}

# Only the maintainer dispatching from reviewed main may start qualification.
test "$(bash "$here/preflight.sh" guard qualify)" = sbq-11122233344-3
GITHUB_ACTOR=another reject bash "$here/preflight.sh" guard qualify
GITHUB_TRIGGERING_ACTOR=another reject bash "$here/preflight.sh" guard qualify
GITHUB_EVENT_NAME=pull_request reject bash "$here/preflight.sh" guard qualify
GITHUB_EVENT_NAME=schedule reject bash "$here/preflight.sh" guard qualify
GITHUB_REF=refs/heads/feature/mvp reject bash "$here/preflight.sh" guard qualify
GITHUB_REPOSITORY=example/other reject bash "$here/preflight.sh" guard qualify
GITHUB_RUN_ID=0 reject bash "$here/preflight.sh" guard qualify

# The independent job allows the scheduled sweep and only maintainer manual cleanup.
test "$(GITHUB_EVENT_NAME=schedule GITHUB_ACTOR=cron GITHUB_TRIGGERING_ACTOR=cron bash "$here/preflight.sh" guard cleanup)" = sbq-11122233344-3
GITHUB_EVENT_NAME=push reject bash "$here/preflight.sh" guard cleanup
GITHUB_TRIGGERING_ACTOR=another reject bash "$here/preflight.sh" guard cleanup

# The account fence checks STS and rejects shared, publisher and malformed targets.
bash "$here/preflight.sh" account
FIXTURE_ACCOUNT=333333333333 reject bash "$here/preflight.sh" account
SHIFTBOX_QUALIFICATION_ACCOUNT_ID=524941137496 FIXTURE_ACCOUNT=524941137496 reject bash "$here/preflight.sh" account
SHIFTBOX_QUALIFICATION_ACCOUNT_ID=222222222222 FIXTURE_ACCOUNT=222222222222 reject bash "$here/preflight.sh" account
SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=missing reject bash "$here/preflight.sh" account

# The published topology needs six vCPUs during the staffed Ohio run.
bash "$here/preflight.sh" capacity automated
reject bash "$here/preflight.sh" capacity staffed
FIXTURE_QUOTA=6 bash "$here/preflight.sh" capacity staffed
FIXTURE_RUNNING=1 reject bash "$here/preflight.sh" capacity automated
FIXTURE_QUOTA=null reject bash "$here/preflight.sh" capacity automated

# Stable tags and user-controlled paths fail before any remote release access.
for tag in v1.0.0 'v01.0.0-rc.1' 'v1.0.0-rc.0' '../v1.0.0-rc.1'; do
  reject bash "$here/preflight.sh" fetch "$tag" "$scratch/assets"
  rg -q 'explicit canonical RC' "$scratch/rejected"
done
