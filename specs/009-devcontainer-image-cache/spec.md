# Feature Specification: Project devcontainer image cache

**Feature Branch**: `feature/mvp`

**Created**: 2026-11-20

**Status**: Design confirmed

**Input**: Reuse finished devcontainer images across workspaces in the same ShiftBox project to shorten the time from the controller’s launch signal to a usable browser IDE.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Launch using a project’s built image (Priority: P1)

A developer launches a fresh workspace and gets a usable IDE without waiting for the devcontainer image to be built again when the project already has a matching image. The new workspace checks out its selected Git source and runs its required setup normally.

**Why this priority**: Repeated Dockerfile builds and Feature installation can account for most of the wait to a usable IDE.

**Independent Test**: Allow a fresh workspace to build and publish an image, then launch a second fresh workspace from the same project.

**Acceptance Scenarios**:

1. **Given** an enabled cache and no matching image, **when** a developer launches, **then** the build starts through the usual launch flow and the completed image can be published without the IDE waiting on the upload.
2. **Given** a complete matching image built by another authorized workspace in the same project, **when** a fresh workspace launches, **then** it uses the image without executing the Dockerfile or reinstalling Features and opens the IDE with its own checkout, mounts, and lifecycle hooks.
3. **Given** unchanged `devcontainer.json` contents at the selected path, **when** the Git commit or branch changes, **then** the cached image can still be reused. A changed file, missing entry, evicted entry, or inaccessible image follows the normal build path.
4. **Given** a different project that references the same repository and configuration, **when** it launches, **then** access to the first project’s image is denied and its own launch proceeds through its authorized path.

---

### User Story 2 - Refresh a project image through Rebuild container (Priority: P2)

A developer uses **Rebuild container** to refresh the running workspace and make the resulting image available to other launchers in the project when it matches the current project configuration.

**Why this priority**: A developer needs a way to refresh tool versions and build inputs when `devcontainer.json` stays the same.

**Independent Test**: Rebuild a workspace with the current project configuration, then repeat using a retained workspace whose configuration differs from the configured origin and ref.

**Acceptance Scenarios**:

1. **Given** a matching cached image, **when** an authorized developer uses **Rebuild container**, **then** the action builds using the existing workspace behavior and publishes the image it produces when eligible. A fresh launch can use the previous matching image until the replacement is complete.
2. **Given** a workspace whose devcontainer configuration differs from the project’s configured Git origin and ref, **when** its developer rebuilds, **then** the local rebuild can succeed and the project’s cached image remains the one eligible for the current configuration.
3. **Given** a failed rebuild or interrupted upload, **when** another matching workspace launches, **then** it can still use the previous complete image if that image is available.

---

### User Story 3 - Control built-image caching for the organization (Priority: P3)

An organization administrator can find and persist one setting for built-image caching and understands that turning it off removes stored project images.

**Why this priority**: One setting lets administrators control organization-wide reuse and its storage lifecycle.

**Independent Test**: Open the registry cache settings, disable built-image caching, reload the page, and launch a workspace whose image was previously cached.

**Acceptance Scenarios**:

1. **Given** a new MVP organization, **when** the administrator opens registry cache settings, **then** built-image caching is **On** by default and applies across projects.
2. **Given** existing built images and an in-flight upload, **when** an administrator saves **Off**, **then** the setting persists after reload, reads and publication stop immediately, and the controller purges built images in the background. The public pull-through cache follows its separate setting.
3. **Given** a member without permission to manage infrastructure settings, **when** they try to change the organization setting, **then** the change is denied and the persisted setting stays intact.

### Edge Cases

