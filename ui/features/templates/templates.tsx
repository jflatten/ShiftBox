'use client';

import { useState } from 'react';
import { Button, Group, Stack, Switch, Table, Text, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell } from '../../components/app-shell';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { useTemplates, type TemplateRevision } from './use-templates';
import { TemplateArchive } from './archive';
import { TemplateSharing } from './sharing';
import { TemplateRevisionForm } from './revision';

export function Templates() {
  const session = useSession();
  const current = session.session;
  const normal = current?.mode === 'normal' && current.installationState === 'active';
  return (
    <ShiftBoxShell session={session.session} alerts={[]} error={session.error ?? undefined}>
      <Title order={1}>Templates</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {normal ? <TemplateList key={current.user.id} userID={current.user.id} admin={current.role === 'administrator'} csrfToken={current.csrfToken}
        canCreate={current.permissions.includes('template.create')} /> : null}
    </ShiftBoxShell>
  );
}

function TemplateList({ userID, admin, csrfToken, canCreate }: {
  userID: string; admin: boolean; csrfToken: string; canCreate: boolean;
}) {
  const [cursors, setCursors] = useState<string[]>([]);
  const [showArchived, setShowArchived] = useState(false);
  const [dialog, setDialog] = useState<
    { action: 'share' | 'archive' | 'revise'; template: TemplateRevision } | { action: 'create' }
  >();
  const cursor = cursors.at(-1);
  const archived = admin && showArchived;
  const page = useTemplates(cursor, true, archived, userID, admin);
  const { items, nextCursor } = page.isError ? {} : page.data ?? {};
  return (
    <Stack>
      {admin || canCreate ? <Group>
        {admin ? <Switch label="Archived templates" checked={archived} onChange={event => {
          setCursors([]);
          setShowArchived(event.currentTarget.checked);
        }} /> : null}
        {canCreate && !archived ? <Button onClick={() => setDialog({ action: 'create' })}>New template</Button> : null}
      </Group> : null}
      {page.isError ? <APIProblemView error={page.error} /> : null}
      {page.isPending ? <Text role="status">Loading templates…</Text> : null}
      {items && items.length > 0 ? (
        <Table.ScrollContainer minWidth={760}>
         <Table aria-label="Templates">
          <Table.Thead><Table.Tr>
            <Table.Th>Name</Table.Th><Table.Th>Revision</Table.Th><Table.Th>Owner</Table.Th>
            <Table.Th>State</Table.Th><Table.Th>Repository</Table.Th><Table.Th>Resource profile</Table.Th>
            <Table.Th>Platform image</Table.Th><Table.Th>Actions</Table.Th>
          </Table.Tr></Table.Thead>
          <Table.Tbody>
            {items.map(template => {
              const owned = template.ownerId === userID;
              const manageable = owned || admin;
              const isArchived = template.state === 'archived';
              return <Table.Tr key={template.id}>
                <Table.Td>{template.name}</Table.Td><Table.Td>{template.currentRevision}</Table.Td>
                <Table.Td>{owned ? 'You' : template.ownerId}</Table.Td><Table.Td>{isArchived ? 'Archived' : 'Active'}</Table.Td>
                <Table.Td>{template.repositoryUrl}</Table.Td><Table.Td>{template.resourceProfileId}</Table.Td>
                <Table.Td>{template.platformArtifactId}</Table.Td>
                <Table.Td>{manageable ? <Group gap="xs" wrap="nowrap">
                  {!isArchived ? <Button size="compact-sm" variant="subtle" onClick={() => setDialog({ action: 'revise', template })}>New revision</Button> : null}
                  <Button size="compact-sm" variant="subtle" onClick={() => setDialog({ action: 'share', template })}>
                    {isArchived ? 'Manage launch grants' : 'Share launch access'}
                  </Button>
                  <Button size="compact-sm" variant="subtle" onClick={() => setDialog({ action: 'archive', template })}>
                    {isArchived ? 'Restore template' : 'Archive template'}
                  </Button>
                </Group> : null}</Table.Td>
              </Table.Tr>;
            })}
          </Table.Tbody>
         </Table>
        </Table.ScrollContainer>
      ) : items ? <Text>{cursor ? 'No templates on this page.' : archived ? 'No archived templates.' : 'No templates yet.'}</Text> : null}
      {cursor || nextCursor ? (
        <Group>
          <Button variant="default" disabled={!cursor} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
          <Button variant="default" disabled={!nextCursor} onClick={() => {
            if (nextCursor) setCursors(current => [...current, nextCursor]);
          }}>Next page</Button>
        </Group>
      ) : null}
      {dialog?.action === 'share' ? <TemplateSharing template={dialog.template} csrfToken={csrfToken}
        onClose={() => setDialog(undefined)} refresh={page.refetch} /> : null}
      {dialog?.action === 'archive' ? <TemplateArchive template={dialog.template} csrfToken={csrfToken}
        onClose={() => setDialog(undefined)} refresh={page.refetch} /> : null}
      {dialog?.action === 'create' || dialog?.action === 'revise' ? (
        <TemplateRevisionForm template={dialog.action === 'revise' ? dialog.template : undefined} csrfToken={csrfToken}
          onClose={() => setDialog(undefined)} refresh={page.refetch} />
      ) : null}
    </Stack>
  );
}
