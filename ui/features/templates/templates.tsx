'use client';

import { useState } from 'react';
import { Button, Group, Table, Text, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell } from '../../components/app-shell';
import { useSession } from '../../lib/use-session';
import { useTemplates } from './use-templates';

export function Templates() {
  const session = useSession();
  const normal = session.session?.mode === 'normal' && session.session.installationState === 'active';
  return (
    <ShiftBoxShell session={session.session} alerts={[]} error={session.error ?? undefined}>
      <Title order={1}>Templates</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button> : null}
      {normal ? <TemplateList /> : null}
    </ShiftBoxShell>
  );
}

function TemplateList() {
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const page = useTemplates(cursor);
  if (page.isError) return <APIProblemView error={page.error} />;
  if (!page.data) return <Text role="status">Loading templates…</Text>;
  const { items, nextCursor } = page.data;
  return (
    <>
      {items.length > 0 ? (
        <Table aria-label="Templates">
          <Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Revision</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {items.map(template => (
              <Table.Tr key={template.id}><Table.Td>{template.name}</Table.Td><Table.Td>{template.currentRevision}</Table.Td></Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      ) : <Text>{cursor ? 'No templates on this page.' : 'No templates yet.'}</Text>}
      {cursor || nextCursor ? (
        <Group>
          <Button variant="default" disabled={!cursor} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
          <Button variant="default" disabled={!nextCursor} onClick={() => {
            if (nextCursor) setCursors(current => [...current, nextCursor]);
          }}>Next page</Button>
        </Group>
      ) : null}
    </>
  );
}
