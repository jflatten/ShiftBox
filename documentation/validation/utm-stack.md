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

The `mvp-rc1` rerun of `TestUTMGuestEnrollment` passed with `-race` in
74.75 seconds. The frozen guest authenticated after 50 seconds. The
administrator offboarding route stopped the exact clone and preserved its
owned disk and disk reservation while releasing compute. The test then
removed the disposable clone.

## Expired guest recovery

The race-enabled `TestUTMExpiredGuestRecovery` passed against the
`mvp-rc1` image in 289.46 seconds. The guest first authenticated with a
short-lived certificate. The test stopped the guest and waited for that
certificate to expire. The ordinary start operation issued fresh
enrollment media. The guest authenticated with a new key and certificate
on the same VM and owned disk. The previous grant was invalidated and the
one-use media was cleared after enrollment.

Use the same native fixture settings as `TestUTMGuestEnrollment` plus
`SHIFTBOX_TEST_UTM_EXPIRED_GUEST=1`. The test caps the issuer validity in
memory for the first enrollment and uses the original issuer for recovery.

## Controller trust failures

The race-enabled `TestUTMGuestRejectsControllerTrustFailures` passed
against the `mvp-rc1` image in 113.26 seconds. After an authenticated
poll, the test restarted the controller's guest listeners twice on the
same addresses. One restart used an unrelated CA. The other used the
original CA to issue a server certificate dated one hour ahead. Each
fault ran for 20 seconds without recording authenticated contact. After
each fault, the original CA and normal time restored polling with the
same guest certificate, running VM and owned disk.

Use the same native fixture settings as `TestUTMGuestEnrollment` plus
`SHIFTBOX_TEST_UTM_TRUST_FAILURES=1`. The future certificate simulates
clock skew at the controller listener. The machine clocks are unchanged.

## Signed maintenance fixture

`TestUTMGuestMaintenance` reuses the enrollment fixture and only needs one
disposable clone. Set the UTM mutation gate, bundle, storage, and guest host
as for `TestUTMGuestEnrollment`. Set `SHIFTBOX_TEST_UTM_ROLLBACK_RELEASES`
and `SHIFTBOX_TEST_UTM_UPDATE_RELEASES` to distinct private release catalogs
signed with the test publisher key baked into that bundle. Each catalog
must contain the image's guest release ID and protocol. The rollback binary
is a Linux amd64 program that exits before making an authenticated poll.
The replacement is a working build of the pinned guest agent with a distinct
digest from the bundled agent. Use the fixture publisher key for both trials.

Run `go test -tags utm_release ./tests/release -run '^TestUTMGuestMaintenance$'
-count=1 -v -timeout=20m`. The test stops the enrolled guest through the
lifecycle journal and checks supervisor rollback, stopped-state restoration,
and the retained-guest migration gate. It then schedules a replacement campaign
and checks the authenticated activation, retained disk identity, and migration
gate again.

On November 14, the `mvp-rc1` UTM guest staged the signed failing fixture
`a7e6a456b341666ad0b0ea88659434c5dc7f51066f183c1802d1995dbae63c83`,
reported supervisor rollback over mutual TLS, and returned to stopped with
its owned disk. The campaign was blocked and the live retained workspace
appeared in the guest upgrade gate. The first run's replacement campaign
exceeded the shared eight-minute test deadline after a slow native boot.
The race-enabled rerun passed both campaigns in 503.65 seconds. It accepted
the replacement fixture digest
`357f83d685255b633cc2825b3300552a80fc38391fd578bbdbfcf2a5ee4a3b31`,
verified the authenticated activation, retained the same disk, restored
stopped power and released compute capacity. The retained-guest checks
reported one blocker after rollback and zero after replacement. Both trials
used the same release ID with different signed binaries. The current and
previous release admission tests exercise version compatibility separately.
Test cleanup removed the disposable clone.

## Dev Container fixtures

`TestDevContainerReleaseFixtures` passed for the digest-pinned Debian
prebuilt image and Ubuntu Dockerfile using Dev Container CLI 0.90.0 and
Docker 29.9.3. Both fixtures ran as glibc amd64 containers, installed a local
Feature, failed a required hook before reaching `waitFor`, and completed that
hook on rebuild. They preserved named-volume data from the original
container and ran the later hook separately. These Docker checks ran
independently of UTM.
