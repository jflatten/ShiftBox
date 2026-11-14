'use client';

import { Button, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../components/app-shell';
import { ActivePasswordForm } from '../features/session/password';
import { useSession } from '../lib/use-session';

export default function Home() {
  const session = useSession();
  const current = session.session;
  const passwordRequired = current?.mode === 'password_change' && current.installationState === 'active' &&
    current.permissions.includes('session.change_password');
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.isError ? session.error : undefined}>
      <Title order={1}>{passwordRequired ? 'Change your password' : 'Your development environments'}</Title>
      {passwordRequired
        ? <ActivePasswordForm key={current.user.id} csrfToken={current.csrfToken} />
        : session.data === null
        ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button>
        : <Text>{session.isPending ? 'Checking your session…' : current ? 'Choose a destination from the navigation.' : 'Your session could not be checked.'}</Text>}
    </ShiftBoxShell>
  );
}
