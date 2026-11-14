'use client';

import { useState } from 'react';
import { Button, Group, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell } from '../../components/app-shell';
import { readProblem } from '../../lib/api-problem';
import { useSession } from '../../lib/use-session';

const auditPath = '/api/v1/admin/audit';

interface AuditEvent {
  id: string;
  occurredAt: string;
  actorType: string;
  actorId?: string;
  action: string;
  resourceType: string;
  resourceId?: string;
  outcome: string;
  reasonCode?: string;
}

export function Audit() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.role === 'administrator' && current.mode === 'normal' &&
    current.installationState === 'active' && current.permissions.includes('audit.list');
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
      <Title order={1}>Audit</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button> : null}
      {current && !allowed ? <Text>Administrator access required.</Text> : null}
      {allowed ? <AuditHistory key={current.user.id} userId={current.user.id} /> : null}
    </ShiftBoxShell>
  );
}

function AuditHistory({ userId }: { userId: string }) {
  const [pages, setPages] = useState([auditPath]);
  const path = pages[pages.length - 1];
  const page = useQuery<{ items: AuditEvent[]; next?: string }>({
    queryKey: ['audit', userId, path],
    queryFn: async ({ signal }) => {
      const response = await fetch(path, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      const link = response.headers.get('Link') ?? '';
      // Accept only a relative URL on this endpoint from the pagination header.
      const next = /^<(\/api\/v1\/admin\/audit\?[^<>]+)>; rel="next"$/.exec(link)?.[1];
      return { items: await response.json(), next };
    },
  });
  const data = !page.isError ? page.data : undefined;
  return (
    <>
      <form onSubmit={event => {
        event.preventDefault();
        const fields = new FormData(event.currentTarget);
        const query = new URLSearchParams();
        for (const name of ['action', 'resourceId']) {
          const value = String(fields.get(name) ?? '').trim();
          if (value) query.set(name, value);
        }
        const nextPath = query.size ? auditPath + '?' + query : auditPath;
        if (pages.length === 1 && nextPath === path) void page.refetch();
        setPages([nextPath]);
      }}>
        <Group align="flex-end">
          <TextInput label="Action" name="action" placeholder="user.approve" maxLength={128} />
          <TextInput label="Resource ID" name="resourceId" maxLength={36} />
          <Button type="submit">Apply filters</Button>
        </Group>
      </form>
      {page.isPending ? <Text role="status">Loading audit history…</Text> : null}
      {page.isError ? (
        <Stack align="flex-start">
          <APIProblemView error={page.error} />
          <Button variant="default" onClick={() => void page.refetch()}>Retry</Button>
        </Stack>
      ) : null}
      {data?.items.length === 0 ? <Text>No matching audit events.</Text> : null}
      {data?.items.length ? <AuditTable items={data.items} /> : null}
      {pages.length > 1 || data?.next ? (
        <Group>
          <Button variant="default" disabled={pages.length === 1}
            onClick={() => setPages(current => current.slice(0, -1))}>Previous page</Button>
          <Button variant="default" disabled={!data?.next} onClick={() => {
            const next = data?.next;
            if (next) setPages(current => [...current, next]);
          }}>Next page</Button>
        </Group>
      ) : null}
    </>
  );
}

function AuditTable({ items }: { items: AuditEvent[] }) {
  return (
    <Table.ScrollContainer minWidth={700} type="native">
      <Table aria-label="Audit events">
        <Table.Thead>
          <Table.Tr>{['Time', 'Action', 'Actor', 'Resource', 'Outcome'].map(label => <Table.Th key={label} scope="col">{label}</Table.Th>)}</Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {items.map(item => (
            <Table.Tr key={item.id}>
              <Table.Td><time dateTime={item.occurredAt}>{new Date(item.occurredAt).toLocaleString()}</time></Table.Td>
              <Table.Td>{item.action}</Table.Td>
              <Table.Td><Text size="sm">{item.actorType}</Text>{item.actorId}</Table.Td>
              <Table.Td><Text size="sm">{item.resourceType}</Text>{item.resourceId}</Table.Td>
              <Table.Td><Text size="sm">{item.outcome}</Text>{item.reasonCode}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
