<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branding/svg/shiftbox-logo-dark.svg">
    <img src="branding/svg/shiftbox-logo-light.svg" alt="ShiftBox" width="360">
  </picture>
</p>

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

ShiftBox is in the design stage. Explore the [architecture readback](documentation/architecture/design-readback.md) for the first release scope and the [technology decisions](documentation/architecture/technology-decisions.md) for the planned stack.

ShiftBox is released under the [MIT License](LICENSE).
