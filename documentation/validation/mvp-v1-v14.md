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

## Candidate provenance

The original `mvp-rc1` controller, guest agent, Dex executable, UTM
executable, installed Packer fork, downloaded Ubuntu image, and
code-server archive matched the SHA-256 values in the checked-in release
manifest. The pinned UTM bundle's own artifact manifest SHA-256 was
`2252ec7cd73f4bd6d1af89365e9bee6734af443924281c6fe46d685cd285844b`.

The PostgreSQL browser checks used the same guest image and a rebuilt
controller containing the sign-in fix. Its SHA-256 was
`49e2d06a105e07e5f2441312bc740cb931648d154faa1bc364809e388e24f0b8`.
The checked-in manifest still identifies the original controller. Repeat
the integrated gates on one final controller and guest pair before
changing the manifest.

The next validation candidate, `mvp-rc2`, pairs a controller and UTM
guest built from the same clean checkout at
`140f9de33593efc6b51c15d47e0c1396f5f002a6`. It includes the sign-in
fix and the updated workflow Stepper. Packer 1.14.2 completed the
pinned guest build in 9 minutes 28 seconds. The manifest parser and
local digest verification accepted the new build.

| Candidate artifact | SHA-256 |
| --- | --- |
| Controller | `d63ee325833d01da4f12c7d427a9d5a42d889502ea9b66b735e89365da249a2d` |
| Guest agent | `c50662346760cc9e5bf6611a76fd8eba7b5ec5d73a0419e0fff94109fd41d5fe` |
| UTM artifact manifest | `35918d82daa8384af876eba207fd630ed4d620aaeae064c52737467af8c23593` |

The clean checkout passed Go tests, the race suite, the tagged test
packages with their default fixture settings, UI type checking, and all
89 UI unit tests. Its exported UI passed 79 fixture checks across Chrome,
Firefox, and WebKit. The 14 skipped cases required a separate live
workspace or targeted browser fixture.

On this candidate, native catalog validation passed in 87.47 seconds.
Two isolated copies received distinct disks and were deleted independently.
Native enrollment and offboarding passed in 76.96 seconds. A separate
Chrome run completed protected PostgreSQL setup in 50.6 seconds, then
verified that the retired SQLite configuration failed and a fresh
administrator could sign in on the PostgreSQL controller.

The November 14 local browser runs used Chrome 157.0.8103.51 on macOS
26.6.2. The PostgreSQL fixture used server and client tools 18.7 with
separately owned application and Dex schemas. The application schema was
27. The trusted localhost test certificate expired December 14, 2026 at
13:14 UTC. The same public certificate was used by the test browser for
setup and management.

## Gate results

| Gate | Evidence available | Evidence needed for release |
| --- | --- | --- |
| V1 Platform and provider | Both `mvp-rc1` and `mvp-rc2` passed native boot and removal. The same workspace ID in two installations received distinct VM and disk identities, and deleting either clone left no owned resources. | Include the result in the final release audit |
| V2 Enrollment delivery | Native initial enrollment and native restart after certificate expiry passed on `mvp-rc2`. The restart replaced the guest identity, cleared enrollment media and preserved the owned disk. Controller tests cover lost enrollment replies. | Include the results in the final release audit |
| V3 Certificates | Rotation, expired-leaf rejection, and local TLS revocation tests. On `mvp-rc2`, a stopped guest recovered after its leaf expired and a running guest rejected an unrelated CA and a future-dated certificate. Actual CA expiry stopped authenticated contact and blocked CA reload with the VM and disk retained. | Exercise native guest-clock correction and final release revocation checks |
| V4 Compatibility | A native `mvp-rc1` guest authenticated as the previous release and blocked migration while stopped. Signed rollback preserved rc1 and its blocker. The exact signed rc2 agent cleared the blocker after authenticating. The exact rc2 CLI reported ready with a stopped rc2 UTM workspace and a verified PostgreSQL backup. | Include the results in the final release audit |
| V5 Networking | On `mvp-rc2`, UTM 5.0.8 advertised shared networking and returned 409 for no-network and allowlist policies. The native guest authenticated over shared networking. The rc1 browser journey cloned the private repository in that mode. | Verify private checkout and required exception behavior on the final candidate |
| V6 Work and data | Chrome verified project files through stop/start, rebuild, repair and retry, and both developer home and named-volume files through stop/start and a verified container replacement on `mvp-rc1`. The pinned Debian and Ubuntu Dev Container fixtures passed their hook failure and rebuild checks. | Exercise the remaining trusted repair failure variants on the final frozen candidate |
| V7 Reconciliation and deletion | The restart matrix passed for launch, start, stop, rebuild, repair, offboarding and deletion. Native UTM lifecycle, capacity and verified removal also have test evidence. | Complete the integrated operations journey and its native reservation checks |
| V8 Retention and maintenance | Activity, outage grace, stale intent, authorized provider deletion, deadline and safe-boundary tests. Native signed maintenance on `mvp-rc2` restored stopped power after both failure and success. | Complete the integrated browser operations journey |
| V9 Identity and bootstrap | Chrome on `mvp-rc2` passed protected setup and fresh sign-in on both data profiles. SQLite People verified forced first password change, stale-reset refusal, reset and disablement with session revocation. Native offboarding preserved the owned disk and released compute. The pinned Dex and a local OpenID Connect issuer passed the authorization round trip. | Include the results in the final release audit |
| V10 Backup and restore | Populated SQLite recovery on `mvp-rc1` and PostgreSQL recovery on `mvp-rc2` matched native retained UTM workspaces. The PostgreSQL run preserved the original stopped VM and disk and rejected copied and recovery sessions before normal sign-in. The earlier PostgreSQL run also covered six prepublication refusals. | Include both profiles in the final release audit |
| V11 Application authorization | Sharing, secret scope, and connection-grant contract tests | Run the two-account native browser journey and controller journal verifier |
| V12 Browser and tunnel | Local TLS tests cover origin checks, WebSockets, and live connection closure | Run the exact-stack operations journey for hostile preview isolation and revocation |
| V13 GitHub OAuth | Native fixture authorization and private clone, plus local refresh and revocation tests | Run two-account owner-context checks before and after revocation |
| V14 Accessibility and operability | Production-export Chrome keyboard suite and manual focus evidence | Complete remaining manual focus observations and moderated first-use study |

The [first-use study protocol](first-use-study.md) records eligibility and
first-attempt outcomes. Release evidence requires at least 10 eligible
participants and at least nine first-attempt successes.
