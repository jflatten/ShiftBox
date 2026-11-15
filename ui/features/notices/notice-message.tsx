'use client';

import { useRef, useState } from 'react';
import { Anchor, Badge, Button, CloseButton, Group, Notification, Paper, Stack, Text, ThemeIcon, Title } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import type { Notice } from './use-notices';

const colors = { info: 'blue', warning: 'yellow', critical: 'red' };
const labels = { info: 'Information', warning: 'Warning', critical: 'Critical' };
const dateLabel = (date: string) => new Date(date).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });

/** Closing is a personal receipt, never acknowledgment of an operational condition. */
export function NoticeMessage({ notice, userId, csrfToken, permissions, compact = false }: {
  notice: Notice; userId: string; csrfToken: string; permissions: string[]; compact?: boolean;
}) {
  const client = useQueryClient();
  const busy = useRef(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const expired = !!notice.expiresAt && Date.parse(notice.expiresAt) <= Date.now();
  const active = !notice.dismissedAt && !expired;
  const canRead = permissions.includes('notice.read');
  const canDismiss = active && permissions.includes('notice.dismiss') && (!!notice.readAt || canRead);
  const color = colors[notice.severity];

  async function save(state: 'read' | 'dismissed') {
    if (busy.current) return;
    busy.current = true;
    setSaving(true);
    setError(undefined);
    try {
      // The existing API requires a read receipt before a dismissal can be stored.
      const states = state === 'dismissed' && !notice.readAt ? ['read', 'dismissed'] : [state];
      for (const next of states) {
        const response = await fetch(`/api/v1/notices/${encodeURIComponent(notice.id)}/receipt`, {
          method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
          body: JSON.stringify({ state: next }),
        });
        if (!response.ok) throw await readProblem(response);
      }
      await client.invalidateQueries({ queryKey: ['notices', userId] });
    } catch (error) {
      setError(error);
    } finally {
      busy.current = false;
      setSaving(false);
    }
  }

  if (compact) return <Notification withBorder radius="sm" color={color}
    title={<Text fw={600} size="sm" style={{ overflowWrap: 'anywhere' }}>{notice.title}</Text>}
    withCloseButton={canDismiss} onClose={() => void save('dismissed')}
    closeButtonProps={{ 'aria-label': `Dismiss ${notice.title}`, disabled: saving }}
    style={{ boxShadow: 'var(--mantine-shadow-md)' }}>
    <Stack gap="xs">
      <Badge variant="light" color={color} size="sm">{labels[notice.severity]}</Badge>
      <Text size="sm" lineClamp={2} style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{notice.body}</Text>
      <Anchor href="/notices/" size="sm">View notices</Anchor>
      {saving ? <Text role="status" size="xs">Saving…</Text> : null}
      {error !== undefined ? <APIProblemView error={error} /> : null}
    </Stack>
  </Notification>;

  return <Paper component="article" aria-label={notice.title} withBorder p="md" radius="sm"
    style={{ borderInlineStart: `4px solid var(--mantine-color-${color}-6)` }}>
    <Group align="flex-start" wrap="nowrap" gap="sm">
      <ThemeIcon variant="light" color={color} radius="xl" size={24} aria-hidden="true">{notice.severity === 'info' ? 'i' : '!'}</ThemeIcon>
      <Stack gap="xs" style={{ flex: 1, minWidth: 0 }}>
        <Group gap="xs">
          <Title order={2} size="md" style={{ overflowWrap: 'anywhere' }}>{notice.title}</Title>
          <Badge variant="light" color={color}>{labels[notice.severity]}</Badge>
          <Badge variant="outline" color="gray">{notice.dismissedAt ? 'Dismissed' : expired ? 'Expired' : notice.readAt ? 'Read' : 'Unread'}</Badge>
        </Group>
        <Text size="sm" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{notice.body}</Text>
        <>
          <Group gap="md">
            {notice.effectiveAt ? <Text size="xs" c="dimmed">Published {dateLabel(notice.effectiveAt)}</Text> : null}
            {notice.expiresAt ? <Text size="xs" c="dimmed">{expired ? 'Expired' : 'Expires'} {dateLabel(notice.expiresAt)}</Text> : null}
          </Group>
          {active && !notice.readAt && canRead ? <Button variant="subtle" size="compact-sm" loading={saving}
            style={{ alignSelf: 'flex-start' }} onClick={() => void save('read')}>Mark as read</Button> : null}
        </>
        {saving ? <Text role="status" size="xs">Saving…</Text> : null}
        {error !== undefined ? <APIProblemView error={error} /> : null}
      </Stack>
      {canDismiss ? <CloseButton aria-label={`Dismiss ${notice.title}`} title="Dismiss notice" disabled={saving}
        onClick={() => void save('dismissed')} /> : null}
    </Group>
  </Paper>;
}
