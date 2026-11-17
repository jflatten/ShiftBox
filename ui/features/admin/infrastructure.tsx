'use client';

import { useState } from 'react';
import { Button, Group, NumberInput, Paper, SimpleGrid, Stack, Tabs, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell } from '../../components/app-shell';
import { readProblem } from '../../lib/api-problem';
import { providerLabel } from '../../lib/provider-label';
import { useInfrastructure, validLimit, type Capacity, type InfrastructureStatus } from '../../lib/use-infrastructure';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { BrowserCertificate } from './browser-certificate';
import { Maintenance } from './maintenance';
import { ResourceProfiles } from './resource-profiles';
import { Secrets } from '../secrets/secrets';

const capacityFields = [
  { name: 'cpuCount', label: 'CPU limit', description: 'Total virtual CPUs available to workspaces.' },
  { name: 'memoryMiB', label: 'Memory limit (MiB)', description: 'Total memory budget. 1,024 MiB = 1 GiB.' },
  { name: 'diskMiB', label: 'Disk limit (MiB)', description: 'Total disk budget, including stopped workspaces.' },
] as const;

export function Infrastructure() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.role === 'administrator' && current.mode === 'normal' &&
    current.installationState === 'active' && current.permissions.includes('infrastructure.view');
  const status = useInfrastructure(allowed ? current.user.id : undefined);
  const { data, alerts } = status;
  const canManage = allowed && current.permissions.includes('infrastructure.manage');
  const canMaintain = allowed && current.permissions.includes('maintenance.manage');
  const canManageSecrets = allowed && !!current.organizationSecretScopeId && current.permissions.includes('secret.manage_organization');
  const [section, setSection] = useState<string | null>('resources');
  const sections = ['resources', ...(canManage ? ['certificates'] : []), ...(canMaintain ? ['maintenance'] : []), ...(canManageSecrets ? ['secrets'] : [])];
  const activeSection = section && sections.includes(section) ? section : 'resources';
  return (
    <ShiftBoxShell session={current} alerts={alerts} error={session.error ?? (allowed ? status.error : undefined) ?? undefined}>
      <Stack gap={4}>
        <Title order={1}>Infrastructure</Title>
        <Text c="dimmed" size="sm">Manage workspace resources, browser security, and guest updates.</Text>
      </Stack>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {current && !allowed ? <Text>Administrator access required.</Text> : null}
      {allowed && status.isPending ? <Text role="status">Loading infrastructure status…</Text> : null}
      {allowed && status.isError ? (
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void status.refetch()}>Retry</Button>
      ) : null}
      {allowed && current ? (
        // Keep drafts and live operation subscriptions when switching settings.
        // Persistent conditions remain outside the tabs in the shared shell.
        <Tabs value={activeSection} onChange={setSection} keepMounted keepMountedMode="display-none">
          <Tabs.List aria-label="Infrastructure settings">
            <Tabs.Tab value="resources">Platform</Tabs.Tab>
            {canManage ? <Tabs.Tab value="certificates">Certificates</Tabs.Tab> : null}
            {canMaintain ? <Tabs.Tab value="maintenance">Maintenance</Tabs.Tab> : null}
            {canManageSecrets ? <Tabs.Tab value="secrets">Secrets</Tabs.Tab> : null}
          </Tabs.List>
          <Tabs.Panel value="resources" pt="lg">
            <Stack gap="lg">
              {data ? <>
                <ProviderDetails provider={data.provider} />
                <Paper component="section" aria-labelledby="capacity-heading" withBorder radius="md" p="lg">
                  <Stack gap="md">
                    <Stack gap={4}>
                      <Title order={2} size="h3" id="capacity-heading">Capacity budget</Title>
                      <Text size="sm" c="dimmed">Set the total resources ShiftBox can reserve across workspaces. These limits are budgets, not live usage.</Text>
                      <Text size="sm" c="dimmed">CPU and memory remain reserved until a confirmed stop. Disk remains allocated until deletion.</Text>
                    </Stack>
                    {canManage
                      ? <CapacityBudget status={data} csrfToken={current.csrfToken} refresh={status.refetch} />
                      : <Text>{data.capacity.cpuCount} vCPU · {data.capacity.memoryMiB} MiB memory · {data.capacity.diskMiB} MiB disk</Text>}
                  </Stack>
                </Paper>
              </> : null}
              <ResourceProfiles key={current.user.id} userId={current.user.id} provider={data?.provider} csrfToken={canManage ? current.csrfToken : undefined} />
            </Stack>
          </Tabs.Panel>
          {canManage ? <Tabs.Panel value="certificates" pt="lg">
            <Paper withBorder radius="md" p="lg">
              <BrowserCertificate key={`certificate:${current.user.id}`} csrfToken={current.csrfToken} refresh={status.refetch} />
            </Paper>
          </Tabs.Panel> : null}
          {canMaintain ? <Tabs.Panel value="maintenance" pt="lg">
            <Paper withBorder radius="md" p="lg">
              <Maintenance key={`maintenance:${current.user.id}`} userId={current.user.id} csrfToken={current.csrfToken} capacity={data?.capacity} />
            </Paper>
          </Tabs.Panel> : null}
          {canManageSecrets && current.organizationSecretScopeId ? <Tabs.Panel value="secrets" pt="lg">
            <Paper withBorder radius="md" p="lg">
              <Secrets key={current.user.id + current.organizationSecretScopeId} scope="organization" scopeID={current.organizationSecretScopeId}
                userID={current.user.id} csrfToken={current.csrfToken} />
              <Text size="sm" c="dimmed" mt="md">Organization values are used when no personal or project-template override applies. Saved secret values cannot be viewed.</Text>
            </Paper>
          </Tabs.Panel> : null}
        </Tabs>
      ) : null}
    </ShiftBoxShell>
  );
}

