import { useEffect, useRef, useState } from 'react';
import { Anchor, Button, Group, Stack, Text, Title } from '@mantine/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';
import { workspaceState, type Workspace } from './workspace';

export function WorkspaceDetail({ id, csrfToken }: { id: string; csrfToken: string }) {
  const workspace = useQuery<Workspace>({
    queryKey: ['workspaces', 'detail', id],
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/workspaces/' + encodeURIComponent(id), { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = !workspace.isError ? workspace.data : undefined;
  const [operationId, setOperationId] = useState<string>();
  const [operationBusy, setOperationBusy] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<unknown>();
  const request = useRef<{ action: 'start' | 'stop'; version: number; key: string }>(undefined);
  const currentOperation = data?.currentOperationId ?? operationId;
  const action = data?.desiredState === 'running' && data.observedState === 'running' ? 'stop' :
    data?.desiredState === 'stopped' && data.observedState === 'stopped' ? 'start' : undefined;

  const requestPower = async (action: 'start' | 'stop') => {
    if (!data || submitting) return;
    setSubmitting(true);
    setError(undefined);
    try {
      if (request.current?.action !== action || request.current.version !== data.version) {
        request.current = { action, version: data.version, key: crypto.randomUUID() };
      }
      const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(id)}/actions/${action}`, {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrfToken, 'If-Match': `"${data.version}"`, 'Idempotency-Key': request.current.key },
      });
      if (!response.ok) {
        if (response.status === 412) void workspace.refetch();
        throw await readProblem(response);
      }
      const accepted: { id: string } = await response.json();
      setOperationBusy(true);
      setOperationId(accepted.id);
      request.current = undefined;
      await workspace.refetch();
    } catch (error) {
      setError(error);
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <>
      <Anchor href="/workspaces/">All workspaces</Anchor>
      <Title order={1}>Workspace</Title>
      {workspace.isError ? <APIProblemView error={workspace.error} /> : null}
      {workspace.isPending ? <Text role="status">Loading workspace…</Text> : null}
      {data ? (
        <Stack gap="lg">
          <Text size="sm" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{data.id}</Text>
          <Group gap="xl" align="flex-start">
            <Stack gap={2}><Text size="sm" c="dimmed">Desired state</Text><Text fw={600}>{workspaceState(data.desiredState)}</Text></Stack>
            <Stack gap={2}><Text size="sm" c="dimmed">Observed state</Text><Text fw={600}>{workspaceState(data.observedState)}</Text></Stack>
          </Group>
          <Text>{data.resources.cpuCount} vCPU · {data.resources.memoryMiB} MiB memory · {data.resources.diskMiB} MiB disk</Text>
          <Stack component="dl" gap="xs" m={0}>
            <div>
              <Text component="dt" size="sm" c="dimmed">Template</Text>
              <Text component="dd" m={0}>{data.templateId} · Revision {data.templateRevision}</Text>
            </div>
            <div>
              <Text component="dt" size="sm" c="dimmed">Platform release</Text>
              <Text component="dd" m={0}>{data.platformRelease}</Text>
            </div>
            <div>
              <Text component="dt" size="sm" c="dimmed">Last activity</Text>
              <Text component="dd" m={0}><WorkspaceTime value={data.lastActivityAt} fallback="No activity recorded" /></Text>
            </div>
            <div>
              <Text component="dt" size="sm" c="dimmed">Retention deadline</Text>
              <Text component="dd" m={0}><WorkspaceTime value={data.retentionDeadline} fallback="No deadline scheduled" /></Text>
            </div>
          </Stack>
          <Text size="sm">Stop and start preserve project files, the developer home, and configured persistent mounts.</Text>
          {error !== undefined ? <APIProblemView error={error} /> : null}
          {data.canOpenIde ? <OpenIDE id={id} csrfToken={csrfToken} /> : null}
          {action ? (
            <Button style={{ alignSelf: 'flex-start' }} loading={submitting}
              disabled={!!data.currentOperationId || operationBusy} onClick={() => void requestPower(action)}>
              {action === 'start' ? 'Start workspace' : 'Stop workspace'}
            </Button>
          ) : null}
          {currentOperation ? <WorkspaceProgress key={currentOperation} workspaceId={id} operationId={currentOperation} onFinished={setOperationBusy} /> : null}
        </Stack>
      ) : null}
    </>
  );
}

function WorkspaceTime({ value, fallback }: { value: string | null; fallback: string }) {
  if (!value) return fallback;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return fallback;
  return <time dateTime={value}>{date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}</time>;
}

function OpenIDE({ id, csrfToken }: { id: string; csrfToken: string }) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<unknown>();

  const open = async () => {
    if (submitting) return;
    setSubmitting(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(id)}/entry-grants`, {
        method: 'POST', cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ service: 'ide' }),
      });
      if (!response.ok) throw await readProblem(response);
      const { exchangeUrl } = await response.json();
      const url = new URL(exchangeUrl);
      if (url.protocol !== 'https:' || url.origin === window.location.origin || url.pathname !== '/_shiftbox/entry' ||
          url.username || url.password || !url.searchParams.get('grant')) {
        throw new Error('Invalid IDE entry URL');
      }
      const entry = document.createElement('a');
      entry.href = url.href;
      entry.rel = 'noreferrer';
      entry.click();
    } catch (error) {
      setError(error);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Stack align="flex-start" gap="xs">
      <Button loading={submitting} onClick={() => void open()}>Open IDE</Button>
      {error !== undefined ? <APIProblemView error={error} /> : null}
    </Stack>
  );
}

function WorkspaceProgress({ workspaceId, operationId, onFinished }: { workspaceId: string; operationId: string; onFinished: (busy: boolean) => void }) {
  const operation = useOperation(operationId);
  const client = useQueryClient();
  const state = operation.data?.status;
  const ideVerified = operation.data?.steps.some(step => step.key === 'verify_ide' && step.status === 'succeeded');
  useEffect(() => {
    const finished = state === 'succeeded' || state === 'failed' || state === 'cancelled';
    if (finished) onFinished(false);
    if (finished || ideVerified) void client.invalidateQueries({ queryKey: ['workspaces', 'detail', workspaceId] });
  }, [client, ideVerified, onFinished, state, workspaceId]);
  if (operation.accessDenied) return <Text role="alert">You no longer have access to this operation.</Text>;
  if (operation.isError) return <APIProblemView error={operation.error} />;
  if (!operation.data) return <Text role="status">Loading operation…</Text>;
  return <WorkflowStepper operation={operation.data} />;
}
