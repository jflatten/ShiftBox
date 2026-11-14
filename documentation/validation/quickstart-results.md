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

T125 remains open for the live scenarios in sections 3–10 of the
quickstart. UTM still reports the disposable workspace as `starting`.
Prior native evidence and the remaining requirements are recorded in the
[release tracker](mvp-v1-v14.md).
