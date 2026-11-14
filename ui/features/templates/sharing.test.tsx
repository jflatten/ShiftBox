import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Templates } from './templates';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const project = { id: 'template', ownerId: 'owner', name: 'Sample', state: 'active', version: 4,
  currentRevision: 2, repositoryUrl: 'https://github.com/example/sample', resourceProfileId: 'small',
  platformArtifactId: 'platform', launchGrants: [] };
const memberID = 'a1234567-1234-4321-8765-123456789abc';
const fixture = (role: string, fetch: (path: string, init?: RequestInit) => unknown) => {
  globalThis.fetch = jest.fn(async (path: string, init?: RequestInit) => {
    const data = path === '/api/v1/session'
      ? { user: { id: 'owner', role }, mode: 'normal', installationState: 'active', permissions: [], csrfToken: 'fixture' }
      : fetch(path, init);
    return { ok: true, status: 200, json: async () => data } as Response;
  }) as unknown as typeof globalThis.fetch;
};

// A template owner must explicitly acknowledge project secret access
// before a grant can be submitted. Members never get the archive filter.
test('requires project secret acknowledgement when sharing a template', async () => {
  fixture('member', path => path === '/api/v1/templates' ? { items: [project] } : null);
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Share launch access' }));
  const dialog = screen.getByRole('dialog', { name: 'Share launch access' });
  expect(within(dialog).getByText(/workspace code can retrieve project secrets/i)).toBeVisible();
  const submit = within(dialog).getByRole('button', { name: 'Grant launch access' });
  await user.type(within(dialog).getByLabelText('User ID'), memberID);
  expect(submit).toBeDisabled();
  await user.click(within(dialog).getByRole('checkbox', { name: 'I acknowledge project secret access' }));
  await user.click(submit);
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template/grants', expect.objectContaining({
    method: 'POST',
    headers: expect.objectContaining({ 'Idempotency-Key': expect.any(String), 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ userId: memberID, acknowledgeProjectSecretExposure: true }),
  }));
  expect(screen.queryByRole('switch', { name: 'Archived templates' })).not.toBeInTheDocument();
});

// Administrators explicitly switch to the archived list before restoring
// a template. Restoring sends the version the administrator reviewed.
test('filters archived templates and restores with the reviewed version', async () => {
  let archived = true;
  fixture('administrator', (path, init) => {
    if (path === '/api/v1/templates/template' && init?.method === 'PATCH') {
      archived = false;
      return null;
    }
    if (path === '/api/v1/templates?archived=true') return { items: archived ? [{ ...project, state: 'archived' }] : [] };
    return { items: [] };
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await screen.findByText('No templates yet.');
  expect(globalThis.fetch).not.toHaveBeenCalledWith('/api/v1/templates?archived=true', expect.anything());
  await user.click(screen.getByRole('switch', { name: 'Archived templates' }));
  await user.click(await screen.findByRole('button', { name: 'Restore template' }));
  const dialog = screen.getByRole('dialog', { name: 'Restore template' });
  await user.click(within(dialog).getByRole('button', { name: 'Restore template' }));
  expect(await screen.findByText('No archived templates.')).toBeVisible();
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template', expect.objectContaining({
    method: 'PATCH', body: '{"state":"active"}', headers: expect.objectContaining({ 'If-Match': '"4"' }),
  }));
});

// Revising a template preserves its current fields and submits the observed
// version so a concurrent edit cannot silently replace the current revision.
test('creates a new revision from the reviewed template', async () => {
  let current = project;
  fixture('member', (path, init) => {
    if (path.endsWith('/revisions') && init?.method === 'POST') {
      current = { ...project, currentRevision: 3 };
      return current;
    }
    return { items: [current] };
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'New revision' }));
  const dialog = screen.getByRole('dialog', { name: 'New revision' });
  expect(within(dialog).getByLabelText(/^Repository URL/)).toHaveValue(project.repositoryUrl);
  await user.type(within(dialog).getByLabelText('Repository ref'), 'next');
  await user.click(within(dialog).getByRole('button', { name: 'Save revision' }));
  expect(await screen.findByRole('cell', { name: '3' })).toBeVisible();
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template/revisions', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'Idempotency-Key': expect.any(String) }),
  }));
});

// A template manager can revoke a previously granted user's access from
// the server's list. Revocation waits for an explicit click.
test('revokes an existing launch grant from the sharing dialog', async () => {
  let grants = [{ userId: memberID, email: 'member@example.test' }];
  fixture('member', (path, init) => {
    if (path.endsWith(memberID) && init?.method === 'DELETE') {
      grants = [];
      return null;
    }
    return { items: [{ ...project, launchGrants: grants }] };
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Share launch access' }));
  expect(screen.getByText('member@example.test')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Revoke launch access for member@example.test' }));
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template/grants/' + memberID, expect.objectContaining({
    method: 'DELETE', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
  }));
  expect(await screen.findByText('No launch grants.')).toBeVisible();
});
