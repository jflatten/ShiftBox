# Data Model: Workspace Lifecycle Status

## Workspace snapshot

Derive the display from the current workspace generation, intended and observed states, and relevant committed operation steps. `current` is required. `next` is nullable. `mode` is one of `settled`, `transitioning`, `failed`. Settled requires a null next value. Transitioning requires a next milestone. Failed requires next exactly `failed`. A nullable workflow reference links the detail Stepper to the authoritative workflow, including its settled or failed record after reload. Existing active operation IDs remain available for command gating.

Use a single mapping of lifecycle milestones to journal evidence to drive projection and timeout scope. The arrows below show the proposed short labels and milestone order. The Stepper retains intermediate journal steps and their full labels.

| Workflow | Displayed milestones in order | Completion evidence |
| --- | --- | --- |
| Provision | Unknown → Provisioned → VM-running → Container-ready → Ready | `provision` verifies owned resources, `enroll` verifies authenticated guest contact on the running VM, `configure` verifies the connection gate, and `verify_ide` verifies the browser IDE. |
| Start | Stopped → VM-running → Container-ready → Ready | `start` verifies provider power, then the same development setup and IDE steps. |
| Rebuild or exit repair and retry | Last confirmed milestone → Container-ready → Ready | Current operation's `configure` and `verify_ide`. Retry uses the rebuild type with the repair action and can start from Repair-ready. |
| Repair | Last confirmed milestone → Repair-ready | `repair_enter` and `verify_repair`. The proxy must verify the repair IDE. |
| Manual, inactivity or offboarding stop | Last confirmed milestone → Stopped | Existing graceful shutdown where required, followed by provider-confirmed `stop`. |
| Manual or retention deletion | Last confirmed milestone → Stopped if needed → Deleted | Existing shutdown and verified stop where applicable, then `delete` verifies owned-resource removal. A workspace already confirmed stopped targets Deleted directly. |
| Running guest maintenance | Last confirmed milestone → Updated, then settle using verified workload/VM evidence | Safe stage, activate and authenticate on the updated guest through `maintenance_verify`. Preserve verified existing readiness where applicable. |
| Stopped guest maintenance | Stopped → VM-running → Updated → Stopped | Management-only `maintenance_boot`, signed update through authenticated `maintenance_verify`, then provider-confirmed `maintenance_restore`. |
| Observed condition or no applicable workflow | Verified condition, such as Unknown or Unreachable, by itself | Existing controller evidence. If running has no applicable IDE verification, use VM-running. Stopped and Deleted require their existing provider evidence. |

Use the current authoritative workflow's first incomplete milestone as the target. Preserve its confirmed base through handoffs, including successfully verified steps in a cancelled predecessor where still applicable. A new start/rebuild/retry must earn its own Ready evidence. A new provision starts at Unknown. Legacy transitional observed values alone supply no confirmed milestone. Scope selection by generation and operation type/action, ignore waiting scheduled targets, and use explicit journal authority and verified evidence to discard superseded failures. After successful `verify_ide`, the separate `finish_setup` remains fully visible in the Stepper, including any hook failure. Existing observed and desired state, repair eligibility, access grants and probes retain their meanings.

## Entered transition and timeout record

Attach the deadline to the first journal step for that displayed transition, with the existing durable first-start timestamp as entry. For multi-step milestones, carry the same deadline until the milestone is verified. A deadline is an absolute UTC instant, nullable for steps outside scope or transitions never entered, and set once per entered transition. Reuse recorded start and step evidence for in-flight legacy operations with the applicable measured or approved provisional default, and preserve completed history.

On expiry, observe the applicable completion evidence again through the existing worker. If completion is still unverified, persist one timeout for the transition, with its occurrence time, the blocked step and a bounded, non-secret reason. Keep it separate from attempt detail and normal step status. The same authority, step and generation fences apply as for other journal updates. The live timeout applies until verified completion or another authoritative condition takes over. The Stepper annotation remains with the original recorded workflow.
