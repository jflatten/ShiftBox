import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Maintenance } from './maintenance';

const originalFetch = globalThis.fetch;
const originalEventSource = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
afterEach(() => {
  globalThis.fetch = originalFetch;
  if (originalEventSource) Object.defineProperty(globalThis, 'EventSource', originalEventSource);
  else Reflect.deleteProperty(globalThis, 'EventSource');
});

// An administrator supplies a release and an explicit local deadline. The
// accepted operation is presented using its server-defined steps.
test('schedules a campaign and presents its controller operation', async () => {
  const deadline = '2099-01-01T12:30';
  const campaign = { id: 'fixture', releaseId: 'release-two', deadline: new Date(deadline).toISOString(), state: 'scheduled', operationId: 'fixture' };
  let scheduled = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/admin/maintenance' && init?.method === 'POST') {
      scheduled = true;
      return { ok: true, json: async () => ({ id: campaign.id }) } as Response;
    }
    if (input === '/api/v1/admin/maintenance') return { ok: true, json: async () => scheduled ? [campaign] : [] } as Response;
    if (input === '/api/v1/operations/fixture') return { ok: true, json: async () => ({
      id: campaign.id, status: 'succeeded', version: 2,
      steps: [{ key: 'update_guests', index: 0, label: 'Update retained workspaces', status: 'succeeded' }],
    }) } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Maintenance csrfToken="fixture" /></Providers>);
  const submit = screen.getByRole('button', { name: 'Schedule maintenance' });
  await user.click(submit);
  const release = screen.getByLabelText(/target release/i);
  expect(release).toHaveFocus();
  await user.type(release, campaign.releaseId);
  fireEvent.change(screen.getByLabelText(/deadline/i), { target: { value: deadline } });
  await user.click(submit);
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/maintenance', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ releaseId: campaign.releaseId, deadline: campaign.deadline }),
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(await screen.findByRole('button', { name: 'Update retained workspaces: Completed' })).toBeVisible();
});

// Keyboard users can reopen a blocked campaign and inspect reached steps
// without being able to advance a pending step or force an unsafe shutdown.
test('keeps a blocked maintenance workflow inspectable by keyboard', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const campaign = { id: 'campaign', releaseId: 'release-two', deadline: '2099-01-01T12:30:00Z', state: 'blocked', operationId: 'operation' };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/admin/maintenance' && init?.method !== 'POST') {
      return { ok: true, json: async () => [campaign] } as Response;
    }
    if (input === '/api/v1/operations/operation') return { ok: true, json: async () => ({
      id: campaign.operationId, status: 'waiting', version: 4, currentStep: 'safe_boundary',
      steps: [
        { key: 'warn', index: 0, label: 'Warn workspace owners', status: 'succeeded', detail: 'Owners have been notified.' },
        { key: 'safe_boundary', index: 1, label: 'Wait for safe activation', status: 'active', detail: 'A workspace is still running.' },
        { key: 'verify', index: 2, label: 'Verify guest update', status: 'pending' },
      ],
    }) } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Maintenance csrfToken="fixture" /></Providers>);
  const view = await screen.findByRole('button', { name: 'View progress' });
  await user.tab();
  expect(screen.getByRole('textbox', { name: /target release/i })).toHaveFocus();
  await user.tab();
  await user.tab();
  await user.tab();
  expect(view).toHaveFocus();
  await user.keyboard('{Enter}');
  expect(await screen.findByText('Waiting — Wait for safe activation')).toHaveAttribute('aria-live', 'polite');
  expect(screen.getByRole('button', { name: 'Wait for safe activation: In progress' })).toHaveAttribute('aria-current', 'step');
  expect(screen.getByRole('button', { name: 'Verify guest update: Pending' })).toBeDisabled();
  expect(screen.queryByRole('button', { name: /force/i })).not.toBeInTheDocument();
  await user.tab();
  expect(screen.getByRole('button', { name: 'Warn workspace owners: Completed' })).toHaveFocus();
  await user.keyboard('{Enter}');
  expect(screen.getByText('Owners have been notified.')).toBeVisible();
  expect(screen.getByText('A workspace is still running.')).toBeVisible();
  expect(fetch.mock.calls.every(([, init]) => init?.method !== 'POST')).toBe(true);
});
