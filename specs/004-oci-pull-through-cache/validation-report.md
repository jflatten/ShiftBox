# OCI image cache validation

## Foundation

- Baseline is MVP commit `798d5299d5798da664b68269e9963087a27a5137`. The worktree is on `codex/oci-image-cache-implementation`.
- The checked Go toolchain is 1.27.4, with Distribution v3.1.2 at `3220848f15d9279c66a41aa0a257469e6aede1e9`.
- Cache and guest tests failed before production code and then passed. The guest test exercises real SQLite enrollment, mTLS, a stream longer than the poll deadline, active revocation and denial of the revoked leaf.
- A disposable Docker 29.9.3 daemon used the containerd snapshotter and embedded BuildKit v0.34.2 with the `docker` driver. With a terminal `_default` TLS mirror and a disposable mTLS certificate, an unchanged Dockerfile `FROM mcr.microsoft.com/hello-world:latest` built for linux/amd64 and Docker pulled the original tag. Both resolved `sha256:92c7f9c92844bbbb5d0a101b22f7c2a7949e40f8ea90c8b3bc396879d95e899a`. Mirror logs recorded BuildKit HEAD/GET manifest and GET blob requests and Docker HEAD, all with `ns=mcr.microsoft.com`. The Dockerfile SHA-256 was `f70fde7e42ab650ef713a9040e2b11d4d16c3292f020632dec00caa6883816cb` before and after. The proof cache had a 64 MiB budget. The host volume was at 84% usage, so the handler streamed content without retaining files under the 80% high watermark.
- This local proof exercises the pinned Docker engine and the ShiftBox cache handler over verified mTLS. It does not measure IDE startup or replace the fresh workspace and UI checks on shiftbox-demo.
- Read-only AWS preflight matched account 524941137496 under the default profile in us-east-2. Both ECS services showed one running task and completed rollout state. No shared service was replaced during foundation testing.

## Original-reference Dev Container proof

- Dev Container CLI 0.90.0 ran in a disposable Docker 29.9.3 daemon with the containerd image store. The prebuilt fixture used `docker.io/library/busybox:1.37` and resolved `sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec`. The Dockerfile fixture used `mcr.microsoft.com/azurelinux/base/core:3.0` and resolved its upstream base to `sha256:3d16d8799f71a78f5bddb3b7cda4a57df1f40ecfa58bac89933010596aa86880`. Both CLI launches succeeded and hit the protected cache with original references. Fixture files were mounted read-only and their hashes matched before and after.
- Focused workspace component tests passed, including the exact outage warning, its independent Running state and separate post-recovery freshness evidence. A signed-in outage/recovery journey on shiftbox-demo remains pending.

## Demo storage

- Terraform attached encrypted 100 GiB gp3 volume `vol-0c49f7a3560108b61` at `/dev/sdg` on `i-06a0dfd4f0a5f1a07`. The plan created only the dedicated cache volume and its attachment. Controller revision 25 and PostgreSQL revision 1 stayed healthy and had no cache bind.
- After approval for the 20 GiB replacement and deletion after detachment, CloudWatch reported zero write operations on the 100 GiB volume. A fresh Terraform plan against the live deployment state replaced only the cache attachment and volume. Terraform detached the 100 GiB disk normally, created and attached encrypted 20 GiB gp3 volume `vol-06b675eca82009f4f` at `/dev/sdg` on the same host, then deleted the detached disk. AWS confirmed the old volume ID was gone and both ECS services stayed on their existing revisions, each with one running task. The active Terraform inputs record 20 GiB and mount readiness false. The new volume has not been formatted, mounted or bound to the controller.
- On resumption, AWS confirmed the cache disk still needed a host mount. The cache volume was attached, distinct from both the root and shared data volumes. The host had no Systems Manager registration, enabled EC2 serial console, EC2 Instance Connect endpoint, or inbound SSH rule. The installed AWS ECS-optimized Amazon Linux 2023 amd64 AMI `ami-0a8975a01978e5b97` matched the regional recommended AMI. The existing boot template already mounts the shared data disk before ECS. A sealed-host boot change or a switch to task-managed storage requires separate review. The live schema and service revisions were left in place.

## Resumed release checks

