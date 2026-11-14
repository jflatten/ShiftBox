import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { People } from './people';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['user.list'], csrfToken: 'fixture',
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// A failed membership read can be retried and then shows the distinct
// account states and credential modes returned by the controller.
test('retries a failed membership read and shows account states', async () => {
  let reads = 0;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input !== '/api/v1/admin/users') throw new Error('unexpected request');
    reads++;
    if (reads === 1) return { ok: false, status: 503, headers: new Headers() } as Response;
    return json([
      { id: 'owner', primaryEmail: 'admin@example.test', membershipState: 'active', credentialMode: 'local', role: 'administrator' },
      { id: 'pending', primaryEmail: 'pending@example.test', membershipState: 'pending', credentialMode: 'external', role: 'member' },
      { id: 'disabled', primaryEmail: 'disabled@example.test', membershipState: 'disabled', credentialMode: 'local', role: 'member' },
    ]);
  });
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  expect(await screen.findByRole('alert')).toHaveTextContent('Unable to complete the request');
  await user.click(screen.getByRole('button', { name: 'Retry' }));
  const active = await screen.findByRole('row', { name: /admin@example.test/ });
  const pending = screen.getByRole('row', { name: /pending@example.test/ });
  const disabled = screen.getByRole('row', { name: /disabled@example.test/ });
  expect(within(active).getByText('Active')).toBeVisible();
  expect(within(active).getByText('Local')).toBeVisible();
  expect(within(pending).getByText('Pending')).toBeVisible();
  expect(within(pending).getByText('External')).toBeVisible();
  expect(within(disabled).getByText('Disabled')).toBeVisible();
  expect(within(disabled).getByRole('link', { name: /View workspaces/ })).toHaveAttribute('href', '/workspaces/?owner=disabled');
  expect(reads).toBe(2);
  expect(screen.queryByRole('button', { name: 'Create local account' })).toBeNull();
});

// Local account creation requires an explicit role and shows the temporary
// password only until the administrator acknowledges private delivery.
test('creates a local account and clears the one-time password', async () => {
  const account = { id: 'new-user', primaryEmail: 'new@example.test', role: 'administrator',
    membershipState: 'active', credentialMode: 'local', version: 1 };
  let created = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (path, init) => {
    if (path === '/api/v1/session') return json({ ...session, permissions: ['user.list', 'user.create'] });
    if (path === '/api/v1/admin/users' && init?.method === 'POST') {
      created = true;
      return { ...json({ user: account, temporaryPassword: 'one-time-fixture' }), status: 201 } as Response;
    }
    if (path === '/api/v1/admin/users') return json(created ? [account] : []);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  const submit = await screen.findByRole('button', { name: 'Create local account' });
  await user.type(screen.getByLabelText(/^Email address/), account.primaryEmail);
  await user.click(submit);
  expect(screen.getByText('Choose a role.')).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
  await user.click(screen.getByRole('radio', { name: 'Administrator' }));
  await user.click(submit);
  expect(await screen.findByText('one-time-fixture')).toBeVisible();
  const row = await screen.findByRole('row', { name: /new@example.test/ });
  expect(within(row).getByText('Active')).toBeVisible();
  const done = screen.getByRole('button', { name: 'Done' });
  expect(done).toBeDisabled();
  await user.click(screen.getByRole('checkbox', { name: 'I saved the temporary password' }));
  await user.click(done);
  expect(screen.queryByText('one-time-fixture')).toBeNull();
  expect(screen.getByLabelText(/^Email address/)).toHaveValue('');
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/users', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ email: account.primaryEmail, role: 'administrator' }),
  }));
});

// Direct navigation by a member never fetches the administrator's list.
test('requires administrator access before reading memberships', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'member', role: 'member' }, permissions: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><People /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session']);
});

