# Controller UI and Administrator Grants

## Install Local Helper

- Participating developers and team members open **Install Local Helper** from the upper-right personal settings menu in the private MVP. Offer the locally built, ad hoc–signed macOS app for per-user installation and identify it as an internal build. Provide the installation's HTTPS URL for first run and instructions for normal macOS prompts, including approval for startup at macOS sign-in.
- On first launch, the helper opens a first-party ShiftBox browser sign-in and approval page on the configured origin. Show which installation and signed-in account are being paired before confirmation. Report only observed download, approval, and connection states. If macOS policy blocks the download or launch, follow the local-build validation path in the [quickstart](../quickstart.md).
- Use the existing branded Mantine shell and workflow Stepper, project state chips, and keyboard-accessible controls. The existing workspace lifecycle view shows the admitted SSH-triggered start with its normal operation and progress.

## Administrator connection grants

Provision the exact workspace grant separately before testing another user's administrator connection. Use the existing **Administrator connection grants** panel under the `workspace.manage_connection_grants` permission to list and revoke grants. Refreshing the panel reads persisted state.

The existing DELETE commits the revocation and closes matching browser IDE and SSH connections through the shared registry. Both discovery and new SSH connections require a current grant for the exact workspace and administrator.
