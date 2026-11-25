import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { AdministrationSettings } from './settings';

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
  window.history.replaceState(null, '', '/');
});

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['infrastructure.view', 'infrastructure.manage', 'secret.manage_organization'],
  organizationSecretScopeId: 'organization', csrfToken: 'fixture',
};

const installedCertificate = {
  subject: 'CN=management.test', issuer: 'CN=Example CA',
  dnsNames: ['management.test', '*.ide.test', '*.preview.test'], ipAddresses: [], emailAddresses: [], uris: [],
  notBefore: '2026-11-01T12:00:00Z', notAfter: '2026-12-01T12:00:00Z', serialNumber: '1234',
  fingerprintSha256: 'ab'.repeat(32), signatureAlgorithm: 'ECDSA-SHA256', publicKeyAlgorithm: 'ECDSA',
  version: 3, chainLength: 2, status: 'expiring',
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// The number and selected unit survive a fresh page mount after saving.
test('validates, saves, and reloads the administration session timeout', async () => {
  const endpoint = '/api/v1/admin/settings/session';
  let stored = { value: 30, unit: 'minutes', version: 1 };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ conditions: [] });
    if (input === endpoint && init?.method === 'PUT') {
      stored = { ...JSON.parse(String(init.body)), version: 2 };
      return { ok: true, status: 204 } as Response;
    }
    if (input === endpoint) return json(stored);
    if (String(input).startsWith('/api/v1/secrets?')) return json([]);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const first = render(<Providers><AdministrationSettings /></Providers>);
  const main = screen.getByRole('main');
  expect(await within(main).findByRole('heading', { name: 'Settings' })).toBeVisible();
  expect(await screen.findByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings/');
  expect(within(main).getByRole('tab', { name: 'Certificates' })).toBeVisible();
  expect(within(main).getByRole('tab', { name: 'Secrets' })).toBeVisible();
  expect(within(main).queryByRole('tab', { name: 'GitHub App' })).not.toBeInTheDocument();
  const timeout = await screen.findByRole('textbox', { name: 'Session timeout' });
  const unit = screen.getByRole('combobox', { name: 'Time unit' });
  const save = screen.getByRole('button', { name: 'Save session timeout' });
  expect(timeout).toHaveValue('30');
  expect(unit).toHaveValue('minutes');
  await user.clear(timeout);
  await user.type(timeout, '0');
  await user.click(save);
  expect(screen.getByText('Enter a positive whole number.')).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);
  await user.clear(timeout);
  await user.type(timeout, '2');
  expect(within(unit).getByRole('option', { name: 'Days' })).toBeInTheDocument();
  await user.selectOptions(unit, 'hours');
  await user.click(save);
  expect(await screen.findByText('Session timeout saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith(endpoint, expect.objectContaining({
    method: 'PUT', body: JSON.stringify({ value: 2, unit: 'hours' }),
    headers: expect.objectContaining({ 'If-Match': '"1"', 'X-CSRF-Token': 'fixture' }),
  }));
  first.unmount();
  render(<Providers><AdministrationSettings /></Providers>);
  expect(await screen.findByRole('textbox', { name: 'Session timeout' })).toHaveValue('2');
  expect(screen.getByRole('combobox', { name: 'Time unit' })).toHaveValue('hours');
});

// Loading the settings URL directly as a member must never request admin data.
test('hides settings from members', async () => {
  const fetch = jest.fn(async () => json({ ...session, user: { id: 'member', role: 'member' }, permissions: [] }));
  globalThis.fetch = fetch;
  render(<Providers><AdministrationSettings /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Settings' })).toBeNull();
  expect(screen.queryByRole('tab')).toBeNull();
  expect(fetch.mock.calls).toEqual([['/api/v1/session', expect.any(Object)]]);
});

// A lost upload response keeps the selected files and request key for retry.
// Committed progress clears the private key selection and refreshes warnings.
test('retries a certificate upload and shows the committed replacement', async () => {
  const requests: RequestInit[] = [];
  const status = { conditions: [{ key: 'browser_certificate.expiring', severity: 'warning', active: true, detail: 'Replace the browser certificate soon.' }] };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(requests.length > 1 ? { ...status, conditions: [] } : status);
    if (input === '/api/v1/admin/settings/session') return json({ value: 30, unit: 'minutes', version: 1 });
    if (String(input).startsWith('/api/v1/secrets?')) return json([]);
    if (input === '/api/v1/admin/certificate' && init?.method !== 'PUT') return json(requests.length > 1
      ? { ...installedCertificate, serialNumber: '5678', notAfter: '2027-11-01T12:00:00Z', status: 'valid' }
      : installedCertificate);
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
  const { container } = render(<Providers><AdministrationSettings /></Providers>);
  const warning = await screen.findByText('Browser certificate expires soon');
  await user.click(screen.getByRole('tab', { name: 'Certificates' }));
  expect(await screen.findByText('1234')).toBeVisible();
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
  await user.click(screen.getByRole('tab', { name: 'Sessions' }));
  await user.click(screen.getByRole('tab', { name: 'Certificates' }));
  expect(screen.getByText('private.pem')).toBeVisible();
  await user.click(submit);
  expect(await screen.findByText('Unable to complete the request')).toBeVisible();
  expect(warning).toBeVisible();
  await user.click(submit);
  expect(await screen.findByRole('button', { name: 'Replace browser certificate: Completed' })).toBeVisible();
  await waitFor(() => expect(warning).not.toBeInTheDocument());
  expect(screen.queryByText('private.pem')).toBeNull();
  expect(await screen.findByText('5678')).toBeVisible();
  expect(screen.getByText('2027-11-01 12:00:00 UTC')).toBeVisible();
  expect(screen.queryByText('1234')).not.toBeInTheDocument();
  expect(requests).toHaveLength(2);
  expect(requests[0]).toEqual(requests[1]);
  expect(requests[0]).toMatchObject({
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
    body: JSON.stringify({ certificateChainPem: 'chain fixture', privateKeyPem: 'key fixture' }),
  });
});
