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

## EC2 resource profiles

- [x] T020 List compatible EC2 types offered in the workspace Availability Zone, derive compute from the selected type, require an administrator-selected AMI for its architecture, and pin the type, AMI, and independently configured EBS disk through launch and reconciliation. Preserve existing EC2 and UTM profiles. Verified locally with the Go and UI suites, SQLite and PostgreSQL upgrade tests, and an exported Chrome fixture.
  - [x] Verify an encrypted management backup, apply PostgreSQL schema 31, and deploy `shiftbox-demo-controller:13` in `us-east-2`.
  - [x] Verify live EC2 profile creation, persistence, editing, enable and disable, and deletion. Confirm AWS-derived compute, independent EBS sizing and validation, and preservation of the existing `t3.medium` profile.

- [x] T021 Validate EC2 instance identity with a challenge-bound STS request, implement single-boot enrollment for new EC2 workspaces, and verify boot count and replay rejection on `shiftbox-demo`.
  - [x] Verify that regional STS returns the built-in EC2 role for the exact disposable instance and rejects a modified signed challenge.
  - [x] Implement the opt-in single-boot flow, durable nonce consumption and key-bound grants. Test concurrent replay, mismatched instance, cancellation, expiry, lost grant response and lost EC2 launch response locally.
  - [x] Apply verified PostgreSQL schema 32, publish candidate AMI `ami-0eea624c1e5701eb3`, and deploy validation controller revision `16`. Verify both ECS services reach `COMPLETED` and the demo login endpoint responds over verified TLS.
  - [x] Register the candidate AMI, launch `single-boot-review`, and verify one kernel boot, one `RunInstances` event and rejection of the exact successful proof replay. Deploy final controller revision `17`, verify its digest and both ECS services reach `COMPLETED`, and remove the temporary registration page. Record results and the interactive IDE verification limitation in `documentation/validation/ec2-single-boot.md`.

- [x] T022 Diagnose and fix IDE WebSocket failures on `shiftbox-demo`, deploy on `feature/mvp`, and verify opening the IDE, terminal commands, editing, saving and reconnecting in `single-boot-review`.

- [x] T024 Remove the single-boot and cold-start test workspaces through ShiftBox, verify EC2 termination and EBS deletion, archive the review template, remove temporary validation images, and preserve the existing workspaces and healthy demo services. Record cleanup in `documentation/validation/ide-websocket.md`.

## Resource profile AMI ownership

- [x] T025 Make AWS resource profiles own the selected platform AMI, compatible EC2 instance type, and independent EBS disk size. AWS templates select a profile without selecting an AMI; future launches use that profile's image and existing workspaces retain their pinned image.
  - [x] T025a [P] Write backend and migration tests first, persist and validate the profile AMI, expose approved image metadata, and resolve AWS template launches through the resource profile. Verified profile validation, template creation without an AMI, launch snapshots and replay, catalog filtering, SQLite schema 32→33 backfill, and the Go backend suites and vet; PostgreSQL migration test is available with the disposable fixture.
  - [x] T025b [P] Write UI tests first, add AMI selection and compatible instance filtering to the profile dialog, and remove independent AMI selection from AWS templates. Verified 25 infrastructure/template/sharing tests and UI typecheck.
  - [x] T025c Verify integration, migrate and deploy `shiftbox-demo` through Terraform, and check the live profile and template flows using disposable records. Schema 33 and controller revision 19 verified; both ECS services completed. See `documentation/validation/resource-profile-ami.md`.

## Dependencies & Execution Order

- [x] T035 Deploy all bundled changes to shiftbox-demo with a verified management backup, schema 34 upgrade, reviewed Terraform rollout, live UI checks, and cleanup. Controller revision 20 and both healthy ECS services verified; user confirmed the deployment. See `documentation/validation/ui-bundle.md`.
- [x] T036 Commit all changes and push to the MVP branch after the user confirmed the live deployment. Bundle commit `2782428` pushed to `origin/feature/mvp`.

