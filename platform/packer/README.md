# Build the ShiftBox Ubuntu guest

Run on the logged-in Mac with UTM 5.0.8, Packer 1.14.2, the repository's Go
toolchain, and `utmctl` and `qemu-img` on `PATH`. The recipe pins the Ubuntu
24.04 Minimal cloud images by dated release and SHA-256. AMD64 uses QEMU
emulation on Apple Silicon. ARM64 uses QEMU with hardware virtualization.

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
platform/packer/build-image.sh development /absolute/path/to/guest-update.pub arm64
```

Use `amd64` or omit the final argument for the original AMD64 build. The
ARM64 build requires an Apple Silicon Mac and appends `-arm64` to the
platform release ID so both images can be imported with the same guest agent
version. Select the ARM64 platform release on a template whose base image
and Dev Container Features support `linux/arm64`.

Supply the release publisher's raw 32-byte Ed25519 public key. The image
installs it as `/usr/local/share/shiftbox/guest-update.pub`. Keep the matching
private key in the release signing environment.

To publish an update, prepare a private `releases/<release-id>/` directory
with the pinned `release.json` and its matching `shiftbox-agent` binary.
Run this from the release signing environment. The command writes
`shiftbox-agent.sig` alongside the binary.

```sh
go run ./platform/packer/sign-guest-update /absolute/path/to/releases <release-id> \
    < /absolute/path/to/publisher-key.pem
```

The signer reads an Ed25519 PKCS#8 PEM key from standard input. Deploy only
the manifest, binary, and detached signature to
`<installationDir>/releases/<release-id>/` (directories 0700, files 0600).
The catalog belongs to persistent installation storage even when the cloud
launcher generates its configuration in a temporary directory.
Run a maintenance campaign for each architecture using its
matching signed guest binary.

Updated agents publish the selected signed executable behind a stable
workspace CLI launcher. Containers created with the older binary file mount
need one normal rebuild to adopt the launcher; retained project, home, and
persistent application data follow the existing rebuild contract.

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

The build installs Coder code-server 4.145.2 from the checksum-pinned Linux
release archive for the guest architecture. The installed distribution retains its license and
third-party notices.

The build also preloads `shiftbox/repair:ubuntu-24.04` from a digest-pinned
Ubuntu base for the guest architecture with the same code-server distribution. It verifies that
code-server starts without network access or an image pull.

The ARM64 build also preloads `mcr.microsoft.com/devcontainers/base:ubuntu-24.04`
from ARM64 manifest `sha256:309df9dde7aeb9e734a8f7ead76ab4d18ee4eb17b6047d2a2d008fa52fdea67b`.
The standard tag is seeded in the VM's local Docker store for templates using
that reference. Templates can use the digest-qualified reference to request
the same bytes explicitly.

Guest setup timings are available in the guest's systemd journal. They
include operation ID, phase, duration in milliseconds, and outcome. Docker
command timings are nested within the Dev Container CLI `up` timings.

```sh
journalctl -u shiftbox-agent.service --grep='Dev Container phase timing'
```

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
directory. The provider settings must include the UTM Documents directory
and resource bounds that include the image's virtual disk size.

For local UTM installs, omit `guestHost`, `enrollmentAddress`, `pollAddress`,
and `tunnelAddress` from the `runtime` configuration to use UTM's default
shared-network address, `192.168.64.1`. Enrollment, polling, and the guest
tunnel use ports `19443`, `19444`, and `19445` respectively. ShiftBox waits
for UTM to create the bridge during the first VM boot. The browser console
uses its separately configured `browserAddress`.

If your UTM shared network uses a different subnet, set all four guest
settings to the appropriate host and listener addresses. An already enrolled
workspace keeps the controller endpoint it was given, so changing the
controller settings alone does not migrate that workspace.

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

To verify the ARM64 Dev Container and offline repair IDE on an Apple Silicon
Mac, use the same environment and add `SHIFTBOX_TEST_UTM_ARM64_WORKBENCHES=1`.
Run `go test -tags utm_release -v ./tests/release -run '^TestUTMARM64Workbenches$' -count=1`.
The test boots a disposable clone, seeds a local checkout, and verifies both
workbench responses over the authenticated guest tunnel. It checks
`uname -m` inside both containers and checks that Docker started the repair
container from the preloaded image with networking disabled.
