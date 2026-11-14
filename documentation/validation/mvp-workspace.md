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
| Chrome management handoff | The controller required a fresh sign-in after activation. Chrome signed in through Dex and reached the GitHub authorization step. |
| Chrome workspace journey | The run reached GitHub and timed out waiting for authorization. Repository clone, Dev Container launch, IDE, stop and start, rebuild, and repair remain pending. |
| Go workspace journal check | Pending a completed workspace journey. |

Run the browser spec with `SHIFTBOX_MVP_FIXTURE` pointing at a fresh local
fixture and with the same valid certificate trust available to Chrome and the
test runner. With Node 26, `NODE_USE_SYSTEM_CA=1` lets the test runner use
system trust for its HTTPS requests. Use the development candidate and
fixture-specific GitHub App installation.
If consent must be completed in a different browser, keep the disposable
controller running until GitHub returns to ShiftBox. Use
`SHIFTBOX_MVP_RESUME_ACTIVE=1` only to resume a previously verified setup
with the same running fixture and before creating a workspace. Record
setup and workspace results separately when using this mode.

After Chrome completes, point `SHIFTBOX_TEST_MVP_DATABASE` at the same
disposable SQLite database and run
`go test ./tests/release -run '^TestMVPWorkspaceJournal$' -count=1 -v`.
The Go test checks the authenticated enrollment and records for the workspace
launch, stop, start, rebuild, expected broken configuration, repair, and retry.
Chrome exercises file retention through the browser IDE.
