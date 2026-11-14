import { useState } from 'react';
import { Anchor, Button, Group, Modal, Stack, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';
import { workspaceState } from '../workspaces/workspace';

interface StopOperation {
  workspaceId: string;
  operationId: string;
}

export function Offboarding({ person, csrfToken, onClose, refresh }: {
  person: { id: string; primaryEmail: string; version: number };
  csrfToken: string;
  onClose: () => void;
  refresh: () => Promise<unknown>;
}) {
  const [stops, setStops] = useState<StopOperation[]>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const stale = error instanceof APIProblemError && error.problem.status === 412;
  const closurePending = error instanceof APIProblemError && error.problem.code === 'user.connection_closure_pending';
  const workspaces = useQuery<{ id: string; observedState: string }[]>({
    queryKey: ['offboarding-workspaces', person.id, person.version],
    enabled: stops === undefined,
    queryFn: async ({ signal }) => {
      const items: { id: string; observedState: string }[] = [];
      let cursor: string | null = null;
      do {
        const params = new URLSearchParams({ owner: person.id });
        if (cursor) params.set('cursor', cursor);
        const response = await fetch('/api/v1/workspaces?' + params, { signal, cache: 'no-store' });
        if (!response.ok) throw await readProblem(response);
        const page: { items: typeof items; nextCursor: string | null } = await response.json();
        items.push(...page.items);
        cursor = page.nextCursor;
      } while (cursor);
      return items.filter(item => item.observedState !== 'deleted');
    },
  });

  const disable = async () => {
    if (saving || stale || closurePending || !workspaces.isSuccess || stops !== undefined) return;
    setSaving(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/admin/users/${encodeURIComponent(person.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${person.version}"` },
        body: '{"membershipState":"disabled"}',
      });
      if (!response.ok) {
        const problem = await readProblem(response);
        if (response.status === 412 || problem.problem.code === 'user.connection_closure_pending') await refresh();
        throw problem;
      }
      const result: { stopOperations: StopOperation[] } = await response.json();
      setStops(result.stopOperations);
      await refresh();
    } catch (error) {
      setError(error);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal opened returnFocus={false} onClose={saving ? () => {} : onClose} closeOnEscape={!saving} closeOnClickOutside={!saving}
      withCloseButton={!saving} title={stops ? 'Account disabled' : 'Disable account'}>
      <Stack>
        <Text fw={600}>{person.primaryEmail}</Text>
        {stops === undefined ? (
          <>
            <Text>ShiftBox will revoke all sign-in sessions and close active connections. It will request a stop for affected workspaces. Workspace files are preserved.</Text>
            {workspaces.isPending ? <Text role="status">Checking owned workspaces…</Text> : null}
            {workspaces.isError ? <>
              <APIProblemView error={workspaces.error} />
              <Button variant="default" onClick={() => void workspaces.refetch()}>Retry workspace list</Button>
            </> : null}
            {workspaces.data?.length ? <Stack gap="xs">
              <Title order={3}>Owned workspaces</Title>
              {workspaces.data.map(workspace => <Text size="sm" key={workspace.id}>
                <Text component="span">{workspace.id}</Text> · {workspaceState(workspace.observedState)}
              </Text>)}
            </Stack> : null}
          </>
        ) : (
          <>
            <Text role="status">Sign-in sessions and tracked connections closed.</Text>
            {stops.length ? stops.map(stop => <StopProgress key={stop.operationId} stop={stop} />) :
              <Text>No workspace stops were needed.</Text>}
          </>
        )}
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Group justify="flex-end">
          <Button variant="default" data-autofocus onClick={onClose} disabled={saving}>{stops ? 'Close' : 'Cancel'}</Button>
          {stops === undefined ? <Button color="red" loading={saving}
            disabled={!workspaces.isSuccess || stale || closurePending} onClick={() => void disable()}>Confirm disable</Button> : null}
        </Group>
      </Stack>
    </Modal>
  );
}

function StopProgress({ stop }: { stop: StopOperation }) {
  const operation = useOperation(stop.operationId);
  return (
    <Stack gap="xs">
      <Anchor href={`/workspaces/?id=${encodeURIComponent(stop.workspaceId)}`}>{stop.workspaceId}</Anchor>
      {operation.isPending ? <Text role="status">Loading stop progress…</Text> : null}
      {operation.accessDenied ? <Text role="alert">Workspace stop progress is unavailable.</Text> : null}
      {operation.isError && !operation.accessDenied ? <APIProblemView error={operation.error} /> : null}
      {operation.data ? <WorkflowStepper operation={operation.data} /> : null}
    </Stack>
  );
}
