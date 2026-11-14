'use client';

import { Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { ActivePasswordForm } from '../../features/session/password';
import { GitConnection } from '../../features/git/connection';
import { SignInChoices } from '../../features/session/sign-in';
import { useSession } from '../../lib/use-session';

export default function Account() {
  const session = useSession();
  const current = session.session;
  const canChange = current?.installationState === 'active' && current.permissions.includes('session.change_password');
  const canManageGit = current?.mode === 'normal' && current.installationState === 'active' && current.permissions.includes('git.manage');
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
      <Title order={1}>Account</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {canChange ? <ActivePasswordForm key={current.user.id} csrfToken={current.csrfToken} /> : null}
      {canManageGit ? <GitConnection key={current.user.id} userID={current.user.id} csrfToken={current.csrfToken} /> : null}
    </ShiftBoxShell>
  );
}
