#!/bin/sh
set -eu
umask 022

test "$(dpkg --print-architecture)" = amd64
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
cat > /etc/apt/sources.list.d/docker.sources <<'DOCKER'
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: amd64
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
node_archive="node-$node_version-linux-x64.tar.xz"
download "$node_archive" "https://nodejs.org/dist/$node_version/$node_archive" \
    4f2b175a6d53a5c617a80dba2676238a0688cef28c6da4eba35c7d844e66fcfb
node_dir="node-$node_version-linux-x64"
tar -xJf "$node_archive" --no-same-owner "$node_dir/bin/node" "$node_dir/LICENSE"
install -m 0755 "$node_dir/bin/node" /usr/local/bin/node
install -D -m 0644 "$node_dir/LICENSE" /usr/local/share/shiftbox/licenses/node.LICENSE

devcontainer_version='0.90.0'
download devcontainer.tgz "https://registry.npmjs.org/@devcontainers/cli/-/cli-$devcontainer_version.tgz" \
    b3d40fec9d274a303a29ea91a24852887cf287701bc8d340bfc1cf970542495a
install -d -m 0755 /usr/local/lib/shiftbox/devcontainer
tar -xzf devcontainer.tgz --no-same-owner --strip-components=1 -C /usr/local/lib/shiftbox/devcontainer
cat > /usr/local/bin/devcontainer <<'CLI'
#!/bin/sh
exec /usr/local/bin/node /usr/local/lib/shiftbox/devcontainer/devcontainer.js "$@"
CLI
chmod 0755 /usr/local/bin/devcontainer

. /tmp/shiftbox-code-server.env
ide_archive="code-server-$CODE_SERVER_VERSION-linux-amd64.tar.gz"
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
test "$(node --version)" = "$node_version"
test "$(devcontainer --version)" = "$devcontainer_version"
git --version

apt-get clean
