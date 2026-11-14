'use client';

import { useState } from 'react';
import { Button, Checkbox, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { absolutePath, RecoveryOperation, submitRecoveryOperation } from './backup';

export function Restore({ csrfToken, profile }: { csrfToken: string; profile?: 'sqlite' | 'postgres' }) {
  const [postgres, setPostgres] = useState(profile === 'postgres');
  const [operationId, setOperationId] = useState('');
  const [error, setError] = useState<unknown>();
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { archivePath: '', destination: '', password: '', connectionUri: '', dexConnectionUri: '' },
    validate: {
      archivePath: value => absolutePath(value) ? null : 'Enter the absolute archive path.',
      destination: value => absolutePath(value) ? null : 'Enter an absolute replacement path.',
      password: value => value && new TextEncoder().encode(value).length <= 1024 ? null : 'Enter the archive passphrase.',
      connectionUri: value => !postgres || value ? null : 'Enter the application owner connection URI.',
      dexConnectionUri: value => !postgres || value ? null : 'Enter the Dex owner connection URI.',
    },
  });
  const clearLogins = () => {
    form.setFieldValue('connectionUri', '');
    form.setFieldValue('dexConnectionUri', '');
  };

  async function submit({ archivePath, destination, password, connectionUri, dexConnectionUri }: typeof form.values) {
    setError(undefined);
    setOperationId('');
    const body = JSON.stringify({
      archivePath, password, destination,
      replacementPostgres: postgres ? { profile: 'postgres', tlsMode: 'verify-full', connectionUri, dexConnectionUri } : undefined,
    });
    form.setFieldValue('password', '');
    clearLogins();
    try {
      setOperationId(await submitRecoveryOperation('/api/v1/restores/validate', csrfToken, body));
    } catch (error) {
      setError(error);
    }
  }
  return (
    <Stack component="section" gap="sm">
      <Title order={2}>Validate a restore</Title>
      <Text>Check an archive and an empty replacement on the original controller. Activation is a separate local command.</Text>
      <form noValidate onSubmit={form.onSubmit(submit, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
        <Stack maw={480}>
          <TextInput label="Archive path" description="Absolute path on the controller." required disabled={form.submitting}
            key={form.key('archivePath')} {...form.getInputProps('archivePath')} />
          <TextInput label="Replacement directory" description="Use this same absolute path when you activate the restore on the controller."
            required disabled={form.submitting} key={form.key('destination')} {...form.getInputProps('destination')} />
          <PasswordInput label="Passphrase" autoComplete="new-password" required disabled={form.submitting}
            key={form.key('password')} {...form.getInputProps('password')} />
          {profile === undefined ? <Checkbox label="PostgreSQL replacement" checked={postgres} disabled={form.submitting}
            onChange={event => { setPostgres(event.currentTarget.checked); clearLogins(); }} /> : null}
          {postgres ? <>
            <PasswordInput label="Application owner connection URI" autoComplete="off" required disabled={form.submitting}
              key={form.key('connectionUri')} {...form.getInputProps('connectionUri')} />
            <PasswordInput label="Dex owner connection URI" autoComplete="off" required disabled={form.submitting}
              key={form.key('dexConnectionUri')} {...form.getInputProps('dexConnectionUri')} />
          </> : null}
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Validate restore</Button>
        </Stack>
      </form>
      {operationId ? <RecoveryOperation key={operationId} id={operationId} validation /> : null}
    </Stack>
  );
}
