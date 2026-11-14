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
review request with an empty inventory. T116 still needs the populated
workspace fixture, live provider reconciliation, and the Go acceptance test.

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
