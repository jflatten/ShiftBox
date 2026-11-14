'use client';

import { useState } from 'react';
import { Anchor, Button, Group, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { LaunchFlow } from './launch-flow';
import { WorkspaceDetail } from './workspace-detail';

import { workspaceState, type Workspace } from './workspace';

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
      ) : launch ? <LaunchFlow csrfToken={current.csrfToken} />
        : workspaceId ? <WorkspaceDetail key={workspaceId} id={workspaceId} csrfToken={current.csrfToken} />
          : <WorkspaceList key={ownerId} ownerId={ownerId} />}
    </ShiftBoxShell>
  );
}

function WorkspaceList({ ownerId }: { ownerId?: string | null }) {
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const page = useQuery<{ items: Workspace[]; nextCursor: string | null }>({
    queryKey: ['workspaces', 'list', ownerId ?? '', cursor ?? ''],
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams();
      if (ownerId) params.set('owner', ownerId);
      if (cursor) params.set('cursor', cursor);
      const suffix = params.size > 0 ? '?' + params : '';
      const response = await fetch('/api/v1/workspaces' + suffix, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = !page.isError ? page.data : undefined;
  return (
    <>
      <Group justify="space-between">
        <Title order={1}>Workspaces</Title>
        {ownerId ? <Anchor href="/workspaces/">All workspaces</Anchor> :
          <Button component="a" href="/workspaces/?launch=1">Launch workspace</Button>}
      </Group>
      {page.isError ? <APIProblemView error={page.error} /> : null}
      {page.isPending ? <Text role="status">Loading workspaces…</Text> : null}
      {data && data.items.length === 0 ? <Text>{cursor ? 'No workspaces on this page.' : 'No workspaces yet.'}</Text> : null}
      {data && data.items.length > 0 ? (
        <Table.ScrollContainer minWidth={440}>
          <Table aria-label="Workspaces">
            <Table.Thead><Table.Tr>
              <Table.Th>Workspace</Table.Th><Table.Th>Desired state</Table.Th><Table.Th>Observed state</Table.Th>
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {data.items.map(workspace => (
                <Table.Tr key={workspace.id}>
                  <Table.Td><Anchor href={`/workspaces/?id=${encodeURIComponent(workspace.id)}`}>{workspace.id}</Anchor></Table.Td>
                  <Table.Td>{workspaceState(workspace.desiredState)}</Table.Td>
                  <Table.Td>{workspaceState(workspace.observedState)}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      ) : null}
      {cursor || data?.nextCursor ? (
        <Group>
          <Button variant="default" disabled={!cursor} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
          <Button variant="default" disabled={!data?.nextCursor} onClick={() => {
            const next = data?.nextCursor;
            if (next) setCursors(current => [...current, next]);
          }}>Next page</Button>
        </Group>
      ) : null}
    </>
  );
}
