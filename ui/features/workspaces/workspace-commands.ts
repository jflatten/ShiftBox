import { useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ControllerUnavailableError, readControllerJSON, readProblem } from '../../lib/api-problem';
import { controllerPollInterval, controllerReadOptions } from '../../lib/query-client';
import type { Workspace } from './workspace';

export type WorkspaceAction = 'start' | 'stop' | 'rebuild' | 'repair' | 'retry' | 'delete' | 'update';
export type WorkspaceService = 'ide' | 'repair' | `preview-${number}`;

export function useWorkspaceDetail(id: string, enabled = true) {
  return useQuery<Workspace>({
    queryKey: ['workspaces', 'detail', id],
    ...controllerReadOptions,
    enabled,
    refetchInterval: query => {
      const workspace = query.state.data;
      if (query.state.error instanceof ControllerUnavailableError) return controllerPollInterval;
      return workspace?.desiredState === 'deleted' && workspace.observedState === 'deleted' ? false : controllerPollInterval;
    },
    queryFn: ({ signal }) => readControllerJSON<Workspace>('/api/v1/workspaces/' + encodeURIComponent(id), { signal, cache: 'no-store' }),
  });
}

export function availableWorkspaceCommands(workspace: Workspace | undefined) {
  const lifecycle: 'start' | 'stop' | undefined = workspace?.desiredState === 'running' && workspace.observedState === 'running' ? 'stop' :
    workspace?.desiredState === 'stopped' && workspace.observedState === 'stopped' ? 'start' : undefined;
  const maintenance = workspace?.maintenance;
  return {
    lifecycle,
    rebuild: lifecycle === 'stop' && !workspace?.canOpenRepair && !workspace?.canRetryAfterRepair,
    update: !!lifecycle && !maintenance?.operationId && (maintenance?.state === 'pending' || maintenance?.state === 'waiting_safe'),
    delete: !!workspace && workspace.desiredState !== 'deleted' && workspace.observedState !== 'deleted',
  };
}

export function useWorkspaceCommand(workspace: Workspace | undefined, csrfToken: string, refresh: () => Promise<unknown>,
  onAccepted: (action: WorkspaceAction, operationID: string, trigger: Element | null) => void,
) {
  const [submitting, setSubmitting] = useState<WorkspaceAction>();
  const [error, setError] = useState<unknown>();
  const request = useRef<{ action: WorkspaceAction; version: number; key: string }>(undefined);

  async function run(action: WorkspaceAction) {
    if (!workspace || submitting) return;
    const trigger = document.activeElement;
    setSubmitting(action);
    setError(undefined);
    try {
      if (request.current?.action !== action || request.current.version !== workspace.version) {
        request.current = { action, version: workspace.version, key: crypto.randomUUID() };
      }
      const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(workspace.id)}/actions/${action}`, {
        method: 'POST',
        headers: {
          'X-CSRF-Token': csrfToken, 'If-Match': `"${workspace.version}"`, 'Idempotency-Key': request.current.key,
          ...(action === 'delete' ? { 'Content-Type': 'application/json' } : {}),
        },
        body: action === 'delete' ? JSON.stringify({ confirmDataLoss: true }) : undefined,
      });
      if (!response.ok) {
        if (response.status === 412) void refresh();
        throw await readProblem(response);
      }
      const accepted: { id: string } = await response.json();
      request.current = undefined;
      onAccepted(action, accepted.id, trigger);
      await refresh();
    } catch (error) {
      setError(error);
    } finally {
      setSubmitting(undefined);
    }
  }

  return { run, submitting, error, setError };
}

export async function openWorkspaceEntry(id: string, csrfToken: string, service: WorkspaceService) {
  // Reserve the tab during the click, before waiting for the grant.
  const tab = window.open('about:blank', '_blank');
  if (!tab) return false;
  try {
    tab.opener = null;
    const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(id)}/entry-grants`, {
      method: 'POST', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      body: JSON.stringify({ service }),
    });
    if (!response.ok) throw await readProblem(response);
    const { exchangeUrl } = await response.json();
    const url = new URL(exchangeUrl);
    if (url.protocol !== 'https:' || url.origin === window.location.origin || url.pathname !== '/_shiftbox/entry' ||
        url.username || url.password || !url.searchParams.get('grant')) {
      throw new Error('Invalid workspace entry URL');
    }
    if (!tab.closed) {
      const entry = tab.document.createElement('a');
      entry.href = url.href;
      entry.rel = 'noreferrer';
      entry.click();
    }
    return true;
  } catch (error) {
    tab.close();
    throw error;
  }
}
