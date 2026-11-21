# Feature Specification: Workspace Lifecycle Status

**Feature Branch** — `feature/mvp`

**Created**: 2026-11-18

**Status** — Implemented and verified on shiftbox-demo

**Input**: Agreed Workspace lifecycle status design

## User Scenarios & Testing

### User Story 1 - Follow workspace progress (Priority: P1)

A workspace user can see what the controller has confirmed and what should happen next from either Workspaces or workspace detail. An administrator can follow the same status for workspaces they are authorized to view.

**Why this priority** — Users need to know when they can start work and what is holding up a workspace.

**Independent Test** — Start a stopped workspace and compare the row, its matching state filter, and workspace detail through first use. Repeat with repair and retry, then observe an automatic stop.

**Acceptance Scenarios**

1. **Given** a stopped workspace, **when** start is accepted, **then** both views show Stopped and the next milestone, advance through verified VM power and container setup, and settle on Ready once the connection gate and browser IDE have been verified.
2. **Given** a Ready workspace, **when** a later setup hook is still running or Code-server reports a service probe error, **then** Ready remains visible. Hook progress stays in the Stepper, service health remains separate, and opening the IDE still requires the caller's authorization.
3. **Given** an active workflow, **when** a scheduled guest update must wait or an administrator's offboarding stop replaces startup, **then** the scheduled update leaves the live chips alone. The stop takes over when it becomes authoritative, and startup cancellation is recorded in operation history.
4. **Given** any refresh that spans several milestones, **when** either view receives the latest state, **then** it jumps directly to the latest confirmed milestone and its next target. The Workspaces filter matches the left chip, and the detail Stepper includes the recorded workflow.
5. **Given** repair was requested after failed development setup, **when** the repair IDE is verified, **then** the single settled chip reads Repair-ready. Exiting repair and retrying follows the normal development setup milestones to Ready.

### User Story 2 - See failed and overdue transitions (Priority: P2)

A workspace user can see the last confirmed milestone and the reason a transition failed or timed out, including after navigating away and returning.

**Why this priority** — Visible failures explain where work stopped and show when the controller has since verified recovery.

**Independent Test** — Cause a development setup failure and separately hold a stop past its deadline. Reload, then confirm a late stop without issuing a second stop.

**Acceptance Scenarios**

1. **Given** an entered transition, **when** final verification still finds it incomplete at the deadline, **then** retain the last confirmed milestone and display Timed out · Still checking. Show Failed when the controller records a terminal failure. In both cases, stop the connector animation and show the step and reason in the Stepper.
2. **Given** a timeout or failure still applies, **when** the user reloads or returns, **then** the same status is shown. A newer authoritative workflow or a newly verified condition updates the live display. A late completion advances the state and the Stepper records Completed after timeout.
3. **Given** a worker retries or the controller restarts, **when** the same transition resumes, **then** its original deadline still applies. Expiry itself leaves the underlying work and resource ownership alone.
4. **Given** an active deployment, **when** an authorized user chooses Stop deployment, **then** cancel the deployment and switch to the durable shutdown workflow whether or not the deadline passed. Preserve retained files and keep uncertain provider resources reserved until their state is verified.

### User Story 3 - Keep progress visible during a controller outage (Priority: P3)

A workspace user can still see the last displayed chips and progress bar while the controller is unavailable and can see the current state when it returns.

**Why this priority** — A failed refresh should preserve the user's place and distinguish lost connectivity from a failed workspace transition.

**Independent Test** — Interrupt connectivity during a transition, restore it after several milestones, and then trigger an actual permission error.

**Acceptance Scenarios**

1. **Given** displayed lifecycle progress, **when** the network or controller becomes unavailable, **then** the last chips and progress bar stay as displayed and one application-wide controller-unavailable modal appears. Polling continues.
2. **Given** the modal is visible, **when** the application receives a successful controller response, **then** it dismisses the modal and uses the next valid workspace snapshot. A genuine permission or domain error also establishes connectivity and keeps its existing handling.

### Edge Cases

- Unknown and Unreachable are distinct controller observations. They do not establish a stopped or deleted workspace, or authorize releasing reservations.
- A maintenance boot for a previously stopped workspace shows maintenance progress and returns to Stopped after verified restoration, unless a newer authorized intent takes over.
- A retained deleted row settles on Deleted only after verified cleanup. The existing option to hide deleted workspaces still applies.
- If the first load fails during an outage, show the unavailable modal. Chips appear after the first authorized snapshot.

## Requirements

### Functional Requirements

