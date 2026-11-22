# ShiftBox MVP Validation Quickstart

This guide defines how an implementer validates the MVP described by the feature specification and plan. The repository is currently in the planning phase, so the commands below are the expected validation interface **after implementation**; they are not claims that an application or test suite already exists.

## 1. Validation environment

Use the release manifest as the authoritative compatibility record. At minimum, prepare:

- macOS on the supported Apple hardware and the exact supported UTM release;
- the approved Ubuntu `amd64` platform image containing the pinned guest agent and browser IDE;
- Go 1.27.4 for controller and guest builds;
- Node.js 26 LTS with npm for UI build and browser tests only;
- SQLite for bootstrap and local/demo coverage;
- PostgreSQL with a dedicated database, owned schema, TLS, and the privileges required by the migration preflight;
- a browser that can trust the installation's local certificate and resolve the configured management, IDE, and preview service names; and
- enough CPU, memory, and disk to validate both successful admission and explicit capacity refusal.

Do not substitute unrecorded UTM, platform-image, guest-agent, browser-IDE, or controller versions in release-gate tests. Record the exact versions and artifact digests in the release manifest.

## 2. Planned validation commands

Run these from the repository root after their corresponding implementation tasks exist:

```text
go test ./...
go test -race ./...
go test -tags=integration ./tests/integration/...
go test -tags=utm_release ./tests/release/...

npm --prefix ui ci
npm --prefix ui test
npm --prefix ui run build
npm --prefix ui run test:e2e
```

Every unit test must be written before its implementation and include a comment describing the real-world behavior or failure it covers. Integration and release suites may be slower, but they must use the same public contracts and state transitions as production.

## 3. Bootstrap and activation

1. Start with an empty installation directory and no production data profile.
2. Run the protected local setup command and open only the loopback setup URL.
3. Complete the Stepper: first administrator, forced password change, data profile, service names/TLS, provider/artifact, validation, and activation.
4. Confirm the temporary password is emitted exactly once and never appears in logs or browser storage.
5. Activate PostgreSQL and confirm bootstrap sessions end, a fresh sign-in is required, and the controller cannot silently fall back to SQLite.
6. Restart the controller during each durable setup step and confirm it resumes without duplicating the administrator, schema, certificates, or provider records.

Expected result: setup reaches an activated installation only after all preflight checks pass; failures remain actionable and resumable.

## 4. Workspace launch and durable files

1. Create a template revision with a repository, approved platform artifact, fixed resources, network policy, Dev Container source, and one supported retention policy.
2. Launch a workspace as an authorized member and watch Provision, Enroll, Configure, and Ready in the Mantine Stepper.
3. Confirm Ready is withheld until the guest registration, Dev Container connection stage, and proxied IDE check all pass.
4. Write files inside the retained workspace paths, stop and start the workspace, restart the controller, and verify the files remain.
5. Run Repair and verify project automation is not rerun and files/Git state are unchanged.
6. Run Rebuild and verify the UI states its contract before the action and the resulting files match that contract.

Expected result: desired and observed state converge after interruptions without duplicate VMs, and retained files survive every lifecycle action whose contract promises preservation.

## 5. Templates, secrets, and Git authorization

1. Store organization, template, and user secret values, including an intentional empty override.
2. Confirm management APIs and UI return names and metadata but never stored values.
3. Retrieve an effective secret from an authorized workspace with `shiftbox secrets get-secret <name>` and verify the documented success, missing, denied, and controller-unavailable exit codes.
4. Delete an override and confirm the lower-scope value that will become effective is named before confirmation.
5. Grant another user template launch access, acknowledge the project-secret warning, and verify workspace code can retrieve project secrets only after that grant.
6. Exercise supported GitHub HTTPS credentials and confirm tokens are obtained just in time and are absent from images, disks, URLs, and logs.

Expected result: secret precedence and authorization are deterministic, while secret values and Git credentials cross only the authenticated guest retrieval boundary.

## 6. Capacity, failure, and reconciliation

