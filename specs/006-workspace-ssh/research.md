# Research — Workspace SSH

The [specification](spec.md) records the confirmed product contract. The sections below explain it and identify proposed implementation details.

## 1. Reuse the VM tunnel

**Decision.** Use a WebSocket upgrade on the controller's existing management HTTPS origin and a fixed, generation-bound SSH service on the existing VM tunnel. Negotiate SSH support before a guest advertises it and keep browser IDE, repair, and preview behavior for older guests.

**Rationale.** [Guest tunnel](../../internal/guest/tunnel.go) already multiplexes and copies streams over one VM-authenticated mTLS connection. [Controller tunnel validation](../../internal/proxy/tunnel.go) and [guest registration](../../internal/guest/agent_tunnel.go) currently accept the HTTP/WebSocket services only. A typed SSH stream can reuse that transport without allowing a caller to supply a dial target. Gitpod's Ona GCP runner is a precedent for workspace-specific gateway routing. [Ona GCP runner security model](https://github.com/gitpod-io/terraform-google-ona-runner/blob/main/SECURITY.md#system-and-scope)

**Alternatives considered.** A second connection, public guest SSH port, and Coder's direct WireGuard and relayed DERP model would add deployment or transport requirements outside the confirmed first release. [Coder networking](https://coder.com/docs/admin/networking)

## 2. Terminate SSH in the existing guest binary

**Decision.** Prototype SSH server handling over a private tunnel stream using the already pinned Go SSH package. Reuse the primary container ID and configured execution context used by the browser IDE for interactive shells, non-interactive commands and transfer. Use in-container execution for editor loopback forwarding. If an in-container bridge is needed, publish the same signed guest binary through the existing managed launcher mechanism. Keep host private key material in VM-owned agent state. Strict per-workspace host-key verification is required. The authenticated controller supplies the intended workspace key, and the helper pins it by controller origin and immutable workspace ID before relaying SSH bytes. The pin survives ordinary stops, starts, and renames. Reject an unexpected key or a different workspace identity with a clear verification error.

**Rationale.** [Devcontainer setup](../../internal/guest/devcontainer.go) resolves the container and configured user and [browser IDE startup](../../internal/guest/browser_ide.go) uses `devcontainer exec --container-id`. [Managed command publishing](../../internal/guest/workspace_command.go) already handles the guest release binary. SSH can use PTY and non-PTY sessions over an existing connection. Normal VS Code Remote–SSH uses a server inside the remote execution environment and SSH forwarding. A local download can be forced to prove client-to-workspace transfer. OpenSSH 9 and later use SFTP for the default SCP transfer, so the setup fixture must confirm the actual file-transfer requests the tested clients send. [Go SSH API](https://pkg.go.dev/golang.org/x/crypto@v0.58.0/ssh#NewServerConn) · [VS Code SSH troubleshooting](https://code.visualstudio.com/docs/remote/troubleshooting#improving-security-on-multi-user-servers) · [VS Code setup transfer](https://code.visualstudio.com/docs/remote/ssh#common-questions) · [OpenSSH 9 release notes](https://www.openssh.com/txt/release-9.0)

**Alternatives considered.** A separate guest SSH daemon or agent package was rejected. Coder's separate main and devcontainer agents are a useful targeting reference. Verify strict host-key enforcement through macOS OpenSSH and VS Code Remote–SSH, including when the first connection starts a stopped workspace. [Coder devcontainer SSH](https://coder.com/docs/user-guides/devcontainers/working-with-dev-containers/)

## 3. Pair through the browser and install per macOS user

**Decision.** Reuse current ShiftBox browser sign-in and approval, then exchange a short-lived, single-use code bound to the helper's proof and loopback callback. Store a separate discovery/SSH credential in macOS Keychain. Bind the helper credential to the approving session's deadlines and revocation. Start the helper with a macOS login item. The private MVP uses a locally built, ad hoc–signed app for per-user installation by participating developers and team members. The controller offers the app through **Install Local Helper** and provides the controller HTTPS URL for first-run pairing.

**Rationale.** The current Dex client is confidential and management uses a host-only session cookie. Scoping the native credential to SSH avoids moving that browser cookie or the Dex client secret into a native process. [Native app browser and loopback guidance](https://www.rfc-editor.org/rfc/rfc8252.html#section-7.3)

Gatekeeper may block the first launch of the downloaded app. Workstation policy can also block the download or launch. Keep the Mac's normal security settings and record the exact result. If policy blocks the downloaded path, validate the local build separately. Verify first launch, Keychain retrieval, and login-item startup on the MVP Mac. Developer ID signing and notarization belong to a later broader-distribution release. [Apple Gatekeeper guidance](https://support.apple.com/en-us/102445) · [Apple Service Management](https://developer.apple.com/documentation/servicemanagement/smappservice) · [Apple Keychain](https://developer.apple.com/documentation/security/using-the-keychain-to-manage-user-secrets) · [Apple notarization](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)

**Alternatives considered.** Clipboard tokens, copying the browser cookie, and package scripts that bypass platform trust checks were rejected. The proposed flow needs no new identity provider or standalone daemon on the controller.

## 4. Generate explicit SSH hosts

**Decision.** Maintain one marked block of explicit hosts in the default user SSH configuration. Allocate the readable alias once from the workspace name and persist a mapping to its immutable ID. Use OpenSSH `ProxyCommand` with safe macOS shell quoting and a trusted absolute helper path. Reconcile with a startup sync and a modest background poll, initially proposed at 30 seconds.

**Rationale.** Coder documents per-workspace stanzas for config readers and auto-start on connect. Ona documents browser login, stable aliases derived from workspace identity, and the VS Code workflow. ShiftBox can keep identity stable while using a readable alias. A managed block supports tools that read literal host entries. Test a path with spaces and native OpenSSH discovery with `ssh -G` and an actual VS Code Remote–SSH connection on macOS. [Coder workspace access](https://coder.com/docs/user-guides/workspace-access/) · [Coder per-host configuration](https://coder.com/docs/reference/cli/config-ssh#--no-wildcard) · [Ona CLI](https://ona.com/docs/ona/integrations/cli#ssh-access) · [Ona VS Code](https://ona.com/docs/ona/editors/vscode) · [OpenSSH configuration](https://man.openbsd.org/ssh_config#ProxyCommand)

**Alternatives considered.** Wildcard entries and an include-only file are simpler to render but can hide individual workspaces from config readers. Push sync would introduce another long-lived subscription on the workstation for a list that can reconcile periodically.

## 5. Reuse start, authorization and grant administration

**Decision.** Keep [existing connection authorization](../../internal/web/workspace_authorization.go), [power admission](../../internal/workspace/lifecycle.go) and [active connection registry](../../internal/proxy/registry.go) authoritative. Resolve eligibility for discovery and connect with the same policy inputs. Use the current durable start and operation status, with a bounded wait that fits the tested OpenSSH and editor startup timeouts. Keep provisioning the grant as a separate prerequisite and reuse the existing grant list and revoke operations.

**Rationale.** The exact-grant model, browser audit path, and grant list and revoke flow already exist. The policy already grants lifecycle management to administrators separately from connection. A just-in-time SSH start must require both, then reauthorize before opening the stream. Existing lifecycle precedence and provider recovery remain in force.

**Alternatives considered.** A parallel SSH grant model or SSH start queue would duplicate policy and reconciliation. The install or connect flow must never mint an administrator workspace grant implicitly.