- [x] T033 [P] Add an accessible header light/dark toggle with browser-persisted preference and branding in both modes; verified 15 shell unit tests, UI typecheck, and Chrome persistence/keyboard/320px-header checks. Deployment held.
- [x] T034 [P] Replace user-facing Templates/template wording with Projects/project across the UI and associated checks; hold deployment. Verified 75 tests across nine targeted UI suites, the final audit paging/label checks, and UI typecheck. Kept routes, API identifiers, permissions, and raw audit codes intact.

Bundle validation: all 153 UI unit tests, production UI build, generated API checks, seven Chrome browser journeys, full Go suite and vet pass. SQLite/PostgreSQL upgrade and reservation tests pass with a disposable fixture; no demo deployment or data changes were made.

- [x] T032 Support independently unlimited CPU, memory, and disk capacity budgets; blank fields and fresh installation defaults mean Unlimited. Verified the full Go suite, Go vet, UI build, SQLite/PostgreSQL migration and reservation tests, and three Chrome browser checks. Deployment held for bundled UI review.
  - [x] T032a [P] Test and implement persisted unlimited limits, admission enforcement, and SQLite/PostgreSQL schema upgrades while preserving existing budgets. Added schema 34; workspace/database suites and vet pass locally, with PostgreSQL fixture verification included in integration.
  - [x] T032b [P] Test and implement blank-as-Unlimited inputs, fresh setup defaults, and readable budget summaries in the Mantine UI. Verified 29 infrastructure/setup/maintenance tests and UI typecheck.
  - [x] T032c Test and update capacity API validation/contracts, verify integration, and review the final change locally.

- [x] T031 Rename the Infrastructure tab from Resources to Platform and update existing UI checks. Verified all 14 infrastructure unit tests and the production UI build; deployment held for the user's bundled UI changes.

- [x] T030 Remove the `groundtruth-single-boot` review workspace through ShiftBox, verify instance, volume, and network-interface cleanup, and confirm temporary migration resources are absent while the demo remains healthy. EC2 termination and EBS/ENI removal verified; migration tasks inactive, helper image absent, existing workspace preserved, and both demo services completed.

- [x] T028 Launch a new workspace from `groundtruth - large` and verify its pinned image and actual EC2 instance use the verified single-boot AMI. `groundtruth-single-boot` reached Running with a healthy code-server probe; EC2 instance `i-0b589ed3f65ba6b26` uses `ami-0eea624c1e5701eb3`, m6i.xlarge, and encrypted 32 GiB gp3 EBS.

- [x] T027 Assign the verified single-boot AMI to `EC2 Large` in the demo and verify persistence after reload. Selected `ami-0eea624c1e5701eb3`; retained m6i.xlarge, 32 GiB EBS, and enabled state.

- [x] T026 Assign the verified single-boot AMI to the existing `AWS demo · t3.medium` resource profile in the demo and verify persistence after reload. Selected `ami-0eea624c1e5701eb3`; retained t3.medium, 32 GiB EBS, and enabled state.

- Setup T001, T002 and T003 can run in separate worktrees or subagents because their build paths are independent.
- T004 and T005 build on T002. T006 establishes provider configuration before AWS-only setup. US1 needs the image, AWS foundation, host and role boundaries.
- US2 depends on the AWS runtime and AMI build. T011 and T012 can proceed in parallel across the catalog-to-adapter lookup boundary. Establish the live enrollment proof before making platform availability part of the normal workflow.
- US3 uses the validated platform and provider. US4 adds recovery and verifies destruction. Release validation follows all four stories.
- Within a story, start with the narrow test that demonstrates the changed boundary. UI changes use the repository's existing workflow components.

## Implementation Strategy

Build the private infrastructure, controller image and guided setup first. Bring up the one supported EC2 workspace shape and private IDE. Finish recovery and verified cleanup before claiming support. Use a single representative deployment and the existing UTM smoke path for release evidence.
