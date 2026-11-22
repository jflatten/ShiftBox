import { useRef, useState } from 'react';
import { Button, FileInput, PasswordInput, Radio, Stack, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { pemFileError } from '../../lib/pem-file';

export interface CloudSetupNames {
  managementOrigin: string;
  ideHostPattern: string;
  previewHostPattern: string;
}

interface SetupFormProps {
  csrfToken: string;
  onSaved: () => Promise<void>;
}

function useSetupSave(path: string, { csrfToken, onSaved }: SetupFormProps) {
  const requestKey = useRef<string>(undefined);
  const [error, setError] = useState<unknown>();
  return {
    error,
    changed: () => { requestKey.current = undefined; },
    save: async (body: () => Promise<object> | object, clear: () => void) => {
      setError(undefined);
      try {
        const payload = await body();
        requestKey.current ??= crypto.randomUUID();
        const response = await fetch(path, {
          method: 'PUT',
          headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': csrfToken,
            'Idempotency-Key': requestKey.current,
          },
          body: JSON.stringify(payload),
        });
        if (!response.ok) throw await readProblem(response);
        clear();
        await onSaved();
      } catch (error) {
        setError(error);
      }
    },
  };
}

function validPostgresURI(value: string) {
  try {
    const uri = new URL(value);
    return (uri.protocol === 'postgres:' || uri.protocol === 'postgresql:') &&
      !!uri.hostname && !!uri.username && !!uri.password && uri.pathname.length > 1 &&
      uri.searchParams.get('sslmode') === 'verify-full';
  } catch {
    return false;
  }
}

export function DatabaseForm(props: SetupFormProps & { cloud?: boolean }) {
  const submission = useSetupSave('/api/v1/setup/data-profile', props);
  const form = useForm({
    mode: 'controlled',
    initialValues: { profile: props.cloud ? 'postgres' : 'sqlite', connectionUri: '' },
    onValuesChange: submission.changed,
    validate: {
      connectionUri: (value, values) => !props.cloud && values.profile === 'postgres' && !validPostgresURI(value.trim())
        ? 'Enter a PostgreSQL connection URI with credentials and sslmode=verify-full.' : null,
    },
  });
  return (
    <form noValidate onSubmit={form.onSubmit(
      ({ profile, connectionUri }) => submission.save(() => profile === 'postgres'
        ? { profile, connectionUri: props.cloud ? undefined : connectionUri.trim(), tlsMode: 'verify-full' } : { profile }, form.reset),
      () => form.getInputNode('connectionUri')?.focus(),
    )}>
      <Stack align="flex-start">
        <Title order={2}>Database profile</Title>
        {props.cloud ? <Text size="sm">Validate the deployed PostgreSQL database and its scoped ShiftBox and Dex logins.</Text> : (
        <Radio.Group label="Database" {...form.getInputProps('profile')}
          onChange={profile => form.setValues({ profile, connectionUri: '' })}>
          <Stack gap="xs" mt="xs">
            <Radio value="sqlite" label="SQLite for a small or demo installation" />
            <Radio value="postgres" label="PostgreSQL for production" />
          </Stack>
        </Radio.Group>
        )}
        {!props.cloud && form.values.profile === 'postgres' ? (
          <>
            <Text size="sm">Use the scoped ShiftBox login. Supply the separate Dex login and database trust when starting local setup.</Text>
            <PasswordInput w="100%" label="PostgreSQL connection URI" autoComplete="off" required
              visibilityToggleButtonProps={{ 'aria-label': 'Show or hide the connection URI' }}
              {...form.getInputProps('connectionUri')} />
          </>
        ) : null}
        {submission.error !== undefined ? <APIProblemView error={submission.error} /> : null}
        <Button type="submit" loading={form.submitting}>Validate database</Button>
      </Stack>
    </form>
  );
}

function validOrigin(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && url.origin === value;
  } catch {
    return false;
  }
}

function suffixError(value: string, max: number) {
  return value.length <= max && value.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))
    ? null : 'Enter a lowercase DNS suffix.';
}

export function CertificateForm(props: SetupFormProps & { cloud?: CloudSetupNames }) {
  const submission = useSetupSave('/api/v1/setup/certificate', props);
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: {
      managementOrigin: props.cloud?.managementOrigin ?? '',
      ideSuffix: props.cloud?.ideHostPattern.replace('{workspace}.', '') ?? '',
      previewSuffix: props.cloud?.previewHostPattern.replace('{service}--{workspace}.', '') ?? '',
      chain: null as File | null,
      key: null as File | null,
    },
    onValuesChange: submission.changed,
    validate: {
      managementOrigin: value => validOrigin(value.trim()) ? null : 'Enter an HTTPS origin without a path.',
      ideSuffix: value => suffixError(value.trim(), 216),
      previewSuffix: value => suffixError(value.trim(), 189),
      chain: pemFileError,
      key: pemFileError,
    },
  });
  return (
    <form noValidate onSubmit={form.onSubmit(
      ({ managementOrigin, ideSuffix, previewSuffix, chain, key }) => submission.save(async () => {
        if (!chain || !key) throw new Error('certificate files missing');
        const [certificateChainPem, privateKeyPem] = await Promise.all([chain.text(), key.text()]);
        return {
          managementOrigin: managementOrigin.trim(),
          ideHostPattern: `{workspace}.${ideSuffix.trim()}`,
          previewHostPattern: `{service}--{workspace}.${previewSuffix.trim()}`,
          certificateChainPem,
          privateKeyPem,
        };
      }, form.reset),
      errors => form.getInputNode(Object.keys(errors)[0])?.focus(),
    )}>
      <Stack>
        <Title order={2}>Browser certificate</Title>
        <TextInput readOnly={!!props.cloud} label="Management address" placeholder="https://shiftbox.example.com" required
          key={form.key('managementOrigin')} {...form.getInputProps('managementOrigin')} />
        <TextInput readOnly={!!props.cloud} label="IDE domain suffix" placeholder="ide.example.com" required
          key={form.key('ideSuffix')} {...form.getInputProps('ideSuffix')} />
        <TextInput readOnly={!!props.cloud} label="Preview domain suffix" placeholder="preview.example.com" required
          key={form.key('previewSuffix')} {...form.getInputProps('previewSuffix')} />
        <Text size="sm">Use a trusted certificate covering the management address and the IDE, preview, and repair wildcards. For ide.example.com, the repair wildcard is *.repair.example.com.</Text>
        <FileInput label="Certificate chain" accept=".pem,.crt" withAsterisk
          key={form.key('chain')} {...form.getInputProps('chain')} />
        <FileInput label="Private key" accept=".pem,.key" withAsterisk
          key={form.key('key')} {...form.getInputProps('key')} />
        {submission.error !== undefined ? <APIProblemView error={submission.error} /> : null}
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Validate certificate</Button>
      </Stack>
    </form>
  );
}
