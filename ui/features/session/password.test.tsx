import { TextEncoder } from 'node:util';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Home from '../../app/page';
import Account from '../../app/account/page';
import { PasswordForm } from './password';
import { Providers } from '../../app/providers';

const originalFetch = globalThis.fetch;
const originalTextEncoder = globalThis.TextEncoder;
beforeAll(() => { globalThis.TextEncoder = TextEncoder; });
afterAll(() => { globalThis.TextEncoder = originalTextEncoder; });
afterEach(() => { globalThis.fetch = originalFetch; });

const restricted = {
  user: { id: 'local-user', role: 'member', mustChangePassword: true },
  mode: 'password_change', installationState: 'active',
  permissions: ['session.change_password'], csrfToken: 'fixture',
};

function response(status: number, payload?: unknown) {
  return {
    status, ok: status < 400, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
    json: async () => payload,
  } as Response;
}

// The post-login landing page gates navigation, handles a rejected current
// password, and offers fresh sign-in after an accepted change.
test('requires a password change on first sign-in', async () => {
  const requests: RequestInit[] = [];
  globalThis.fetch = jest.fn(async (path, init) => {
    if (path === '/api/v1/session') return response(200, restricted);
    if (path === '/api/v1/session/password' && init?.method === 'PUT') {
      requests.push(init);
      if (requests.length === 1) return response(403, { type: 'about:blank', status: 403,
        title: 'Current password is incorrect', code: 'password.current_invalid', requestId: 'fixture' });
      return response(204);
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Home /></Providers>);
  const submit = await screen.findByRole('button', { name: 'Change password' });
  expect(screen.queryByRole('link', { name: 'Workspaces' })).toBeNull();
  await user.click(submit);
  const current = screen.getByLabelText(/^Current password/);
  expect(current).toHaveFocus();
  expect(requests).toHaveLength(0);
  await user.type(current, 'temporary-fixture');
  await user.type(screen.getByLabelText(/^New password/), 'replacement-fixture');
  await user.type(screen.getByLabelText(/^Confirm password/), 'replacement-fixture');
  await user.click(submit);
  expect(await screen.findByText('Current password is incorrect')).toBeVisible();
  await user.click(submit);
  expect(await screen.findByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login');
  expect(screen.queryByLabelText(/^New password/)).toBeNull();
  expect(requests[1]).toMatchObject({
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ currentPassword: 'temporary-fixture', newPassword: 'replacement-fixture' }),
  });
});

// Local members can open Account from normal navigation, and permissions
// determine whether Account offers the password change action.
test.each([true, false])('offers Account password changes only with permission (%s)', async allowed => {
  globalThis.fetch = jest.fn(async path => {
    if (path === '/api/v1/session') return response(200, {
      ...restricted, mode: 'normal', user: { ...restricted.user, mustChangePassword: false },
      permissions: allowed ? ['session.change_password'] : ['git.manage'],
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Account /></Providers>);
  expect(await screen.findByRole('heading', { name: 'Personal settings', level: 1 })).toBeVisible();
  await screen.findByRole('heading', { name: 'Sign-in & security' });
  expect(Boolean(screen.queryByRole('button', { name: 'Change password' }))).toBe(allowed);
});

// Missing or different confirmation must never send a password change request,
// including differences in whitespace that could otherwise be silently trimmed.
test.each(['', 'different-password', 'replacement-fixture '])('rejects unmatched password confirmation (%s)', async confirmation => {
  globalThis.fetch = jest.fn();
  const user = userEvent.setup();
  render(<Providers><PasswordForm csrfToken="fixture" onChanged={jest.fn()} /></Providers>);
  await user.type(screen.getByLabelText(/^Current password/), 'current-fixture');
  await user.type(screen.getByLabelText(/^New password/), 'replacement-fixture');
  const confirm = screen.getByLabelText(/^Confirm password/);
  if (confirmation) await user.type(confirm, confirmation);
  await user.click(screen.getByRole('button', { name: 'Change password' }));
  expect(globalThis.fetch).not.toHaveBeenCalled();
  expect(confirm).toHaveFocus();
  expect(confirm).toHaveAttribute('aria-invalid', 'true');
  expect(screen.getByText(confirmation ? 'Passwords do not match. Re-enter your new password.' : 'Re-enter your new password.')).toBeVisible();
});

// Recheck confirmation against the current new password at submission time;
// only the existing API fields are sent and all inputs clear after success.
test('revalidates confirmation after editing the new password', async () => {
  globalThis.fetch = jest.fn(async () => response(204));
  const changed = jest.fn();
  const user = userEvent.setup();
  render(<Providers><PasswordForm csrfToken="fixture" onChanged={changed} /></Providers>);
  await user.type(screen.getByLabelText(/^Current password/), 'current-fixture');
  const next = screen.getByLabelText(/^New password/);
  const confirm = screen.getByLabelText(/^Confirm password/);
  await user.type(next, 'replacement-fixture');
  await user.type(confirm, 'replacement-fixture');
  await user.type(next, '-edited');
  await user.click(screen.getByRole('button', { name: 'Change password' }));
  expect(globalThis.fetch).not.toHaveBeenCalled();
  await user.type(confirm, '-edited');
  await user.click(screen.getByRole('button', { name: 'Change password' }));
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/session/password', expect.objectContaining({
    body: JSON.stringify({ currentPassword: 'current-fixture', newPassword: 'replacement-fixture-edited' }),
  }));
  expect(changed).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText(/^Confirm password/)).toHaveValue('');
});

// Explain the specific correction without technical encoding terminology;
// multibyte passwords must still respect the server's storage limit.
test.each([
  ['short', 'Use at least 12 characters.'],
  ['current-fixture', 'Choose a password different from your current one.'],
  ['🔒'.repeat(19), 'This password is too long. Try a shorter one.'],
])('explains an invalid new password in plain language', async (password, message) => {
  globalThis.fetch = jest.fn();
  const user = userEvent.setup();
  render(<Providers><PasswordForm csrfToken="fixture" onChanged={jest.fn()} /></Providers>);
  await user.type(screen.getByLabelText(/^Current password/), 'current-fixture');
  await user.type(screen.getByLabelText(/^New password/), password);
  await user.type(screen.getByLabelText(/^Confirm password/), password);
  await user.click(screen.getByRole('button', { name: 'Change password' }));
  expect(screen.getByText(message)).toBeVisible();
  expect(globalThis.fetch).not.toHaveBeenCalled();
});
