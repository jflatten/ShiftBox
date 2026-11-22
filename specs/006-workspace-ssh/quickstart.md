# Validation Quickstart — Workspace SSH

Run after implementation with the locally built, ad hoc–signed internal app staged on the controller. Use the MVP Mac with normal macOS security settings, native OpenSSH, and VS Code Remote–SSH. Use a disposable workspace in `shiftbox-demo` with a known developer identity and project path. Have a workspace owner, an unrelated member, and an administrator account. Record macOS, OpenSSH, VS Code extension, controller, and guest versions with the results.

## Test first

Write each slice's tests before its implementation, with a comment describing the behavior. Use the focused checks in the [implementation sequence](plan.md#implementation-sequence) and run the touched package suites. From the repository root, the expected commands after implementation are:

```sh
go test ./internal/auth ./internal/web ./internal/proxy ./internal/workspace ./internal/guest ./cmd/shiftbox-helper
npm --prefix ui test -- app-shell helper
```

## Download and first launch

1. In ShiftBox, open **Install Local Helper** from the upper-right personal settings menu. Confirm the link targets the intended staged internal build. A missing or incorrect artifact fails this check. Download it in the browser, verify the downloaded artifact matches the staged build when the download completes, and attempt per-user installation and first launch through the normal macOS prompts.
2. Record whether the downloaded app actually launches. If Gatekeeper or workstation policy blocks the download or launch, capture the exact message and stage at which it stopped. Build and ad hoc sign a copy from the same revision on the MVP Mac, then attempt its first launch with the same security settings. Record the local result separately and leave the downloaded-launch result as blocked. If the local copy also fails to launch, record a functional validation blocker.

## Sign in, discover, and connect

1. Use the downloaded app if it launched, or the locally built copy if macOS policy blocked the download path. Record which copy runs these checks. Add a disposable unrelated host stanza to the native SSH config. Complete browser sign-in and approve the login item through the normal macOS prompts.
2. Confirm the running and authorized stopped aliases appear. Remove eligibility from a disposable workspace and wait for synchronization. Confirm its alias is removed and the unrelated entry is preserved. Verify the scoped credential is stored in Keychain and absent from SSH config. Sign out of macOS, sign back in while the ShiftBox session is valid, and verify that the helper starts automatically, retrieves its Keychain credential, and resumes syncing.
3. Replace `shiftbox-example` with the visible alias. Run the first two commands locally, then run `id -un` and `pwd` inside the SSH session. Compare the terminal identity, directory and project with the browser IDE.

   ```sh
   ssh -G shiftbox-example
   ssh shiftbox-example
   id -un
   pwd
   ```

