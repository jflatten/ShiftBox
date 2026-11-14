# MVP Stepper accessibility evidence

T119 tracks the keyboard and focus portions of FR-058, SC-010, and V14. The browser suite uses a production export with disposable API responses. Each test navigates with Tab and activates with Enter or Space. File picker tests use Enter to open the native picker and supply synthetic files through Playwright. The browser does not contact GitHub, Dex, UTM, or the running acceptance installation.

## Results from November 14, 2026

The production Next.js 16.4.6 build and TypeScript check passed. All 17 Playwright 1.65.1 checks passed on macOS with installed Google Chrome 157.0.8103.51 and a 390 × 844 viewport. The Playwright-managed Chromium binary was unavailable. Firefox and WebKit are pending.

| Workflow | Keyboard and focus evidence |
| --- | --- |
| Setup | Tab skips the informational steps and first focuses Administrator email. Enter on an empty submission returns focus there. Space acknowledges the temporary password, then Tab reaches sign-in. A restricted fixture progresses through password, database, certificate, provider, and activation. The current stage exposes `aria-current="step"`. |
| GitHub connection and external reauthorization | Enter begins each handoff. The resulting Continue link gets focus and the informational Stepper remains outside the tab path. |
| Workspace launch | Space selects the template radio. Enter advances to review and submits after keyboard entry of the workspace name. The resulting progress retains both current and earlier step evidence. |
| Workspace start, stop, rebuild, repair, and exit repair and retry | Each action is reachable by Tab and Enter on workspace detail. The shared Stepper exposes an accessible name and status for each step and a polite current-status announcement. Enter on a completed step preserves current status and focus. Tab moves to the current step and skips the disabled pending step. |
| Workspace deletion | The dialog starts on Cancel. Escape returns focus to the trigger. The action stays disabled until the typed name matches and the user acknowledges the data-loss condition. Enter then opens deletion progress. |
| Maintenance and browser certificate replacement | Empty certificate submission focuses the first file picker. Enter opens both pickers and submits. A keyboard user can then open the campaign's progress. Both operations retain the selected and current step evidence. |
| Local administrator password reset and offboarding | Both modals start on Cancel. The reset's Done button requires the acknowledgement checkbox. The offboarding dialog can show and inspect a durable workspace stop operation. |
| Backup and restore validation | Invalid submission returns focus to Destination or Archive path, respectively. Keyboard submission clears the passphrase. The resulting Steppers expose disabled pending steps and inspectable reached steps. |
| Recovery review and activation | Space and Enter select and confirm the recorded identity and retention decision. Provider reconciliation precedes activation. The final activation step remains inspectable by keyboard. |

The initial browser run found two identical certificate forms after Infrastructure loaded for an administrator with both certificate and maintenance permissions. The app used the same React key for two sibling workflows. The corrected export presents one certificate form. The combined Infrastructure browser test exercises that transition using a single accessible certificate submission control.

## Manual Chrome observations

In a separate Chrome tab connected to an isolated loopback fixture, I stepped through the following paths using keyboard input and checked the browser accessibility tree for actual focus:

- On Local setup, Tab landed on Administrator email, then on Create administrator. Enter on the empty form revealed “Enter the administrator email” and returned focus to the email field.
- On Backup & Recovery, Tab reached Archive path and Enter on the empty restore form kept focus there while showing all three required-field messages. Shift+Tab reached Create backup. Enter showed the two required backup fields and moved focus to Destination.
- On workspace detail, Tab reached the completed step. Enter revealed the earlier evidence alongside current evidence and preserved focus on the completed step. The next Tab moved to the active step. Another Tab skipped the pending step.
- In workspace deletion, focus moved to Cancel on open. Escape returned focus to Delete workspace. The confirmation stayed disabled while the name and acknowledgement were empty.
- In Account, selecting Renew authorization and Reconnect GitHub with Enter moved focus to Continue to organization and Continue to GitHub. I stopped at the links.
- In Infrastructure, an empty certificate submission moved focus to Certificate chain and showed both missing-file errors. Enter on View progress exposed the active campaign step while leaving focus on the trigger.
- In People, opening Reset password and Disable account focused Cancel. Dismissing either with Escape initially sent focus to the document. After the fix, the browser regression confirmed focus returns to both original triggers.

The manual session covers shared workspace progress, setup and backup/restore validation, the two handoff flows, three dialog entry/exit paths, and Infrastructure entry. Individual workspace action starts, the subsequent setup submissions, native file selection, People submission, backup/restore submission, and recovery review and activation have automated evidence only. The exact-stack controller, screen-reader speech output, Firefox, and WebKit are pending.

## Reproduce

Build the exported UI and serve `ui/out` on an unused loopback port. The suite intercepts `/api/v1/**` and supplies workflow responses to the exported pages.

```sh
npm --prefix ui run build
python3 -m http.server 38491 --bind 127.0.0.1 --directory ui/out
```

In another terminal, with the Playwright-managed Chromium browser installed:

```sh
SHIFTBOX_TEST_URL=http://127.0.0.1:38491 npm --prefix ui run test:e2e -- accessibility.spec.ts --project=chromium
```

The recorded run used a temporary Playwright configuration with `channel: 'chrome'` and the same loopback origin to select installed Chrome.
