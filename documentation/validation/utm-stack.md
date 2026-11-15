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

## Separate installation copies

The race-enabled `TestUTMCatalogValidation` passed against the pinned
`mvp-rc1` image in 90.51 seconds. The catalog first booted a disposable
candidate and verified removal before publishing the artifact. Two
installations then created native VMs from that artifact with the same
workspace ID and generation. UTM reported distinct VM and disk
identities and separate ownership markers. Deleting one clone left the
other stopped with its original disk. The adapter verified both
deletions, and both clones were absent afterward.

## Native certificate renewal

`TestUTMGuestRenewsBeforeExpiry` passed with `-race` in 190.36 seconds on
UTM 5.0.8 and the pinned `mvp-rc2` guest. The fixture shortened only the
first leaf's lifetime so the packaged agent reached its renewal window
during the run. After enrollment, the controller restarted its listeners
on the same addresses with the original CA and ordinary issuance limits.

The agent generated a different key, received a new eight-hour
certificate, and used it to authenticate before the first leaf expired.
The still-valid predecessor was refused for ordinary guest access.
No second enrollment grant was issued, and the running VM and disk kept
their original identities. Both race-enabled renewal contract tests also
passed, including retry of a lost renewal response. Set
`SHIFTBOX_TEST_UTM_NATIVE_RENEWAL=1` with the normal native fixture settings.

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

The race-enabled rerun on the `mvp-rc2` image passed in 284.84 seconds.
The restarted guest used a replacement identity on the same VM and
owned disk, and the controller cleared the one-use media.

## Cloned enrollment media

`TestUTMClonedEnrollmentMedia` passed with `-race` in 218.46 seconds on
`mvp-rc2` and UTM 5.0.8. The fixture privately copied the enrollment
image before the first workspace booted. The original guest authenticated,
the controller consumed the grant and cleared its media, and the original
VM stopped with its disk retained.

A second UTM workspace booted the same pinned platform with the copied
image. The packaged guest agent reported enrollment denied and produced
no guest identity file. The controller had no registration for the clone
and retained the original guest certificate, key binding, and owned disk.
The test removed both disposable VMs and the private image copy. Set
`SHIFTBOX_TEST_UTM_CLONED_ENROLLMENT=1` with the normal native settings.

The frozen `mvp-rc3` image passed the same test with the race detector in
464.35 seconds. Its first guest authenticated after 5 minutes 5 seconds.
The packaged agent refused the copied media in the second guest. The
test verified that the clone had no controller registration and that
the original certificate, key binding, and disk were retained. Both
disposable VMs were absent from UTM after the test.

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

The race-enabled rerun on `mvp-rc2` passed in 104.86 seconds. Both
faults withheld authenticated contact until valid trust returned.

## Guest clock correction

`TestUTMGuestClockRecovery` passed with `-race` in 132.71 seconds on
UTM 5.0.8 and the exact `mvp-rc2` platform disk. A temporary NoCloud seed
ran only on the disposable workspace after its first authenticated poll.
The seed reported the Linux guest clock after moving it one hour back.
The runtime restarted its TLS listeners with the original CA on the same
addresses, so the guest had to establish a fresh connection. Authenticated
contact stopped for 30 seconds. After the seed corrected and reported the
guest clock, polling resumed with the same certificate, VM, and owned disk.
The fixture removed the clone at the end of the run.

