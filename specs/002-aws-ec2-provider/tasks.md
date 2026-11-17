# Tasks for Private ShiftBox on AWS

**Inputs** [Specification](spec.md), [plan](plan.md), [contracts](contracts/aws-provider.md), [data model](data-model.md), and [quickstart](quickstart.md).

Every implementation task starts with a small, commented test or an applicable existing test that exercises the changed behavior. A successful deployment run can cover several acceptance scenarios together.

## Phase 1 · Setup

- [x] T001 [P] Add a build/smoke check first, then build the Linux amd64 controller with existing static UI, pinned Dex and PostgreSQL 17 client tools in `platform/container/Dockerfile` and `platform/container/`. Include the build context exclusions in `.dockerignore`.
- [x] T002 [P] Write a mocked Terraform test first, then create the locked Terraform root, private VPC, deployer CIDR inputs, private service/workspace subnets, single outbound NAT path, controller ECR repository, and separately managed encrypted gp3 data volume in `platform/terraform/aws/{versions,variables,network,foundation,outputs}.tf`. Include Terraform state and plan ignores.
- [x] T003 [P] Establish a release input validation check and build a standalone `amazon-ebs` HCL recipe in `platform/packer/aws/` that pins an explicit Canonical Noble Minimal x86-64 source AMI and reuses the ShiftBox agent and toolchain assets. Produce a private AMI provenance manifest with distinct build credentials.

## Phase 2 · Foundational

- [x] T004 Test the mount guard, then implement one private ECS-on-EC2 host and cluster in `platform/terraform/aws/ecs_host.tf`. Mount the Terraform-owned data volume before registering capacity, create distinct state paths and fail startup when the designated volume is absent. Keep the data volume independent of host replacement.
- [x] T005 Define separate ECS instance, execution, controller and setup roles in `platform/terraform/aws/iam.tf`. Use ECS task credentials for controller AWS calls, block instance metadata access from tasks, scope SSM forwarding to setup and EC2 mutations to tagged resources, and deny workspace access to deployment IAM.
- [x] T006 Add explicit provider mode and cloud runtime composition in `internal/config/config.go` and `cmd/shiftbox/serve_runtime.go`. Test that EC2 mode skips UTM startup dependencies and that implicit local settings still select UTM.

## Phase 3 · US1 Deploy and initialize a private installation (P1)

**Independent test** Deploy the two services, complete the protected wizard, sign in over private DNS and verify retained state across replacement.

- [x] T007 [US1] Implement the separate single-task PostgreSQL and controller services with EBS bind paths, task security groups, PostgreSQL TLS and private Cloud Map and Route 53 records in `platform/terraform/aws/{services,dns}.tf`. Verify private network and storage assertions in Terraform tests.
- [x] T008 [US1] Add a one-off setup task and deployer initialization commands in `platform/terraform/aws/bootstrap.tf` and `platform/container/`. Scope SSM transport to the setup task and deliver its one-time password through the protected session without logging it.
- [x] T009 [US1] Test and wire AWS-only setup in `cmd/shiftbox/setup.go`, `internal/app/setup_platform.go` and corresponding existing setup UI. Preserve loopback TLS, scoped ShiftBox/Dex PostgreSQL ownership, forced password change and normal TLS activation. Publish an initialized controller while launch remains gated on image validation.
- [x] T010 [US1] Document Terraform plan/apply phases, digest publication, deployer DNS/VPN and TLS inputs, setup session and replacement checks in `platform/terraform/aws/README.md` and `specs/002-aws-ec2-provider/quickstart.md`.

## Phase 4 · US2 Publish and qualify the EC2 platform (P1)

**Independent test** Register one real regional release and observe the disposable instance clean up after authenticated contact. A mismatched candidate remains unavailable.

- [x] T011 [P] [US2] Implement minimal regional AMI catalog metadata, provider discriminator with legacy UTM backfill, one verified workspace shape and immutable bindings in `internal/release/`, `internal/workspace/` and both migration dialects.
- [x] T012 [P] [US2] Test reconciliation of a lost launch response, then implement task-role EC2 client, tagged launch, stable token, verified inventory and inert first boot to stopped in `internal/provider/ec2/` and `internal/workspace/provision.go`.
- [x] T013 [US2] Prove stopped-instance enrollment using the proposed bounded IMDSv2 seed path in `internal/guest/`, `platform/guest/` and `internal/provider/ec2/`. Test real grant confidentiality and loss of an AWS response in a single focused run. Keep release blocked if the delivery proof fails.
- [x] T014 [US2] Route admission and disposable canary to the EC2 provider in `internal/app/platform_boot.go` and `internal/app/platform_import.go`. Validate metadata, authenticate a disposable guest on private listeners, verify cleanup, then enable the AWS image.

## Phase 5 · US3 Launch and use a private AWS workspace (P1)

**Independent test** Launch the one supported AWS shape, open the private IDE, and verify retained paths through restart and rebuild.

- [x] T015 [US3] Route launch, start, stop and IDE readiness by immutable provider binding in `internal/workspace/`, `internal/app/workspace_runtime.go` and `internal/web/workspace_routes.go`. Confirm retained root EBS on stop and reuse guest workspace behavior.
- [x] T016 [US3] Show the AWS provider and image in existing template and workspace UI in `ui/features/`, using shared Mantine progress and status. Verify the reference browser flow and one standalone UTM regression.

## Phase 6 · US4 Recover and delete a private AWS workspace (P2)

**Independent test** Re-enroll the same stopped instance after expiry, then delete it and verify resource and capacity cleanup.

- [ ] T017 [US4] Reuse stopped-instance enrollment authorization for EC2 recovery and preserve maintenance and retention policy routing in `internal/guest/enrollment.go` and `internal/workspace/`.
- [ ] T018 [US4] Test partial deletion, then reconcile instance, owned volume and interface removal before releasing capacity in `internal/provider/ec2/` and `internal/workspace/reservation.go`. Preserve Terraform and Packer resources.

## Phase 7 · Release validation

- [ ] T019 Execute one live private deployment acceptance run using `specs/002-aws-ec2-provider/quickstart.md` and `tests/release/`, covering protected setup, persistent state after task and host replacement, private IDE, retained files, expired trust, verified delete, task-role isolation and no Terraform ownership drift. Record any unsupported path.

## Dependencies & Execution Order

- Setup T001, T002 and T003 can run in separate worktrees or subagents because their build paths are independent.
- T004 and T005 build on T002. T006 establishes provider configuration before AWS-only setup. US1 needs the image, AWS foundation, host and role boundaries.
- US2 depends on the AWS runtime and AMI build. T011 and T012 can proceed in parallel across the catalog-to-adapter lookup boundary. Establish the live enrollment proof before making platform availability part of the normal workflow.
- US3 uses the validated platform and provider. US4 adds recovery and verifies destruction. Release validation follows all four stories.
- Within a story, start with the narrow test that demonstrates the changed boundary. UI changes use the repository's existing workflow components.

## Implementation Strategy

Build the private infrastructure, controller image and guided setup first. Bring up the one supported EC2 workspace shape and private IDE. Finish recovery and verified cleanup before claiming support. Use a single representative deployment and the existing UTM smoke path for release evidence.
