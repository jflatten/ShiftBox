# Implementation Plan: Project devcontainer image cache

**Branch** — `feature/mvp`

**Date** — 2026-11-20

**Spec** — [Project devcontainer image cache](spec.md)

## Summary

Reuse finished devcontainer images in the existing controller registry. Capture the immutable output of the existing guest build and allow later authorized launches in the same project to pull it by digest. A single project and platform slot points to the current complete image. The controller checks the configured Git origin and ref at publication. The organization setting defaults to On, and Off fences access immediately and purges in the background.

## Technical Context

| Area | Plan |
| --- | --- |
| Controller and guest | Go 1.27.4. Reuse the guest HTTPS listener, enrollment, project authorization, GitHub App, operation dispatch, and in-process registry. |
| Guest tooling | Docker 29.9.3, containerd 2.4.2, Buildx 0.39.0, and Dev Container CLI 0.90.0 from the Packer toolchain. Verify the image resolver seam and native single-platform OCI transfer against those pins first. |
| Browser | Existing static Next.js/TypeScript application, Mantine Switch and status chips, Carbon and ShiftBox styling. Keep the existing workspace rebuild workflow. |
| Storage | SQLite and PostgreSQL for policy, slot pointers, and recovery state. Image contents go through the existing cache store and shared volume limits. Proposed fresh-organization budget is 14 GiB with the existing disk watermarks. |
| Target | Current UTM and EC2 workspace paths and their supported Linux guest platforms. Use `shiftbox-demo` for functional integration checks after implementation. |
| Launch scope | Fresh workspace launch and existing **Rebuild container**. Accept both Dockerfile and image-plus-Features build outputs. Keep public images on their existing upstream path. |
| Performance | Skip image build on a hit and keep upload off the path to IDE readiness. Measure elapsed time from the controller launch signal to a usable browser IDE when reporting performance. The performance hypothesis benchmark and threshold are outside this plan. |
| Functional verification | Test-first Go tests with intent comments, pinned-runtime contract checks, settings UI coverage, and functional demo checks described in the [quickstart](quickstart.md). |

## Constitution Check

| Principle | Design check |
| --- | --- |
| KISS | Reuse the embedded registry, storage coordinator, existing guest and rebuild paths, GitHub authorization, and admin settings. Justify the small pinned CLI extension with evidence that it preserves existing startup behavior. |
| Ownership and trust | The controller authorizes and promotes project output. Trust within a project is an explicit grant to execute that project’s published image. Treat guest identity and reports as scoped claims, keep controller credentials outside the guest, and enforce isolation before serving any registry object. |
| Preserve work | Keep the ordinary checkout, retained home and application mounts, and use the immutable image output. Rebuild and publication failure paths retain the previous usable image where it still matches. |
| Test first | Exercise partial uploads, concurrent writers, disk pressure, disable, authorization, and runtime parity. Confirm functional behavior without testing the projected speedup. |
| Visible workflows | Reuse the existing launch and rebuild steppers. In Infrastructure → Cache, show the persisted setting with permission checks, validation, and save/reload feedback. Purge is a background operation. |

The Phase 1 design satisfies these gates. [Phase 0 research](research.md) records the pinned-runtime proofs still required during implementation.

## Project Structure

### Documentation (this feature)

```text
specs/009-devcontainer-image-cache/
├── spec.md
├── checklists/requirements.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
└── contracts/built-image-cache.md
```

### Source Code (repository root)

| Area | Intended changes |
| --- | --- |
| `internal/guest/` and `platform/packer/` | Locate the exact config used by the CLI, capture the resolved build result, pull an authorized digest on hits, and enqueue eligible output for bounded background upload. Package and verify the pinned CLI extension. Preserve the existing home, lifecycle and IDE paths. |
| `internal/app/` and `internal/imagecache/` | Extend the existing guest listener and shared storage coordinator. Fence each transfer by generation and project scope. Reconcile publication, eviction, and background cleanup. |
| `internal/identity/`, `internal/template/` and `internal/workspace/` | Read the authoritative project origin and current config through GitHub, preserve existing ref rules, and derive launch and rebuild authority from controller records. |
| `internal/web/` and `ui/features/admin/` | Add the organization setting to registry cache settings, including independent versioning, permissions, audit and save/reload. Keep existing Mantine controls and feedback patterns. |
| `migrations/{postgres,sqlite}/` and `specs/001-shiftbox-mvp/contracts/` | Use the next available equivalent schema revision in both dialects. Extend the canonical management API and regenerate the browser types during implementation. |

**Structure decision** — Keep the feature in the existing controller and guest. The private registry namespace owns transfer authorization. The shared storage coordinator remains the only admission authority.

## Implementation Sequence

1. **Prove the runtime seams.** Write focused tests first. On the pinned stack, show how to capture image identity from an unchanged launch and rebuild and how a digest hit preserves metadata, Feature policy, mount, lifecycle, and IDE behavior. Prove the exact OCI push/pull sequence over rotating guest mTLS with a complete, single-platform image. Make the release gate depend on these functional proofs.
2. **Implement scoped storage and authority.** Add the durable enable policy, project slot, source checks, attempt scope, and bounded storage operations. Verify data and leases before disclosure. Stage the candidate, resolve the current configured origin outside the write transaction, and recheck local authority and policy generation when promoting.
3. **Integrate fresh launches and existing rebuilds.** Preflight lookup and pull before entering the workspace lifecycle, or recover inside the same CLI resolution before spawning. Continue the normal network-policy build on a miss. Pass the chosen image through retained-home recreation. Capture build output before runtime customization and queue publication outside the wait for the IDE.
4. **Complete admin controls and functional verification.** Add the setting under Administration → Infrastructure → Cache, including keyboard access and the disable consequence. Verify independent public and built policy, concurrent publication and cleanup, and authorized launch and rebuild on the demo. Remove disposable resources after checking.

## Complexity Tracking

No constitution exception is planned. The pinned CLI extension addresses the missing image-only startup and build-result hooks. The private OCI namespace and generation guard enforce project access and immediate disable with the existing storage budget.
