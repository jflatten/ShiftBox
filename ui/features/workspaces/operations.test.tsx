import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Workspaces } from './workspaces';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

// A workspace owner can add a port by keyboard and save the exact versioned
// configuration. The new port is available only after the server accepts it.
test('saves a manually configured preview port', async () => {
  const id = 'workspace-one';
  let previewPorts: number[] = [];
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return { ok: true, json: async () => ({
      user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
      permissions: [], csrfToken: 'fixture',
    }) } as Response;
    if (input === `/api/v1/workspaces/${id}`) {
      if (init?.method === 'PATCH') previewPorts = JSON.parse(String(init.body)).previewPorts;
      return { ok: true, json: async () => ({ id, ownerId: 'member', desiredState: 'running', observedState: 'running',
        currentOperationId: null, version: 4, previewPorts, canEditPreviewPorts: true, canOpenPreviews: true,
        resources: { cpuCount: 2, memoryMiB: 4096, diskMiB: 32768 } }) } as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={id} /></Providers>);
  const ports = await screen.findByRole('textbox', { name: 'Preview ports' });
  await user.type(ports, '8080');
  await user.click(screen.getByRole('button', { name: 'Save preview ports' }));
  expect(fetch).toHaveBeenCalledWith(`/api/v1/workspaces/${id}`, expect.objectContaining({
    method: 'PATCH', body: '{"previewPorts":[8080]}',
    headers: expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': 'fixture' }),
  }));
  expect(await screen.findByRole('button', { name: 'Open preview 8080' })).toBeVisible();
});

// A keyboard user can inspect the consequences of deletion, cancel with
// Escape, and continue from the original action without submitting a request.
test('restores keyboard focus after cancelling workspace deletion', async () => {
  const id = 'workspace-one';
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (init?.method === 'POST') throw new Error('unexpected workspace mutation');
    if (input === '/api/v1/session') return { ok: true, json: async () => ({
      user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
      permissions: [], csrfToken: 'fixture',
    }) } as Response;
    if (input === `/api/v1/workspaces/${id}`) return { ok: true, json: async () => ({
      id, name: 'Keyboard workspace', ownerId: 'member', templateId: 'template-one', templateRevision: 1,
      desiredState: 'stopped', observedState: 'stopped', currentOperationId: null, version: 4,
      platformRelease: 'release', resources: { cpuCount: 2, memoryMiB: 4096, diskMiB: 32768 },
    }) } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={id} /></Providers>);
  const deletion = await screen.findByRole('button', { name: 'Delete workspace' });
  // The compact navigation toggle is the first keyboard stop. Opening it
  // keeps the page title present and exposes the labeled workspace link.
  const toggle = screen.getByRole('button', { name: 'Toggle navigation' });
  await user.tab();
  expect(toggle).toHaveFocus();
  expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await user.keyboard('{Enter}');
  expect(toggle).toHaveAttribute('aria-expanded', 'true');
  expect(screen.getByRole('heading', { name: 'Keyboard workspace' })).toBeVisible();
  await user.tab();
  expect(screen.getByRole('button', { name: 'User menu' })).toHaveFocus();
  await user.tab();
  const nav = within(screen.getByRole('navigation', { name: 'Main navigation' }));
  expect(nav.getByRole('link', { name: 'Workspaces' })).toHaveFocus();
  deletion.focus();
  await user.keyboard('{Enter}');
  const dialog = within(await screen.findByRole('dialog', { name: 'Delete workspace' }));
  await waitFor(() => expect(dialog.getByRole('button', { name: 'Cancel' })).toHaveFocus());
  expect(dialog.getByText(/including project files, the developer home, and named application volumes/i)).toBeVisible();
  expect(dialog.getByRole('button', { name: 'Delete permanently' })).toBeDisabled();
  await user.keyboard('{Escape}');
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Delete workspace' })).not.toBeInTheDocument());
  await waitFor(() => expect(deletion).toHaveFocus());
  expect(fetch.mock.calls.every(([, init]) => init?.method !== 'POST')).toBe(true);
});
