'use client';

import { Paper, Stack, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { useInfrastructure } from '../../lib/use-infrastructure';
import { useSession } from '../../lib/use-session';
import { SignInChoices } from '../session/sign-in';
import { GitHubAppSettings } from './github-app';

export function AdministrationIntegrations() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.role === 'administrator' && current.mode === 'normal' &&
    current.installationState === 'active' && current.permissions.includes('infrastructure.manage');
  const status = useInfrastructure(allowed && current.permissions.includes('infrastructure.view') ? current.user.id : undefined);

  return <ShiftBoxShell session={current} alerts={status.alerts} error={session.error ?? undefined}>
    <Title order={1}>Integrations</Title>
    {session.isPending ? <Text role="status">Checking your session…</Text> : null}
    {session.data === null ? <SignInChoices /> : null}
    {current && !allowed ? <Text>Administrator access required.</Text> : null}
    {allowed ? <Stack gap="lg">
      <Paper component="section" aria-labelledby="github-app-settings-heading" withBorder radius="md" p="lg">
        <GitHubAppSettings key={current.user.id} userId={current.user.id} csrfToken={current.csrfToken} />
      </Paper>
    </Stack> : null}
  </ShiftBoxShell>;
}
