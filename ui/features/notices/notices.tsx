'use client';

import { useState } from 'react';
import { Alert, Button, Checkbox, Group, Stack, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell, type ShellSession } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { useSession } from '../../lib/use-session';
import { PublishNotice } from './publish-notice';

interface Notice {
  id: string;
  title: string;
  body: string;
  severity: 'info' | 'warning' | 'critical';
  readAt?: string | null;
  dismissedAt?: string | null;
  expiresAt?: string | null;
}

const noticePath = '/api/v1/notices';
const severityColors = { info: 'blue', warning: 'yellow', critical: 'red' };

export function Notices() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.mode === 'normal' && current.installationState === 'active' && current.permissions.includes('notice.list');
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
      <Title order={1}>Notices</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button> : null}
      {allowed ? <NoticeList key={current.user.id} userId={current.user.id} csrfToken={current.csrfToken} session={current} /> : null}
    </ShiftBoxShell>
  );
}

function NoticeList({ userId, csrfToken, session }: { userId: string; csrfToken: string; session: ShellSession }) {
  const [history, setHistory] = useState(false);
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const page = useQuery<{ items: Notice[]; next?: string }>({
    queryKey: ['notices', userId, history, cursor],
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams();
      if (history) params.set('includeDismissed', 'true');
      if (cursor) params.set('cursor', cursor);
      const response = await fetch(noticePath + (params.size ? '?' + params : ''), { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      const link = response.headers.get('Link') ?? '';
      const next = /^<\/api\/v1\/notices\?([^<>]+)>; rel="next"$/.exec(link)?.[1];
      return { items: await response.json(), next: next ? new URLSearchParams(next).get('cursor') ?? undefined : undefined };
    },
  });
  const current = !page.isError ? page.data : undefined;
  return (
    <>
      <Checkbox label="Include dismissed and historical notices" checked={history} onChange={event => {
        setCursors([]);
        setHistory(event.currentTarget.checked);
      }} />
      {page.isPending ? <Text role="status">Loading notices…</Text> : null}
      {page.isError ? <Stack align="flex-start">
        <APIProblemView error={page.error} />
        <Button variant="default" onClick={() => void page.refetch()}>Retry</Button>
      </Stack> : null}
      {current?.items.length === 0 ? <Text>No notices.</Text> : null}
      {current?.items.map(notice => <NoticeItem key={notice.id} notice={notice} csrfToken={csrfToken}
        permissions={session.permissions} refresh={page.refetch} />)}
      {cursor || current?.next ? <Group>
        <Button variant="default" disabled={!cursor} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
        <Button variant="default" disabled={!current?.next} onClick={() => {
          const next = current?.next;
          if (next) setCursors(cursors => [...cursors, next]);
        }}>Next page</Button>
      </Group> : null}
      {session.role === 'administrator' && session.permissions.includes('notice.publish') ? <PublishNotice csrfToken={csrfToken} /> : null}
    </>
  );
}

function NoticeItem({ notice, csrfToken, permissions, refresh }: {
  notice: Notice;
  csrfToken: string;
  permissions: string[];
  refresh: () => Promise<unknown>;
}) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const expired = !!notice.expiresAt && new Date(notice.expiresAt).getTime() <= Date.now();
  const save = async (state: 'read' | 'dismissed') => {
    setSaving(true);
    setError(undefined);
    try {
      const response = await fetch(noticePath + '/' + encodeURIComponent(notice.id) + '/receipt', {
        method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ state }),
      });
      if (!response.ok) throw await readProblem(response);
      await refresh();
    } catch (error) {
      setError(error);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Alert color={severityColors[notice.severity]} title={notice.title}>
      <Stack gap="xs" align="flex-start">
        <Text style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{notice.body}</Text>
        {notice.dismissedAt ? <Text size="sm">Dismissed</Text> : null}
        {!notice.dismissedAt && !expired && !notice.readAt && permissions.includes('notice.read') ? (
          <Button size="compact-sm" variant="default" loading={saving} onClick={() => void save('read')}>Mark as read</Button>
        ) : null}
        {!notice.dismissedAt && !expired && notice.readAt && permissions.includes('notice.dismiss') ? (
          <Button size="compact-sm" variant="default" loading={saving} onClick={() => void save('dismissed')}>Dismiss</Button>
        ) : null}
        {error !== undefined ? <APIProblemView error={error} /> : null}
      </Stack>
    </Alert>
  );
}
