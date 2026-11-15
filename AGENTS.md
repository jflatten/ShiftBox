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

  Tasks that are labeld with `[P]` shoudl be run in parallel inside a subagent.

  Do not run more than 5 subagent parallel tasks as one time.

  The project has the appropriate skills installed for the technologies being utilized.  Always
  look for a relevant skill before proceeding.

  Never edit or change the documents in `documentation/architecture`.  These are reference only
  documents that require human oversight. 

## Architecture and Guiding Principles

- Simplicity in ALL things
- Components displayed in the UI should have human readible name and not UUID's
- Human readible code with comments that help a reader understand the context of the code
  as it relates to the application as a whole.
- Workflows are always expressed visually in our UI in realtime using Mantine Stepper.
- Always use Mantine controls for our UI. Reference: <https://mantine.dev/llms.txt>
- Always use the mantine MCP server when selecting components.
- Always apply our branding standard with the artifacts located in the `branding/` directory.

## Testing

- Always write unit tests before writing code
- Test should reflect real world failure scenarios
- Each test must have a comment that describes the test so a reader understands what is being
  tested.
