'use client';

import { useState, type ReactNode } from 'react';
import { usePathname } from 'next/navigation';
import { Alert, AppShell, Avatar, Burger, Group, Image, Menu, NavLink, Stack, Text, UnstyledButton, useComputedColorScheme } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from './api-problem';
import { ExternalReauthorization } from '../features/session/external-reauthorization';
import { useAuthorizationExpired } from '../lib/use-authorization-expired';
import { readProblem } from '../lib/api-problem';
import { NoticeToast } from '../features/notices/notice-toast';
import { SignInChoices } from '../features/session/sign-in';

export interface ShellSession {
  user?: { id?: string; displayName?: string | null; primaryEmail?: string };
  role: 'member' | 'administrator';
  mode: string;
  installationState: string;
  permissions: string[];
  csrfToken?: string;
  externalAuthorizationDueAt?: string | null;
}

/** Shared navigation is a convenience; every destination still enforces server authorization. */
export function ShiftBoxShell({ session, alerts, error, children, showNoticeToast = true }: {
  session?: ShellSession;
  alerts: { id: string; title: string; detail?: string; color?: string }[];
  error?: unknown;
  children: ReactNode;
  showNoticeToast?: boolean;
}) {
  const [opened, setOpened] = useState(false);
  const [signedOut, setSignedOut] = useState(false);
  const [signOutError, setSignOutError] = useState<unknown>();
  const [signOutPending, setSignOutPending] = useState(false);
  const scheme = useComputedColorScheme('light');
  const pathname = usePathname();
  const at = (href: string) => pathname === href || pathname === href.slice(0, -1) || pathname?.startsWith(href);
  const permitted = (action: string) => session?.permissions.includes(action);
  const canRenew = permitted('session.external_reauthorize') && !!session?.csrfToken;
  const deadline = Date.parse(session?.externalAuthorizationDueAt ?? '');
  const expired = useAuthorizationExpired(deadline);
  const renewalRequired = canRenew && (expired || !Number.isFinite(deadline));
  const normal = !signedOut && session?.mode === 'normal' && session.installationState === 'active' && !renewalRequired;
  const account = normal ||
    (!signedOut && !renewalRequired && session?.installationState === 'active' && permitted('session.change_password'));
  const recovery = !signedOut && session?.mode === 'recovery_review' &&
    session.installationState === 'recovery' && permitted('recovery.review');
  const backup = normal && (permitted('backup.create') || permitted('restore.validate'));
  const adminNotice = normal && session?.role === 'administrator' && permitted('notice.list');
  const adminInfrastructure = !signedOut && !renewalRequired && session?.role === 'administrator' && permitted('infrastructure.view');
  const adminUsers = normal && session?.role === 'administrator' && permitted('user.list');
  const adminAudit = normal && session?.role === 'administrator' && permitted('audit.list');
  const adminRecovery = session?.role === 'administrator' && (backup || recovery);
  const administration = adminNotice || adminInfrastructure || adminUsers || adminAudit || adminRecovery;
  return (
    <AppShell header={{ height: 72 }} navbar={{ width: 240, breakpoint: 'sm', collapsed: { mobile: !opened } }} padding="lg">
      <AppShell.Header>
        <Group h="100%" px="lg" justify="space-between" wrap="nowrap">
          <Group wrap="nowrap">
            <Burger opened={opened} onClick={() => setOpened(value => !value)} hiddenFrom="sm" aria-label="Toggle navigation" aria-expanded={opened} />
            <Image src={`/brand/shiftbox-logo-${scheme}.svg`} alt="ShiftBox" w={160} h={40} fit="contain" />
          </Group>
          {!signedOut && session ? <Menu position="bottom-end" shadow="md" width={280}
            transitionProps={{ duration: 0 }} styles={{ dropdown: { maxWidth: 'calc(100vw - 32px)' } }}>
            <Menu.Target>
              <UnstyledButton aria-label="User menu" title="Personal settings and sign out" style={{ borderRadius: 'var(--mantine-radius-md)' }}>
                <Avatar name={session.user?.displayName || session.user?.primaryEmail?.split('@')[0]} color="violet" radius="md" size={44} aria-hidden="true" />
              </UnstyledButton>
            </Menu.Target>
            <Menu.Dropdown>
              <Menu.Label>
                <Text fw={600} c="var(--mantine-color-text)" style={{ overflowWrap: 'anywhere' }}>{session.user?.displayName || session.user?.primaryEmail || 'Signed in'}</Text>
                {session.user?.displayName && session.user.primaryEmail ? <Text size="sm" style={{ overflowWrap: 'anywhere' }}>{session.user.primaryEmail}</Text> : null}
                <Text size="xs" mt={4}>{session.role === 'administrator' ? 'Administrator' : 'Member'}</Text>
              </Menu.Label>
              <Menu.Divider />
              {account ? <Menu.Item component="a" href="/account/">Personal settings</Menu.Item> : null}
              {normal && session.role === 'member' && permitted('notice.list') ?
                <Menu.Item component="a" href="/notices/">Notice history</Menu.Item> : null}
              {permitted('session.logout') && session.csrfToken ? <SignOut csrfToken={session.csrfToken}
                pending={signOutPending} setPending={setSignOutPending} onError={setSignOutError} onSignedOut={() => setSignedOut(true)} /> : null}
            </Menu.Dropdown>
          </Menu> : null}
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="md" component="nav" aria-label="Main navigation">
        {normal ? <NavLink href="/workspaces/" label="Workspaces" active={!!at('/workspaces/')}
          aria-current={at('/workspaces/') ? 'page' : undefined} /> : null}
        {normal ? <NavLink href="/templates/" label="Templates" active={!!at('/templates/')}
          aria-current={at('/templates/') ? 'page' : undefined} /> : null}
        {administration ? <NavLink component="button" type="button" label="Administration" defaultOpened childrenOffset={16}>
          {adminNotice ? <NavLink href="/notices/" label="Notices" active={!!at('/notices/')}
            aria-current={at('/notices/') ? 'page' : undefined} /> : null}
          {adminInfrastructure ? <NavLink href="/infrastructure/" label="Infrastructure" active={!!at('/infrastructure/')}
            aria-current={at('/infrastructure/') ? 'page' : undefined} /> : null}
          {adminUsers ? <NavLink href="/users/" label="Users" active={!!at('/users/')}
            aria-current={at('/users/') ? 'page' : undefined} /> : null}
          {adminAudit ? <NavLink href="/audit/" label="Audit" active={!!at('/audit/')}
            aria-current={at('/audit/') ? 'page' : undefined} /> : null}
          {adminRecovery ? <NavLink href="/recovery/" label="Backup & Recovery" active={!!at('/recovery/')}
            aria-current={at('/recovery/') ? 'page' : undefined} /> : null}
        </NavLink> : null}
      </AppShell.Navbar>
      <AppShell.Main>
        <Stack gap="lg">
          {showNoticeToast && normal && permitted('notice.list') && session?.user?.id && session.csrfToken ?
            <NoticeToast key={session.user.id} userId={session.user.id} csrfToken={session.csrfToken} permissions={session.permissions} /> : null}
          <Stack gap="sm" role="region" aria-label="Persistent conditions">
            {error !== undefined ? <APIProblemView error={error} /> : null}
            {signOutError !== undefined ? <APIProblemView error={signOutError} /> : null}
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

function SignOut({ csrfToken, onSignedOut, onError, pending, setPending }: {
  csrfToken: string; onSignedOut: () => void; onError: (error: unknown) => void;
  pending: boolean; setPending: (pending: boolean) => void;
}) {
  const client = useQueryClient();

  async function signOut() {
    if (pending) return;
    setPending(true);
    onError(undefined);
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
      onError(error);
    } finally {
      setPending(false);
    }
  }

  return <Menu.Item closeMenuOnClick={false} disabled={pending} onClick={() => void signOut()}>
    {pending ? 'Signing out…' : 'Sign out'}
  </Menu.Item>;
}
