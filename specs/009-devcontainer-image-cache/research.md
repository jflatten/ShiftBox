# Phase 0 research: Project devcontainer image cache

## Capture the image at the Dev Container CLI build boundary

**Decision** — Use the pinned Dev Container CLI’s image resolver boundary to capture the immutable Dockerfile or image-plus-Features output before optional per-workspace UID adaptation. Use the same boundary to supply a verified, locally pulled image on hits. Keep the original config and run the ordinary CLI startup path, including retained-home recreation. Match the exact config bytes read by that invocation. Start with a narrow, versioned extension to the pinned CLI and verify it against the installed release before proceeding with runtime implementation.

**Rationale** — The pinned `openDockerfileDevContainer` merges image metadata and workspace configuration after image resolution, and then applies the per-workspace UID adjustment. CLI startup also checks Features and runs the host initializer upstream of that boundary. The stock `up` output identifies a container, whose image can be per-workspace-derived. Capture the resolved image ID at the image seam and carry a hit through both ordinary startup and retained-home recreation. A bare image with no project build output already follows the public image path. A cache fault can fall back before startup or within the same resolver invocation before container creation.

**Alternatives considered** — A separate prebuild would change the existing rebuild behavior or run the build twice. An override changing the image and dropping Features can duplicate image metadata in the merge and hide declared Features from CLI policy. A running container snapshot includes writable state. BuildKit cache import still runs a build on hits.

