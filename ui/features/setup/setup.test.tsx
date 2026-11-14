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

// Saving a profile reloads server progress and the next form sends the approved
// hostname patterns over the same restricted session.
test('saves SQLite and validates browser certificate files', async () => {
  let stage = 'data_profile';
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/setup/status') return response(200, bootstrap(stage)) as Response;
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'csrf-fixture' }) as Response;
    if (input === '/api/v1/setup/data-profile' && init?.method === 'PUT') {
      stage = 'certificate';
      return response(202, { status: 'succeeded' }) as Response;
    }
    if (input === '/api/v1/setup/certificate' && init?.method === 'PUT') {
      stage = 'provider';
      return response(202, { status: 'succeeded' }) as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Validate database' }));
  expect(fetch).toHaveBeenCalledWith('/api/v1/setup/data-profile', expect.objectContaining({ body: '{"profile":"sqlite"}' }));
  const save = await screen.findByRole('button', { name: 'Validate certificate' });
  await user.click(save);
  const origin = screen.getByLabelText(/^Management address/);
  expect(origin).toHaveFocus();
  await user.type(origin, 'https://shiftbox.example.test');
  await user.type(screen.getByLabelText(/^IDE domain suffix/), 'ide.example.test');
  await user.type(screen.getByLabelText(/^Preview domain suffix/), 'preview.example.test');
  const file = (contents: string, name: string) => Object.assign(new File([contents], name), {
    text: async () => contents,
  });
  const inputs = document.querySelectorAll<HTMLInputElement>('input[type="file"]');
  expect(inputs).toHaveLength(2);
  await user.upload(inputs[0], file('certificate-fixture', 'chain.pem'));
  await user.upload(inputs[1], file('key-fixture', 'key.pem'));
  await user.click(save);
  expect(await screen.findByText('Provider configuration is pending.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/setup/certificate', expect.objectContaining({
    headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': expect.any(String) }),
    body: JSON.stringify({
      managementOrigin: 'https://shiftbox.example.test',
      ideHostPattern: '{workspace}.ide.example.test',
      previewHostPattern: '{service}--{workspace}.preview.example.test',
      certificateChainPem: 'certificate-fixture',
      privateKeyPem: 'key-fixture',
    }),
  }));
});

// PostgreSQL preflight failures keep the stage available and an unchanged
// retry uses the same request key without putting the URI in the page copy.
test('keeps a failed PostgreSQL profile available for retry', async () => {
  const requests: RequestInit[] = [];
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/setup/status') return response(200, bootstrap('data_profile')) as Response;
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'csrf-fixture' }) as Response;
    if (input === '/api/v1/setup/data-profile' && init) {
      requests.push(init);
      return {
        ...response(409, { type: 'about:blank', title: 'PostgreSQL validation failed', status: 409, code: 'setup.postgres_validation_failed', requestId: 'fixture' }),
        headers: new Headers({ 'Content-Type': 'application/problem+json' }),
      } as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  await user.click(await screen.findByRole('radio', { name: 'PostgreSQL for production' }));
  const submit = screen.getByRole('button', { name: 'Validate database' });
  await user.click(submit);
  const uri = screen.getByLabelText(/^PostgreSQL connection URI/);
  expect(uri).toHaveFocus();
  expect(uri).toHaveAttribute('type', 'password');
  expect(requests).toHaveLength(0);
  const connectionUri = 'postgresql://owner:fixture@db.example.test/shiftbox?sslmode=verify-full';
  await user.type(uri, connectionUri);
  await user.click(submit);
  expect(await screen.findByRole('alert')).toHaveTextContent('PostgreSQL validation failed');
  await user.click(submit);
  await waitFor(() => expect(requests).toHaveLength(2));
  expect(requests[1]).toEqual(requests[0]);
  expect(requests[0].body).toBe(JSON.stringify({ profile: 'postgres', connectionUri, tlsMode: 'verify-full' }));
  expect(screen.getByRole('alert')).not.toHaveTextContent(connectionUri);
});