Set `SHIFTBOX_TEST_UTM_GUEST_CLOCK=1` with the normal native fixture settings.
The seed runs in the cloned guest. Its temporary server uses a separate
guest-reachable port and serves only the test script and clock values.
The first attempt to use UTM's guest command interface failed because the
image had no running QEMU guest agent. See [cloud-init NoCloud discovery](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html#discovery-configuration).

## Expired certificate authority

The race-enabled `TestUTMGuestRejectsExpiredCA` passed on `mvp-rc2`
in 269.37 seconds. The test generated a private installation CA with
four minutes of remaining validity. The native guest authenticated
before expiry. After expiry, polling no longer advanced authenticated
contact, and the controller refused to reload the stored CA. UTM
reported the same running VM and owned disk until fixture cleanup.

Set `SHIFTBOX_TEST_UTM_EXPIRED_CA=1` with the normal native settings.
The test waits for wall-clock expiry and uses the ordinary TLS checks.

## Revoked native guest and verified deletion

`TestUTMRevokedGuestStaysIneligible` passed with `-race` in 104.73 seconds
on `mvp-rc2` and UTM 5.0.8. After initial authentication, the fixture
marked the guest registration revoked in the disposable database. The
controller refused the original certificate, and authenticated contact did
not advance during 25 seconds with the same VM running. The host stopped
the revoked guest, and the ordinary stop operation reconciled the retained
disk without relying on a guest acknowledgement.

The ordinary start operation reported that the revoked identity was
ineligible for automatic recovery. It issued no enrollment grant and left
the original VM stopped with the same disk. The ordinary deletion operation
confirmed provider absence, released the disk reservation, and recorded
the registration as deleted. The original certificate remained denied.
Set `SHIFTBOX_TEST_UTM_REVOKED_GUEST=1` with the normal native fixture settings.

## Native preview tunnel revocation

`TestUTMNativeTunnelRevocation` passed with `-race` in 128.92 seconds on
`mvp-rc2` and UTM 5.0.8. A temporary NoCloud seed started a localhost
WebSocket fixture on the disposable VM. The packaged agent advertised the
configured preview port, registered its ordinary mutual TLS tunnel, and
delivered a WebSocket upgrade and frame to the controller.

The normal administrator membership handler disabled the workspace owner.
An injected uncertain provider response held the VM running while the
controller closed the established stream. The packaged agent reported
identity denial on a subsequent poll, and an attempt to open a new preview
stream failed. The offboarding stop then completed against real UTM and
retained the original disk. Fixture cleanup deleted the VM.

Set `SHIFTBOX_TEST_UTM_TUNNEL_REVOCATION=1` with the normal native fixture
settings. The [integrated Chrome preview run](workspace-operations.md)
also passed with the packaged UTM agent. The release tracker records the
remaining GitHub-backed project and IDE checks.

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

On `mvp-rc2`, the race-enabled fixture passed in 491.25 seconds.
The signed failing digest
`3086460ccbd8dab34db2eafede03b55acc04dfe7b3cd125129b5bae1653238f9`
rolled back, returned the workspace to stopped and left one retained-guest
migration blocker. The signed replacement digest
`7f1117f76060959899ee0a52b22f710a20fe7ba20446d0b6c060eba1321e7f7f`
authenticated after activation, returned the same VM to stopped and
cleared the blocker. The same owned disk survived both trials. These
two signed builds shared the `mvp-rc2` release ID.

### Upgrade across release IDs

`TestUTMGuestReleaseUpgrade` sets the controller's current release from
`SHIFTBOX_TEST_UTM_TARGET_BUNDLE` and boots the previous image from
`SHIFTBOX_TEST_UTM_BUNDLE`. Set the same two signed fixture catalog
variables as for `TestUTMGuestMaintenance`. The successful update must
match the SHA-256 digest of the agent bundled in the target image.

The race-enabled test passed in 508.98 seconds using the pinned
`mvp-rc1` and `mvp-rc2` exports. The controller accepted the previous
guest, then reported one blocker while it was stopped. The first run
ended at campaign admission because the isolated test catalog marked the
target image rejected. On the corrected run, the signed failing update
rolled the guest back to `mvp-rc1` and the blocker remained. The signed
`mvp-rc2` agent authenticated after activation and cleared the blocker.
Both trials restored stopped power on the same owned disk. Cleanup
removed the disposable VM.

The race-enabled `SHIFTBOX_TEST_UTM_EARLY_MAINTENANCE=1` rerun passed in
505.80 seconds. The first, deadline-driven trial rolled the guest back to
`mvp-rc1` and preserved the upgrade blocker. The owner then requested the
replacement update 20 minutes ahead of its scheduled deadline. The same
native VM authenticated with the signed `mvp-rc2` agent before the deadline,
returned to stopped on its original disk, and cleared the migration
blocker. The fixture used the updated controller code and the existing
pinned rc1 and rc2 guest bundles.

The signed `mvp-rc2` to `mvp-rc3` run passed with the race detector in
513.35 seconds. The stopped RC2 guest blocked migration. The first signed
trial failed activation, returned to RC2 on the same stopped disk, and
left one retained guest blocker. The owner then invoked the scheduled
replacement before its deadline. The guest authenticated with the exact
RC3 agent digest in the frozen image manifest, returned to the stopped
state with the original disk, and cleared the blocker. Both trials kept
the disk reservation and released compute after the VM stopped. UTM then
confirmed removal of the disposable VM.

## Dev Container fixtures

`TestDevContainerReleaseFixtures` passed for the digest-pinned Debian
prebuilt image and Ubuntu Dockerfile using Dev Container CLI 0.90.0 and
Docker 29.9.3. Both fixtures ran as glibc amd64 containers, installed a local
Feature, failed a required hook before reaching `waitFor`, and completed that
hook on rebuild. They preserved named-volume data from the original
container and ran the later hook separately. These Docker checks ran
independently of UTM.

The frozen RC3 source repeated both reference projects with the same
Dev Container CLI and Docker versions. `TestDevContainerReleaseFixtures`
passed with the race detector in 18.01 seconds.

### Trusted repair after setup failures

The race-enabled `TestRepairDockerFailures` passed all four cases in
26.47 seconds using the `mvp-rc2` guest runtime code and a disposable
Docker Desktop fixture. The fixture built the repair image from the
pinned Linux amd64 Ubuntu digest and verified code-server 4.145.2
archive used by Packer. It used the real Dev Container CLI 0.90.0 and
Docker 29.9.3.

The normal setup failed at configuration parsing, a Dockerfile build
step, a required project hook, and IDE startup with a missing
distribution. In each case, the repair workbench served the retained
checkout over its Unix socket with no container network and a
read-only root. The repair environment kept its own settings, left
the hook log and Git index intact, and preserved an edit after exit.
The fixture exercised the repair container over local Docker. The
frozen candidate's browser and trusted repair origin remain a separate
native validation.

The frozen RC3 source repeated all four failure cases with the race
detector in 27.74 seconds. The first fixture extracted the public IDE
files with a restrictive mask, which prevented the non-root editor
from loading. The rerun used the platform recipe's permissions and a
writable temporary IDE home. Configuration, build, hook, and IDE failures
all reached the isolated repair workbench and preserved the checkout.

Run with `SHIFTBOX_TEST_REPAIR_IMAGE` set to the local repair image's
`sha256:` ID and `SHIFTBOX_TEST_DEVCONTAINER_CLI` set to the pinned CLI.
Use `go test -race -tags utm_release ./internal/guest -run '^TestRepairDockerFailures$' -count=1 -v`.
