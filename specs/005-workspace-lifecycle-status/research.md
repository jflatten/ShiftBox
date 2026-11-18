# Research: Workspace Lifecycle Status

| Decision | Rationale | Alternative considered |
| --- | --- | --- |
| Derive one batched lifecycle projection from current-generation workspace and journal evidence. Apply the lifecycle filter before pagination. | The current workspace read mixes observed power and the latest active operation. [Offboarding](../../internal/workspace/offboarding.go) cancels prior work in the stop admission transaction. [Maintenance](../../internal/workspace/maintenance.go) distinguishes waiting targets from admitted workspace operations. A common read keeps the chips, filter and Stepper reference consistent. | Per-row operation reads or browser-derived state would add requests and miss authoritative handoffs. A second status ledger would duplicate journal evidence. |
| Reuse the authenticated Configure, Verify IDE, and Verify repair steps. | [Guest configuration](../../internal/workspace/enroll.go) keeps the declared connection gate separate from later hooks. [IDE verification](../../internal/workspace/ide.go) checks the proxied workbench and WebSocket. [Proxy entry](../../internal/web/proxy_routes.go) adds caller-specific access control. | Provider power and Code-server service probes each lack the full first-use evidence. |
| Store the deadline and one durable timeout annotation with the relevant journal step. | [Step storage](../../internal/operation/store.go) already retains first activation. The [reconciler](../../internal/operation/reconciler.go) observes before applying and fences updates with its claim. The annotation must survive waiting-detail updates and later success. | A terminal failure at expiry would remove the work from ordinary reconciliation. Deriving a new deadline on each retry would reset the clock. |
| Reuse visible queries and the detail operation stream. Surface unavailability through the app provider. | [Query defaults](../../ui/lib/query-client.ts) already refresh on focus and suppress online-mode requests while offline. [Session](../../ui/lib/use-session.ts) and [operation](../../ui/lib/use-operation.ts) currently hide previous data on any failed read. An outage requires a display-only fallback that preserves genuine permission handling. The Mantine MCP confirms the existing library's titled, focus-managed Modal fits the notice. | An independent heartbeat or workspace streaming layer would duplicate the established read path. [Mantine Modal](https://mantine.dev/core/modal/#accessibility) |
| Calibrate defaults in one demo pass using existing recorded timing and representative slow paths. | Realistic samples inform each transition's cutoff. Worker lease durations and the existing retention grace measure different things. | P95 data takes precedence wherever available. The owner approved provisional values for the specific demo coverage gaps. |

## Demo calibration on November 18, 2026

Two disposable EC2 workspaces used revision 3 of the existing `shiftbox-mvp-fixture` project on `shiftbox-demo-controller:33`. The workspaces used the existing EC2 Large profile (`m6i.xlarge`, 32 GiB) and fresh instances and disks. The slow connection-gate case used a 35-second `postCreateCommand` in the disposable checkouts. A second repair retry capped its development container at 0.5 CPU to exercise constrained IDE startup. All fixture edits were local to these two workspaces.

Durations use the controller journal's event timestamps from first step activation through the first verified success. For grouped transitions, the first and last named steps define the measured interval. P95 is the nearest-rank sample percentile. These small scenario groups describe the observed pass and have limited tail precision. A deliberate hook failure was used on each fixture to exercise repair. Failed observations are excluded from completion percentiles. The user's edits in repair occurred between Repair-ready and entry into retry, before the next transition's clock started.

### Measured transitions

| Transition evidence | Scenario and individual samples in seconds | Samples | Scenario P95 in seconds |
| --- | --- | ---: | ---: |
| `provision` | Fresh EC2 resources, 5.574 and 5.538 | 2 | 5.574 |
| `enroll` | Fresh EC2 guest contact, 19.680 and 18.624 | 2 | 19.680 |
| `start` | Warm power-on after manual and inactivity stops, 9.186 and 9.196 | 2 | 9.196 |
| `configure` | Fresh instances, 24.008 and 23.006 | 2 | 24.008 |
| `configure` | Warm restarts, 34.209 and 37.171 | 2 | 37.171 |
| `configure` | Delayed rebuild and repair retry, 88.337 and 99.021 | 2 | 99.021 |
| `configure` | CPU-constrained repair retry, 52.115 | 1 | 52.115 |
| `verify_ide` | Fresh instances, 0.394 and 0.451 | 2 | 0.451 |
| `verify_ide` | Warm restarts, 0.573 and 0.581 | 2 | 0.581 |
| `verify_ide` | After delayed development setup, 0.530 and 0.465 | 2 | 0.530 |
| `verify_ide` | CPU-constrained repair retry, 0.802 | 1 | 0.802 |
| `repair_enter` through `verify_repair` | Repair after a failed required hook, 47.693 and 44.646 | 2 | 47.693 |
| `shutdown` through `stop` | Manual graceful stops, 62.022 and 69.810 | 2 | 69.810 |
| `stop` | Inactivity-triggered provider stop, 17.138 | 1 | 17.138 |
| `stop` | Provider-only segments of manual stops, 23.021 and 17.016 | 2 | 23.021 |
| `shutdown` through `stop` | Manual deletion from running, 73.789 | 1 | 73.789 |
| `delete` | Verified resource removal after stop, 7.198 and 9.468 | 2 | 9.468 |
| `shutdown` through `delete` | Direct deletion from already stopped, 10.243 | 1 | 10.243 |

Applying twice the highest relevant scenario P95 yields 11.148 seconds for provision, 39.360 for enrollment, 18.392 for start, 198.042 for configure, 1.604 for IDE verification, and 95.386 for repair. Grouped manual stop yields 139.620 seconds, or 147.578 seconds when the same budget covers the manual deletion stop. The provider-only stop evidence yields 46.042 seconds when including the manual stop segments. Verified deletion yields 18.936 seconds when timed at the deletion step and 20.486 seconds when starting the direct transition while already stopped.

Both disposable workspace deletes completed through the controller. AWS confirmed both EC2 instances terminated and zero matching EBS volumes and network interfaces remained. The controller and PostgreSQL remained stable with one running task each.

### Unmeasured scenarios

- Retention deletion is disabled on revision 33 because the controller configuration omits the retention maximum. The verified final `delete` step uses the same provider deletion and inventory checks as manual deletion. The earlier retention stop can force provider stop after the 60-second graceful shutdown window, so manual-stop observations do not measure that path.
- The demo's maintenance screen showed two completed campaigns. A new campaign would also target the shared retained workspace. Completed campaign operations expose aggregate progress, and the workspace API does not expose completed individual target references. The prior saved rollout records contained no per-target timings. Running and stopped maintenance remain unmeasured.


### Provisional defaults approved November 18, 2026

| Transition | Built-in estimate | Basis |
| --- | ---: | --- |
| Retention shutdown through provider stop | 3 minutes | Covers the existing 60-second force-stop grace, the measured provider stop segment of up to 23.021 seconds, and an allowance for verification and retry. |
| Stopped guest maintenance boot | 3 minutes | Allows for provider boot, enrollment recovery, and authenticated management contact. |
| Guest maintenance stage through verification | 5 minutes | Allows for signed artifact transfer, safe activation, trial restart, and authenticated guest verification. |
| Maintenance return to stopped | 2 minutes | Allows for a normal provider stop and repeat verification while preserving maintenance safeguards. |

These are provisional estimates. Replace them with twice the highest realistic scenario P95 once the missing workflows can be measured safely. Both deletion paths share the measured 18.936-second default for the verified removal step. Direct deletion from an already stopped VM hides the prerequisite stop checks from the chips. The controller still verifies those checks before entering removal.
