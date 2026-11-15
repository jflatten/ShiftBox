'use client';

import { useState } from 'react';
import { Button, Group, Modal, Paper, SegmentedControl, Stack, Text, Title } from '@mantine/core';
import { ShiftBoxShell, type ShellSession } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { useInfrastructure } from '../../lib/use-infrastructure';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { PublishNotice } from './publish-notice';
import { NoticeMessage } from './notice-message';
import { useNotices } from './use-notices';

export function Notices() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.mode === 'normal' && current.installationState === 'active' && current.permissions.includes('notice.list');
  const canViewInfrastructure = allowed && current.role === 'administrator' && current.permissions.includes('infrastructure.view');
  const { alerts, error } = useInfrastructure(canViewInfrastructure ? current.user.id : undefined);
  return (
    <ShiftBoxShell showNoticeToast={false} session={current} alerts={alerts} error={session.error ?? (canViewInfrastructure ? error : undefined) ?? undefined}>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {allowed ? <NoticeList key={current.user.id} userId={current.user.id} csrfToken={current.csrfToken} session={current} /> : <Title order={1}>Notices</Title>}
    </ShiftBoxShell>
  );
}

function NoticeList({ userId, csrfToken, session }: { userId: string; csrfToken: string; session: ShellSession }) {
  const [history, setHistory] = useState(false);
  const [cursors, setCursors] = useState<string[]>([]);
  const [publishing, setPublishing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [published, setPublished] = useState('');
  const cursor = cursors.at(-1);
  const page = useNotices(userId, history, cursor);
  const current = !page.isError ? page.data : undefined;
  const canPublish = session.role === 'administrator' && session.permissions.includes('notice.publish');
  const close = () => { if (!saving) setPublishing(false); };
  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Title order={1}>Notices</Title>
          <Text c="dimmed">Updates and announcements for everyone in ShiftBox.</Text>
        </Stack>
        {canPublish ? <Button onClick={() => { setPublished(''); setPublishing(true); }}>Publish notice</Button> : null}
      </Group>
      {published ? <Text role="status" c="green">{published}</Text> : null}
      <Paper withBorder p="md" radius="md">
        <Stack gap="md">
          <Group justify="space-between">
            <SegmentedControl aria-label="Notice view" value={history ? 'all' : 'active'} data={[
              { value: 'active', label: 'Active notices' }, { value: 'all', label: 'All notices' },
            ]} onChange={value => { setCursors([]); setHistory(value === 'all'); }} />
            <Button variant="default" size="sm" loading={page.isFetching} onClick={() => void page.refetch()}>Refresh</Button>
          </Group>
          <Text size="sm" c="dimmed">{history ? 'Active notices and your read or dismissed history.' : 'Dismiss a notice to hide it across the app for you. You can find it again in All notices.'}</Text>
          {page.isPending ? <Text role="status">Loading notices…</Text> : null}
          {page.isError ? <Stack align="flex-start">
            <APIProblemView error={page.error} />
            <Button variant="default" onClick={() => void page.refetch()}>Retry</Button>
          </Stack> : null}
          {current?.items.length === 0 ? <Stack align="center" gap="xs" py="xl">
            <Title order={2} size="lg">{cursor ? 'No more notices' : history ? 'No notices yet' : 'No active notices'}</Title>
            <Text size="sm" c="dimmed" ta="center">{history ? 'New announcements and your notice history will appear here.' : 'You’re all caught up. New announcements will appear here and across the app.'}</Text>
          </Stack> : null}
          {current?.items.map(notice => <NoticeMessage key={notice.id} notice={notice} userId={userId}
            csrfToken={csrfToken} permissions={session.permissions} />)}
          {cursor || current?.next ? <Group justify="space-between">
            <Text size="sm" c="dimmed">Page {cursors.length + 1}</Text>
            <Group gap="xs">
              <Button size="sm" variant="default" disabled={!cursor || page.isFetching} onClick={() => setCursors(value => value.slice(0, -1))}>Previous page</Button>
              <Button size="sm" variant="default" disabled={!current?.next || page.isFetching} onClick={() => {
                const next = current?.next;
                if (next) setCursors(value => [...value, next]);
              }}>Next page</Button>
            </Group>
          </Group> : null}
        </Stack>
      </Paper>
      <Text size="xs" c="dimmed">Dismissing a notice also marks it as read. It does not cancel maintenance or resolve an operational warning.</Text>
      <Modal opened={publishing} onClose={close} title="Publish notice" size="lg" closeOnClickOutside={!saving}
        closeOnEscape={!saving} withCloseButton={!saving} closeButtonProps={{ 'aria-label': 'Close publish notice' }}>
        {publishing ? <PublishNotice csrfToken={csrfToken} onCancel={close} onSaving={setSaving} onPublished={scheduled => {
          setPublishing(false);
          setPublished(scheduled ? 'Notice scheduled. It will appear for everyone at the start time.' : 'Notice published. It is now visible to everyone.');
        }} /> : null}
      </Modal>
    </Stack>
  );
}
