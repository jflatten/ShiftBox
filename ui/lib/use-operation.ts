'use client';

import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { WorkflowOperation } from '../components/workflow-stepper';
import { subscribeOperation } from './operation-events';
import { readProblem } from './api-problem';

type Operation = WorkflowOperation & { version: number };

class OperationReadError extends Error {
  constructor(readonly status: number) { super('Unable to refresh workflow progress.'); }
}

/** useOperation keeps workflow snapshots in memory and revalidates after SSE changes. */
export function useOperation(operationId: string) {
  const client = useQueryClient();
  const [revokedId, setRevokedId] = useState<string | null>(null);
  const query = useQuery<Operation>({
    queryKey: ['operations', operationId],
    enabled: revokedId !== operationId,
    gcTime: 0,
    retry: false,
    queryFn: async ({ signal }) => {
      const response = await fetch(`/api/v1/operations/${encodeURIComponent(operationId)}`, { signal, credentials: 'same-origin', cache: 'no-store' });
      if (response.status === 401) await readProblem(response);
      if (!response.ok) throw new OperationReadError(response.status);
      return response.json();
    },
  });
  const accessDenied = revokedId === operationId || (query.error instanceof OperationReadError && (query.error.status === 401 || query.error.status === 403));
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

  // Never display cached progress as current authority after a failed refresh.
  return { ...query, data: accessDenied || query.isError ? undefined : query.data, accessDenied };
}
