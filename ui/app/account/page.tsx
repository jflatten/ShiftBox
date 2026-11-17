'use client';

import { useRef, useState } from 'react';
import { Avatar, Badge, Button, Group, Modal, Paper, Stack, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { ActivePasswordForm } from '../../features/session/password';
import { GitConnection } from '../../features/git/connection';
import { Secrets } from '../../features/secrets/secrets';
import { SignInChoices } from '../../features/session/sign-in';
import { useSession } from '../../lib/use-session';

export default function Account() {
  const session = useSession();
  const current = session.session;
  const active = current?.installationState === 'active';
  const canChange = active && current.permissions.includes('session.change_password');
  const normal = active && current.mode === 'normal';
  const canManageGit = normal && current.permissions.includes('git.manage');
  const canManageSecrets = normal && current.permissions.includes('secret.manage_user');
  const showSettings = normal || canChange;
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
      <Stack gap="lg" maw={960}>
        <Stack gap={4}>
          <Title order={1}>Personal settings</Title>
          <Text size="sm" c="dimmed">Manage your sign-in, GitHub connection, and personal workspace secrets.</Text>
        </Stack>
        {session.isPending ? <Text role="status">Checking your session…</Text> : null}
        {session.isError ? <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void session.refetch()}>Retry</Button> : null}
        {session.data === null ? <SignInChoices /> : null}
        {showSettings && current ? <>
          <Paper withBorder radius="md" p="lg" component="section" aria-label="Your profile">
            <Group wrap="nowrap" align="flex-start">
              <Avatar name={current.user.displayName || current.user.primaryEmail?.split('@')[0]} color="violet" radius="md" size={52} aria-hidden="true" />
              <Stack gap={4} style={{ minWidth: 0 }}>
                <Title order={2} size="h3" style={{ overflowWrap: 'anywhere' }}>{current.user.displayName || current.user.primaryEmail || 'Your profile'}</Title>
                {current.user.displayName && current.user.primaryEmail ? <Text size="sm" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{current.user.primaryEmail}</Text> : null}
                <Group gap="xs" mt={4}>
                  <Badge variant="light">{current.role === 'administrator' ? 'Administrator' : 'Member'}</Badge>
                  {current.user.credentialMode ? <Badge variant="outline" color="gray">{current.user.credentialMode === 'local' ? 'Local sign-in' : 'Organization sign-in'}</Badge> : null}
                </Group>
                <Text size="xs" c="dimmed" mt={4}>Your profile and role are managed by your administrator or identity provider.</Text>
              </Stack>
            </Group>
          </Paper>
          <Paper withBorder radius="md" p="lg" component="section" aria-label="Sign-in & security">
            <Stack gap="md">
              <Group justify="space-between" align="flex-start">
                <Stack gap={4}>
                  <Title order={2}>Sign-in &amp; security</Title>
                  <Text size="sm" c="dimmed">{current.user.credentialMode === 'external'
                    ? 'Your sign-in credentials are managed by your organization’s identity provider.'
                    : 'Manage the password you use to sign in to ShiftBox.'}</Text>
                </Stack>
                {canChange && current.mode !== 'password_change' ? <PasswordSettings key={current.user.id} csrfToken={current.csrfToken} /> : null}
              </Group>
              {canChange && current.mode === 'password_change' ? <>
                <Text fw={600}>Change your password to continue.</Text>
                <ActivePasswordForm key={current.user.id} csrfToken={current.csrfToken} />
              </> : null}
              {!canChange && current.user.credentialMode !== 'external' ? <Text size="sm">Password changes are not available for this session.</Text> : null}
            </Stack>
          </Paper>
          {canManageGit ? <Paper withBorder radius="md" p="lg" component="section" aria-label="GitHub connection">
            <GitConnection key={current.user.id} userID={current.user.id} csrfToken={current.csrfToken}
              description="Connect GitHub to let your workspaces access repositories using your GitHub permissions. This connection is separate from your ShiftBox sign-in." />
          </Paper> : null}
          {canManageSecrets ? <Paper withBorder radius="md" p="lg" component="section" aria-label="Personal secrets">
            <Secrets key={current.user.id} scope="user" scopeID={current.user.id} userID={current.user.id} csrfToken={current.csrfToken}
              description="Personal secrets override project and organization values for your workspaces. Workspace code can retrieve these values; saved values cannot be viewed here." />
          </Paper> : null}
        </> : null}
      </Stack>
    </ShiftBoxShell>
  );
}

function PasswordSettings({ csrfToken }: { csrfToken: string }) {
  const [opened, setOpened] = useState(false);
  const [pending, setPending] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const close = () => {
    if (pending) return;
    setOpened(false);
    trigger.current?.focus();
  };
  return <>
    <Button ref={trigger} variant="default" onClick={() => setOpened(true)}>Change password</Button>
    {opened ? <Modal opened title="Change password" onClose={close} size="lg" returnFocus={false}
      closeOnEscape={!pending} closeOnClickOutside={!pending} withCloseButton={!pending}
      closeButtonProps={{ 'aria-label': 'Close password dialog' }} transitionProps={{ duration: 0 }}>
      <ActivePasswordForm csrfToken={csrfToken} onCancel={close} onPendingChange={setPending} />
    </Modal> : null}
  </>;
}
