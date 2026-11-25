# ShiftBox

ShiftBox is an open-source development sandboxing solution that utilizes the devcontainer
standard to provide a fully isolated and configured development environments that are shared between
the developer and AI development agents.

## Agent Rules

  When you are thinking and generating code, use our design decisions as your primary guidance.
  When expanding the scope or design always first confirm with a human.  Your guiding principle
  is to ask when you are unsure. Ground all decisions in our approved architecuture.  Always
  consult the top level documentation and follow relevant links to details to understand or
  approved architecture here: `documentation/architecture/design-readback.md`

  When executing a specification, only implement what the tasks request.  Refrain from over-
  engineering and expanding into edges cases that are not explicitly called out.

  Always report what task you are working on in your responses. Example:
  
  ```text
  T104: Building the backend API infrastructure endpoint /api/v1/loging
  ```

  Always checkoff tasks when they are complete immediately in `tasks.md`

  Administrative and operator-facing capabilities are complete only when discoverable admin UI
  controls and state, permissions, and validation ship with the backend and are checked end-to-end,
  including save/reload persistence; API or database support alone is not complete.

  Tasks that are labeld with `[P]` should be run in parallel inside a subagent.

  Do not run more than 5 subagent parallel tasks as one time.

  The project has the appropriate skills installed for the technologies being utilized.  Always
  look for a relevant skill before proceeding.

  Never edit or change the documents in `documentation/architecture`.  These are reference only
  documents that require human oversight.

  Use the AWS `shiftbox-testing` account (`888293065132`) in `us-east-2` for development test
  instances and review deployments. Use the `shiftbox-testing` AWS CLI profile.

  Before taking development actions against a live instance, identify the running ShiftBox
  deployments in that account. If multiple deployments are running and the user has yet to
  name the target for the current task, list the candidates, ask which deployment to use,
  and wait for the answer. Use the selected CloudFormation stack name and management URL for
  deployment, testing, and cleanup.

## Architecture and Guiding Principles

- Simplicity in ALL things
- Components displayed in the UI should have human readible name and not UUID's
- Human readible code with comments that help a reader understand the context of the code as it relates to the application as a whole.
- Workflows are always expressed visually in our UI in realtime using Mantine Stepper.
- Always use Mantine controls for our UI. Reference: <https://mantine.dev/llms.txt>
- Always use IBM's Carbon Design System for our UI. Reference: <https://www.carbondesignsystem.com/>
- States should always be displayed in the UI using chips. Reference: <https://mantine.dev/core/chip/>
- Always use the mantine MCP server when selecting components.
- Always apply our branding standard with the artifacts located in the `branding/` directory.

## AWS Practices

- Every fresh ShiftBox AWS deployment has exactly two operator steps. Run CloudFormation once,
  then complete first-login setup in the ShiftBox browser UI. This is a hard product requirement
  for both public and private deployments. The initial stack must bootstrap all stack-owned
  prerequisites, including database TLS and any managed browser certificate. First login and
  activation must work without a follow-up stack update, a manual certificate bootstrap, or a
  separate setup task.
- Use CloudFormation for ShiftBox deployments on AWS. Review and execute change sets for stack
  creation and updates. Use Terraform for the separately managed release publisher, qualification
  infrastructure, and shared development registry.
- Always clean up your test resources when completing your task.
- Prefix new ShiftBox-managed AWS tag keys with `shiftbox-`. For new
  CloudFormation deployments use `shiftbox-project=shiftbox` and
  `shiftbox-cluster-name=<name of deployment>` where tagging is supported.
  Preserve existing tag contracts for deployed resources and legacy release assets.

## Deploy an update to a development test instance

1. Run `aws sts get-caller-identity --profile shiftbox-testing --region us-east-2` and verify
   that `Account` is `888293065132`. Confirm that the selected deployment's management URL and running
   controller match the target for the task.
2. Review `git status --short`. The controller build includes the browser assets from the current
   checkout. Use the [AWS development guide](platform/development/README.md#iterate-on-one-artifact-at-a-time)
   to build and publish a controller image to private ECR. Use the selected deployment's
   guest compatibility version and management URL, and record the published image digest.
3. Confirm the deployment's database schema is compatible with the build. For a build with
   new migrations, complete the approved schema upgrade before switching the image.
4. Save the selected stack's current template and record the current image for rollback. Set the
   controller image to the new digest-pinned URI in the template. Use the same image for the
   setup task if the stack has one. Create a CloudFormation change set with the current stack
   parameters and review it before executing it. Expect a new controller task definition and
   an update to the controller ECS service. Investigate changes to PostgreSQL, storage,
   networking, or permissions before proceeding. A single-task controller may be briefly
   unavailable during replacement.
5. Wait for the infrastructure update and the controller and PostgreSQL ECS services to
   stabilize. Confirm that the running controller uses the published digest. Verify the feature
   while signed in at the selected management URL, including save and reload for persistent
   changes. Report the review URL, ECS task revision, and checks performed.

If the rollout or live smoke test fails, check the selected deployment's ECS service events
and controller logs in CloudWatch. To roll back a controller-only update, review and execute a
new CloudFormation change set using the saved template and previous image digest.
Confirm the previous digest is running and verify the management URL again.

## Testing

- Always write unit tests before writing code
- Test should reflect real world failure scenarios
- Each test must have a comment that describes the test so a reader understands what is being
  tested.
