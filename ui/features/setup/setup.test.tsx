import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { TextEncoder } from 'node:util';
import { Providers } from '../../app/providers';
import { SetupFlow } from './setup-flow';
import { CapacityForm } from './setup-activation';

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

// A new installation starts with no capacity caps; untouched blank inputs
// must save Unlimited for every dimension instead of blocking setup.
test('saves unlimited capacity defaults for a fresh installation', async () => {
  const onSaved = jest.fn(async () => {});
  globalThis.fetch = jest.fn(async () => response(204) as Response);
  const user = userEvent.setup();
  render(<Providers><CapacityForm csrfToken="fixture" version={0} onSaved={onSaved} /></Providers>);
  const limits = screen.getAllByPlaceholderText('Unlimited');
  expect(limits).toHaveLength(3);
  for (const input of limits) {
    expect(input).toHaveValue('');
    expect(input).not.toBeRequired();
  }
  await user.click(screen.getByRole('button', { name: 'Save capacity' }));
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    body: JSON.stringify({ cpuCount: 0, memoryMiB: 0, diskMiB: 0 }),
  }));
  expect(onSaved).toHaveBeenCalledTimes(1);
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
  await user.type(screen.getByLabelText(/^Confirm password/), 'different-long-password');
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
  expect(await screen.findByRole('heading', { name: 'Workspace capacity' })).toBeVisible();
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

// The configured local image can be checked again after a boot failure.
test('validates the configured platform image before activation', async () => {
  let attempts = 0;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/setup/status') return response(200, {
      ...bootstrap(attempts === 2 ? 'activation' : 'provider'), capacityVersion: 1, compatibleArtifact: attempts === 2,
    }) as Response;
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'csrf-fixture' }) as Response;
    if (input === '/api/v1/setup/platform' && init?.method === 'POST') {
      attempts++;
      if (attempts === 2) return response(204) as Response;
      return {
        ...response(409, { type: 'about:blank', title: 'Platform image validation failed', status: 409,
          code: 'setup.platform_validation_failed', requestId: '' }),
        headers: new Headers({ 'Content-Type': 'application/problem+json' }),
      } as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  const validate = await screen.findByRole('button', { name: 'Validate platform image' });
  await user.click(validate);
  expect(await screen.findByText('Platform image validation failed')).toBeVisible();
  await user.click(validate);
  expect(await screen.findByRole('button', { name: 'Activate installation' })).toBeDisabled();
  expect(fetch).toHaveBeenCalledWith('/api/v1/setup/platform', {
    method: 'POST', headers: { 'X-CSRF-Token': 'csrf-fixture' },
  });
});

// Capacity uses the server revision and activation waits for acknowledgement.
test('saves capacity and confirms activation before submitting', async () => {
  let stage = 'provider';
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/setup/status') return response(200, {
      ...bootstrap(stage), capacityVersion: 3, compatibleArtifact: true,
      managementOrigin: 'https://shiftbox.example.test',
    }) as Response;
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'csrf-fixture' }) as Response;
    if (input === '/api/v1/admin/capacity' && init?.method === 'PUT') {
      stage = 'activation';
      return response(204) as Response;
    }
    if (input === '/api/v1/setup/activate' && init?.method === 'POST') {
      return response(202, { status: 'succeeded' }) as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  await user.type(await screen.findByLabelText(/^vCPU limit/), '4');
  await user.type(screen.getByLabelText(/^Memory limit/), '8192');
  await user.type(screen.getByLabelText(/^Disk limit/), '32768');
  await user.click(screen.getByRole('button', { name: 'Save capacity' }));
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    headers: expect.objectContaining({ 'If-Match': '"3"', 'X-CSRF-Token': 'csrf-fixture' }),
    body: JSON.stringify({ cpuCount: 4, memoryMiB: 8192, diskMiB: 32768 }),
  }));
  const activate = await screen.findByRole('button', { name: 'Activate installation' });
  expect(activate).toBeDisabled();
  await user.click(screen.getByRole('checkbox', { name: 'I understand that I will need to sign in again' }));
  await user.click(activate);
  expect(await screen.findByRole('link', { name: 'Sign in to ShiftBox' })).toHaveAttribute('href', 'https://shiftbox.example.test/auth/login');
});

