import { useState } from 'react';
import { Button, Group, Modal, Stack, Text } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import type { TemplateRevision } from './use-templates';

export function TemplateArchive({ template, csrfToken, onClose, refresh }: {
  template: TemplateRevision;
  csrfToken: string;
  onClose: () => void;
  refresh: () => Promise<unknown>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const stale = error instanceof APIProblemError && error.problem.status === 412;
  const restoring = template.state === 'archived';
  const title = restoring ? 'Restore project' : 'Archive project';

  async function save() {
    if (busy || stale) return;
    setBusy(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/templates/${encodeURIComponent(template.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'If-Match': `"${template.version}"`, 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ state: restoring ? 'active' : 'archived' }),
      });
      if (!response.ok) throw await readProblem(response);
      await refresh();
      onClose();
    } catch (error) {
      setError(error);
    } finally {
      setBusy(false);
    }
  }

  async function refreshAndClose() {
    await refresh();
    onClose();
  }

  return (
    <Modal opened title={title} onClose={onClose} closeOnEscape={!busy} closeOnClickOutside={!busy}
      withCloseButton={!busy} transitionProps={{ duration: 0 }}>
      <Stack>
        <Text fw={600}>{template.name}</Text>
        <Text>{restoring
          ? 'Restoring allows authorized users to launch again with the existing grants.'
          : 'Archiving prevents new launches. Existing workspaces, files, and grants are preserved.'}</Text>
        {error !== undefined ? <APIProblemView error={error} /> : null}
        <Group justify="flex-end">
          <Button variant="default" disabled={busy} onClick={onClose} data-autofocus>Cancel</Button>
          {stale ? <Button onClick={() => void refreshAndClose()}>Refresh projects</Button>
            : <Button color={restoring ? undefined : 'red'} loading={busy} onClick={() => void save()}>{title}</Button>}
        </Group>
      </Stack>
    </Modal>
  );
}
