'use client';

import { Anchor, Box, Group, Paper, Portal, Text } from '@mantine/core';
import { NoticeMessage } from './notice-message';
import { useNotices } from './use-notices';

/** A persistent actionable toast floats below the header without shifting page content. */
export function NoticeToast({ userId, csrfToken, permissions }: {
  userId: string; csrfToken: string; permissions: string[];
}) {
  const notices = useNotices(userId);
  const notice = notices.data?.items.find(item => !item.dismissedAt && (!item.expiresAt || Date.parse(item.expiresAt) > Date.now()));
  if (!notice && !notices.isError) return null;
  return <Portal>
    <Box role="region" aria-label="Application notice" aria-live="polite" pos="fixed"
      top="calc(var(--app-shell-header-height, 72px) + 16px)" right={16}
      w="min(360px, calc(100vw - 32px))" style={{ zIndex: 110, maxHeight: 'calc(100dvh - 104px)', overflowY: 'auto' }}>
      {notices.isError ? <Paper withBorder shadow="md" p="md"><Group gap="xs">
        <Text size="sm">Notices could not be loaded.</Text>
        <Anchor href="/notices/" size="sm">Review notices</Anchor>
      </Group></Paper> : notice ? <NoticeMessage key={notice.id} notice={notice} userId={userId}
        csrfToken={csrfToken} permissions={permissions} compact /> : null}
    </Box>
  </Portal>;
}
