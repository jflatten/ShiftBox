'use client';

import { useState, type ReactNode } from 'react';
import { Alert, AppShell, Burger, Group, Image, NavLink, Stack, useComputedColorScheme } from '@mantine/core';
import { APIProblemView } from './api-problem';
import { ExternalReauthorization } from '../features/session/external-reauthorization';
import { useAuthorizationExpired } from '../lib/use-authorization-expired';

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
  const scheme = useComputedColorScheme('light');
  const permitted = (action: string) => session?.permissions.includes(action);
  const canRenew = permitted('session.external_reauthorize') && !!session?.csrfToken;
  const deadline = Date.parse(session?.externalAuthorizationDueAt ?? '');
  const expired = useAuthorizationExpired(deadline);
  const renewalRequired = canRenew && (expired || !Number.isFinite(deadline));
  const normal = session?.mode === 'normal' && session.installationState === 'active' && !renewalRequired;
  const account = (normal && permitted('git.manage')) ||
    (!renewalRequired && session?.installationState === 'active' && permitted('session.change_password'));
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
        {!renewalRequired && session?.role === 'administrator' && permitted('infrastructure.view') ? <NavLink href="/infrastructure/" label="Infrastructure" /> : null}
        {normal && session?.role === 'administrator' && permitted('user.list') ? <NavLink href="/users/" label="People" /> : null}
        {normal && session?.role === 'administrator' && permitted('audit.list') ? <NavLink href="/audit/" label="Audit" /> : null}
      </AppShell.Navbar>
      <AppShell.Main>
        <Stack gap="lg">
          <Stack gap="sm" role="region" aria-label="Persistent conditions">
            {error !== undefined ? <APIProblemView error={error} /> : null}
            {alerts.map(alert => <Alert key={alert.id} title={alert.title} color={alert.color ?? 'red'}>{alert.detail}</Alert>)}
            {canRenew && session?.csrfToken ? (
              <ExternalReauthorization key={session.csrfToken} csrfToken={session.csrfToken}
                dueAt={session.externalAuthorizationDueAt} expired={!!renewalRequired} />
            ) : null}
          </Stack>
          {!renewalRequired ? children : null}
        </Stack>
      </AppShell.Main>
    </AppShell>
  );
}
