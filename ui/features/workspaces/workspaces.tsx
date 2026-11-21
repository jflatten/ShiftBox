'use client';

import { useState } from 'react';
import { Anchor, Button, Checkbox, Group, NativeSelect, Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { useControllerUnavailable } from '../../app/providers';
import { ShiftBoxShell } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { ControllerUnavailableError, readControllerJSON } from '../../lib/api-problem';
import { controllerPollInterval, controllerReadOptions } from '../../lib/query-client';
import { SignInChoices } from '../session/sign-in';
import { ProjectCatalog } from '../templates/project-catalog';
import { useProjectCatalog, type TemplateRevision } from '../templates/use-templates';
import { useSession } from '../../lib/use-session';
import { LaunchFlow } from './launch-flow';
import { WorkspaceDetail } from './workspace-detail';
import { WorkspaceInventoryRow } from './workspace-inventory-row';

import { workspaceMilestones, workspaceState, type Workspace } from './workspace';

export function Workspaces({ workspaceId, ownerId, launch = false }: {
  workspaceId: string | null;
  ownerId?: string | null;
  launch?: boolean;
}) {
  const session = useSession();
  const unavailable = useControllerUnavailable();
  const visibleSession = session.session ?? session.cachedSession;
  const current = visibleSession?.mode === 'normal' && visibleSession.installationState === 'active' ? visibleSession : undefined;
  const sessionUnavailable = session.error instanceof ControllerUnavailableError;
  return (
    <ShiftBoxShell session={unavailable ? visibleSession : session.session} alerts={[]} error={sessionUnavailable ? undefined : session.error ?? undefined}>
      {!current ? (
        <>
          <Title order={1}>Workspaces</Title>
          {session.isPending ? <Text role="status">Checking your session…</Text> : null}
          {session.data === null ? <SignInChoices /> : null}
        </>
      ) : (
        <div style={{ display: 'contents' }} inert={sessionUnavailable && !unavailable}>
          {launch ? <LaunchFlow key={current.user.id} csrfToken={current.csrfToken}>
            {onReview => <WorkspaceLaunchPicker userID={current.user.id} admin={current.role === 'administrator'} onReview={onReview} />}
          </LaunchFlow>
            : workspaceId ? <WorkspaceDetail key={workspaceId} id={workspaceId} userId={current.user.id} csrfToken={current.csrfToken}
                admin={current.role === 'administrator'} />
              : <WorkspaceList key={ownerId} ownerId={ownerId} csrfToken={current.csrfToken} />}
        </div>
      )}
    </ShiftBoxShell>
  );
}

function WorkspaceLaunchPicker({ userID, admin, onReview }: {
  userID: string; admin: boolean; onReview: (project: TemplateRevision) => void;
}) {
  const catalog = useProjectCatalog(userID, admin);
  return <>
    <Anchor href="/workspaces/">All workspaces</Anchor>
    <Title order={1}>Launch workspace</Title>
    <ProjectCatalog catalog={catalog} onProjectAction={onReview} />
  </>;
}

function WorkspaceList({ ownerId, csrfToken }: { ownerId?: string | null; csrfToken: string }) {
  const unavailable = useControllerUnavailable();
  const [state, setState] = useState('');
  const [showDeleted, setShowDeleted] = useState(false);
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const page = useQuery<{ items: Workspace[]; nextCursor: string | null }>({
    queryKey: ['workspaces', 'list', ownerId ?? '', state, showDeleted, cursor ?? ''],
    ...controllerReadOptions,
    refetchInterval: controllerPollInterval,
    queryFn: ({ signal }) => {
      const params = new URLSearchParams();
      if (ownerId) params.set('owner', ownerId);
      if (state) params.set('lifecycleState', state);
      if (!showDeleted) params.set('excludeDeleted', 'true');
      if (cursor) params.set('cursor', cursor);
      const suffix = params.size > 0 ? '?' + params : '';
      return readControllerJSON('/api/v1/workspaces' + suffix, { signal, cache: 'no-store' });
    },
  });
  const readUnavailable = page.error instanceof ControllerUnavailableError;
  const data = !page.isError || readUnavailable ? page.data : undefined;
  const states = showDeleted ? [...workspaceMilestones, 'deleted'] : workspaceMilestones;
  return (
    <Stack gap="lg" inert={readUnavailable && !unavailable}>
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Title order={1}>Workspaces</Title>
          <Text c="dimmed" size="sm">{ownerId ? 'Workspaces for the selected user.' : 'Your development environments, resources, and lifecycle status.'}</Text>
        </Stack>
        {ownerId ? <Anchor href="/workspaces/">All workspaces</Anchor> : <Anchor href="/templates/" fw={600}>
          <Group component="span" gap="xs" wrap="nowrap">
            Launch Project
            <svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
              <path d="M4 10h11m-5-5 5 5-5 5" />
            </svg>
          </Group>
        </Anchor>}
      </Group>
      <Paper withBorder radius="md" p="md">
        <Stack gap="md">
          <Group justify="space-between" align="flex-end">
            <Group align="flex-end">
              <NativeSelect label="Current state" value={state} data={[
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
            <Button variant="default" onClick={() => void page.refetch()}>Refresh</Button>
          </Group>
          {page.isError && !readUnavailable ? <Stack align="flex-start">
            <APIProblemView error={page.error} />
            <Button variant="default" onClick={() => void page.refetch()}>Retry</Button>
          </Stack> : null}
          {page.isPending ? <Text role="status">Loading workspaces…</Text> : null}
          {data && data.items.length === 0 ? <Stack align="center" py="xl" gap="xs">
            <Text fw={600}>{cursor ? 'No workspaces on this page.' : state ? 'No workspaces match this filter.' : 'No workspaces yet.'}</Text>
            <Text size="sm" c="dimmed">{state ? 'Choose another state or clear the filters.' :
              cursor ? 'Return to the previous page to see more workspaces.' :
              showDeleted ? 'Launch a workspace from a project to get started.' : 'Launch a workspace to get started, or show deleted workspaces to view history.'}</Text>
          </Stack> : null}
          {data && data.items.length > 0 ? (
            <Table.ScrollContainer minWidth={680}>
              <Table aria-label="Workspaces" highlightOnHover verticalSpacing="md">
                <Table.Thead><Table.Tr>
                  <Table.Th>Workspace</Table.Th><Table.Th>State</Table.Th><Table.Th>Resources</Table.Th>
                  <Table.Th>Last activity</Table.Th><Table.Th>Services</Table.Th><Table.Th>Actions</Table.Th>
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {data.items.map(workspace => (
                    <WorkspaceInventoryRow key={workspace.id} row={workspace} href={workspaceURL(workspace)} csrfToken={csrfToken} />
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          ) : null}
          <Group justify="space-between">
            <Text size="sm" c="dimmed" role="status">{data ? `${data.items.length} ${data.items.length === 1 ? 'workspace' : 'workspaces'} on this page` : 'Workspace list unavailable'}</Text>
            <Group gap="xs">
              <Button variant="default" disabled={!cursor || page.isPending} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
              <Text size="sm">Page {cursors.length + 1}</Text>
              <Button variant="default" disabled={!data?.nextCursor} onClick={() => {
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
