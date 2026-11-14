# MVP engineering checks

This record covers the local verification of `ea6c1b8` on macOS 26.6.2
(arm64) on November 14, 2026. The [release tracker](mvp-v1-v14.md) records
the separate release gates.

| Check | Result |
| --- | --- |
| Go 1.27.4 formatting and static analysis | `make fmt-check` and `go vet ./...` passed. |
| Go tests | The standard suite and race-enabled suite passed with serial package execution. |
| Tagged Go suites | `go test -tags=integration ./tests/integration/...` and `go test -tags=utm_release ./tests/release/...` passed with the default environment. Cases requiring external fixture settings were skipped. |
| Generated API types | The `openapi` build-tag test matched the checked-in TypeScript API types to the contract. |
| UI on Node 26.13.0 | TypeScript checking passed, 86 Jest tests passed, and Next.js 16.4.6 exported the production UI. |
| Embedded UI | The `shiftbox_ui` build-tag test served the embedded browser assets. |
| Browser fixtures | All 22 Playwright 1.65.1 tests passed in Chrome 157.0.8103.51 against the current export on a disposable loopback server. Coverage included accessibility, backup and recovery UI, browser storage, and maintenance and deletion. |

Next.js warned that it ignored a package lock above the Git checkout.
It compiled and exported the pages from the ShiftBox UI successfully.
The clean npm install reported three low and 16 moderate advisories.

The exact-stack workspace and sharing journeys remain pending. UTM reported
the existing disposable VM as `starting` during the readiness check. The
browser run used API fixtures and did not exercise UTM, live GitHub
authorization, Firefox, or WebKit. The exported Go API and trust-code
documentation review remains open under T126.
