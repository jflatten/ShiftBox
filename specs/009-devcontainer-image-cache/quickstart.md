# Functional validation: Project devcontainer image cache

## Prerequisites

Use `shiftbox-demo` with the supported controller and guest toolchain, an administrator, two authorized users in one disposable project, and a second project referencing the same fixture repository. Give the fixture a small image-plus-Features config and a Dockerfile variant. Use a runtime hook marker and a non-sensitive file in each checkout to identify the normal lifecycle. Use the planned implementation’s commented tests and the [contracts](contracts/built-image-cache.md) as the expected outcomes.

Run local checks after implementation from the repository root.

```sh
go test ./internal/guest ./internal/imagecache ./internal/identity ./internal/web ./internal/workspace
go test ./tests/contract
npm --prefix ui test -- infrastructure
npm --prefix ui run build
```

## Checks

1. **Launch and reuse.** Open the settings as an administrator and confirm built-image caching is On. Launch the fixture on a fresh VM, let it reach a usable IDE, and confirm publication. Launch a fresh workspace as the second authorized user. Confirm it pulls the complete cached image without building or installing Features. Open the project file and verify the runtime hook and separate checkout. Repeat with the Dockerfile variant and verify that retained-home recreation takes the selected image path.
2. **Matching and rebuild.** Push an ordinary change with identical config and launch again. Verify a hit. Change `devcontainer.json` on the configured origin and verify the next launch builds and can replace it. Run **Rebuild container** from a matching workspace and confirm the new complete result supersedes the prior image. Rebuild an older workspace with different config and verify local success with no shared replacement. Exercise interrupted publication and overlapping misses in the controlled fixture.
3. **Scope, storage, and fallback.** Request the same image from the second project and with a revoked grant. Verify the manifest, config and layer access are denied. Force an unavailable, incomplete, or evicted entry and verify an authorized ordinary build. Use the storage test fixture to verify admission limits, lease protection, and accounting for shared content.
4. **Admin save and background purge.** Attempt a settings change as a member, then save Off as an administrator and reload. Verify new built reads and writes stop, including an upload already in flight. Confirm built references from the disabled generation are removed and public reuse works under its own policy. Re-enable and confirm the next fresh launch can publish a new image. Clean up the disposable resources.

These checks validate functionality, access, and recovery. The `strands-playground` speed hypothesis has no benchmark or acceptance threshold.