function ProviderDetails({ provider }: { provider: InfrastructureStatus['provider'] }) {
  const architectureLabels: Record<string, string> = { amd64: 'x86-64 (amd64)', arm64: 'ARM64' };
  const architectures = provider.architectures ?? [provider.architecture];
  const details = [
    ['Provider', providerLabel(provider.name)],
    ['Version', provider.version || 'Unavailable'],
    ['Architecture', architectures.map(architecture => architectureLabels[architecture] ?? architecture).join(', ') || 'Unavailable'],
    ['Network modes', provider.networkModes?.map(mode => mode === 'shared' ? 'Shared' : mode).join(', ') || 'Unavailable'],
  ];
  return <Paper component="section" aria-labelledby="provider-heading" withBorder radius="md" p="lg">
    <Stack gap="md">
      <Stack gap={4}>
        <Title order={2} size="h3" id="provider-heading">Provider</Title>
        <Text size="sm" c="dimmed">The configured VM provider and its reported capabilities.</Text>
      </Stack>
      <SimpleGrid component="dl" cols={{ base: 1, xs: 2, lg: 4 }} m={0} spacing="md">
        {details.map(([label, value]) => <Stack key={label} gap={4}>
          <Text component="dt" size="xs" c="dimmed">{label}</Text>
          <Text component="dd" size="sm" fw={600} m={0} style={{ overflowWrap: 'anywhere' }}>{value}</Text>
        </Stack>)}
      </SimpleGrid>
    </Stack>
  </Paper>;
}

function CapacityBudget({ status, csrfToken, refresh }: {
  status: InfrastructureStatus;
  csrfToken: string;
  refresh: () => Promise<{ data?: InfrastructureStatus; isError: boolean }>;
}) {
  const [error, setError] = useState<unknown>();
  const [saved, setSaved] = useState(false);
  return (
    <Stack gap="sm">
      <CapacityForm key={status.version} status={status} csrfToken={csrfToken} refresh={refresh} report={(error, saved) => {
        setError(error);
        setSaved(saved);
      }} />
      {error !== undefined ? <APIProblemView error={error} /> : null}
      {saved ? <Text role="status">Capacity budget saved.</Text> : null}
    </Stack>
  );
}

function CapacityForm({ status, csrfToken, refresh, report }: {
  status: InfrastructureStatus;
  csrfToken: string;
  refresh: () => Promise<{ data?: InfrastructureStatus; isError: boolean }>;
  report: (error: unknown, saved: boolean) => void;
}) {
  const form = useForm<Record<keyof Capacity, string | number>>({
    mode: 'uncontrolled',
    initialValues: status.capacity,
    onValuesChange: () => report(undefined, false),
    validate: { cpuCount: validLimit, memoryMiB: validLimit, diskMiB: validLimit },
  });
  const save = async (values: typeof form.values) => {
    report(undefined, false);
    try {
      const response = await fetch('/api/v1/admin/capacity', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${status.version}"` },
        body: JSON.stringify(values),
      });
      if (!response.ok) {
        const problem = await readProblem(response);
        if (response.status === 412) await refresh();
        throw problem;
      }
      await refresh();
      report(undefined, true);
    } catch (error) {
      report(error, false);
    }
  };
  return (
    <form noValidate onSubmit={form.onSubmit(save, errors => {
      const first = Object.keys(errors)[0];
      if (first) form.getInputNode(first)?.focus();
    })}>
      <Stack gap="md">
        <SimpleGrid cols={{ base: 1, md: 3 }} spacing="md">
          {capacityFields.map(({ name, label, description }) => (
            <NumberInput key={form.key(name)} label={label} description={description} required allowNegative={false} allowDecimal={false}
              disabled={form.submitting} {...form.getInputProps(name)} />
          ))}
        </SimpleGrid>
        {form.isDirty() ? <Text size="sm" role="status">You have unsaved capacity changes.</Text> : null}
        <Group>
          <Button type="submit" disabled={!form.isDirty()} loading={form.submitting}>Save capacity budget</Button>
          <Button variant="default" disabled={!form.isDirty() || form.submitting} onClick={() => {
            form.reset();
            report(undefined, false);
          }}>Discard changes</Button>
        </Group>
      </Stack>
    </form>
  );
}