Sources — [pinned Dockerfile launch](https://github.com/devcontainers/cli/blob/v0.90.0/src/spec-node/singleContainer.ts#L16-L49), [config and initialization](https://github.com/devcontainers/cli/blob/v0.90.0/src/spec-node/configContainer.ts#L41-L91), [image metadata](https://github.com/devcontainers/cli/blob/v0.90.0/src/spec-node/imageMetadata.ts#L273-L295), [published CLI installation](../../platform/packer/install-toolchain.sh), [retained-home launch](../../internal/guest/home.go), [Docker build secrets](https://docs.docker.com/build/building/secrets/).

## Use project-scoped OCI transfer in the controller registry

**Decision** — Use the existing guest HTTPS endpoint and a reserved private OCI namespace. Plan for the pinned platform’s Docker push and digest pull, with a controller-issued publication attempt and a separate atomic promotion. Implement only the transfer operations used by the supported client. Confirm the protocol and certificate handling with the actual pinned containerd image-store path before committing to the adapter.

**Rationale** — Moby’s containerd-backed push delegates to the containerd pusher, which sends scoped existence checks and a sized monolithic upload after opening an upload location. This fits the existing content store’s pre-write reservations when verified with the pinned client. A platform-specific push avoids multi-platform index and attestation handling. The public mirror is a read-only upstream proxy with a different trust model. Its existing guest authorization can authenticate the connection, while the private route additionally checks project, operation, enabled generation, and precise artifact scope. Private manifest and blob requests do not initiate upstream requests. Read and write transfers need bounded deadlines and revocation cancellation.

**Alternatives considered** — Opening the existing proxy for pushes would couple private authority to public registry resolution. Archive export and load would add a separate transfer format and large staging overhead, and would lose existing layer-level reuse.

Sources — [Moby push path](https://github.com/moby/moby/blob/docker-v29.9.3/daemon/containerd/image_push.go), [containerd pusher](https://github.com/containerd/containerd/blob/v2.4.2/core/remotes/docker/pusher.go), [OCI transfer contract](https://github.com/opencontainers/distribution-spec/blob/main/spec.md), [Docker image push](https://docs.docker.com/reference/cli/docker/image/push/), [current public handler](../../internal/imagecache/cache.go), [guest transport](../../internal/app/guest_server.go), [guest mirror configuration](../../internal/guest/image_cache.go).

## Verify source and grant authority separately from content

**Decision** — Resolve installation, workspace generation, owner, project, platform, operation, and source from controller-owned records. Check current project launch eligibility and GitHub source access, using the existing owner-scoped GitHub App authorization. At promotion, fetch the configured GitHub file at a resolved revision from the project’s current configured repository and ref, and compare its exact contents with the candidate’s build configuration digest. Preserve the guest checkout’s resolution rules for default, branch, tag, and commit refs. Use a short transaction after the remote read to recheck local authority and publish.

**Rationale** — The workspace’s launch snapshot and checkout receipt can be stale. A successful guest build can be used within the project trust boundary, while the controller decides visibility from source and project authority. The GitHub read and controller transaction cannot form one atomic commit. Publication uses the latest bounded authorized observation and checks the local project revision again at promotion. Each launch still compares the exact bytes it is about to use, so a later Git change does not make an image with a different configuration an eligible hit. Record digests rather than storing config text or tokens. A repository change requires source authorization and a candidate from the configured repository. Branch names and commit IDs within that source are not part of the match.

**Alternatives considered** — A guest-reported checkout alone does not establish current remote configuration or project authorization. Keying by Git revision would violate the approved cross-commit and cross-branch reuse policy.

Sources — [project revisions](../../internal/template/store.go), [workspace launch snapshot](../../internal/workspace/launch.go), [guest ref resolution](../../internal/guest/repository.go), [GitHub app authorization](../../internal/identity/github.go), [project launch authorization](../../internal/auth/policy.go), [GitHub commit resolution](https://docs.github.com/en/rest/commits/commits#get-a-commit), [GitHub repository contents](https://docs.github.com/en/rest/repos/contents#get-repository-content).

## Share disk admission and fence background cleanup

**Decision** — Share the existing content store, hard budget, watermarks, and eviction across both cache classes. Give the built-image policy independent enablement and a generation. The settings save immediately fences reads and writes. A background cleanup reconciles old generations in all tracked managed roots, preserving public references and active leases. Establish a bounded default budget on fresh MVP setup so the **On** default can retain images without a separate admin action. Propose 14 GiB, the existing demo’s configured value, with its existing 80% and 70% volume watermarks. Honor any explicitly configured budget.

**Rationale** — Public retention currently starts storage only when its own setting is enabled. Shared retention must use either feature’s enablement if a valid budget and path are available. A full closure must be on disk before publication. Hold a previous matching image during replacement admission, and decline the new retention if both cannot fit. Leases protect content during active reads and during the interval in which the client fetches related OCI objects. Disabled or evicted content uses the normal build path. Publication uploads run outside the wait for the IDE.

**Alternatives considered** — A separate cache volume or storage service would create a second capacity policy and lose shared layer accounting. Applying the built-image disable after waiting for the public settings drain would violate immediate disable. An unbounded or zero default budget would either remove disk protection or leave an enabled fresh organization unable to retain an image.

Sources — [disk admission and leases](../../internal/imagecache/storage.go), [settings lifecycle](../../internal/app/image_cache_settings.go), [existing cache contract](../004-oci-pull-through-cache/spec.md), [demo storage configuration](../004-oci-pull-through-cache/validation-report.md).

## Reuse workspace and admin workflows

**Decision** — Extend the existing launch and **Rebuild container** paths, plus the registry cache settings screen. Use a labeled Mantine Switch, the existing explicit save and feedback patterns, and the existing admin permissions. Display the effective setting and actionable storage availability. Keep purge background-only and leave performance benchmarking outside this work.

**Rationale** — This exposes the organization control where administrators already manage registry storage, and it gives developers an existing action for requesting a refresh. The planning checks distinguish functional behavior, authorization, and durability from the unvalidated speed estimate.

Sources — [registry settings](../../ui/features/admin/registry-cache.tsx), [settings permissions](../../internal/web/image_cache_routes.go), [Mantine Switch](https://mantine.dev/core/switch), [existing launch performance view](../../ui/features/workspaces/launch-performance.tsx).
