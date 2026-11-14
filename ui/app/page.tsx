'use client';

import { Button, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../components/app-shell';
import { useSession } from '../lib/use-session';

export default function Home() {
  const session = useSession();
  const current = session.session;
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.isError ? session.error : undefined}>
      <Title order={1}>Your development environments</Title>
      {session.data === null
        ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button>
        : <Text>{session.isPending ? 'Checking your session…' : current ? 'Choose a destination from the navigation.' : 'Your session could not be checked.'}</Text>}
    </ShiftBoxShell>
  );
}
