# Native ARM64 UTM validation

On November 15, 2026, Packer 1.14.2 and UTM 5.0.8 built the
`development-arm64` platform image on an Apple Silicon Mac. The Ubuntu
24.04 Minimal export reported `aarch64` and `Hypervisor = true` in its UTM
configuration. Packer provisioned Docker Engine 29.9.3, Dev Container CLI
0.90.0, and code-server 4.145.2. The build used a local development publisher
key.

The final Packer build took 1 minute 10 seconds. The native release test
imported the prepared bundle as a disposable ShiftBox workspace and confirmed
authenticated guest enrollment in 35 seconds.

- The test seeded a local checkout and used the normal workspace launch
  operation. A Dev Container lifecycle hook confirmed `uname -m` was
  `aarch64`. The guest tunnel served the VS Code workbench.
- After an intentionally malformed configuration caused a rebuild to fail,
  the normal repair operation opened the VS Code workbench through the guest
  tunnel. Docker reported the preloaded repair image ID, `none` networking,
  and a read-only root filesystem. The repair container also reported
  `aarch64`.
- The release test removed the disposable clone after verification.

A separate catalog test validated boot and enrollment before publishing the
image in its isolated catalog. It created independent VM and disk copies for
two installations and verified that deleting one preserved the other.

The template catalog and workspace launch contracts exercised both image
architectures. The catalog migration passed on SQLite and PostgreSQL 17
with an existing referenced AMD64 artifact. Both Packer plans validated
with the pinned plugin.

See the [Packer guide](../../platform/packer/README.md) for the build and
test commands.


## Launch polling and preloaded base image

The updated controller requests a jittered 1–1.25 second retry during the
authenticated handoff from enrollment to the first launch dispatch. This
window ends after dispatch or 90 seconds from the start of enrollment. The
agent reports new committed development setup receipts immediately. Idle
polling uses the original 27–33 second interval.

The native release fixture verified that the archived ARM64 image already
contained the pinned base image under the template's configured Docker tag.
The fixture used a seeded checkout and a five-second required hook. The
guest reported no Docker pull during launch.

| Controller step | Duration | Time from launch |
| --- | ---: | ---: |
| Provision UTM VM | 11.244 s | 11.246 s |
| Boot and enroll | 50.243 s | 61.492 s |
| Configure Dev Container | 66.511 s | 128.005 s |
| Verify browser IDE | 1.078 s | 130.081 s |
| Finish later setup | 0.001 s | 130.086 s |

The guest logged 25.766 seconds for its first Docker build and 18.018
seconds for code-server installation. Container readiness took 0.995
seconds for the first container and 0.725 seconds for the retained-home
replacement. The remaining hooks took 1.341 seconds in the guest and
overlapped controller IDE verification and reconciliation. Docker and
container timings are nested within the Dev Container CLI and retained-home
timings.

This run measured the controller and guest workflow with a seeded checkout.
The previous local UI launch included a real GitHub checkout.
