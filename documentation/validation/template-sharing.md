# Template sharing acceptance

The user approved skipping the live two-user sharing journey in the MVP.
T068 and T078 retain their opt-in browser and Go journal fixtures for a
future run. The supported release still exercises private repository
checkout with a single authorized owner and verifies owner-bound Git,
workspace access, and secret rules through the focused contracts.

The frozen RC3 source passed all 12 focused tests with the Go race detector.
They exercised guest owner and template scope, metadata-only secret reads,
template grant and archive authority, GitHub callback and repository coverage,
and concurrent refresh, reconnect, and revocation fencing.

## Fixture

Use an active SQLite controller with valid browser TLS on every fixture origin.
Create separate local Dex administrator and member accounts with usable
passwords. Connect each account to the fixture GitHub App with access to the
same private repository. Create a template owned by the administrator, grant
launch access to the member, and launch a workspace as the member. Wait for IDE
readiness, revoke the member's template launch grant, and seed an explicit
`workspace.connect_admin` grant for the administrator on that workspace.
Use a separate fixture from the workspace lifecycle journey.

Save the following fields in a local private JSON file with mode 0600.

| Field | Fixture value |
| --- | --- |
| `disposable` | `true` |
| `managementURL` | Management HTTPS origin |
| `administrator`, `member` | Separate objects with `email` and `password` |
| `templateName` | The administrator-owned template name |
| `workspaceID` | The running member-owned workspace UUID |
| `repositoryURL` | Private repository HTTPS URL on github.com |

## Browser and Go checks

Set `SHIFTBOX_SHARING_FIXTURE` to the absolute path of the fixture JSON. Set
`SHIFTBOX_SHARING_RESULT` to an unused absolute path. From the repository
root, run the following with the fixture CA in the browser's trust store.

```sh
NODE_USE_SYSTEM_CA=1 npm --prefix ui run test:e2e -- tests/e2e/template-sharing.spec.ts
```

On success the browser writes the fixture IDs and synthetic secret names to
`SHIFTBOX_SHARING_RESULT`. The result excludes secret values and account
credentials. Keep the controller running and set `SHIFTBOX_SHARING_DATABASE`
to the absolute path of the same disposable SQLite database.

```sh
go test ./tests/acceptance -run '^TestTemplateSharingJournal$' -count=1 -v
```

| Check | Evidence | Result |
| --- | --- | --- |
| Owner privacy and permission revocation | Live browser IDE and WebSocket assertions, followed by committed grants and audit records | Live two-user run waived for MVP |
| Secret precedence and non-disclosure | Guest command assertions, browser and API metadata checks, and durable override deletion | Live two-user run waived for MVP |
| Owner-bound Git | Private repository access before owner disconnection, denied access afterward, and distinct persisted Git authorization states | Live two-user run waived for MVP |
| Test discovery | TypeScript typecheck, Chrome collection, and Go test discovery | Passed |

T068 covers direct grant refusal without the project-secret acknowledgement,
management metadata denial to an authorized member, and refusal to issue a
new IDE entry grant after administrator access is revoked. The corresponding
Go contract tests passed under the race detector.

The browser archives the fixture template and revokes Git and workspace grants.
Provision a fresh fixture for another complete run.

## Release candidate checks with a local guest client

Chrome completed protected SQLite setup with the exact `mvp-rc2` controller.
Two distinct local Dex accounts signed in. The administrator created a
template in the browser with the named profile and pinned image. A disposable
client enrolled with the installation CA, then called the running controller's
guest secret endpoint over mutual TLS in the member workspace context.

Chrome confirmed that ungranted templates were hidden from the member and
launch was refused. The sharing dialog required acknowledgement of project
secret access. Management rendered only secret names and metadata, refused
the member's direct project metadata request, and excluded the synthetic
values from the audit response. The enrolled client verified organization,
project, personal, and explicitly empty values as grants and overrides
changed. Archiving preserved the existing workspace's project grant and
refused new launches. Revoking the grant removed the project-only value and
restored the organization fallback. The journal confirmed the archived
template and retained member workspace, acknowledged grant revocation and
its audit record, and deletion of the personal override while encrypted lower
scopes remained.

On the same installation, the local guest client served an HTTP and
WebSocket fixture on the IDE route. Two Chrome sessions received WebSocket
frames on the private origin. Revoking the administrator's grant closed its
socket, rejected its existing origin cookie and blocked a new entry while
the owner's stream remained open. The journal matched the grant and the
connection audit event to the member workspace. The journal run reported
only the missing GitHub authorization evidence.

The optional full browser and journal fixture uses the native code-server
workbench and two upstream GitHub authorizations. The journal verifies that
disconnection clears only the member's credential.
