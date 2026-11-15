# Backup and recovery validation

## Populated `mvp-rc1` recovery

On November 14, 2026, Chrome 157.0.8103.51 used the frozen `mvp-rc1`
controller, SQLite schema 27, and UTM 5.0.8. The source held the ready
native workspace from T061. The browser opened its IDE before backup and
confirmed that the workbench and authenticated IDE HTTP access still worked
after the archive was verified. The browser then validated the archive.

| Check | Result |
| --- | --- |
| Playwright and protected local command | Chrome completed backup and validation. `restore activate` created a separate replacement installation directory. In the restricted listener, Chrome matched the retained workspace as running, completed the three ordered reviews, explicitly activated the installation, and confirmed a fresh sign-in was required. |
| `TestBackupRecoveryJournal` with `-race` | Passed using the same archive, original database, and activated replacement database. Confirmed archive digest and size, matching owner, generation and provider identity, copied management session and origin-grant revocation, and recovery-session revocation on activation. |

The original controller returned to service after the isolated review. The
test verified management state and provider observations. Workspace VM disks
remain outside the archive.

## SQLite recovery on `mvp-rc3`

The exact RC3 controller and guest completed protected SQLite setup in
Chrome. The first run stopped when the disposable fixture helper lacked
the SQLite driver. The corrected helper resumed the same installation,
admitted a native guest, and stopped it after the first authenticated
poll with its original owned disk retained.

With a second local user and a pre-backup browser session present, Chrome
created and verified the encrypted archive and validated its replacement.
The CLI independently refused a wrong passphrase, truncated archive,
incompatible release, mismatched key, and nonempty replacement directory
before publishing any replacement. The nonempty directory kept its
original file.

The protected activation command entered recovery review. Chrome rejected
the copied session and normal workspace and user mutations, reviewed
identities and the stopped VM, and explicitly activated. The recovered
installation retained the local user and required a fresh normal sign-in.
The database and UTM matched the original workspace generation, provider
instance, ownership marker, and disk. A read-only check matched the file
digest and size to the completed source backup and confirmed the restored
normal and recovery-session revocation records. This fixture stopped at
enrollment. The RC1 run above also exercised the live IDE and copied
origin-grant checks. The RC3 VM, owned disk, and disposable files were
removed after verification.

## PostgreSQL recovery

On November 14, 2026, the rebuilt `mvp-rc1` controller backed up a
production-profile test installation with separate application and Dex
owners. Chrome signed in as the administrator, created a second local user,
and retained the live browser session for replay after restore.

| Check | Result |
| --- | --- |
| Archive | The live controller created and verified the password-encrypted archive. Its entries were the manifest, application and Dex dumps, controller configuration, guest CA, and installation key. It contained no VM disks. The restored listener served HTTPS with the same trusted browser certificate. |
| Refused restores | The CLI rejected the wrong password, truncated archive, incompatible release, mismatched key, unowned Dex schema, and non-empty application schema. No replacement directory was published. The non-empty target's original row survived. |
| Protected restore | The CLI validated the two owned schemas and restored to an empty replacement on the same Mac. A fresh administrator session was restricted to recovery review. Chrome verified the copied session was invalid and the normal workspace and member-creation endpoints were unavailable. |
| Review and activation | Chrome confirmed both identities, completed provider and authorization/retention review, and explicitly activated. Fresh administrator sign-in returned to normal mode and listed the restored member. Both the copied session and the recovery session were denied. PostgreSQL recorded an active installation and an activated review. |

The first activation was followed by an intermittent UTM capability lookup
failure as normal service restarted. A fresh replacement from the same
archive completed review, activation, and the return to normal service
without a manual restart. The production-profile fixture had no native
workspace. Matching a live retained workspace while exercising PostgreSQL
recovery was exercised on the next candidate below.

### PostgreSQL recovery with a retained UTM workspace

On `mvp-rc2`, the activated PostgreSQL controller admitted a disposable
workspace through its lifecycle journal. The native guest authenticated
and the controller stopped it while retaining its owned disk. This
fixture stopped after enrollment, before project checkout or IDE setup.
A second local user and a live administrator browser session were also
present before the checkpoint.

The controller verified the archive and the CLI restored into an empty
replacement database with separately owned ShiftBox and Dex schemas.
Chrome denied the copied session, signed in with restricted recovery
permissions, and matched the retained workspace as stopped during
provider review. After the operator explicitly activated, both the copied
and recovery sessions were rejected. Fresh sign-in returned to normal
mode and showed the restored member and stopped workspace. The replacement
database and UTM independently reported the original provider instance,
ownership marker, generation, and disk identity. Cleanup verified removal
of the disposable VM and disk.

## Earlier empty-inventory exercise

On macOS 26.6.2 with the pinned Dex build and Chrome 157.0.8103.51, a
disposable controller served the exported Backup & Recovery page over the
existing trusted test certificate. The administrator used the browser to
create an encrypted archive and validate it against an empty replacement
directory. The browser showed the validation ID after the server completed
the operation.

The protected local command used that ID to restore a separate installation.
Chrome signed in to the restricted listener and completed identity, provider,
and authorization and retention review before explicitly activating normal
service. The restored database recorded the activation, revoked the recovery
session, and returned the installation to active. The source installation
remained active.

This fixture contained zero workspaces. The live check covered the provider
review request with an empty inventory. The new Go acceptance verifier
successfully matched the archive against its recorded digest and size and
found the activated review, then rejected the empty inventory. The populated
exercise above supplied the retained-workspace evidence.

## Populated recovery acceptance

Before capturing the archive, retain a ready workspace and mint a browser
entry grant for it. Use a disposable source installation with the source
database and archive available after restore. Preserve the source database
through verification, including the recorded backup. After the protected
restore and browser review, set `SHIFTBOX_RECOVERY_SOURCE_DATABASE`,
`SHIFTBOX_RECOVERY_RESTORED_DATABASE`, and `SHIFTBOX_RECOVERY_ARCHIVE` to
absolute paths and run:

```sh
go test ./tests/acceptance -run '^TestBackupRecoveryJournal$' -count=1 -v
```

The verifier reads both databases without writes. It checks the exact
archive bytes against the source backup record, the same logical
installation and retained workspace ownership, matched provider review,
explicit activation, and revocation of copied authority. It only verifies
management records and recorded provider observations.

## Supporting checks

- T108’s Chrome tests use API and event stream fixtures to check keyboard
  focus, password and connection URI clearing, ordered server progress,
  revoked workflow access, provider conflict display, and activation gating.
- T106’s recovery tests exercise retained workspace evidence, refusal of
  overdue deletion and workspace entry before activation, invalidation of
  copied authority, and explicit activation.
- T111’s live PostgreSQL tests use separately owned application and Dex
  schemas. They verify damaged dump refusal, refusal to retry a partial load,
  and successful restore after preparing an empty replacement.
- Management archives exclude workspace VM disks.
