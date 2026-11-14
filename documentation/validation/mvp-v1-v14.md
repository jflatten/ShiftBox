# MVP release validation tracker

T124 remains open. `development-repair` has native stop/start, rebuild, and
repair evidence. The newer `mvp-rc1` build has completed native image
validation, protected setup, fresh administrator sign-in, and a verified
prelaunch management backup. The workspace run completed GitHub consent,
private checkout, initial browser IDE verification, and the retained-file
journey through stop/start, rebuild, repair, and retry. The strict controller
journal check passed on the same installation.
See [MVP workspace validation](mvp-workspace.md) for both sets of results.
Run the release gates against one frozen candidate and record its component
versions and hashes in `platform/manifests/release.json` before marking a
gate passed.

| Gate | Evidence available | Evidence needed for release |
| --- | --- | --- |
| V1 Platform and provider | Pinned Packer build and disposable native UTM boot, stop, and removal | Complete distinct clone identity and owned-disk lifecycle checks on the frozen candidate |
| V2 Enrollment delivery | Native initial enrollment. Controller tests prove fresh delivery before power-on after expiry and lost-reply reconciliation. | Native prolonged-stop re-enrollment and drive preservation |
| V3 Certificates | Rotation, expired-leaf rejection, and local TLS revocation tests | Native clock, long-offline, and CA failure cases on the pinned stack |
| V4 Compatibility | Current and previous guest admission, successful migration preflight with an enrolled `mvp-rc1` UTM workspace, and native signed rollback and replacement with the retained-guest gate changing from blocked to clear | Repeat release-gate checks against the final frozen candidate |
| V5 Networking | Pinned UTM capability and policy rejection tests | Confirm every advertised mode and required exception on the frozen candidate |
| V6 Work and data | The frozen candidate's native Chrome and Go checks verified saved-file retention through stop/start, rebuild, repair and retry. Local Dev Container fixtures verified named-volume retention. | Complete the retained home and named-volume checks on the frozen candidate |
| V7 Reconciliation and deletion | Lost provider replies, capacity, exact inventory, retention of reservations, and verified deletion tests | Full persisted-step restart matrix and native lifecycle evidence |
| V8 Retention and maintenance | Activity, outage grace, stale intent, authorized provider deletion, deadline and safe-boundary tests, and native signed maintenance restoring stopped power after failure and success | Complete the integrated browser operations journey |
| V9 Identity and bootstrap | Native SQLite setup and Dex login. Local PostgreSQL and upstream authorization tests. | Repeat locked setup, scoped production setup, and offboarding on the frozen candidate |
| V10 Backup and restore | `mvp-rc1` passed populated SQLite backup, protected restore, native provider reconciliation, explicit recovery activation, and the journal check for copied authority revocation | Run the populated release gate for the production PostgreSQL profile |
| V11 Application authorization | Sharing, secret scope, and connection-grant contract tests | Run the two-account native browser journey and controller journal verifier |
| V12 Browser and tunnel | Local TLS tests cover origin checks, WebSockets, and live connection closure | Run the exact-stack operations journey for hostile preview isolation and revocation |
| V13 GitHub OAuth | Native fixture authorization and private clone, plus local refresh and revocation tests | Run two-account owner-context checks before and after revocation |
| V14 Accessibility and operability | Production-export Chrome keyboard suite and manual focus evidence | Complete remaining manual focus observations and moderated first-use study |

The [first-use study protocol](first-use-study.md) records eligibility and
first-attempt outcomes. Release evidence requires at least 10 eligible
participants and at least nine first-attempt successes.
