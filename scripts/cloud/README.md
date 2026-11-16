# Cloud development helpers

These scripts record the setup prepared in the shared Linux cloud environment
for `feature/mvp`. They are specific to that environment, not a general installer.
Invoke them with `bash` from any working directory.

Demo startup is paused until an AWS provider is implemented. Do not run either
startup script automatically. The existing SQLite profile still requires a Mac,
UTM, and a validated platform image before activation; these scripts do not
provide a simulated provider or bypass that requirement.

## Retained prerequisites

The prepared environment contains the checkout at `/workspace/ShiftBox` and these
dependencies outside Git:

- Checksum-verified Go 1.27.4 at `/workspace/.shiftbox-tools/go`.
- Checksum-verified Node.js 26.13.1 at
  `/workspace/.shiftbox-tools/node-v26.13.1-linux-x64`.
- Dex sources checked out at tag `v2.46.1` in
  `/workspace/.shiftbox-tools/dex-src`.

`env.sh` activates those tools and directs dependency/build caches to writable
directories under `/workspace/.shiftbox-tools`. It sets build parallelism to four.
`install.sh` refreshes locked dependencies, builds the UI and controller, and
builds the pinned Dex binary. It does not install the prerequisites above.

## Manual startup, when requested

`start-ui.sh` starts the Next.js development server on port 3000, listening on all
interfaces. It serves the UI only; it does not supply backend APIs. Check the
listener before starting another instance and verify the `/setup/` response.

`start-setup.sh` starts the supported restricted bootstrap flow with administrator
email `demo@shiftbox.test`. It preserves installation state in the ignored
`.shiftbox/demo` directory. Its dynamically allocated loopback HTTPS address and
certificate fingerprint appear in `/workspace/shiftbox-env/setup.log`. Verify TLS
with `.shiftbox/demo/bootstrap-tls.pem` before checking `/setup/` and
`/api/v1/setup/status`.

The initial password is emitted once to
`/workspace/shiftbox-env/private/initial-password`, protected by a private umask.
The script appends rather than overwrites so restarting cannot erase it. Do not
commit installation state, keys, credentials, or caches. Processes must be
started again after restoring an environment snapshot.

## Validation status

Dependency installation and the production UI export completed during setup.
Controller/Dex builds and test runs were stopped when the demo was deferred;
they have not been verified as passing. No demo was activated or published.
The environment draft separately records the paused startup instructions;
publishing environment configuration is outside these Git helpers.
