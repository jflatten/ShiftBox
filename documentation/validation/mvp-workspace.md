# MVP workspace validation

## Candidate

The `development-repair` development candidate ran on Apple silicon with
macOS 26.6.2 (25G83), UTM 5.0.8, Packer 1.14.2, and Chrome
157.0.8103.51. The guest used the dated Ubuntu 24.04 Minimal amd64 image,
Dev Container CLI 0.90.0, Docker Engine 29.9.3, and code-server 4.145.2.
The [release manifest](../../platform/manifests/release.json) records hashes
for the exact candidate components. These are the versions used in this run.
The local controller binary was built with the setup release change recorded
in commit `4c3cb95` and pinned to `development-repair`. Its digest identifies
that local binary. The companion UTM export's artifact manifest SHA-256 was
`bc0f0bf860c21b984216089579cc5c6f1485854f2a585829c85344ce54c9c301`.

## Evidence

| Check | Result |
| --- | --- |
| Native protected setup | A fresh SQLite installation used the pinned Dex build, rotated the temporary password, accepted fixture HTTPS, and saved capacity. |
| Native UTM platform validation | Setup booted the imported image in a disposable VM, confirmed an authenticated guest poll, removed the VM, and recorded the image as available. |
| Go native UTM enrollment and stop | `TestUTMGuestEnrollment` passed in 63.76 seconds. The guest enrolled in 45 seconds, and offboarding stopped the VM while preserving its owned disk. The test removed the disposable VM afterward. |
| Chrome management handoff | The controller required a fresh sign-in after activation. Chrome signed in through Dex. The resumed run verified a connected fixture GitHub App authorization. |
| Chrome workspace launch | Chrome created the private-repository template, launched a UTM workspace, and reconnected the operation stream. The four-minute browser timer elapsed during Dev Container setup. The same controller journal later confirmed repository configuration, IDE verification, and completion of the remaining hooks. |
| Chrome IDE entry | The pinned controller returned a 403 after Chrome stored the Strict service cookie and withheld it on the cross-site redirect. A follow-up build of the same controller source with the entry fix opened the workbench and saved a file. This build was used only for the resumed verification. |
| Retained workspace restart | Chrome saved the marker file and the controller recorded a graceful VM stop. UTM 5.0.8 refused the subsequent boot because its removable drive still referenced the removed enrollment ISO. The cleanup regression now verifies that consumed media becomes a credential-free ISO at the same path. Native restart, rebuild, and repair still require verification. |
| Go workspace journal check | Pending a completed workspace journey. |

## November 14 retained-workspace rerun

After restarting UTM, the rebuilt controller resumed the pending start
against a private, current-schema copy of the test database. The guest
authenticated and the controller reached IDE readiness. The resumed browser
journey then completed another graceful stop and authenticated start and
reopened the same IDE. The marker check failed. A follow-up test read the
editor text, invoked File: Save, and reopened the file before requesting a
stop. It read an older marker, so the failure can occur without a VM restart.
In a short follow-up, typing the marker and using the workbench's Ctrl+S
shortcut returned the new contents after a reload when the saving page
remained open long enough for the remote write. The lifecycle test now uses a
second tab to wait for the saved contents before stopping the VM. A resumed
Chrome run confirmed the marker survived both stop/start and rebuild. The
next rebuild unexpectedly succeeded after the editor was used to break the
configuration. The test now verifies the broken and restored configuration
in a second tab before issuing either action.

The focused repair browser run passed in 6m 48s on the same retained VM and
SQLite fixture. The verified invalid configuration failed at the required
setup step. Chrome opened the separate repair origin, read the retained
marker, restored and verified the configuration, then exited repair and
retried setup. The regular IDE reopened with the same marker. On this same
database, `TestMVPWorkspaceJournal` passed with `-race` in resumed mode,
requiring the ordered lifecycle evidence and a successful final repair retry.

The retained stop/start, rebuild, and focused repair above used the rebuilt
development controller and its existing image. The frozen candidate below
repeated the journey on one installation.

## November 14 frozen candidate setup

Built the `mvp-rc1` controller and guest agent from
`fae90de2076cc5db32535020119931c796af6ac0`. Packer 1.14.2 built and
sealed the image using UTM 5.0.8. The artifact manifest SHA-256 is
`2252ec7cd73f4bd6d1af89365e9bee6734af443924281c6fe46d685cd285844b`.
The controller SHA-256 is
`2dd15a0cf9e5b87d4a8ab68d6ed990092741e0bb880b20fa4a9aad230ed21b60`.
The local candidate manifest passed the Go parser check with the original
Dex `v2.46.1`, UTM, Packer fork, Ubuntu and code-server pins.

