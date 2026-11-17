# IDE WebSocket readiness validation

T022 was validated on November 17, 2026, on `feature/mvp`, against `shiftbox-demo`
in AWS account `524941137496`, region `us-east-2`, using the `default` profile.

## Change and regression coverage

The launch readiness probe previously accepted a successful VS Code workbench HTML
response without checking WebSocket support. It now also opens a separate,
generation-scoped `websocket` stream on the authenticated guest tunnel and requires
HTTP 101, the upgrade headers, and the correct response to a fresh RFC 6455
challenge. It uses the browser's host and HTTPS origin. The probe closes its
connection immediately afterward and retains the existing ten-second deadline
and operation retry behavior. The same check applies to the repair workbench.

The regression failed before the implementation when HTML was available but the
WebSocket backend returned 503 or an invalid acceptance challenge. These cases now
remain pending. Tests also cover a stalled upgrade respecting cancellation,
successful upgrades, project-folder redirects, and stale workspace generations.

`go test ./internal/proxy ./internal/guest ./internal/workspace ./internal/web
./internal/app`, the focused stalled-upgrade regression, `go vet ./...`, the UI
production build, and the embedded browser asset test passed.

## Live acceptance

| Check | Result |
| --- | --- |
| Controller | `shiftbox-demo-controller:18` |
| Image digest | `sha256:da63889bfe050a90921d1f80933ba45b38cdd6341567ea1adf6ec78d04925164` |
| ECS services | Controller and PostgreSQL each reported one running task and `COMPLETED` |
| Fresh workspace | `ide-cold-start-review`, `dc2b5c36-49e5-4512-a81d-93568aaaf800` |
| Fresh instance | `i-070b1e66387a8529c`, private `t3.medium` in `us-east-2a` |
| AMI | `ami-0eea624c1e5701eb3` |
| First launch | Provision, enrollment, configuration, IDE verification, and setup completed without a manual retry |
| First browser access | First Open IDE loaded the workbench and opened `retained.txt` without a WebSocket error or reload |
| Cold instance boots | One kernel boot; one `RunInstances` event before test cleanup |
| Retained review workspace | `single-boot-review`, `15518f23-9390-4e9e-abf2-a29e6483b34d` |
| Browser connections | Both browser WebSocket handshakes returned HTTP 101 after the controller deployment |
| Terminal | `pwd` returned `/workspaces/project`; `cat` read the verification file |
| Editing and saving | Saved `shiftbox-ide-review.txt` through the editor, then read the new contents through the terminal |
| Persistence | The verification file survived a container rebuild and browser reload |
| Original instance boots | Still one kernel boot and one `RunInstances`, with no EC2 stop, start, or reboot |

The original intermittent first-launch failure was not deterministically
reproduced. The new readiness test establishes that launch cannot finish with
HTML alone, and the fresh t3.medium acceptance launch passed. A stale browser tab
across a container rebuild reported `Unknown reconnection token (never seen)`;
opening a new tab or reloading established a fresh connection. This is distinct
from the first-open acceptance test. Code-server also logged a nonfatal GitHub
authentication-provider timeout while the workbench remained usable.

The retained review workspace contains `/workspaces/project/shiftbox-ide-review.txt`
with `ShiftBox IDE verified after controller 18 deployment.` It is left running
for [human review](https://shiftbox.demo.shiftbox.internal:8443/workspaces/?id=15518f23-9390-4e9e-abf2-a29e6483b34d).

Local rollout plans, logs, image verification, and redacted EC2 evidence are in
`.cache/aws-demo/ide-readiness/`. The additional cold-start acceptance workspace
is stopped after verification, preserving its files.
