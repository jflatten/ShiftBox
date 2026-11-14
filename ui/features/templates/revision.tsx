import { useRef, useState } from 'react';
import { Button, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import type { TemplateRevision } from './use-templates';

export function TemplateRevisionForm({ template, csrfToken, onClose, refresh }: {
  template?: TemplateRevision;
  csrfToken: string;
  onClose: () => void;
  refresh: () => Promise<unknown>;
}) {
  const [error, setError] = useState<unknown>();
  const request = useRef<{ body: string; key: string }>(undefined);
  const stale = error instanceof APIProblemError && error.problem.status === 412;
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
  });

  async function save(values: typeof form.values) {
    if (stale) return;
    setError(undefined);
    const body = JSON.stringify({
      ...values,
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
    <Modal opened title={template ? 'New revision' : 'New template'} onClose={onClose}
      closeOnEscape={!form.submitting} closeOnClickOutside={!form.submitting}
      withCloseButton={!form.submitting} transitionProps={{ duration: 0 }}>
      <form onSubmit={form.onSubmit(save)}>
        <Stack>
          {template ? <Text size="sm">This revision applies to future launches. Existing workspaces keep their current configuration.</Text> : null}
          <TextInput label="Name" required maxLength={120} key={form.key('name')} {...form.getInputProps('name')} />
          <TextInput label="Repository URL" type="url" required key={form.key('repositoryUrl')} {...form.getInputProps('repositoryUrl')} />
          <TextInput label="Repository ref" key={form.key('repositoryRef')} {...form.getInputProps('repositoryRef')} />
          <TextInput label="Dev Container configuration" required key={form.key('devcontainerPath')} {...form.getInputProps('devcontainerPath')} />
          <TextInput label="Resource profile ID" required key={form.key('resourceProfileId')} {...form.getInputProps('resourceProfileId')} />
          <TextInput label="Platform image ID" required key={form.key('platformArtifactId')} {...form.getInputProps('platformArtifactId')} />
          <TextInput label="Network policy ID" key={form.key('networkPolicyId')} {...form.getInputProps('networkPolicyId')} />
          <TextInput label="Secret names" description="Separate references with commas." key={form.key('secretNames')} {...form.getInputProps('secretNames')} />
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Group justify="flex-end">
            <Button variant="default" disabled={form.submitting} onClick={onClose}>Cancel</Button>
            {stale ? <Button onClick={async () => { await refresh(); onClose(); }}>Refresh templates</Button>
              : <Button type="submit" loading={form.submitting}>{template ? 'Save revision' : 'Create template'}</Button>}
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
