# Provider Interface Contract

The provider adapter is compiled into ShiftBox and runs on the trusted controller host. It translates durable workspace intent into native VM operations. The provider is authoritative for machine/resource facts; the database is authoritative for desired state, ownership, policy, operations, and reservations.

This is an internal Go-facing contract, not a runtime plugin or remote-worker protocol.

## Interface shape

```go
type Provider interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    Inspect(ctx context.Context, ref WorkspaceRef) (Inventory, error)
    Create(ctx context.Context, req CreateRequest) (Inventory, error)
    Start(ctx context.Context, req StartRequest) (Inventory, error)
    Stop(ctx context.Context, req StopRequest) (Inventory, error)
    Delete(ctx context.Context, req DeleteRequest) (Inventory, error)
    ApplyNetwork(ctx context.Context, req NetworkRequest) (Inventory, error)
    DeliverEnrollment(ctx context.Context, req EnrollmentRequest) (DeliveryResult, error)
}
```

Interfaces are declared at the controller consumer boundary. Every method accepts `context.Context` first, returns an inventory observation where applicable, and returns semantic errors. The UTM implementation never calls `os.Exit` or logs and returns the same error.

## Common types

### WorkspaceRef

```text
installation ID
workspace ID
workspace generation
known provider instance ID, if assigned
expected ownership marker
```

The adapter never adopts an instance identified only by a guest-supplied value.

### Capabilities

```text
provider name and validated version
supported architecture and execution mode
create/start/stop/delete support
network modes and whether stopped-only mutation is required
recovery delivery mechanisms
maximum/minimum CPU, memory, and disk bounds
capability evidence/version
```

The UI and admission layer expose only capabilities returned by the active, validated adapter. Unsupported restriction controls remain unavailable with an explanation.

### Inventory

```text
provider instance identity
power state: unknown | stopped | starting | running | stopping | deleted
complete owned-resource list
effective CPU, memory, disk, and network values
platform artifact/release evidence
ownership markers and workspace generation
observed timestamp
provider-native diagnostic reference (redacted)
```

An inventory is evidence, not an instruction. `unknown` never implies stopped or deleted.

### OwnedResource

Each resource has provider ID, type, ownership marker, workspace generation, immutable/shared classification, and provider facts. Types initially include VM configuration, persistent disk, recovery media, and provider-created network policy. Shared catalog artifacts and host networks are never workspace-owned deletion targets.

## Method contracts

### Capabilities

- Performs no mutation.
- Reports only behavior verified for the running provider version.
- Version or permission mismatch removes the affected capability rather than simulating it.

### Inspect

- Performs no mutation.
- Returns the complete matching inventory, including partial/orphaned resources.
- Distinguishes not found from provider unavailable and malformed/ambiguous ownership.
- Is safe before every retry and after every timeout or controller restart.

### Create

Preconditions:

- Durable provision operation and reservation already exist.
- Platform artifact is available and compatible.
- No live provider inventory already satisfies or conflicts with this workspace/generation.

Behavior:

- Creates a writable workspace copy without mutating the immutable catalog artifact.
- Applies pinned CPU, memory, disk, supported network policy, unique ownership, and fresh enrollment delivery.
- On an ambiguous result, returns `OutcomeUnknown` and any evidence; caller reconciles with `Inspect` before retry.
- A repeated request with the same operation/generation returns or reconstructs the same logical instance, never a duplicate.

### Start

- Verifies the exact instance, generation, ownership, network policy, and enrollment eligibility.
- If stopped credentials expired, requires successful `DeliverEnrollment` immediately before start.
- Does not treat provider running state as IDE readiness.
- An ambiguous response remains pending until inspection.

### Stop

- Supports graceful requested stop and, only when the caller supplies explicit retention-deletion authority after its timeout, provider-enforced stop.
- Maintenance authority never sets the force flag.
- CPU/RAM reservation is released only after inventory confirms stopped/deleted.

### Delete

- Requires a committed delete operation with rechecked authorization/policy/activity evidence.
- Enumerates the complete owned-resource set before mutation.
- Deletes workspace-owned resources only; shared catalog/network resources are preserved.
- Completes only when a later inventory confirms every owned resource absent.
- Partial failure returns remaining inventory and never releases disk reservation.

### ApplyNetwork

- Requires confirmed stopped state.
- Validates the requested policy against current capabilities before mutation.
- Re-inspects effective policy after mutation and keeps the VM stopped on mismatch/failure.
- Required controller/proxy, designated DNS, and approved repository exceptions are explicit; redirects or application discovery never widen them.

### DeliverEnrollment

- Requires a stopped, exact known instance; eligible non-revoked registration; current owner and generation; and durable recovery/start operation.
- Replaces only the designated recovery medium source while preserving the entire drive list and VM identity.
- Uses fresh per-attempt media containing one short-lived, one-use grant, endpoint, and current trust bundle.
- Cleans temporary delivery material only after safe consumption/evidence or explicit invalidation.
- Never changes workspace persistent disks, resets first-boot state, or places a permanent recovery password in the guest.

## Error taxonomy

Adapters return typed errors that the controller translates without string matching:

| Error | Meaning | Controller action |
| --- | --- | --- |
| `ErrUnavailable` | Provider/host cannot currently respond | Keep operation waiting and reservations held |
| `ErrOutcomeUnknown` | Mutation response lost/ambiguous | Inspect before any retry |
| `ErrNotFound` | No matching resource | Interpret only in context of desired operation |
| `ErrOwnershipMismatch` | Resource exists but ownership/generation is wrong or ambiguous | Block mutation and require administrator review |
| `ErrCapabilityUnsupported` | Requested behavior is not verified | Reject admission or keep VM stopped |
| `ErrInvalidState` | Provider state cannot accept operation | Reconcile and expose conflict |
| `ErrPartial` | Some owned resources changed; inventory included | Continue cleanup/recovery from evidence |
| `ErrPermission` | Host/provider permission denied | Persist actionable administrator failure |

Provider-native errors may be wrapped for diagnostics but are not exposed directly to users or used as durable state keys.

## UTM adapter rules

- Pin and verify the supported UTM version and Packer-compatible plugin fork in the release manifest.
- Use the supported local scripting API; do not edit UTM configuration bundles directly.
- Serialize conflicting mutations for one workspace and generation.
- Normalize script output into typed inventory before returning it to domain code.
- When updating an identified list such as drives, submit the complete preserved list with only the designated item changed.
- Never infer successful shutdown, media replacement, or deletion solely from command exit; inspect afterward.
- Keep provider credentials and controller paths outside guest-visible media except the intentionally delivered one-use enrollment payload.

## Acceptance suite

Every provider implementation must pass a reusable contract suite covering:

1. Idempotent create and start after lost responses.
2. Complete inventory and distinct cloned identities/disks.
3. Stop versus delete semantics and reservation evidence.
4. Partial deletion with retry and shared-resource preservation.
5. Stopped-only network mutation and unsupported capability hiding.
6. Recovery-media replacement without drive-list changes.
7. Concurrent start/delete/recovery generation fencing.
8. Host sleep/unavailability and later reconciliation.
9. Permission/version mismatch with actionable errors.
10. Full owned-resource deletion confirmation.

The fake provider runs this suite in ordinary tests. The UTM adapter runs the same suite plus exact Packer/import/boot/recovery validation in the controlled release environment.
