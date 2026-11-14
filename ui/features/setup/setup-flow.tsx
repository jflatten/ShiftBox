'use client';

import { useState } from 'react';
import { Alert, Button, Checkbox, Code, Container, Image, PasswordInput, Stack, Stepper, Text, TextInput, Title, useComputedColorScheme } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

const steps = [
  ['administrator', 'Administrator'],
  ['password', 'Password'],
  ['data_profile', 'Database'],
  ['certificate', 'Certificate'],
  ['provider', 'Provider'],
  ['activation', 'Activation'],
] as const;

interface SetupStatus {
  state: string;
  completedSteps: string[];
  availableSteps: string[];
}

interface SetupSession {
  mode: string;
  csrfToken: string;
}

async function readSetup(signal: AbortSignal): Promise<SetupStatus> {
  const response = await fetch('/api/v1/setup/status', { signal, cache: 'no-store' });
  if (!response.ok) throw await readProblem(response);
  return response.json();
}

async function readSession(signal: AbortSignal): Promise<SetupSession | null> {
  const response = await fetch('/api/v1/session', { signal, cache: 'no-store' });
  if (response.status === 401) return null;
  if (!response.ok) throw await readProblem(response);
  return response.json();
}

export function SetupFlow() {
  const scheme = useComputedColorScheme('light');
  // Keep the one-time password visible during the acknowledgement, including
  // when the operator switches windows to save it.
  const status = useQuery({
    queryKey: ['setup-status'], queryFn: ({ signal }) => readSetup(signal),
    refetchOnWindowFocus: false, refetchOnReconnect: false,
  });
  const session = useQuery({ queryKey: ['session', 'setup'], queryFn: ({ signal }) => readSession(signal) });
  const [administratorCreated, setAdministratorCreated] = useState(false);
  const [passwordChanged, setPasswordChanged] = useState(false);
  const stage = status.data?.availableSteps[0];
  const completed = status.data?.completedSteps ?? [];
  const current = passwordChanged ? 'data_profile' : administratorCreated && stage === 'administrator' ? 'password' : stage;
  const active = steps.findIndex(([key]) => key === current);
  const currentSession = session.data?.mode === 'bootstrap_restricted' ? session.data : null;
  const error = status.error ?? session.error;
  const showAdministrator = !error && !status.isPending && stage === 'administrator' && !administratorCreated;
  return (
    <Container component="main" size="sm" py="xl">
      <Stack gap="lg">
        <Image src={`/brand/shiftbox-logo-${scheme}.svg`} alt="ShiftBox" w={160} h={40} fit="contain" />
        <Title order={1}>Local setup</Title>
        <Stepper active={status.data?.state === 'active' ? steps.length : active < 0 ? completed.length : active}
          orientation="vertical" size="sm" allowNextStepsSelect={false}>
          {steps.map(([key, label]) => (
            <Stepper.Step key={key} label={label} aria-label={label} aria-current={key === current ? 'step' : undefined} allowStepSelect={false} />
          ))}
        </Stepper>
        {error ? <APIProblemView error={error} /> : null}
        {!error && (status.isPending || session.isPending) ? <Text role="status">Checking setup…</Text> : null}
        {showAdministrator ? <AdministratorForm onAcknowledged={() => setAdministratorCreated(true)} /> : null}
        {!error && !session.isPending && !showAdministrator && (administratorCreated || passwordChanged || (stage && !currentSession)) ? (
          <Stack align="flex-start">
            <Text role="status">{passwordChanged ? 'Sign in again with your new password.' : 'Sign in with the administrator account to continue.'}</Text>
            <Button component="a" href="/auth/login">Sign in</Button>
          </Stack>
        ) : null}
        {!error && stage === 'password' && currentSession && !passwordChanged ? (
          <PasswordForm csrfToken={currentSession.csrfToken} onChanged={() => setPasswordChanged(true)} />
        ) : null}
        {!error && active > 1 && currentSession && !passwordChanged ? (
          <Text role="status">{steps[active][1]} configuration is pending.</Text>
        ) : null}
        {!error && status.data?.state === 'active' ? <Text role="status">Setup is complete. Sign in at the management address.</Text> : null}
      </Stack>
    </Container>
  );
}

function AdministratorForm({ onAcknowledged }: { onAcknowledged: () => void }) {
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { email: '' },
    validate: { email: value => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim()) ? null : 'Enter the administrator email.' },
  });
  const [request, setRequest] = useState<{ email: string; key: string }>();
  const [password, setPassword] = useState<string>();
  const [acknowledged, setAcknowledged] = useState(false);
  const [error, setError] = useState<unknown>();
  const create = async ({ email: rawEmail }: { email: string }) => {
    setError(undefined);
    try {
      const email = rawEmail.trim().toLowerCase();
      const key = request?.email === email ? request.key : crypto.randomUUID();
      setRequest({ email, key });
      const response = await fetch('/api/v1/setup/administrator', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
        body: JSON.stringify({ email }),
      });
      if (!response.ok) throw await readProblem(response);
      const result: { temporaryPassword?: string } = await response.json();
      if (!result.temporaryPassword) throw new Error('temporary password missing');
      setPassword(result.temporaryPassword);
      form.reset();
    } catch (error) {
      setError(error);
    }
  };
  if (password) {
    return (
      <Stack align="flex-start">
        <Alert title="Temporary password" color="yellow">
          <Stack gap="xs">
            <Text size="sm">Save this password before continuing. It is displayed once.</Text>
            <Code block>{password}</Code>
          </Stack>
        </Alert>
        <Checkbox label="I saved the temporary password" checked={acknowledged}
          onChange={event => setAcknowledged(event.currentTarget.checked)} />
        <Button disabled={!acknowledged} onClick={onAcknowledged}>Continue to sign-in</Button>
      </Stack>
    );
  }
  return (
    <form noValidate onSubmit={form.onSubmit(create, () => form.getInputNode('email')?.focus())}>
      <Stack align="stretch">
        <TextInput label="Administrator email" type="email" autoComplete="email" required
          key={form.key('email')} {...form.getInputProps('email')} />
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Create administrator</Button>
      </Stack>
    </form>
  );
}

function PasswordForm({ csrfToken, onChanged }: { csrfToken: string; onChanged: () => void }) {
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
      <Stack>
        <PasswordInput label="Current password" autoComplete="current-password" required
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide current password' }}
          key={form.key('currentPassword')} {...form.getInputProps('currentPassword')} />
        <PasswordInput label="New password" autoComplete="new-password" required minLength={12}
          visibilityToggleButtonProps={{ 'aria-label': 'Show or hide new password' }}
          key={form.key('newPassword')} {...form.getInputProps('newPassword')} />
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Change password</Button>
      </Stack>
    </form>
  );
}
