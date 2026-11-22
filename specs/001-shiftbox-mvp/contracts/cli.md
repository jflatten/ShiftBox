# ShiftBox CLI Contract

The `shiftbox` executable is the native controller and protected operator CLI. The `shiftbox-agent` executable is installed inside platform VMs and exposes only its supervised service plus the developer secret retrieval command.

All commands write human-readable status to stderr and requested data/artifacts to stdout or the named output. `--json` changes status/results to versioned JSON with stable error codes. Secret values, passwords, private keys, database setup credentials, and enrollment grants never appear in process arguments, logs, or JSON diagnostics.

## Controller commands

### `shiftbox serve`

Starts the configured controller, managed Dex process, reconciler, proxy, and management listeners.

```text
shiftbox serve --config <path>
```

Rules:

- Default startup is manual.
- Configuration path is explicit or resolved from one documented installation-directory default.
- Startup preflight checks persistent directory, installation key, active data profile, migrations, Dex configuration, browser certificate, internal CA, provider version/capabilities, guest compatibility gate, and backup utility compatibility.
- Unsafe preflight failure prevents normal mutation and reports the recovery action. It never starts against bootstrap SQLite after production activation.

The `runtime` object in the configuration file supplies the following settings.

| Setting | Value |
| --- | --- |
| `browserAddress` | Browser HTTPS bind address including a port |
| `guestHost` | Hostname or IP address reachable from the UTM guests, without a port |
| `enrollmentAddress`, `pollAddress`, `tunnelAddress` | Distinct listener bind addresses including ports |
| `utmStorage` | Absolute path to UTM's existing Documents directory |
| `utmMinimum`, `utmMaximum` | Validated allocation limits with `cpu`, `memoryMiB`, and `diskMiB` |
| `dexBinary` | Optional absolute path to the pinned Dex executable, which defaults to `dex` alongside the controller |
| `githubAppFile` | Optional absolute path to a private JSON file containing `clientId`, `clientSecret`, and `installationId` |

### `shiftbox setup`

Creates or resumes protected local bootstrap and prints the loopback setup URL plus the one-time first-administrator password only when it is newly generated.

```text
shiftbox setup --installation-dir <path> --administrator-email <email>
```

Optional certificate/key inputs are file paths read by the process, not raw key arguments. Re-running setup preserves the existing owner/password and resumes durable steps.

Provide `--config <path> --platform-folder <absolute-folder>` for the provider
step. The folder holds the UTM bundle beside its `manifest.json`. Setup uses
the controller configuration for UTM storage, guest connectivity, and
resource bounds.

### `shiftbox backup`

Creates a manually initiated encrypted management backup.

```text
shiftbox backup --output <archive-path>
```

- Password is read twice from an attached terminal or stdin when `--password-stdin` is explicitly selected.
- The command coordinates with the running controller rather than copying live files.
- Success is printed only after checkpoint, archive close, and verification.
- Workspace disks are never included.

### `shiftbox restore validate`

Validates an archive and proposed empty replacement target without activation.

```text
shiftbox restore validate --archive <path> [--postgres-config <path>]
```

Outputs compatibility, owned schemas/roles, required key disposition, and review steps. Wrong password, truncation, incompatible release, non-empty/unowned target, or mismatched mounted key fails before mutation.

### `shiftbox restore activate`

Activates a previously validated restore transaction and enters recovery-review mode.

```text
shiftbox restore activate --validation-id <id>
```

This command never bypasses administrator review, provider reconciliation, stale-session/grant invalidation, or user-access gating.

### `shiftbox recover-admin`

Protected local recovery of an existing administrator.

```text
shiftbox recover-admin --email <existing-email>
```

- Requires local installation state and database access.
- Refuses unknown users and non-administrators.
- Displays a one-time temporary password, forces change, revokes existing sessions, and records an audit event.
- Does not create a new administrator or expose a network endpoint.

### `shiftbox migrate preflight`

Checks a release before an irreversible application or Dex migration.

```text
shiftbox migrate preflight --release <release-manifest>
```

Requires a verified management backup, all retained guests current, compatible tools, key access, provider compatibility, database ownership/privileges, certificate validity, and CA lifetime. It does not perform the migration.

## Guest commands

### `shiftbox`, `shiftbox help`, and `shiftbox --help`

Print the complete guest command help. Unknown commands or command options also
print help and return exit 2; explicit help and no arguments return exit 0.

### `shiftbox watch <PID>`

Run in the foreground and keep the workspace alive while the original process
exists in this container's PID namespace. Sleeping processes count as alive;
exited/zombie processes and reused PIDs do not. Ctrl+C ends the watch without
terminating the watched process. A killed watch command's local lease expires
after 15 seconds. Watch heartbeats renew every 5 seconds, and the agent reports
them through authenticated polling. Background CPU use alone does not keep a
workspace alive.

### `shiftbox secrets list`

List sorted effective secret keys with their winning scope, without reading
values. Duplicate names resolve User → Project → Organization.

```text
DATABASE_URL (project)
EXAMPLE_TOKEN (user)
SHARED_ENDPOINT (organization)
```

### `shiftbox-agent serve`

Runs only under the platform supervisor. Configuration and enrollment paths are fixed by the platform image and protected from the development container. It has no arbitrary execute subcommand.

### `shiftbox secrets get <name>`

Returns the effective authorized secret value to stdout and nothing else. Diagnostics go to stderr.

```text
shiftbox secrets get DATABASE_URL
```

`shiftbox secrets get-secret <name>` remains a compatibility alias. Values are
written exactly, without an extra newline.

Rules:

- The guest service derives workspace identity from its authenticated registration; the caller cannot choose owner, template, organization, or workspace scope.
- Exit 0 means a value was written, including an intentional empty value.
- Exit 3 means no effective value exists.
- Exit 4 means current authorization denies retrieval.
- Exit 5 means controller access is unavailable.
- Values are never cached persistently, printed in logs, or returned by a `list`/`show` management command.

### `shiftbox git credential`

Internal Git credential-helper entry point used for supported GitHub HTTPS operations. It retrieves only the current owner-authorized access token and never prints refresh/app credentials. It is not a bundled `gh` launcher or PR workflow.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Completed successfully |
| `1` | General operation failure with stable error code on stderr/JSON |
| `2` | Invalid command or input |
| `3` | Requested record/value not found |
| `4` | Authentication or authorization denied |
| `5` | Required controller/provider/data service unavailable |
| `6` | Conflict, stale version, or operation already in progress |
| `7` | Validation failed before mutation |

## JSON result envelope

```json
{
  "apiVersion": "shiftbox.io/cli/v1",
  "command": "backup",
  "status": "succeeded",
  "result": {
    "operationId": "uuid",
    "archive": "/validated/path/backup.age",
    "manifestDigest": "sha256:..."
  },
  "problem": null
}
```

Problems use the same stable `code`, `title`, `detail`, `retryable`, and `requestId` shape as the management API. Protected fields are omitted rather than redacted placeholders that might be mistaken for values.
