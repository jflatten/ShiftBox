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
  conditions: [
    { key: 'browser_certificate.expiring', severity: 'warning', active: true, detail: 'Replace the browser certificate before December 14.' },
    { key: 'storage.pressure', severity: 'critical', active: true, detail: 'Free space on the workspace storage volume before allocating another workspace.' },
    { key: 'storage.recovered', severity: 'info', active: false, detail: 'A previous storage warning cleared.' },
  ],
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// Loading infrastructure results must preserve one form per permitted action.
test('keeps certificate and maintenance forms distinct after status loads', async () => {
  const duplicateKeys = jest.spyOn(console, 'error').mockImplementation(() => {});
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: [...session.permissions, 'maintenance.manage'] });
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === '/api/v1/admin/maintenance') return json([]);
    if (input === '/api/v1/workspaces') return json({ items: [] });
    throw new Error('unexpected request');
  });
  try {
    const user = userEvent.setup();
    render(<Providers><Infrastructure /></Providers>);
    expect(await screen.findByRole('heading', { name: 'Provider' })).toBeVisible();
    expect(screen.getByRole('tab', { name: 'Resources' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('button', { name: 'Replace certificate' })).toBeNull();
    await user.click(screen.getByRole('tab', { name: 'Certificates' }));
    expect(screen.getAllByRole('button', { name: 'Replace certificate' })).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Schedule maintenance' })).toBeNull();
    await user.click(screen.getByRole('tab', { name: 'Maintenance' }));
    expect(screen.getAllByRole('button', { name: 'Schedule maintenance' })).toHaveLength(1);
    expect(duplicateKeys.mock.calls.flat().join(' ')).not.toContain('same key');
  } finally {
    duplicateKeys.mockRestore();
  }
});

// A stale budget write reloads the current version without resending the
// mutation. Active certificate and storage warnings remain visible during the edit.
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
  expect(screen.getByText('UTM')).toBeVisible();
  expect(screen.getByText('5.0.8')).toBeVisible();
  expect(screen.getByText('x86-64 (amd64)')).toBeVisible();
  const cpu = screen.getByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Refresh the capacity budget before updating')).toBeVisible();
  expect(await screen.findByDisplayValue('6')).toBeVisible();
  expect(within(conditions).getByText('Replace the browser certificate before December 14.')).toBeVisible();
  expect(within(conditions).getByText('Free space on the workspace storage volume before allocating another workspace.')).toBeVisible();
  expect(within(conditions).queryByText('A previous storage warning cleared.')).not.toBeInTheDocument();
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
  await user.click(screen.getByRole('tab', { name: 'Certificates' }));
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
  await user.click(screen.getByRole('tab', { name: 'Resources' }));
  await user.click(screen.getByRole('tab', { name: 'Certificates' }));
  expect(screen.getByText('private.pem')).toBeVisible();
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
  expect(screen.queryByRole('tab', { name: 'Certificates' })).toBeNull();
  expect(screen.queryByRole('tab', { name: 'Maintenance' })).toBeNull();
  expect(screen.queryByRole('tab', { name: 'Secrets' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Save capacity budget' })).toBeNull();
});

// Switching settings categories must not silently discard a capacity draft;
// Discard changes restores the saved values without issuing a mutation.
test('preserves a capacity draft across tabs and discards it explicitly', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(status);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const save = await screen.findByRole('button', { name: 'Save capacity budget' });
  expect(save).toBeDisabled();
  const cpu = screen.getByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  expect(screen.getByText('You have unsaved capacity changes.')).toBeVisible();
  await user.click(screen.getByRole('tab', { name: 'Certificates' }));
  expect(screen.getByText('Browser certificate expires soon')).toBeVisible();
  await user.click(screen.getByRole('tab', { name: 'Resources' }));
  expect(cpu).toHaveValue('5');
  expect(save).toBeEnabled();
  await user.click(screen.getByRole('button', { name: 'Discard changes' }));
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).toHaveValue('4');
  expect(save).toBeDisabled();
  expect(screen.queryByText('You have unsaved capacity changes.')).toBeNull();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session', '/api/v1/admin/infrastructure']);
});

// A failed capacity save must keep the administrator's draft and permit a
// deliberate retry. Success resets the save state only after refreshed data.
test('retains failed capacity edits and confirms a successful retry', async () => {
  let attempts = 0;
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(attempts === 2
      ? { ...status, version: 3, capacity: { ...status.capacity, cpuCount: 5 } } : status);
    if (input === '/api/v1/admin/capacity' && init?.method === 'PUT') {
      attempts++;
      return attempts === 1 ? new Response(null, { status: 503 }) : json(null);
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const cpu = await screen.findByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Unable to complete the request')).toBeVisible();
  expect(cpu).toHaveValue('5');
  expect(screen.queryByText('Capacity budget saved.')).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Capacity budget saved.')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Save capacity budget' })).toBeDisabled();
  expect(attempts).toBe(2);
});

// Operators can see both image architectures the Apple Silicon host can launch.
test('shows AMD64 and native ARM64 provider capabilities', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({
      ...status, provider: { ...status.provider, architectures: ['amd64', 'arm64'] },
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('x86-64 (amd64), ARM64')).toBeVisible();
});
