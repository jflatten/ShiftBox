'use client';

import { useState } from 'react';
import { Button, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';

export function Backup({ csrfToken }: { csrfToken: string }) {
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { destination: '', password: '' },
    validate: {
      destination: value => absolutePath(value) ? null : 'Enter an absolute archive destination.',
      password: value => [...value].length >= 12 && new TextEncoder().encode(value).length <= 1024
        ? null : 'Enter a passphrase of at least 12 characters and at most 1024 UTF-8 bytes.',
    },
  });
  const [operationId, setOperationId] = useState('');
  const [error, setError] = useState<unknown>();
  async function submit(values: typeof form.values) {
    setError(undefined);
    setOperationId('');
    const body = JSON.stringify(values);
    form.setFieldValue('password', '');
    try {
      setOperationId(await submitRecoveryOperation('/api/v1/backups', csrfToken, body));
    } catch (error) {
      setError(error);
    }
  }
  return (
    <Stack component="section" gap="sm">
      <Title order={2}>Backup</Title>
      <Text>The encrypted archive contains management and identity data, configuration, and keys. Workspace VM disks and files stay outside the archive.</Text>
      <form noValidate onSubmit={form.onSubmit(submit, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
        <Stack maw={480}>
          <TextInput label="Destination" description="Absolute archive path on the controller." required disabled={form.submitting}
            key={form.key('destination')} {...form.getInputProps('destination')} />
          <PasswordInput label="Passphrase" autoComplete="new-password" required disabled={form.submitting}
            key={form.key('password')} {...form.getInputProps('password')} />
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Create backup</Button>
        </Stack>
      </form>
      {operationId ? <RecoveryOperation key={operationId} id={operationId} /> : null}
    </Stack>
  );
}

export function RecoveryOperation({ id, validation }: { id: string; validation?: boolean }) {
  const operation = useOperation(id);
  if (operation.accessDenied) return <Text role="alert">Workflow access expired. Sign in again to check progress.</Text>;
  if (operation.isError) return <Stack align="flex-start">
    <Text role="alert">Workflow progress is unavailable.</Text>
    <Button variant="default" onClick={() => void operation.refetch()}>Retry progress</Button>
  </Stack>;
  if (!operation.data) return <Text role="status">Loading workflow progress…</Text>;
  const finding = validation && operation.data.status === 'succeeded'
    ? operation.data.steps.find(step => step.key === 'validate' && step.status === 'succeeded')?.detail : undefined;
  return <>
    <WorkflowStepper operation={operation.data} />
    {finding ? <Stack gap="xs">
      <Text>Validation ID <Text span fw={600}>{finding}</Text></Text>
      <Text>Use <Text span ff="monospace">shiftbox restore activate</Text> on the controller with this validation ID to enter recovery review.</Text>
    </Stack> : null}
  </>;
}

export function absolutePath(value: string) {
  return value.length > 1 && value.length <= 4096 && value.startsWith('/') && !value.includes('\0');
}

export async function submitRecoveryOperation(endpoint: string, csrfToken: string, body: string) {
  const response = await fetch(endpoint, {
    method: 'POST', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() },
    body,
  });
  if (!response.ok) throw await readProblem(response);
  const operation = await response.json();
  if (typeof operation?.id !== 'string' || !operation.id) throw new Error('Invalid operation response');
  return operation.id as string;
}
