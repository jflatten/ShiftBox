// Only real input in the focused workbench renews activity. Loading a tab,
// websocket traffic, editor output and periodic timers never create activity.
(() => {
  let lastSent = -Infinity;
  let pending = false;
  let scheduled = false;
  const interval = 15000;
  const send = () => {
    scheduled = false;
    if (!pending || document.visibilityState !== 'visible' || !document.hasFocus()) {
      pending = false;
      return;
    }
    pending = false;
    lastSent = performance.now();
    fetch('/_shiftbox/activity', {
      method: 'POST', credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      headers: {'Content-Type': 'application/json', 'X-ShiftBox-Activity': 'input'}, body: '{}',
    }).catch(() => {});
  };
  const input = event => {
    if (!event.isTrusted || document.visibilityState !== 'visible' || !document.hasFocus()) return;
    pending = true;
    const remaining = interval - (performance.now() - lastSent);
    if (remaining <= 0) send();
    else if (!scheduled) {
      scheduled = true;
      setTimeout(send, remaining);
    }
  };
  for (const name of ['keydown', 'pointerdown', 'pointermove', 'wheel', 'touchstart']) {
    window.addEventListener(name, input, {capture: true, passive: true});
  }
})();
