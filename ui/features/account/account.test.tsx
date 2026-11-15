import { TextEncoder } from 'node:util';
import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Account from '../../app/account/page';
import { Providers } from '../../app/providers';

const originalFetch = globalThis.fetch;
const originalEncoder = globalThis.TextEncoder;
beforeAll(() => { globalThis.TextEncoder = TextEncoder; });
afterAll(() => { globalThis.TextEncoder = originalEncoder; });
afterEach(() => { globalThis.fetch = originalFetch; });
const local = { user: { id: 'owner', displayName: 'Alex Developer', primaryEmail: 'alex@example.test', role: 'member', credentialMode: 'local' },
  mode: 'normal', installationState: 'active', permissions: ['session.change_password', 'git.manage', 'secret.manage_user'], csrfToken: 'fixture' };
const json = (data: unknown) => ({ ok: true, status: 200, json: async () => data }) as Response;
function fixture(session = local) {
  globalThis.fetch = jest.fn(async path => {
    if (path === '/api/v1/session') return json(session);
    if (path === '/api/v1/git/authorization') return json({ state: 'connected' });
    if (String(path).startsWith('/api/v1/secrets?')) return json([]);
    throw new Error('Unexpected request');
  });
}

// Opening and cancelling password editing must not change credentials or
// leave a draft password behind when the user opens the dialog again.
test('groups personal settings and clears a cancelled password draft', async () => {
  fixture();
  const user = userEvent.setup();
  render(<Providers><Account /></Providers>);
  expect(await screen.findByText('alex@example.test')).toBeVisible();
  expect(screen.getByRole('heading', { name: 'Personal settings', level: 1 })).toBeVisible();
  expect(screen.getByRole('region', { name: 'Sign-in & security' })).toBeVisible();
  expect(screen.getByRole('region', { name: 'GitHub connection' })).toBeVisible();
  expect(screen.getByRole('region', { name: 'Personal secrets' })).toBeVisible();
  expect(screen.queryByLabelText(/^Current password/)).toBeNull();
  const trigger = screen.getByRole('button', { name: 'Change password' });
  await user.click(trigger);
  const dialog = screen.getByRole('dialog', { name: 'Change password' });
  expect(within(dialog).getByText(/signs you out/i)).toBeVisible();
  expect(within(dialog).getByText(/at least 12 characters/i)).toBeVisible();
  await user.type(within(dialog).getByLabelText(/^Current password/), 'draft-current');
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
  await waitFor(() => expect(trigger).toHaveFocus());
  await user.click(trigger);
  expect(screen.getByLabelText(/^Current password/)).toHaveValue('');
  expect((globalThis.fetch as jest.Mock).mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);
});

// Organizational accounts explain where credentials are managed and never
// offer the local password form or load settings without permission.
test('explains organizational sign-in without offering local password controls', async () => {
  fixture({ ...local, user: { ...local.user, credentialMode: 'external' }, permissions: [] });
  render(<Providers><Account /></Providers>);
  expect(await screen.findByText(/managed by your organization’s identity provider/i)).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Change password' })).toBeNull();
  expect(screen.queryByRole('region', { name: 'GitHub connection' })).toBeNull();
  expect(screen.queryByRole('region', { name: 'Personal secrets' })).toBeNull();
  expect((globalThis.fetch as jest.Mock).mock.calls.every(([path]) => path === '/api/v1/session')).toBe(true);
});

// A forced password change stays visible immediately and does not load
// GitHub or personal secrets even if an inconsistent session lists permissions.
test('keeps the required password change inline and gates other settings', async () => {
  fixture({ ...local, mode: 'password_change' });
  render(<Providers><Account /></Providers>);
  expect(await screen.findByLabelText(/^Current password/)).toBeVisible();
  expect(screen.getByText(/change your password to continue/i)).toBeVisible();
  expect(screen.queryByRole('region', { name: 'GitHub connection' })).toBeNull();
  expect(screen.queryByRole('region', { name: 'Personal secrets' })).toBeNull();
});

// While a password request is in flight the dialog cannot be dismissed;
// success removes personal settings and requires a fresh sign-in.
test('waits for a password change before retiring the session', async () => {
  let finish: (response: Response) => void = () => {};
  globalThis.fetch = jest.fn(async (path, init) => {
    if (path === '/api/v1/session') return json({ ...local, permissions: ['session.change_password'] });
    if (init?.method === 'PUT') return new Promise<Response>(resolve => { finish = resolve; });
    throw new Error('Unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Account /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Change password' }));
  const dialog = screen.getByRole('dialog', { name: 'Change password' });
  await user.type(within(dialog).getByLabelText(/^Current password/), 'current-fixture');
  await user.type(within(dialog).getByLabelText(/^New password/), 'replacement-fixture');
  await user.type(within(dialog).getByLabelText(/^Confirm password/), 'replacement-fixture');
  await user.click(within(dialog).getByRole('button', { name: 'Change password' }));
  expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled();
  await user.keyboard('{Escape}');
  expect(dialog).toBeVisible();
  finish({ ok: true, status: 204 } as Response);
  expect(await screen.findByRole('link', { name: 'Sign in' })).toBeVisible();
  expect(screen.queryByRole('region', { name: 'Your profile' })).toBeNull();
});
