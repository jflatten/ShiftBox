# UI Workflow Contract

The management UI is a statically exported Next.js application served by Go. It uses approved ShiftBox branding and Mantine controls exclusively. API state remains authoritative; the UI does not persist authorization or query caches across reloads.

## Application frame

- Mantine `AppShell` with header, responsive navbar, and one semantic main region.
- Header uses the supplied horizontal light/dark ShiftBox logos with correct clear space and a labeled home link.
- Primary navigation: Workspaces, Templates, Notices, and, for administrators, People, Infrastructure, Maintenance, Backup & Recovery, and Audit.
- Persistent installation conditions appear in an `Alert` region before page content and remain until their server condition clears.
- Route-level errors identify the failed action and provide Retry or Return controls; notifications are supplementary only.

## Workflow component rules

Every setup, provision, rebuild, repair, stop, delete, maintenance, backup, restore, and recovery operation renders a Mantine `Stepper` from the server's ordered `Operation.steps`.

- `active` is derived from the active/first-pending step; it is never client-invented.
- Upcoming steps are not selectable (`allowNextStepsSelect=false`). Completed steps may be inspected without changing server state.
- Step labels and descriptions are always present; icons never carry the only meaning.
- `loading` marks the active step. Failed steps use an error icon/color plus visible error text and recovery action.
- SSE applies only newer operation versions. Sequence gaps or reconnect failure trigger a full operation refetch.
- A finished stepper remains visible in operation history; progress does not disappear on failure.

## Setup workflow

```text
First administrator -> Password change -> Data profile -> Service names/TLS
-> Provider/artifact -> Validation -> Activation -> Fresh sign-in
```

- Setup is available only on the protected loopback origin until activation.
- Each form uses Mantine Form and labeled Mantine inputs; invalid submission focuses the first invalid field.
- The temporary password is shown exactly once with a Copy action and an explicit acknowledgement before leaving the step.
- Production database setup shows privilege/TLS/schema validation separately from activation.
- Activation explains that bootstrap sessions will end and requires fresh sign-in.

## Workspace launch workflow

```text
Choose template -> Review repository/secrets -> Choose permitted retention
-> Capacity check -> Confirm -> Provision/Enroll/Configure/Ready
```

- The template revision, owner context, resource values, platform artifact, network policy, and project-secret exposure are visible before confirmation.
- Capacity failure identifies CPU, memory, or disk and does not show queue/eviction controls.
- Ready is shown only after the declared Dev Container connection stage and proxied IDE check.
- Open IDE creates a short-lived entry grant and navigates to the distinct IDE origin; no management credential is placed in the URL.

## Workspace detail and lifecycle

- Desired state and observed state are displayed separately with timestamps.
- Current operation, retained-data contract, exact platform release, template revision, pinned resources, activity/retention deadline, and actionable failures are visible.
- Start, Stop, Rebuild, Repair, Update, and Delete are Mantine Buttons gated by server permissions/capabilities.
- Repair explains that project automation will not run and that files/Git state remain unchanged.
- Unsupported network options are absent or disabled with the provider capability explanation.

### Destructive confirmation

Delete uses an accessible Mantine Modal:

- Initial focus is on Cancel; focus is trapped and returns to the invoking control.
- The workspace name, retained paths, and explicit statement that management backups exclude VM disks are shown.
- Confirmation requires typing the workspace name and checking “I have pushed work I need to keep.” The checkbox records acknowledgement, not a backup claim.
- The destructive button remains disabled until inputs match. Closing the modal has no effect.

Retention-policy warnings use the same data-loss language, deadline, last activity, and outage-grace state. Once deletion commits, the UI shows graceful shutdown and provider-enforced cleanup as separate steps.

## Templates and secrets

- Templates use Mantine Table/List controls with owner, state, current revision, repository, resource size, artifact, and launch grants.
- Granting launch access shows a warning that authorized workspace code can retrieve project secrets and requires acknowledgement.
- Secret lists show name, scope, override/fallback metadata, and updated time only. Values never return to or render in the management UI.
- Secret input clears after successful storage. Deleting an override names the lower scope that will become effective and requires confirmation.
- Archived templates are excluded by default and appear only in the administrator filter; existing workspace provenance remains visible without linking members to archived details.

## Membership and offboarding

- A password-change session exposes only the password form and logout; successful change requires a fresh permitted session state.
- External users can see when actual upstream authorization is due and explicitly begin reauthorization; token refresh alone never clears the gate.
- People view distinguishes pending, active, and disabled membership and local/external credential modes.
- Approving external users chooses member or administrator explicitly.
- Disabling a user presents affected sessions and owned workspaces, then shows session closure and each provider stop operation in real time.
- A failed workspace stop remains a critical actionable condition; the UI never implies the files were deleted.

## Maintenance

- Maintenance campaign form requires target release, explicit deadline, and review of affected retained workspaces/capacity.
- Users see persistent warnings and per-workspace status before the deadline.
- After deadline, the UI distinguishes waiting for a safe boundary, updating, verifying, restoring prior stopped state, and blocked failure.
- No force-kill action is offered under maintenance authority. An administrator may separately choose an already authorized lifecycle/retirement action with its own consequences.

## Backup and recovery

- Backup form states exactly what is included and excludes VM disks. Password and confirmation are never retained in form state after submission.
- Stepper: Preflight → Drain writes → Application checkpoint → Identity checkpoint → Collect protected files → Encrypt → Verify → Complete.
- Restore first runs validation and presents archive/release/schema/key/target findings. Activation is a separate explicit action.
- Recovery mode disables ordinary mutations and workspace entry. Review workflow: Identities → Provider inventory → Workspace matches/conflicts → Authorization/retention → Activate.
- Restored sessions/grants are shown as invalidated, and stale OAuth state is shown as requiring revalidation rather than restored authority.

## Accessibility and focus acceptance

- All workflows complete by keyboard alone with visible focus.
- Stepper steps have accessible names and correct current/completed state.
- Dialog focus traps, initial safe focus, Escape behavior, and focus restoration are verified.
- Form errors are connected to fields and focus the first invalid field.
- Status is expressed by text and semantics in addition to color/icon.
- Live operation updates use an appropriate polite status region; critical revocation/failure announcements do not repeatedly interrupt users.
- Table actions remain reachable and labeled at narrow widths; AppShell navigation collapses without hiding the current page title.

## Required browser tests

1. Complete loopback setup and fresh sign-in using keyboard only.
2. Launch a workspace and observe every operation step through an SSE reconnect.
3. Verify ready is withheld until the proxied IDE check succeeds.
4. Confirm management cookies are absent from hostile preview requests and cross-origin mutations fail.
5. Revoke a session while IDE/preview/WebSocket connections are active and verify closure.
6. Exercise project-secret sharing warning, empty override, and fallback deletion warning without rendering stored values.
7. Exercise delete modal focus, typed confirmation, data-loss copy, and retained reservation on partial cleanup.
8. Exercise backup validation failures and recovery-mode gating.
9. Run automated accessibility checks plus manual keyboard/focus review for all Stepper workflows.
