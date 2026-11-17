'use client';

import { platformImageLabel, providerLabel } from '../../lib/provider-label';

import { useEffect, useRef, useState } from 'react';
import { Anchor, Badge, Button, Group, Menu, Modal, Paper, SimpleGrid, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell } from '../../components/app-shell';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { useTemplates, type TemplateRevision } from './use-templates';
import { TemplateArchive } from './archive';
import { TemplateSharing } from './sharing';
import { TemplateRevisionForm } from './revision';
import { Secrets } from '../secrets/secrets';

type TemplateAction = 'details' | 'share' | 'archive' | 'revise' | 'secrets';
type TemplateDialog = { action: TemplateAction; template: TemplateRevision } | { action: 'create' };

export function Templates() {
  const session = useSession();
  const current = session.session;
  const normal = current?.mode === 'normal' && current.installationState === 'active';
  return (
    <ShiftBoxShell session={session.session} alerts={[]} error={session.error ?? undefined}>
      {!normal ? <Title order={1}>Templates</Title> : null}
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
  const [searchDraft, setSearchDraft] = useState('');
  const [search, setSearch] = useState('');
  const [dialog, setDialog] = useState<TemplateDialog>();
  const trigger = useRef<HTMLButtonElement | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const cursor = cursors.at(-1);
  const archived = admin && showArchived;
  const page = useTemplates(cursor, true, archived, userID, admin, search);
  const { items, nextCursor } = page.isError ? {} : page.data ?? {};
  useEffect(() => {
    if (dialog || !trigger.current) return;
    // Archiving can remove the row; the page heading provides a stable
    // return point when the original action control no longer exists.
    const destination = trigger.current.isConnected ? trigger.current : heading.current;
    trigger.current = null;
    destination?.focus();
  }, [dialog]);
  const open = (selected: TemplateDialog, control: HTMLButtonElement) => {
    trigger.current = control;
    setDialog(selected);
  };
  const close = () => setDialog(undefined);
  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Title order={1} ref={heading} tabIndex={-1}>Templates</Title>
          <Text size="sm" c="dimmed">Reusable project configurations for new workspaces.</Text>
        </Stack>
        {canCreate && dialog?.action !== 'secrets' ? <Button onClick={event => open({ action: 'create' }, event.currentTarget)}>New template</Button> : null}
      </Group>
      {dialog?.action === 'secrets' ? <Stack>
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={close}>Back to templates</Button>
        <Paper withBorder radius="md" p="md">
          <Stack gap="md">
            <Title order={2} size="h3">{dialog.template.name}</Title>
            <Text size="sm" c="dimmed">Project secrets are available to users with launch access. Saved values cannot be viewed.</Text>
            <Secrets scope="template" scopeID={dialog.template.id} userID={userID} csrfToken={csrfToken} />
          </Stack>
        </Paper>
      </Stack> : <Paper withBorder radius="md" p="md">
        <Stack gap="md">
          <Group justify="space-between" align="flex-end">
            <form onSubmit={event => { event.preventDefault(); setSearch(searchDraft.trim()); setCursors([]); }} style={{ flex: '1 1 280px' }}>
              <Group align="flex-end" gap="xs">
                <TextInput type="search" label="Search templates" placeholder="Name or repository" value={searchDraft} maxLength={200}
                  onChange={event => setSearchDraft(event.currentTarget.value)} style={{ flex: '1 1 160px' }} />
                <Button type="submit" variant="default">Search</Button>
                {search || searchDraft ? <Button variant="subtle" onClick={() => { setSearchDraft(''); setSearch(''); setCursors([]); }}>Clear search</Button> : null}
              </Group>
            </form>
            {admin ? <Switch label="Archived templates" checked={archived} mb={8} onChange={event => {
              setCursors([]); setShowArchived(event.currentTarget.checked);
            }} /> : null}
            <Button variant="default" loading={page.isFetching} onClick={() => void page.refetch()}>Refresh</Button>
          </Group>
          <Text size="xs" c="dimmed">{archived
            ? 'Showing archived templates. Restore a template to allow new launches; existing workspaces and grants are preserved.'
            : 'Showing active templates. New revisions apply to future launches; existing workspaces keep their configuration.'}</Text>
          {page.isError ? <Stack align="flex-start">
            <APIProblemView error={page.error} />
            <Button variant="default" onClick={() => void page.refetch()}>Retry</Button>
          </Stack> : null}
          {page.isPending ? <Text role="status">Loading templates…</Text> : null}
          {items && items.length > 0 ? (
            <Table.ScrollContainer minWidth={680}>
              <Table aria-label="Templates" highlightOnHover verticalSpacing="md">
                <Table.Thead><Table.Tr>
                  <Table.Th>Template</Table.Th><Table.Th>Repository</Table.Th><Table.Th>Configuration</Table.Th>
                  <Table.Th>Owner</Table.Th><Table.Th>Actions</Table.Th>
                </Table.Tr></Table.Thead>
                <Table.Tbody>{items.map(template => <Table.Tr key={template.id}>
                  <Table.Td style={{ maxWidth: 230, overflowWrap: 'anywhere' }}>
                    <Stack gap={4} align="flex-start">
                      <Anchor component="button" type="button" fw={600} ta="left" aria-label={`View ${template.name}`}
                        onClick={event => open({ action: 'details', template }, event.currentTarget)}>{template.name}</Anchor>
                      <Text size="xs" c="dimmed">Revision {template.currentRevision}</Text>
                      <Badge size="sm" variant="light" color={template.state === 'archived' ? 'gray' : 'green'}>{template.state === 'archived' ? 'Archived' : 'Active'}</Badge>
                    </Stack>
                  </Table.Td>
                  <Table.Td style={{ maxWidth: 230, overflowWrap: 'anywhere' }}>
                    <Text size="sm" title={template.repositoryUrl}>{template.repositoryUrl?.replace(/^https:\/\/github\.com\//, '') || 'Unavailable'}</Text>
                    <Text size="xs" c="dimmed">{template.repositoryRef || 'Default branch'}</Text>
                  </Table.Td>
                  <Table.Td style={{ maxWidth: 200, overflowWrap: 'anywhere' }}>
                    <Text size="sm">{template.resourceProfileName || 'Profile unavailable'}</Text>
                    <Text size="xs" c="dimmed">{template.platformImageName ? platformImageLabel(template.platformImageName, template.providerKind) : 'Image unavailable'}</Text>
                  </Table.Td>
                  <Table.Td style={{ maxWidth: 180, overflowWrap: 'anywhere' }}>
                    <Text size="sm">{template.ownerId === userID ? 'You' : template.ownerName || 'Template owner'}</Text>
                  </Table.Td>
                  <Table.Td>{template.ownerId === userID || admin ? <TemplateActions template={template} open={open} /> : <Text size="sm" c="dimmed">Shared with you</Text>}</Table.Td>
                </Table.Tr>)}</Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          ) : items ? <Stack align="center" gap="xs" py="xl">
            <Text fw={600}>{search ? 'No templates match your search.' : cursor ? 'No templates on this page.' : archived ? 'No archived templates.' : 'No templates yet.'}</Text>
            <Text size="sm" c="dimmed">{search ? 'Try a different name or repository, or clear the search.' : cursor ? 'Return to the previous page.' : archived ? 'Archived templates will appear here.' :
              canCreate ? 'Create a template to define a repeatable workspace configuration.' : 'Ask a template owner to share launch access with you.'}</Text>
          </Stack> : null}
          <Group justify="space-between">
            <Text size="sm" c="dimmed" role="status">{items ? `${items.length} ${items.length === 1 ? 'template' : 'templates'} on this page` : page.isPending ? 'Loading…' : 'Template list unavailable'}</Text>
            <Group gap="xs">
              <Button variant="default" disabled={!cursor || page.isFetching} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
              <Text size="sm">Page {cursors.length + 1}</Text>
              <Button variant="default" disabled={!nextCursor || page.isFetching} onClick={() => { if (nextCursor) setCursors(current => [...current, nextCursor]); }}>Next page</Button>
            </Group>
          </Group>
        </Stack>
      </Paper>}
      {dialog?.action === 'details' ? <TemplateDetails template={dialog.template} onClose={close} /> : null}
      {dialog?.action === 'share' ? <TemplateSharing template={dialog.template} csrfToken={csrfToken} onClose={close} refresh={page.refetch} /> : null}
      {dialog?.action === 'archive' ? <TemplateArchive template={dialog.template} csrfToken={csrfToken} onClose={close} refresh={page.refetch} /> : null}
      {dialog?.action === 'create' || dialog?.action === 'revise' ? <TemplateRevisionForm
        template={dialog.action === 'revise' ? dialog.template : undefined} admin={admin} csrfToken={csrfToken} onClose={close} refresh={page.refetch} /> : null}
    </Stack>
  );
}

function TemplateActions({ template, open }: { template: TemplateRevision; open: (dialog: TemplateDialog, control: HTMLButtonElement) => void }) {
  const trigger = useRef<HTMLButtonElement>(null);
  const archived = template.state === 'archived';
  const run = (action: TemplateAction) => { if (trigger.current) open({ action, template }, trigger.current); };
  return <Menu position="bottom-end" withinPortal transitionProps={{ duration: 0 }}>
    <Menu.Target><Button ref={trigger} variant="default" size="xs" aria-label={`Actions for ${template.name}`}>Actions</Button></Menu.Target>
    <Menu.Dropdown>
      {!archived ? <Menu.Item onClick={() => run('revise')}>New revision</Menu.Item> : null}
      <Menu.Item onClick={() => run('share')}>{archived ? 'Manage launch grants' : 'Share launch access'}</Menu.Item>
      {!archived ? <Menu.Item onClick={() => run('secrets')}>Project secrets</Menu.Item> : null}
      <Menu.Divider />
      <Menu.Item color={archived ? undefined : 'red'} onClick={() => run('archive')}>{archived ? 'Restore template' : 'Archive template'}</Menu.Item>
    </Menu.Dropdown>
  </Menu>;
}

function TemplateDetails({ template, onClose }: { template: TemplateRevision; onClose: () => void }) {
  const fields = [
    ['Revision', String(template.currentRevision)], ['Owner', template.ownerName || 'Template owner'],
    ['Repository URL', template.repositoryUrl || 'Unavailable'], ['Repository ref', template.repositoryRef || 'Default branch'],
    ['Dev Container configuration', template.devcontainerPath || 'Unavailable'], ['Resource profile', template.resourceProfileName || 'Unavailable'],
    ['Provider', providerLabel(template.providerKind)], ['Platform image', template.platformImageName || 'Unavailable'],
    ['Secret references', template.secretNames?.join(', ') || 'None'],
  ];
  return <Modal opened title="Template details" size="lg" onClose={onClose}
    closeButtonProps={{ 'aria-label': 'Close template details' }} transitionProps={{ duration: 0 }}>
    <Stack gap="md">
      <Title order={2} size="h3" style={{ overflowWrap: 'anywhere' }}>{template.name}</Title>
      <Text size="sm" c="dimmed">This revision defines future launches. Secret references are names, not saved values.</Text>
      <SimpleGrid component="dl" cols={{ base: 1, sm: 2 }} m={0} spacing="md">
        {fields.map(([label, value]) => <div key={label} style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
          <Text component="dt" size="sm" c="dimmed">{label}</Text><Text component="dd" m={0}>{value}</Text>
        </div>)}
      </SimpleGrid>
      <Group justify="flex-end"><Button variant="default" onClick={onClose}>Done</Button></Group>
    </Stack>
  </Modal>;
}
