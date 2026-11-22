# Implementation Plan for Private ShiftBox on AWS

**Branch** `feature/mvp`  
**Updated** 2026-11-16  
**Spec** [Private ShiftBox on AWS](spec.md)

## Summary

Deliver a Terraform-deployed private EC2-only installation for phase II. A single ECS-on-EC2 host runs a ShiftBox service and a separate PostgreSQL service. Ship the Go controller, static UI, pinned Dex helper and PostgreSQL client utilities in one Linux controller image. Use one encrypted data volume with separate host bind mounts for the database and installation. The local Mac installation remains an independent UTM deployment.

## Technical Context

| Area | Plan |
| --- | --- |
| Infrastructure | Terraform and locked AWS provider. ECR, single private ECS host, encrypted gp3 EBS, ECS task definitions and services, Cloud Map and private Route 53 records, security groups, logging and role separation. One VPC with private service and workspace placement and outbound connectivity. |
| Controller | Versioned Linux amd64 image using the existing Go and static UI build, a pinned Dex release, and matching PostgreSQL client utilities. Run one exclusive controller and the managed Dex child process. |
| PostgreSQL | Pin one supported PostgreSQL major for the service and client tools. Persist its data directory separately from ShiftBox. Verify DB TLS and scoped role ownership for ShiftBox and Dex, following the existing production setup. |
| Initialization | Deploy prerequisites and PostgreSQL, keep the controller service at desired count zero, and guide the deployer through a one-off task using Session Manager and protected loopback HTTPS. Activate identity and DB/TLS before normal startup, then verify the AMI in the running controller before opening workspace launches. |
| Networking | Private service discovery for database and controller. Private records for management, guest, IDE, preview and repair names. PostgreSQL accepts connections only from controller and setup tasks. Browser ingress is restricted to deployer-provided private sources. Guest enrollment, polling and tunnel ports accept only the workspace network. The deployer supplies client VPN/routing, DNS resolution and certificate inputs. |
| IAM | Explicit ECS task credential source for the EC2 adapter. Separate execution, host, controller and bootstrap roles. No controller permissions on PostgreSQL or guests. Keep the AWS key-entry proposal for a later off-AWS controller milestone. |
| Workspace image | Separate Packer Amazon EBS recipe for a pinned Canonical Ubuntu 24.04 LTS Minimal x86-64 base image. Reuse guest provisioning assets. Publish the private regional AMI and provenance with separate release credentials. |
| Testing | Commented test-first checks for storage, provider mode, credential isolation, lost launch and partial delete. One live deployment acceptance run with focused UTM regression coverage. |

## Constitution Check

- Reuse the shipped controller, Dex, installer wizard, guest protocol, migration and backup behavior. Cloud Map and host-bound EBS satisfy stable naming and state without an additional controller service.
- Terraform owns shared infrastructure, Packer owns release images, and ShiftBox owns workspace instances and their attachments.
- Keep one controller active. Gate services on the mounted data volume, preserve data through replacement, reconcile cloud effects before retries, and keep the workspace data-loss warning.
- Retain the Mantine Stepper and status feedback when adding cloud-specific initialization and platform steps.
- The operator-managed Terraform PostgreSQL service preserves the approved PostgreSQL ownership, TLS, and backup boundaries. No managed DB or VPN service is required.

## Implementation Sequence

1. **Deployment foundation.** Build the controller image and Terraform foundation. Pin tested tools and image inputs. Create the private VPC, outbound route, segregated security groups, ECR and durable encrypted EBS. Reuse an operator-supplied outbound route when configured, and supply a single NAT path for a self-contained network otherwise.
2. **Control plane and protected setup.** Place the two ECS services and one-off setup task on the same private ECS host. Mount the persistent volume before scheduling; use host-level path separation and task-level network isolation. Add Cloud Map, role isolation, and private endpoints. Reuse the local wizard through a scoped Session Manager loopback tunnel and deliver the administrator password through protected setup. Activate the normal controller with launches gated on platform readiness.
3. **EC2 and image.** Add the compiled EC2 adapter and cloud-only composition. Bind workspace provisioning to the regional AMI and one verified resource shape. Launch an inert, tagged instance with a stable token, reconcile it to stopped, then use the existing scoped grant protocol. Test the EC2 user-data IMDSv2 delivery candidate in the exact environment before relying on it.
4. **Catalog and lifecycle.** Register pinned releases, validate a disposable guest and verified cleanup, and publish the release only after validation. Route launch, stop, start, recovery, delete and maintenance to the stored workspace binding. Preserve capacity under uncertainty and verify all owned-resource deletion.
5. **Release validation.** Run one live acceptance pass through stack setup, task and host replacement, AMI enrollment, private IDE, retained files, expired trust and deletion. Use focused local coverage for the highest-risk failures and a UTM regression run.

## Project Structure

| Path | Changes |
| --- | --- |
| `platform/container/` | Controller image, build and smoke entry points. |
| `platform/terraform/aws/` | AWS deployment, infrastructure tests, and operator inputs and outputs. |
| `platform/packer/aws/` | Pinned regional AMI build and release manifest. |
| `cmd/shiftbox/`, `internal/app/`, `internal/config/` | EC2-only composition, guided setup and task-role configuration. |
| `internal/provider/ec2/`, `internal/workspace/`, `internal/guest/`, `internal/release/` | EC2 inventory, lifecycle, enrollment delivery, and catalog validation. |
| `migrations/{sqlite,postgres}/`, `internal/web/`, `ui/features/` | Provider persistence, platform readiness and administration. |
| `tests/contract/`, `tests/release/` | Focused authorization, reconciliation and exact-stack validation. |

See [data model](data-model.md), [contracts](contracts/aws-provider.md), and [quickstart](quickstart.md).

## Resource profile AMI ownership (T025)

The user-approved follow-up makes an AWS resource profile the owner of its platform artifact, EC2 type, and EBS size. Reuse the admitted regional image catalog and zone offerings. Persist the platform artifact reference in both database dialects and validate the image's availability, architecture, and minimum root disk against the selected instance and disk. Backfill only an unambiguous EC2 image referenced by an existing profile's template revisions; an unresolved profile requires administrator selection.

AWS template forms select a resource profile without an independent AMI control. The server derives the template's stored image reference for compatibility with existing revision storage, while new workspace admission resolves the current profile image and captures it in the immutable workspace binding. Existing workspaces and the UTM image-selection flow retain their behavior. Test migration, rejected image/type combinations, AMI changes between launches, UI filtering, and profile persistence before applying schema 33 and deploying the demo.
