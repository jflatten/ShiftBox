import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { WorkspaceKeepAlive } from './keep-alive';
import type { Workspace } from './workspace';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
const policy = { maximumMinutes: 0, allowedMinutes: [30, 60, 120, 240, 480, 720, 0], defaultMinutes: 30, version: 3 };
const workspace = {
  id: 'workspace', version: 7, desiredState: 'running', observedState: 'running',
  keepAlivePolicy: policy, keepAliveMinutes: 30, keepAliveOverrideMinutes: null,
  canEditKeepAlive: true, inactivityMinutes: 5, inactivityShutdownAt: '2026-11-17T22:30:00Z',
} as Workspace;
function view(value = workspace, refresh = jest.fn(async () => undefined)) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><MantineProvider env="test">
    <WorkspaceKeepAlive workspace={value} csrfToken="csrf" disabled={false} refresh={refresh} />
  </MantineProvider></QueryClientProvider>);
  return { client, refresh };
}

// A finite timeout tells the owner which SSH actions renew the workspace clock.
test('explains activity based SSH keep-alive', () => {
  view();
  expect(screen.getByText(/SSH commands and client input/)).toBeVisible();
  expect(screen.getByText(/Idle SSH connections count toward inactivity/)).toBeVisible();
});

// A workspace owner can choose an administrator-offered Unlimited value with
// the workspace version, without converting it into inheritance or a zero timeout.
test('saves a workspace choice including Unlimited', async () => {
  const fetch = jest.fn(async () => ({ ok: true, status: 200 }) as Response);
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const { refresh } = view();
  await user.click(screen.getByRole('combobox', { name: 'Shutdown after inactivity' }));
  await user.click(await screen.findByRole('option', { name: 'Unlimited' }));
  await user.click(screen.getByRole('button', { name: 'Save keep-alive' }));
  expect(await screen.findByText('Keep-alive saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace', expect.objectContaining({
    method: 'PATCH', headers: expect.objectContaining({ 'If-Match': '"7"' }), body: JSON.stringify({ keepAliveMinutes: 0 }),
  }));
  expect(refresh).toHaveBeenCalled();
});

// Returning to the organization default must send null rather than copying the
// current default, so later administrator edits still apply to this workspace.
test('can return an explicit workspace choice to the administrator default', async () => {
  const fetch = jest.fn(async () => ({ ok: true, status: 200 }) as Response);
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  view({ ...workspace, keepAliveMinutes: 120, keepAliveOverrideMinutes: 120 });
  await user.click(screen.getByRole('combobox', { name: 'Shutdown after inactivity' }));
  await user.click(await screen.findByRole('option', { name: 'Administrator default (30 minutes)' }));
  await user.click(screen.getByRole('button', { name: 'Save keep-alive' }));
  expect(await screen.findByText('Keep-alive saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace', expect.objectContaining({ body: JSON.stringify({ keepAliveMinutes: null }) }));
});

// Users without policy permission still need the cost behavior explained, and
// Unlimited must clearly state that automatic inactivity shutdown is disabled.
test('shows a read-only Unlimited selection without a deadline', () => {
  view({ ...workspace, canEditKeepAlive: false, keepAliveMinutes: 0, keepAliveOverrideMinutes: 0, inactivityShutdownAt: null });
  expect(screen.getByText('Unlimited — automatic inactivity shutdown is disabled.')).toBeVisible();
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Save keep-alive' })).not.toBeInTheDocument();
});

// A concurrent lifecycle or policy change must refresh the workspace and show
// a retryable decision to the owner without reporting an unsaved draft as saved.
test('refreshes and shows a stale workspace policy failure', async () => {
  globalThis.fetch = jest.fn(async () => ({
    ok: false, status: 412, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
    json: async () => ({ type: 'about:blank', title: 'Refresh before changing keep-alive', status: 412,
      code: 'request.version_mismatch', requestId: 'conflict' }),
  }) as Response);
  const user = userEvent.setup();
  const { refresh } = view();
  await user.click(screen.getByRole('combobox', { name: 'Shutdown after inactivity' }));
  await user.click(await screen.findByRole('option', { name: '1 hour' }));
  await user.click(screen.getByRole('button', { name: 'Save keep-alive' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Refresh before changing keep-alive');
  expect(refresh).toHaveBeenCalled();
  expect(screen.queryByText('Keep-alive saved.')).not.toBeInTheDocument();
});
