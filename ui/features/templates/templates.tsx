'use client';

import { providerLabel } from '../../lib/provider-label';

import { useEffect, useRef, useState } from 'react';
import { Button, Group, Menu, Modal, Paper, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { useProjectCatalog, type TemplateRevision } from './use-templates';
import { ProjectCatalog } from './project-catalog';
import { LaunchFlow } from '../workspaces/launch-flow';
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
      {!normal ? <Title order={1}>Projects</Title> : null}
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {normal ? <LaunchFlow key={current.user.id} csrfToken={current.csrfToken}
        gitUserID={current.permissions.includes('git.manage') ? current.user.id : undefined}>
        {onReview => <TemplateList userID={current.user.id} admin={current.role === 'administrator'} csrfToken={current.csrfToken}
          canCreate={current.permissions.includes('template.create')} onReview={onReview} />}
      </LaunchFlow> : null}
    </ShiftBoxShell>
  );
}

function TemplateList({ userID, admin, csrfToken, canCreate, onReview }: {
  userID: string; admin: boolean; csrfToken: string; canCreate: boolean; onReview: (project: TemplateRevision) => void;
}) {
  const [showArchived, setShowArchived] = useState(false);
  const [dialog, setDialog] = useState<TemplateDialog>();
  const trigger = useRef<HTMLButtonElement | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const archived = admin && showArchived;
  const catalog = useProjectCatalog(userID, admin, archived);
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
          <Title order={1} ref={heading} tabIndex={-1}>Projects</Title>
          <Text size="sm" c="dimmed">Choose a project to review and launch a workspace.</Text>
        </Stack>
        {canCreate && dialog?.action !== 'secrets' ? <Button onClick={event => open({ action: 'create' }, event.currentTarget)}>New project</Button> : null}
      </Group>
      <div hidden={dialog?.action === 'secrets'}>
        <ProjectCatalog catalog={catalog} onProjectAction={(template, control) => {
          if (template.state === 'archived') open({ action: 'details', template }, control);
          else onReview(template);
        }} management={{
          userID, archived, canCreate, onArchivedChange: admin ? setShowArchived : undefined,
          actions: template => template.ownerId === userID || admin
            ? <TemplateActions template={template} open={open} />
            : <Button variant="subtle" size="compact-sm" aria-label={`View ${template.name}`} aria-haspopup="dialog"
                onClick={event => open({ action: 'details', template }, event.currentTarget)}>View details</Button>,
        }} />
      </div>
      {dialog?.action === 'secrets' ? <Stack>
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={close}>Back to projects</Button>
        <Paper withBorder radius="md" p="md">
          <Stack gap="md">
            <Title order={2} size="h3">{dialog.template.name}</Title>
            <Text size="sm" c="dimmed">Project secrets are available to users with launch access. Saved values cannot be viewed.</Text>
            <Secrets scope="template" scopeID={dialog.template.id} userID={userID} csrfToken={csrfToken} />
          </Stack>
        </Paper>
      </Stack> : null}
      {dialog?.action === 'details' ? <TemplateDetails template={dialog.template} onClose={close} /> : null}
      {dialog?.action === 'share' ? <TemplateSharing template={dialog.template} csrfToken={csrfToken} onClose={close} refresh={catalog.refetch} /> : null}
      {dialog?.action === 'archive' ? <TemplateArchive template={dialog.template} csrfToken={csrfToken} onClose={close} refresh={catalog.refetch} /> : null}
      {dialog?.action === 'create' || dialog?.action === 'revise' ? <TemplateRevisionForm
        template={dialog.action === 'revise' ? dialog.template : undefined} admin={admin} csrfToken={csrfToken} onClose={close} refresh={catalog.refetch} /> : null}
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
      {!archived ? <Menu.Item onClick={() => run('details')}>View details</Menu.Item> : null}
      <Menu.Divider />
      <Menu.Item color={archived ? undefined : 'red'} onClick={() => run('archive')}>{archived ? 'Restore project' : 'Archive project'}</Menu.Item>
    </Menu.Dropdown>
  </Menu>;
}

function TemplateDetails({ template, onClose }: { template: TemplateRevision; onClose: () => void }) {
  const fields = [
    ['Revision', String(template.currentRevision)], ['Owner', template.ownerName || 'Project owner'],
    ['Repository URL', template.repositoryUrl || 'Unavailable'], ['Repository ref', template.repositoryRef || 'Default branch'],
    ['Dev Container configuration', template.devcontainerPath || 'Unavailable'], ['Resource profile', template.resourceProfileName || 'Unavailable'],
    ['Provider', providerLabel(template.providerKind)], ['Platform image', template.platformImageName || 'Unavailable'],
    ['Secret references', template.secretNames?.join(', ') || 'None'],
  ];
  return <Modal opened title="Project details" size="lg" onClose={onClose}
    closeButtonProps={{ 'aria-label': 'Close project details' }} transitionProps={{ duration: 0 }}>
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
