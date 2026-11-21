import { useState } from 'react';
import { Button, Group, NumberInput, Paper, Stack, Text, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import type { components } from '../../lib/api-types';

type Retention = NonNullable<components['schemas']['InfrastructureStatus']['launchLogRetention']>;

export function LaunchLogRetention({ settings, csrfToken, refresh }: {
  settings: Retention; csrfToken?: string; refresh: () => Promise<unknown>;
}) {
  const [saved, setSaved] = useState(false);
  return <Paper component="section" aria-label="Launch log retention" withBorder radius="md" p="lg">
    <Stack gap="sm">
      <Title order={2} size="h3">Launch log retention</Title>
      <Text size="sm" c="dimmed">Every four hours, ShiftBox removes expired diagnostics for completed launches.</Text>
      {csrfToken ? <RetentionForm key={settings.version} settings={settings} csrfToken={csrfToken} refresh={refresh} reportSaved={setSaved} /> :
        <Text size="sm">{settings.days} days</Text>}
      {saved ? <Text size="sm" role="status">Launch log retention saved.</Text> : null}
    </Stack>
  </Paper>;
}

function RetentionForm({ settings, csrfToken, refresh, reportSaved }: {
  settings: Retention; csrfToken: string; refresh: () => Promise<unknown>; reportSaved: (saved: boolean) => void;
}) {
  const [days, setDays] = useState<number | string>(settings.days);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const valid = typeof days === 'number' && Number.isInteger(days) && days >= 1 && days <= 3650;

  async function save() {
    if (!valid || days === settings.days || saving) return;
    setSaving(true);
    setError(undefined);
    reportSaved(false);
    try {
      const response = await fetch('/api/v1/admin/launch-log-retention', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'If-Match': JSON.stringify(String(settings.version)), 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ days }),
      });
      if (!response.ok) throw await readProblem(response);
      await refresh();
      reportSaved(true);
    } catch (error) {
      setError(error);
    } finally {
      setSaving(false);
    }
  }

  return <Stack component="form" align="flex-start" onSubmit={event => { event.preventDefault(); void save(); }}>
    <NumberInput label="Retention days" description="1 to 3,650 days" required value={days}
      min={1} max={3650} allowDecimal={false} clampBehavior="none" disabled={saving}
      onChange={value => { setDays(value); setError(undefined); reportSaved(false); }}
      error={!valid && days !== '' ? 'Enter between 1 and 3,650 days.' : undefined} />
    {error !== undefined ? <APIProblemView error={error} /> : null}
    <Group>
      <Button type="submit" disabled={!valid || days === settings.days} loading={saving}>Save log retention</Button>
    </Group>
  </Stack>;
}
