import { useRef, useState } from 'react';
import { Alert, Button, Checkbox, NumberInput, Stack, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

export function CapacityForm({ csrfToken, version, compatibleArtifact, onSaved }: {
  csrfToken: string;
  version: number;
  compatibleArtifact: boolean;
  onSaved: () => Promise<void>;
}) {
  const [error, setError] = useState<unknown>();
  const positive = (value: string | number) => typeof value === 'number' && Number.isSafeInteger(value) && value > 0
    ? null : 'Enter a positive whole number.';
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { cpuCount: '' as string | number, memoryMiB: '' as string | number, diskMiB: '' as string | number },
    validate: { cpuCount: positive, memoryMiB: positive, diskMiB: positive },
  });
  const save = async (values: typeof form.values) => {
    setError(undefined);
    try {
      const response = await fetch('/api/v1/admin/capacity', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${version}"` },
        body: JSON.stringify(values),
      });
      if (!response.ok) throw await readProblem(response);
      await onSaved();
    } catch (error) {
      setError(error);
    }
  };
  return (
    <Stack>
      <Title order={2}>Workspace capacity</Title>
      {!compatibleArtifact ? (
        <Alert color="yellow" title="Compatible platform image required">
          Setup needs a validated default workspace image before activation.
        </Alert>
      ) : null}
      {version > 0 ? <Text size="sm">Capacity limits are saved.</Text> : null}
      <form noValidate onSubmit={form.onSubmit(save, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
        <Stack>
          <NumberInput label="vCPU limit" required min={1} allowDecimal={false}
            key={form.key('cpuCount')} {...form.getInputProps('cpuCount')} />
          <NumberInput label="Memory limit (MiB)" required min={1} allowDecimal={false}
            key={form.key('memoryMiB')} {...form.getInputProps('memoryMiB')} />
          <NumberInput label="Disk limit (MiB)" required min={1} allowDecimal={false}
            key={form.key('diskMiB')} {...form.getInputProps('diskMiB')} />
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Stack align="flex-start" gap="xs">
            <Button type="submit" loading={form.submitting}>Save capacity</Button>
            <Button type="button" variant="subtle" onClick={() => { setError(undefined); void onSaved(); }}>
              Recheck prerequisites
            </Button>
          </Stack>
        </Stack>
      </form>
    </Stack>
  );
}

export function ActivateForm({ csrfToken, onActivated }: {
  csrfToken: string;
  onActivated: () => void;
}) {
  const [acknowledged, setAcknowledged] = useState(false);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState<unknown>();
  const idempotencyKey = useRef<string>(undefined);
  const activate = async () => {
    if (!acknowledged || working) return;
    setWorking(true);
    setError(undefined);
    try {
      idempotencyKey.current ??= crypto.randomUUID();
      const response = await fetch('/api/v1/setup/activate', {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrfToken, 'Idempotency-Key': idempotencyKey.current },
      });
      if (!response.ok) throw await readProblem(response);
      const operation: { status: string } = await response.json();
      if (operation.status !== 'succeeded') throw new Error('activation did not complete');
      onActivated();
    } catch (error) {
      setError(error);
    } finally {
      setWorking(false);
    }
  };
  return (
    <Stack align="flex-start">
      <Title order={2}>Activate installation</Title>
      <Alert color="yellow" title="The local setup session will end">
        After activation, start the controller and sign in at the management address.
      </Alert>
      <Checkbox label="I understand that I will need to sign in again" checked={acknowledged}
        onChange={event => setAcknowledged(event.currentTarget.checked)} disabled={working} />
      {error !== undefined ? <APIProblemView error={error} /> : null}
      <Button disabled={!acknowledged} loading={working} onClick={() => void activate()}>Activate installation</Button>
    </Stack>
  );
}

export function SetupComplete({ origin }: { origin?: string }) {
  let signInURL: string | undefined;
  try {
    if (origin) {
      const url = new URL(origin);
      if (url.protocol === 'https:' && url.origin === origin) signInURL = `${origin}/auth/login`;
    }
  } catch {
    // The completion message still applies if the origin is unavailable.
  }
  return (
    <Stack align="flex-start">
      <Text role="status">Setup is complete. Start the controller and sign in at the management address.</Text>
      {signInURL ? <Button component="a" href={signInURL}>Sign in to ShiftBox</Button> : null}
    </Stack>
  );
}
