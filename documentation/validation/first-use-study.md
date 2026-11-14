# Moderated first-use study

T124 and SC-002 require at least 10 participants who have never used
ShiftBox, with at least nine completing their first attempt without
administrator assistance. Recruitment and the study are pending.

## Before the sessions

- Recruit people who can work with a browser-based development environment
  and confirm each person has never used ShiftBox.
- Freeze a release candidate and record its controller, browser, database,
  UTM, platform, guest, IDE, schema, and certificate versions and artifact
  digests. Use the same candidate and start conditions for every participant.
- Give each participant a fresh member session with access to a fixture
  repository and one authorized template. Verify capacity, TLS, Git
  authorization, and provider availability before starting the session.
- Keep the participant's real name and account credentials outside the
  evidence log. Use a participant code in each row.

## Moderator script

“Starting from this page, launch the authorized workspace template. Tell me
what ShiftBox is doing while it starts. When the workspace is ready, open it
in the browser.”

Start timing when the participant receives the prompt. Observe the template
selection and launch, what the participant identifies as the current
workflow step, and whether the browser opens the ready IDE. Let the
participant recover from mistakes within the same attempt. Avoid pointing
out controls, interpreting status, or giving lifecycle instructions. End
the attempt when all three criteria are met, the participant stops, or the
moderator intervenes. Record interruptions and failures with the stage and
visible message. A later assisted retry can help diagnose a failure and
belongs outside the first-attempt success count.

| Participant code | Eligibility confirmed | Launched authorized template | Identified current progress | Opened ready IDE | Intervention or failure | First-attempt result |
| --- | --- | --- | --- | --- | --- | --- |
| Pending recruitment | | | | | | |

The release gate passes when the completed study records at least 10 eligible
participants and at least nine first-attempt successes across all three
criteria without administrator assistance.
