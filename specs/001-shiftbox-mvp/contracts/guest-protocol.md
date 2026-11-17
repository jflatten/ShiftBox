# Guest Protocol Contract

The guest VM is untrusted. Authentication proves possession of the current registered key for one workspace generation; it does not make guest telemetry truthful. Every request is authorized from the mTLS identity and controller records. Guest-supplied workspace, owner, template, or secret-scope identifiers never select authority.

## Transport

- Dedicated internal HTTPS listener and certificate chain, separate from browser HTTPS.
- TLS 1.3 with controller server authentication and per-workspace client authentication.
- Client and server leaves/keys are replaced every four hours and valid for eight hours, bounded by issuer validity.
- Every long-lived poll/tunnel connection reauthenticates no later than the certificate used for connection authentication expires. Revocation closes it immediately.
- JSON bodies use UTF-8, reject unknown fields, and carry an explicit `protocolVersion`.
- Controller release N accepts guest product releases N and N−1 only.

## Identity derivation

The server derives:

```text
installation ID
workspace ID
workspace generation
guest registration state
certificate serial and expiry
```

from the verified connection. A body value that conflicts with derived identity is a protocol violation and audit event.

## Poll exchange

`POST /internal/v1/guest/poll`

Request:

```json
{
  "protocolVersion": "1",
  "guestRelease": "2026.11.0",
  "lastOperationId": "uuid-or-null",
  "lastOperationVersion": 7,
  "operationReport": {
    "operationId": "uuid",
    "workspaceGeneration": 4,
    "status": "running",
    "step": "devcontainer.lifecycle",
    "attempt": 1,
    "evidence": {
      "containerId": "opaque",
      "waitForReached": false
    },
    "problem": null
  },
  "activity": {
    "sampledAt": "2026-11-13T20:10:00Z",
    "cpuPercent": 6.2,
    "ideConnections": 1
  },
  "probes": [
    { "id": "code-server", "status": "healthy" }
  ],
  "tunnel": {
    "connected": true,
    "sessionId": "opaque"
  }
}
```

`probes` is optional. A successful poll with the response header
`Shiftbox-Probe-Reports: 1` enables reporting on the next poll while the desired
state is running. The code-server probe checks the local IDE socket and reports
`healthy` when its health endpoint responds successfully, including while the
editor is idle. An unavailable socket or unsuccessful health response produces
`error`. The controller validates IDs and statuses and records the results
against the authenticated workspace generation. An omitted or empty report
leaves the previous result to expire 90 seconds after receipt. The controller
uses these reports for display only.

Response:

```json
{
  "protocolVersion": "1",
  "serverTime": "2026-11-13T20:10:01Z",
  "workspaceGeneration": 4,
  "desiredState": "running",
  "operation": {
    "id": "uuid",
    "version": 8,
    "type": "provision",
    "command": "ensure-development-container",
    "arguments": {
      "repositoryUrl": "https://github.com/example/repo.git",
      "repositoryRef": "main",
      "devcontainerPath": ".devcontainer/devcontainer.json",
      "retainedMounts": []
    }
  },
  "certificate": {
    "renewAfter": "2026-11-13T22:00:00Z"
  },
  "nextPollAfterSeconds": 30
}
```

### Rules

1. The controller returns at most one active lifecycle operation for the generation.
2. The guest persists operation ID, version, attempt, and completed local steps before acknowledging them.
3. Re-delivery of the same operation/version resumes or reports the same result; it never repeats a completed destructive local step blindly.
4. A higher workspace generation fences all unfinished lower-generation work.
5. Evidence fields are bounded, typed, and redacted. Arbitrary logs are retrieved through a separate bounded diagnostic operation.
6. `waitFor` readiness and an actual proxied IDE check are distinct completion evidence.

## Approved guest commands

| Command | Purpose | Required completion evidence |
| --- | --- | --- |
| `prepare-repository` | Clone/fetch/checkout using current owner Git authority | Exact repository/ref and working tree path |
| `ensure-development-container` | Run supported Dev Container configuration | Container identity, retained mounts, lifecycle step status |
| `verify-ide-ready` | Verify declared `waitFor` point and proxied IDE function | Successful authenticated proxy probe |
| `rebuild-development-container` | Replace disposable container layers | New container identity and retained-data checks |
| `enter-repair` | Start known-good repair environment | Repair endpoint and project automation suppression |
| `exit-repair` | Stop repair environment without resetting work | Preserved project state |
| `stage-guest-update` | Download and verify compatible signed release | Digest/signature and staged path |
| `activate-guest-update` | Activate at safe boundary under supervisor | New authenticated release health or rollback evidence |
| `shutdown-for-stop` | Ask workloads to stop gracefully | Shutdown completion or bounded timeout |

