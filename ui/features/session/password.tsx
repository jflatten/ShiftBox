'use client';

import { useState } from 'react';
import { Button, PasswordInput, Stack } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

export function ActivePasswordForm({ csrfToken }: { csrfToken: string }) {
  const client = useQueryClient();
  return <PasswordForm csrfToken={csrfToken} onChanged={() => {
    void client.cancelQueries({ queryKey: ['session'], exact: true });
    client.setQueryData(['session'], null);
  }} />;
}

export function PasswordForm({ csrfToken, onChanged }: { csrfToken: string; onChanged: () => void }) {
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { currentPassword: '', newPassword: '' },
    validate: {
      currentPassword: value => value ? null : 'Enter your current password.',
      newPassword: (value, values) => [...value].length >= 12 && new TextEncoder().encode(value).length <= 72 &&
        value !== values.currentPassword ? null : 'Choose a different password with at least 12 characters and at most 72 UTF-8 bytes.',
    },
  });
  const [error, setError] = useState<unknown>();
  const change = async (values: typeof form.values) => {
    setError(undefined);
    try {
      const response = await fetch('/api/v1/session/password', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify(values),
      });
      if (!response.ok) throw await readProblem(response);
      form.reset();
      onChanged();
    } catch (error) {
      setError(error);
    }
  };
  return (
    <form noValidate onSubmit={form.onSubmit(change, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
      <Stack maw={480}>
        <PasswordInput label="Current password" autoComplete="current-password" required disabled={form.submitting}
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide current password' }}
          key={form.key('currentPassword')} {...form.getInputProps('currentPassword')} />
        <PasswordInput label="New password" autoComplete="new-password" required minLength={12} disabled={form.submitting}
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide new password' }}
          key={form.key('newPassword')} {...form.getInputProps('newPassword')} />
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Change password</Button>
      </Stack>
    </form>
  );
}
