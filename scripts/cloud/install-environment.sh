#!/usr/bin/env bash
set -euo pipefail
mkdir -p /workspace/.setup/bin /workspace/.setup/downloads /workspace/.shiftbox-tools
cd /workspace/.setup/downloads
if [ ! -x /workspace/.shiftbox-tools/go/bin/go ]; then
  curl -fsSL https://go.dev/dl/go1.27.4.linux-amd64.tar.gz -o go.tar.gz
  echo '7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840  go.tar.gz' | sha256sum -c -
  tar -xzf go.tar.gz -C /workspace/.shiftbox-tools
fi
if [ ! -x /workspace/.shiftbox-tools/node-v26.13.1-linux-x64/bin/node ]; then
  curl -fsSL https://nodejs.org/dist/v26.13.1/node-v26.13.1-linux-x64.tar.gz -o node.tar.gz
  echo '518674de2b621728a5e4486f5fb3b7d2900bb3cc4aa97969edaa463db99f9778  node.tar.gz' | sha256sum -c -
  tar -xzf node.tar.gz -C /workspace/.shiftbox-tools
fi
if [ ! -x /workspace/.setup/bin/terraform ]; then
  curl -fsSL https://releases.hashicorp.com/terraform/1.14.0/terraform_1.14.0_linux_amd64.zip -o terraform.zip
  echo '33ac217458ba8b44ce2813553083bc132c9a07e41a79c2e3627977682d283093  terraform.zip' | sha256sum -c -
  python3 -c "import zipfile; zipfile.ZipFile('terraform.zip').extract('terraform', '/workspace/.setup/bin')"
  chmod +x /workspace/.setup/bin/terraform
fi
source /workspace/ShiftBox/scripts/cloud/env.sh
export PATH=/workspace/.setup/bin:$PATH
export UV_PYTHON_INSTALL_DIR=/workspace/.setup/python UV_CACHE_DIR=/workspace/.setup/uv-cache
if [ ! -x /workspace/.setup/python/cpython-3.11-linux-x86_64-gnu/bin/python3.11 ]; then
  uv python install 3.11 --no-bin
fi
if [ ! -x /workspace/.setup/venvs/ts-cloud311/bin/python ]; then
  /workspace/.setup/python/cpython-3.11-linux-x86_64-gnu/bin/python3.11 -m venv /workspace/.setup/venvs/ts-cloud311
fi
/workspace/.setup/venvs/ts-cloud311/bin/python -m pip install --cache-dir /workspace/.setup/pip-cache -r /workspace/ts-cloud/aws_provision_python/requirements.txt
/workspace/.setup/venvs/ts-cloud311/bin/python -m pip check
cd /workspace/ShiftBox
npm --prefix ui ci --no-audit --no-fund
npm --prefix branding ci --no-audit --no-fund
# Do not run the brand exporter in this checkout: it overwrites tracked assets.
npm --prefix ui run typecheck
npm --prefix ui test
npm --prefix ui run build
if [ ! -d /workspace/.shiftbox-tools/dex-src/.git ]; then
  git clone --depth 1 --branch v2.46.1 https://github.com/dexidp/dex.git /workspace/.shiftbox-tools/dex-src
fi
# dex-src is a tool dependency, not an additional project checkout.
go mod download
make fmt-check vet test race
go build -p 4 -mod=readonly -tags=shiftbox_ui -o bin/shiftbox ./cmd/shiftbox
cd /workspace/.shiftbox-tools/dex-src
go build -p 4 -mod=readonly -ldflags '-w -X main.version=v2.46.1' -o /workspace/.shiftbox-tools/dex ./cmd/dex
# Services stay stopped: scripts/cloud/README.md explicitly pauses demo startup.
