import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { TextEncoder } from 'node:util';
import { Providers } from '../../app/providers';
import { SetupFlow } from './setup-flow';

const bootstrap = (available: string, completed: string[] = []) => ({
  state: 'configuring', localOnly: true, completedSteps: completed, availableSteps: [available],
});
const response = (status: number, body?: unknown) => ({
  ok: status >= 200 && status < 300, status, headers: new Headers(), json: async () => body,
});

const originalFetch = globalThis.fetch;
const originalEncoder = globalThis.TextEncoder;
beforeEach(() => { globalThis.TextEncoder = TextEncoder as typeof globalThis.TextEncoder; });
afterEach(() => {
  globalThis.fetch = originalFetch;
  globalThis.TextEncoder = originalEncoder;
});

// The delivered password stays visible only until the operator acknowledges it.
test('requires acknowledgement before continuing to local sign-in', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return response(401) as Response;
    if (input === '/api/v1/setup/status') return response(200, bootstrap('administrator')) as Response;
    if (input === '/api/v1/setup/administrator' && init?.method === 'POST') {
      return response(201, { temporaryPassword: 'one-time-password-from-server' }) as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  const create = await screen.findByRole('button', { name: 'Create administrator' });
  await user.click(create);
  expect(screen.getByRole('textbox', { name: 'Administrator email' })).toHaveFocus();
  await user.type(screen.getByRole('textbox', { name: 'Administrator email' }), 'owner@example.test');
  await user.click(create);
  expect(await screen.findByText('one-time-password-from-server')).toBeVisible();
  const proceed = screen.getByRole('button', { name: 'Continue to sign-in' });
  expect(proceed).toBeDisabled();
  expect(fetch).toHaveBeenCalledWith('/api/v1/setup/administrator',
    expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ 'Idempotency-Key': expect.any(String) }) }));
  await user.click(screen.getByRole('checkbox', { name: 'I saved the temporary password' }));
  await user.click(proceed);
  expect(screen.queryByText('one-time-password-from-server')).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login');
});

// A missing current password receives focus. Successful change clears the form
// and asks the operator to sign back in with the new credential.
test('changes the password with the restricted session and requires fresh sign-in', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/setup/status') return response(200, bootstrap('password', ['administrator'])) as Response;
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'csrf-fixture' }) as Response;
    if (input === '/api/v1/session/password' && init?.method === 'PUT') return response(204) as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  const save = await screen.findByRole('button', { name: 'Change password' });
  await user.click(save);
  expect(screen.getByLabelText(/^Current password/)).toHaveFocus();
  await user.type(screen.getByLabelText(/^Current password/), 'temporary-password');
  await user.type(screen.getByLabelText(/^New password/), 'different-long-password');
  await user.click(save);
  await waitFor(() => expect(screen.getByText('Sign in again with your new password.')).toBeVisible());
  expect(screen.queryByLabelText(/^New password/)).not.toBeInTheDocument();
  expect(fetch).toHaveBeenCalledWith('/api/v1/session/password',
    expect.objectContaining({ headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-fixture' }) }));
});