1. Exhaust CPU, memory, and disk independently and attempt a new launch.
2. Confirm the request is refused with the exhausted resource identified; no queue or eviction is offered.
3. Interrupt the controller after provider creation but before the operation is marked complete, then restart it.
4. Simulate a lost provider response and retry the same idempotent request.
5. Force partial cleanup failure and confirm the reservation remains held until provider absence is verified.
6. Introduce a provider object that conflicts with controller records and confirm reconciliation reports the conflict rather than adopting or deleting it silently.

Expected result: admission never overcommits configured capacity, retries do not duplicate provider resources, and ambiguous resources remain visible for repair.

## 7. Origin isolation and revocation

1. Sign in on the management origin and open an IDE plus a preview origin using short-lived entry grants.
2. Inspect requests and confirm management cookies are never sent to IDE or preview origins.
3. Attempt cross-origin state-changing requests without a valid CSRF token and trusted Origin; confirm denial.
4. Revoke the session while HTTP, SSE, WebSocket, IDE, and preview connections are active.
5. Disable the user and confirm all remaining sessions close and each owned workspace receives a visible stop operation.

Expected result: hostname routing alone never authorizes access, and revocation closes live connections as well as blocking future requests.

## 8. Certificates and guest recovery

1. Enroll a provider/guest with a one-time grant and confirm the grant cannot be replayed.
2. Allow a guest certificate to approach renewal and verify renewal occurs over the authenticated channel.
3. Make a guest stale beyond the compatibility gate and confirm irreversible migrations are blocked.
4. Exercise a lost guest identity and confirm recovery requires an explicit replacement/enrollment flow rather than accepting the old identity.
5. Test CA lifetime and rotation preflight with retained workspaces, including an offline guest.

Expected result: controller-to-guest trust remains installation-scoped, replay-resistant, renewable, and safe across retained/offline workspaces.

## 9. Retention and maintenance

1. Configure an inactivity policy and generate activity only through declared signals.
2. Verify warnings include the deadline, last activity, retained-data consequences, and outage-grace state.
3. Cross the deadline and confirm graceful shutdown occurs before provider-enforced deletion.
4. Begin a maintenance campaign with an explicit deadline and target release.
5. Keep a workspace busy through the deadline and confirm maintenance waits for a safe boundary; it must not force-kill the workspace under maintenance authority.
6. Allow the update, verify the target release, and confirm the prior stopped/running state is restored.

Expected result: retention deletion and maintenance use distinct authority, state machines, and user-facing consequences.

## 10. Backup, restore, and recovery review

1. Create an encrypted backup and verify it contains the application checkpoint, identity checkpoint, configuration, certificates, key metadata, and manifest—but no workspace VM disks.
2. Validate wrong password, truncated archive, incompatible release, mismatched key, and non-empty/unowned PostgreSQL target failures before mutation.
3. Restore into an empty replacement target and activate recovery mode.
4. Confirm ordinary mutations and workspace entry remain disabled while identities, provider inventory, workspace matches/conflicts, authorizations, and retention are reviewed.
5. Confirm restored sessions, entry grants, enrollment grants, and stale OAuth state are invalidated.
6. Finish the review and explicitly activate normal service.

Expected result: restore is transactional, never merges with existing state, and cannot silently restore stale authority or claim to include workspace disks.

## 11. Release gate

A release candidate is acceptable only when:

- unit, race, integration, UI, browser, accessibility, and exact-stack UTM suites pass;
- the moderated first-use study includes at least 10 eligible participants who have not used ShiftBox and records at least 9 first-attempt successes plus every failure;
- every durable workflow survives controller restart at each persisted step;
- the OpenAPI, guest, provider, CLI, and UI contracts match observed behavior;
- migrations and rollback/preflight behavior are verified against supported SQLite and PostgreSQL profiles;
- no logs, API responses, URLs, browser stores, backups, or process arguments expose protected values; and
- the exact controller, agent, platform artifact, UTM, Dex, browser IDE, and schema versions are recorded.

Use [data-model.md](./data-model.md) for invariants and state transitions, [openapi.yaml](./contracts/openapi.yaml) for management requests, and the remaining files in `contracts/` for guest, provider, CLI, and UI acceptance boundaries.
