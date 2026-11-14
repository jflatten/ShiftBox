import { useRef, useState } from 'react';
import { Button, FileInput, Stack, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper, type WorkflowOperation } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { pemFileError } from '../../lib/pem-file';

export function BrowserCertificate({ csrfToken, refresh }: {
  csrfToken: string;
  refresh: () => Promise<unknown>;
}) {
  const requestKey = useRef<string>(undefined);
  const [error, setError] = useState<unknown>();
  const [operation, setOperation] = useState<WorkflowOperation>();
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { chain: null as File | null, key: null as File | null },
    validate: { chain: pemFileError, key: pemFileError },
    onValuesChange: () => {
      requestKey.current = undefined;
      setError(undefined);
      setOperation(undefined);
    },
  });

  const replace = async ({ chain, key }: typeof form.values) => {
    if (!chain || !key) return;
    setError(undefined);
    try {
      const [certificateChainPem, privateKeyPem] = await Promise.all([chain.text(), key.text()]);
      requestKey.current ??= crypto.randomUUID();
      const response = await fetch('/api/v1/admin/certificate', {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': csrfToken,
          'Idempotency-Key': requestKey.current,
        },
        body: JSON.stringify({ certificateChainPem, privateKeyPem }),
      });
      if (!response.ok) throw await readProblem(response);
      const result: WorkflowOperation = await response.json();
      form.reset();
      setOperation(result);
      await refresh();
    } catch (error) {
      setError(error);
    }
  };

  return (
    <form noValidate onSubmit={form.onSubmit(replace, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
      <Stack maw={480} gap="sm">
        <Title order={2}>Browser certificate</Title>
        <Text size="sm">Use a trusted certificate covering the management address and both IDE and preview wildcards.</Text>
        <FileInput label="Certificate chain" accept=".pem,.crt" withAsterisk disabled={form.submitting}
          key={form.key('chain')} {...form.getInputProps('chain')} />
        <FileInput label="Private key" accept=".pem,.key" withAsterisk disabled={form.submitting}
          key={form.key('key')} {...form.getInputProps('key')} />
        <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Replace certificate</Button>
        {error !== undefined ? <APIProblemView error={error} /> : null}
        {operation ? <WorkflowStepper operation={operation} /> : null}
      </Stack>
    </form>
  );
}
