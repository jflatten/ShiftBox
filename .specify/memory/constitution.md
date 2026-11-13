<!--
Sync Impact Report
Version change: unratified scaffold → 1.0.0 (initial adoption)
Principles added: KISS, explicit ownership and trust, durable user work,
test-first delivery, visible workflows.
Sections added: Architecture Constraints, Development Workflow, Governance.
Sections removed: none. Follow-up TODOs: none.
-->

# ShiftBox Constitution

## Core Principles

### I. KEEP IT SIMPLE STUPID (KISS)

KISS governs every design and implementation choice. Teams MUST choose the smallest
solution that meets the approved requirements and preserves the security, data,
and operational contracts. Use existing code, standards, and dependencies where
they fit. Keep code readable and use comments to explain intent that the code
cannot convey. A proposal that adds a dependency, service, or abstraction MUST name
the current requirement it serves and explain why a simpler approach falls short.

### II. Keep Ownership and Trust Explicit

ShiftBox MUST own workspace intent, authorization, and reconciliation. VM providers
MUST own machine infrastructure, and guest services MUST handle work inside their
own VMs. Treat the entire guest as untrusted. Keep provider credentials, signing
keys, and controller authority outside guests. Authorize each workspace access
and secret retrieval at the controller. Keep browser management credentials out
of IDE and preview traffic. Advertise only network controls the provider can enforce.

### III. Preserve Work and Reconcile Before Acting

Designated workspace files MUST survive reboot, stop/start, and container rebuild.
The UI MUST communicate that VM destruction loses those files and that management
backups cover controller and identity state. Destructive actions MUST follow current
authorization or explicitly configured retention policy. After a crash, timeout,
or uncertain response, reconcile actual provider state before retrying side effects.
Release reservations only after the provider confirms the relevant stop or removal.
Maintenance MUST preserve the agreed safe upgrade boundary.

### IV. Test First and Verify Real Failure Paths

Write unit tests before implementing code. Every test MUST include a comment that
explains the behavior under test. Tests MUST cover realistic failures relevant to
the change, including authorization errors, retries, or partial failure where they
apply. Verify changed integration contracts against their supported configuration
before claiming support. Follow the architecture's functional and security release
gates. Defer scaling and load tests until the product is fully implemented.

### V. Make Workflows Visible

The UI MUST show workflow progress in real time using Mantine Stepper. Use Mantine
controls, consult the Mantine MCP server when selecting components, and apply the
project's branding assets from `branding/`. Show actionable failures and make
destructive consequences clear before users commit. Verify keyboard access and
focus behavior for affected workflows.

## Architecture Constraints

Phase one is a greenfield, self-hosted installation for one organization with one
active Go controller. Run it natively beside UTM on macOS. Use Packer-built Ubuntu
24.04 LTS Minimal amd64 VM artifacts and one primary devcontainer per workspace.
VS Code Browser is the initial IDE. EC2 and Linux/container production deployment
belong to phase II.

The Go controller serves the static React, Next.js, TypeScript, and Mantine UI
over HTTPS. Dex owns authentication through OIDC. ShiftBox owns application
permissions. Use SQLite for demo and very small installations and operator-managed
PostgreSQL for production, with separate ownership of ShiftBox and Dex data.

Use the [design readback](../../documentation/architecture/design-readback.md) and
[decision record](../../documentation/architecture/shiftbox-design-decisions.md)
for detailed contracts. The
[technology register](../../documentation/architecture/technology-decisions.md)
records dependency choices and their status. Keep engineering proposals and
unvalidated integrations identified as such.

## Development Workflow

1. Read the architecture and applicable project guidance before writing a spec or
   changing code. Map the work to approved requirements and implement only the
   requested tasks.
2. During planning and review, check the change against KISS, ownership, failure
   handling, and user-visible behavior. Record the justification for any added
   complexity with the plan or change.
3. Write the relevant tests first, implement the simplest conforming solution, and
   run the checks that establish the requested behavior. Use the
   [release validation gates](../../documentation/architecture/validation-and-engineering-defaults.md#mandatory-release-validation)
   to determine what evidence a supported release requires.

## Governance

This constitution governs specifications, plans, implementation, and review.
Reviewers MUST verify compliance before accepting a change. Changes to approved
scope or architecture require human approval. Propose amendments with the affected
principle, rationale, and consequences for existing specs and operations. Record
approval, the amendment date, and the version in this file.

Use semantic versioning. Increment the major version for incompatible changes to
governance or principles, the minor version for new or materially expanded guidance,
and the patch version for clarifications that preserve existing obligations.

**Version**: 1.0.0 | **Ratified**: 2026-11-13 | **Last Amended**: 2026-11-13
