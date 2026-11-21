---
layout: default
title: Troubleshoot an AWS installation
permalink: /troubleshooting/
---

# Troubleshoot an AWS installation

| Symptom | Check |
| --- | --- |
| No stable download | Open the official releases list and choose an explicitly published RC if one is available. |
| Launch link opens the wrong region | Compare the console region and S3 object URL and version against the specific release manifest. Use the link for your planned region. |
| Change set fails or shows replacement | Check the account, region, IAM capability, AZ, quotas and service-side validation findings. Retain the stack and the original installation volume. Review a new change set after changing inputs. |
| Controller stays stopped | Check that PostgreSQL is healthy, the retained encrypted volume is mounted, and the protected setup wizard durably activated and stopped its one-off task. |
| Setup tunnel won't connect | Check the AWS CLI and Session Manager plugin, the scoped bootstrap policy, the exact stack ID and ECS setup task. Resume using the recorded installation instead of creating another stack. |
| A private hostname fails | Confirm the client route and DNS suffix, route approvals, reachable resolver and security group sources. Resolve the private address before opening the browser. |
| The browser reports an untrusted certificate | Check the issuer fingerprint, trust store, hostname and certificate validity. Correct the trust chain or install a valid certificate for the intended names. |
| Official image won't validate | Confirm the manifest is from the installed release and region, the AMI is available and the workspace instance type is compatible with amd64. Review the canary operation status. |
| Checkout fails after workspace launch | Verify the repository, project devcontainer configuration and workspace owner's GitHub App authorization. |

Use CloudFormation operation events for validation and diagnosis. On supported AWS CLI v2, run `aws cloudformation describe-events --stack-name "$stack_id" --region "$region" --filters FailedEvents=true`. Review stack events, task state and applicable logs with the authorized AWS session. Keep private URLs, identifiers and credentials out of public reports.

[Upgrade and recovery]({{ '/upgrade-recovery/' | relative_url }})
