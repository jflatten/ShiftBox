import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const script = readFileSync(resolve(__dirname, '../../internal/proxy/human-activity.js'), 'utf8');

// An unattended tab, focus changes and synthetic events must never renew the
// clock; only trusted input may send a bounded, throttled activity notification.
test('only trusted human input renews activity', async () => {
  jest.useFakeTimers();
  const handlers: Record<string, (event: {isTrusted: boolean}) => void> = {};
  const request = jest.fn().mockResolvedValue({ok:true});
  let focused = true;
  const doc = { visibilityState: 'visible', hasFocus: () => focused };
  new Function('window', 'document', 'fetch', 'performance', 'setTimeout', script)(
    {addEventListener: (name: string, handler: typeof handlers[string]) => {handlers[name]=handler;}},
    doc, request, {now: () => Date.now()}, setTimeout,
  );
  expect(request).not.toHaveBeenCalled();
  expect(handlers.focus).toBeUndefined();
  handlers.keydown({isTrusted:false});
  focused=false; handlers.pointerdown({isTrusted:true});
  focused=true; doc.visibilityState='hidden'; handlers.keydown({isTrusted:true});
  expect(request).not.toHaveBeenCalled();
  doc.visibilityState='visible'; handlers.keydown({isTrusted:true});
  expect(request).toHaveBeenCalledTimes(1);
  handlers.pointerdown({isTrusted:true});
  handlers.wheel({isTrusted:true});
  expect(request).toHaveBeenCalledTimes(1);
  jest.advanceTimersByTime(15000);
  expect(request).toHaveBeenCalledTimes(2);
  jest.advanceTimersByTime(60000);
  expect(request).toHaveBeenCalledTimes(2);
  expect(request).toHaveBeenCalledWith('/_shiftbox/activity', expect.objectContaining({method:'POST',body:'{}',credentials:'same-origin'}));
  jest.useRealTimers();
});
