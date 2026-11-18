import { useState } from 'react';
import { Button, Group, NativeSelect, NumberInput, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import type { components } from '../../lib/api-types';
import { validLimit } from '../../lib/use-infrastructure';

type SessionTimeout = components['schemas']['SessionTimeoutSettings'];
type Unit = SessionTimeout['unit'];

const endpoint = '/api/v1/admin/settings/session';
const units: { value: Unit; label: string }[] = [
  { value: 'minutes', label: 'Minutes' },
  { value: 'hours', label: 'Hours' },
  { value: 'days', label: 'Days' },
];
const limits: Record<Unit, number> = { minutes: 576_000, hours: 9_600, days: 400 };

export function SessionTimeoutEditor({ userId, csrfToken, active }: { userId: string; csrfToken: string; active: boolean }) {
  const [error, setError] = useState<unknown>();
  const [saved, setSaved] = useState(false);
  const timeout = useQuery<SessionTimeout>({
    queryKey: ['session-timeout', userId],
    enabled: active,
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });

  async function save(values: components['schemas']['SessionTimeout'], version: number) {
    setError(undefined);
    setSaved(false);
    try {
      const response = await fetch(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${version}"` },
        body: JSON.stringify(values),
      });
      if (!response.ok) {
        const problem = await readProblem(response);
        if (response.status === 412) await timeout.refetch();
        throw problem;
      }
      setSaved(true);
      await timeout.refetch();
    } catch (err) {
      setError(err);
    }
  }

  return <Stack gap="md">
    <Stack gap={4}>
      <Title order={2} size="h3" id="session-settings-heading">Session timeout</Title>
      <Text size="sm" c="dimmed">Set how long users stay signed in. Changes take effect the next time they sign in.</Text>
    </Stack>
    {timeout.isPending ? <Text role="status" size="sm">Loading session timeout…</Text> : null}
    {timeout.isError ? <Stack gap="sm">
      <APIProblemView error={timeout.error} />
      <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void timeout.refetch()}>Retry</Button>
    </Stack> : timeout.data ? <SessionTimeoutForm key={timeout.data.version} timeout={timeout.data} save={save}
      onEdit={() => { setSaved(false); setError(undefined); }} /> : null}
    {error !== undefined ? <APIProblemView error={error} /> : null}
    {saved ? <Text role="status" size="sm">Session timeout saved.</Text> : null}
  </Stack>;
}

function SessionTimeoutForm({ timeout, save, onEdit }: {
  timeout: SessionTimeout;
  save: (values: components['schemas']['SessionTimeout'], version: number) => Promise<void>;
  onEdit: () => void;
}) {
  const form = useForm<{ value: number | string; unit: Unit }>({
    mode: 'uncontrolled',
    initialValues: { value: timeout.value, unit: timeout.unit },
    onValuesChange: onEdit,
    validate: {
      value: (value, values) => validLimit(value) ?? (Number(value) > limits[values.unit] ? 'Choose a timeout of 400 days or less.' : null),
    },
  });

  return <form noValidate onSubmit={form.onSubmit(({ value, unit }) => save({ value: Number(value), unit }, timeout.version),
    () => form.getInputNode('value')?.focus())}>
    <Stack gap="sm" maw={480}>
      <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
        <NumberInput label="Session timeout" required allowDecimal={false} allowNegative={false}
          key={form.key('value')} disabled={form.submitting} {...form.getInputProps('value')} />
        <NativeSelect label="Time unit" data={units}
          key={form.key('unit')} disabled={form.submitting} {...form.getInputProps('unit')} />
      </SimpleGrid>
      {form.isDirty() ? <Text size="sm" role="status">You have unsaved changes.</Text> : null}
      <Group>
        <Button type="submit" disabled={!form.isDirty()} loading={form.submitting}>Save session timeout</Button>
        <Button variant="default" disabled={!form.isDirty() || form.submitting} onClick={() => { form.reset(); onEdit(); }}>Discard changes</Button>
      </Group>
    </Stack>
  </form>;
}
