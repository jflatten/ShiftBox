<picture>
  <source media="(prefers-color-scheme: dark)" srcset="branding/svg/shiftbox-logo-dark.svg">
  <img src="branding/svg/shiftbox-logo-light.svg" alt="ShiftBox" width="320">
</picture>

# A workspace for you and your coding agent

ShiftBox is an open-source, self-hosted development sandbox for developers and AI coding agents. The goal is to give both the same project files, tools, and runtime so they can work in the environment your repository defines.

When an agent edits a file or runs a test, you can pick up the work in the same workspace. A `devcontainer.json` describes the environment, and each workspace runs in its own virtual machine on infrastructure you control.

## What ShiftBox is designed for

- **Start from the repository.** Use the project's Dev Container configuration and reusable templates to set up workspaces consistently.
- **Work together in one environment.** Open the workspace in VS Code in your browser and inspect the files and test results your agent worked on.
- **Give each workspace its own VM.** Keep project workloads separate from each other and from the host that manages them.
- **Pick up where you left off.** Keep project files, your home directory, and designated data across restarts and container rebuilds. Deleting the VM deletes its workspace data, so push work you want to keep.

## Start small, stay self-hosted

The first planned deployment runs on a Mac alongside UTM. ShiftBox manages workspace setup, access, and cleanup, and UTM runs the virtual machines. AWS EC2 is planned as a second provider.

ShiftBox is being implemented and is not yet a runnable product. Explore the [architecture readback](documentation/architecture/design-readback.md) for the first release scope and the [implementation tasks](specs/001-shiftbox-mvp/tasks.md) for current progress.

## Development prerequisites

Use Go 1.27.4 and Node.js 26.13.x with npm. The UI is exported as static files;
Node is a build dependency only. Install UI dependencies with `npm --prefix ui ci`.
The single Go module pins dependencies in `go.mod` and `go.sum`.

The [validation quickstart](specs/001-shiftbox-mvp/quickstart.md) describes the
planned environment and acceptance scenarios. Full validation also requires an
awake, logged-in Apple Mac with the pinned UTM release, an approved Ubuntu 24.04
Minimal platform image, PostgreSQL with owned schemas and TLS, and trusted browser
certificates/service names. The [platform image recipe](platform/packer/README.md)
covers hardware-virtualized ARM64 and emulated AMD64 on Apple Silicon. Release versions and artifact digests must be recorded
before claiming support.

Build/test entry points are `make fmt-check vet test race`, `make ui-test ui-build`,
`make integration`, and `make browser`. These commands become usable as their
corresponding implementation and test tasks land; the current skeleton does not
provide application routes or end-to-end fixtures. UI tests deliberately fail
when no tests exist. Integration/browser tests require configured services.

`make release SHIFTBOX_UTM_RELEASE=1` is reserved for a controlled Mac with
disposable UTM fixtures. It must not be run against personal workspaces. No
release-gate or first-use-study success is implied by a passing unit test.

ShiftBox is released under the [MIT License](LICENSE).
