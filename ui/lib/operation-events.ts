/** Subscribe to invalidation signals; operation reads remain the source of truth.
 * Refetching for every update also handles sequence gaps without applying partial
 * or stale event snapshots. Native EventSource retains Last-Event-ID on reconnect.
 */
export function subscribeOperation(operationId: string, refresh: () => void, revoked: () => void): () => void {
  const source = new EventSource(`/api/v1/operations/${encodeURIComponent(operationId)}/events`);
  let stopped = false;
  const changed = () => { if (!stopped) refresh(); };
  const denied = () => {
    if (stopped) return;
    stop();
    revoked();
  };
  const listeners: [string, EventListener][] = [
    ['operation.updated', changed], ['step.updated', changed],
    ['open', changed], ['error', changed], ['authorization.revoked', denied],
  ];
  function stop() {
    if (stopped) return;
    stopped = true;
    for (const [type, listener] of listeners) source.removeEventListener(type, listener);
    source.close();
  }
  for (const [type, listener] of listeners) source.addEventListener(type, listener);
  return stop;
}
