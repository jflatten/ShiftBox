# Authorization Matrix

ShiftBox owns every application authorization decision. Authentication establishes a user or guest identity; it never grants a resource action by itself. Handlers evaluate the action below against current controller records in the same transaction that creates durable intent or changes authority.

## Decision inputs

Every decision uses:

- installation state and session mode;
- active user membership and predefined role;
- resource owner and current resource state;
- active `ResourceGrant` records for the exact user, resource, and action;
- active template launch grants where applicable;
- authenticated workspace ID and generation for guest reads; and
- recovery, retention, and operation gates.

Email address, guest-supplied owner/template/scope values, provider display names, hostname routing alone, and possession of an expired or revoked credential never establish authority.

## Role defaults and resource actions

| Action | Active member | Active administrator | Additional conditions |
| --- | --- | --- | --- |
| `workspace.list` / `workspace.view` | Own workspaces only | All workspaces | Recovery mode returns review views only |
| `workspace.launch` | Templates the user owns or has an active launch grant for | Any active template | Capacity, artifact, profile, network, and current template authorization must pass |
| `workspace.start` / `workspace.stop` / `workspace.rebuild` / `workspace.repair` / `workspace.delete` / `workspace.update_policy` | Own workspace | Any workspace | Current version, provider capability, installation gate, and lifecycle authority must pass |
| `workspace.connect_owner` | Own workspace | Own workspace | Workspace and origin grant must be eligible |
| `workspace.connect_admin` | Never by role alone | Only with an active exact-resource `workspace.connect_admin` ResourceGrant | Connection is audited; secret and Git context remains the recorded workspace owner's |
| `workspace.manage_connection_grants` | Never | List and revoke administrator connection grants for a workspace | Revocation is audited and closes connections authorized by the exact grant |
| `template.create` | Creates a template owned by self | Creates a template owned by self unless an explicit owner is selected and recorded | No silent ownership transfer |
| `template.view` | Owned or actively granted template | All templates, including archived through the administrator filter | Archived templates cannot launch |
| `template.edit` / `template.create_revision` / `template.share` | Owned template | Any template | Mutation creates an immutable revision where launch fields change; ownership is unchanged |
| `template.archive` | Owned template | Any template | Existing workspace provenance remains available |
| `secret.manage_user` | Own user scope | Own user scope only | Administrators do not manage another user's personal secrets by role |
| `secret.manage_template` | Owned template | Any template | Values are write-only; fallback metadata may be read |
| `secret.manage_organization` | Never | Organization scope | Values are write-only |
| `secret.resolve` | No browser action | No browser action | Only an active authenticated guest; controller derives owner/template/organization and current launch authorization |
| `git.manage` | Own Git authorization | Own Git authorization | Organization sign-in remains separate |
| `git.resolve` | No browser action | No browser action | Only an active authenticated guest for the recorded workspace owner and covered repository |
| `user.list` / `user.create` / `user.approve` / `user.disable` / `user.change_role` | Never | Allowed | Cannot remove the last usable administrator without the protected recovery invariant |
| `user.reset_password` | Never | Active local accounts | Revokes sessions and browser grants, requires a password change, and preserves a usable administrator |
| `notice.list` / `notice.read` / `notice.dismiss` | Allowed | Allowed | Dismissal never clears an active persistent condition or maintenance requirement |
| `notice.publish` | Never | Allowed | Security-relevant publication is audited |
| `audit.list` | Never | Allowed | Returned metadata is redacted and bounded |
| `infrastructure.view` | Never | Allowed | Includes provider, capacity, artifact, certificate, key, storage, and compatibility conditions |
| `infrastructure.manage` | Never | Allowed | Covers profiles, budgets, artifacts, supported network policies, certificate replacement, and external identity configuration |
| `maintenance.manage` | Never | Allowed | Maintenance authority cannot force-kill unsafe work or become deletion authority |
| `backup.create` | Never | Allowed | Password remains request-local and workspace disks remain excluded |
| `restore.validate` / `restore.activate` | Never | Allowed through protected operator/recovery flow | Target, host, key, compatibility, and recovery gates must pass |
| `recovery.review` / `recovery.activate` | Never | Recovery-review administrator session only | Ordinary mutations and workspace entry remain disabled until activation |
| `migration.preflight` | Never | Allowed | Preflight does not perform the migration |

## Management operation mapping

Every OpenAPI operation maps to exactly one action. Grouped operation IDs below share the same action and decision rule.

