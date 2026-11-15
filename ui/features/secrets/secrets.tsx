import { useState, type FormEvent } from 'react';
import { Button, Checkbox, Group, Modal, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

type Scope = 'user' | 'template' | 'organization';
const scopeLabels = { user: 'Personal', template: 'Project template', organization: 'Organization' };
const namePattern = /^[A-Z][A-Z0-9_]{0,126}$/;

interface SecretMetadata {
  name: string;
  explicitEmpty: boolean;
  fallbackScope?: Scope;
  updatedAt: string;
}

type SecretEditor = { action: 'save'; name?: string } | { action: 'delete'; item: SecretMetadata };

export function Secrets({ scope, scopeID, userID, csrfToken, description }: {
  scope: Scope;
  scopeID: string;
  userID: string;
  csrfToken: string;
  description?: string;
}) {
  const metadata = useQuery({
    queryKey: ['secret-metadata', userID, scope, scopeID],
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({ scope, scopeId: scopeID });
      const response = await fetch('/api/v1/secrets?' + params, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      const rows: SecretMetadata[] = await response.json();
      // Keep the client cache metadata-only as well.
      return rows.map(({ name, explicitEmpty, fallbackScope, updatedAt }) => ({ name, explicitEmpty, fallbackScope, updatedAt }));
    },
  });
  const [editor, setEditor] = useState<SecretEditor>();
  const [status, setStatus] = useState('');
  const rows = metadata.isError ? undefined : metadata.data;

  async function saved(message: string) {
    setEditor(undefined);
    setStatus(message);
    await metadata.refetch();
  }

  return (
    <Stack align="stretch" gap="sm">
      <Group justify="space-between">
        <Title order={2}>{scopeLabels[scope]} secrets</Title>
        {rows ? <Button onClick={() => { setStatus(''); setEditor({ action: 'save' }); }}>Add secret</Button> : null}
      </Group>
      {description ? <Text size="sm" c="dimmed">{description}</Text> : null}
      {status ? <Text role="status" size="sm">{status}</Text> : null}
      {metadata.isPending ? <Text role="status">Loading secrets…</Text> : null}
      {metadata.isError ? <>
        <APIProblemView error={metadata.error} />
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void metadata.refetch()}>Retry</Button>
      </> : null}
      {rows?.length === 0 ? <Text size="sm">No secrets in this scope.</Text> : null}
      {rows?.length ? (
        <Table.ScrollContainer minWidth={540}>
          <Table aria-label={scopeLabels[scope] + ' secret metadata'}>
            <Table.Thead><Table.Tr>
              <Table.Th>Name</Table.Th><Table.Th>Override</Table.Th><Table.Th>Fallback</Table.Th>
              <Table.Th>Updated</Table.Th><Table.Th>Actions</Table.Th>
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {rows.map(item => <Table.Tr key={item.name}>
                <Table.Th scope="row">{item.name}</Table.Th>
                <Table.Td>{item.explicitEmpty ? 'Explicitly empty' : 'Set'}</Table.Td>
                <Table.Td>{fallbackLabel(scope, item.fallbackScope)}</Table.Td>
                <Table.Td><time dateTime={item.updatedAt}>{new Date(item.updatedAt).toLocaleString()}</time></Table.Td>
                <Table.Td>
                  <Group gap="xs" wrap="nowrap">
                    <Button size="compact-sm" variant="subtle" aria-label={'Replace ' + item.name}
                      onClick={() => { setStatus(''); setEditor({ action: 'save', name: item.name }); }}>Replace</Button>
                    <Button size="compact-sm" color="red" variant="subtle" aria-label={'Delete ' + item.name}
                      onClick={() => { setStatus(''); setEditor({ action: 'delete', item }); }}>Delete</Button>
                  </Group>
                </Table.Td>
              </Table.Tr>)}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      ) : null}
      {editor ? <SecretDialog editor={editor} scope={scope} scopeID={scopeID} csrfToken={csrfToken}
        onClose={() => setEditor(undefined)} onSaved={saved} /> : null}
    </Stack>
  );
}

