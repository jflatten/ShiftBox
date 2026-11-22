import { useRef, useState } from 'react';
import { Button, Card, Chip, FileInput, Group, Paper, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper, type WorkflowOperation } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { pemFileError } from '../../lib/pem-file';
import type { components } from '../../lib/api-types';

type CertificateDetails = components['schemas']['BrowserCertificateDetails'];

export function BrowserCertificate({ userId, active, csrfToken, refresh }: {
  userId: string;
  active: boolean;
  csrfToken: string;
  refresh: () => Promise<unknown>;
}) {
  const requestKey = useRef<string>(undefined);
  const [error, setError] = useState<unknown>();
  const [operation, setOperation] = useState<WorkflowOperation>();
  const installed = useQuery<CertificateDetails>({
    queryKey: ['browser-certificate', userId],
    enabled: active,
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/admin/certificate', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
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
      await Promise.all([installed.refetch(), refresh()]);
    } catch (error) {
      setError(error);
    }
  };

  return (
    <Stack gap="lg">
      <Stack gap={4}>
        <Title order={2} size="h3">Browser certificate</Title>
        <Text size="sm" c="dimmed">The certificate served for management, IDE, and preview HTTPS connections.</Text>
      </Stack>
      {installed.isPending ? <Text size="sm" role="status">Loading certificate details…</Text> : null}
      {installed.isError ? <Stack gap="sm">
        <APIProblemView error={installed.error} />
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void installed.refetch()}>Retry certificate details</Button>
      </Stack> : installed.data ? <InstalledCertificate certificate={installed.data} /> : null}
      <Paper component="form" withBorder radius="md" p="lg" noValidate
        onSubmit={form.onSubmit(replace, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
        <Stack maw={480} gap="sm">
          <Title order={3} size="h4">Replace certificate</Title>
          <Text size="sm">Use a trusted certificate covering the management address and both IDE and preview wildcards.</Text>
          <FileInput label="Certificate chain" description="PEM-encoded certificate chain (.pem or .crt)." accept=".pem,.crt" withAsterisk disabled={form.submitting}
            key={form.key('chain')} {...form.getInputProps('chain')} />
          <FileInput label="Private key" description="The matching PEM-encoded private key (.pem or .key)." accept=".pem,.key" withAsterisk disabled={form.submitting}
            key={form.key('key')} {...form.getInputProps('key')} />
          <Button type="submit" loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Replace certificate</Button>
          {error !== undefined ? <APIProblemView error={error} /> : null}
          {operation ? <WorkflowStepper operation={operation} /> : null}
        </Stack>
      </Paper>
    </Stack>
  );
}

const validity = {
  valid: { label: 'Valid', color: 'green' },
  expiring: { label: 'Expires soon', color: 'yellow' },
  expired: { label: 'Expired', color: 'red' },
  not_yet_valid: { label: 'Not yet valid', color: 'red' },
};

function InstalledCertificate({ certificate }: { certificate: CertificateDetails }) {
  const status = validity[certificate.status];
  const details = [
    ['Expires on', utcDate(certificate.notAfter)],
    ['Valid from', utcDate(certificate.notBefore)],
    ['Subject', certificate.subject || 'Not specified'],
    ['Issuer', certificate.issuer || 'Not specified'],
    ['Serial number (hex)', certificate.serialNumber],
    ['Certificate version', `X.509 v${certificate.version}`],
    ['Signature algorithm', certificate.signatureAlgorithm],
    ['Public key algorithm', certificate.publicKeyAlgorithm],
    ['Certificate chain', `${certificate.chainLength} ${certificate.chainLength === 1 ? 'certificate' : 'certificates'}`],
  ];
  const sans = [
    ...certificate.dnsNames.map(value => ({ type: 'DNS', value })),
    ...certificate.ipAddresses.map(value => ({ type: 'IP', value })),
    ...certificate.emailAddresses.map(value => ({ type: 'Email', value })),
    ...certificate.uris.map(value => ({ type: 'URI', value })),
  ];
  return <Card component="section" aria-label="Installed certificate" withBorder radius="md" padding="lg">
    <Stack gap="md">
      <Group justify="space-between">
        <Title order={3} size="h4">Installed certificate</Title>
        <Chip checked readOnly hidden icon={false} color={status.color} variant="light"
          styles={{ label: { cursor: 'default' } }} wrapperProps={{ role: 'status' }}>{status.label}</Chip>
      </Group>
      {certificate.status === 'expiring' ? <Text size="sm">This certificate expires within 30 days. Install a replacement before it expires.</Text> : null}
      <SimpleGrid component="dl" cols={{ base: 1, sm: 2 }} m={0} spacing="md">
        {details.map(([label, value]) => <Stack key={label} gap={4} style={{ minWidth: 0 }}>
          <Text component="dt" size="xs" c="dimmed">{label}</Text>
          <Text component="dd" size="sm" m={0} style={{ overflowWrap: 'anywhere' }}>{value}</Text>
        </Stack>)}
      </SimpleGrid>
      <Stack component="dl" m={0} gap="md">
        <Stack gap={4}>
          <Text component="dt" size="xs" c="dimmed">Subject alternative names (SANs)</Text>
          <Stack component="dd" m={0} gap={4}>
            {sans.length ? sans.map(({ type, value }) => <Group key={`${type}:${value}`} gap="xs" wrap="nowrap" align="flex-start">
              <Text size="xs" c="dimmed" w={40} style={{ flexShrink: 0 }}>{type}</Text>
              <Text size="sm" style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{value}</Text>
            </Group>) : <Text size="sm">Not specified</Text>}
          </Stack>
        </Stack>
        <Stack gap={4}>
          <Text component="dt" size="xs" c="dimmed">SHA-256 fingerprint</Text>
          <Text component="dd" size="sm" ff="monospace" m={0} style={{ overflowWrap: 'anywhere' }}>{certificate.fingerprintSha256}</Text>
        </Stack>
      </Stack>
    </Stack>
  </Card>;
}

function utcDate(value: string) {
  return new Date(value).toISOString().replace('T', ' ').replace(/\.\d{3}Z$/, ' UTC');
}