No command accepts an arbitrary executable, shell string, or controller credential.

## Secret retrieval

`POST /internal/v1/guest/secrets/{name}:resolve`

Request body contains only delivery format hints. The server derives owner and template context from the mTLS identity. Response is never cached persistently by the guest service:

```json
{
  "name": "DATABASE_URL",
  "value": "secret-value",
  "source": "user",
  "retrievedAt": "2026-11-13T20:10:01Z"
}
```

Rules:

- Name is normalized and authorized against User → Project Template → Organization.
- Explicit empty returns an empty string and does not fall through.
- Controller/provider/signing/CA/OAuth-refresh credentials are never resolvable.
- Response and value are excluded from logs, telemetry, audit metadata, and operation evidence.
- Controller outage fails the read. Previously copied values may continue in developer processes.

## Git access token retrieval

`POST /internal/v1/guest/git/github:access-token`

The controller returns only the current access token for the recorded owner and covered GitHub App authorization. It may coordinate one refresh for concurrent requests. Refresh token, app private key, and client secret never enter the guest. A copied access token is not updated after rotation.

## Certificate replacement

`POST /internal/v1/guest/certificates:renew`

- Requires a currently valid, eligible mTLS identity.
- Guest sends a CSR for a freshly generated key.
- Controller chooses subject, workspace, generation, usages, and validity.
- Issuance records the new serial before response; guest installs key/certificate atomically.
- Tunnel reconnects or performs fresh authenticated connection no later than old authentication expiry.

## Provider-mediated enrollment

Enrollment is available only through a provider-delivered, short-lived, single-use grant bound to installation, workspace, generation, operation, and verified provider instance.

`POST /internal/v1/enroll`

```json
{
  "grant": "one-use-secret",
  "protocolVersion": "1",
  "guestRelease": "2026.11.0",
  "csrPem": "-----BEGIN CERTIFICATE REQUEST-----..."
}
```

The controller consumes the grant and records the CSR key proof atomically. A lost response may return the same issued result only for the same transaction and key proof. Reuse, a different key, expiry, revoked registration, provider mismatch, or stale generation is denied.

## Tunnel registration

The authenticated poll advertises manually configured preview ports in the
`Shiftbox-Preview-Ports` header as a JSON array. The agent registers each port
as `preview-<port>` at the workspace VM's IPv4 loopback address. Missing or
empty configuration advertises no previews.

After mTLS authentication, the guest opens a Yamux client session and sends a control stream:

```json
{
  "type": "register",
  "protocolVersion": "1",
  "workspaceGeneration": 4,
  "services": [
    {"key": "ide", "target": "127.0.0.1:3000", "protocols": ["http", "websocket"]},
    {"key": "preview-8080", "target": "127.0.0.1:8080", "protocols": ["http", "websocket"]}
  ]
}
```

The controller ignores any workspace identity in this message and binds services to the authenticated registration. Each accepted proxy request opens one stream containing a small framed header with service key, request ID, and protocol, followed by a raw HTTP/1.1 connection. The proxy strips all ShiftBox cookies, CSRF tokens, origin grants, and hop-by-hop headers before forwarding.

## Stable problems

| Code | Meaning | Guest response |
| --- | --- | --- |
| `guest.registration_revoked` | Registration explicitly revoked | Stop reconnecting; await administrator remediation |
| `guest.identity_expired` | Certificate no longer valid | Stop authenticated retries; require provider recovery |
| `guest.generation_stale` | Controller has a newer workspace generation | Abandon fenced operation and local tunnel |
| `guest.release_unsupported` | Outside N/N−1 | Enter visible recovery/update requirement |
| `operation.conflict` | Another serialized operation owns the workspace | Report waiting; do not execute |
| `operation.command_unsupported` | Command unknown for protocol/release | Fail operation without shell fallback |
| `secret.not_found` | No effective value | Return not-found to caller without lower unauthorized lookup |
| `git.reconnect_required` | Provider grant cannot renew | Fail current request and show owner reconnection action |

## Required contract tests

- Expired, revoked, deleted, cloned, and stale-generation identities fail closed.
- A copied workspace ID in a different authenticated connection cannot select another workspace.
- Poll replay and lost response do not repeat a completed step.
- Enrollment grant replay, alternate CSR key, and expiry are rejected.
- Certificate renewal chooses controller-owned identity fields.
- Secret precedence, explicit empty, revocation, and admin-connection owner context match the data model.
- Tunnel registration cannot add management targets or leak management headers.
- Revocation closes active poll and tunnel connections immediately.