- The focused UI run passed 37 tests, followed by TypeScript, the Next.js static build and the embedded browser asset test. A local Chrome fixture showed the **Cache** tab, restored the selected tab after reload and displayed both Complete and Partial image rows. Next.js reported no session or configuration errors. This is component and local browser evidence for UI-01–UI-13. The two signed-in demo journeys, pressure through the live controller, and the live outage/recovery journey remain pending.
- The Go integration, contract and cache/app race checks passed. Focused added checks confirmed an OCI 429 forwards Retry-After without using stale payloads, TLS verification failures reject stale fallback, workspace detail separates startup and verified digests while Running, and budget reduction protects leased and unrelated files. The full Linux amd64 controller image passed its production entry-point smoke check, including packaged notices for 95 linked modules. The Linux amd64 guest agent compiled locally and still requires the normal signed guest release before live guest validation.
- The live mount, management backup and schema upgrade, new controller rollout, and fresh-workspace direct/cold/warm comparison remain pending. No image was published and no new demo services, workspaces, or test profiles were created during the resumed checks.

## Approved demo rollout

- The original controller verified encrypted backup `ca7026b5-1b21-4f31-a04a-c78cbbc45cf8`. The scoped backup task exited zero. The migration later matched the archive checksum to the completed backup record and checked the retained guest release gate.
- Terraform quiesced the controller and then PostgreSQL with two count-only plans. The fresh host plan was one in-place user-data update. After the restart, AWS reported the same EC2 instance `i-06a0dfd4f0a5f1a07`, AMI `ami-0a8975a01978e5b97`, root and shared volume IDs, and the same dedicated cache volume. New-boot console output verified XFS and measured `21,407,727,616` bytes of filesystem capacity on the expected volume. The boot hook explicitly disabled formatting on the existing shared volume. PostgreSQL startup reported an existing data directory and accepted connections on its original task revision.
- The temporary migration task upgraded PostgreSQL from application schema 35 to 37 and verified the result. The subsequent Terraform plan changed the controller task and service plus their normal setup task and setup policy dependencies. Both ECS services reported `COMPLETED` with one running task each. The controller was `shiftbox-demo-controller:26` at the verified digest `sha256:99ce4790a9b1061608651a225ea1a23bafaf97a0048ac3484ea170871e43c924`. PostgreSQL stayed at revision 1. The canonical local Terraform inputs record both cache bootstrap and controller bind enabled.
- A signed-in administrator opened the live **Cache** tab, enabled retention at `/var/lib/shiftbox-registry-cache/cache`, and saved a 14 GiB budget with 70% low and 80% high watermarks. After reload the tab selection, settings and Ready status persisted at effective version 2. The UI volume-capacity reading matched the host console byte-for-byte. The Cached images section displayed its empty state. [Live Cache tab](https://shiftbox.demo.shiftbox.internal:8443/infrastructure/?section=registry-cache)
- The two temporary task definitions were removed through their isolated Terraform state, and the two disposable helper image tags were deleted. A fresh plan against the canonical state found no changes, and all nine mocked Terraform tests passed. The encrypted backup and deployed controller image remain in place.
- Live member-policy, pressure, path-change, stale-image recovery and direct/cold/warm fresh-workspace measurements remain pending. The shipped controller and local guest builds passed, but the demo's approved guest image still carries the prior guest agent. A cache-capable approved guest image or signed guest release is required for live pull validation. No disposable workspaces or profiles were created during the rollout.

## Scoped guest validation preparation

- In the signed-in Cache tab, an attempt to use the protected controller installation directory failed activation and left the dedicated cache active at effective version 2. Resaving the original dedicated path succeeded. The cache returned to Ready at effective version 4 after reload with its 14 GiB budget and original watermarks. This is the live invalid-path and recovery check. No payloads were retained during it.
- Built a fresh Linux amd64 guest executable with the demo controller's existing guest release identifier. Validated the candidate manifest with the production parser and verified that its SHA-256 matches the new executable. The old package verified against the public key pinned to the current demo guest AMI and carries different executable bytes. The staged candidate is unsigned. Its archive contains only the candidate executable and manifest.
- The current maintenance route selects retained workspaces across the installation. Both approved AMI admission slots are occupied, and AWS has neither the previous private Packer builder security group nor its build key pair. A supported disposable-guest assignment path and the corresponding approved builder or publisher action remain required. No guest release, trust key, AMI assignment, workspace, or fleet maintenance operation was changed. No direct, cold, or warm results were recorded.
- Reviewed the [private builder and disposable guest plan](disposable-guest-validation-plan.md). Terraform validated and produced a read-only preview with 14 isolated builder creates, zero updates and zero deletes. AWS confirmed the subnet has a NAT route and automatic public IP assignment disabled, and the pinned Canonical source image is available. No builder resources or AMI were created. Live workspace validation still requires approval support for additional AMIs.

## AMI family rollout

- Resource profiles can follow a named family. HTTP contract tests verified that a validating image cannot win selection, an approved patch affects the next launch, and earlier workspace bindings and idempotent retries keep their exact image. Tests cover incompatible releases, architecture and disk constraints, and reject retirement of a retained workspace's image. Existing pinned profiles retain their behavior.
- Focused Go and contract suites, Go vet, 54 browser component tests, the Next.js production export and the embedded browser asset test passed. Component tests verified family profile save and publication progress across reload.
- ShiftBox verified encrypted management backup `eee3cd99-fe70-4d0b-a64a-332fbd28392e`. Terraform paused only the controller. The migration helper checked the retained guest gate and backup checksum, then migrated PostgreSQL from schema 37 to 38. PostgreSQL remained at revision 1. The controller and setup task definitions were updated and the controller reached `COMPLETED` on revision `shiftbox-demo-controller:27` at `sha256:97fe34cb107812435d5b196a9542b014384083c57e91bac61dddbcebe072dda4`. The two temporary helper definitions and their image tag were removed. Terraform reported no drift.
- A signed-in administrator opened the live [Platform page](https://shiftbox.demo.shiftbox.internal:8443/infrastructure/) and saw the AMI family section, manifest publication form and resource profiles. Live image validation and fresh-workspace tests are tracked in the next step.
- The first live import exposed an incorrect column in the canary's isolated SQLite insert. AWS verified the unsuccessful canary terminated and its root volume was removed. Added a regression that reproduces the failure against the real schema, then confirms that the corrected canary can issue its single-boot enrollment challenge. Shipped the fix in controller revision `shiftbox-demo-controller:28` at `sha256:329ea76189843c45f17be914c64dfda446e98822c6f4a6efc428a5876f7ff556`; both services reached `COMPLETED` on schema 38.
- The corrected live import succeeded. The baseline image appeared as Validated and its import canary terminated with no root volume remaining. The Admin UI rejected a malformed manifest, and the family resource profile persisted after reload. Both existing pinned profiles still displayed the original single-boot release.
- The first actual workspace used baseline AMI `ami-0deac6c82c8d8b8e3`, confirmed independently in AWS and its persisted workspace configuration. The test used the unchanged fixture manifest from Git commit `dcbb326d75969861ca689b15d3f67ae37818b588` and `mcr.microsoft.com/devcontainers/base:ubuntu-24.04`. AMD64 resolution before the comparison was `sha256:3612b800eaae8513e7dca54f21d2e4eda48eda57426f3746a78677f342ac7ee9`.
- The direct run used shared egress with retention disabled. IDE readiness was observed about 4 minutes 23 seconds after launch submission, and code-server opened successfully through the proxy. Direct guest payload bytes were unmeasured and the controller retained no image references. Restored retention on the dedicated disk with the original 14 GiB budget and 70/80 watermarks before the cold run.
- Published a newer sealed AMI, `ami-006ec36e54693ec97`, to the same family. Both temporary images used the same guest executable checksum. ShiftBox validated the newer AMI without a profile edit. The next workspace's saved configuration showed the newer release. AWS independently confirmed that the original workspace kept its baseline AMI and that the new workspace used the newer AMI, each on a distinct private instance with its own root volume and no instance profile.
- The cache had no retained references and its counters were zero immediately before the cold run. The unchanged fixture tag and its AMD64 manifest were Complete after the cold run and stayed Complete through the warm run. Public registry resolution returned the same AMD64 digest before and after the comparison. AWS confirmed three distinct private VMs and root volumes. The sealed images preload only the separate repair image.

| Run | Observed IDE readiness | Controller-served payload | Upstream payload | Upstream metadata |
| --- | ---: | ---: | ---: | ---: |
| Direct | about 4m 23s | 0 B | Guest transfer unmeasured | Unmeasured |
| Cold | about 2m 53s | 0 B | 279,613,458 B | 4,179 B |
| Warm | about 4m 27s | 279,613,458 B | 0 B | 1,609 B |

- Warm numbers are deltas from the cold run's counters. Neither cached run streamed image payload without retention. The launch times came from periodic browser checks. The warm run overlapped cleanup of the earlier test VMs. The samples establish image payload reuse.
- The live catalog refused to retire the baseline image while a workspace was still bound to it. After deletion, both disposable images were retired, the test project was archived, and the referenced test profile was disabled. Reload confirmed that the original profiles stayed enabled on their original release and that the Cache page stayed Ready. The retained fixture is eligible for normal cache eviction.
- AWS confirmed termination of all three test workspaces and their canaries, and confirmed that the three test root volumes, two disposable AMIs and both exclusive snapshots were gone. Terraform removed the private builder and found no drift in the demo configuration after the corrected rollout. Both originally approved AMIs stayed available and both ECS services stayed healthy. The encrypted management backup remains retained.
- Remaining end-to-end gates include the live member-policy, disk-pressure, valid path move and upstream outage/recovery journeys.

## Cold and warm launch timing follow-up

The controller event journal recorded 143.2 seconds from cold launch admission to completion and 203.7 seconds for the warm launch, a difference of 60.5 seconds. The earlier table records periodic browser observations of IDE readiness. [Cold launch record](https://shiftbox.demo.shiftbox.internal:8443/api/v1/operations/94ac29d3-e02b-4147-a7ea-3cf44b891c04) · [Warm launch record](https://shiftbox.demo.shiftbox.internal:8443/api/v1/operations/1c727ef1-6377-4fca-962f-dddfdeb7584b)

| Controller step | Cold | Warm | Warm minus cold |
| --- | ---: | ---: | ---: |
| Provision workspace | 5.9 s | 5.7 s | −0.2 s |
| Enroll guest | 34.3 s | 36.4 s | +2.1 s |
| Configure development container | 100.0 s | 158.4 s | +58.4 s |
| Verify browser IDE | 0.49 s | 0.48 s | −0.01 s |

The configure step includes repository preparation, image transfer to the guest, Docker unpacking and startup, and required hooks. A fresh guest still received all 279,613,458 payload bytes from the warm controller cache. The finer timing records were written to the guests' local systemd journals. Neither the retained EC2 console output nor the controller logs contained those records, and the guest volumes were removed during test cleanup. The retained evidence localizes the extra time to configuration and leaves its subphase undetermined. [Guest timing documentation](../../platform/packer/README.md)

## Instrumented cold and warm rerun

Both runs used fresh private `t3.medium` instances with distinct 32 GiB root volumes, the same instrumented guest AMI, and the original pinned fixture. The cold run started with an empty cache directory on the same dedicated cache volume. The warm run reused all 279,613,458 payload bytes from the controller and fetched 1,609 bytes of upstream metadata. The cold run fetched the same payload from upstream and fetched 4,179 bytes of upstream metadata. Both guests reached a healthy IDE, and their sanitized timing records were saved before guest cleanup. [Timestamped guest, controller and cache evidence](guest-timing-rerun.json)

| Stage | Cold | Warm | Warm minus cold |
| --- | ---: | ---: | ---: |
| Launch admission to completion | 119.953 s | 219.683 s | +99.730 s |
| Configure the development container | 70.014 s | 169.007 s | +98.993 s |
| Guest browser IDE preparation and startup | 26.677 s | 107.742 s | +81.065 s |
| ↳ Install bundled code-server with `docker cp` | 22.802 s | 105.889 s | +83.087 s |
| ↳ Start code-server and wait for its socket | 3.875 s | 1.853 s | −2.022 s |
| Read image configuration | 6.779 s | 16.285 s | +9.506 s |
| Initial `devcontainer up` | 12.804 s | 22.469 s | +9.665 s |
| Guest Docker prefetch, including transfer and unpack | 11.021 s | 10.242 s | −0.779 s |
| Controller IDE verification | 0.492 s | 0.458 s | −0.033 s |

The guest IDE step accounted for about 81% of the rerun's extra launch time. Separate journal records put 83.087 seconds of extra time in `installCodeServer`, which copies the bundled distribution from the guest filesystem into the fresh container and writes the local installation receipt. Code-server startup recovered 2.022 seconds. The initial `devcontainer up` also took 9.665 seconds longer even though its observed Docker build was 0.107 seconds faster. The original pair's per-guest phase logs were unavailable at the time of investigation. [Guest IDE implementation](../../internal/guest/browser_ide.go)

CloudWatch recorded the same 12 successful, byte-for-byte blob responses through the controller in both runs. The approximate request window fell from 3.018 seconds cold to 2.691 seconds warm. The largest layer fell from 2.788 to 2.652 seconds. These samples compare the controller's upstream-fed and cache-served paths. The earlier direct run recorded overall readiness only, so the direct upstream-to-guest path has no comparable transfer series.

The fresh guest root disks each read about 1.23 GB and wrote about 2.70 GB during setup and immediate writeback. Average read latency rose from 6.031 ms cold to 14.448 ms warm, while write latency stayed near 2.06 ms. No instance-level EBS IOPS or throughput limit breach appeared in the sampled minutes. Guest disk read latency is the strongest observed correlate of the copy delay. First access to snapshot-backed EBS blocks can increase read latency during initialization. The aggregate metrics do not identify the cause of individual reads or resolve sub-minute CPU behavior. [Timestamped evidence](guest-timing-rerun.json) · [AWS EBS performance guidance](https://docs.aws.amazon.com/ebs/latest/userguide/ebs-performance.html)

Restored the original dedicated cache path with a 14 GiB budget and 70/80% watermarks. Verified Ready with zero retired bytes after reclaiming the scoped fixture directory. ShiftBox deleted both disposable workspaces, archived their project, disabled their profile, and retired their AMI release. AWS confirmed both EC2 instances terminated, both root volumes were removed, the AMI and its encrypted snapshot were removed, and the two ECS services remained healthy. Terraform removed the temporary directory helper definitions. Both guest traces were saved before workspace deletion.

### T023 comparison across both pairs

Archived controller logs record 12 successful layer GETs totaling 279,613,458 bytes in each cached run. The first-request times below use EC2's launch timestamp as their common origin. Disk latency is weighted by completed reads over the launch minute and the following five UTC minutes. All five guests ran in the same Availability Zone. Each cold/warm pair shared an AMI. The direct baseline used a different AMI with the same guest executable. [CloudTrail and CloudWatch comparison](guest-timing-rerun.json)

| Run | EC2 launch to first controller layer GET | Controller layer response window | Mean guest root read |
| --- | ---: | ---: | ---: |
| Original direct | Bypassed controller | Unmeasured | 11.92 ms |
| Original cold | 45.1 s | 5.69 s | 7.37 ms |
| Original warm | 62.5 s | 2.90 s | 11.02 ms |
| Rerun cold | 57.8 s | 3.02 s | 6.03 ms |
| Rerun warm | 67.9 s | 2.69 s | 14.45 ms |

Both warm guests arrived later at their first layer request and received the layers faster. The direct guest also had comparatively slow disk reads. Neither warm volume flagged an IOPS limit, throughput limit or stalled I/O. The faster cold rerun volume flagged its throughput limit in two samples. A throughput limit on the warm root volume is therefore a poor explanation. No direct-to-upstream transfer time was captured. [AWS metric definitions](https://docs.aws.amazon.com/ebs/latest/userguide/using_cloudwatch_ebs.html)

The rerun guest recorded 9.51 additional seconds in the configuration reader before the pull, 9.67 in the first devcontainer startup, and 83.09 in the IDE installer. The installer reads package metadata, asks Docker to copy the bundled tree into the container, then synchronously commits the receipt file and its parent directory. The trace covers that entire operation. Variable latency on first reads of snapshot blocks, waits while Docker creates destination files, and waits for filesystem writeback can each extend it. Both roots read about 1.23 GB and wrote about 2.70 GB, and both guests consumed about 1.03 CPU credits during launch. Per-process I/O waits, file sync duration and snapshot initialization progress were unmeasured. Both pairs ran cold first and warm second on separate guests. This leaves cache state, launch order and the individual guest confounded for whole-launch comparisons. [Installer](../../internal/guest/browser_ide.go) · [Receipt persistence](../../internal/guest/agent.go) · [EBS volume initialization](https://docs.aws.amazon.com/ebs/latest/userguide/initalize-volume.html)

### T023 warm-first m6i.xlarge rerun

The two timed workspaces used fresh private `m6i.xlarge` instances in `us-east-2a` with separate encrypted 32 GiB gp3 roots at 3,000 IOPS and 125 MiB/s from the same rebuilt AMI snapshot. The build preserved the base AMI, toolchain recipe, code-server recipe, and guest executable checksum from the instrumented t3.medium pair. Both used the same project revision and AMD64 OCI digest. A separate untimed workspace prefilled the warm directory and finished before the warm launch. The cold launch followed on a second verified empty directory on the same cache disk. [Raw guest timings, controller events, cache counters and EBS metrics](guest-timing-m6i.json)

| Stage | Warm first | Cold second | Warm minus cold |
| --- | ---: | ---: | ---: |
| Launch admission to completion | 193.51 s | 144.38 s | +49.13 s |
| Configure development container | 152.02 s | 98.02 s | +54.01 s |
| Read image configuration | 15.93 s | 14.96 s | +0.97 s |
| Controller layer response window | 0.64 s | 2.19 s | −1.55 s |
| Guest pull including unpack | 6.15 s | 7.29 s | −1.14 s |
| First `devcontainer up` | 18.42 s | 29.00 s | −10.58 s |
| Install bundled browser IDE | 99.00 s | 33.94 s | +65.06 s |
| Start IDE and wait for the socket | 1.49 s | 1.52 s | −0.03 s |
| Mean guest root read | 13.26 ms | 8.21 ms | +5.05 ms |
| Mean guest root write | 2.05 ms | 1.95 ms | +0.10 ms |

The warm run served 279,613,458 layer bytes from cache with zero upstream layer bytes. The cold run fetched the same layer payload from upstream. Warm and cold fetched 1,609 and 4,179 bytes of upstream metadata, respectively. The warm guest reached the first request 4.10 seconds earlier, reversing the earlier pre-pull pattern. Its first Docker build was 0.19 seconds faster, so nearly all of the 10.58-second advantage in initial devcontainer startup was outside the timed Docker build. The IDE install accounted for more than the net launch gap because earlier phases favored the warm run.

The warm launch took 26.17 seconds less than the prior instrumented warm launch, and the cold launch took 24.42 seconds more than the prior instrumented cold launch. The warm launch penalty fell from 99.73 to 49.13 seconds. Changing instance type and order together leaves their individual effects undetermined. The seed workspace fetched its layers from upstream and spent 116.08 seconds in IDE installation. A slow IDE install therefore also occurred on this instance type with a cold cache.

Both measured roots read about 1.22 GB and wrote about 2.70 GB. AWS reported both roots still initializing during IDE installation. The warm root recorded a one-minute mean read of 23.85 ms during that install. The warm volume flagged a throughput breach in one minute and the cold volume in two. Neither reached an instance-level EBS limit. The persistence of the IDE delay in the warm-first run shows that launching second and CPU burst credits are unnecessary for the delay to occur. Read latency again tracked the delay. The volume metrics cover the whole VM and the install timer still spans the local Docker copy and final receipt sync. Neither measurement attributes the wait to specific snapshot blocks, Docker filesystem work, or the sync. [EBS initialization and its reporting interval](https://docs.aws.amazon.com/ebs/latest/userguide/initalize-volume.html)

The enabled AWS demo and EC2 Large profiles both showed `m6i.xlarge`, 4 vCPUs, 16,384 MiB and 32 GiB after reload. The AWS demo profile was briefly disabled by a cleanup edit and was re-enabled. The timing profile kept `m6i.xlarge` and returned to Disabled, and the fixture project returned to Archived. The original cache path, budget and watermarks were restored and its Ready status showed zero retired bytes after cleanup. AWS verified that the three test guests and validation canary terminated and their root volumes were removed. The disposable AMI and exclusive snapshot were removed. Terraform removed the private builder and both directory helper definitions. The two original AMIs remained available and both ECS services reported Completed.

### T026 read-only code-server bundle

Packer built `shiftbox-platform-2026111805-5f28a90e-b87c-4dbc-92b4-702406286248` (`ami-02d5a6f7ec323dc39`, `us-east-2`). The name uses the build-start hour in Pacific time. ShiftBox validated the private, encrypted AMI in the `oci-cache-timing` family. The rerun used the same base AMI, pinned toolchain and IDE bundle, fixture revision and OCI digest as the previous M6i pair. The guests used the new agent and separate fresh 32 GiB gp3 roots from the new AMI snapshot on `m6i.xlarge` instances in `us-east-2a`. An unmeasured launch populated the warm directory. The measured warm guest finished before the cold guest started against a second empty directory. [Bind-mount traces and CloudWatch measurements](guest-timing-bind.json) · [Copy-based M6i baseline](guest-timing-m6i.json)

| Stage | Warm copy | Warm bind | Cold copy | Cold bind |
| --- | ---: | ---: | ---: | ---: |
| Launch admission to completion | 193.51 s | 115.73 s | 144.38 s | 107.16 s |
| Configure development container | 152.02 s | 70.01 s | 98.02 s | 62.02 s |
| Controller serving all OCI layers | 0.64 s | 0.70 s | 2.19 s | 2.38 s |
| Guest image pull including unpack | 6.15 s | 6.19 s | 7.29 s | 6.81 s |
| First `devcontainer up` | 18.42 s | 27.86 s | 29.00 s | 27.95 s |
| IDE bundle preparation | 99.00 s copy | 0.017 s mount check | 33.94 s copy | 0.016 s mount check |
| Start code-server and wait for its socket | 1.49 s | 13.25 s | 1.52 s | 2.94 s |
| Mean guest root read latency | 13.26 ms | 11.62 ms | 8.21 ms | 10.23 ms |
| Guest root writes | 2.70 GB | 1.95 GB | 2.70 GB | 1.96 GB |

Both bind-mount traces verified the expected read-only source before starting code-server inside the Dev Container. Neither recorded a copy or receipt write. The controller verified both browser IDEs and Chrome opened the cold guest's workbench. The warm guest received all 279,613,458 layer bytes from cache and the cold guest fetched the same payload from upstream. Upstream metadata was 1,609 bytes warm and 4,179 bytes cold.

Warm improved by 77.78 seconds (40.2%) and cold by 37.21 seconds (25.8%). The warm launch penalty narrows from 49.13 to 8.57 seconds. Code-server's socket-ready step took 10.32 seconds longer warm than cold in the bind pair, accounting for the remaining net gap after offsets in other launch stages. AWS still reported initialization in progress on both roots. The timing covers process startup through socket readiness, and the EBS series aggregates all guest I/O during launch and immediate writeback. These samples do not resolve which startup reads or other waits caused the residual delay.

The Go guest, release and EC2 suites passed with the race detector, targeted `go vet` passed, and the Packer recipe and its release checks passed. Existing pre-mount containers retain the copy path until a normal rebuild supplies the read-only mount. [Guest implementation](../../internal/guest/browser_ide.go) · [Development container launch](../../internal/guest/devcontainer.go)

AWS confirmed the three benchmark guests and the validation canary terminated and their volumes were removed. Terraform removed the temporary builder and both cache-directory task definitions. The Cache tab returned to Ready on the original path with zero retired bytes. The test profile returned to Disabled and its project to Archived. The named AMI and its encrypted snapshot remain available, and both ECS services report Completed.

### T027 combined warm launch

Controller revision 29 and the validated guest AMI `ami-06f90d2cfd6790183` served one fresh `m6i.xlarge` workspace using the existing fixture and cache. CloudTrail recorded a 300 MiB/s initialization rate on the encrypted 32 GiB gp3 root. The active cache was already populated. All 12 layer responses succeeded, serving 279,613,458 bytes from cache with zero upstream layer bytes and 1,609 upstream metadata bytes. No cache path change, seed launch or cold benchmark was needed.

Controller admission to verified workbench HTML and WebSocket took **58.04 seconds**. The previous warm run reached the same milestone in **115.71 seconds**. Enrollment finished 31.81 seconds after admission, guest configuration took 24.01 seconds on the controller, and IDE verification took 0.41 seconds. On the guest, image preparation took 6.46 seconds, the first Dev Container startup took 6.69 seconds with no observed Docker build, and the IDE socket was ready in 1.47 seconds. The fixture's required hook took 5.45 seconds and the guest verified the read-only bundle mount. Chrome opened the authenticated workbench and displayed the project files in Explorer. The exact first usable browser render timestamp was unavailable. One combined sample measures the change in total latency. Individual contributions and repeatability remain unmeasured. [Saved guest journal timings, controller events and launch inputs](guest-timing-combined-warm.json)

The guest, EC2 provider and release race tests, targeted Go vet, UI export and embedded asset test passed. ECS reported completed controller and PostgreSQL deployments, Terraform reported no drift, and the temporary private builder was removed.

After the operator approved deletion, AWS confirmed termination of both the timed guest and the validation canary, removal of the timed guest's root volume, and continued availability of the published AMI and encrypted snapshot.
