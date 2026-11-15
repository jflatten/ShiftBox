import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { People } from './people';

const originalFetch = globalThis.fetch;
beforeEach(() => {
  // jsdom has no layout; give the menu a visible anchor and viewport so
  // Mantine's detached-anchor detection does not hide an open dropdown.
  jest.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 40, width: 100, height: 40, toJSON: () => ({}),
  });
  jest.spyOn(document.documentElement, 'clientWidth', 'get').mockReturnValue(1024);
  jest.spyOn(document.documentElement, 'clientHeight', 'get').mockReturnValue(768);
});
afterEach(() => { globalThis.fetch = originalFetch; jest.restoreAllMocks(); });

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['user.list'], csrfToken: 'fixture',
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

function directory() {
  return Array.from({ length: 23 }, (_, index) => ({
    id: `person-${index}`, displayName: `Person ${String(index + 1).padStart(2, '0')}`,
    primaryEmail: `account-${index + 1}@example.test`, role: index === 22 ? 'administrator' : 'member',
    membershipState: index === 22 ? 'pending' : 'active', credentialMode: 'external', version: 1,
  })).reverse();
}

// Pagination must not discard records, and sorting must cover the full list,
// not just reorder the currently visible page.
test('sorts all accounts before paging and supports changing page size', async () => {
  globalThis.fetch = jest.fn(async path => json(path === '/api/v1/session' ? session : directory()));
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  const table = await screen.findByRole('table', { name: 'Organization membership' });
  expect(within(table).getAllByRole('row')).toHaveLength(11);
  expect(within(table).getAllByRole('row')[1]).toHaveTextContent('Person 01');
  expect(screen.getByText('Showing 1–10 of 23 users')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(screen.getByText('Showing 11–20 of 23 users')).toBeVisible();
  expect(within(table).getAllByRole('row')[1]).toHaveTextContent('Person 11');
  await user.click(screen.getByRole('button', { name: 'Sort by Account' }));
  expect(screen.getByRole('columnheader', { name: /Account/ })).toHaveAttribute('aria-sort', 'descending');
  expect(within(table).getAllByRole('row')[1]).toHaveTextContent('Person 23');
  await user.selectOptions(screen.getByRole('combobox', { name: 'Rows per page' }), '25');
  expect(within(table).getAllByRole('row')).toHaveLength(24);
  expect(screen.getByText('Showing 1–23 of 23 users')).toBeVisible();
});

// Searching on a later page must find off-page people by name or email;
// clearing a no-match search must recover the directory.
test('searches the entire directory and combines role and status filters', async () => {
  globalThis.fetch = jest.fn(async path => json(path === '/api/v1/session' ? session : directory()));
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  await screen.findByRole('table');
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  const search = screen.getByRole('searchbox', { name: 'Search users' });
  await user.type(search, ' PERSON 23 ');
  expect(screen.getByRole('row', { name: /account-23@example.test/ })).toBeVisible();
  expect(screen.getByText('Showing 1–1 of 1 user')).toBeVisible();
  await user.clear(search);
  await user.type(search, 'ACCOUNT-1@');
  expect(screen.getByRole('row', { name: /Person 01/ })).toBeVisible();
  await user.clear(search);
  await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by role' }), 'administrator');
  await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by status' }), 'active');
  expect(screen.getByText('No users match your search or filters.')).toBeVisible();
  await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by status' }), 'pending');
  expect(screen.getByRole('row', { name: /Person 23/ })).toBeVisible();
  await user.type(search, 'missing');
  expect(screen.getByText('No users match your search or filters.')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Clear filters' }));
  expect(search).toHaveValue('');
  expect(screen.getByText('Showing 1–10 of 23 users')).toBeVisible();
});

// Approving the sole account on the last filtered page must return to a
// populated page and keep the Pending filter, rather than leave a blank page.
test('keeps pagination valid when an approval removes the last matching row', async () => {
  const accounts = directory().slice(-11).map(person => ({ ...person, membershipState: 'pending' }));
  let approved = false;
  globalThis.fetch = jest.fn(async (path, init) => {
    if (path === '/api/v1/session') return json({ ...session, permissions: ['user.list', 'user.approve'] });
    if (path === '/api/v1/admin/users') return json(accounts.map(person =>
      approved && person.id === 'person-10' ? { ...person, membershipState: 'active', version: 2 } : person));
    if (path === '/api/v1/admin/users/person-10' && init?.method === 'PATCH') {
      approved = true;
      return json(null);
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  await screen.findByRole('table');
  await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by status' }), 'pending');
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  await user.click(screen.getByRole('button', { name: 'Actions for account-11@example.test' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Approve account' }));
  const dialog = screen.getByRole('dialog');
  await user.selectOptions(within(dialog).getByRole('combobox'), 'member');
  await user.click(within(dialog).getByRole('button', { name: 'Approve' }));
  expect(await screen.findByText('Showing 1–10 of 10 users')).toBeVisible();
  expect(screen.getByRole('combobox', { name: 'Filter by status' })).toHaveValue('pending');
  expect(screen.queryByRole('row', { name: /Person 11/ })).toBeNull();
});

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
  expect(screen.queryByRole('button', { name: 'Add local account' })).toBeNull();
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
  expect(screen.queryByLabelText(/^Email address/)).toBeNull();
  await user.click(await screen.findByRole('button', { name: 'Add local account' }));
  const submit = screen.getByRole('button', { name: 'Create local account' });
  await user.type(screen.getByLabelText(/^Email address/), account.primaryEmail);
  await user.click(submit);
  expect(screen.getByText('Choose a role.')).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
  await user.click(screen.getByRole('radio', { name: 'Administrator' }));
  await user.click(submit);
  expect(await screen.findByText('one-time-fixture')).toBeVisible();
  await user.keyboard('{Escape}');
  expect(screen.getByText('one-time-fixture')).toBeVisible();
  const row = await screen.findByRole('row', { name: /new@example.test/ });
  expect(within(row).getByText('Active')).toBeVisible();
  const done = screen.getByRole('button', { name: 'Done' });
  expect(done).toBeDisabled();
  await user.click(screen.getByRole('checkbox', { name: 'I saved the temporary password' }));
  await user.click(done);
  expect(screen.queryByText('one-time-fixture')).toBeNull();
  expect(screen.queryByRole('dialog')).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Add local account' }));
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
  await user.click(within(row).getByRole('button', { name: /Actions for/ }));
  await user.click(await screen.findByRole('menuitem', { name: 'Approve account' }));
  const dialog = screen.getByRole('dialog', { name: 'Approve account' });
  const role = within(dialog).getByRole('combobox', { name: /Role for/ });
  const approve = within(dialog).getByRole('button', { name: 'Approve' });
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
  await user.click(within(row).getByRole('button', { name: /Actions for/ }));
  await user.click(await screen.findByRole('menuitem', { name: 'Change role' }));
  const dialog = screen.getByRole('dialog', { name: 'Change role' });
  await user.selectOptions(within(dialog).getByRole('combobox', { name: /Role for/ }), 'member');
  await user.click(within(dialog).getByRole('button', { name: 'Save role' }));
  expect(await within(dialog).findByRole('alert')).toHaveTextContent('Keep at least one active administrator');
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
      { id: 'workspace-one', name: 'Documentation', observedState: 'running', currentOperationId: null },
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
  await user.click(await screen.findByRole('button', { name: /Actions for target/ }));
  await user.click(await screen.findByRole('menuitem', { name: 'Disable account' }));
  const dialog = screen.getByRole('dialog');
  expect(await within(dialog).findByText('Documentation')).toBeVisible();
  expect(within(dialog).getByText(/files are preserved/)).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(false);
  await user.click(within(dialog).getByRole('button', { name: 'Confirm disable' }));
  expect(await within(dialog).findByText('Provider refused the stop.')).toBeVisible();
  expect(within(dialog).getByRole('link', { name: 'Documentation' })).toHaveAttribute('href', '/workspaces/?id=workspace-one');
  expect(within(dialog).getByText('Sign-in sessions and tracked connections closed.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/users/target', expect.objectContaining({
    method: 'PATCH', body: '{"membershipState":"disabled"}',
    headers: expect.objectContaining({ 'If-Match': '"1"' }),
  }));
});

// An administrator must confirm the reset, receives the password only on
// success, and can dismiss it without storing it in the membership view.
test('confirms a local password reset and clears the temporary password', async () => {
  const account = { id: 'member', primaryEmail: 'member@example.test', membershipState: 'active',
    credentialMode: 'local', role: 'member', version: 2 };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['user.list', 'user.reset_password'] });
    if (input === '/api/v1/admin/users') return json([account, {
      ...account, id: 'external', primaryEmail: 'external@example.test', credentialMode: 'external',
    }]);
    if (input === '/api/v1/admin/users/member/password-reset' && init?.method === 'POST') {
      return json({ user: { ...account, version: 3 }, temporaryPassword: 'reset-once-fixture' });
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  const member = await screen.findByRole('row', { name: /member@example.test/ });
  const external = screen.getByRole('row', { name: /external@example.test/ });
  expect(within(external).queryByRole('button', { name: /Actions for/ })).toBeNull();
  await user.click(within(member).getByRole('button', { name: /Actions for/ }));
  await user.click(await screen.findByRole('menuitem', { name: 'Reset password' }));
  const dialog = await screen.findByRole('dialog', { name: 'Reset password' });
  expect(within(dialog).getByText(/member@example.test/)).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
  await user.click(within(dialog).getByRole('button', { name: 'Generate temporary password' }));
  expect(await within(dialog).findByText('reset-once-fixture')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/users/member/password-reset', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'If-Match': '"2"' }),
  }));
  const done = within(dialog).getByRole('button', { name: 'Done' });
  expect(done).toBeDisabled();
  await user.click(within(dialog).getByRole('checkbox', { name: 'I saved the temporary password' }));
  await user.click(done);
  expect(screen.queryByText('reset-once-fixture')).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
});
