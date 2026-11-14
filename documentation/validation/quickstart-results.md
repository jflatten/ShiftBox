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
| Section 4 | Native Chrome lifecycle and retained checkout through repair and retry, plus the exact Go journal. A subsequent Chrome run verified actual developer-home and named-volume data after stop/start and rebuild on the same workspace. |
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

T125 remains open for the outstanding scenarios in sections 3–10. The
[release tracker](mvp-v1-v14.md) records the remaining live checks.