// Cloud setup validates the deployed scoped database without exposing its URI.
test('validates the deployed PostgreSQL database in cloud setup', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'csrf' }) as Response;
    if (input === '/api/v1/setup/status') return response(200, {
      ...bootstrap('data_profile'),
      cloud: {
        managementOrigin: 'https://shiftbox.dev.example.test:8443',
        ideHostPattern: '{workspace}.ide.dev.example.test',
        previewHostPattern: '{service}--{workspace}.preview.dev.example.test',
      },
    }) as Response;
    if (input === '/api/v1/setup/data-profile' && init?.method === 'PUT') return response(202) as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><SetupFlow /></Providers>);
  await userEvent.click(await screen.findByRole('button', { name: 'Validate database' }));
  expect(screen.queryByLabelText('PostgreSQL connection URI')).toBeNull();
  expect(fetch).toHaveBeenCalledWith('/api/v1/setup/data-profile', expect.objectContaining({
    body: JSON.stringify({ profile: 'postgres', tlsMode: 'verify-full' }),
  }));
});

// The private claim form rejects a mistyped confirmation and hands off directly
// to sign-in with the confirmed password.
test('claims a private installation with a confirmed password and continues to sign-in', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return response(401) as Response;
    if (input === '/api/v1/setup/status') return response(200, { ...bootstrap('administrator'), localOnly: false }) as Response;
    if (input === '/api/v1/setup/administrator' && init?.method === 'POST') return response(201, {}) as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  const create = await screen.findByRole('button', { name: 'Create administrator' });
  await user.type(screen.getByRole('textbox', { name: 'Administrator email' }), 'owner@example.test');
  await user.type(screen.getByLabelText(/^Setup claim code/), 'verified-deployment-claim');
  await user.type(screen.getByLabelText(/^New password/), 'chosen-owner-password');
  const confirmation = screen.getByLabelText(/^Confirm password/);
  await user.type(confirmation, 'mistyped-owner-password');
  await user.click(create);
  expect(await screen.findByText('Passwords do not match.')).toBeVisible();
  expect(fetch).not.toHaveBeenCalledWith('/api/v1/setup/administrator', expect.anything());
  await user.clear(confirmation);
  await user.type(confirmation, 'chosen-owner-password');
  await user.click(create);
  expect(await screen.findByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login');
  expect(screen.getByText('Sign in with the administrator email and password you just set.')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Password' })).not.toBeInTheDocument();
  expect(screen.queryByLabelText(/^Setup claim code/)).not.toBeInTheDocument();
  expect(fetch).toHaveBeenCalledWith('/api/v1/setup/administrator', expect.objectContaining({
    method: 'POST',
    body: JSON.stringify({
      email: 'owner@example.test', claimCode: 'verified-deployment-claim',
      initialPassword: 'chosen-owner-password', confirmPassword: 'chosen-owner-password',
    }),
  }));
});

// Activation keeps the operator on the same private origin while the active
// controller replaces the setup listener and retires its bootstrap API.
test('waits for the private controller before offering fresh sign-in', async () => {
  let activated = false;
  let online = false;
  globalThis.fetch = jest.fn(async (input) => {
    if (input === '/api/v1/setup/status') {
      if (activated && online) return response(404) as Response;
      return response(200, {
        ...bootstrap('activation'), localOnly: false, managementOrigin: 'https://shiftbox.fixture.internal:8443',
      }) as Response;
    }
    if (input === '/api/v1/session') return response(200, { mode: 'bootstrap_restricted', csrfToken: 'fixture' }) as Response;
    if (input === '/api/v1/setup/activate') {
      activated = true;
      return response(202, { status: 'succeeded' }) as Response;
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><SetupFlow /></Providers>);
  await user.click(await screen.findByRole('checkbox', { name: 'I understand that I will need to sign in again' }));
  await user.click(screen.getByRole('button', { name: 'Activate installation' }));
  expect(await screen.findByText('Starting ShiftBox…')).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Sign in to ShiftBox' })).not.toBeInTheDocument();
  online = true;
  const signIn = await screen.findByRole('link', { name: 'Sign in to ShiftBox' }, { timeout: 3000 });
  expect(signIn).toHaveAttribute('href', 'https://shiftbox.fixture.internal:8443/auth/login');
});
