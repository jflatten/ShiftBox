# MVP quickstart results

The following commands were exercised on November 14, 2026 at
`ea6c1b8`. [Engineering checks](final-checks.md) records tool versions
and the build and browser results.

| Quickstart command | Result |
| --- | --- |
| `go test ./...` | Passed with `-p 1` for serial package execution. |
| `go test -race ./...` | Passed with `-p 1` for serial package execution. |
| `go test -tags=integration ./tests/integration/...` | Passed with default fixture settings. |
| `go test -tags=utm_release ./tests/release/...` | Passed with native mutation cases skipped by their fixture gate. |
| `npm --prefix ui ci` | Passed from the checked-in package files in a disposable copy. The offline attempt missed an uncached package. The normal registry-backed install succeeded and reported three low and 16 moderate advisories. |
| `npm --prefix ui test` | All 86 tests passed. |
| `npm --prefix ui run build` | Production export passed. |
| `npm --prefix ui run test:e2e` | The follow-up fixture run passed 64 checks across Chrome, Firefox, and WebKit using the rebuilt production export. Native workspace and sharing journeys remain pending. |

## Migration preflight fixture

On November 14, 2026, the CLI built from `4080728` ran against a private
copy of the activated SQLite acceptance installation at schema 27. The
copy used the trusted localhost test certificate, the installed Dex
`v2.46.1` binary, and UTM 5.0.8 inventory. The backup service captured and
verified an encrypted management archive with the SQLite and Dex stores.
The saved acceptance installation was left intact.

| Fixture state | CLI result |
| --- | --- |
| Before the verified backup, using the archived manifest | Exit 7 with `migration.backup_required` and `migration.tools_incompatible`. |
| After the backup, using the archived manifest | Exit 7 with `migration.tools_incompatible`. The manifest's Dex version is a build revision, while the executable reports `v2.46.1`. Its SHA-256 digest matches. |
| Verified backup and a test manifest using `v2.46.1` and the tested controller digest | Exit 0 with `ready: true` and zero blockers. |
| Stopped workspace record with a previous guest release | Exit 7 with `migration.guests_not_current` and the matching workspace in `guestBlockers`. |
| Same workspace record with the deployed guest release and protocol | Exit 0 with `ready: true` and zero blockers. |

The workspace row in the table was synthetic. On the subsequent `mvp-rc1`
run, the check-only CLI reported ready with a real enrolled retained UTM
workspace. The native signed maintenance fixture verified rollback and a
successful replacement, including one retained-guest blocker after rollback
and zero after replacement.

## Frozen candidate follow-up

| Quickstart scope | Verified result |
| --- | --- |
| Section 3 | Protected SQLite setup, image validation and fresh administrator sign-in on `mvp-rc1`. A separate native run verified distinct VM and disk ownership across installations and deletion of both test clones. |
| Section 4 | Native Chrome lifecycle and retained checkout through repair and retry, plus the exact Go journal. A subsequent Chrome run verified developer-home and named-volume data after stop/start and rebuild. With the rc2 runtime, real Docker fixtures verified that the isolated repair workbench opened after configuration, build, hook and IDE failures and preserved the Git index and a repair edit. |
| Sections 8–9 | `mvp-rc2` passed authenticated native enrollment and stopped-state signed maintenance with rollback and successful replacement. A native restart after the stopped guest's certificate expired verified a fresh identity, cleared enrollment media, and the same VM and owned disk. A running guest rejected an unrelated controller CA and a future-dated server certificate and resumed polling after each fault was cleared. |
| Section 10 | Populated SQLite backup, protected restore, native provider and authority review, explicit activation and copied-grant revocation. PostgreSQL passed encrypted backup, six refused restore cases, and Chrome review and reactivation with retained users. On `mvp-rc2`, PostgreSQL recovery matched and preserved the original stopped native VM and disk and rejected copied and recovery sessions. |

