'use client';

import { Stack, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { Backup } from '../../features/recovery/backup';
import { Restore } from '../../features/recovery/restore';
import { RecoveryReview } from '../../features/recovery/review';
import { SignInChoices } from '../../features/session/sign-in';
import { useSession } from '../../lib/use-session';

export default function RecoveryPage() {
  const session = useSession();
  const current = session.session;
  const active = current?.role === 'administrator' && current.mode === 'normal' && current.installationState === 'active';
  const canBackup = active && current.permissions.includes('backup.create');
  const canRestore = active && current.permissions.includes('restore.validate');
  const reviewId = current?.role === 'administrator' && current.mode === 'recovery_review' && current.installationState === 'recovery'
    && current.permissions.includes('recovery.review') ? current.recoveryReviewId : undefined;
  return <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
    <Title order={1}>Backup &amp; Recovery</Title>
    {session.isPending ? <Text role="status">Checking your session…</Text> : null}
    {session.data === null ? <SignInChoices /> : null}
    {current && !canBackup && !canRestore && !reviewId ? <Text>Administrator access required.</Text> : null}
    {current ? <Stack gap="xl" key={current.csrfToken}>
      {canBackup ? <Backup csrfToken={current.csrfToken} /> : null}
      {canRestore ? <Restore csrfToken={current.csrfToken} /> : null}
      {reviewId ? <RecoveryReview csrfToken={current.csrfToken} reviewId={reviewId} /> : null}
    </Stack> : null}
  </ShiftBoxShell>;
}