- **FR-001**: Both workspace views MUST show the same lifecycle status from the controller. A settled state has one chip. A transition has the last confirmed milestone on the left, the immediate next milestone on the right, and an indeterminate connector. Advance only when the controller verifies a milestone. Labels MUST use a short name, preferably one word or two hyphenated words.
- **FR-002**: Ready MUST mean the controller verified the browser IDE through its proxy after the declared Dev Container connection gate. VM power alone is insufficient. Repair-ready MUST require a verified repair IDE. Later setup hooks remain visible in the Stepper. Service probe health and caller authorization MUST remain independent of the lifecycle display.
- **FR-003**: Status MUST cover provision, start, manual and automatic stop, rebuild, repair and retry, guest maintenance, manual and retention deletion, and observed conditions such as Unknown or Unreachable. A replacing authoritative workflow MUST take over the chips and preserve cancellation in operation history. Scheduled maintenance waiting behind active work MUST leave that workflow in view.
- **FR-004**: Both views MUST refresh the visible workspace snapshot regularly at the current cadence of about three seconds, promptly after commands are accepted, and on focus or reconnect. They MUST jump to the latest confirmed status. Workspaces MUST filter on the current/left chip across the inventory. The detail Stepper MUST show the full recorded workflow.
- **FR-005**: An applicable failed or timed-out transition MUST retain its last confirmed milestone and pause the animation. Show Failed for a terminal error and Timed out · Still checking for deadline expiry while verification continues. The status MUST survive refresh and navigation until superseded by a newer authoritative workflow or verified condition. The Stepper MUST retain the affected step and reason. Service probe errors remain separate.
- **FR-006**: Every entered lifecycle transition MUST have a controller-owned deadline measured from entry and stable across worker retries and controller restarts. For measured transitions, each built-in duration MUST be twice the highest measured P95 across tested realistic scenarios, including representative long waits for human input where applicable. The owner approved provisional estimates for retention stop and guest maintenance until safe measurements exist. This deadline is independent of worker attempt limits.
- **FR-007**: Before recording Timed out, the controller MUST recheck completion. Expiry alone MUST leave ongoing work and resource ownership alone. Verified late completion MUST advance the chips and preserve the timeout in Stepper history. Existing provider cleanup, retention authority, and maintenance safeguards still apply.
- **FR-008**: During network or controller unavailability, the application MUST preserve the displayed chips and progress bar, present one application-wide unavailable modal, and continue checking for recovery. A successful controller response MUST clear the modal and valid snapshots MUST refresh the views. Genuine controller permission or domain errors establish connectivity and retain their handling.

- **FR-009**: Authorized owners and administrators MUST be able to select Stop deployment from the launch, inventory, and detail views during an active launch, start or rebuild. The controller MUST atomically cancel the deployment and accept shutdown. Graceful provider shutdown preserves retained resources. If no provider request was made, the stop can complete without allocating a VM.

### Key Entities

- **Workspace lifecycle status** — The controller-confirmed current milestone, optional next milestone or Failed outcome, and whether the connector animates.
- **Workflow record** — The authorized operation and its ordered milestones, evidence, cancellation, and failure or timeout history.
- **Transition deadline** — The cutoff attached to an entered lifecycle transition. Later reconciliation may verify completion while retaining the timeout record.

## Success Criteria

### Measurable Outcomes

- **SC-001**: For every workflow in FR-003, both views agree given the same snapshot. At each tested transition the list filter includes the workspace for its current chip. Following a successful visible refresh, the display reflects the latest confirmed milestone even when intermediate milestones were missed.
- **SC-002**: The test workspace displays Ready only after both first-use conditions are verified and Repair-ready only after the repair IDE is verified. Later hooks and probe errors remain visible separately, and an unauthorized caller cannot open the IDE.
- **SC-003**: In representative failure, retry/restart, and late-completion cases, refresh preserves the applicable timeout or Failed pair, the deadline remains fixed, and late completion leaves a visible Timed out record. Every built-in duration is traceable to measured inputs or an explicitly approved provisional estimate using FR-006.
- **SC-004**: During each representative network and controller outage, the last rendered lifecycle state remains visible behind one modal. The first confirmed response clears the outage indication and the next valid workspace snapshot updates the display.

## Assumptions

- The three-second cadence is approximate and applies to the visible view. Actual refresh timing depends on connectivity and controller response time.
- Post-connection hooks are outside the first-use lifecycle milestones. Their outcomes stay in the Stepper. Future agent services and SSH are outside this feature.
- Calibration on `shiftbox-demo` established measured defaults for the reachable workflows. Retention stop and guest maintenance use the documented provisional defaults.
