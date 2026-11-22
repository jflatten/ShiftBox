# Project devcontainer image cache validation

## Demo deployment

- Review URL: [ShiftBox demo](https://shiftbox.demo.shiftbox.internal:8443).
- AWS account `524941137496`, region `us-east-2`, ECS cluster `shiftbox-demo`.
- Controller task `shiftbox-demo-controller:57` is running digest `sha256:da90e68829c8067739cd3610728ff7f0a459f1d5442f79bf9bc3323df8c22842`. Both the controller and PostgreSQL services reached `COMPLETED` with one of one tasks running. The first rollout was revision `56`; revision `57` contains the rebuild publication fix confirmed by the regression test below.
- Verified the encrypted application backup `035eb6ad-8006-4d9a-a805-825166817608` before applying PostgreSQL migration 43. Reviewed the Terraform plan and verified the running digest after rollout. Removed the temporary backup and migration task definitions and helper image tag.
- Signed in to the deployed UI and verified that **Administration → Infrastructure → Cache** showed **Built devcontainer images**, **Saved On**, and **Available**. The guest endpoint shown in the separate registry settings was `https://guest.demo.shiftbox.internal:19444`.
- Published the private amd64 guest AMI `ami-0180079320895ad55`, release `built-images-20261121T013022Z`. The demo boot validation completed and the release appeared in the existing `oci-cache-timing` family. The AMI and root snapshot have the required ShiftBox project and cluster tags. Verified termination of the Packer build instance and both temporary runners.

## Focused verification

The Go package, migration, contract, and release tests passed, along with the focused UI tests, TypeScript, the static browser export, and the embedded browser asset check. The PostgreSQL migration tests used TLS and were rerun for parity with SQLite. The pinned amd64 guest checks in AWS covered both devcontainer variants, exact configuration bytes, Feature policy, retained-home recreation, code-server, and OCI transfers with Docker 29.9.3 and containerd 2.4.2. The temporary proof instances were terminated. After the controller fix the new regression and affected Go suites passed. One Dex startup check failed on the first rerun and passed both in isolation and in the subsequent suite run. See [research evidence](research.md).

## Live functional checks

The two-user scenario and live checks requiring a second user are deferred by request. Same-account cold and warm launches and distinct project scope were verified in the demo.

- **Administrator save and reload:** On the signed-in demo, saved **Off**, reloaded, and verified **Saved Off** with the switch unchecked. Restored **On**, reloaded, and verified **Saved On** and **Available**. Public pull-through retention stayed checked at settings version 20 across both saves.
- **Cold and warm launches:** Both cold launches of `def0626` reported a project image build, reached the code-server IDE, and showed the fixture checkout and lifecycle marker. Both fresh launches of `d098c8f` reached the IDE with no image-build event. The checked-out fixture showed revision B and the lifecycle marker ran, while the baked IDs matched their respective cold launches (Feature `176d2854`, Dockerfile `adb17780`). Both devcontainer configurations were byte-identical between those commits.
- **Project isolation:** A fresh workspace in the separate project used the same repository, ref, and Feature configuration. It reported a new image build, ran the lifecycle hook, and had a different baked ID (`a2113e57`).
- **Changed configuration:** A fresh Feature workspace after the configuration changed at `1ce5aae` reported a project image build and reached a usable IDE.
- **Recreation and data preservation:** A Feature rebuild produced a new local image (`b889fdbf`) and preserved an untracked file in its checkout. A Dockerfile rebuild preserved a file under `/home/vscode` and returned to the browser IDE.
- **Rebuild publication defect found in the demo:** The first fresh launch after the matching Feature rebuild still selected the previous image. A regression test reproduced an authorization failure for an admitted upload after configure completed and before the guest finished setup. The controller fix preserves the admitted scope during IDE verification and remaining hooks and still requires the rebuild to succeed before promoting its image.
- **Matching rebuild retest on revision 57:** A fresh checkout of `a5fa54d` reused the cached image (`5cfe33b8`) with the exact configuration from `1ce5aae`. Its explicit rebuild produced `757a8470`, and the next fresh workspace loaded that exact image in the IDE with no image-build event.
- **Stale rebuild:** Restarted the retained revision-B workspace and rebuilt against its older config. The IDE continued to show the revision-B checkout. A separate fresh workspace still loaded the current shared image `757a8470` with no image-build event.
- **In-flight disable and purge:** Used a disposable Dockerfile with a 512 MiB synthetic layer to create an observable write. Saved **Off** while the controller reported 142 bytes of unwritten staging reservations. The reservation cleared and the workspace reached its IDE. Retained bytes returned from `1,165,483,541` to the original `1,165,387,644` after background cleanup. A fresh Feature workspace under the disabled policy reported a new image build and reached its IDE. The pinned MCR source remained complete in the public cache, and public retention stayed enabled at version 20.
- **Storage pressure and transfer recovery:** Deterministic controller and storage fixtures exercised rejected and interrupted uploads, incomplete candidates, restart recovery, eviction, lease protection, byte accounting, and preservation of the last complete image when a replacement cannot fit. These checks used fixtures so they did not evict unrelated content from the shared demo registry.
- **Re-enable and overlapping misses:** Restored **On** and verified it after reload. Two simultaneous first launches of the same Feature config built distinct images (`523c91d2`, `db89ecc5`). The next fresh workspace reached its IDE with no image-build event and loaded `db89ecc5`. The controller contract separately checks that the first complete candidate wins.

The [registry contract tests](../../tests/contract/built_image_cache_test.go) directly exercise cross-project manifest, config, and layer denial and revocation. The live separate-project check observed an independent build. [Policy tests](../../internal/app/built_image_policy_test.go) exercise authorization fencing and cleanup across restart. [Storage tests](../../internal/imagecache/built_storage_test.go), [controller rebuild tests](../../internal/app/built_image_rebuild_test.go), and the [pinned guest proof](../../tests/release/devcontainer_image_cache_test.go) cover forced storage failures, fallback, and cached recreation.

## Launch timings

These values come from the archived **Performance** views for fresh EC2 workspaces on the same demo resource profile. Each row compares individual runs.

| Fixture | IDE ready, build → cache hit | IDE time saved | Configure, build → cache hit | Configure time saved |
| --- | ---: | ---: | ---: | ---: |
| Image + Feature | 61.083 s → 47.724 s | 13.359 s (22%) | 32.909 s → 18.039 s | 14.870 s |
| Dockerfile | 57.617 s → 50.703 s | 6.914 s (12%) | 29.990 s → 21.981 s | 8.009 s |
| Feature with built cache Off → re-enabled warm, revision 57 | 55.834 s → 42.955 s | 12.879 s (23%) | 30.999 s → 19.006 s | 11.993 s |

The main build event took 16.914–17.583 s in these runs, with a second recorded build event under half a second. Cache hits recorded no build events. Each build run in the table already received 266.65 MiB from public pull-through retention with zero upstream image payload. Cache hits still boot a fresh VM, fetch the built image and checkout, and run lifecycle hooks. The two overlapping post-enable cold launches took 78.314 s and 85.328 s to IDE ready, while their configure steps took 32.306 s and 31.984 s. Use the single-run comparisons for the observed effect on these fixtures.

## Cleanup

- Verified that all 15 disposable workspace instances had terminated in AWS and that zero EBS volumes remained for those workspace IDs.
- Archived the three disposable projects and verified their state after reload. Removed the temporary GitHub fixture branch and verified the ref was gone. Closed every Chrome tab opened for the checks.
- Built-image reuse ended **On** and **Available**, with public retention enabled. The validated guest AMI remains in the `oci-cache-timing` family for demo launches.