// Approval starts with no selected role and sends the choice with the
// membership version. The table refreshes after the controller commits it.
test('approves a pending account with an explicitly selected role', async () => {
  const account = { id: 'pending', primaryEmail: 'pending@example.test', membershipState: 'pending',
    credentialMode: 'external', role: 'member', version: 2 };
  let approved = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['user.list', 'user.approve'] });
    if (input === '/api/v1/admin/users') return json([approved
      ? { ...account, membershipState: 'active', role: 'administrator', version: 3 } : account]);
    if (input === '/api/v1/admin/users/pending' && init?.method === 'PATCH') {
      approved = true;
      return json(null);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  const row = await screen.findByRole('row', { name: /pending@example.test/ });
  const role = within(row).getByRole('combobox', { name: /Role for/ });
  const approve = within(row).getByRole('button', { name: 'Approve' });
  expect(role).toHaveValue('');
  expect(approve).toBeDisabled();
  await user.selectOptions(role, 'administrator');
  await user.click(approve);
  expect(await within(row).findByText('Active')).toBeVisible();
  expect(within(row).getByText('Administrator')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/users/pending', expect.objectContaining({
    method: 'PATCH',
    headers: expect.objectContaining({ 'If-Match': '"2"', 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ membershipState: 'active', role: 'administrator' }),
  }));
});

// The role form sends only the changed role, and shows a controller refusal
// without visually demoting the account.
test('keeps the role unchanged when the controller protects the last administrator', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['user.list', 'user.change_role'] });
    if (input === '/api/v1/admin/users') return json([{ id: 'operator', primaryEmail: 'admin@example.test',
      membershipState: 'active', credentialMode: 'local', role: 'administrator', version: 1 }]);
    if (input === '/api/v1/admin/users/operator' && init?.method === 'PATCH') return {
      ok: false, status: 409, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
      json: async () => ({ type: 'about:blank', title: 'Keep at least one active administrator with sign-in access',
        code: 'user.last_administrator', status: 409, requestId: 'ref' }),
    } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  const row = await screen.findByRole('row', { name: /admin@example.test/ });
  await user.selectOptions(within(row).getByRole('combobox', { name: /Role for/ }), 'member');
  await user.click(within(row).getByRole('button', { name: 'Save role' }));
  expect(await within(row).findByRole('alert')).toHaveTextContent('Keep at least one active administrator');
  expect(within(row).getByRole('cell', { name: 'Administrator' })).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/users/operator', expect.objectContaining({
    method: 'PATCH', body: '{"role":"member"}',
  }));
});

// Disabling an account previews owned workspaces and follows the controller's
// stop operation, including a provider failure after access was closed.
test('confirms offboarding and shows a failed workspace stop', async () => {
  const account = { id: 'target', primaryEmail: 'target@example.test', membershipState: 'active',
    credentialMode: 'local', role: 'member', version: 1 };
  let disabled = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['user.list', 'user.disable'] });
    if (input === '/api/v1/admin/users') return json([disabled ? { ...account, membershipState: 'disabled' } : account]);
    if (input === '/api/v1/workspaces?owner=target') return json({ items: [
      { id: 'workspace-one', observedState: 'running', currentOperationId: null },
    ], nextCursor: null });
    if (input === '/api/v1/admin/users/target' && init?.method === 'PATCH') {
      disabled = true;
      return json({ ...account, membershipState: 'disabled',
        stopOperations: [{ workspaceId: 'workspace-one', operationId: 'stop-one' }] });
    }
    if (input === '/api/v1/operations/stop-one') return json({
      id: 'stop-one', status: 'failed', version: 2, currentStep: 'stop',
      steps: [{ key: 'stop', index: 0, label: 'Stop workspace', status: 'failed', detail: 'Provider refused the stop.' }],
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Disable account' }));
  const dialog = screen.getByRole('dialog');
  expect(await within(dialog).findByText('workspace-one')).toBeVisible();
  expect(within(dialog).getByText(/files are preserved/)).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(false);
  await user.click(within(dialog).getByRole('button', { name: 'Confirm disable' }));
  expect(await within(dialog).findByText('Provider refused the stop.')).toBeVisible();
  expect(within(dialog).getByText('Sign-in sessions and tracked connections closed.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/users/target', expect.objectContaining({
    method: 'PATCH', body: '{"membershipState":"disabled"}',
    headers: expect.objectContaining({ 'If-Match': '"1"' }),
  }));
});
