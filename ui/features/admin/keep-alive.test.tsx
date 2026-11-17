import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import { KeepAliveSettings } from './keep-alive';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
const policy = { maximumMinutes: 0, allowedMinutes: [30, 60, 120, 240, 480, 720, 0], defaultMinutes: 30, version: 3 };

// Administrator edits use the policy's own concurrency version and normalize
// duration choices to minutes while retaining an explicit Unlimited choice.
test('saves the maximum, allowed list, and selected default', async () => {
  const fetch = jest.fn(async () => ({ ok: true, status: 204 }) as Response);
  globalThis.fetch = fetch;
  const refresh = jest.fn(async () => undefined);
  const user = userEvent.setup();
  render(<MantineProvider env="test"><KeepAliveSettings policy={policy} csrfToken="csrf" refresh={refresh} /></MantineProvider>);
  const values = screen.getByRole('textbox', { name: 'Allowed keep-alive values' });
  await user.clear(values);
  await user.type(values, '15m, 30m, 1h, 2h, Unlimited');
  await user.click(screen.getByRole('combobox', { name: 'Default keep-alive' }));
  await user.click(await screen.findByRole('option', { name: '1 hour' }));
  await user.click(screen.getByRole('button', { name: 'Save keep-alive settings' }));
  expect(await screen.findByText('Keep-alive settings saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/keep-alive', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'If-Match': '"3"', 'X-CSRF-Token': 'csrf' }),
    body: JSON.stringify({ maximumMinutes: 0, allowedMinutes: [15, 30, 60, 120, 0], defaultMinutes: 60 }),
  }));
  expect(refresh).toHaveBeenCalled();
});

// A finite administrator maximum cannot leave an Unlimited choice available;
// otherwise users could bypass the intended cloud cost limit.
test('rejects choices that exceed a finite maximum before sending a mutation', async () => {
  const fetch = jest.fn();
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<MantineProvider env="test"><KeepAliveSettings policy={policy} csrfToken="csrf" refresh={async () => undefined} /></MantineProvider>);
  const maximum = screen.getByRole('textbox', { name: 'Maximum inactivity timeout' });
  await user.clear(maximum);
  await user.type(maximum, '2h');
  await user.click(screen.getByRole('button', { name: 'Save keep-alive settings' }));
  expect(await screen.findByText('Every choice must be within the maximum. Remove Unlimited when using a finite maximum.')).toBeVisible();
  expect(fetch).not.toHaveBeenCalled();
});

// Read-only infrastructure access must show the actual limit and default
// without exposing administrative save controls.
test('renders administrator keep-alive settings without write permission', () => {
  render(<MantineProvider env="test"><KeepAliveSettings policy={policy} refresh={async () => undefined} /></MantineProvider>);
  expect(screen.getByText('Maximum: Unlimited · Default: 30 minutes')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Save keep-alive settings' })).not.toBeInTheDocument();
});

// Concurrent administrator edits must refresh the policy and surface the
// controller's conflict guidance instead of claiming the draft was saved.
test('refreshes and displays a stale administrator policy failure', async () => {
  globalThis.fetch = jest.fn(async () => ({
    ok: false, status: 412, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
    json: async () => ({ type: 'about:blank', title: 'Refresh keep-alive settings before updating', status: 412,
      code: 'request.version_mismatch', requestId: 'conflict' }),
  }) as Response);
  const refresh = jest.fn(async () => undefined);
  const user = userEvent.setup();
  render(<MantineProvider env="test"><KeepAliveSettings policy={policy} csrfToken="csrf" refresh={refresh} /></MantineProvider>);
  await user.clear(screen.getByRole('textbox', { name: 'Allowed keep-alive values' }));
  await user.type(screen.getByRole('textbox', { name: 'Allowed keep-alive values' }), '30m, 1h');
  await user.click(screen.getByRole('button', { name: 'Save keep-alive settings' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Refresh keep-alive settings before updating');
  expect(refresh).toHaveBeenCalled();
  expect(screen.queryByText('Keep-alive settings saved.')).not.toBeInTheDocument();
});
