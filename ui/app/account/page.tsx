'use client';

import { Button, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { ActivePasswordForm } from '../../features/session/password';
import { useSession } from '../../lib/use-session';

export default function Account() {
  const session = useSession();
  const current = session.session;
  const canChange = current?.installationState === 'active' && current.permissions.includes('session.change_password');
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
      <Title order={1}>Account</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button> : null}
      {canChange ? <ActivePasswordForm key={current.user.id} csrfToken={current.csrfToken} /> : null}
    </ShiftBoxShell>
  );
}