The engineering rerun after `17374f4` passed the serial Go suite and race
suite, the tagged integration and release packages under their default
fixture settings, a fresh `npm ci`, all 89 UI unit tests, and the production
export. `npm ci` reported three low and 16 moderate advisories. The browser
rerun passed 26 fixture checks each in Chrome, Firefox, and WebKit. The
separate Chrome browser-storage case also passed. Live sharing and preview
cases require their dedicated fixture.

The restart-focused race suite also passed, including the populated
PostgreSQL setup interruption after source retirement. The first PostgreSQL
test invocation supplied the CA twice, in the setup URI and in the separate
trust input. The corrected fixture uses the separate trust input.
The separately owned PostgreSQL replacement validation and activation tests
also passed again with `-race` against fresh dedicated schemas.

## Protected PostgreSQL setup in Chrome

The pinned UTM image passed validation during protected setup. Activation
ended the restricted browser session and locked the old SQLite installation.
Starting the controller with the retired configuration failed. The first
PostgreSQL sign-in returned 401 after Dex accepted the administrator's
password. The session regression reproduced the failure with PostgreSQL's
integer password-change flag. After an explicit integer comparison fixed
the query, the race-enabled regression passed on both profiles.

On the rerun, Chrome completed protected setup in 54.9 seconds and a
separate Chrome session signed in to the active PostgreSQL installation
as administrator. The controller was rebuilt with the embedded browser
export and the `mvp-rc1` release ID. Its SHA-256 was
`49e2d06a105e07e5f2441312bc740cb931648d154faa1bc364809e388e24f0b8`.

The `mvp-rc2` controller and guest image repeated the protected setup.
Chrome finished in 50.6 seconds. The old bootstrap browser session was
rejected, the old SQLite configuration failed on startup, and a fresh
Chrome session signed in as administrator on PostgreSQL. The [release
tracker](mvp-v1-v14.md) records the controller and guest digests.

## SQLite identity and migration preflight on `mvp-rc2`

Chrome completed protected SQLite setup in 54.6 seconds. A fresh
administrator session confirmed the management listener hid setup.
The People page created a local member, whose first sign-in was
restricted until the temporary password changed. The browser verified
that a member could list workspaces after that change and could not use
the administrator's membership API.

An administrator reset held open across the member's password change
was refused with HTTP 412. The browser closed the stale dialog, refreshed
People, and completed a reset. The old password and normal session were
then rejected. The replacement temporary password required another
change. Disabling the member revoked the replacement session, denied
fresh sign-in and preserved administrator access. The database recorded
the reset, password changes and disablement, and the expected session
revocation reasons. Separate race-enabled tests using the pinned Dex
process passed local membership checks and an organization sign-in
round trip against a local OpenID Connect issuer.

The check-only migration command on the same SQLite installation returned
exit 7 with only `migration.backup_required` before a backup. After the
controller created and verified an encrypted management archive, preflight
against the exact `mvp-rc2` manifest returned exit 0 with zero blockers.
The fixture had no retained workspaces.

