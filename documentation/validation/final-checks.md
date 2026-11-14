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
| Browser fixtures | The offboarding focus follow-up passed 64 Playwright 1.65.1 tests against the rebuilt production export, with 22 in Chrome 157.0.8103.51 and 21 each in Firefox 159.0 and WebKit 27.2. The browser-storage test ran in Chrome. |

Next.js warned that it ignored a package lock above the Git checkout.
It compiled and exported the pages from the ShiftBox UI successfully.
The clean npm install reported three low and 16 moderate advisories.

The exact-stack workspace and sharing journeys remain pending. UTM reported
the existing disposable VM as `starting` during the readiness check. The
browser run used API fixtures and did not exercise UTM or live GitHub
authorization. The exported Go API and trust-code
documentation review found 73 missing comments across production packages
and command wrappers. They now describe the provider observation contract,
operation replay, enrollment, connection closure, and other interface
behavior. The scoped documentation scan passed for `cmd`, `internal`,
`migrations`, `platform`, and `ui/export.go`. Formatting and Go vet
passed again after the comment changes.

## November 14 follow-up

Go formatting, vet, the serial standard and race suites, and the tagged
integration and release suites passed. Native mutation cases used their
normal opt-in skips during this rerun. The embedded assets test and the
OpenAPI TypeScript drift check also passed.

The production Go scopes passed the exported API documentation scan again.
The trust and reconciliation review added context for retained VM identity,
guest registration and IDE readiness, maintenance completion after restart,
and preserving the checkout, home, and named volumes during rebuild.

The production export and 88 UI unit tests passed. The browser fixtures
passed 26 cases each in installed Chrome, Firefox, and WebKit, plus the
separate Chrome browser-storage case. The reruns corrected an outdated
recovery completion assertion and a synthetic preview redirect that
WebKit cannot fulfill. The preview fixture checks entry navigation and
referrer handling. Live exchange and revocation remain release checks.
