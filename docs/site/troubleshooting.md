---
layout: default
title: Troubleshoot an AWS installation
permalink: /troubleshooting/
---

# Troubleshoot ShiftBox on AWS

Start with the first step that failed. Keep the CloudFormation stack open so you can check its **Events** and **Outputs**.

## Stack and setup problems

| What you see | What to check |
| --- | --- |
| CloudFormation cannot create the stack | Open **Events** and read the first error. Confirm the AWS account and Region, that both subnets belong to your VPC, and that you chose the service subnet’s Availability Zone. Ask your AWS administrator to check permissions for named IAM resources. |
| Secrets Manager denies access | Confirm you selected the same AWS account and Region as the stack. Give your AWS administrator the two secret references from **Outputs** so they can grant you permission to read them. |
| The secret for the browser certificate has no value yet | Confirm the stack reached **CREATE_COMPLETE**. Ask your AWS administrator to check whether the ShiftBox server and its services started. |
| ShiftBox rejects the setup claim | Copy the complete value from the secret listed under **SetupClaimSecretArn**. If account creation was interrupted, retry with the same email and password. If the account was created, sign in with those credentials. |
| **Validate database** fails | Ask your AWS administrator to check the PostgreSQL service events and logs in Amazon ECS. The setup screen checks the database created by the stack. |
| ShiftBox is still starting after activation | Keep the setup tab open. Ask your AWS administrator to check the controller service in Amazon ECS if the status does not change. |

## Browser and workspace problems

| What you see | What to check |
| --- | --- |
| The browser cannot find the ShiftBox address | Connect to your private network. Ask your network administrator to check the DNS settings for your installation’s name. [Private routing and DNS]({{ '/private-access/' | relative_url }}) |
| The browser finds the address but times out | Ask your network administrator to check the route to the service subnet and access to port 8443 from your computer. |
| The browser reports an untrusted certificate | Check that you imported the certificate for this stack on this computer. Reopen your browser and use the exact address in **ManagementOrigin**. [Trust the browser certificate]({{ '/private-access/' | relative_url }}#trust-the-browser-certificate) |
| ShiftBox cannot open the GitHub repository | Check that the GitHub App has access to the repository and that you connected the right GitHub account. Verify the branch and Dev Container file in the ShiftBox project. [GitHub and project setup]({{ '/first-workspace/' | relative_url }}) |
| A workspace cannot build or start | Open the workspace and read its build or startup log. Ask your network administrator to check that the workspace subnet can reach the project’s image and package sources. |

When reporting a problem, include your ShiftBox version, AWS Region, the step that failed, and the error message. Remove private URLs, account and resource identifiers, and credentials before posting publicly. [ShiftBox issues](https://github.com/jflatten/ShiftBox/issues)
