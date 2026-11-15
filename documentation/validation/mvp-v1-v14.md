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

### `mvp-rc3`

The controller and UTM guest were built from the clean checkout at
`cab2289686eb2c213537fd37a43791fba2683c4f`. This candidate includes the
owner-requested workspace update before the scheduled deadline. Packer
1.14.2 completed the native guest build in 9 minutes 20 seconds. Local
verification matched every UTM bundle resource, the guest executable,
and the pinned Dex, UTM, Packer plug-in, Ubuntu archive, and code-server
archive against their manifests.

| Candidate artifact | SHA-256 |
| --- | --- |
| Controller | `a2f2efee3de12ff621cc38101942da8435a6105429b81f73dc0744b487308eda` |
| Guest agent | `5a8154cdacdc790bf92f5cbfd31fe4e9d5fb409a3e8bf1d44d620240de916c09` |
| UTM artifact manifest | `d46c21cd1a215cda6fca8fd561fd6f7154fcc361b4685186bf6d8a768b5a1721` |

The [RC3 quickstart results](quickstart-results.md#engineering-runs-on-mvp-rc3)
record the command checks on this revision. The checked-in release
manifest remains the original `mvp-rc1` record until the integrated
release gates pass on RC3.

Native catalog validation passed in 348.96 seconds. The candidate booted
and authenticated before publication. Two installations using the same
workspace ID received distinct VM and disk identities. Deleting the first
clone preserved the second, which was then removed independently.
The separate native enrollment and offboarding run passed in 351.80
seconds. The first authenticated poll arrived after five minutes.
Offboarding stopped the VM and preserved its original owned disk before
the test removed the VM. Both native checks ran with the race detector.

## Gate results

| Gate | Evidence available | Evidence needed for release |
| --- | --- | --- |
| V1 Platform and provider | `mvp-rc1`, `mvp-rc2`, and `mvp-rc3` passed native boot and removal. The same workspace ID in two installations received distinct VM and disk identities, and each clone was removed independently. | Include the RC3 result in the final release audit |
| V2 Enrollment delivery | Native initial enrollment passed on `mvp-rc3`. A second native RC3 guest rejected a copy of the consumed enrollment image and registered no identity. The original certificate and disk were preserved. Native restart after certificate expiry on `mvp-rc2` replaced the guest identity, cleared enrollment media, and preserved the disk. Controller tests cover lost enrollment replies. [Native enrollment replay evidence](utm-stack.md#cloned-enrollment-media) | Repeat expired identity recovery on RC3 |
| V3 Certificates | On RC3, revocation blocked polling and restart. Verified deletion released the disk and left the identity denied. On RC2, the guest renewed its key and certificate before expiry, resumed after leaf expiry, rejected invalid controller trust, recovered from clock error, and closed a native tunnel on offboarding. [Native trust evidence](utm-stack.md#native-certificate-renewal), [RC3 revocation results](utm-stack.md#revoked-native-guest-and-verified-deletion) | Repeat expiry, renewal, controller trust, and tunnel revocation on RC3 |
| V4 Compatibility | On the frozen `mvp-rc3` candidate, the previous RC2 guest authenticated and blocked migration while stopped. Signed rollback preserved RC2 and its blocker. The exact signed RC3 agent cleared the blocker after authenticating. In the native PostgreSQL installation, the RC3 CLI refused preflight before a verified backup and reported ready afterward. [Signed upgrade evidence](utm-stack.md#upgrade-across-release-ids), [RC3 preflight results](quickstart-results.md#protected-postgresql-setup-preflight-and-recovery) | Include these results in the final release audit |
| V5 Networking | On `mvp-rc3`, the controller reported that UTM 5.0.8 supported shared networking and returned 409 for no-network and allowlist policies. The focused race tests passed admission, launch rechecks, and the stopped-state guard after failed verification. The RC1 browser journey cloned the private repository over shared networking. | Verify the private checkout on RC3 |
| V6 Work and data | The RC3 reference projects passed required-hook failure and retry with named-volume retention in Docker. The RC3 guest source also served trusted repair after configuration, build, hook, and IDE failures with the pinned Ubuntu and code-server inputs. Chrome on RC1 verified retained checkout, developer home and named-volume files across lifecycle actions. [Repair results](utm-stack.md#trusted-repair-after-setup-failures) | Repeat retained-data and trusted repair browser checks on the final native candidate |
| V7 Reconciliation and deletion | The restart matrix passed for launch, start, stop, rebuild, repair, offboarding and deletion. Native rc2 recovered the original VM and disk after exit during import. Competing Start and Delete requests admitted exactly one operation in each ordering. The winning Start kept its disk, and the winning Delete released its disk reservation after confirmed removal. Native capacity and retention tests covered refusals and partial cleanup. [Native concurrency evidence](workspace-operations.md#competing-native-start-and-delete-requests) | Complete the remaining native project lifecycle checks |
| V8 Retention and maintenance | Native retention exercised outage grace, stale-candidate refusal after fresh activity, the full shutdown window and verified deletion. The frozen RC3 source and guest passed native signed rollback and an owner-requested replacement before the deadline from RC2. Both paths preserved stopped power and the same disk. The contract verifies that other targets wait until the deadline. [Native maintenance results](utm-stack.md#upgrade-across-release-ids) | Repeat native retention on RC3 |
| V9 Identity and bootstrap | Chrome on `mvp-rc3` passed six protected SQLite setup restarts and fresh management sign-in on both data profiles. The SQLite run preserved one administrator, schema, trust identity, and default platform artifact. The PostgreSQL run refused startup with the retired SQLite configuration. Chrome on RC2 passed SQLite administrator reset and disablement with revocation. Native offboarding preserved the owned disk and released compute. The pinned Dex and a local OpenID Connect issuer passed the authorization round trip. [RC3 setup restart results](quickstart-results.md#protected-setup-restarts) | Repeat the user revocation scenarios on RC3 |
| V10 Backup and restore | The exact RC3 CLI and Chrome completed SQLite and PostgreSQL restore and explicit recovery activation with native retained workspaces. The replacements preserved the original stopped VMs and disks, revoked the prior sessions, and accepted fresh normal sign-ins. SQLite refused five invalid inputs before replacement publication. [SQLite results](backup-recovery.md#sqlite-recovery-on-mvp-rc3), [PostgreSQL results](quickstart-results.md#protected-postgresql-setup-preflight-and-recovery) | Verify live IDE continuity and copied origin-grant revocation on RC3 |
| V11 Application authorization | Chrome on `mvp-rc2` passed template grant and archive checks, secret metadata checks, and IDE-route connection revocation with two local Dex users and an enrolled local guest client. The guest verified effective secret precedence over mutual TLS. See [sharing validation](template-sharing.md). | Run the two-account native browser journey and controller journal verifier |
| V12 Browser and tunnel | Chrome on the exact rc2 controller and packaged UTM agent denied ungranted requests and cross-origin mutation, separated cookies, and delivered both preview WebSockets. Revocation closed only the administrator's socket and preserved owner access. A separate native run closed the owner's stream on offboarding. [Browser and native preview results](workspace-operations.md) | Include the results in the final release audit and finish the native project IDE journey |
| V13 GitHub OAuth | Native fixture authorization and private clone, plus local refresh and revocation tests | Run two-account owner-context checks before and after revocation |
| V14 Accessibility and operability | Production-export Chrome keyboard suite and manual focus evidence | Complete remaining manual focus observations and moderated first-use study |

The [first-use study protocol](first-use-study.md) records eligibility and
first-attempt outcomes. Release evidence requires at least 10 eligible
participants and at least nine first-attempt successes.
