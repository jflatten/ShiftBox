# Native UTM release checks

The checks ran on the logged-in arm64 Mac with macOS 26.6.2 (25G83) and
UTM 5.0.8. They used the existing Packer 1.14.2
`development-repair` export built from Ubuntu 24.04 Minimal amd64. Its
artifact manifest SHA-256 was
`bc0f0bf860c21b984216089579cc5c6f1485854f2a585829c85344ce54c9c301`.
The [release manifest](../../platform/manifests/release.json) records the
component versions and digests.

| Check | Controlled-host result |
| --- | --- |
| Packer recipe and artifact | Recipe, guest toolchain, code-server and repair-image assertions passed. The catalog verified and published the real export after boot and cleanup. |
| Catalog validation | `TestUTMCatalogValidation` passed in 62.09 seconds and confirmed that the disposable validation VM was absent afterward. |
| Boot and enrollment | `TestUTMGuestEnrollment` passed in 66.28 seconds. The real guest enrolled and made an authenticated poll in 50 seconds. Offboarding stopped the VM, preserved the owned disk, and released compute capacity. Cleanup removed the clone. |
| Native inventory and recovery media | `TestLiveUTMInventory` confirmed absence for an unknown installation. `TestLiveUTMTaggedDrive` passed with native power, media, and import checks enabled. It verified persistent drive preservation, import isolation, unchanged source digests, and idempotent verified removal. |

The race-enabled packages ran with `go test -race -tags utm_release -p 1
-count=1 -v ./tests/release ./internal/provider/utm`. The live checks used
the exact UTM 5.0.8 gates and an isolated guest-reachable listener. The
tagged-drive rerun also set `SHIFTBOX_TEST_UTM_IMPORT=5.0.8`.

The PostgreSQL catalog case and the browser workspace journal check were
skipped in this run. GitHub checkout, the browser IDE, and project repair
remain part of T061.

On November 14, two disposable QEMU guests remained in UTM's `starting`
state. After an operator-approved UTM restart, `TestUTMGuestEnrollment`
passed in 89.89 seconds against the same pinned export. The fresh clone
made its authenticated poll after 65 seconds. Offboarding preserved its
owned disk, and test cleanup confirmed removal of the new clone.

## Dev Container fixtures

`TestDevContainerReleaseFixtures` passed for the digest-pinned Debian
prebuilt image and Ubuntu Dockerfile using Dev Container CLI 0.90.0 and
Docker 29.9.3. Both fixtures ran as glibc amd64 containers, installed a local
Feature, failed a required hook before reaching `waitFor`, and completed that
hook on rebuild. They preserved named-volume data from the original
container and ran the later hook separately. These Docker checks ran
independently of UTM.
