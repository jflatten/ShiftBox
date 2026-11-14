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

The workspace row was synthetic. Real guest convergence and the signed
update and rollback exercise still require a frozen release candidate and
a working UTM guest. The CLI performed checks only. The disposable schema
was prepared separately before the run.

T125 remains open for the live scenarios in sections 3–10 of the
quickstart. UTM still reports the disposable workspace as `starting`.
Prior native evidence and the remaining requirements are recorded in the
[release tracker](mvp-v1-v14.md).
