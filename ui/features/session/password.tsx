'use client';

import { useState } from 'react';
import { Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

interface PasswordFormOptions {
  csrfToken: string;
  onCancel?: () => void;
  onPendingChange?: (pending: boolean) => void;
}

export function ActivePasswordForm({ csrfToken, onCancel, onPendingChange }: PasswordFormOptions) {
  const client = useQueryClient();
  return <PasswordForm csrfToken={csrfToken} onCancel={onCancel} onPendingChange={onPendingChange} onChanged={() => {
    void client.cancelQueries({ queryKey: ['session'], exact: true });
    client.setQueryData(['session'], null);
  }} />;
}

export function PasswordForm({ csrfToken, onChanged, onCancel, onPendingChange }: PasswordFormOptions & { onChanged: () => void }) {
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { currentPassword: '', newPassword: '', confirmPassword: '' },
    validate: {
      currentPassword: value => value ? null : 'Enter your current password.',
      newPassword: (value, values) => {
        if ([...value].length < 12) return 'Use at least 12 characters.';
        if (new TextEncoder().encode(value).length > 72) return 'This password is too long. Try a shorter one.';
        return value !== values.currentPassword ? null : 'Choose a password different from your current one.';
      },
      confirmPassword: (value, values) => !value ? 'Re-enter your new password.'
        : value === values.newPassword ? null : 'Passwords do not match. Re-enter your new password.',
    },
  });
  const [error, setError] = useState<unknown>();
  const change = async (values: typeof form.values) => {
    setError(undefined);
    onPendingChange?.(true);
    try {
      const response = await fetch('/api/v1/session/password', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        // Confirmation is a local typo check, not another credential to send.
        body: JSON.stringify({ currentPassword: values.currentPassword, newPassword: values.newPassword }),
      });
      if (!response.ok) throw await readProblem(response);
      form.reset();
      onChanged();
    } catch (error) {
      setError(error);
    } finally {
      onPendingChange?.(false);
    }
  };
  return (
    <form noValidate onSubmit={form.onSubmit(change, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
      <Stack maw={480}>
        <Text size="sm" c="dimmed">Changing your password signs you out. Sign in again with your new password.</Text>
        <PasswordInput label="Current password" autoComplete="current-password" required disabled={form.submitting}
          data-autofocus
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide current password' }}
          key={form.key('currentPassword')} {...form.getInputProps('currentPassword')} />
        <PasswordInput label="New password" autoComplete="new-password" required minLength={12} disabled={form.submitting}
          description="Use at least 12 characters and choose a password different from your current one."
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide new password' }}
          key={form.key('newPassword')} {...form.getInputProps('newPassword')} />
        <PasswordInput label="Confirm password" autoComplete="new-password" required disabled={form.submitting}
          aria-invalid={!!form.errors.confirmPassword}
          description="Re-enter your new password exactly."
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide confirmation password' }}
          key={form.key('confirmPassword')} {...form.getInputProps('confirmPassword')} />
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Group justify={onCancel ? 'flex-end' : 'flex-start'}>
          {onCancel ? <Button variant="default" disabled={form.submitting} onClick={onCancel}>Cancel</Button> : null}
          <Button type="submit" loading={form.submitting}>Change password</Button>
        </Group>
      </Stack>
    </form>
  );
}
