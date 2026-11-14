import { useState } from 'react';
import { Button, NativeSelect, Stack, Text, Textarea, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

export function PublishNotice({ csrfToken }: { csrfToken: string }) {
  const client = useQueryClient();
  const [error, setError] = useState<unknown>();
  const [published, setPublished] = useState(false);
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { severity: 'info', title: '', body: '', effectiveAt: '', expiresAt: '' },
    validate: {
      title: value => value.trim() ? null : 'Enter a title.',
      body: value => value.trim() ? null : 'Enter a message.',
      expiresAt: (value, values) => value && values.effectiveAt && new Date(value) <= new Date(values.effectiveAt)
        ? 'Set expiry after the start time.' : null,
    },
  });
  const publish = async ({ severity, title, body, effectiveAt, expiresAt }: typeof form.values) => {
    setError(undefined);
    setPublished(false);
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
      form.reset();
      setPublished(true);
      await client.invalidateQueries({ queryKey: ['notices'] });
    } catch (error) {
      setError(error);
    }
  };
  return (
    <form onSubmit={form.onSubmit(publish)}>
      <Stack maw={500}>
        <Title order={2}>Publish a notice</Title>
        <NativeSelect label="Severity" data={[{ value: 'info', label: 'Information' }, { value: 'warning', label: 'Warning' },
          { value: 'critical', label: 'Critical' }]} key={form.key('severity')} {...form.getInputProps('severity')} />
        <TextInput label="Title" required maxLength={120} key={form.key('title')} {...form.getInputProps('title')} />
        <Textarea label="Message" required maxLength={4000} autosize minRows={3} key={form.key('body')} {...form.getInputProps('body')} />
        <TextInput type="datetime-local" label="Start time (optional)" key={form.key('effectiveAt')} {...form.getInputProps('effectiveAt')} />
        <TextInput type="datetime-local" label="Expiry time (optional)" key={form.key('expiresAt')} {...form.getInputProps('expiresAt')} />
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Publish notice</Button>
        {published ? <Text role="status">Notice published.</Text> : null}
        {error !== undefined ? <APIProblemView error={error} /> : null}
      </Stack>
    </form>
  );
}
