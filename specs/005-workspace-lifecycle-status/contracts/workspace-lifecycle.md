# Workspace Lifecycle Contract

## Workspace reads and filtering

- Existing `GET /api/v1/workspaces` and `GET /api/v1/workspaces/{workspaceId}` include the same controller-derived `lifecycle` object with `current`, `next`, `mode`, and nullable `workflowOperationId`. Values are stable keys for the short labels in the [data model](../data-model.md), for example `stopped`, `vm-running`, `repair-ready`, and `ready`. Existing desired/observed fields and the active operation reference retain their semantics.
- Add `lifecycleState` to the list read. Match `lifecycle.current` before cursor and limit, subject to existing owner and deleted filters. The existing `state` filter continues matching observed state. An unsupported `lifecycleState` value uses the existing invalid-filter problem. A right-hand Failed result does not match the filter.
- Example start progression, with the workflow ID remaining stable throughout

  | Current | Next | Mode |
  | --- | --- | --- |
  | `stopped` | `vm-running` | `transitioning` |
  | `vm-running` | `container-ready` | `transitioning` |
  | `container-ready` | `ready` | `transitioning` |
  | `ready` | `null` | `settled` |

- Example failure while verifying the IDE shows `container-ready`, `failed`, `failed`. The Stepper shows the Verify browser IDE step and its reason. Late success can replace that live pair with Ready.

## Operation and connectivity behavior

- The existing authorized operation read and event snapshots carry an optional timeout annotation on the blocked step containing `occurredAt` and `reason`. The Stepper labels the annotation Timed out even if the step later succeeds. Existing operation statuses, event authorization and cancellation remain in force.
- The browser updates chips from successful workspace reads. The detail event stream serves its one selected operation and can request an early scoped refresh. Locally accepted commands never count as verified milestones.
- On lost connectivity, retain last rendered authorized progress for display and pause the connector in place. A single titled application modal covers the existing view. Participating reads keep checking. A successful or genuine permission/domain response clears the notice while actual permission errors continue to remove access, including cached protected content.
- The lifecycle payload conveys workspace status. IDE and repair entry still require their existing caller authorization and audited grant paths. Probe details remain separate.
