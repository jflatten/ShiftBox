# Workstation and Guest SSH Contract

The [Workspace SSH specification](../spec.md) requires strict per-workspace host-key verification. This contract proposes the routes and framing that carry out that requirement.

## Helper and controller

| Interface | Behavior |
| --- | --- |
| Browser approval and `POST /api/v1/ssh/helper/approve` | The helper opens the configured ShiftBox HTTPS origin with a fresh state, S256 challenge, and a numeric loopback callback. A valid normal browser session approves through the existing origin and CSRF boundary. Send JSON `{state, challenge, redirectUri}` and navigate to the returned `callbackUrl`, which carries the one-time `code` and matching `state` on the exact loopback endpoint. |
| `POST /api/v1/ssh/helper/exchange` | The helper checks state and sends JSON `{code, state, verifier, redirectUri}` to the same controller over TLS. The controller consumes the code once and returns `{credential, expiresAt, accountId}`. Approval codes expire after five minutes and do not survive a controller restart. |
| `GET /api/v1/ssh/workspaces` | With the helper credential, return the current eligible items with workspace ID and display name. Return ready running workspaces and stopped or starting workspaces eligible for SSH-triggered start, subject to both permissions as applicable. Capacity remains authoritative at start admission. |
| `GET /api/v1/ssh/workspaces/{workspaceId}/readiness` | With the helper credential and workspace connection permission, verify an SSH handshake using the current authenticated guest tunnel and its registered host key. Return 204 while ready. The probe opens no channel and does not start the workspace. |
| `GET /api/v1/ssh/workspaces/{workspaceId}/connect` | With the helper credential, authorize a dedicated WebSocket upgrade using proposed subprotocol `shiftbox.ssh.v1`. For a stopped workspace, use the existing authorized lifecycle operation. Return progress until ready, then the requested workspace ID and host public key, then binary SSH bytes. Only the helper consumes control messages. |

The macOS helper keeps the credential in Keychain and uses a scoped bearer header for discovery and connection. Management cookies, browser-origin attempts, and browser service tokens cannot authorize this upgrade. Keep helper credentials out of SSH config, arguments, logs, and guest traffic, and disallow cross-origin redirects of authenticated requests. Return safe authentication or availability errors before upgrade when possible. After upgrade, relay a bounded progress or terminal failure message to helper stderr. SSH transport bytes alone go to `ProxyCommand` stdout.

Discovery and connection share the same policy inputs. To initiate or join a startup, check connection policy and start permission before creating work. Concurrent authorized attempts join the one current start for the workspace and generation. Respect competing lifecycle operations, capacity and recovery gates. Wait with bounded client and controller deadlines and forward only when the primary container is ready and the active VM tunnel advertises SSH. If the caller disconnects, stop its wait and leave the admitted durable operation to the existing lifecycle. An ordinary failed start surfaces its error and existing retry guidance.

Before relaying, recheck the caller, current container target, generation and exact administrator grant. Match the existing registry's commit/close ordering, with session and grant scope for waiting and live connections and a clock deadline for session expiry. Record the durable administrator access audit before guest forwarding. On revocation, close both relay ends and dispose of the guest connection's execution resources. Proxy resources are also released on disconnect or workspace invalidation.

## macOS OpenSSH config

The helper serves browser alias lookups on `127.0.0.1:48652` for the paired management origin and signed-in account. It returns an alias only when its managed OpenSSH stanza is present and the controller's SSH probe passes. The browser may request permission to access the local helper.

- Write one literal `Host` stanza per eligible workspace in a helper-owned portion of the default user SSH config. Preserve unrelated file content and permissions, avoid user-owned alias collisions, and apply updates atomically. Use the absolute installed helper path and a safe workspace reference in `ProxyCommand`.
- Keep alias allocation keyed to workspace identity and installation/account. Aliases remain readable and survive renames. Remove entries after an authoritative eligibility change or session invalidation. A transient fetch failure leaves the last good list while locally known session validity permits it. Every connection goes back to the controller.
- Bind the pin to the authenticated controller origin and requested immutable workspace ID. Check the controller response matches that identity and write the first pin before passing SSH bytes to the client. Require the SSH server to present the pinned key. An unexpected key or mismatched workspace identity ends the connection with a clear verification error and preserves the existing pin. Ordinary stops, starts, and renames preserve the same pin.

## Controller-to-guest

- Negotiate the fixed SSH service on the authenticated guest poll and tunnel registration. Bind the host key and stream to the same authorized workspace and its current authenticated VM and generation. The stream uses only the SSH service and accepts no caller-supplied guest host, port, user, container, or path. Unsupported guests keep existing IDE/repair access.
- The existing guest binary accepts SSH only over the private stream. Shells, exec, and any editor-required transfer use the configured developer identity and project context in the primary devcontainer. The SSH user field cannot select a different guest identity.
- Support the PTY and exec behavior required by OpenSSH. Prove the default VS Code Remote–SSH loopback forwarding and setup transfer from the supported clients. Resolve editor forwarding only inside the primary container. No general guest-host forwarding or additional guest listener is part of this contract. Standalone file-transfer workflows are deferred.
