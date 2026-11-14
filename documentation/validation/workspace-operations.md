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
The Go acceptance test passed with the race detector. The live
operations journey for T088 and T103 requires a native workspace and live
browser sessions so origin isolation and established connection closure can
be observed on the frozen candidate.
