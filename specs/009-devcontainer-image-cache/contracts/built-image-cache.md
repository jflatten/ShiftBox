# Contracts: Project devcontainer image cache

## Administrator settings

Use **Administration → Infrastructure → Cache**. Extend the current registry settings read response with a built-image setting and effective availability. Propose a dedicated immediate update under `/api/v1/admin/image-cache/built-images` so a setting save can fence access independently of a public storage activation. The update accepts an enabled boolean and its own version precondition. Use existing infrastructure view and manage permissions, CSRF and audit handling.

- The form describes the organization-wide impact and removal of stored built images before saving Off.
- A successful save returns the persisted setting and version. Stale versions use the existing precondition failure response. Validation, permission, and storage availability are visible with the existing controls and status patterns.
- The public cache has independent enablement. Both share the effective storage budget. Purge runs in the background after access is fenced.

## Guest negotiation and image transport

| Interaction | Scope and outcome |
| --- | --- |
| Lookup | A current fresh-launch dispatch, resolved by guest mTLS identity, can request a lookup for the config bytes its CLI will use. Derive project and platform from controller records. Check current source access before issuing a short-lived read grant. Return a complete immutable image reference or a miss. |
| Start publication | A current launch or explicit-rebuild dispatch can request a short-lived upload scope for its own candidate. The scope is bound to project, workspace generation, operation, config digest, platform, and enabled generation. An unavailable cache is a decline. An admitted background upload can finish after the workspace operation reports success. |
| Transfer | Serve scoped Docker/OCI manifest and blob reads and the pinned client’s upload sequence on the existing guest listener. Private routing targets an exact reserved namespace for direct controller access. Public mirror routing uses its upstream namespace and public-only policy. Require project authority before checking object existence. Use expected size and digest for admission. Cross-scope mount attempts gain no access to existing bytes. |
| Finish publication | Validate the staged single-platform image, verify the current project origin configuration through the authorized GitHub connection, and conditionally promote the complete digest under live authority. A changed origin/config, a stale generation or dispatch, insufficient capacity, or a newer winning publication can discard the attempt. Report disposition to the uploader while the local workspace outcome stands. |

Cancel or refuse built-image access upon policy disable or loss of current local authority. Use bounded request sizes and idle and total deadlines. Support retry when a scoped attempt remains valid, or start a fresh attempt after expiry. Send source credentials only to the configured GitHub endpoint. Keep staged references inaccessible to other workspaces until promotion.

## Workspace behavior

- A cache hit uses the exact digest at the CLI image boundary and continues normal workspace setup. If admission, transfer, or image validation fails before container startup, build via the ordinary project path under its existing network policy.
- **Rebuild container** uses its usual worktree and build behavior. Offer its result for shared publication when the configuration used matches current origin and ref. Local rebuild success is independent of publication.
- Launch status and IDE readiness continue to come from the existing workspace lifecycle. A publication result does not complete or fail a local launch or rebuild operation.
