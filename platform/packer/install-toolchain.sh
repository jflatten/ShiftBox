#!/bin/sh
set -eu
umask 022

architecture=$1
test "$(dpkg --print-architecture)" = "$architecture"
case "$architecture" in
    amd64)
        node_arch=x64
        node_sha=4f2b175a6d53a5c617a80dba2676238a0688cef28c6da4eba35c7d844e66fcfb
        repair_base='ubuntu@sha256:77eb101c607d6e3d4f5ad0c8a02c2136c689b98aba1f359e6502ba517e5c57ec'
        ;;
    arm64)
        node_arch=arm64
        node_sha=6e51a2ada00f0011cc266f65d98922b403d7ad88f809ca2aa615a5fe1ce6e86c
        repair_base='ubuntu@sha256:c92376659a7f14cc1f51615d73c9f4b7d8e18d59f370e02b4ed683ab4485c66c'
        ;;
    *) exit 2 ;;
esac
. /etc/os-release
test "$ID" = ubuntu && test "$VERSION_CODENAME" = noble

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl git xz-utils

downloads=$(mktemp -d)
trap 'rm -rf "$downloads"' EXIT
cd "$downloads"

download() {
    curl --fail --silent --show-error --location --proto '=https' \
        --connect-timeout 15 --max-time 180 --retry 3 --output "$1" "$2"
    printf '%s  %s\n' "$3" "$1" | sha256sum --check --strict
}

# Use Docker's signed Ubuntu package index for the engine and Buildx.
download docker.asc https://download.docker.com/linux/ubuntu/gpg \
    1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570
install -D -m 0644 docker.asc /etc/apt/keyrings/docker.asc
cat > /etc/apt/sources.list.d/docker.sources <<DOCKER
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: $architecture
Signed-By: /etc/apt/keyrings/docker.asc
DOCKER

docker_version='5:29.9.3-1~ubuntu.24.04~noble'
containerd_version='2.4.2-4~ubuntu.24.04~noble'
buildx_version='0.39.0-1~ubuntu.24.04~noble'
apt-get update
apt-get install -y --no-install-recommends \
    "docker-ce=$docker_version" "docker-ce-cli=$docker_version" \
    "containerd.io=$containerd_version" "docker-buildx-plugin=$buildx_version"

node_version='v24.22.3'
node_dir="node-$node_version-linux-$node_arch"
node_archive="$node_dir.tar.xz"
download "$node_archive" "https://nodejs.org/dist/$node_version/$node_archive" "$node_sha"
tar -xJf "$node_archive" --no-same-owner "$node_dir/bin/node" "$node_dir/LICENSE"
install -m 0755 "$node_dir/bin/node" /usr/local/bin/node
install -D -m 0644 "$node_dir/LICENSE" /usr/local/share/shiftbox/licenses/node.LICENSE

devcontainer_version='0.90.0'
download devcontainer.tgz "https://registry.npmjs.org/@devcontainers/cli/-/cli-$devcontainer_version.tgz" \
    b3d40fec9d274a303a29ea91a24852887cf287701bc8d340bfc1cf970542495a
install -d -m 0755 /usr/local/lib/shiftbox/devcontainer
tar -xzf devcontainer.tgz --no-same-owner --strip-components=1 -C /usr/local/lib/shiftbox/devcontainer
devcontainer_bundle=/usr/local/lib/shiftbox/devcontainer/dist/spec-node/devContainersSpecCLI.js
/usr/local/bin/node /tmp/shiftbox-extend-devcontainer-cli.cjs "$devcontainer_bundle"
/usr/local/bin/node --check "$devcontainer_bundle"
rm -f /tmp/shiftbox-extend-devcontainer-cli.cjs
cat > /usr/local/bin/devcontainer <<'CLI'
#!/bin/sh
exec /usr/local/bin/node /usr/local/lib/shiftbox/devcontainer/devcontainer.js "$@"
CLI
chmod 0755 /usr/local/bin/devcontainer

. /tmp/shiftbox-code-server.env
if [ "$architecture" = arm64 ]; then
    CODE_SERVER_SHA256=$CODE_SERVER_ARM64_SHA256
fi
ide_archive="code-server-$CODE_SERVER_VERSION-linux-$architecture.tar.gz"
download "$ide_archive" "$CODE_SERVER_SOURCE/releases/download/v$CODE_SERVER_VERSION/$ide_archive" "$CODE_SERVER_SHA256"
install -d -m 0755 /usr/local/lib/shiftbox/code-server
tar -xzf "$ide_archive" --no-same-owner --strip-components=1 -C /usr/local/lib/shiftbox/code-server
test -s /usr/local/lib/shiftbox/code-server/LICENSE
test -s /usr/local/lib/shiftbox/code-server/ThirdPartyNotices.txt
ln -s /usr/local/lib/shiftbox/code-server/bin/code-server /usr/local/bin/code-server
code-server --version | grep -F "$CODE_SERVER_VERSION"

systemctl enable --now docker.service
test "$(docker version --format '{{.Server.Version}}')" = 29.9.3
test "$(docker version --format '{{.Client.Version}}')" = 29.9.3
docker buildx version | grep -E '(^| )v0\.39\.0( |$)'

# Seed the measured template's reference from its immutable ARM64 manifest.
if [ "$architecture" = arm64 ]; then
    devcontainer_base='mcr.microsoft.com/devcontainers/base:ubuntu-24.04'
    devcontainer_pin="$devcontainer_base@sha256:309df9dde7aeb9e734a8f7ead76ab4d18ee4eb17b6047d2a2d008fa52fdea67b"
    docker pull --platform linux/arm64 "$devcontainer_pin"
    docker tag "$devcontainer_pin" "$devcontainer_base"
    test "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$devcontainer_base")" = linux/arm64
fi

# Bake the independent repair environment into the VM's local image store.
# It uses the same verified IDE archive and never consults a project Dockerfile.
repair_image='shiftbox/repair:ubuntu-24.04'
docker pull --platform "linux/$architecture" "$repair_base"
docker build --network=none --platform "linux/$architecture" --tag "$repair_image" \
    --file - /usr/local/lib/shiftbox/code-server <<REPAIR
FROM $repair_base
COPY . /opt/shiftbox-code-server/
REPAIR
docker run --rm --pull=never --network=none "$repair_image" \
    /opt/shiftbox-code-server/bin/code-server --version | grep -F "$CODE_SERVER_VERSION"
docker image inspect --format '{{.Id}}' "$repair_image" > /usr/local/share/shiftbox/repair-image

test "$(node --version)" = "$node_version"
test "$(devcontainer --version)" = "$devcontainer_version"
git --version

apt-get clean