Chrome completed protected setup with the approved loopback certificate,
rotated the fixture password, and activated SQLite after the native image
boot check. A separate Chrome context verified fresh administrator sign-in
against the same binary and installation. The workspace run resumed at this
handoff and reached GitHub sign-in for the fixture App. The November 14
retry completed authorization, created a workspace, and recorded an
authenticated poll from the candidate's native UTM guest. The private
checkout, initial browser IDE verification, and remaining setup hooks passed.

| Exact-stack check on `mvp-rc1` | Result |
| --- | --- |
| T039 Playwright workspace journey, Chrome 157.0.8103.51 | Passed in 24.1 minutes after the protected setup handoff. Verified operation-stream reconnect, IDE gating and entry, saved marker after stop/start and rebuild, expected setup failure, separate repair origin, and marker retention after repair and retry. |
| `TestMVPWorkspaceJournal` and `TestMVPReleaseManifest` | Passed with `-race` against the same active SQLite installation and the frozen manifest. The strict journal check verified one fresh workspace and the exact lifecycle sequence, including the expected failed rebuild. |

The [release manifest](../../platform/manifests/release.json) records the
controller and guest digests and the existing Dex, UTM, Packer fork, Ubuntu,
and code-server pins. The active SQLite installation was at schema 27.

The running candidate created and verified an encrypted management backup
before the workspace launch. The check-only `migrate preflight` command
returned ready with zero blockers against the candidate's manifest. The
same command returned ready with zero blockers after the retained UTM
workspace recorded its authenticated `mvp-rc1` guest release and protocol `1`.

Run the browser spec with `SHIFTBOX_MVP_FIXTURE` pointing at a fresh local
fixture and with the same valid certificate trust available to Chrome and the
test runner. With Node 26, `NODE_USE_SYSTEM_CA=1` lets the test runner use
system trust for its HTTPS requests. Use the development candidate and
fixture-specific GitHub App installation.
If consent must be completed in a different browser, keep the disposable
controller running until GitHub returns to ShiftBox. Set
`SHIFTBOX_MVP_RESUME_ACTIVE=1` to resume a previously verified setup with
the same installation. Record setup and workspace results separately when
using this mode.
If a previously recorded launch completed after a browser interruption, also
set `SHIFTBOX_MVP_RESUME_WORKSPACE` to that existing disposable workspace's UUID
to rerun the browser IDE and lifecycle checks without creating a second VM.

After Chrome completes, point `SHIFTBOX_TEST_MVP_DATABASE` at the same
disposable SQLite database and run
`go test ./tests/release -run '^TestMVPWorkspaceJournal$' -count=1 -v`.
Use `SHIFTBOX_MVP_RESUME_ACTIVE=1` for the journal check when a resumed run
has prior lifecycle attempts in its journal. A fresh run checks the exact
sequence.
Set `SHIFTBOX_TEST_MVP_RELEASE` to the absolute path of a new candidate's
manifest when verifying it before updating the checked-in release manifest.
The Go test checks the authenticated enrollment and records for the workspace
launch, stop, start, rebuild, expected broken configuration, repair, and retry.
Chrome exercises file retention through the browser IDE.

To check the retained developer home and named volume on the disposable
workspace, run the separate Chrome test after the exact journal check. It adds
a stop/start and rebuild to that workspace's history. Use the same fixture,
set `SHIFTBOX_MVP_RESUME_WORKSPACE` to its workspace UUID, and use the fixture
Dev Container that mounts `shiftbox-mvp-fixture` at `/var/lib/shiftbox-fixture`.

```sh
NODE_USE_SYSTEM_CA=1 SHIFTBOX_TEST_MVP_PERSISTENCE=1 \
  npm --prefix ui run test:e2e -- mvp-workspace.spec.ts --project=chromium \
  -g 'retains the developer home'
```

The test waits for the shell in the disposable checkout, writes one file in
the developer's actual home and one in the named mount, then verifies both
after each lifecycle change. It removes its two files after verification.

On November 14, this test passed in 8.4 minutes on the same `mvp-rc1` UTM
workspace. Chrome verified both files after stop/start and after rebuild.
The start and rebuild journal receipts identified two distinct containers.
