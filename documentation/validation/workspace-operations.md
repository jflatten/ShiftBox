# Workspace operations acceptance

Run the Go acceptance test against its isolated SQLite fixture and simulated
provider.

```sh
go test -race ./tests/acceptance -run '^TestWorkspaceOperationsAcceptance$' -count=1 -v
```

| Check | Go evidence |
| --- | --- |
| Capacity | A CPU refusal identifies the limit, queues no operation, and leaves the disk reservation held. |
| Revocation | Session revocation closes the registered IDE connection before returning and refuses the same session's next request. |
| Deletion | An unconfirmed deletion is refused. An explicit confirmation persists the actor and reason and retains the disk reservation pending provider cleanup. |
| Recovery denial | A revoked guest identity cannot receive a new enrollment grant or boot the retained VM. |
| Maintenance authority | A member cannot schedule a campaign. Administrator admission records the retained workspace generation and waits until the deadline. |

The browser fixture in `ui/tests/e2e/operations.spec.ts` exercises the
maintenance Stepper, destructive confirmation, recovery grace, long-stop
refusal, revocation feedback, and private preview navigation. All six cases
passed in Chrome. The maintenance case alternates running and waiting
responses for the same waiting step and checks that the live announcement
does not change and its spinner stays hidden until a real state change.
The Go acceptance test passed with the race detector. The combined T088
and T103 run passed all seven Chrome cases in 11.3 seconds using the
`mvp-rc2` controller. The live preview case used two genuine local Dex
sessions and a disposable test guest enrolled over the production HTTPS
enrollment handler. Its WebSocket service ran on loopback behind the
actual mutual TLS guest tunnel and browser proxy.

| Browser evidence | Result |
| --- | --- |
| Preview and mutation authority | An ungranted preview request was refused. The preview origin could not issue a management entry grant, even with the administrator's valid CSRF token. |
| Origin separation and live revocation | Chrome observed separate host-only management and preview cookies and initial WebSocket frames for both accounts. Revoking the administrator's grant closed only their established socket, rejected reload and new entry, and preserved owner access. The database recorded the revocation. |
| Destructive clarity | The deletion dialog started on Cancel, required confirmation, explained retained-file loss and showed the unresolved cleanup state. |
| Recovery and maintenance | The browser showed activity and outage grace, kept a refused long-stop workspace stopped, and kept waiting progress stable until the maintenance outcome changed. |

The first live case used a seeded workspace record and an independently
enrolled client. The T124 and T125 follow-up ran the same Chrome cases
against the exact rc2 controller with the packaged guest agent on UTM
5.0.8. Protected setup created a fresh SQLite installation and two Dex
accounts. The ordinary launch worker enrolled the native VM. A temporary
NoCloud seed provided the localhost preview service, and the fixture
supplied the explicit administrator grant. All seven Chrome cases passed
in 11.5 seconds, including the live origin and revocation checks. The Go
operations acceptance also passed with `-race`, and the live controller
recorded one allowed explicit administrator connection.

The fixture removed the enrolled VM and its disk after the run. The
GitHub-backed project-container and IDE journey remains in the release
checks.

## Native capacity refusal

The race-enabled `TestUTMCapacityRefusesExhaustedResources` passed on
`mvp-rc2` in 61.41 seconds. A native UTM guest enrolled and stayed
running while the fixture set three revisioned budgets. Each budget
left room in two resource dimensions and used all capacity in the
third. CPU, memory, and disk exhaustion each refused a launch and its
retry, identified the resource and quantities, and created no workspace,
reservation, or queued operation. The existing VM kept the same
allocation, disk and reservations.

Set `SHIFTBOX_TEST_UTM_CAPACITY=1` with the normal native test settings.
The test exercises configured capacity limits on the real provider.
The separate launch API contract test covers the host-volume space check.

## Native retention and disk accounting

The race-enabled `TestUTMRetentionDeletion` passed against the pinned
`mvp-rc2` image in 67.97 seconds. After native guest enrollment, the
fixture disconnected the guest, advanced a test policy clock, and fed a
fresh CPU sample through the production activity evaluator. Outage grace
withheld the candidate. The fresh sample invalidated an earlier
candidate and postponed deletion to the new deadline.

The worker withheld provider-enforced stop through the 59-second
observation and stopped the real VM after the full graceful shutdown
window. A simulated partial cleanup left the real disk in place and
reserved. The next attempt deleted through UTM, but the fixture withheld
the successful reply. The worker kept the reservation until a retry
verified provider absence and storage cleanup. The final operation
succeeded with the disk released.

Run this opt-in test with the usual native UTM settings and
`SHIFTBOX_TEST_UTM_RETENTION=1`. The policy clock, activity sample,
and lost replies are fixture inputs. UTM observation timestamps,
the stop, deletion, and cleanup verification are native.

## Live browser fixture

Run the live Chrome case with `SHIFTBOX_OPERATIONS_FIXTURE` set to the
absolute path of a private JSON file. Include `disposable: true`,
`managementURL`, `workspaceID`, `previewURL`, and separate `owner` and
`administrator` objects with `email` and `password` fields. Use the
configured private HTTPS origin for `previewURL`. Its service must open a
same-origin WebSocket and send an initial frame. The member owns the running
workspace. The administrator needs an existing explicit connection grant,
which the test revokes.

```sh
NODE_USE_SYSTEM_CA=1 npm --prefix ui run test:e2e -- operations.spec.ts \
  --project=chromium -g 'private preview isolates'
```
