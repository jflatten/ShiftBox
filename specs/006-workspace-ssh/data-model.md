# Data Model — Workspace SSH

This is the proposed design for the confirmed [specification](spec.md). Existing resource and lifecycle ownership stays with the controller.

| Entity | Fields and relationship | Lifetime and validation |
| --- | --- | --- |
| Browser pairing request | State, proof challenge, exact numeric loopback redirect, expiry, and approving normal session ID after confirmation | Short-lived controller state. Approval uses the existing browser session and CSRF protection. The code is one-use and proof-bound. An expired or consumed request cannot create a helper credential. |
| Helper credential | Server-side credential verifier digest, approving session ID, creation and expiry timestamps | Linked to one existing ShiftBox session, which supplies user, installation, membership, and reauthorization state. Store only the verifier on the controller and the opaque credential in macOS Keychain. Effective validity ends at the earliest applicable deadline or revocation. Accept the verifier on helper routes only. |
| Managed alias | Controller origin, signed-in account identity, immutable workspace ID, human-readable alias | Workstation mapping. Allocate once from the workspace name and reuse on rename. Retain the mapping through temporary ineligibility while removing its active SSH entry. Use collision checks against existing user hosts. |
| SSH host identity | Controller origin, immutable workspace ID, and public host-key pin | Agent owns the private key in VM-persistent state outside the primary container. The controller obtains the key via the authenticated guest session for the authorized workspace. The helper stores its pin separately from SSH entries, retains it across ordinary stops, starts, and renames, and rejects an unexpected key or mismatched workspace identity. |
| Administrator connection grant (existing) | Exact `resource_grants` row with `resource_type=workspace`, `action=workspace.connect_admin`, workspace ID, subject administrator, issuer, and creation/revocation timestamps | Shared by browser IDE and SSH. Provisioning is separate. The existing workflow lists active grants and revokes them. SSH admission checks the exact grant for the current administrator and workspace. |
| Workspace start operation (existing) | Workspace ID and generation, authorized requester, current operation ID/state, existing policy and reservation data | SSH start uses the ordinary `workspace.start` operation. Callers wait on the same admitted start for the target generation. Failure, retry, and provider reconciliation use existing state. |
| Active SSH connection | User and approving session IDs, workspace ID and generation, exact grant ID when needed, session deadline, and cancellation handle | Controller registry state only. Register with a current-authority recheck. Close the relay and its guest execution resources on authority loss, workspace invalidation, or disconnect. Administrator admission uses the existing durable audit log. |

## State Rules

- **Helper** — Unpaired → paired after browser approval and successful exchange → sign-in required after expiry or revocation. Sync updates the managed list only while authority is valid.
- **Connect** — Authorize → wait on an admitted start if needed → authorize the current ready target → strictly verify its pinned host key → establish SSH → close. An ineligible target, failed start, changed authority, or host verification error ends the attempt.
- **Grant** — Issued → active → revoked. Listing reads durable state. The existing audit captures grant context and the SSH admission records the exact grant used.
