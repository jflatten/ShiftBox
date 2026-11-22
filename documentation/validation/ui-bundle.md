# Bundled UI deployment validation

T031–T035 were deployed to `shiftbox-demo` on November 17, 2026, using the `default` AWS profile in account `524941137496`, region `us-east-2`. The user confirmed the live deployment before requesting the commit and push.

## Changes

- Renamed the Infrastructure Resources tab to Platform.
- Blank CPU, memory, and disk capacity limits independently mean Unlimited. Fresh setup defaults to Unlimited; existing finite budgets survive the schema upgrade.
- Added the header light/dark toggle with browser storage persistence and matching branded logos.
- Replaced user-facing Templates wording with Projects, including server problem titles. Existing routes, API fields, permission identifiers, and raw audit action codes remain unchanged.

## Upgrade and rollout

The running controller completed and verified encrypted management backup `c6722f73-e010-4404-81dc-b132566f3714`. The archive and protected passphrase remain on the installation volume under `/var/lib/shiftbox/pre-schema34/`.

Terraform stopped only the controller for migration. The migration task verified the archive checksum against the completed backup record, passed the all-retained-workspaces-current gate, and applied and verified PostgreSQL schema 33 to 34. The schema change permits zero budget limits while rejecting negative limits; resource profile allocations remain positive.

The reviewed release plan updated the controller service, replaced the controller and setup task definitions, and recomputed the existing setup operator policy reference. PostgreSQL, persistent storage, and networking were unchanged.

- Running controller: `shiftbox-demo-controller:20`.
- Image digest: `sha256:ebe18fd3543bc6b369cd95f9704672857a10442d66877bebaab92f0a6b46a370`.
- Controller and PostgreSQL both report `COMPLETED`, with one desired and running task each.
- The management Infrastructure URL returned HTTP 200 over verified TLS.

Review URL: <https://shiftbox.demo.shiftbox.internal:8443/infrastructure/>.

## Validation

Before deployment, all 153 UI unit tests, seven local Chrome browser journeys, the production UI build, OpenAPI generated-type check, full Go suite, and Go vet passed. SQLite and disposable PostgreSQL checks verified migration preservation, unlimited persistence, and reservation enforcement when finite limits are reinstated. The deployment build passed the embedded browser assets check and Linux container executable smoke check.

Chrome readback after rollout confirmed the Platform tab, Projects navigation, dark mode control, and saved unlimited capacity inputs. The user independently confirmed the deployment. The in-app browser could not resolve the private hostname, so live browser inspection used the existing Chrome session. No budget or profile settings were changed by the deployment verification.

## Cleanup

Terraform removed the temporary backup and migration task definitions; both are inactive and their local Terraform state contains no managed resources. The temporary helper ECR image was deleted. ECS reports the migration task stopped and its network attachment deleted. The deployed controller image, previous release image, and protected management backup were retained.

Local plans, variables, logs, image digests, binary hashes, and cleanup evidence remain under `.cache/aws-demo/ui-bundle-schema34/`. Deployment state and registry credentials are not committed.
