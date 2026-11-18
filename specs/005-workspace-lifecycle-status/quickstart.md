# Calibration and Verification: Workspace Lifecycle Status

## Prerequisites

Use private network access to `shiftbox-demo`, the repository's `default` AWS profile and the configured management origin. Use a disposable workspace and approved test template, plus authorized owner and administrator accounts. Make policy changes and exercise offboarding with disposable test data and accounts. For the final review build, follow the repository's demo rollout instructions, including applying any required schema upgrade before switching the controller. [Repository instructions](../../AGENTS.md)

## Calibrate once before setting defaults

1. Use existing operation and step timestamps. Measure from the first activation of each target through verified completion, spanning all steps for grouped milestones. Use the lifecycle mapping to track which transitions each run covers. [Milestone mapping](data-model.md)
2. Take repeat samples in a single bounded pass. Reuse the same demo fixtures across workflows. Include cold provision/enrollment and image/container startup, slow connection-gate and IDE verification, rebuild and repair/retry, graceful stop and retention cleanup, and safe maintenance of a running and a previously stopped guest. Include longer representative waits where an entered transition actually awaits human input. Record scenario, sample count, and observed P95 per transition.
3. Use **2 × the highest scenario P95** as the built-in duration when measured. Record the inputs and results. Use verified completion timestamps for latency samples and record failed or incomplete runs separately. Label the owner-approved retention stop and maintenance estimates as provisional and replace them when safe measurements become available. Leave worker attempt limits, retention grace and maintenance safety rules in force.

## Run focused checks after implementation

```sh
go test ./internal/operation ./internal/workspace ./internal/web ./internal/database ./tests/contract
npm --prefix ui run typecheck
npm --prefix ui test -- workspaces.test.tsx workflow-stepper.test.tsx use-operation.test.tsx
scripts/check-openapi.sh
```

Use a controlled clock and observed provider/guest test doubles to exercise restart, retry, final observation and late completion. For the outage tests, fail the management read transport or return a controller failure. Restore it and check the latest snapshot. Keep the real permission/domain response cases in the same test run.

## Browser review on the demo

| Exercise | Expected result |
| --- | --- |
| Provision, stop, and start a workspace. Switch between Workspaces and detail, filter by the current milestone, and let one refresh span several steps. | Both views jump to the latest confirmed chip pair. VM-running precedes Ready. The full Stepper keeps the intermediate evidence and later hooks. Ready can coexist with a service probe error. Test that a caller lacking the connection grant still cannot open the IDE. |
| Rebuild, trigger a setup failure, then repair and retry. Reload after failure and after recovery. | Failed remains on refresh while applicable, with the reason in the Stepper. Repair-ready appears after repair IDE verification. Retry returns through development setup to Ready. |
| Observe an automatic stop, maintenance waiting versus active, and authorized offboarding takeover. Exercise manual and retention deletion on disposable data. | Live status follows the controlling workflow, with cancellation in its prior record. Maintenance returns a previously stopped guest to Stopped. Retained inventory, reservations and deleted visibility change only with the existing verified outcomes. |
| Interrupt browser access during visible progress and restore it after several recorded steps. Exercise representative timeout via the controlled verification case. | One modal overlays unchanged chips and progress. Reads recover, the modal closes and the latest state appears. Timeout stops chip animation, late verification updates status and Stepper history retains Timed out. |

Record the demo revision and observed results. Remove disposable fixtures and verify cleanup through the existing provider checks. Use focused controller tests for failure cases that require delaying or withholding provider responses.

## November 18, 2026 demo review

[Review Workspaces on shiftbox-demo](https://shiftbox.demo.shiftbox.internal:8443/workspaces/)

- The protected management backup and retained-guest upgrade gate passed. PostgreSQL schema 39 advanced to 40. Terraform deployed `shiftbox-demo-controller:34` once, and controller and PostgreSQL both reported `COMPLETED`. The controller ran image `sha256:dc2fd932f5184c06ae795e9b2186f7e58d0323f22d98fbb6df001156b455ea5e`.
- Go tests and vet, OpenAPI generation, UI type checking, 185 Jest tests, the production and embedded UI builds, and 14 local Chrome checks passed. Controlled tests covered workflow supersession, repair and retry projection, and timeout history after late completion. Chrome verified the half-width chip spacing at 1280 and 390 pixels.
- The signed-in administrator launched and owned the disposable verification workspace. Both live views agreed on the transition chips. The current-chip filter retained a workspace transitioning out of Ready and applied the settled Ready filter correctly. Stop and start focused the Stepper, and the operation remained visible after reload. Rebuild reached Ready while later hooks were still running. The owner opened the browser IDE, and an unauthenticated IDE request returned HTTP 403.
- A tab-scoped network interruption left one modal over unchanged chips and paused the connector. Restoring the tab connection dismissed the notice and refreshed the inventory.
- The disposable workspace settled on Deleted, its completed cleanup Stepper survived reload, and the default list hid the deleted row. AWS confirmed the instance terminated with zero matching EBS volumes and network interfaces. The temporary backup and migration task definitions and helper image were removed. The protected management backup remains on the installation volume.
