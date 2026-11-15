'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../components/app-shell';
import { ActivePasswordForm } from '../features/session/password';
import { SignInChoices } from '../features/session/sign-in';
import { useSession } from '../lib/use-session';

export default function Home() {
  const router = useRouter();
  const session = useSession();
  const current = session.session;
  const passwordRequired = current?.mode === 'password_change' && current.installationState === 'active' &&
    current.permissions.includes('session.change_password');
  const destination = current && !passwordRequired
    ? current.mode === 'recovery_review' ? '/recovery/' : '/workspaces/'
    : undefined;
  useEffect(() => {
    if (destination) router.replace(destination);
  }, [destination, router]);

  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.isError ? session.error : undefined}>
      {passwordRequired
        ? <><Title order={1}>Change your password</Title><ActivePasswordForm key={current.user.id} csrfToken={current.csrfToken} /></>
        : session.data === null
        ? <><Title order={1}>Log in to ShiftBox</Title><SignInChoices /></>
        : session.isPending ? <Text role="status">Checking your session…</Text> : null}
    </ShiftBoxShell>
  );
}
