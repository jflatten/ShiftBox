'use client';

import { useState, type ReactNode } from 'react';
import { Alert, AppShell, Burger, Button, Group, Image, NavLink, Stack, useComputedColorScheme } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from './api-problem';
import { ExternalReauthorization } from '../features/session/external-reauthorization';
import { useAuthorizationExpired } from '../lib/use-authorization-expired';
import { readProblem } from '../lib/api-problem';
import { SignInChoices } from '../features/session/sign-in';

export interface ShellSession {
  role: 'member' | 'administrator';
  mode: string;
  installationState: string;
  permissions: string[];
  csrfToken?: string;
  externalAuthorizationDueAt?: string | null;
}

/** Shared navigation is a convenience; every destination still enforces server authorization. */
export function ShiftBoxShell({ session, alerts, error, children }: {
  session?: ShellSession;
  alerts: { id: string; title: string; detail?: string; color?: string }[];
  error?: unknown;
  children: ReactNode;
}) {
  const [opened, setOpened] = useState(false);
  const [signedOut, setSignedOut] = useState(false);
  const scheme = useComputedColorScheme('light');
  const permitted = (action: string) => session?.permissions.includes(action);
  const canRenew = permitted('session.external_reauthorize') && !!session?.csrfToken;
  const deadline = Date.parse(session?.externalAuthorizationDueAt ?? '');
  const expired = useAuthorizationExpired(deadline);
  const renewalRequired = canRenew && (expired || !Number.isFinite(deadline));
  const normal = !signedOut && session?.mode === 'normal' && session.installationState === 'active' && !renewalRequired;
  const account = (normal && (permitted('git.manage') || permitted('secret.manage_user'))) ||
    (!signedOut && !renewalRequired && session?.installationState === 'active' && permitted('session.change_password'));
  const recovery = !signedOut && session?.mode === 'recovery_review' &&
    session.installationState === 'recovery' && permitted('recovery.review');
  const backup = normal && (permitted('backup.create') || permitted('restore.validate'));
  return (
    <AppShell header={{ height: 72 }} navbar={{ width: 240, breakpoint: 'sm', collapsed: { mobile: !opened } }} padding="lg">
      <AppShell.Header>
        <Group h="100%" px="lg">
          <Burger opened={opened} onClick={() => setOpened(value => !value)} hiddenFrom="sm" aria-label="Toggle navigation" aria-expanded={opened} />
          <Image src={`/brand/shiftbox-logo-${scheme}.svg`} alt="ShiftBox" w={160} h={40} fit="contain" />
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="md" component="nav" aria-label="Main navigation">
        {normal ? <NavLink href="/workspaces/" label="Workspaces" /> : null}
        {normal ? <NavLink href="/templates/" label="Templates" /> : null}
        {normal && permitted('notice.list') ? <NavLink href="/notices/" label="Notices" /> : null}
        {account ? <NavLink href="/account/" label="Account" /> : null}
        {!signedOut && !renewalRequired && session?.role === 'administrator' && permitted('infrastructure.view') ? <NavLink href="/infrastructure/" label="Infrastructure" /> : null}
        {normal && session?.role === 'administrator' && permitted('user.list') ? <NavLink href="/users/" label="People" /> : null}
        {normal && session?.role === 'administrator' && permitted('audit.list') ? <NavLink href="/audit/" label="Audit" /> : null}
        {backup || recovery ? <NavLink href="/recovery/" label="Backup & Recovery" /> : null}
        {!signedOut && permitted('session.logout') && session?.csrfToken ? (
          <AppShell.Section mt="auto" pt="md">
            <SignOut csrfToken={session.csrfToken} onSignedOut={() => setSignedOut(true)} />
          </AppShell.Section>
        ) : null}
      </AppShell.Navbar>
      <AppShell.Main>
        <Stack gap="lg">
          <Stack gap="sm" role="region" aria-label="Persistent conditions">
            {error !== undefined ? <APIProblemView error={error} /> : null}
            {alerts.map(alert => <Alert key={alert.id} title={alert.title} color={alert.color ?? 'red'}>{alert.detail}</Alert>)}
            {!signedOut && canRenew && session?.csrfToken ? (
              <ExternalReauthorization key={session.csrfToken} csrfToken={session.csrfToken}
                dueAt={session.externalAuthorizationDueAt} expired={!!renewalRequired} />
            ) : null}
          </Stack>
          {!signedOut && !renewalRequired ? children : null}
          {signedOut ? <SignInChoices /> : null}
        </Stack>
      </AppShell.Main>
    </AppShell>
  );
}

function SignOut({ csrfToken, onSignedOut }: { csrfToken: string; onSignedOut: () => void }) {
  const client = useQueryClient();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();

  async function signOut() {
    if (pending) return;
    setPending(true);
    setError(undefined);
    try {
      const response = await fetch('/api/v1/session', {
        method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken }, cache: 'no-store',
      });
      if (!response.ok && response.status !== 401) throw await readProblem(response);
      onSignedOut();
      await client.cancelQueries();
      client.removeQueries({ predicate: query => query.queryKey[0] !== 'session' });
      client.setQueryData(['session'], null);
    } catch (error) {
      setError(error);
    } finally {
      setPending(false);
    }
  }

  return <Stack align="flex-start">
    {error !== undefined ? <APIProblemView error={error} /> : null}
    <Button variant="default" loading={pending} onClick={() => void signOut()}>Sign out</Button>
  </Stack>;
}