- Simultaneous misses for the same eligible configuration build independently. The first completed eligible publication supplies future launches.
- If Git origin changes during a build or upload, the controller evaluates the result against the current project configuration before exposing it. Unavailable origin verification blocks publication while the local operation can succeed.
- A missing, inaccessible, incomplete, or evicted image is a miss. The workspace follows its ordinary authorized network and build path.
- A disable followed by re-enable starts with an empty built-image cache. Background cleanup from the disable cannot remove images published after re-enable or let pre-disable uploads become visible.
- When the project's configured ref moves to different configuration contents, a successful publication occupies the single project image position for that platform. Returning to an older configuration can require a new build.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The organization MUST have an administrator-managed built-image caching setting in the controller registry settings, defaulting to **On** for a new MVP organization. Saved changes MUST persist after reload and apply across projects.
- **FR-002**: Disabling built-image caching MUST immediately stop serving and publishing built images, including publication from in-flight uploads, and MUST purge stored built images in the background. The public pull-through cache MUST have independent enablement. Re-enable MUST allow a fresh build and publication.
- **FR-003**: The controller MUST authorize use and publication in the exact ShiftBox project. Authorized project workspaces can trust the images built by other workspaces in that project. The controller MUST enforce current workspace, project, and source access before granting reuse or publication.
- **FR-004**: The controller MUST retain at most one current built image per project and target platform. A hit requires the project's configured repository and exact `devcontainer.json` bytes at the selected path. The Git branch name and commit alone MUST NOT invalidate a match. Changes to the Dockerfile, Features, or other repository files take effect in the cached image when a build occurs for changed configuration, explicit rebuild, or a cache miss.
- **FR-005**: A hit MUST use the finished build image and skip the Dockerfile build and Feature installation. Each launch MUST follow the usual checkout, workspace mounts, runtime credentials, lifecycle hooks, and IDE readiness behavior. Cached data is limited to the finished image and its metadata, with credentials excluded from the stored artifact.
- **FR-006**: A miss or cache failure MUST let launch proceed via the normal authorized build. Upload and publication MUST be outside the wait for a usable IDE. Parallel misses for the same eligible configuration MUST build independently, and the first complete eligible publication MUST win.
- **FR-007**: The existing **Rebuild container** action MUST build the workspace using its existing inputs and offer the resulting image for publication. The worktree and retained home MUST survive the action. A rebuild request bypasses reuse of the project’s finished image for that workspace operation.
- **FR-008**: The controller MUST publish a finished image only if the configuration used to build it matches the file at the project’s current configured origin and ref. A build that cannot publish MUST be able to complete locally. Eligible explicit rebuilds MUST replace the prior image only after the replacement is complete.
- **FR-009**: Built images MUST use the controller registry’s shared storage budget and eviction policy. Eviction MUST make a later matching request a miss, and disabling built-image caching MUST preserve any storage content still referenced by the public pull-through cache.
- **FR-010**: The admin control MUST communicate the consequence of disabling before save and show the persisted setting afterward, using the existing infrastructure permission model. Background purge has no user-facing progress requirement.

### Key Entities

- **Organization cache setting** — whether built-image reuse and publication are enabled.
- **Project image** — the current complete image for a project and target platform, associated with the selected config path and exact file contents.
- **Publication attempt** — the result of a fresh launch build or an explicit rebuild, checked against the current project configuration before it can replace the current image.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In a controlled pair of fresh launches with identical configuration, the first can publish and the second reaches a usable IDE with zero executions of the image build or Feature installation.
- **SC-002**: Changing the configuration, clearing or evicting the image, and making the cache inaccessible each produce a successful ordinary build when the project’s normal build succeeds.
- **SC-003**: A successful eligible explicit rebuild becomes the current image. An interrupted upload, a rebuild with outdated configuration, and a request from a different project each produce zero unauthorized replacements or reads.
- **SC-004**: An administrator can save **Off**, reload to see it persisted, and start a fresh workspace with zero built-image reads or publications admitted after disabling. The background purge subsequently removes the built images.
- **SC-005**: Launch speed is measured from the controller’s start signal to a usable browser IDE. The estimated `strands-playground` savings are exploratory. This feature has no elapsed-time acceptance threshold or performance-hypothesis benchmark.

## Assumptions

- The existing devcontainer runtime, project and Git authorization, registry storage, and infrastructure settings are prerequisites. Registry storage must be available and have room under its configured limits before it can retain a built image.
- The Git checkout is mounted from the workspace VM. A project build can explicitly include files in its image and must keep credentials out of those layers and metadata. ShiftBox runtime secret injection stays separate from image publication.
- Scope is new MVP setup and the existing launch and rebuild actions.
