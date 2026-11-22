import { useRef, useState } from 'react';
import { Button, Group, NativeSelect, SimpleGrid, Stack, Text, Textarea, TextInput } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

export function PublishNotice({ csrfToken, onCancel, onSaving, onPublished }: {
  csrfToken: string; onCancel: () => void; onSaving: (saving: boolean) => void; onPublished: (scheduled: boolean) => void;
}) {
  const client = useQueryClient();
  const [error, setError] = useState<unknown>();
  const busy = useRef(false);
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { severity: 'info', title: '', body: '', effectiveAt: '', expiresAt: '' },
    validate: {
      title: value => value.trim() ? null : 'Enter a title.',
      body: value => value.trim() ? null : 'Enter a message.',
      expiresAt: (value, values) => value && Date.parse(value) <= (values.effectiveAt ? Date.parse(values.effectiveAt) : Date.now())
        ? 'Choose an expiry after the start time.' : null,
    },
  });
  const publish = async ({ severity, title, body, effectiveAt, expiresAt }: typeof form.values) => {
    if (busy.current) return;
    busy.current = true;
    onSaving(true);
    setError(undefined);
    try {
      const response = await fetch('/api/v1/admin/notices', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({
          severity, title: title.trim(), body: body.trim(),
          ...(effectiveAt ? { effectiveAt: new Date(effectiveAt).toISOString() } : {}),
          ...(expiresAt ? { expiresAt: new Date(expiresAt).toISOString() } : {}),
        }),
      });
      if (!response.ok) throw await readProblem(response);
      await client.invalidateQueries({ queryKey: ['notices'] });
      onPublished(!!effectiveAt && Date.parse(effectiveAt) > Date.now());
    } catch (error) {
      setError(error);
    } finally {
      busy.current = false;
      onSaving(false);
    }
  };
  return (
    <form onSubmit={form.onSubmit(publish)}>
      <Stack>
        <Text size="sm" c="dimmed">This announcement appears across ShiftBox for all users. Each person can dismiss it and keep a copy in their notice history.</Text>
        <NativeSelect label="Severity" data={[{ value: 'info', label: 'Information' }, { value: 'warning', label: 'Warning' },
          { value: 'critical', label: 'Critical' }]} key={form.key('severity')} {...form.getInputProps('severity')} />
        <TextInput label="Title" data-autofocus required maxLength={120} key={form.key('title')} {...form.getInputProps('title')} />
        <Textarea label="Message" required maxLength={4000} autosize minRows={3} key={form.key('body')} {...form.getInputProps('body')} />
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <TextInput type="datetime-local" description="Leave blank to publish now. Times are local." label="Start time (optional)" key={form.key('effectiveAt')} {...form.getInputProps('effectiveAt')} />
          <TextInput type="datetime-local" description="Leave blank to keep the notice active." label="Expiry time (optional)" key={form.key('expiresAt')} {...form.getInputProps('expiresAt')} />
        </SimpleGrid>
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Group justify="flex-end">
          <Button variant="default" disabled={form.submitting} onClick={onCancel}>Cancel</Button>
          <Button type="submit" loading={form.submitting}>Publish notice</Button>
        </Group>
      </Stack>
    </form>
  );
}
