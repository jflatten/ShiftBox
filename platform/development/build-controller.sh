#!/usr/bin/env bash
# Build a local checkout for iteration and push only to the private testing ECR repository.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
if [[ $# -lt 1 || $# -gt 2 || ! $1 =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$ ]]; then
  printf 'Usage on macOS: %s <existing guest compatibility version> [management HTTPS origin]\n' "$0" >&2
  exit 2
fi
account=888293065132
region=us-east-2
repository=shiftbox-development
registry="$account.dkr.ecr.$region.amazonaws.com"
export AWS_PAGER='' AWS_CLI_AUTO_PROMPT=off AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true
testing_aws() { aws --profile shiftbox-testing --region "$region" "$@"; }
[[ $(testing_aws sts get-caller-identity --query Account --output text) == "$account" ]] || exit 1
testing_aws ecr describe-repositories --registry-id "$account" --repository-names "$repository" --output json |
  jq -e --arg uri "$registry/$repository" '.repositories | length == 1 and
    (.[0].repositoryUri == $uri and .[0].imageTagMutability == "IMMUTABLE")' >/dev/null

context=$(docker context show)
endpoint=$(docker context inspect "$context" --format '{{.Endpoints.docker.Host}}')
buildx=$(docker info --format '{{json .ClientInfo.Plugins}}' |
  jq -er '.[] | select(.Name == "buildx") | .Path | select(startswith("/"))')
[[ -x $buildx ]]
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
go_binary="$root/.cache/toolchains/go/bin/go"
if [[ ! -x $go_binary ]]; then go_binary=go; fi
GO="$go_binary" sh "$root/platform/helper/macos/package.sh" "${2:---portable}" "$scratch/helper" >&2
mkdir "$scratch/cli-plugins"
ln -s -- "$buildx" "$scratch/cli-plugins/docker-buildx"
export DOCKER_CONFIG="$scratch" DOCKER_HOST="$endpoint"
unset DOCKER_CONTEXT BUILDX_BUILDER
docker buildx version >/dev/null
revision=$(git -C "$root" rev-parse --short=12 HEAD)
tag="dev-$(date -u +%Y%m%d%H%M%S)-$revision"
testing_aws ecr get-login-password | docker login --username AWS --password-stdin "$registry" >&2
docker buildx build --platform linux/amd64 --build-arg "RELEASE_CANDIDATE=$1" \
  --build-context "macos-helper=$scratch/helper" \
  --tag "$registry/$repository:$tag" --push --file "$root/platform/container/Dockerfile" "$root" >&2
digest=$(testing_aws ecr describe-images --registry-id "$account" --repository-name "$repository" \
  --image-ids "imageTag=$tag" --query 'imageDetails[0].imageDigest' --output text)
[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || exit 1
printf '%s/%s@%s\n' "$registry" "$repository" "$digest"
