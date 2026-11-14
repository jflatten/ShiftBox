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
| V5 Networking | The frozen controller reported only shared networking from UTM 5.0.8 and returned 409 for no-network and allowlist policy requests. The native workspace authenticated to the controller and cloned the private repository over shared networking. | Repeat provider and required exception checks against the final frozen candidate |
| V6 Work and data | Chrome verified project files through stop/start, rebuild, repair and retry, and both developer home and named-volume files through stop/start and a verified container replacement on `mvp-rc1`. The pinned Debian and Ubuntu Dev Container fixtures passed their hook failure and rebuild checks. | Exercise the remaining trusted repair failure variants on the final frozen candidate |
| V7 Reconciliation and deletion | The restart matrix passed for launch, start, stop, rebuild, repair, offboarding and deletion. Native UTM lifecycle, capacity and verified removal also have test evidence. | Complete the integrated operations journey and its native reservation checks |
| V8 Retention and maintenance | Activity, outage grace, stale intent, authorized provider deletion, deadline and safe-boundary tests, and native signed maintenance restoring stopped power after failure and success | Complete the integrated browser operations journey |
| V9 Identity and bootstrap | Frozen SQLite setup, Dex login, and native offboarding with verified owned-disk and capacity accounting. The activated HTTPS controller returned 404 for the setup status route. Local PostgreSQL setup and upstream authorization tests passed. | Verify protected production-profile setup and locked bootstrap on the final frozen candidate |
| V10 Backup and restore | `mvp-rc1` passed populated SQLite backup, protected restore, native provider reconciliation, explicit recovery activation, and the journal check for copied authority revocation | Run the populated release gate for the production PostgreSQL profile |
| V11 Application authorization | Sharing, secret scope, and connection-grant contract tests | Run the two-account native browser journey and controller journal verifier |
| V12 Browser and tunnel | Local TLS tests cover origin checks, WebSockets, and live connection closure | Run the exact-stack operations journey for hostile preview isolation and revocation |
| V13 GitHub OAuth | Native fixture authorization and private clone, plus local refresh and revocation tests | Run two-account owner-context checks before and after revocation |
| V14 Accessibility and operability | Production-export Chrome keyboard suite and manual focus evidence | Complete remaining manual focus observations and moderated first-use study |

The [first-use study protocol](first-use-study.md) records eligibility and
first-attempt outcomes. Release evidence requires at least 10 eligible
participants and at least nine first-attempt successes.
