'use client';

import { useState } from 'react';
import { Alert, Button, Checkbox, Code, Container, Image, PasswordInput, Stack, Stepper, Text, TextInput, Title, useComputedColorScheme } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { PasswordForm } from '../session/password';
import { ActivateForm, CapacityForm, PlatformCheck, SetupComplete } from './setup-activation';
import { CertificateForm, DatabaseForm, type CloudSetupNames } from './setup-configuration';

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
  localOnly?: boolean;
  completedSteps: string[];
  availableSteps: string[];
  capacityVersion?: number;
  compatibleArtifact?: boolean;
  managementOrigin?: string;
  cloud?: CloudSetupNames;
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
  const [activated, setActivated] = useState(false);
  const privateSetup = status.data?.localOnly === false;
  const setupComplete = activated || status.data?.state === 'active';
  const controller = useQuery({
    queryKey: ['setup-handoff'],
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/setup/status', { signal, cache: 'no-store' });
      // The active controller retires the bootstrap API at this same origin.
      return response.status === 404;
    },
    enabled: privateSetup && setupComplete,
    refetchInterval: query => query.state.data ? false : 1000,
  });
  const stage = setupComplete ? undefined : status.data?.availableSteps[0];
  const completed = status.data?.completedSteps ?? [];
  const showPasswordStep = !privateSetup || stage === 'password';
  const setupSteps = showPasswordStep ? steps : steps.filter(([key]) => key !== 'password');
  let current = stage;
  if (passwordChanged || (administratorCreated && privateSetup && stage === 'administrator')) current = 'data_profile';
  if (administratorCreated && !privateSetup && stage === 'administrator') current = 'password';
  const active = setupSteps.findIndex(([key]) => key === current);
  const currentSession = session.data?.mode === 'bootstrap_restricted' ? session.data : null;
  const error = status.error ?? session.error;
  const refreshStatus = async () => { await status.refetch(); };
  const showAdministrator = !error && !status.isPending && stage === 'administrator' && !administratorCreated;
  let signInMessage = 'Sign in with the administrator account to continue.';
  if (administratorCreated && privateSetup) signInMessage = 'Sign in with the administrator email and password you just set.';
  if (passwordChanged) signInMessage = 'Sign in again with your new password.';
  return (
    <Container component="main" size="sm" py="xl">
      <Stack gap="lg">
        <Image src={`/brand/shiftbox-logo-${scheme}.svg`} alt="ShiftBox" w={160} h={40} fit="contain" />
        <Title order={1}>{privateSetup ? 'Set up ShiftBox' : 'Local setup'}</Title>
        <Stepper active={setupComplete ? setupSteps.length : active < 0 ? completed.length : active}
          orientation="vertical" size="sm" allowNextStepsSelect={false}>
          {setupSteps.map(([key, label]) => (
            <Stepper.Step key={key} label={label} aria-label={label} aria-current={key === current ? 'step' : undefined} allowStepSelect={false} />
          ))}
        </Stepper>
        {error ? <APIProblemView error={error} /> : null}
        {!error && (status.isPending || session.isPending) ? <Text role="status">Checking setup…</Text> : null}
        {showAdministrator ? <AdministratorForm privateSetup={privateSetup} onAcknowledged={() => setAdministratorCreated(true)} /> : null}
        {!error && !session.isPending && !showAdministrator && (!setupComplete && (administratorCreated || passwordChanged || (stage && !currentSession))) ? (
          <Stack align="flex-start">
            <Text role="status">{signInMessage}</Text>
            <Button component="a" href="/auth/login">Sign in</Button>
          </Stack>
        ) : null}
        {!error && stage === 'password' && currentSession && !passwordChanged ? (
          <PasswordForm csrfToken={currentSession.csrfToken} onChanged={() => setPasswordChanged(true)} />
        ) : null}
        {!error && stage === 'data_profile' && currentSession && !passwordChanged ? (
          <DatabaseForm cloud={!!status.data?.cloud} csrfToken={currentSession.csrfToken} onSaved={refreshStatus} />
        ) : null}
        {!error && stage === 'certificate' && currentSession && !passwordChanged ? (
          <CertificateForm cloud={status.data?.cloud} deployed={privateSetup} csrfToken={currentSession.csrfToken} onSaved={refreshStatus} />
        ) : null}
        {!error && stage === 'provider' && currentSession && !passwordChanged ? (
          <Stack gap="xl">
            {status.data?.cloud
              ? <Text size="sm">Register and validate the AWS platform image after activation. Workspace launch opens after that check.</Text>
              : status.data?.compatibleArtifact === true ? null : (
              <PlatformCheck csrfToken={currentSession.csrfToken} onValidated={refreshStatus} />
            )}
            <CapacityForm csrfToken={currentSession.csrfToken} version={status.data?.capacityVersion ?? 0} onSaved={refreshStatus} />
          </Stack>
        ) : null}
        {!error && stage === 'activation' && currentSession && !passwordChanged ? (
          <ActivateForm privateSetup={privateSetup} csrfToken={currentSession.csrfToken} onActivated={() => setActivated(true)} />
        ) : null}
        {setupComplete ? (
          <SetupComplete origin={status.data?.managementOrigin} privateSetup={privateSetup} ready={controller.data === true} />
        ) : null}
      </Stack>
    </Container>
  );
}

