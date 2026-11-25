<picture>
  <source media="(prefers-color-scheme: dark)" srcset="branding/svg/shiftbox-logo-dark.svg">
  <img src="branding/svg/shiftbox-logo-light.svg" alt="ShiftBox" width="320">
</picture>

# A workspace for you and your coding agent

ShiftBox is an open-source, self-hosted development sandbox for you and your AI coding agent. You share the same project files, tools, and runtime, so you can open VS Code in your browser and pick up where your agent left off.

Launch a workspace from a GitHub repository using its Dev Container configuration. Each workspace runs in its own private EC2 virtual machine in your AWS account. Project files and your home directory persist across restarts and container rebuilds. Deleting the workspace removes its disk.

## Get started

Use AWS CloudFormation to install ShiftBox in your existing VPC, then finish setup in your browser.

* [Install ShiftBox on AWS](https://jflatten.github.io/ShiftBox/install/)
* [Launch your first workspace](https://jflatten.github.io/ShiftBox/first-workspace/)

ShiftBox is released under the [MIT License](LICENSE).
