# Build the ShiftBox Ubuntu guest

Run on the logged-in Mac with UTM 5.0.8, Packer 1.14.2, the repository's Go
toolchain, and `utmctl` and `qemu-img` on `PATH`. The recipe pins the Ubuntu
24.04 Minimal amd64 cloud image by dated release and SHA-256.

Build the UTM plugin from the UTM 5-compatible revision. This uses the
upstream plugin source that includes the UTM 5 driver.

```sh
mkdir -p .cache/packer
export PACKER_PLUGIN_PATH="$PWD/.cache/packer/plugins"
git clone https://github.com/naveenrajm7/packer-plugin-utm.git .cache/packer/utm-plugin
git -C .cache/packer/utm-plugin checkout --detach fccda3be64189b5c057f6076184be7906475a1f2
(
    cd .cache/packer/utm-plugin
    go build -trimpath -ldflags '-X github.com/naveenrajm7/packer-plugin-utm/version.Version=4.0.0 -X github.com/naveenrajm7/packer-plugin-utm/version.VersionPrerelease=' -o ../packer-plugin-utm .
)
packer plugins install --path .cache/packer/packer-plugin-utm github.com/naveenrajm7/utm
platform/packer/build-image.sh development
```

The build creates a disposable VM under a unique name and provisions it
using a temporary SSH key. The shutdown step removes the build key,
clears machine identity and cloud-init seed state, and disables SSH. The
systemd agent uses installation-specific enrollment media on workspace boot.
The final export is in the build directory's `final` folder. ShiftBox uses
UTM scripting to remove the temporary second network and attach an empty
removable recovery drive while preserving the persistent disk.

The build installs Docker Engine 29.9.3, containerd 2.4.2, and Buildx
0.39.0 from Docker's signed Noble package index. It verifies the Node.js
24.22.3 and Dev Container CLI 0.90.0 archives by SHA-256, then checks the
running Docker service and installed tools before sealing the guest.

The build installs Coder code-server 4.145.2 from its checksum-pinned Linux
amd64 release archive. The installed distribution retains its license and
third-party notices.

The build also preloads `shiftbox/repair:ubuntu-24.04` from a digest-pinned
Ubuntu amd64 base with the same code-server distribution. It verifies that
code-server starts without network access or an image pull.

The build writes `manifest.json` beside `shiftbox.utm` in the `final`
directory. The manifest records the guest and IDE versions and digests,
source revision, and exported file digests. The manifest tool can also run
against a prepared bundle and its build metadata.

```sh
go run ./platform/packer/manifest /absolute/path/to/final/shiftbox.utm \
    < build-metadata.json > /absolute/path/to/final/manifest.json
```

The tool hashes the exported files in place. Release validation uses the
candidate bundle and its manifest for UTM clone, boot, and enrollment checks.

To import the candidate during local setup, pass its `final` directory to
the setup command. Use the controller configuration for the same installation
directory. The provider settings must include the UTM Documents directory,
a host address the guest can reach, and resource bounds that include the
image's virtual disk size.

```sh
shiftbox setup --installation-dir /absolute/path/to/installation \
    --administrator-email owner@example.com \
    --config /absolute/path/to/shiftbox.json \
    --platform-folder /absolute/path/to/final
```

Choose **Validate platform image** in the provider step. Setup checks the
manifest, imports a disposable clone, waits for an authenticated agent poll,
and verifies VM removal before selecting the default image.

To verify that a prepared development export can enroll, run the opt-in
`utm_release` test with UTM 5.0.8 running. Set the guest host to a local IP
address reachable from the VM and point storage at UTM's Documents directory.

```sh
SHIFTBOX_TEST_UTM_MUTATIONS=5.0.8 \
SHIFTBOX_TEST_UTM_STORAGE="$HOME/Library/Containers/com.utmapp.UTM/Data/Documents" \
SHIFTBOX_TEST_UTM_BUNDLE="/absolute/path/to/final/shiftbox.utm" \
SHIFTBOX_TEST_UTM_GUEST_HOST="your-guest-reachable-ip" \
go test -tags utm_release -v ./tests/release -run '^TestUTMGuestEnrollment$'
```
