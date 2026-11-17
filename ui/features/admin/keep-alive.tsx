import { useState } from 'react';
import { Button, Group, Select, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { formatKeepAlive, keepAliveNotation, parseKeepAlive, parseKeepAliveOptions, type KeepAlivePolicy } from '../../lib/keep-alive';

export function KeepAliveSettings({ policy, csrfToken, refresh }: {
  policy: KeepAlivePolicy; csrfToken?: string; refresh: () => Promise<unknown>;
}) {
  const [error, setError] = useState<unknown>();
  const [saved, setSaved] = useState(false);
  return <Stack gap="md" pt="md">
    <Stack gap={4}>
      <Title order={3} size="h4">Inactivity shutdown</Title>
      <Text size="sm" c="dimmed">Set how long workspaces can remain inactive before stopping. Workspace files and disks are preserved.</Text>
      <Text size="sm" c="dimmed">The default applies to workspaces using the administrator default. Removed choices also fall back to the default.</Text>
    </Stack>
    {csrfToken ? <KeepAliveForm key={policy.version} policy={policy} csrfToken={csrfToken} refresh={refresh}
      report={(error, saved) => { setError(error); setSaved(saved); }} /> : <Stack gap={4}>
      <Text size="sm">Maximum: {formatKeepAlive(policy.maximumMinutes)} · Default: {formatKeepAlive(policy.defaultMinutes)}</Text>
      <Text size="sm" c="dimmed">Choices: {policy.allowedMinutes.map(formatKeepAlive).join(', ')}</Text>
    </Stack>}
    {error !== undefined ? <APIProblemView error={error} /> : null}
    {saved ? <Text role="status">Keep-alive settings saved.</Text> : null}
  </Stack>;
}

function KeepAliveForm({ policy, csrfToken, refresh, report }: {
  policy: KeepAlivePolicy; csrfToken: string; refresh: () => Promise<unknown>;
  report: (error: unknown, saved: boolean) => void;
}) {
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: {
      maximum: keepAliveNotation(policy.maximumMinutes),
      allowed: policy.allowedMinutes.map(keepAliveNotation).join(', '),
      defaultMinutes: String(policy.defaultMinutes),
    },
    onValuesChange: () => report(undefined, false),
    validate: values => {
      const maximum = parseKeepAlive(values.maximum);
      const allowed = parseKeepAliveOptions(values.allowed);
      return {
        maximum: maximum === null ? 'Enter a positive duration such as 30m, 2h, 1d, or Unlimited.' : null,
        allowed: !allowed ? 'Enter distinct durations separated by commas, such as 30m, 1h, Unlimited.' :
          maximum !== null && maximum > 0 && allowed.some(value => value === 0 || value > maximum)
            ? 'Every choice must be within the maximum. Remove Unlimited when using a finite maximum.' : null,
        defaultMinutes: allowed?.includes(Number(values.defaultMinutes)) ? null : 'Choose a default from the allowed values.',
      };
    },
  });
  const allowed = parseKeepAliveOptions(form.useWatchValue('allowed'));
  const options = (allowed ?? []).map(value => ({ value: String(value), label: formatKeepAlive(value) }));
  const save = async (values: typeof form.values) => {
    report(undefined, false);
    try {
      const response = await fetch('/api/v1/admin/keep-alive', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${policy.version}"` },
        body: JSON.stringify({ maximumMinutes: parseKeepAlive(values.maximum), allowedMinutes: parseKeepAliveOptions(values.allowed), defaultMinutes: Number(values.defaultMinutes) }),
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
  return <form noValidate onSubmit={form.onSubmit(save, errors => {
    const first = Object.keys(errors)[0];
    if (first) form.getInputNode(first)?.focus();
  })}>
    <Stack gap="md">
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <TextInput label="Maximum inactivity timeout" description="Use m for minutes, h for hours, d for days, or Unlimited for no upper limit."
          key={form.key('maximum')} disabled={form.submitting} {...form.getInputProps('maximum')} />
        <Select label="Default keep-alive" description="Applied unless a workspace selects another allowed value." data={options} allowDeselect={false}
          key={form.key('defaultMinutes')} disabled={form.submitting} {...form.getInputProps('defaultMinutes')} />
      </SimpleGrid>
      <TextInput label="Allowed keep-alive values" description="Separate choices with commas: 30m, 1h, 2h, 4h, 8h, 12h, Unlimited. Unlimited disables automatic shutdown."
        key={form.key('allowed')} disabled={form.submitting} {...form.getInputProps('allowed')} />
      {form.isDirty() ? <Text size="sm" role="status">You have unsaved keep-alive changes.</Text> : null}
      <Group>
        <Button type="submit" disabled={!form.isDirty()} loading={form.submitting}>Save keep-alive settings</Button>
        <Button variant="default" disabled={!form.isDirty() || form.submitting} onClick={() => { form.reset(); report(undefined, false); }}>Discard keep-alive changes</Button>
      </Group>
    </Stack>
  </form>;
}
