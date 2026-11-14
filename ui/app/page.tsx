'use client';

import { Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell, type ShellSession } from '../components/app-shell';

interface SessionView extends Omit<ShellSession, 'role'> {
  user: { role: ShellSession['role'] };
}

export default function Home() {
  const session = useQuery<SessionView | null>({
    queryKey: ['session'],
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/session', { signal, credentials: 'same-origin', cache: 'no-store' });
      if (response.status === 401) return null;
      if (!response.ok) throw new Error('Unable to connect to ShiftBox');
      return response.json();
    },
  });
  const current = !session.isError && session.data ? { ...session.data, role: session.data.user.role } : undefined;
  return (
    <ShiftBoxShell session={current} alerts={session.isError ? [{ id: 'connection', title: 'Unable to connect to ShiftBox', detail: 'Check your connection and try again.' }] : []}>
      <Title order={1}>Your development environments</Title>
      <Text>{session.isPending ? 'Checking your session…' : session.data === null ? 'Sign in to continue.' : current ? 'Choose a destination from the navigation.' : 'Your session could not be checked.'}</Text>
    </ShiftBoxShell>
  );
}
