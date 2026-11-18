import { useEffect, useState } from 'react';
import { Button, Chip, Group, Modal, Paper, Stack, Table, Text, Textarea, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';
import { useProfileImageCatalog } from './resource-profiles';

export function AMIFamilies({ userID, csrfToken }: { userID: string; csrfToken?: string }) {
  const catalog = useProfileImageCatalog(userID, true);
  const [retiring, setRetiring] = useState<{ id: string; name: string }>();
  const members = (catalog.isSuccess ? catalog.data.platformImages : [])
    .filter(image => image.amiFamily)
    .toSorted((a, b) => a.amiFamily!.localeCompare(b.amiFamily!) ||
      (b.imageCreatedAtUnixMillis ?? 0) - (a.imageCreatedAtUnixMillis ?? 0) || a.name.localeCompare(b.name));
  return <Paper component="section" aria-labelledby="ami-families-heading" withBorder radius="md" p="lg">
    <Stack gap="md">
      <Title order={2} size="h3" id="ami-families-heading">AMI families</Title>
      <Text size="sm" c="dimmed">Resource profiles that follow a family use its newest compatible validated image for new workspaces.</Text>
      {catalog.isPending ? <Text role="status">Loading AMI families…</Text> : null}
      {catalog.isError ? <>
        <APIProblemView error={catalog.error} />
        <Button variant="default" onClick={() => void catalog.refetch()}>Retry loading AMI families</Button>
      </> : null}
      {catalog.isSuccess && members.length === 0 ? <Text size="sm" c="dimmed">No validated AMI family images are available.</Text> : null}
      {members.length ? <Table.ScrollContainer minWidth={500} type="native" tabIndex={0} role="region" aria-label="Validated AMI family images">
        <Table>
          <Table.Thead><Table.Tr><Table.Th scope="col">Family</Table.Th><Table.Th scope="col">Release</Table.Th>
            <Table.Th scope="col">Status</Table.Th>{csrfToken ? <Table.Th scope="col">Actions</Table.Th> : null}
          </Table.Tr></Table.Thead>
          <Table.Tbody>{members.map(image => <Table.Tr key={image.id}>
            <Table.Th scope="row">{image.amiFamily}</Table.Th><Table.Td>{image.name}</Table.Td>
            <Table.Td><Chip checked readOnly hidden icon={false} color="green" size="xs">Validated</Chip></Table.Td>
            {csrfToken ? <Table.Td><Button variant="subtle" size="xs" aria-label={`Retire ${image.name}`}
              onClick={() => setRetiring(image)}>Retire</Button></Table.Td> : null}
          </Table.Tr>)}</Table.Tbody>
        </Table>
      </Table.ScrollContainer> : null}
      {csrfToken ? <PublishAMI userID={userID} csrfToken={csrfToken} /> : null}
      {csrfToken && retiring ? <RetireAMI image={retiring} userID={userID} csrfToken={csrfToken} close={() => setRetiring(undefined)} /> : null}
    </Stack>
  </Paper>;
}

function RetireAMI({ image, userID, csrfToken, close }: {
  image: { id: string; name: string };
  userID: string;
  csrfToken: string;
  close: () => void;
}) {
  const client = useQueryClient();
  const retirement = useMutation({
    mutationFn: async () => {
      const response = await fetch(`/api/v1/admin/platform-artifacts/${encodeURIComponent(image.id)}`, {
        method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
      });
      if (!response.ok) throw await readProblem(response);
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['template-catalog', userID] });
      close();
    },
  });
  return <Modal opened onClose={close} title="Retire AMI" closeOnEscape={!retirement.isPending}
    closeOnClickOutside={!retirement.isPending} withCloseButton={!retirement.isPending}>
    <Stack>
      <Text size="sm">Retire <strong>{image.name}</strong> from new workspace launches? The image stays registered in AWS.</Text>
      {retirement.isError ? <APIProblemView error={retirement.error} /> : null}
      <Group justify="flex-end">
        <Button variant="default" data-autofocus disabled={retirement.isPending} onClick={close}>Cancel</Button>
        <Button loading={retirement.isPending} onClick={() => retirement.mutate()}>Retire AMI</Button>
      </Group>
    </Stack>
  </Modal>;
}

function parseManifest(value: string): object | undefined {
  try {
    const parsed: unknown = JSON.parse(value);
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) return parsed;
  } catch {
    return undefined;
  }
}

function PublishAMI({ userID, csrfToken }: { userID: string; csrfToken: string }) {
  const storageKey = `shiftbox:ami-family-publish:${userID}`;
  const [operationID, setOperationID] = useState<string | null>(null);
  useEffect(() => setOperationID(window.sessionStorage.getItem(storageKey)), [storageKey]);
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { amiFamily: '', manifest: '' },
    validate: {
      amiFamily: value => /^[a-z0-9][a-z0-9-]{0,62}$/.test(value.trim())
        ? null : 'Use 1–63 lowercase letters, numbers, or hyphens, starting with a letter or number.',
      manifest: value => parseManifest(value) ? null : 'Paste the Packer manifest as JSON.',
    },
  });
  const publication = useMutation({
    mutationFn: async ({ amiFamily, manifest }: { amiFamily: string; manifest: string }) => {
      const response = await fetch('/api/v1/admin/platform-artifacts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() },
        body: JSON.stringify({ amiFamily: amiFamily.trim(), manifest: parseManifest(manifest) }),
      });
      if (!response.ok) throw await readProblem(response);
      return response.json() as Promise<{ id: string }>;
    },
    onSuccess: ({ id }) => {
      window.sessionStorage.setItem(storageKey, id);
      setOperationID(id);
      form.reset();
    },
  });
  return <Stack gap="md">
    <Title order={3} size="h4">Publish an AMI</Title>
    <form noValidate onSubmit={form.onSubmit(values => publication.mutate(values), errors => {
      const field = Object.keys(errors)[0];
      if (field) form.getInputNode(field)?.focus();
    })}>
      <Stack gap="sm">
        <TextInput label="AMI family" description="Enter an existing family name or choose a name for a new family."
          required key={form.key('amiFamily')} {...form.getInputProps('amiFamily')} disabled={publication.isPending} />
        <Textarea label="Packer manifest" required minRows={3} key={form.key('manifest')}
          {...form.getInputProps('manifest')} disabled={publication.isPending} />
        {publication.isError ? <APIProblemView error={publication.error} /> : null}
        <Group><Button type="submit" loading={publication.isPending}>Validate and publish</Button></Group>
      </Stack>
    </form>
    {operationID ? <PublicationProgress key={operationID} id={operationID} userID={userID} /> : null}
  </Stack>;
}

function PublicationProgress({ id, userID }: { id: string; userID: string }) {
  const client = useQueryClient();
  const operation = useOperation(id);
  const status = operation.data?.status;
  useEffect(() => {
    if (status === 'succeeded') void client.invalidateQueries({ queryKey: ['template-catalog', userID] });
  }, [status, client, userID]);
  if (operation.data) return <WorkflowStepper operation={operation.data} failureTitle="AMI validation failed" />;
  if (operation.isPending) return <Text role="status">Loading AMI validation progress…</Text>;
  return operation.isError ? <APIProblemView error={operation.error} /> : null;
}
