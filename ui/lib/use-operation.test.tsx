import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor, act } from '@testing-library/react';
import type { ReactNode } from 'react';
import { useOperation } from './use-operation';

class Stream {
  static latest: Stream;
  listeners = new Map<string, EventListener>();
  close = jest.fn();
  constructor() { Stream.latest = this; }
  addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener); }
  removeEventListener(type: string) { this.listeners.delete(type); }
  emit(type: string, id: string) { this.listeners.get(type)?.(new MessageEvent(type, { lastEventId: id })); }
}

const initial = { id: 'one', version: 1, status: 'running', steps: [] };
let fetchMock: jest.Mock;
beforeEach(() => {
  fetchMock = jest.fn().mockResolvedValue({ ok: true, json: async () => initial });
  Object.defineProperty(globalThis, 'fetch', { configurable: true, value: fetchMock });
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: Stream });
});

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return { client, ...renderHook(() => useOperation('one'), { wrapper }) };
}

// An event gap must fetch the latest server state and close the stream once the operation has finished.
test('refetches a gap and stops listening after terminal state', async () => {
  const { result, unmount, client } = setup();
  await waitFor(() => expect(result.current.data?.version).toBe(1));
  fetchMock.mockResolvedValue({ ok: true, json: async () => ({ ...initial, status: 'succeeded', version: 4 }) });
  act(() => Stream.latest.emit('operation.updated', '3'));
  await waitFor(() => expect(result.current.data?.version).toBe(4));
  await waitFor(() => expect(Stream.latest.close).toHaveBeenCalled());
  unmount();
  client.clear();
});

// Loss of authority on a refresh must hide cached operation details and stop reconnecting.
test('hides cached progress when the server denies a refresh', async () => {
  const { result, unmount, client } = setup();
  await waitFor(() => expect(result.current.data?.version).toBe(1));
  fetchMock.mockResolvedValue({ ok: false, status: 403 });
  act(() => Stream.latest.emit('error', ''));
  await waitFor(() => expect(result.current.accessDenied).toBe(true));
  expect(result.current.data).toBeUndefined();
  expect(Stream.latest.close).toHaveBeenCalled();
  unmount();
  client.clear();
});

// Explicit revocation must remove the operation cache and prevent another read from restoring it.
test('removes cached authority on an explicit revocation event', async () => {
  const { result, unmount, client } = setup();
  await waitFor(() => expect(result.current.data?.version).toBe(1));
  act(() => Stream.latest.emit('authorization.revoked', '2'));
  await waitFor(() => expect(result.current.accessDenied).toBe(true));
  await waitFor(() => expect(client.getQueryData(['operations', 'one'])).toBeUndefined());
  expect(result.current.data).toBeUndefined();
  expect(Stream.latest.close).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledTimes(1);
  unmount();
  client.clear();
});

// Transport loss preserves the last rendered record, but a genuine masked
// denial removes the cached record once the controller responds.
test('preserves progress during outage and clears it on a genuine denied read', async () => {
  const { result, unmount, client } = setup();
  await waitFor(() => expect(result.current.data?.version).toBe(1));
  fetchMock.mockRejectedValue(new TypeError('No network'));
  act(() => Stream.latest.emit('error', ''));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data?.version).toBe(1);
  expect(result.current.accessDenied).toBe(false);

  fetchMock.mockResolvedValue({ ok: false, status: 404, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
    json: async () => ({ type: 'about:blank', title: 'Operation not found', status: 404, code: 'operation.not_found', requestId: 'fixture' }) });
  await act(async () => { await result.current.refetch(); });
  await waitFor(() => expect(result.current.accessDenied).toBe(true));
  expect(result.current.data).toBeUndefined();
  unmount();
  client.clear();
});
