import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Infrastructure } from './infrastructure';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['infrastructure.view', 'infrastructure.manage'], csrfToken: 'fixture',
};

const status = {
  provider: { name: 'utm', version: '5.0.8', architecture: 'amd64', networkModes: ['shared'] },
  capacity: { cpuCount: 4, memoryMiB: 8192, diskMiB: 131072 },
  version: 2,
  conditions: [{ key: 'browser_certificate.expiring', severity: 'warning', active: true, detail: 'Replace the browser certificate before December 14.' }],
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// A stale budget write reloads the current version without resending the
// mutation. Certificate warnings remain visible during the edit.
test('shows infrastructure warnings and refreshes after a concurrent capacity edit', async () => {
  let capacityChanged = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(capacityChanged
      ? { ...status, capacity: { ...status.capacity, cpuCount: 6 }, version: 3 } : status);
    if (input === '/api/v1/admin/capacity' && init?.method === 'PUT') {
      capacityChanged = true;
      return {
        ok: false, status: 412, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
        json: async () => ({ type: 'about:blank', title: 'Refresh the capacity budget before updating',
          code: 'request.version_mismatch', status: 412, requestId: 'ref' }),
      } as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const conditions = screen.getByRole('region', { name: 'Persistent conditions' });
  expect(await within(conditions).findByText('Replace the browser certificate before December 14.')).toBeVisible();
  expect(screen.getByText('utm · 5.0.8 · amd64')).toBeVisible();
  const cpu = screen.getByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Refresh the capacity budget before updating')).toBeVisible();
  expect(await screen.findByDisplayValue('6')).toBeVisible();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'If-Match': '"2"', 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ cpuCount: 5, memoryMiB: 8192, diskMiB: 131072 }),
  }));
});

// Direct visits by a member do not trigger a read of administrator data.
test('gates infrastructure data on the current administrator session', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'member', role: 'member' }, permissions: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session']);
});

// A lost upload response keeps the selected files and request key for retry.
// Committed progress clears the private key selection and refreshes warnings.
test('retries a certificate upload and shows the committed replacement', async () => {
  const requests: RequestInit[] = [];
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(requests.length > 1 ? { ...status, conditions: [] } : status);
    if (input === '/api/v1/admin/certificate' && init?.method === 'PUT') {
      requests.push(init);
      if (requests.length === 1) throw new TypeError('response lost');
      return json({ id: 'operation', status: 'succeeded', steps: [
        { key: 'replace_browser_certificate', index: 0, label: 'Replace browser certificate', status: 'succeeded' },
      ] });
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const { container } = render(<Providers><Infrastructure /></Providers>);
  const warning = await screen.findByText('Browser certificate expires soon');
  const submit = screen.getByRole('button', { name: 'Replace certificate' });
  await user.click(submit);
  expect(screen.getByLabelText(/^Certificate chain/)).toHaveFocus();
  expect(requests).toHaveLength(0);
  const file = (contents: string, name: string) => Object.assign(new File([contents], name), {
    text: async () => contents,
  });
  const inputs = container.querySelectorAll<HTMLInputElement>('input[type="file"]');
  await user.upload(inputs[0], file('chain fixture', 'chain.pem'));
  await user.upload(inputs[1], file('key fixture', 'private.pem'));
  await user.click(submit);
  expect(await screen.findByText('Unable to complete the request')).toBeVisible();
  expect(warning).toBeVisible();
  await user.click(submit);
  expect(await screen.findByRole('button', { name: 'Replace browser certificate: Completed' })).toBeVisible();
  await waitFor(() => expect(warning).not.toBeInTheDocument());
  expect(screen.queryByText('private.pem')).toBeNull();
  expect(requests).toHaveLength(2);
  expect(requests[0]).toEqual(requests[1]);
  expect(requests[0]).toMatchObject({
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
    body: JSON.stringify({ certificateChainPem: 'chain fixture', privateKeyPem: 'key fixture' }),
  });
});

// Administrators with infrastructure read access can see warnings without upload controls.
test('requires infrastructure management permission to replace a certificate', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['infrastructure.view'] });
    if (input === '/api/v1/admin/infrastructure') return json(status);
    throw new Error('unexpected request');
  });
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('Browser certificate expires soon')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Replace certificate' })).toBeNull();
});
