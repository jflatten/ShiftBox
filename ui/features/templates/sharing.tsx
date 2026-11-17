import { useRef, useState, type FormEvent } from 'react';
import { Alert, Button, Checkbox, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import type { TemplateRevision } from './use-templates';

export function TemplateSharing({ template, csrfToken, onClose, refresh }: {
  template: TemplateRevision;
  csrfToken: string;
  onClose: () => void;
  refresh: () => Promise<{ data?: { items: TemplateRevision[] }; isError: boolean }>;
}) {
  const [userID, setUserID] = useState('');
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const [grants, setGrants] = useState(template.launchGrants ?? []);
  const request = useRef<{ userID: string; key: string }>(undefined);
  const validID = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(userID.trim());

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !validID || !acknowledged) return;
    const id = userID.trim();
    if (request.current?.userID !== id) request.current = { userID: id, key: crypto.randomUUID() };
    setBusy(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/templates/${encodeURIComponent(template.id)}/grants`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': request.current.key, 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ userId: id, acknowledgeProjectSecretExposure: true }),
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

  async function revoke(id: string) {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/templates/${encodeURIComponent(template.id)}/grants/${encodeURIComponent(id)}`, {
        method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
      });
      if (!response.ok) throw await readProblem(response);
      setGrants(current => current.filter(grant => grant.userId !== id));
      const updated = await refresh();
      if (!updated.isError) {
        const current = updated.data?.items.find(item => item.id === template.id);
        if (current) setGrants(current.launchGrants ?? []);
      }
    } catch (error) {
      setError(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened title="Share launch access" onClose={onClose} closeOnClickOutside={!busy}
      closeOnEscape={!busy} withCloseButton={!busy} transitionProps={{ duration: 0 }}>
      <form onSubmit={save}>
        <Stack>
          <Text fw={600}>{template.name}</Text>
          {grants.length ? <>
            <Text size="sm">Revoking access blocks new launches and future project-secret retrieval. Existing workspaces and files are preserved.</Text>
            {grants.map(grant => <Group key={grant.userId} justify="space-between">
              <Text size="sm">{grant.email}</Text>
              <Button size="compact-sm" color="red" variant="subtle" disabled={busy}
                aria-label={`Revoke launch access for ${grant.email}`} onClick={() => void revoke(grant.userId)}>Revoke</Button>
            </Group>)}
          </> : <Text size="sm">No launch grants.</Text>}
          {template.state !== 'archived' ? <>
            <TextInput label="User ID" value={userID} disabled={busy} onChange={event => setUserID(event.currentTarget.value)} data-autofocus />
            <Alert color="yellow">Authorized workspace code can retrieve project secrets when this user launches from the project.</Alert>
            <Checkbox label="I acknowledge project secret access" checked={acknowledged} disabled={busy}
              onChange={event => setAcknowledged(event.currentTarget.checked)} />
          </> : null}
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Group justify="flex-end">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            {template.state !== 'archived' ? (
              <Button type="submit" loading={busy} disabled={!validID || !acknowledged}>Grant launch access</Button>
            ) : null}
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
