import { useEffect, useRef, useState } from 'react';
import { Anchor, Button, Group, Modal, NativeSelect, Stack, Text, TextInput } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import type { operations } from '../../lib/api-types';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import { platformImageLabel } from '../../lib/provider-label';
import type { TemplateRevision } from './use-templates';

type TemplateCatalog = operations['listTemplateCatalog']['responses'][200]['content']['application/json'];

export function TemplateRevisionForm({ template, csrfToken, onClose, refresh, admin }: {
  template?: TemplateRevision;
  admin?: boolean;
  csrfToken: string;
  onClose: () => void;
  refresh: () => Promise<unknown>;
}) {
  const [error, setError] = useState<unknown>();
  const request = useRef<{ body: string; key: string }>(undefined);
  const stale = error instanceof APIProblemError && error.problem.status === 412;
  const catalog = useQuery<TemplateCatalog>({
    queryKey: ['template-catalog'],
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/templates/catalog', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const profiles = catalog.data?.resourceProfiles ?? [];
  const images = catalog.data?.platformImages ?? [];
  const ec2 = catalog.data?.providerKind === 'ec2' || profiles.some(profile => profile.providerKind === 'ec2');
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: {
      name: template?.name ?? '',
      repositoryUrl: template?.repositoryUrl ?? '',
      repositoryRef: template?.repositoryRef ?? '',
      devcontainerPath: template?.devcontainerPath ?? '.devcontainer/devcontainer.json',
      resourceProfileId: template?.resourceProfileId ?? '',
      platformArtifactId: template?.platformArtifactId ?? '',
      networkPolicyId: template?.networkPolicyId ?? '',
      secretNames: template?.secretNames?.join(', ') ?? '',
    },
    validate: {
      resourceProfileId: value => profiles.some(profile => profile.id === value) ? null : 'Select an available resource profile.',
      platformArtifactId: value => ec2 || images.some(image => image.id === value) ? null : 'Select an available platform image.',
    },
  });

  const { getValues, isTouched, setFieldValue } = form;
  const defaultImage = catalog.data?.defaultPlatformArtifactId;
  useEffect(() => {
    if (!template && defaultImage && !isTouched('platformArtifactId') && !getValues().platformArtifactId) {
      setFieldValue('platformArtifactId', defaultImage);
    }
  }, [defaultImage, getValues, isTouched, setFieldValue, template]);

  async function save(values: typeof form.values) {
    if (stale) return;
    setError(undefined);
    const body = JSON.stringify({
      ...values,
      ...(ec2 ? { platformArtifactId: undefined } : {}),
      secretNames: values.secretNames.split(',').map(name => name.trim()).filter(Boolean),
    });
    if (request.current?.body !== body) request.current = { body, key: crypto.randomUUID() };
    const headers: Record<string, string> = { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken,
      'Idempotency-Key': request.current.key };
    if (template) headers['If-Match'] = `"${template.version}"`;
    const endpoint = template ? `/api/v1/templates/${encodeURIComponent(template.id)}/revisions` : '/api/v1/templates';
    try {
      const response = await fetch(endpoint, { method: 'POST', headers, body });
      if (!response.ok) throw await readProblem(response);
      await refresh();
      onClose();
    } catch (error) {
      setError(error);
    }
  }

  return (
    <Modal opened title={template ? 'New revision' : 'New project'} onClose={onClose}
      closeOnEscape={!form.submitting} closeOnClickOutside={!form.submitting}
      withCloseButton={!form.submitting} transitionProps={{ duration: 0 }}>
      <form onSubmit={form.onSubmit(save)}>
        <Stack>
          {template ? <Text size="sm">This revision applies to future launches. Existing workspaces keep their current configuration.</Text> : null}
          <TextInput label="Name" required maxLength={120} key={form.key('name')} {...form.getInputProps('name')} />
          <TextInput label="Repository URL" type="url" required key={form.key('repositoryUrl')} {...form.getInputProps('repositoryUrl')} />
          <TextInput label="Repository ref" key={form.key('repositoryRef')} {...form.getInputProps('repositoryRef')} />
          <TextInput label="Dev Container configuration" required key={form.key('devcontainerPath')} {...form.getInputProps('devcontainerPath')} />
          {catalog.isPending ? <Text role="status">Loading resource profiles and platform images…</Text> : null}
          {catalog.isError ? <>
            <APIProblemView error={catalog.error} />
            <Button variant="default" onClick={() => void catalog.refetch()}>Retry loading choices</Button>
          </> : null}
          {catalog.isSuccess ? <>
            <NativeSelect label="Resource profile" required key={form.key('resourceProfileId')}
              data={[{ value: '', label: 'Select a resource profile' }, ...profiles.map(({ id, name }) => ({ value: id, label: name }))]}
              {...form.getInputProps('resourceProfileId')} />
            {ec2 ? <Text size="sm" c="dimmed">The resource profile defines the AMI image, EC2 instance type, and EBS disk size.</Text> : <NativeSelect label="Platform image" required key={form.key('platformArtifactId')}
              data={[{ value: '', label: 'Select a platform image' }, ...images.map(({ id, name, providerKind }) => ({ value: id, label: platformImageLabel(name, providerKind) }))]}
              {...form.getInputProps('platformArtifactId')} />}
            {!profiles.length ? <Text size="sm">
              {admin ? <>Create a resource profile in <Anchor href="/infrastructure/">Infrastructure</Anchor>.</>
                : 'Ask an administrator to create a resource profile.'}
            </Text> : null}
            {!ec2 && !images.length ? <Text size="sm">An administrator must configure an available platform image.</Text> : null}
          </> : null}
          <TextInput label="Network policy ID" key={form.key('networkPolicyId')} {...form.getInputProps('networkPolicyId')} />
          <TextInput label="Secret names" description="Separate references with commas." key={form.key('secretNames')} {...form.getInputProps('secretNames')} />
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Group justify="flex-end">
            <Button variant="default" disabled={form.submitting} onClick={onClose}>Cancel</Button>
            {stale ? <Button onClick={async () => { await refresh(); onClose(); }}>Refresh projects</Button>
              : <Button type="submit" loading={form.submitting} disabled={!catalog.isSuccess || !profiles.length || (!ec2 && !images.length)}>
                {template ? 'Save revision' : 'Create project'}
              </Button>}
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
