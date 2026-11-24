---
layout: default
title: Troubleshoot an AWS installation
permalink: /troubleshooting/
---

# Troubleshoot an AWS installation

Start with the first step that failed. Use the same AWS account, region, stack, and rendered template when you retry.

## Stack and setup problems

| Symptom | What to check |
| --- | --- |
| A change set fails or replaces a persistent resource | Review the CloudFormation **Events** tab and the change set. Check the selected account, region, Availability Zone and permissions for named IAM resources. Resolve unexpected replacements before executing the change set. |
| PostgreSQL does not start | In ECS, check service events and PostgreSQL task logs. Verify the installation volume mounted, the TLS secret has the expected certificate and key, and the public CA in `PostgresCaPem` matches the database certificate. |
| The setup tunnel does not open | Check the local Session Manager plugin, stack read access and the bootstrap policy identified by `SetupOperatorPolicyArn`. Confirm PostgreSQL is ready and port 8444 is available locally. |
| The one-pass setup claim fails | Read the claim from `SetupClaimSecretArn` for the correct stack. If account creation was interrupted, retry with the same email and password. After account creation, sign in with those credentials. |
| The controller stays stopped | Confirm the helper verified activation and stopped the setup task. Check that the stack reached `Phase=active`, then look at controller service events and task logs in ECS. |

With an authorized AWS CLI session, you can list failed CloudFormation events. Set `STACK_ID` and `AWS_REGION` to your stack ID and region.

```sh
aws cloudformation describe-events --stack-name "$STACK_ID" \
  --region "$AWS_REGION" --filters FailedEvents=true
```

For interrupted staged setup, [run the helper again for the same stack]({{ '/install/' | relative_url }}#complete-the-protected-browser-setup). Before changing the controller build or replacing storage, follow the [upgrade and recovery guide]({{ '/upgrade-recovery/' | relative_url }}).

## Browser and workspace problems

| Symptom | What to check |
| --- | --- |
| The management or IDE hostname does not resolve | Verify the client is on the private network and the installation suffix is forwarded to a resolver in the VPC. With Tailscale, check the restricted nameserver and its route. [Private DNS and routing]({{ '/private-access/' | relative_url }}) |
| The hostname resolves but the browser times out | Verify the client accepts the private subnet route and can reach the service subnet on TCP 8443. Check the route approval, source CIDR or tailnet access policy. |
| The browser reports an untrusted certificate | Check the hostname, validity and issuer. For a private CA, verify the fingerprint and import the browser CA on that client. The protected loopback setup uses a separate certificate. |
| The official AMI does not validate | Check that the catalog JSON matches the image configured in the template and the stack Region. Review the validation operation and its temporary EC2 instance. Check EC2 capacity and network egress in the workspace subnet. |
| Repository authorization or checkout fails | Check that both the GitHub App installation and the workspace owner's connected account cover the repository. Check the ref and Dev Container file path in the project. [GitHub and project setup]({{ '/first-workspace/' | relative_url }}) |
| The workspace cannot build or start | Open the workspace and inspect its build or lifecycle logs. Check whether the devcontainer's images and dependencies are reachable from the workspace subnet. |

When reporting a problem, include the ShiftBox commit or controller image digest, AWS Region, failing phase and relevant error text. Remove private hostnames, account and resource identifiers, and credentials before posting publicly. [ShiftBox issues](https://github.com/jflatten/ShiftBox/issues)
