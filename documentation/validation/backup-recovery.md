# Backup and recovery validation

## Native SQLite controller and Chrome

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
found the activated review, then rejected the empty inventory. T116 still
needs the populated workspace fixture and live provider reconciliation.

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
