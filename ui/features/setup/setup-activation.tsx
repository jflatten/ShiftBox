import { useRef, useState } from 'react';
import { Alert, Button, Checkbox, NumberInput, Stack, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { validCapacityLimit } from '../../lib/use-infrastructure';

export function PlatformCheck({ csrfToken, onValidated }: {
  csrfToken: string;
  onValidated: () => Promise<void>;
}) {
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<unknown>();
  const validate = async () => {
    setChecking(true);
    setError(undefined);
    try {
      const response = await fetch('/api/v1/setup/platform', {
        method: 'POST', headers: { 'X-CSRF-Token': csrfToken },
      });
      if (!response.ok) throw await readProblem(response);
      await onValidated();
    } catch (error) {
      setError(error);
    } finally {
      setChecking(false);
    }
  };
  return (
    <Stack align="flex-start">
      <Title order={2}>Platform image</Title>
      <Text size="sm">ShiftBox checks the configured local image by booting a temporary UTM VM.</Text>
      {checking ? <Text role="status">Validating the platform image. This can take several minutes.</Text> : null}
      {error !== undefined ? <APIProblemView error={error} /> : null}
      <Button loading={checking} onClick={() => void validate()}>Validate platform image</Button>
    </Stack>
  );
}

export function CapacityForm({ csrfToken, version, onSaved }: {
  csrfToken: string;
  version: number;
  onSaved: () => Promise<void>;
}) {
  const [error, setError] = useState<unknown>();
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { cpuCount: '' as string | number, memoryMiB: '' as string | number, diskMiB: '' as string | number },
    validate: { cpuCount: validCapacityLimit, memoryMiB: validCapacityLimit, diskMiB: validCapacityLimit },
  });
  const save = async (values: typeof form.values) => {
    setError(undefined);
    try {
      const response = await fetch('/api/v1/admin/capacity', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${version}"` },
        body: JSON.stringify({ cpuCount: Number(values.cpuCount), memoryMiB: Number(values.memoryMiB), diskMiB: Number(values.diskMiB) }),
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
      <Text size="sm">Leave a limit blank for Unlimited. No capacity budget is enforced for that resource.</Text>
      {version > 0 ? <Text size="sm">Capacity limits are saved.</Text> : null}
      <form noValidate onSubmit={form.onSubmit(save, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
        <Stack>
          <NumberInput label="vCPU limit" placeholder="Unlimited" allowNegative={false} allowDecimal={false}
            key={form.key('cpuCount')} {...form.getInputProps('cpuCount')} />
          <NumberInput label="Memory limit (MiB)" placeholder="Unlimited" allowNegative={false} allowDecimal={false}
            key={form.key('memoryMiB')} {...form.getInputProps('memoryMiB')} />
          <NumberInput label="Disk limit (MiB)" placeholder="Unlimited" allowNegative={false} allowDecimal={false}
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

export function ActivateForm({ csrfToken, onActivated, privateSetup }: {
  csrfToken: string;
  onActivated: () => void;
  privateSetup?: boolean;
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
      <Alert color="yellow" title="The setup session will end">
        {privateSetup
          ? 'After activation, ShiftBox starts automatically. Sign in again at this address.'
          : 'After activation, start the controller and sign in at the management address.'}
      </Alert>
      <Checkbox label="I understand that I will need to sign in again" checked={acknowledged}
        onChange={event => setAcknowledged(event.currentTarget.checked)} disabled={working} />
      {error !== undefined ? <APIProblemView error={error} /> : null}
      <Button disabled={!acknowledged} loading={working} onClick={() => void activate()}>Activate installation</Button>
    </Stack>
  );
}

export function SetupComplete({ origin, privateSetup, ready }: { origin?: string; privateSetup?: boolean; ready?: boolean }) {
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
      <Text role="status">{privateSetup
        ? ready ? 'ShiftBox is online.' : 'Starting ShiftBox…'
        : 'Setup is complete. Start the controller and sign in at the management address.'}</Text>
      {signInURL && (!privateSetup || ready) ? <Button component="a" href={signInURL}>Sign in to ShiftBox</Button> : null}
    </Stack>
  );
}
