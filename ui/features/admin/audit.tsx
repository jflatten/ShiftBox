'use client';

import { Fragment, useRef, useState } from 'react';
import { Accordion, Badge, Button, Code, Group, NativeSelect, Paper, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell, type ShellSession } from '../../components/app-shell';
import { readProblem } from '../../lib/api-problem';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';

const auditPath = '/api/v1/admin/audit';
interface AuditEvent {
  id: string; occurredAt: string; actorType: string; actorId?: string;
  action: string; resourceType: string; resourceId?: string; outcome: string; reasonCode?: string;
}
interface Person { id: string; displayName?: string | null; primaryEmail?: string }
const words = (value: string) => value.replace(/[._-]+/g, ' ')
  .replace(/\b(?:project )?templates?\b/g, term => term.endsWith('s') ? 'projects' : 'project')
  .replace(/^./, letter => letter.toUpperCase());
const actionLabels: Record<string, string> = {
  'session.change_password': 'Change password', 'session.external_reauthorize': 'Renew external authorization',
  'user.create': 'Create user', 'user.approve': 'Approve user', 'user.disable': 'Disable user',
  'user.change_role': 'Change user role', 'user.reset_password': 'Reset user password',
  'workspace.launch': 'Launch workspace', 'workspace.start': 'Start workspace', 'workspace.stop': 'Stop workspace',
  'workspace.delete': 'Delete workspace', 'workspace.rebuild': 'Rebuild workspace', 'workspace.repair': 'Repair workspace',
  'template.create': 'Create project', 'template.archive': 'Archive project',
};
const actionLabel = (action: string) => actionLabels[action] ?? words(action);
const outcomeColor = (outcome: string) => ['denied', 'failed', 'rejected'].includes(outcome) ? 'red'
  : ['pending', 'started'].includes(outcome) ? 'yellow' : outcome === 'allowed' ? 'teal' : 'gray';

export function Audit() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.role === 'administrator' && current.mode === 'normal' &&
    current.installationState === 'active' && current.permissions.includes('audit.list');
  return <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
    <Stack gap={4}>
      <Title order={1}>Audit</Title>
      <Text c="dimmed">Review security decisions and administrative activity across ShiftBox.</Text>
    </Stack>
    {session.isPending ? <Text role="status">Checking your session…</Text> : null}
    {session.data === null ? <SignInChoices /> : null}
    {current && !allowed ? <Text>Administrator access required.</Text> : null}
    {allowed ? <AuditHistory key={current.user.id} userId={current.user.id} session={current} /> : null}
  </ShiftBoxShell>;
}