4. Open a clean VS Code Remote–SSH profile. Set `remote.SSH.localServerDownload` to `always` and select the workspace alias. Confirm the first-time transfer completes, open the project folder, and save a disposable file. Confirm it appears in the browser IDE. [Microsoft transfer setup](https://code.visualstudio.com/docs/remote/ssh#common-questions)
5. Record the pin after the first successful connection. Rename the workspace and confirm the alias is unchanged. Stop/start the workspace, reconnect, and confirm the original pin still verifies it. In an isolated test fixture, exercise an unexpected key change and another workspace's identity. Verify the client gets a clear failure, no SSH session opens, and the original pin stays intact.

## Start, authorize, and revoke on the demo

Use a saved entry for negative connection attempts so they exercise controller authorization even when discovery omits the workspace.

1. Deliberately stop the workspace. Try to connect as an administrator without a connection grant. Expect denial and no new Start operation even though that administrator has lifecycle permission. Confirm the unrelated member is also denied, and neither account discovers the workspace. Then open the alias from two terminals at once as the owner. Confirm one Start operation in Workspaces and that both original attempts land in the primary devcontainer.
2. Have the operator arrange an exact workspace connection grant for the test administrator as a separate prerequisite. Open the workspace's **Administrator connection grants** panel as an authorized administrator and confirm the grant is listed after reload. As the grantee, confirm discovery and connect from SSH. Inspect the connection audit record for the same workspace and exact grant.
3. Revoke the grant through the existing panel with the grantee's SSH terminal open. Confirm the terminal closes, the row stays absent after reload, the alias disappears on sync, and a stale alias cannot reconnect. Sign out of the approving browser session during an owner SSH session and confirm that stream also closes. Cover expiry and offboarding in the focused connection tests using existing session and membership fixtures.

## Clean up

Remove the disposable grant, workspaces and files created for validation. Keep existing demo resources and user SSH entries intact.

## Isolated client proof (T005)

On November 19, 2026, the isolated macOS 26.6.2 fixture used OpenSSH 10.3p1, VS Code 1.146.0 with Remote–SSH 0.128.0, Dev Container CLI 0.90.0, and code-server 4.145.2. Native commands and a PTY ran as `vscode` in `/workspaces/ssh-proof`. OpenSSH rejected a different host key, and its editor bootstrap transfer primitive succeeded. A clean Remote–SSH profile completed initial VS Code Server setup in the network-isolated fixture. A file edited in desktop VS Code opened with the same content in the browser IDE. The proof used a token-gated loopback harness and a disposable container.

## Local controller and helper checkpoint (T006–T011)

On November 19, 2026, the helper credential migration passed against SQLite and disposable TLS PostgreSQL 17. Local controller race tests verified the exact administrator grant, audited SSH admission, denied reconnect after revocation, one Start operation for concurrent authorized connections, and cancellation of a waiting connection without cancelling the durable operation. In an isolated browser, the existing grant panel showed a separately provisioned grant after reload. Revocation closed a live TLS SSH relay and the row stayed absent after reload. The panel was hidden from a member.

Controller pairing tests covered browser session and CSRF validation, expiry, proof and callback binding, one successful exchange under concurrent redemption, and revocation before exchange. Native `ssh -G` tests on temporary files verified the managed aliases and strict pin settings. At that checkpoint, T011 still needed the helper entry point, browser pairing client, Keychain, login-item startup, and discovery loop. No deployment or migration was run on `shiftbox-demo` during this checkpoint.

## Private macOS artifact (T012)

Build on the MVP Mac with `sh platform/helper/macos/package.sh <management HTTPS origin> <output directory>`. The directory holds the app and the ZIP. Run the packaging verification with `sh platform/helper/macos/package.test.sh`. After the production UI export, stage the ZIP as `ui/out/_shiftbox/ShiftBox-Local-Helper.zip` before building the embedded controller. The authenticated download route serves the archive staged for that controller build.

The demo bundle uses ID `com.shiftbox.local-helper`, an arm64 macOS executable, and an ad hoc signature. `codesign --verify --deep --strict` passed on the assembled app and the unpacked verification archive. The final deployed ZIP has SHA-256 `f0eba761cf442a48f9f9c829f81dfe9b7a1bbe0f66e573810be03f0263b0ba3f` and contains the demo management HTTPS origin.

## Live demo verification (T014)

On November 20, 2026, the PostgreSQL migration verified the encrypted management backup, current retained guests, and schema 41 before restarting the controller. The demo validated and published EC2 AMI `ami-0c8a8b6f90cf9b5a7` in the private `workspace-ssh-preview` family. The disposable workspace used this AMI and the existing fixture repository. Existing workspaces retained their image selections.

The authenticated Chrome download matched the initial staged ZIP. macOS preserved quarantine through extraction and per-user installation. The first launch request returned successfully, but the app did not start and the unchanged macOS assessment reported `rejected` (exit 3). The separately built app started with Gatekeeper enabled. Its browser approval returned to the numeric loopback, the scoped Keychain entry appeared, and native SSH retrieved it from a separate proxy process. The existing SSH config outside the helper block was byte-identical.

OpenSSH 10.3p1 entered the primary devcontainer as `vscode` in `/workspaces/project`. With VS Code 1.146.0 and Remote–SSH 0.128.0 in a clean profile, forced local download and transfer completed and the server unpacked successfully. A desktop edit to a disposable file appeared in the same workspace’s browser IDE. A deliberately incorrect throwaway pin was rejected and both the throwaway and original pins were preserved. While the EC2 workspace was stopped, an ungranted administrator and unrelated member were both denied, and the instance stayed stopped. Two simultaneous authorized SSH PTYs then showed startup progress and entered the same devcontainer. The original pin verified after that stop and start.

The first local app logged a Service Management registration error. A disposable native regression probe reproduced the initial `notFound` status and verified that attempting registration from that status yields `enabled` on this Mac. The packaging and helper tests passed with the correction. The final browser download matched the deployed ZIP and the quarantined app again received `rejected` (exit 3) from the unchanged assessment. macOS registered the separately built corrected app at its per-user Applications location as enabled and allowed. After an actual macOS sign-out and sign-in, the helper had a new process and reused its existing Keychain item without opening a new pairing listener. Once Tailscale reconnected, that same process established its own connection to the demo management HTTPS endpoint without being restarted.

The durable journal recorded exactly one successful Start for the two SSH attempts. The separate operator fixture provisioned the exact administrator grant. The existing panel displayed the grant after reload and kept it absent after revocation and reload. The live administrator SSH client exited with status 255, the stale alias received HTTP 403, and discovery synchronization removed that alias. The audit showed one administrator SSH admission bound to the exact grant and one revocation. The owner’s original alias and pin worked after the disposable workspace was renamed.

The final management HTTPS endpoint returned 200 with controller task definition `shiftbox-demo-controller:44` and PostgreSQL task definition `shiftbox-demo-postgres:1` both reporting one running task and `COMPLETED`. The controller digest was `sha256:05ac88c85627854616fcd433713e2b73f4df3ad2ddec0f5ff14c677a86fc913e`. The disposable EC2 instance was terminated with zero remaining workspace EBS volumes. The test alias disappeared, the operator identities and grant were removed, and the temporary runner and task definitions were cleaned up. The test project was archived and its referenced resource profile was disabled. The validated preview AMI remains available for review.