The native UTM upgrade from `mvp-rc1` to `mvp-rc2` verified a
migration blocker for the stopped previous-release guest. The blocker
survived rollback and cleared after the signed rc2 agent authenticated.
The fixture called the retained-guest check used by preflight. See
[UTM validation](utm-stack.md#upgrade-across-release-ids).

## Protected setup restarts on `mvp-rc2`

For section 3, Chrome drove the exact rc2 SQLite setup through six
process restarts. The setup command was restarted after administrator
delivery, password change, database validation, browser certificate
validation, native platform validation, and capacity save. Each
invocation opened a new protected loopback address. A fresh browser
context confirmed the saved step and signed in to continue.

Only the first invocation delivered a temporary password. The password
change survived restart, the setup and guest CA files kept their original
digests, and the installation identity, administrator ID, Dex account
count, and SQLite schema were stable throughout. The completed database
contained one administrator, one available default platform artifact,
one capacity record, and one successful activation. UTM confirmed the
platform validation VM was removed.

Activation invalidated the restricted session. After the setup process
stopped, the exact rc2 management process required a fresh sign-in and
accepted the original administrator with the saved replacement password.
The separate PostgreSQL cutover interruption is recorded above.

## PostgreSQL preflight with a native retained workspace

The exact `mvp-rc2` controller and PostgreSQL schema authenticated an
`mvp-rc2` native guest and stopped it while retaining its owned disk.
The fixture stopped after enrollment and before project setup.
`migrate preflight` against the exact candidate manifest returned exit 7
with only `migration.backup_required` and no guest blockers. After the
controller created and verified the PostgreSQL management archive, the
same command returned exit 0 with zero blockers. UTM still reported the
original stopped VM and disk. Test cleanup verified deletion.

On the same installation, Chrome checked the active UTM 5.0.8 provider
capabilities. The controller advertised only shared networking and
returned HTTP 409 for attempts to add no-network and allowlist policies.

## Guest CA lifetime preflight

The `mvp-rc2` check-only CLI ran against a private copy of a previously
restored SQLite installation. The test presented its retained workspace
record as stopped and on the previous release. With the original valid
CA, preflight listed the stopped guest and required a new backup. A
disposable CA with five hours remaining also produced
`migration.guest_ca_expiring`. With an expired CA, preflight returned
`migration.installation_unverified` before producing an assessment.
All three calls returned exit 7 and left the copied CA file and stopped
workspace record intact. The test used the stored workspace record
without starting or changing a VM.

Separately, a running UTM guest lost authenticated contact after its
temporary installation CA expired on the wall clock. The controller
refused to reload the expired CA. UTM still reported the same running VM
and owned disk. See [the native CA test](utm-stack.md#expired-certificate-authority).

For section 8, the packaged guest also renewed a short first leaf over its
authenticated channel. It used a different key and a new eight-hour
certificate before the first leaf expired. The still-valid predecessor was
refused for ordinary access, and the agent kept its original running VM
and disk. See [native certificate renewal](utm-stack.md#native-certificate-renewal).

A separate native rc2 run verified actual guest-clock correction. A
disposable NoCloud seed moved the Linux guest clock one hour behind, and
fresh TLS contact stopped. Correcting the guest clock restored contact
with the original guest certificate, VM, and disk. See [the native clock
test](utm-stack.md#guest-clock-correction).

The native `mvp-rc2` revocation run denied the old certificate while the
guest was running. After a host stop, the start workflow issued no recovery
grant and kept the disk. Normal deletion verified provider absence, released
the disk reservation, and left the deleted identity denied. See [the native
revocation test](utm-stack.md#revoked-native-guest-and-verified-deletion).

The `mvp-rc2` cloned-media run verified one-time enrollment on two native
UTM VMs. After the first guest authenticated, a fresh VM booted with a
copy of its enrollment image. The packaged guest agent reported denial,
the second VM obtained no identity, and the original stopped VM retained
its certificate binding and disk. See [cloned enrollment media](utm-stack.md#cloned-enrollment-media).

The `mvp-rc2` retention fixture exercised outage grace, fresh activity,
the full shutdown window, and actual UTM deletion. Simulated partial
cleanup and a lost provider reply both kept the disk reservation until
native cleanup was verified. See [workspace operations](workspace-operations.md#native-retention-and-disk-accounting).

The native capacity fixture independently reached the configured CPU,
memory and disk limits. Both launch attempts in each case reported the
limiting resource and left the running VM unchanged, with no queued
operations. The launch API contract returned the disk refusal when
reported free space was below the requested disk allocation.

For section 6, a fixture launch worker exited after native UTM import
and before recording the provider response. The provisioning journal
and request key survived. The exact restarted rc2 controller enrolled the
guest on the original VM and disk. Chrome showed the original provision
and enrollment steps complete. See [native launch recovery](workspace-operations.md#native-launch-recovery-after-worker-exit).

Chrome also raced Start and confirmed Delete on two stopped native
workspaces. Both request orders admitted one operation and refused the
second with HTTP 412. Start recovered the same VM and retained disk.
Delete verified provider removal and released the disk reservation.
See [the competing-request results](workspace-operations.md#competing-native-start-and-delete-requests).

Chrome and the Go operations acceptance test passed together on the
rc2 controller. The live preview case used an enrolled loopback guest
client and real mutual TLS, with two local users. It verified separate
cookies, cross-origin denial and selective WebSocket revocation. The
same seven Chrome cases then passed with the exact rc2 controller and
packaged agent in a native UTM VM. A temporary NoCloud service supplied the
preview. The live case verified host-only cookies, blocked cross-origin
entry, and closed only the administrator's socket. The Go operations
acceptance passed with the race detector, and cleanup verified removal of
the VM and disk. See [workspace operations](workspace-operations.md).

For section 7, a separate native `mvp-rc2` run verified the packaged
agent's tunnel using a disposable WebSocket service in UTM. Offboarding
closed the stream while the VM was running, the agent reported that the
controller denied subsequent polling, and fresh stream entry was refused.
The workspace then stopped with its disk retained. The GitHub-backed
project and IDE rerun remains pending. See [native preview tunnel
revocation](utm-stack.md#native-preview-tunnel-revocation).

## Final-candidate rerun of the guest secret command

Quickstart section 5 ran the exact `mvp-rc2` Linux guest binary in a
disposable Ubuntu 24.04 container. The agent used its ordinary Unix
socket and a real enrollment certificate to reach the exact rc2
controller over mutual TLS. The test used the existing Ubuntu base
image after finding that the earlier repair fixture image had been
cleaned up.

| Condition | Exit | Output verified |
| --- | --- | --- |
| Authorized stored value | 0 | Exact stdout bytes, including the trailing newline |
| Intentional empty value | 0 | Empty stdout and stderr |
| Missing name | 3 | Empty stdout and a diagnostic on stderr |
| Workspace authorization withdrawn | 4 | Empty stdout and a diagnostic on stderr |
| Controller stopped while the agent was still serving its socket | 5 | Empty stdout and a diagnostic on stderr |

The denial used a stopped desired state in the disposable workspace
record. The container hosted the production guest agent and command.
The native private Git checkout and owner-context authorization checks
remain part of the [full sharing journey](template-sharing.md).

## Fresh engineering runs on `mvp-rc2`

The frozen candidate's `npm ci`, 89 UI unit tests, and production export
passed. `npm ci` reported three low and 16 moderate advisories. The first
browser invocation ran Chrome but could not start Firefox or WebKit
because their locked binaries were absent. After installing those versions
in a disposable directory, the suite passed 79 checks across Chrome
157.0.8103.51, Firefox 159.0, and WebKit 27.2, including the separate
Chrome storage check. It skipped 14 cases gated on a live installation
or the selected browser.

A fresh Go run under `umask 077` exposed two permission fixtures that
relied on `WriteFile` to create a world-readable file. The mask made
both files private, which invalidated their negative assertions. The
updated fixtures set mode 0644 explicitly before testing refusal. Both
tests passed with the race detector under `umask 077`. On the unchanged
frozen checkout, the standard and race suites plus both tagged suites
passed without cached test results under `umask 022`. The tagged runs
used the default fixture gates with the rc2 manifest override. The
separate native and database evidence is recorded above.

## T095 workspace update contract gap

The section 9 contract audit enabled `SHIFTBOX_TEST_WORKSPACE_DELETE_UPDATE=1`
and ran `TestWorkspaceDeleteAndUpdateAPI` with the race detector. The
test's update request without a scheduled campaign received HTTP 400
`workspace.invalid_action` where it expected HTTP 409. Explicit deletion
passed. The release contract lists `update` as a workspace action, and
the maintenance design lets a workspace owner update before the scheduled
deadline. T095 is open again. The existing maintenance scheduler and
signed-update results above cover deadline-driven updates.

T125 remains open. The [release tracker](mvp-v1-v14.md) records the
remaining live checks.