| OpenAPI operation ID | Action |
| --- | --- |
| `getSetupStatus` | `setup.view` (protected local origin only before activation) |
| `createInitialAdministrator`, `configureDataProfile`, `configureBrowserCertificate`, `validateSetupPlatform`, `activateInstallation` | `setup.mutate` (protected local origin plus current setup/session gate) |
| `getSession` | `session.view` |
| `logout` | `session.logout` |
| `changeOwnPassword` | `session.change_password` |
| `beginExternalReauthorization` | `session.external_reauthorize` |
| `listWorkspaces` | `workspace.list` |
| `launchWorkspace` | `workspace.launch` |
| `getWorkspace` | `workspace.view` |
| `updateWorkspacePolicy` | `workspace.update_policy` |
| `requestWorkspaceAction` with `start`, `stop`, `rebuild`, `repair`, `delete`, or `update` | Corresponding `workspace.<action>` |
| `requestWorkspaceAction` with `retry` | The action of the failed operation being retried; retry never grants new authority |
| `createWorkspaceEntryGrant` for owner | `workspace.connect_owner` |
| `createWorkspaceEntryGrant` for non-owner administrator | `workspace.connect_admin` |
| `listWorkspaceConnectionGrants`, `revokeWorkspaceConnectionGrant` | `workspace.manage_connection_grants` |
| `getOperation`, `streamOperationEvents` | Same permission required to view the operation's bound resource; installation-wide operations require their originating administrative action |
| `listTemplates` | `template.view` |
| `createTemplate` | `template.create` |
| `listTemplateCatalog` | `template.create` |
| `updateTemplate` | `template.edit` or `template.archive` according to requested fields |
| `createTemplateRevision` | `template.create_revision` |
| `grantTemplateLaunch`, `revokeTemplateLaunch` | `template.share` |
| `listSecretMetadata`, `setSecret`, `deleteSecretOverride` | `secret.manage_user`, `secret.manage_template`, or `secret.manage_organization` from the requested scope |
| `getGitAuthorization`, `beginGitAuthorization`, `completeGitAuthorization`, `revokeGitAuthorization` | `git.manage` for the state-bound current user |
| `listUsers`, `createLocalUser`, `updateMembership` | `user.list`, `user.create`, or the matching `user.approve` / `user.disable` / `user.change_role` action |
| `resetLocalUserPassword` | `user.reset_password` |
| `configureExternalIdentity` | `infrastructure.manage` |
| `publishNotice` | `notice.publish` |
| `listNotices` | `notice.list` |
| `updateNoticeReceipt` | `notice.read` or `notice.dismiss` from requested receipt state |
| `listAuditEvents` | `audit.list` |
| `getInfrastructureStatus`, `getBrowserCertificate`, `listResourceProfiles` | `infrastructure.view` |
| `replaceBrowserCertificate`, `importPlatformArtifact`, `createResourceProfile`, `updateResourceProfile`, `deleteResourceProfile`, `updateCapacityBudget`, `updateKeepAlivePolicy`, `createNetworkPolicy` | `infrastructure.manage` |
| `listMaintenanceCampaigns` | `infrastructure.view` |
| `createMaintenanceCampaign` | `maintenance.manage` |
| `runMigrationPreflight` | `migration.preflight` |
| `createBackup` | `backup.create` |
| `validateRestore` | `restore.validate` |
| `getRecoveryReview`, `updateRecoveryReview` | `recovery.review` |
| `activateRecovery` | `recovery.activate` |

The Git callback's empty HTTP authentication declaration does not make it public authority: the one-time state record binds it to the initiating authenticated user, exact redirect, expiry, and connector transaction.

## Session and installation gates

| Gate | Permitted actions |
| --- | --- |
| `bootstrap_restricted` | Current setup step, logout, and forced password change only |
| `password_change` | Session read, own password change, and logout only |
| `normal` with active installation | Role/resource actions above |
| `recovery_review` | Session read/logout plus `recovery.review` and, when ready, `recovery.activate` |
| Installation `blocked` | Read-only status, protected recovery, and explicitly permitted preflight actions |

Disabling membership or revoking an applicable grant commits first, then closes matching management, SSE, IDE, preview, WebSocket, poll, and tunnel connections. Previously copied secret or access-token material is outside the controller's ability to retract and is never represented as revoked in place.

## Test obligation

Contract tests must enumerate every route and proxy entry point, assert its action key, and cover owner, non-owner member, administrator without a resource grant, administrator with the exact grant, disabled user, stale session, recovery mode, and cross-resource grant cases. Adding a management route requires updating this matrix and its route-to-action test before handler implementation.
