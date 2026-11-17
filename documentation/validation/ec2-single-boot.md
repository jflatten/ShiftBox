# EC2 single-boot enrollment validation

Validated on November 17, 2026, against `shiftbox-demo` in AWS account `524941137496`, region `us-east-2`, using the `default` profile. Source changes are on `feature/mvp`.

## Instance identity and replay

The first disposable probe used EC2's built-in instance identity credentials to sign a regional STS `GetCallerIdentity` request containing the challenge header. STS returned the exact account and instance identity. Replacing the signed challenge caused `SignatureDoesNotMatch`. The probe was removed through Terraform.

Unit tests cover a different instance, an unrelated role with the same session name, an unsigned challenge, an unexpected AWS action, duplicate query parameters, a stale signature and an untrusted endpoint. Enrollment tests cover concurrent nonce consumption, replacement keys, expired challenges, cancellation and a lost grant response. The concurrent replay test passed against SQLite and PostgreSQL with verified TLS. The Go suite and `go vet` passed.

## Live workspace

| Check | Observed result |
| --- | --- |
| AMI | `ami-0eea624c1e5701eb3` |
| AMI import operation | `8f8f5671-a001-4a73-9b66-1fbfd1fc833c`, succeeded |
| Import canary | `i-06e62d5e7c9bb6816`, confirmed terminated |
| Review template | `Single-boot EC2 review`, revision 1 |
| Review workspace | `single-boot-review` |
| Workspace ID | `15518f23-9390-4e9e-abf2-a29e6483b34d` |
| EC2 instance | `i-0c56b0eb800277ad0`, `t3.medium`, `us-east-2a` |
| EC2 power history | One `RunInstances` at 09:28:39 Pacific. No `StartInstances`, `StopInstances` or `RebootInstances` events when rechecked after the final controller cutover. |
| Guest console | One kernel boot |
| User data | Only version, public launch reference, controller endpoints and public CA |
| Network and AWS permissions | Private instance with no public IP and no attached IAM instance profile |
| Guest enrollment | Completed, followed by dev container configuration |
| Workspace readiness | UI reported Running and a healthy code-server probe |
| Exact proof replay | Validation controller rejected a second use of the successful proof before returning the first grant response |

The replay instrument logged `ShiftBox single-boot acceptance: exact successful proof replay rejected.` at 17:29:23 UTC. It reused the actual successful request in memory. The log contains no grant, nonce, AWS session credential or signed request.

The initial browser check reported a WebSocket failure and a subsequent expired management session. T022 subsequently added WebSocket launch readiness and verified first access on a fresh t3.medium, plus terminal use, editing, saving and reload on `single-boot-review`. See [IDE WebSocket validation](ide-websocket.md) for evidence and the controller revision 18 follow-up deployment.

The review template and running workspace were retained for human review, then cleaned up at the user's request on November 17, 2026. Both test workspaces and their disks were deleted, and the review template was archived. See [cleanup verification](ide-websocket.md#cleanup).

## Deployment

An encrypted management backup was verified before applying PostgreSQL schema 32. The archive checksum matched backup operation `6cefd088-7d87-48da-bab6-b503982eed05`. The first validation image had incorrect executable permissions. The corrected image restored service before live validation.

The final controller revision is `shiftbox-demo-controller:17`, with image digest `sha256:d7594535f72dcfbe60db27fcf996305292773a247eb1515a2998593eb819fc4d`. It contains the product implementation. The temporary AMI registration page and replay instrumentation were confined to the validation image.

After cutover, both controller and PostgreSQL reported `COMPLETED` with one running task each. The running controller's digest matched the final image. The login endpoint responded over verified TLS, and the temporary registration page returned HTTP 404.

Terraform removed the disposable identity probe, Packer runner and its resources, migration and backup task definitions, and the one-off diagnostic task definition. The diagnostic task could not connect from the host network and completed without changing application data.

Local redacted evidence and Terraform rollout logs are under `.cache/aws-demo/single-boot/`. The one-boot check was repeated after the final controller cutover.
