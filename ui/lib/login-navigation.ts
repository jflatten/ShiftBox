const sessionKey = 'shiftbox:session-deadline';
let observedDeadline: string | undefined;
let openingLogin = false;

// Remember only the deadline so a denial after a full page load can explain
// the loss of a previously authenticated session in this tab.
export function rememberSession(expiresAt: string | undefined) {
  observedDeadline = expiresAt ?? 'unknown';
  try { window.sessionStorage.setItem(sessionKey, observedDeadline); } catch { /* The in-memory deadline still works. */ }
}

export function openLogin(reason?: 'session-expired' | 'session-ended') {
  if (openingLogin) return;
  openingLogin = true;
  observedDeadline = undefined;
  try { window.sessionStorage.removeItem(sessionKey); } catch { /* Navigation still works. */ }
  window.location.replace(reason ? `/auth/login#${reason}` : '/auth/login');
}

export function returnToLoginOnSessionLoss() {
  let previous = observedDeadline;
  try { previous ??= window.sessionStorage.getItem(sessionKey) ?? undefined; } catch { /* Use the in-memory deadline. */ }
  if (!previous) return false;
  openLogin(Date.parse(previous) <= Date.now() ? 'session-expired' : 'session-ended');
  return true;
}
