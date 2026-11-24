'use client';

import { useEffect, useState } from 'react';
import { Paper, Tabs, Text, Title } from '@mantine/core';
import { ShiftBoxShell } from '../../components/app-shell';
import { useInfrastructure } from '../../lib/use-infrastructure';
import { useSession } from '../../lib/use-session';
import { Secrets } from '../secrets/secrets';
import { SignInChoices } from '../session/sign-in';
import { BrowserCertificate } from './browser-certificate';
import { GitHubAppSettings } from './github-app';
import { SessionTimeoutEditor } from './session-timeout';

export function AdministrationSettings() {
  const session = useSession();
  const current = session.session;
  const administrator = current?.role === 'administrator' && current.mode === 'normal' && current.installationState === 'active';
  const canManage = administrator && current.permissions.includes('infrastructure.manage');
  const organization = administrator && current.permissions.includes('secret.manage_organization') ? current.organizationSecretScopeId : undefined;
  const allowed = canManage || !!organization;
  const status = useInfrastructure(allowed && current?.permissions.includes('infrastructure.view') ? current.user.id : undefined);
  const [section, setSection] = useState<string | null>(null);
  useEffect(() => {
    const fromURL = () => setSection(new URLSearchParams(window.location.search).get('section'));
    fromURL();
    window.addEventListener('popstate', fromURL);
    return () => window.removeEventListener('popstate', fromURL);
  }, []);
  const sections = [...(canManage ? ['session', 'certificates', 'github-app'] : []), ...(organization ? ['secrets'] : [])];
  const active = section && sections.includes(section) ? section : sections[0];
  const selectSection = (value: string | null) => {
    if (!value) return;
    const url = new URL(window.location.href);
    if (value === sections[0]) url.searchParams.delete('section');
    else url.searchParams.set('section', value);
    window.history.replaceState(window.history.state, '', url);
    setSection(value);
  };

  return <ShiftBoxShell session={current} alerts={status.alerts} error={session.error ?? undefined}>
    <Title order={1}>Settings</Title>
    {session.isPending ? <Text role="status">Checking your session…</Text> : null}
    {session.data === null ? <SignInChoices /> : null}
    {current && !allowed ? <Text>Administrator access required.</Text> : null}
    {allowed && current ? <Tabs value={active} onChange={selectSection} keepMounted keepMountedMode="display-none">
      <Tabs.List aria-label="Administration settings">
        {canManage ? <>
          <Tabs.Tab value="session">Sessions</Tabs.Tab>
          <Tabs.Tab value="certificates">Certificates</Tabs.Tab>
          <Tabs.Tab value="github-app">GitHub App</Tabs.Tab>
        </> : null}
        {organization ? <Tabs.Tab value="secrets">Secrets</Tabs.Tab> : null}
      </Tabs.List>
      {canManage ? <>
        <Tabs.Panel value="session" pt="lg">
          <Paper component="section" aria-labelledby="session-settings-heading" withBorder radius="md" p="lg">
            <SessionTimeoutEditor key={current.user.id} userId={current.user.id} csrfToken={current.csrfToken} active={active === 'session'} />
          </Paper>
        </Tabs.Panel>
        <Tabs.Panel value="certificates" pt="lg">
          <BrowserCertificate key={current.user.id} userId={current.user.id} active={active === 'certificates'}
            csrfToken={current.csrfToken} refresh={status.refetch} />
        </Tabs.Panel>
        <Tabs.Panel value="github-app" pt="lg">
          <Paper component="section" aria-labelledby="github-app-settings-heading" withBorder radius="md" p="lg">
            <GitHubAppSettings key={current.user.id} userId={current.user.id} active={active === 'github-app'}
              csrfToken={current.csrfToken} />
          </Paper>
        </Tabs.Panel>
      </> : null}
      {organization ? <Tabs.Panel value="secrets" pt="lg">
        <Paper withBorder radius="md" p="lg">
          <Secrets key={current.user.id + organization} scope="organization" scopeID={organization}
            userID={current.user.id} csrfToken={current.csrfToken} />
          <Text size="sm" c="dimmed" mt="md">Organization values are used when no personal or project override applies. Saved secret values cannot be viewed.</Text>
        </Paper>
      </Tabs.Panel> : null}
    </Tabs> : null}
  </ShiftBoxShell>;
}