function AuditHistory({ userId, session }: { userId: string; session: ShellSession }) {
  const [pages, setPages] = useState([auditPath]);
  const [action, setAction] = useState('');
  const [resource, setResource] = useState('');
  const [errors, setErrors] = useState<{ action?: string; resource?: string }>({});
  const [search, setSearch] = useState('');
  const [outcome, setOutcome] = useState('');
  const [expanded, setExpanded] = useState<string>();
  const heading = useRef<HTMLHeadingElement>(null);
  const path = pages.at(-1)!;
  const page = useQuery<{ items: AuditEvent[]; next?: string }>({
    queryKey: ['audit', userId, path],
    queryFn: async ({ signal }) => {
      const response = await fetch(path, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      const link = response.headers.get('Link') ?? '';
      // Only follow a relative link to this endpoint; do not send authority elsewhere.
      const next = /^<(\/api\/v1\/admin\/audit\?[^<>]+)>; rel="next"$/.exec(link)?.[1];
      return { items: await response.json(), next };
    },
  });
  const people = useQuery<Person[]>({
    queryKey: ['people', userId], enabled: session.permissions.includes('user.list'),
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/admin/users', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  // Names describe current accounts, while the original identifiers remain in event details.
  const names = new Map((people.isError ? [] : people.data ?? []).map(person =>
    [person.id, person.displayName || person.primaryEmail || 'User (name unavailable)']));
  names.set(userId, session.user?.displayName || session.user?.primaryEmail || 'You');
  const actorName = (event: AuditEvent) => event.actorType === 'user'
    ? names.get(event.actorId ?? '') ?? 'User (name unavailable)' : words(event.actorType);
  const resourceName = (event: AuditEvent) => event.resourceType === 'user'
    ? names.get(event.resourceId ?? '') ?? 'User account' : words(event.resourceType);
  const data = !page.isError ? page.data : undefined;
  const query = search.trim().toLocaleLowerCase();
  const rows = data?.items.filter(event => (!outcome || event.outcome === outcome) && (!query || [
    actionLabel(event.action), event.action, actorName(event), resourceName(event), event.reasonCode ?? '',
    event.actorId ?? '', event.resourceId ?? '', words(event.outcome),
  ].join(' ').toLocaleLowerCase().includes(query))) ?? [];
  const filteredHistory = pages[0] !== auditPath;
  const filteredPage = !!search || !!outcome;

  function applyFilters(nextAction: string, nextResource: string) {
    const validation = {
      action: nextAction && !/^[a-z][a-z0-9_.-]{0,127}$/.test(nextAction) ? 'Enter an action code from event details, such as user.approve.' : undefined,
      resource: nextResource && !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(nextResource)
        ? 'Enter a valid resource ID from event details.' : undefined,
    };
    setErrors(validation);
    if (validation.action || validation.resource) return;
    const params = new URLSearchParams();
    if (nextAction) params.set('action', nextAction);
    if (nextResource) params.set('resourceId', nextResource);
    const nextPath = auditPath + (params.size ? '?' + params : '');
    if (pages.length === 1 && path === nextPath) void page.refetch();
    setPages([nextPath]); setExpanded(undefined); setSearch(''); setOutcome('');
  }
  function clearHistory() { setAction(''); setResource(''); applyFilters('', ''); }
  const active = new URLSearchParams(pages[0].split('?')[1]);

  return <Stack gap="md">
    <Paper withBorder radius="md" p="md">
      <Stack gap="md">
        <Group justify="space-between">
          <Stack gap={2}>
            <Title order={2} size="lg" ref={heading} tabIndex={-1}>Event history</Title>
            <Text size="sm" c="dimmed">Newest recorded first · Up to 50 events per page · Times shown in your local time zone</Text>
          </Stack>
          <Button variant="default" loading={page.isFetching} onClick={() => void page.refetch()}>Refresh</Button>
        </Group>
        <Accordion variant="contained">
          <Accordion.Item value="filters">
            <Accordion.Control>Filter full history</Accordion.Control>
            <Accordion.Panel>
              <form onSubmit={event => { event.preventDefault(); applyFilters(action.trim(), resource.trim()); }}>
                <Stack gap="sm">
                  <Text size="sm" c="dimmed">Match an exact action or resource across all pages. Find these values in event details.</Text>
                  <SimpleGrid cols={{ base: 1, sm: 2 }}>
                    <TextInput label="Action code" placeholder="All actions" description="For example, user.approve" maxLength={128}
                      value={action} onChange={event => setAction(event.currentTarget.value)} error={errors.action} />
                    <TextInput label="Resource ID" placeholder="All resources" description="Copy the identifier from event details" maxLength={36}
                      value={resource} onChange={event => setResource(event.currentTarget.value)} error={errors.resource} />
                  </SimpleGrid>
                  <Group gap="xs">
                    <Button type="submit" size="sm">Apply filters</Button>
                    <Button variant="default" size="sm" onClick={clearHistory}>Reset filters</Button>
                  </Group>
                </Stack>
              </form>
            </Accordion.Panel>
          </Accordion.Item>
        </Accordion>
        {filteredHistory ? <Group gap="xs">
          {active.get('action') ? <Badge variant="light">Action: {actionLabel(active.get('action')!)}</Badge> : null}
          {active.get('resourceId') ? <Badge variant="light">Resource filter active</Badge> : null}
          <Button size="compact-sm" variant="subtle" onClick={clearHistory}>Clear history filters</Button>
        </Group> : null}
        <Group align="flex-end">
          <TextInput label="Search this page" placeholder="Search actions, users, resources or reasons" value={search}
            onChange={event => setSearch(event.currentTarget.value)} style={{ flex: '1 1 260px' }} />
          <NativeSelect label="Outcome on this page" value={outcome} onChange={event => setOutcome(event.currentTarget.value)} data={[
            { value: '', label: 'All outcomes' }, ...Array.from(new Set([...(data?.items.map(item => item.outcome) ?? []), ...(outcome ? [outcome] : [])]))
              .sort().map(value => ({ value, label: words(value) })),
          ]} />
          {filteredPage ? <Button variant="subtle" size="sm" onClick={() => { setSearch(''); setOutcome(''); }}>Clear page filters</Button> : null}
        </Group>
        {page.isPending ? <Text role="status">Loading audit history…</Text> : null}
        {page.isError ? <Stack align="flex-start"><APIProblemView error={page.error} />
          <Button variant="default" onClick={() => void page.refetch()}>Retry</Button></Stack> : null}
        {people.isError ? <Text size="sm" c="dimmed">Some user names are unavailable. Original identifiers remain in event details.</Text> : null}
        {data ? <>
          {rows.length ? <Table.ScrollContainer minWidth={760} type="native">
            <Table aria-label="Audit events" verticalSpacing="sm" highlightOnHover>
              <Table.Thead><Table.Tr>{['Details', 'Time', 'Action', 'Actor', 'Resource', 'Outcome'].map(label =>
                <Table.Th key={label} scope="col">{label}</Table.Th>)}</Table.Tr></Table.Thead>
              <Table.Tbody>{rows.map(item => <Fragment key={item.id}>
                <Table.Tr>
                  <Table.Td w={64}><Button variant="subtle" size="compact-sm" aria-label={expanded === item.id ? 'Hide event details' : 'Show event details'}
                    aria-expanded={expanded === item.id} aria-controls={`audit-${item.id}`} onClick={() => setExpanded(expanded === item.id ? undefined : item.id)}>
                    <span aria-hidden="true">{expanded === item.id ? '▾' : '▸'}</span></Button></Table.Td>
                  <Table.Td style={{ whiteSpace: 'nowrap' }}><Text component="time" dateTime={item.occurredAt} size="sm">
                    {new Date(item.occurredAt).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })}</Text>
                    <Text size="xs" c="dimmed">{new Date(item.occurredAt).toLocaleTimeString()}</Text></Table.Td>
                  <Table.Td><Text size="sm" fw={500}>{actionLabel(item.action)}</Text></Table.Td>
                  <Table.Td><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{actorName(item)}</Text></Table.Td>
                  <Table.Td><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{resourceName(item)}</Text></Table.Td>
                  <Table.Td miw={110}><Badge variant="light" maw="none" color={outcomeColor(item.outcome)}>{words(item.outcome)}</Badge></Table.Td>
                </Table.Tr>
                {expanded === item.id ? <Table.Tr id={`audit-${item.id}`}><Table.Td colSpan={6}>
                  <Stack gap="md" p="sm">
                    <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>{[
                      ['Event ID', item.id], ['Time (UTC)', new Date(item.occurredAt).toISOString()], ['Action code', item.action],
                      ['Actor ID', item.actorId || 'Not recorded'], ['Resource ID', item.resourceId || 'Not recorded'],
                      ['Reason code', item.reasonCode || 'Not recorded'],
                    ].map(([label, value]) => <Stack key={label} gap={4}><Text size="xs" fw={600}>{label}</Text>
                      <Code style={{ overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>{value}</Code></Stack>)}</SimpleGrid>
                    <Group gap="xs">
                      <Button size="compact-sm" variant="default" onClick={() => {
                        setAction(item.action); setResource(''); applyFilters(item.action, ''); heading.current?.focus();
                      }}>Filter by this action</Button>
                      {item.resourceId ? <Button size="compact-sm" variant="default" onClick={() => {
                        setAction(''); setResource(item.resourceId!); applyFilters('', item.resourceId!); heading.current?.focus();
                      }}>Filter by this resource</Button> : null}
                    </Group>
                  </Stack>
                </Table.Td></Table.Tr> : null}
              </Fragment>)}</Table.Tbody>
            </Table>
          </Table.ScrollContainer> : <Stack gap="xs" align="center" py="xl">
            <Title order={3} size="lg">{filteredPage ? 'No matches on this page' : filteredHistory ? 'No matching audit events' : 'No audit events yet'}</Title>
            <Text size="sm" c="dimmed" ta="center">{filteredPage ? 'Clear the page filters or check another page of history.'
              : filteredHistory ? 'Try a different action or resource, or clear the history filters.' : 'Recorded security and administrative activity will appear here.'}</Text>
          </Stack>}
          <Group justify="space-between">
            <Text size="sm" c="dimmed" role="status">Showing {rows.length} of {data.items.length} events on this page</Text>
            <Group gap="xs">
              <Button variant="default" size="sm" disabled={pages.length === 1 || page.isFetching} onClick={() => { setExpanded(undefined); setPages(value => value.slice(0, -1)); }}>Previous page</Button>
              <Text size="sm">Page {pages.length}</Text>
              <Button variant="default" size="sm" disabled={!data.next || page.isFetching} onClick={() => {
                if (data.next) { setExpanded(undefined); setPages(value => [...value, data.next!]); }
              }}>Next page</Button>
            </Group>
          </Group>
        </> : null}
      </Stack>
    </Paper>
    <Text size="xs" c="dimmed">Audit records are read-only. An allowed decision does not necessarily mean the operation completed. User names reflect current accounts.</Text>
  </Stack>;
}