function AdministratorForm({ privateSetup, onAcknowledged }: { privateSetup: boolean; onAcknowledged: () => void }) {
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { claimCode: '', email: '', initialPassword: '', confirmPassword: '' },
    validate: {
      claimCode: value => privateSetup && !value.trim() ? 'Enter the setup claim code.' : null,
      email: value => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim()) ? null : 'Enter the administrator email.',
      initialPassword: value => privateSetup && ([...value].length < 12 || new TextEncoder().encode(value).length > 72)
        ? 'Use at least 12 characters and at most 72 UTF-8 bytes.' : null,
      confirmPassword: (value, values) => privateSetup && value !== values.initialPassword ? 'Passwords do not match.' : null,
    },
  });
  const [request, setRequest] = useState<{ email: string; key: string }>();
  const [password, setPassword] = useState<string>();
  const [acknowledged, setAcknowledged] = useState(false);
  const [error, setError] = useState<unknown>();
  const create = async ({ email: rawEmail, claimCode, initialPassword, confirmPassword }: typeof form.values) => {
    setError(undefined);
    try {
      const email = rawEmail.trim().toLowerCase();
      const key = request?.email === email ? request.key : crypto.randomUUID();
      setRequest({ email, key });
      const response = await fetch('/api/v1/setup/administrator', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
        body: JSON.stringify(privateSetup ? { email, claimCode: claimCode.trim(), initialPassword, confirmPassword } : { email }),
      });
      if (!response.ok) throw await readProblem(response);
      if (privateSetup) {
        form.reset();
        onAcknowledged();
        return;
      }
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
    <form noValidate onSubmit={form.onSubmit(create, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
      <Stack align="stretch">
        {privateSetup ? (
          <PasswordInput label="Setup claim code" autoComplete="off" required
            description="Retrieve the code from the AWS Secrets Manager reference in this stack’s outputs."
            visibilityToggleButtonProps={{ 'aria-label': 'Show or hide the setup claim code' }}
            key={form.key('claimCode')} {...form.getInputProps('claimCode')} />
        ) : null}
        <TextInput label="Administrator email" type="email" autoComplete="email" required
          key={form.key('email')} {...form.getInputProps('email')} />
        {privateSetup ? (
          <>
            <PasswordInput label="New password" autoComplete="new-password" required
              description="Use at least 12 characters and at most 72 UTF-8 bytes."
              visibilityToggleButtonProps={{ 'aria-label': 'Show or hide the new password' }}
              key={form.key('initialPassword')} {...form.getInputProps('initialPassword')} />
            <PasswordInput label="Confirm password" autoComplete="new-password" required
              visibilityToggleButtonProps={{ 'aria-label': 'Show or hide the password confirmation' }}
              key={form.key('confirmPassword')} {...form.getInputProps('confirmPassword')} />
          </>
        ) : null}
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Create administrator</Button>
      </Stack>
    </form>
  );
}
