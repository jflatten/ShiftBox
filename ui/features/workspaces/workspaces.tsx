'use client';

import { useState } from 'react';
import { Anchor, Button, Checkbox, Group, NativeSelect, Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { LaunchFlow } from './launch-flow';
import { WorkspaceDetail } from './workspace-detail';
import { WorkspaceStatus, WorkspaceTime } from './workspace-summary';

import { workspaceName, workspaceState, type Workspace } from './workspace';

export function Workspaces({ workspaceId, ownerId, launch = false }: {
  workspaceId: string | null;
  ownerId?: string | null;
  launch?: boolean;
}) {
  const session = useSession();
  const current = session.session?.mode === 'normal' && session.session.installationState === 'active' ? session.session : undefined;
  return (
    <ShiftBoxShell session={session.session} alerts={[]} error={session.error ?? undefined}>
      {!current ? (
        <>
          <Title order={1}>Workspaces</Title>
          {session.isPending ? <Text role="status">Checking your session…</Text> : null}
          {session.data === null ? <SignInChoices /> : null}
        </>
      ) : launch ? <LaunchFlow key={current.user.id} csrfToken={current.csrfToken} userID={current.user.id}
        admin={current.role === 'administrator'} />
        : workspaceId ? <WorkspaceDetail key={workspaceId} id={workspaceId} csrfToken={current.csrfToken}
            admin={current.role === 'administrator'} />
          : <WorkspaceList key={ownerId} ownerId={ownerId} />}
    </ShiftBoxShell>
  );
}

function WorkspaceList({ ownerId }: { ownerId?: string | null }) {
  const [state, setState] = useState('');
  const [showDeleted, setShowDeleted] = useState(false);
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const page = useQuery<{ items: Workspace[]; nextCursor: string | null }>({
    queryKey: ['workspaces', 'list', ownerId ?? '', state, showDeleted, cursor ?? ''],
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams();
      if (ownerId) params.set('owner', ownerId);
      if (state) params.set('state', state);
      if (!showDeleted) params.set('excludeDeleted', 'true');
      if (cursor) params.set('cursor', cursor);
      const suffix = params.size > 0 ? '?' + params : '';
      const response = await fetch('/api/v1/workspaces' + suffix, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = !page.isError ? page.data : undefined;
  const states = ['unknown', 'provisioning', 'stopped', 'starting', 'running', 'unreachable', 'stopping', 'deleting', 'failed',
    ...(showDeleted ? ['deleted'] : [])];
  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Title order={1}>Workspaces</Title>
          <Text c="dimmed" size="sm">{ownerId ? 'Workspaces for the selected user.' : 'Your development environments, resources, and lifecycle status.'}</Text>
        </Stack>
        {ownerId ? <Anchor href="/workspaces/">All workspaces</Anchor> :
          <Button component="a" href="/workspaces/?launch=1">Launch workspace</Button>}
      </Group>
      <Paper withBorder radius="md" p="md">
        <Stack gap="md">
          <Group justify="space-between" align="flex-end">
            <Group align="flex-end">
              <NativeSelect label="Observed state" value={state} data={[
                { value: '', label: 'All states' }, ...states.map(value => ({ value, label: workspaceState(value) })),
              ]} onChange={event => { setState(event.currentTarget.value); setCursors([]); }} />
              <Checkbox label="Show deleted workspaces" checked={showDeleted} mb={8} onChange={event => {
                setShowDeleted(event.currentTarget.checked);
                if (!event.currentTarget.checked && state === 'deleted') setState('');
                setCursors([]);
              }} />
              {state || showDeleted ? <Button variant="subtle" onClick={() => {
                setState(''); setShowDeleted(false); setCursors([]);
              }}>Clear filters</Button> : null}
            </Group>
            <Button variant="default" loading={page.isFetching} onClick={() => void page.refetch()}>Refresh</Button>
          </Group>
          <Text c="dimmed" size="xs">Observed state is the latest reported status. Desired state is the requested target.</Text>
          {page.isError ? <Stack align="flex-start">
            <APIProblemView error={page.error} />
            <Button variant="default" onClick={() => void page.refetch()}>Retry</Button>
          </Stack> : null}
          {page.isPending ? <Text role="status">Loading workspaces…</Text> : null}
          {data && data.items.length === 0 ? <Stack align="center" py="xl" gap="xs">
            <Text fw={600}>{cursor ? 'No workspaces on this page.' : state ? 'No workspaces match this filter.' : 'No workspaces yet.'}</Text>
            <Text size="sm" c="dimmed">{state ? 'Choose another state or clear the filters.' :
              cursor ? 'Return to the previous page to see more workspaces.' :
              showDeleted ? 'Launch a workspace from a project template to get started.' : 'Launch a workspace to get started, or show deleted workspaces to view history.'}</Text>
          </Stack> : null}
          {data && data.items.length > 0 ? (
            <Table.ScrollContainer minWidth={680}>
              <Table aria-label="Workspaces" highlightOnHover verticalSpacing="md">
                <Table.Thead><Table.Tr>
                  <Table.Th>Workspace</Table.Th><Table.Th>State</Table.Th><Table.Th>Resources</Table.Th>
                  <Table.Th>Last activity</Table.Th><Table.Th>Actions</Table.Th>
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {data.items.map(workspace => (
                    <Table.Tr key={workspace.id}>
                      <Table.Td style={{ maxWidth: 250, overflowWrap: 'anywhere' }}>
                        <Anchor fw={600} href={workspaceURL(workspace)}>{workspaceName(workspace)}</Anchor>
                        {workspace.currentOperationId ? <Text size="xs" c="dimmed">Operation in progress</Text> : null}
                      </Table.Td>
                      <Table.Td><Stack gap={4} align="flex-start">
                        <WorkspaceStatus value={workspace.observedState} />
                        <Text size="xs" c="dimmed">Desired: {workspaceState(workspace.desiredState)}</Text>
                      </Stack></Table.Td>
                      <Table.Td><Stack gap={2}>
                        <Text size="sm">{workspace.resources.cpuCount} vCPU · {workspace.resources.memoryMiB.toLocaleString()} MiB memory</Text>
                        <Text size="xs" c="dimmed">{workspace.resources.diskMiB.toLocaleString()} MiB disk</Text>
                      </Stack></Table.Td>
                      <Table.Td><Text size="sm"><WorkspaceTime value={workspace.lastActivityAt} fallback="Not recorded" /></Text></Table.Td>
                      <Table.Td><Button component="a" href={workspaceURL(workspace)} variant="default" size="compact-sm"
                        aria-label={`View ${workspaceName(workspace)}`}>View workspace</Button></Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          ) : null}
          <Group justify="space-between">
            <Text size="sm" c="dimmed" role="status">{data ? `${data.items.length} ${data.items.length === 1 ? 'workspace' : 'workspaces'} on this page` : 'Workspace list unavailable'}</Text>
            <Group gap="xs">
              <Button variant="default" disabled={!cursor || page.isFetching} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
              <Text size="sm">Page {cursors.length + 1}</Text>
              <Button variant="default" disabled={!data?.nextCursor || page.isFetching} onClick={() => {
                const next = data?.nextCursor;
                if (next) setCursors(current => [...current, next]);
              }}>Next page</Button>
            </Group>
          </Group>
        </Stack>
      </Paper>
    </Stack>
  );
}

function workspaceURL(workspace: Workspace) {
  return `/workspaces/?id=${encodeURIComponent(workspace.id)}`;
}
