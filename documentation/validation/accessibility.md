# MVP Stepper accessibility evidence

T119 tracks the keyboard and focus portions of FR-058, SC-010, and V14. The browser suite uses a production export with disposable API responses. Each test navigates with Tab and activates with Enter or Space. File picker tests use Enter to open the native picker and supply synthetic files through Playwright. The browser does not contact GitHub, Dex, UTM, or the running acceptance installation.

## Results from November 14, 2026

The production Next.js 16.4.6 build and TypeScript check passed. All 18 browser cases passed in each of Chrome 157.0.8103.51, Playwright Firefox 159.0, and Playwright WebKit 27.2 on macOS with a 390 × 844 viewport. The 16 focused workspace unit tests also passed.

The first WebKit run skipped buttons with plain Tab. On macOS, the WebKit tests now use Option+Tab to reach page controls with the browser's default keyboard settings. With that correction, the offboarding test exposed a focus loss after the Confirm disable button disappeared. The dialog now focuses the completion message once it appears. Keyboard users can then inspect the workspace stop progress. [Safari keyboard shortcuts](https://support.apple.com/guide/safari/keyboard-shortcuts-and-gestures-cpsh003/mac)

| Workflow | Keyboard and focus evidence |
| --- | --- |
| Setup | Tab skips the informational steps and first focuses Administrator email. Enter on an empty submission returns focus there. Space acknowledges the temporary password, then Tab reaches sign-in. A restricted fixture progresses through password, database, certificate, provider, and activation. The current stage exposes `aria-current="step"`. |
| GitHub connection and external reauthorization | Enter begins each handoff. The resulting Continue link gets focus and the informational Stepper remains outside the tab path. |
| Workspace launch | Space selects the template radio. Enter advances to review and submits after keyboard entry of the workspace name. The resulting progress retains both current and earlier step evidence. |
| Workspace start, stop, rebuild, repair, and exit repair and retry | Each action is reachable by Tab and Enter on workspace detail. When the submitted button becomes disabled, focus moves to the Workspace operation heading unless the user moved to another control. The shared Stepper exposes an accessible name and status for each step and a polite current-status announcement. Enter on a completed step preserves current status and focus. Tab moves to the current step and skips the disabled pending step. |
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
- In Recovery review, Tab reached the current step and the identity checkbox. Space selected the identity and Enter submitted it. I continued through provider reconciliation, retention approval, and activation, then opened the completed step to read its evidence. Between stages, the submitted button disappeared and focus fell back to the document. The updated UI focuses the review heading after each successful decision and the completion heading after activation. The regression passed in Chrome, Firefox, and WebKit.

In a follow-up Chrome session, I sent individual keyboard inputs through Playwright and inspected the focused DOM element and accessibility tree after each transition. The fixture supplied local API responses and disposable files through the picker interface. It did not exercise native file dialog navigation, a real sign-in, or screen-reader speech.

- In restricted setup, Tab reached both password fields and Enter submitted the change. One Tab reached Sign in. Reloading simulated returning after sign-in. Enter submitted the SQLite profile, both certificate file buttons opened their pickers, and Enter validated the certificate and platform image. After entering and saving capacity, one Tab reached the activation acknowledgement. Space enabled activation and Enter advanced to Sign in to ShiftBox, reached with one Tab.
- In workspace launch, Space selected the template. Enter opened review, then the workspace name and Launch workspace were reachable by Tab. The review and submission transitions reset focus to the document. Tab reached the completed step, Enter exposed its evidence, and the next Tab reached Enroll guest.
- In workspace detail, Enter submitted each of Start, Stop, Rebuild, Start repair, and Exit repair and retry setup. With the updated export, the named Workspace operation heading received focus after each submission. The next Tab reached the first available step.
- In workspace deletion, the entered name and Space on the acknowledgement enabled Delete permanently. Enter closed the dialog and focus returned to the document. Tab reached deletion progress and Enter opened earlier evidence.
- In Infrastructure, Enter opened the two certificate file pickers and submitted the selected fixtures. Focus returned to the document as the operation loaded. Tab reached the completed step, Enter opened its evidence, and the next Tab reached the active install step. Enter on maintenance View progress preserved focus on the trigger while the campaign Stepper loaded.
- In People, Enter generated the fixture password. Focus returned to the document. Tab reached the acknowledgement, Space enabled Done, and Enter closed the dialog. Confirm disable focused the completion message and Tab reached the stop workflow.
- In Backup & Recovery, Enter submitted both forms with valid fixture paths. Focus returned to the document after each submission. Tab reached the first available step, Enter opened its evidence, and one Tab reached the active archive or replacement check.

The release tracker covers exact-stack V14 validation. The document focus resets observed in the fixture remain visible here for follow-up.

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
