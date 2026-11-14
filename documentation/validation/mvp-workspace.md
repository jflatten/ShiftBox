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
in a second tab before issuing either action. Repair and the Go journal check
remain pending.

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
The Go test checks the authenticated enrollment and records for the workspace
launch, stop, start, rebuild, expected broken configuration, repair, and retry.
Chrome exercises file retention through the browser IDE.
