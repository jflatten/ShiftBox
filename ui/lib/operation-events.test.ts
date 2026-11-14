import { subscribeOperation } from './operation-events';

class TestEventSource {
  static latest: TestEventSource;
  listeners = new Map<string, EventListener>();
  close = jest.fn();
  constructor(public url: string) { TestEventSource.latest = this; }
  addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener); }
  removeEventListener(type: string) { this.listeners.delete(type); }
  emit(type: string, lastEventId = '') { this.listeners.get(type)?.(new MessageEvent(type, { lastEventId })); }
}

beforeEach(() => { Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: TestEventSource }); });
afterEach(() => { jest.restoreAllMocks(); });

// Missing replay events must cause an authoritative read, never extrapolated progress from event counts.
test('refetches after updates including a replay sequence gap', () => {
  const refresh = jest.fn();
  const stop = subscribeOperation('operation-one', refresh, jest.fn());
  const source = TestEventSource.latest;
  expect(source.url).toBe('/api/v1/operations/operation-one/events');
  source.emit('operation.updated', '1');
  source.emit('step.updated', '3');
  expect(refresh).toHaveBeenCalledTimes(2);
  stop();
  expect(source.close).toHaveBeenCalledTimes(1);
});

// A disconnected stream can hide a committed change, so both disconnect and reconnect refresh state.
test('refreshes on transport loss and reconnection but not heartbeats', () => {
  const refresh = jest.fn();
  const stop = subscribeOperation('operation-one', refresh, jest.fn());
  const source = TestEventSource.latest;
  source.emit('error');
  source.emit('open');
  source.emit('heartbeat');
  expect(refresh).toHaveBeenCalledTimes(2);
  stop();
});

// Revocation stops reconnection and clears authority through the caller before further updates can be consumed.
test('closes and ignores subsequent events after authorization revocation', () => {
  const refresh = jest.fn();
  const revoke = jest.fn();
  const stop = subscribeOperation('operation-one', refresh, revoke);
  const source = TestEventSource.latest;
  source.emit('authorization.revoked');
  source.emit('operation.updated', '4');
  expect(source.close).toHaveBeenCalledTimes(1);
  expect(revoke).toHaveBeenCalledTimes(1);
  expect(refresh).not.toHaveBeenCalled();
  stop();
  expect(source.close).toHaveBeenCalledTimes(1);
});