function fallbackLabel(scope: Scope, fallback?: Scope) {
  if (scope === 'user') return fallback === 'organization' ? 'Project template or Organization' : 'Project template, if available';
  return fallback === 'organization' ? 'Organization' : 'None';
}

function fallbackWarning(scope: Scope, fallback?: Scope) {
  if (scope === 'user') {
    return fallback === 'organization'
      ? 'An authorized Project template value will take effect where available. Otherwise the Organization value will take effect.'
      : 'An authorized Project template value will take effect where available. Other workspaces will have no value for this name.';
  }
  return fallback === 'organization'
    ? 'The Organization value will take effect where no Personal override applies.'
    : scope === 'template'
      ? 'Workspaces without a Personal override will have no value for this name.'
      : 'Workspaces without a Personal or Project template override will have no value for this name.';
}

function SecretDialog({ editor, scope, scopeID, csrfToken, onClose, onSaved }: {
  editor: SecretEditor;
  scope: Scope;
  scopeID: string;
  csrfToken: string;
  onClose: () => void;
  onSaved: (message: string) => Promise<unknown>;
}) {
  const deleting = editor.action === 'delete';
  const initialName = deleting ? editor.item.name : editor.name;
  const [name, setName] = useState(initialName ?? '');
  const [value, setValue] = useState('');
  const [empty, setEmpty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const valid = deleting || (namePattern.test(name) && (empty || value.length > 0));

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !valid) return;
    setBusy(true);
    setError(undefined);
    const url = '/api/v1/secrets/' + [scope, scopeID, name].map(encodeURIComponent).join('/');
    try {
      const headers: Record<string, string> = { 'X-CSRF-Token': csrfToken };
      if (deleting) headers['X-Acknowledge-Fallback'] = 'acknowledged';
      else headers['Content-Type'] = 'application/json';
      const response = await fetch(url, {
        method: deleting ? 'DELETE' : 'PUT', headers,
        body: deleting ? undefined : JSON.stringify({ value: empty ? '' : value }),
      });
      if (!response.ok) {
        // Save errors may come from configuration or an intermediary. Keep
        // arbitrary field/detail text away from the write-only input.
        if (!deleting) throw new Error('secret could not be saved');
        throw await readProblem(response);
      }
      setValue('');
      void onSaved(deleting ? 'Secret deleted.' : 'Secret saved.');
    } catch (error) {
      setError(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened title={deleting ? 'Delete secret' : initialName ? 'Replace secret' : 'Add secret'} onClose={onClose}
      closeOnEscape={!busy} closeOnClickOutside={!busy} withCloseButton={!busy}
      closeButtonProps={{ 'aria-label': 'Close secret dialog' }} transitionProps={{ duration: 0 }}>
      <form onSubmit={submit}>
        <Stack>
          {initialName ? <Text fw={600}>{initialName}</Text> : (
            <TextInput label="Secret name" description="Use uppercase letters, digits, and underscores. Start with a letter."
              value={name} onChange={event => setName(event.currentTarget.value)} maxLength={127} disabled={busy} data-autofocus />
          )}
          {deleting ? <Text>{fallbackWarning(scope, editor.item.fallbackScope)}</Text> : <>
            <TextInput label="Secret value" type="password" autoComplete="off" value={value} disabled={busy || empty}
              onChange={event => setValue(event.currentTarget.value)} data-autofocus={!!initialName} />
            <Checkbox label="Set an explicit empty value" checked={empty} disabled={busy} onChange={event => {
              setEmpty(event.currentTarget.checked);
              if (event.currentTarget.checked) setValue('');
            }} />
          </>}
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Group justify="flex-end">
            <Button variant="default" disabled={busy} onClick={onClose} data-autofocus={deleting}>Cancel</Button>
            <Button type="submit" color={deleting ? 'red' : undefined} loading={busy} disabled={!valid}>
              {deleting ? 'Confirm deletion' : 'Save secret'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
