'use client';

import { useState } from 'react';
import { Button, NumberInput, Stack, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { ShiftBoxShell } from '../../components/app-shell';
import { readProblem } from '../../lib/api-problem';
import { useSession } from '../../lib/use-session';

type Capacity = { cpuCount: number; memoryMiB: number; diskMiB: number };
type Condition = { key: string; severity: 'info' | 'warning' | 'critical'; active: boolean; detail?: string };

interface InfrastructureStatus {
  provider: { name: string; version: string; architecture: string; networkModes: string[] | null };
  capacity: Capacity;
  version: number;
  conditions: Condition[];
}

const conditionTitles: Record<string, string> = {
  'browser_certificate.expiring': 'Browser certificate expires soon',
  'browser_certificate.invalid': 'Browser certificate needs attention',
};
const conditionColors = { info: 'blue', warning: 'yellow', critical: 'red' };
const capacityFields = [
  { name: 'cpuCount', label: 'CPU limit' },
  { name: 'memoryMiB', label: 'Memory limit (MiB)' },
  { name: 'diskMiB', label: 'Disk limit (MiB)' },
] as const;

export function Infrastructure() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.role === 'administrator' && current.mode === 'normal' &&
    current.installationState === 'active' && current.permissions.includes('infrastructure.view');
  const status = useQuery<InfrastructureStatus>({
    queryKey: ['infrastructure', current?.user.id],
    enabled: allowed,
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/admin/infrastructure', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = allowed && !status.isError ? status.data : undefined;
  const alerts = (data?.conditions ?? []).filter(condition => condition.active).map(condition => ({
    id: condition.key, title: conditionTitles[condition.key] ?? 'Infrastructure condition',
    detail: condition.detail, color: conditionColors[condition.severity],
  }));
  return (
    <ShiftBoxShell session={current} alerts={alerts} error={session.error ?? (allowed ? status.error : undefined) ?? undefined}>
      <Title order={1}>Infrastructure</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button> : null}
      {current && !allowed ? <Text>Administrator access required.</Text> : null}
      {allowed && status.isPending ? <Text role="status">Loading infrastructure status…</Text> : null}
      {allowed && status.isError ? (
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void status.refetch()}>Retry</Button>
      ) : null}
      {data && current ? (
        <>
          <Stack gap="xs">
            <Title order={2}>Provider</Title>
            <Text>{[data.provider.name, data.provider.version, data.provider.architecture].filter(Boolean).join(' · ') || 'Unavailable'}</Text>
            {data.provider.networkModes?.length ? <Text>Network modes · {data.provider.networkModes.join(', ')}</Text> : null}
          </Stack>
          <Stack gap="xs">
            <Title order={2}>Capacity budget</Title>
            {current.permissions.includes('infrastructure.manage')
              ? <CapacityBudget status={data} csrfToken={current.csrfToken} refresh={status.refetch} />
              : <Text>{data.capacity.cpuCount} vCPU · {data.capacity.memoryMiB} MiB memory · {data.capacity.diskMiB} MiB disk</Text>}
          </Stack>
        </>
      ) : null}
    </ShiftBoxShell>
  );
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
  const validLimit = (value: string | number) => typeof value === 'number' && Number.isSafeInteger(value) && value > 0
    ? null : 'Enter a positive whole number.';
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
      <Stack maw={360} gap="sm">
        {capacityFields.map(({ name, label }) => (
          <NumberInput key={form.key(name)} label={label} required allowNegative={false} allowDecimal={false}
            disabled={form.submitting} {...form.getInputProps(name)} />
        ))}
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Save capacity budget</Button>
      </Stack>
    </form>
  );
}
