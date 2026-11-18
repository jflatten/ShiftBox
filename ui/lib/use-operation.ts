'use client';

import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { WorkflowOperation } from '../components/workflow-stepper';
import { subscribeOperation } from './operation-events';
import { APIProblemError, ControllerUnavailableError, readControllerJSON } from './api-problem';
import { controllerPollInterval, controllerReadOptions } from './query-client';

type Operation = WorkflowOperation & { type: string; version: number };

/** useOperation keeps workflow snapshots in memory and revalidates after SSE changes. */
export function useOperation(operationId: string) {
  const client = useQueryClient();
  const [revokedId, setRevokedId] = useState<string | null>(null);
  const query = useQuery<Operation>({
    queryKey: ['operations', operationId],
    ...controllerReadOptions,
    enabled: revokedId !== operationId,
    gcTime: 0,
    retry: false,
    refetchInterval: query => query.state.error instanceof ControllerUnavailableError ? controllerPollInterval : false,
    queryFn: ({ signal }) => readControllerJSON(`/api/v1/operations/${encodeURIComponent(operationId)}`,
      { signal, credentials: 'same-origin', cache: 'no-store' }),
  });
  const accessDenied = revokedId === operationId ||
    (query.error instanceof APIProblemError && [401, 403, 404].includes(query.error.problem.status));
  const status = query.data?.status;
  const listening = !accessDenied && status !== undefined && !['succeeded', 'failed', 'cancelled'].includes(status);

  useEffect(() => {
    if (!listening) return;
    const queryKey = ['operations', operationId];
    return subscribeOperation(operationId, () => {
      // Invalidation cancels an older in-flight read before fetching fresh state.
      void client.invalidateQueries({ queryKey, exact: true });
    }, () => {
      setRevokedId(operationId);
      void client.invalidateQueries({ queryKey: ['session'], exact: true });
      void client.cancelQueries({ queryKey, exact: true }).then(() => {
        client.removeQueries({ queryKey, exact: true });
      });
    });
  }, [client, operationId, listening]);

  const unavailable = query.error instanceof ControllerUnavailableError;
  // Only a transport outage can leave previously authorized progress visible.
  return { ...query, data: accessDenied || (query.isError && !unavailable) ? undefined : query.data, accessDenied };
}
