import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import Account from '../../app/account/page';
import { AdministrationSettings } from '../admin/settings';
import { Templates } from '../templates/templates';

import { Secrets } from './secrets';

const originalFetch = globalThis.fetch;
beforeEach(() => {
  // Menu visibility needs layout measurements that jsdom does not provide.
  jest.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 40, width: 100, height: 40, toJSON: () => ({}),
  });
  jest.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1024);
  jest.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(768);
});
afterEach(() => { globalThis.fetch = originalFetch; jest.restoreAllMocks(); });

const now = '2026-11-14T00:00:00Z';
const secret = { name: 'API_KEY', scope: 'template', scopeId: 'project', explicitEmpty: true,
  fallbackScope: 'organization', updatedAt: now, value: 'unexpected-private-fixture' };

// Editing never preloads a value from metadata. Empty overrides require
// intent, and successful storage clears the input before reopening.
test('shows metadata and writes an explicit empty override', async () => {
  const fetch = jest.fn(async (_path: string, init?: RequestInit) => ({
    ok: true, status: init?.method === 'PUT' ? 204 : 200, json: async () => [secret],
  }));
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch;
  const user = userEvent.setup();
  render(<Providers><Secrets scope="template" scopeID="project" userID="owner" csrfToken="fixture" /></Providers>);
  expect(await screen.findByText('API_KEY')).toBeVisible();
  expect(screen.getByText('Explicitly empty')).toBeVisible();
  expect(screen.queryByText(secret.value)).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Replace API_KEY' }));
  const dialog = screen.getByRole('dialog', { name: 'Replace secret' });
  expect(within(dialog).getByLabelText('Secret value')).toHaveValue('');
  const save = within(dialog).getByRole('button', { name: 'Save secret' });
  expect(save).toBeDisabled();
  await user.click(within(dialog).getByRole('checkbox', { name: 'Set an explicit empty value' }));
  await user.click(save);
  expect(fetch).toHaveBeenCalledWith('/api/v1/secrets/template/project/API_KEY', expect.objectContaining({
    method: 'PUT', body: '{"value":""}', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
  }));
  await screen.findByText('Secret saved.');
  await user.click(screen.getByRole('button', { name: 'Replace API_KEY' }));
  expect(screen.getByLabelText('Secret value')).toHaveValue('');
});

// Deleting an override names the fallback before the administrator can
// confirm, and sends the acknowledgement with the exact scope and key.
test('requires confirmation before revealing organization fallback', async () => {
  const fetch = jest.fn(async (_path: string, init?: RequestInit) => ({
    ok: true, status: init?.method === 'DELETE' ? 204 : 200, json: async () => [secret],
  }));
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch;
  const user = userEvent.setup();
  render(<Providers><Secrets scope="template" scopeID="project" userID="owner" csrfToken="fixture" /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Delete API_KEY' }));
  expect(screen.getByText(/Organization value will take effect/)).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
  await user.click(screen.getByRole('button', { name: 'Confirm deletion' }));
  expect(fetch).toHaveBeenCalledWith('/api/v1/secrets/template/project/API_KEY', expect.objectContaining({
    method: 'DELETE', headers: expect.objectContaining({
      'X-Acknowledge-Fallback': 'acknowledged', 'X-CSRF-Token': 'fixture',
    }),
  }));
  expect(await screen.findByText('Secret deleted.')).toBeVisible();
});

// Account uses the signed-in user's ID for personal secret management.
test('lists only the signed-in user’s personal secret metadata on Account', async () => {
  const fetch = jest.fn(async (path: string) => ({
    ok: true, status: 200, json: async () => path === '/api/v1/session'
      ? { user: { id: 'owner', role: 'member' }, mode: 'normal', installationState: 'active',
        permissions: ['secret.manage_user'], csrfToken: 'fixture' }
      : [],
  }));
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch;
  render(<Providers><Account /></Providers>);
  expect(await screen.findByRole('heading', { name: 'Personal secrets' })).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/secrets?scope=user&scopeId=owner', expect.anything());
  expect(screen.queryByRole('heading', { name: 'Organization secrets' })).not.toBeInTheDocument();
});

// Administration Settings takes the organization scope from the authorized session.
test('opens organization secrets with the server-provided scope', async () => {
  const fetch = jest.fn(async (path: string) => ({
    ok: true, status: 200, json: async () => {
      if (path === '/api/v1/session') return { user: { id: 'admin', role: 'administrator' }, mode: 'normal',
        installationState: 'active', permissions: ['infrastructure.view', 'secret.manage_organization'],
        csrfToken: 'fixture', organizationSecretScopeId: 'installation' };
      if (path === '/api/v1/admin/infrastructure') return { provider: {}, capacity: {}, conditions: [] };
      return [];
    },
  }));
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch;
  const user = userEvent.setup();
  render(<Providers><AdministrationSettings /></Providers>);
  await user.click(await screen.findByRole('tab', { name: 'Secrets' }));
  expect(await screen.findByRole('heading', { name: 'Organization secrets' })).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/secrets?scope=organization&scopeId=installation', expect.anything());
});

// A launch-only user cannot open a project's secret manager.
test('offers project secret management only for projects the user manages', async () => {
  globalThis.fetch = jest.fn(async (path: string) => ({
    ok: true, status: 200, json: async () => {
      if (path === '/api/v1/session') return { user: { id: 'owner', role: 'member' }, mode: 'normal',
        installationState: 'active', permissions: [], csrfToken: 'fixture' };
      if (path === '/api/v1/templates') return { items: [
        { id: 'own-project', name: 'My project', ownerId: 'owner', state: 'active' },
        { id: 'shared-project', name: 'Shared project', ownerId: 'another', state: 'active' },
      ] };
      return [];
    },
  })) as unknown as typeof globalThis.fetch;
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  const own = await screen.findByRole('article', { name: 'My project' });
  const shared = screen.getByRole('article', { name: 'Shared project' });
  expect(within(shared).queryByRole('button', { name: /Actions for/ })).not.toBeInTheDocument();
  await user.click(within(own).getByRole('button', { name: 'Actions for My project' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Project secrets' }));
  await screen.findByRole('heading', { name: 'Project secrets' });
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/secrets?scope=template&scopeId=own-project', expect.anything());
});
