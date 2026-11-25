import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { components } from '../../lib/api-types';
import { Providers } from '../../app/providers';
import { Workspaces } from './workspaces';

type Lifecycle = components['schemas']['WorkspaceLifecycle'];

function lifecycle(current: Lifecycle['current'], next: Lifecycle['next'] = null, workflowOperationId: string | null = null): Lifecycle {
  return { current, next, mode: next === 'failed' ? 'failed' : next ? 'transitioning' : 'settled', workflowOperationId };
}

const workspace = {
  id: 'workspace-one', name: 'Example workspace', ownerId: 'member', templateId: 'template-one', templateRevision: 2,
  desiredState: 'running', observedState: 'stopped', currentOperationId: 'operation-one', version: 4,
  lifecycle: lifecycle('stopped'),
  platformRelease: 'ubuntu-amd64-r5', lastActivityAt: '2026-11-14T10:00:00Z', retentionDeadline: '2026-11-15T10:00:00Z',
  resources: { cpuCount: 2, memoryMiB: 4096, diskMiB: 32768 },
};
const session = {
  user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
  permissions: [], csrfToken: 'fixture',
};
const launchTemplate = {
  id: 'template-one', name: 'Example project', currentRevision: 2,
  repositoryUrl: 'https://github.com/example/project', devcontainerPath: '.devcontainer/devcontainer.json',
  resourceProfileId: 'small', platformArtifactId: 'ubuntu', secretNames: ['API_KEY'],
};
const originalFetch = globalThis.fetch;
const originalEventSource = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');

function mockMenuLayout() {
  jest.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 40, width: 100, height: 40, toJSON: () => ({}),
  });
  jest.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1024);
  jest.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(768);
}

afterEach(() => {
  jest.useRealTimers();
  jest.restoreAllMocks();
  globalThis.fetch = originalFetch;
  if (originalEventSource) Object.defineProperty(globalThis, 'EventSource', originalEventSource);
  else Reflect.deleteProperty(globalThis, 'EventSource');
});

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

function problem(status: number, code: string, title: string) {
  return { ok: false, status, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
    json: async () => ({ type: 'about:blank', status, code, title, requestId: 'fixture' }) } as Response;
}

// An open detail view advances from the last confirmed milestone even when several steps pass between polls.
test('shows one steady chip and jumps to the newest confirmed milestone on poll', async () => {
  jest.useFakeTimers();
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  let state = { ...workspace, desiredState: 'stopped', currentOperationId: null as string | null };
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === `/api/v1/workspaces/${workspace.id}`) return json(state);
    if (input === '/api/v1/operations/start') return json({ id: 'start', status: 'running', version: 1, steps: [] });
    throw new Error(`unexpected request: ${input}`);
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const controls = within(await screen.findByRole('region', { name: 'Workspace controls' }));
  expect(controls.getByText('Current state')).toBeVisible();
  expect(controls.getAllByText('Stopped')).toHaveLength(1);
  expect(controls.queryByText('Next state')).not.toBeInTheDocument();
  expect(controls.queryByRole('progressbar')).not.toBeInTheDocument();

  state = { ...state, desiredState: 'running', currentOperationId: 'start', lifecycle: lifecycle('stopped', 'vm-running', 'start') };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  const progress = await controls.findByRole('progressbar', { name: 'State transition in progress' });
  expect(progress).not.toHaveAttribute('aria-valuenow');
  expect(controls.getByText('VM-running')).toBeVisible();
  expect(controls.getByText('Stopped')).toBeVisible();

  state = { ...state, observedState: 'running', currentOperationId: null, lifecycle: lifecycle('ready', null, 'start') };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  await waitFor(() => expect(controls.queryByRole('progressbar')).not.toBeInTheDocument());
  expect(controls.getAllByText('Ready')).toHaveLength(1);
});

// Deletion keeps polling until the provider confirms it, even if the operation fails in between.
test('polls through a deletion failure and stops reporting progress once it is confirmed', async () => {
  jest.useFakeTimers();
  let state = { ...workspace, observedState: 'deleting', desiredState: 'deleted', currentOperationId: null, lifecycle: lifecycle('stopped', 'deleted') };
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === `/api/v1/workspaces/${workspace.id}`) return json(state);
    throw new Error(`unexpected request: ${input}`);
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const controls = within(await screen.findByRole('region', { name: 'Workspace controls' }));
  expect(controls.getByRole('progressbar')).toBeVisible();

  state = { ...state, observedState: 'failed', lifecycle: lifecycle('stopped', 'failed') };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(await controls.findByText('Failed')).toBeVisible();
  expect(controls.getByText('Stopped')).toBeVisible();
  expect(controls.queryByRole('progressbar')).not.toBeInTheDocument();

  state = { ...state, observedState: 'deleted', lifecycle: lifecycle('deleted') };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  await waitFor(() => expect(controls.getAllByText('Deleted')).toHaveLength(1));
  const requests = jest.mocked(globalThis.fetch).mock.calls.filter(([input]) => input === `/api/v1/workspaces/${workspace.id}`).length;
  await act(async () => { await jest.advanceTimersByTimeAsync(6000); });
  expect(jest.mocked(globalThis.fetch).mock.calls.filter(([input]) => input === `/api/v1/workspaces/${workspace.id}`)).toHaveLength(requests);
});

// Owners can find inactivity settings and keep an unsaved selection across tabs.
test('keeps workspace controls visible and preserves a keep-alive draft across tabs', async () => {
  mockMenuLayout();
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) : json({
    ...workspace, desiredState: 'running', observedState: 'running', currentOperationId: null,
    keepAlivePolicy: { maximumMinutes: 0, allowedMinutes: [30, 60, 120, 240, 480, 720, 0], defaultMinutes: 30, version: 1 },
    keepAliveMinutes: 30, keepAliveOverrideMinutes: null, canEditKeepAlive: true,
    inactivityMinutes: 5, inactivityShutdownAt: '2026-11-17T22:30:00Z',
  }));
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const controls = await screen.findByRole('region', { name: 'Workspace controls' });
  expect(within(controls).getByRole('button', { name: 'Stop workspace' })).toBeVisible();
  expect(within(controls).getByText(/Stop and start preserve project files/)).toBeVisible();
  expect(screen.queryByRole('combobox', { name: 'Shutdown after inactivity' })).not.toBeInTheDocument();
  const settings = screen.getByRole('tab', { name: 'Settings' });
  await user.click(settings);
  const panel = within(screen.getByRole('tabpanel', { name: 'Settings' }));
  const inactivity = panel.getByRole('combobox', { name: 'Shutdown after inactivity' });
  expect(inactivity).toHaveValue('Administrator default (30 minutes)');
  expect(panel.getByText(/Inactive for 5 minutes/)).toBeVisible();
  await user.click(inactivity);
  await user.click(await screen.findByRole('option', { name: '1 hour' }));
  await user.click(screen.getByRole('tab', { name: 'Overview' }));
  await user.click(settings);
  expect(inactivity).toHaveValue('1 hour');
  expect(within(controls).getByRole('button', { name: 'Stop workspace' })).toBeVisible();
});

// Each row keeps its Carbon status chip and shows the active target in the same cell.
test('shows Carbon-colored chips in a single workspace state column', async () => {
  const states = [
    ['Ready', lifecycle('ready'), '#24a148'],
    ['VM-running', lifecycle('vm-running', 'container-ready'), '#0043ce'],
    ['Unreachable', lifecycle('unreachable'), '#ff832b'],
    ['Stopped', lifecycle('stopped'), '#6f6f6f'],
    ['Unknown', lifecycle('unknown'), '#8a3ffc'],
  ] as const;
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) : json({
    items: states.map(([label, state]) => ({ ...workspace, id: state.current, name: `${label} workspace`, lifecycle: state })),
    nextCursor: null,
  }));
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const table = await screen.findByRole('table', { name: 'Workspaces' });
  for (const [label, , color] of states) {
    const row = within(table).getByRole('row', { name: new RegExp(`${label} workspace`) });
    const cells = within(row).getAllByRole('cell');
    expect(cells).toHaveLength(6);
    const state = within(cells[1]);
    const chip = state.getByText(label, { exact: true }).closest<HTMLElement>('.mantine-Chip-root');
    expect(chip?.style.getPropertyValue('--chip-bg')).toBe(color);
    if (label === 'VM-running') {
      expect(state.getByRole('progressbar', { name: 'State transition in progress' })).not.toHaveAttribute('aria-valuenow');
      expect(state.getByText('Container-ready')).toBeVisible();
      expect(state.getByText('Next state')).toBeInTheDocument();
    } else {
      expect(state.queryByRole('progressbar')).not.toBeInTheDocument();
      expect(state.queryByText('Next state')).not.toBeInTheDocument();
    }
    expect(within(row).queryByRole('checkbox')).toBeNull();
  }
  expect(within(table).getByRole('columnheader', { name: 'State' })).toBeVisible();
  expect(within(table).queryByRole('columnheader', { name: 'Target State' })).not.toBeInTheDocument();
});

// A Start requested elsewhere appears in a previously idle inventory, using only the list request.
test('polls idle inventory rows for externally started state changes', async () => {
  jest.useFakeTimers();
  let row = { ...workspace, desiredState: 'stopped', currentOperationId: null as string | null };
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces?excludeDeleted=true') return json({ items: [row], nextCursor: null });
    throw new Error(`unexpected request: ${input}`);
  });
  globalThis.fetch = fetch;
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const cell = within(await screen.findByRole('cell', { name: /Current state.*Stopped/ }));
  expect(cell.queryByRole('progressbar')).not.toBeInTheDocument();

  row = { ...row, desiredState: 'running', currentOperationId: 'start', lifecycle: lifecycle('stopped', 'vm-running', 'start') };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(await cell.findByRole('progressbar')).toBeVisible();
  expect(cell.getByText('VM-running')).toBeVisible();

  row = { ...row, observedState: 'running', currentOperationId: null, lifecycle: lifecycle('ready', null, 'start') };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  await waitFor(() => expect(cell.queryByRole('progressbar')).not.toBeInTheDocument());
  expect(cell.queryByText('Stopped')).not.toBeInTheDocument();
  expect(cell.getByText('Ready')).toBeVisible();
  expect(fetch).not.toHaveBeenCalledWith(`/api/v1/workspaces/${workspace.id}`, expect.anything());
});

// The inventory updates when code-server becomes available or loses health.
// The badge opens the same IDE grant flow from the keyboard without fetching detail.
test('shows the VS Code badge in Services and opens the IDE in a separate tab', async () => {
  const tabDocument = document.implementation.createHTMLDocument();
  const entry = tabDocument.createElement('a');
  jest.spyOn(tabDocument, 'createElement').mockReturnValue(entry);
  const navigate = jest.spyOn(entry, 'click').mockImplementation(() => {});
  const tab = { document: tabDocument, opener: window };
  const openTab = jest.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
  const exchangeUrl = 'https://workspace.ide.test/_shiftbox/entry?grant=fixture';
  let services: string[] = [];
  const fetch = jest.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces?excludeDeleted=true') {
      return json({ items: [{ ...workspace, observedState: 'running', lifecycle: lifecycle('ready'), services }], nextCursor: null });
    }
    if (input === `/api/v1/workspaces/${workspace.id}/entry-grants` && init?.method === 'POST') {
      expect(openTab).toHaveBeenCalledWith('about:blank', '_blank');
      return json({ exchangeUrl });
    }
    throw new Error(`unexpected request: ${input}`);
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const table = await screen.findByRole('table', { name: 'Workspaces' });
  const activity = within(table).getByRole('columnheader', { name: 'Last activity' });
  expect(activity.nextElementSibling).toBe(within(table).getByRole('columnheader', { name: 'Services' }));
  const servicesCell = within(within(table).getAllByRole('cell')[4]);
  const label = 'Open VS Code in a new tab';
  expect(servicesCell.queryByRole('button', { name: label })).not.toBeInTheDocument();

  services = ['code-server'];
  const refresh = screen.getByRole('button', { name: 'Refresh' });
  await user.click(refresh);
  const badge = await servicesCell.findByRole('button', { name: label });
  expect(badge.querySelector('img')).toHaveAttribute('src', '/services/vscode-icon.png');
  badge.focus();
  await user.keyboard('{Enter}');
  await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  expect(entry.href).toBe(exchangeUrl);
  expect(fetch).toHaveBeenCalledWith(`/api/v1/workspaces/${workspace.id}/entry-grants`, expect.objectContaining({
    body: '{"service":"ide"}',
  }));

  services = [];
  await user.click(refresh);
  await waitFor(() => expect(badge).not.toBeInTheDocument());
  expect(fetch).not.toHaveBeenCalledWith(`/api/v1/workspaces/${workspace.id}`, expect.anything());
});

// A helper that can reach workspace SSH enables the keyboard-accessible icon and
// exposes the exact locally configured command. An offline service removes it.
test('shows the SSH service and copies the command from its dialog', async () => {
  const user = userEvent.setup();
  const copy = jest.spyOn(navigator.clipboard, 'writeText');
  let helperOnline = true;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces?excludeDeleted=true') {
      return json({ items: [{ ...workspace, observedState: 'running', lifecycle: lifecycle('ready') }], nextCursor: null });
    }
    if (String(input) === 'http://127.0.0.1:48652/v1/ssh-alias?workspace=workspace-one&account=member') {
      return helperOnline ? json({ alias: 'shiftbox-local-project-2' }) : problem(404, 'ssh.not_available', 'Unavailable');
    }
    throw new Error(`unexpected request: ${input}`);
  });

  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const icon = await screen.findByRole('button', { name: 'Connect with SSH' });
  act(() => icon.focus());
  await user.keyboard('{Enter}');
  const modal = await screen.findByRole('dialog', { name: 'Connect with SSH' });
  expect(within(modal).getByText('ssh shiftbox-local-project-2')).toBeVisible();
  await user.click(within(modal).getByRole('button', { name: 'Copy SSH command' }));
  expect(copy).toHaveBeenCalledWith('ssh shiftbox-local-project-2');
  expect(within(modal).getByRole('status')).toHaveTextContent('Copied');
  await user.keyboard('{Escape}');
  await waitFor(() => expect(icon).toHaveFocus());

  helperOnline = false;
  await user.click(screen.getByRole('button', { name: 'Refresh' }));
  await waitFor(() => expect(icon).not.toBeInTheDocument());
});

// Workspace detail updates the read-only probe chip as the service fails and its report expires.
test('shows the latest code-server probe on workspace detail', async () => {
  let probes = [{ id: 'code-server', status: 'healthy' }];
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) :
    json({ ...workspace, observedState: 'running', currentOperationId: null, lifecycle: lifecycle('ready'), probes }));
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);

  const section = await screen.findByRole('region', { name: 'Probes' });
  expect(within(section).getByText('Code-server · Healthy')).toBeVisible();
  expect(within(section).queryByRole('checkbox')).not.toBeInTheDocument();
  probes = [{ id: 'code-server', status: 'error' }];
  await user.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await within(section).findByText('Code-server · Error')).toBeVisible();
  expect(within(screen.getByRole('region', { name: 'Workspace controls' })).getByText('Ready')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Open IDE' })).not.toBeInTheDocument();
  probes = [];
  await user.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await within(section).findByText('No recent probe results.')).toBeVisible();
});

// Opening row actions fetches the current workspace version and starts the
// same tracked workflow as the detail page without navigating away.
test('starts a stopped workspace from its inventory row', async () => {
  mockMenuLayout();
  const stopped = { ...workspace, desiredState: 'stopped', currentOperationId: null };
  const operationID = 'start-from-row';
  const fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (String(input).startsWith('/api/v1/workspaces?')) return json({ items: [stopped], nextCursor: null });
    if (input === `/api/v1/workspaces/${workspace.id}`) return json({ ...stopped, version: 9 });
    if (input === `/api/v1/workspaces/${workspace.id}/actions/start` && init?.method === 'POST') return json({ id: operationID });
    if (input === `/api/v1/operations/${operationID}`) return json({ id: operationID, status: 'succeeded', version: 1, steps: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const actions = await screen.findByRole('button', { name: 'Actions for Example workspace' });
  expect(fetch).not.toHaveBeenCalledWith(`/api/v1/workspaces/${workspace.id}`, expect.anything());
  await user.click(actions);
  const menu = await screen.findByRole('menu');
  expect(actions).toHaveAttribute('aria-expanded', 'true');
  expect(within(menu).queryByRole('menuitem', { name: 'Open IDE' })).not.toBeInTheDocument();
  expect(within(menu).getByRole('menuitem', { name: 'Open workspace' })).toHaveAttribute('href', '/workspaces/?id=workspace-one');
  await user.click(await within(menu).findByRole('menuitem', { name: 'Start' }));
  expect(fetch).toHaveBeenCalledWith(`/api/v1/workspaces/${workspace.id}/actions/start`, expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'If-Match': '"9"', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.queryByRole('region', { name: 'Workspace controls' })).not.toBeInTheDocument();
  await user.click(actions);
  await user.click(await screen.findByRole('menuitem', { name: 'View progress' }));
  expect(await within(screen.getByRole('dialog', { name: 'Workspace operation' })).findByText('Completed')).toBeVisible();
  expect(screen.getByRole('table', { name: 'Workspaces' })).toBeVisible();
});

// Ready can appear while startup hooks still run. Lifecycle actions become available
// after the controller clears the operation, even if View progress has never opened.
test('enables inventory actions after startup finishes with progress closed', async () => {
  jest.useFakeTimers();
  mockMenuLayout();
  const operationId = 'start-from-row';
  let row = { ...workspace, desiredState: 'stopped', currentOperationId: null as string | null, canOpenIde: false };
  const fetch = jest.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces?excludeDeleted=true') return json({ items: [row], nextCursor: null });
    if (input === `/api/v1/workspaces/${workspace.id}`) return json(row);
    if (input === `/api/v1/workspaces/${workspace.id}/actions/start` && init?.method === 'POST') {
      row = { ...row, desiredState: 'running', currentOperationId: operationId, version: 5,
        lifecycle: lifecycle('stopped', 'vm-running', operationId) };
      return json({ id: operationId });
    }
    if (input === `/api/v1/operations/${operationId}`) {
      return json({ id: operationId, status: row.currentOperationId ? 'running' : 'succeeded', version: 1, steps: [] });
    }
    throw new Error(`unexpected request: ${input}`);
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup({ advanceTimers: jest.advanceTimersByTime });
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const actions = await screen.findByRole('button', { name: 'Actions for Example workspace' });
  await user.click(actions);
  const start = await screen.findByRole('menuitem', { name: 'Start' });
  await act(async () => { await user.click(start); });
  expect(actions).toHaveAttribute('aria-expanded', 'false');

  row = { ...row, observedState: 'running', canOpenIde: true, lifecycle: lifecycle('ready', null, operationId) };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(await screen.findByRole('cell', { name: /Current state.*Ready/ })).toBeVisible();
  await act(async () => { await user.click(actions); });
  const lifecycleActions = ['Stop', 'Rebuild', 'Delete workspace'];
  for (const name of lifecycleActions) {
    expect(await screen.findByRole('menuitem', { name })).toBeDisabled();
  }
  expect(screen.getByRole('menuitem', { name: 'Open IDE' })).toBeEnabled();
  await act(async () => { await user.click(actions); });

  const requestCount = fetch.mock.calls.length;
  row = { ...row, currentOperationId: null, version: 6 };
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(fetch.mock.calls.slice(requestCount).some(([url]) => url === '/api/v1/workspaces?excludeDeleted=true')).toBe(true);
  await act(async () => { await user.click(actions); });
  for (const name of lifecycleActions) {
    expect(await screen.findByRole('menuitem', { name })).toBeEnabled();
  }
  expect(screen.getByRole('menuitem', { name: 'View progress' })).toBeEnabled();
  expect(screen.queryByRole('dialog', { name: 'Workspace operation' })).not.toBeInTheDocument();
});

// Running rows use detail capabilities for IDE and previews, and deletion
// still requires the existing typed name and data-loss acknowledgement.
test('offers ready actions and guarded deletion in the inventory', async () => {
  mockMenuLayout();
  const running = { ...workspace, observedState: 'running', currentOperationId: null, canOpenIde: true,
    canOpenPreviews: true, canEditPreviewPorts: true, previewPorts: [3000] };
  const fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) :
    input === `/api/v1/workspaces/${workspace.id}` ? json(running) : json({ items: [running], nextCursor: null }));
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for Example workspace' }));
  const menu = await screen.findByRole('menu');
  for (const name of ['Open IDE', 'Stop', 'Rebuild', 'Open preview 3000', 'Open workspace']) {
    expect(await within(menu).findByRole('menuitem', { name })).toBeVisible();
  }
  expect(screen.queryByLabelText('Preview ports')).not.toBeInTheDocument();
  await user.click(within(menu).getByRole('menuitem', { name: 'Delete workspace' }));
  const dialog = within(await screen.findByRole('dialog', { name: 'Delete workspace' }));
  const confirm = dialog.getByRole('button', { name: 'Delete permanently' });
  expect(confirm).toBeDisabled();
  await user.type(dialog.getByLabelText('Type the workspace name'), workspace.name);
  expect(confirm).toBeDisabled();
  await user.click(dialog.getByRole('checkbox', { name: 'I have pushed work I need to keep.' }));
  expect(confirm).toBeEnabled();
  await user.click(dialog.getByRole('button', { name: 'Cancel' }));
  expect(screen.queryByRole('dialog', { name: 'Delete workspace' })).not.toBeInTheDocument();
});

// Filters apply before server pagination, reset stale cursors, and retain an
// administrator's selected owner instead of accidentally widening the list.
test('filters workspace state and excludes deleted workspaces by default', async () => {
  const fetch = jest.fn(async (input: RequestInfo | URL) => {
    if (input === '/api/v1/session') return json(session);
    const url = new URL(String(input), 'https://management.test');
    if (url.pathname === '/api/v1/workspaces') return json({ items: [workspace], nextCursor: 'next' });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} ownerId="member" /></Providers>);
  await screen.findByRole('link', { name: 'Example workspace' });
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces?owner=member&excludeDeleted=true', expect.anything());
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  await screen.findByText('Page 2');
  await user.selectOptions(screen.getByLabelText('Current state'), 'stopped');
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces?owner=member&lifecycleState=stopped&excludeDeleted=true', expect.anything()));
  expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled();
  await user.click(screen.getByRole('checkbox', { name: 'Show deleted workspaces' }));
  await user.selectOptions(screen.getByLabelText('Current state'), 'deleted');
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces?owner=member&lifecycleState=deleted', expect.anything()));
  await user.click(screen.getByRole('checkbox', { name: 'Show deleted workspaces' }));
  expect(screen.getByLabelText('Current state')).toHaveValue('');
  expect(screen.queryByRole('option', { name: 'Deleted' })).not.toBeInTheDocument();
});

// A valid domain error still uses the existing retry and empty-result handling.
test('retries workspace list domain errors and distinguishes filtered empty results', async () => {
  let failed = true;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (failed) return problem(503, 'workspace.storage_unavailable', 'Workspace storage is unavailable');
    return json({ items: [], nextCursor: null });
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const retry = await screen.findByRole('button', { name: 'Retry' });
  expect(screen.queryByRole('dialog', { name: 'Controller unavailable' })).not.toBeInTheDocument();
  expect(screen.queryByRole('table')).not.toBeInTheDocument();
  failed = false;
  await user.click(retry);
  expect(await screen.findByText('No workspaces yet.')).toBeVisible();
  await user.selectOptions(screen.getByLabelText('Current state'), 'unreachable');
  expect(await screen.findByText('No workspaces match this filter.')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Clear filters' }));
  expect(screen.getByLabelText('Current state')).toHaveValue('');
});

// A completed launch keeps its historical metrics and lower-frequency controls in separate tabs.
test('groups workspace detail without burying status or routine actions', async () => {
  const user = userEvent.setup();
  const launch = '/api/v1/operations/original-launch';
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === launch) return json({ id: 'original-launch', status: 'succeeded', version: 1, steps: [],
      launchPerformance: { timeToIdeReadyMs: 42000, steps: [], events: [] },
    });
    return json({ ...workspace, currentOperationId: null, observedState: 'running', canEditPreviewPorts: true, launchOperationId: 'original-launch' });
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByRole('heading', { level: 1, name: 'Example workspace' })).toBeVisible();
  const controls = within(screen.getByRole('region', { name: 'Workspace controls' }));
  const tabs = within(screen.getByRole('tablist', { name: 'Workspace details' }));
  expect(tabs.getByRole('tab', { name: 'Overview', selected: true })).toBeVisible();
  const tabLabels = tabs.getAllByRole('tab').map(tab => tab.textContent);
  expect(tabLabels[tabLabels.indexOf('Performance') + 1]).toBe('Logs');
  expect(within(screen.getByRole('tabpanel', { name: 'Overview' })).getByRole('heading', { name: 'Configuration' })).toBeVisible();
  expect(screen.queryByRole('region', { name: 'Launch performance' })).not.toBeInTheDocument();
  expect(screen.queryByRole('region', { name: 'Previews' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Delete workspace' })).not.toBeInTheDocument();
  expect(globalThis.fetch).not.toHaveBeenCalledWith(launch, expect.anything());
  await user.click(tabs.getByRole('tab', { name: 'Performance' }));
  const performance = within(screen.getByRole('tabpanel', { name: 'Performance' }));
  expect(await performance.findByText('42 s')).toBeVisible();
  await user.click(tabs.getByRole('tab', { name: 'Connections' }));
  const connections = within(screen.getByRole('tabpanel', { name: 'Connections' }));
  expect(connections.getByLabelText('Preview ports')).toBeVisible();
  expect(connections.queryByRole('region', { name: 'Administrator connection grants' })).not.toBeInTheDocument();
  await user.click(tabs.getByRole('tab', { name: 'Settings' }));
  expect(within(screen.getByRole('tabpanel', { name: 'Settings' })).getByRole('button', { name: 'Delete workspace' })).toBeVisible();
  expect(controls.getByRole('button', { name: 'Stop workspace' })).toBeVisible();
  expect(screen.queryByText(workspace.id, { exact: true })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Open IDE' })).not.toBeInTheDocument();
});

// Live launch timings share the operation subscription with the overview Stepper.
test('keeps following the launch when switching from Overview to Performance', async () => {
  const subscribe = jest.fn();
  class OperationStream extends EventTarget {
    constructor(url: string) { super(); subscribe(url); }
    close() {}
  }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const id = 'active-launch';
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === `/api/v1/operations/${id}`) return json({ id, type: 'provision', version: 1, status: 'waiting', currentStep: 'configure',
      steps: [{ key: 'configure', index: 0, label: 'Configure the development container', status: 'active' }],
      launchPerformance: { steps: [], events: [] },
    });
    return json({ ...workspace, launchOperationId: id, currentOperationId: id, lifecycle: lifecycle('vm-running', 'container-ready', id) });
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByRole('button', { name: 'Configure the development container: In progress' })).toBeVisible();
  await user.click(screen.getByRole('tab', { name: 'Performance' }));
  expect(await screen.findByRole('region', { name: 'Launch performance' })).toBeVisible();
  expect(screen.queryByRole('heading', { name: 'Workspace operation' })).not.toBeInTheDocument();
  expect(subscribe).toHaveBeenCalledTimes(1);
});

// An accepted start clears the old timings before the workspace refresh
// completes. Reopening the page shows the finished timings for that start.
test('replaces previous performance as soon as the workspace starts', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const refresh = Promise.withResolvers<Response>();
  let accepted = false;
  let finished = false;
  const path = '/api/v1/workspaces/' + workspace.id;
  const snapshot = { ...workspace, desiredState: 'stopped', observedState: 'stopped',
    currentOperationId: null, launchOperationId: 'initial-launch' };
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === path + '/actions/start') {
      accepted = true;
      return json({ id: 'new-start' });
    }
    if (input === path) {
      if (finished) return json({ ...snapshot, desiredState: 'running', observedState: 'running', launchOperationId: 'new-start' });
      return accepted ? refresh.promise : json(snapshot);
    }
    if (input === '/api/v1/operations/initial-launch') return json({ id: 'initial-launch', type: 'provision',
      status: 'succeeded', version: 1, steps: [], launchPerformance: { timeToIdeReadyMs: 42000, steps: [], events: [] },
    });
    if (input === '/api/v1/operations/new-start') return json({ id: 'new-start', type: 'start', status: finished ? 'succeeded' : 'pending',
      version: 1, steps: [], launchPerformance: { steps: [], events: [], ...(finished ? { timeToIdeReadyMs: 9000 } : {}) },
    });
    throw new Error(`Unexpected request ${input}`);
  });
  const user = userEvent.setup();
  const view = render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await user.click(await screen.findByRole('tab', { name: 'Performance' }));
  expect(await screen.findByText('42 s')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Start workspace' }));
  await screen.findByRole('heading', { name: 'Workspace operation' });
  await user.click(screen.getByRole('tab', { name: 'Performance' }));
  expect(await screen.findByText('Pending verification')).toBeVisible();
  expect(screen.queryByText('42 s')).not.toBeInTheDocument();

  finished = true;
  await act(async () => refresh.resolve(json({ ...snapshot, launchOperationId: 'new-start' })));
  view.unmount();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await user.click(await screen.findByRole('tab', { name: 'Performance' }));
  expect(await screen.findByText('9 s')).toBeVisible();
  expect(screen.queryByText('42 s')).not.toBeInTheDocument();
});

// Administrators can confirm revocation by email and see the active list refresh.
test('revokes an administrator connection grant from workspace details', async () => {
  const path = '/api/v1/admin/workspaces/workspace-one/connection-grants';
  let revoked = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'admin', role: 'administrator' } });
    if (input === '/api/v1/workspaces/workspace-one') return json({ ...workspace, currentOperationId: null });
    if (input === path) return json({ items: revoked ? [] : [{ id: 'grant-one', email: 'admin@example.test' }] });
    if (input === path + '/grant-one' && init?.method === 'DELETE') {
      revoked = true;
      return json(null);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId="workspace-one" /></Providers>);
  await user.click(await screen.findByRole('tab', { name: 'Connections' }));
  await user.click(await screen.findByRole('button', { name: 'Revoke access for admin@example.test' }));
  const dialog = await screen.findByRole('dialog', { name: 'Revoke connection grant' });
  await waitFor(() => {
    expect(within(dialog).getByText(/close immediately/)).toBeVisible();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
  });
  await user.click(within(dialog).getByRole('button', { name: 'Revoke access' }));
  expect(fetch).toHaveBeenCalledWith(path + '/grant-one', expect.objectContaining({
    method: 'DELETE', headers: { 'X-CSRF-Token': 'fixture' },
  }));
  expect(await screen.findByText('No active administrator connection grants.')).toBeVisible();
});

// Before entering repair, explain what developers can safely edit and tie the
// explanation to the action so keyboard and screen-reader users receive it.
test('explains repair before starting it and renders the server workflow', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null, canStartRepair: true,
    });
    if (input === '/api/v1/workspaces/workspace-one/actions/repair' && init?.method === 'POST') return json({ id: 'repair-op' });
    if (input === '/api/v1/operations/repair-op') return json({
      id: 'repair-op', version: 2, status: 'failed', currentStep: 'verify_repair',
      steps: [{ key: 'verify_repair', index: 0, label: 'Verify the repair IDE', status: 'failed',
        detail: 'The repair IDE did not respond.' }],
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const start = await screen.findByRole('button', { name: 'Start repair' });
  expect(start).toHaveAccessibleDescription(
    'Repair opens your project files in a separate IDE and skips project hooks, tasks, and extensions.',
  );
  expect(screen.queryByRole('button', { name: 'Open repair IDE' })).not.toBeInTheDocument();
  await user.click(start);
  expect(await screen.findByRole('alert', { name: 'Workspace operation failed' })).toHaveTextContent('The repair IDE did not respond.');
  expect(screen.getByRole('button', { name: 'Verify the repair IDE: Failed' })).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/repair', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'Idempotency-Key': expect.any(String) }),
  }));
});

// Repair is only offered with the controller's readiness decision and opens
// through a grant scoped to the repair service. Denied entry closes its tab.
test('requests a repair entry grant for a verified repair workspace', async () => {
  const close = jest.fn();
  jest.spyOn(window, 'open').mockReturnValue({ close } as unknown as Window);
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null, canOpenRepair: true, canRetryAfterRepair: true,
    });
    if (input === '/api/v1/workspaces/workspace-one/actions/retry' && init?.method === 'POST') return json({ id: 'retry-op' });
    if (input === '/api/v1/operations/retry-op') return json({
      id: 'retry-op', version: 1, status: 'succeeded', currentStep: null, steps: [],
    });
    if (input === '/api/v1/workspaces/workspace-one/entry-grants' && init?.method === 'POST') return {
      ok: false, status: 403, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
      json: async () => ({ type: 'about:blank', title: 'Workspace service access denied', status: 403,
        code: 'workspace.entry_denied', requestId: 'fixture' }),
    } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await userEvent.setup().click(await screen.findByRole('button', { name: 'Open repair IDE' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Workspace service access denied');
  expect(close).toHaveBeenCalledTimes(1);
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/entry-grants', expect.objectContaining({
    method: 'POST', body: '{"service":"repair"}',
  }));
  expect(screen.queryByRole('button', { name: 'Rebuild container' })).toBeNull();
  await userEvent.setup().click(screen.getByRole('button', { name: 'Exit repair and retry setup' }));
  expect(await screen.findByText('Completed')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/retry', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'Idempotency-Key': expect.any(String) }),
  }));
});

// Workspace detail keeps requested power separate from provider observations
// and shows the operation step and retained-data guidance.
test('shows the saved workspace state and workflow', async () => {
  globalThis.fetch = jest.fn(async (input) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, currentOperationId: null, lifecycle: lifecycle('vm-running', 'failed', 'operation-one'),
    });
    if (input === '/api/v1/operations/operation-one') return json({
      id: 'operation-one', version: 3, type: 'provision', status: 'failed', currentStep: 'configure',
      steps: [
        { key: 'boot', index: 0, label: 'Boot workspace', status: 'succeeded' },
        { key: 'configure', index: 1, label: 'Configure development container', status: 'failed',
          detail: 'The guest could not prepare the repository.',
          problem: { title: 'Repository preparation failed', detail: 'Check the GitHub connection and retry.' } },
        { key: 'verify', index: 2, label: 'Verify IDE', status: 'pending' },
      ],
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByText('VM-running', { exact: true })).toBeVisible();
  expect(within(screen.getByRole('region', { name: 'Workspace controls' })).getByText('Failed')).toBeVisible();
  expect(screen.getByText('ubuntu-amd64-r5')).toBeVisible();
  expect(screen.getByText('Revision 2')).toBeVisible();
  expect(screen.getByText('Last activity').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.lastActivityAt);
  expect(screen.getByText('Retention deadline').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.retentionDeadline);
  const warning = await screen.findByRole('alert', { name: 'Workspace launch failed' });
  expect(warning).toHaveTextContent('Configure development container');
  expect(warning).toHaveTextContent('The guest could not prepare the repository.');
  expect(warning).toHaveTextContent('Repository preparation failed');
  expect(warning).toHaveTextContent('Check the GitHub connection and retry.');
  expect(screen.getAllByRole('alert')).toHaveLength(1);
  expect(screen.getByRole('button', { name: 'Boot workspace: Completed' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Configure development container: Failed' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Verify IDE: Pending' })).toBeDisabled();
  expect(screen.getByText(/Stop and start preserve project files, the developer home, and named application volumes/)).toHaveTextContent(
    'Rebuilding also preserves this data and replaces the container’s writable layer.',
  );
  expect(screen.queryByRole('button', { name: 'Start repair' })).not.toBeInTheDocument();
});

// An overdue workspace shows recovery grace and the files at risk alongside
// its original deadline, which stays visible while cleanup is paused.
test('shows retention recovery grace and data loss guidance', async () => {
  const graceUntil = '2026-11-16T10:10:00Z';
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, currentOperationId: null, retention: { state: 'grace', graceUntil },
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByText('Retention cleanup paused')).toBeVisible();
  expect(screen.getByText(/Recovery grace ends/).querySelector('time')).toHaveAttribute('datetime', graceUntil);
  expect(screen.getByText(/Management backups exclude workspace disks/)).toBeVisible();
  expect(screen.getByText('Retention deadline').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.retentionDeadline);
});

// Cursor pagination follows the server response and workspace links use a
// fixed static route with the selected ID in the query.
test('lists workspaces and goes to the next page', async () => {
  globalThis.fetch = jest.fn(async (input) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces?excludeDeleted=true') return json({ items: [workspace], nextCursor: 'cursor-one' });
    if (input === '/api/v1/workspaces?excludeDeleted=true&cursor=cursor-one') return json({ items: [], nextCursor: null });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  expect(await screen.findByRole('link', { name: 'Example workspace' })).toHaveAttribute('href', '/workspaces/?id=workspace-one');
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('No workspaces on this page.')).toBeVisible();
});

// An admitted stop uses the displayed server version and waits for its
// durable operation before changing the displayed observed state. Focus
// moves to its progress after the submitted action becomes disabled.
test('submits stop with CSRF and the workspace version', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null,
    });
    if (input === '/api/v1/workspaces/workspace-one/actions/stop' && init?.method === 'POST') return json({ id: 'stop-operation' });
    if (input === '/api/v1/operations/stop-operation') return json({
      id: 'stop-operation', version: 1, status: 'failed', currentStep: 'shutdown',
      steps: [{ key: 'shutdown', index: 0, label: 'Stop workspace workloads', status: 'failed' }],
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await user.click(await screen.findByRole('tab', { name: 'Performance' }));
  await user.click(await screen.findByRole('button', { name: 'Stop workspace' }));
  expect(await screen.findByRole('alert', { name: 'Workspace operation failed' })).toHaveTextContent('Stop workspace workloads');
  expect(screen.getByRole('heading', { name: 'Workspace operation' })).toHaveFocus();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/stop', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(within(screen.getByRole('region', { name: 'Workspace controls' })).getAllByText('Stopped')).toHaveLength(1);
});

// Permanent deletion requires the exact workspace name and explicit consent,
// then displays the provider verification step without changing observed state.
test('confirms workspace deletion and follows its durable operation', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  let deleting = false;
  const endpoint = '/api/v1/workspaces/workspace-one/actions/delete';
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, name: 'My Workspace', desiredState: deleting ? 'deleted' : 'stopped', currentOperationId: deleting ? 'delete-op' : null,
      lifecycle: deleting ? lifecycle('stopped', 'deleted', 'delete-op') : lifecycle('stopped'),
    });
    if (input === endpoint && init?.method === 'POST') {
      deleting = true;
      return json({ id: 'delete-op' });
    }
    if (input === '/api/v1/operations/delete-op') return json({
      id: 'delete-op', version: 1, status: 'waiting', currentStep: 'delete',
      steps: [{ key: 'delete', index: 0, label: 'Delete workspace and retained files', status: 'active' }],
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await user.click(await screen.findByRole('tab', { name: 'Settings' }));
  await user.click(await screen.findByRole('button', { name: 'Delete workspace' }));
  const dialog = within(await screen.findByRole('dialog', { name: 'Delete workspace' }));
  await waitFor(() => expect(dialog.getByRole('button', { name: 'Cancel' })).toHaveFocus());
  expect(dialog.getByText(/permanently delete/i)).toBeVisible();
  expect(dialog.getByText('Management backups exclude workspace VM disks.')).toBeVisible();
  const confirm = dialog.getByRole('button', { name: 'Delete permanently' });
  const typedName = dialog.getByRole('textbox', { name: /workspace name/i });
  await user.type(typedName, workspace.id);
  await user.click(dialog.getByRole('checkbox', { name: 'I have pushed work I need to keep.' }));
  expect(confirm).toBeDisabled();
  await user.click(dialog.getByRole('checkbox', { name: 'I have pushed work I need to keep.' }));
  await user.clear(typedName);
  await user.type(typedName, 'My Workspace');
  expect(confirm).toBeDisabled();
  await user.click(dialog.getByRole('checkbox', { name: 'I have pushed work I need to keep.' }));
  await user.click(confirm);
  expect(fetch).toHaveBeenCalledWith(endpoint, expect.objectContaining({
    method: 'POST', body: JSON.stringify({ confirmDataLoss: true }),
    headers: expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': 'fixture' }),
  }));
  expect(await screen.findByText('Waiting — Delete workspace and retained files')).toBeVisible();
  const controls = within(screen.getByRole('region', { name: 'Workspace controls' }));
  expect(await controls.findByText('Deleted')).toBeVisible();
  expect(controls.getByText('Stopped')).toBeVisible();
});

// A scheduled target warns its owner on the workspace page while the
// controller still has no maintenance operation for the target.
test('shows the scheduled maintenance deadline and target release', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, currentOperationId: null,
      maintenance: { releaseId: 'update-release', deadline: '2026-11-15T14:30:00Z', state: 'pending', operationId: null },
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const warning = await screen.findByRole('alert');
  expect(warning).toHaveTextContent('Scheduled maintenance');
  expect(warning).toHaveTextContent('update-release');
  expect(warning.querySelector('time')).toHaveAttribute('datetime', '2026-11-15T14:30:00Z');
});

// An owner can request the scheduled update before the deadline and follow
// the same durable maintenance steps from workspace detail.
test('requests a scheduled guest update and shows its progress', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  let accepted = false;
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, desiredState: 'stopped', currentOperationId: accepted ? 'update-op' : null,
      maintenance: { releaseId: 'update-release', deadline: '2026-11-15T14:30:00Z',
        state: accepted ? 'waiting_safe' : 'pending', operationId: accepted ? 'update-op' : null },
    });
    if (input === '/api/v1/workspaces/workspace-one/actions/update' && init?.method === 'POST') {
      accepted = true;
      return json({ id: 'update-op' });
    }
    if (input === '/api/v1/operations/update-op') return json({
      id: 'update-op', version: 1, status: 'running', currentStep: 'maintenance_boot',
      steps: [{ key: 'maintenance_boot', label: 'Start workspace management services', status: 'active' }],
    });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const button = await screen.findByRole('button', { name: 'Update now' });
  await user.click(button);
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/update', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'If-Match': '"4"', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(await screen.findByRole('button', { name: 'Start workspace management services: In progress' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Update now' })).not.toBeInTheDocument();
});

// Rebuild can be requested for a running workspace and displays the durable
// configuration step while withholding other power actions.
test('requests a rebuild and shows its progress', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null,
    });
    if (input === '/api/v1/workspaces/workspace-one/actions/rebuild' && init?.method === 'POST') return json({ id: 'rebuild-op' });
    if (input === '/api/v1/operations/rebuild-op') return json({
      id: 'rebuild-op', version: 1, status: 'running', currentStep: 'configure',
      steps: [{ key: 'configure', index: 0, label: 'Rebuild the development container', status: 'active' }],
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  class OperationStream extends EventTarget { close() {} }
  const original = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  try {
    const user = userEvent.setup();
    render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
    await user.click(await screen.findByRole('button', { name: 'Rebuild container' }));
    expect(await screen.findByText('In progress — Rebuild the development container')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Stop workspace' })).toBeDisabled();
    expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/rebuild', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
    }));
  } finally {
    if (original) Object.defineProperty(globalThis, 'EventSource', original);
    else Reflect.deleteProperty(globalThis, 'EventSource');
  }
});

// The workspace inventory's launch action opens the Projects catalog.
test('links Launch Project to Projects', async () => {
  globalThis.fetch = jest.fn(async input => json(input === '/api/v1/session' ? session : { items: [], nextCursor: null }));
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  expect(await screen.findByRole('link', { name: 'Launch Project' })).toHaveAttribute('href', '/templates/');
});

// The workspace list opened from People keeps the disabled member's scope
// across pages so the operator can return to their stop workflows.
test('keeps the owner filter when paging from People', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'admin', role: 'administrator' } });
    if (input === '/api/v1/workspaces?owner=member&excludeDeleted=true') return json({ items: [workspace], nextCursor: 'cursor-one' });
    if (input === '/api/v1/workspaces?owner=member&excludeDeleted=true&cursor=cursor-one') return json({ items: [], nextCursor: null });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} ownerId="member" /></Providers>);
  expect(await screen.findByRole('link', { name: 'Example workspace' })).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('No workspaces on this page.')).toBeVisible();
});

// Search matches names across API pages and keeps its filter when switching views.
test('filters project cards and table rows by name across the launch catalog', async () => {
  const repositoryOnly = { ...launchTemplate, id: 'other', name: 'Other project', repositoryUrl: 'https://github.com/example/groundtruth' };
  const match = { ...launchTemplate, id: 'groundtruth', name: 'Groundtruth' };
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/templates') return json({ items: [repositoryOnly], nextCursor: 'more' });
    if (input === '/api/v1/templates?cursor=more') return json({ items: [match], nextCursor: null });
    throw new Error(`unexpected request: ${input}`);
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} launch /></Providers>);
  const search = await screen.findByRole('searchbox', { name: 'Search projects' });
  await user.type(search, ' GROUND ');
  expect(await screen.findByRole('button', { name: 'Launch workspace for Groundtruth' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Launch workspace for Other project' })).not.toBeInTheDocument();

  await user.click(screen.getByRole('radio', { name: 'Table' }));
  const table = screen.getByRole('table', { name: 'Projects' });
  expect(within(table).getAllByRole('row')).toHaveLength(2);
  const review = within(table).getByRole('button', { name: 'Launch workspace for Groundtruth' });
  await user.click(review);
  const dialog = await screen.findByRole('dialog', { name: 'Review launch' });
  await waitFor(() => expect(within(dialog).getByText('Groundtruth · Revision 2')).toBeVisible());
  await user.keyboard('{Escape}');
  await waitFor(() => expect(review).toHaveFocus());
  expect(search).toHaveValue(' GROUND ');

  await user.type(search, 'missing');
  expect(screen.getByRole('status')).toHaveTextContent('No projects match your search.');
  await user.click(screen.getByRole('button', { name: 'Clear search' }));
  expect(within(screen.getByRole('table', { name: 'Projects' })).getAllByRole('row')).toHaveLength(3);
});

// A filter entered on a later table page starts from the first page of matches.
test('paginates the project table and resets its page when the search changes', async () => {
  const items = Array.from({ length: 12 }, (_, index) => ({
    ...launchTemplate, id: `project-${index + 1}`, name: `Project ${String(index + 1).padStart(2, '0')}`,
  }));
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/templates') return json({ items, nextCursor: null });
    throw new Error(`unexpected request: ${input}`);
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} launch /></Providers>);
  expect(await screen.findByRole('button', { name: 'Launch workspace for Project 12' })).toBeVisible();
  await user.click(screen.getByRole('radio', { name: 'Table' }));
  const table = screen.getByRole('table', { name: 'Projects' });
  expect(within(table).getAllByRole('row')).toHaveLength(11);
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(within(table).getAllByRole('row')).toHaveLength(3);
  expect(within(table).getByText('Project 12')).toBeVisible();
  const search = screen.getByRole('searchbox', { name: 'Search projects' });
  await user.type(search, 'Project 01');
  expect(within(table).getAllByRole('row')).toHaveLength(2);
  expect(within(table).getByText('Project 01')).toBeVisible();
  await user.clear(search);
  expect(within(table).getAllByRole('row')).toHaveLength(11);
  expect(within(table).queryByText('Project 12')).not.toBeInTheDocument();
});

// Launch requires review of the selected revision and preserves the reviewed
// request identity when capacity refusal leaves the form available for retry.
test('reviews a project revision and reports a capacity refusal', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/templates') return json({ items: [launchTemplate], nextCursor: null });
    if (input === '/api/v1/workspaces' && init?.method === 'POST') return {
      ok: false, status: 409, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
      json: async () => ({
        type: 'about:blank', title: 'There is insufficient CPU capacity',
        code: 'workspace.capacity_exceeded', status: 409, requestId: 'ref',
      }),
    } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} launch /></Providers>);
  const project = await screen.findByRole('button', { name: 'Launch workspace for Example project' });
  await user.click(project);
  const dialog = within(await screen.findByRole('dialog', { name: 'Review launch' }));
  await waitFor(() => expect(dialog.getByText('https://github.com/example/project')).toBeVisible());
  expect(dialog.getByText('API_KEY')).toBeVisible();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
  await user.type(screen.getByRole('textbox', { name: /workspace name/i }), 'My development workspace');
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('insufficient CPU capacity');
  await user.click(dialog.getByRole('button', { name: 'Cancel' }));
  await user.click(project);
  await user.click(await screen.findByRole('button', { name: 'Launch workspace' }));
  const submitted = fetch.mock.calls.filter(([, init]) => init?.method === 'POST');
  expect(submitted).toHaveLength(2);
  expect(submitted[0][1]?.body).toBe('{"templateId":"template-one","templateRevision":2,"name":"My development workspace"}');
  expect(submitted[1][1]?.headers).toEqual(submitted[0][1]?.headers);
});

// Slow grants must not lose the click's permission to open a service tab.
test.each([
  { button: 'Open IDE', service: 'ide', readiness: { canOpenIde: true } },
  { button: 'Open repair IDE', service: 'repair', readiness: { canOpenRepair: true } },
  { button: 'Open preview 4173', service: 'preview-4173', readiness: { canOpenPreviews: true, previewPorts: [4173] } },
])('opens a separate tab before waiting for the $service entry grant', async ({ button, service, readiness }) => {
  const url = `https://workspace.${service}.test/_shiftbox/entry?grant=fixture`;
  const tabDocument = document.implementation.createHTMLDocument();
  const entry = tabDocument.createElement('a');
  jest.spyOn(tabDocument, 'createElement').mockReturnValue(entry);
  const navigate = jest.spyOn(entry, 'click').mockImplementation(() => {});
  const tab = { document: tabDocument, opener: window };
  const openTab = jest.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
  let grantRequested = false;
  let openedBeforeGrant = false;
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null, ...readiness,
    });
    if (input === '/api/v1/workspaces/workspace-one/entry-grants') {
      grantRequested = init?.body === JSON.stringify({ service });
      openedBeforeGrant = openTab.mock.calls.length === 1;
      return json({ exchangeUrl: url });
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  if (service.startsWith('preview-')) await user.click(await screen.findByRole('tab', { name: 'Connections' }));
  await user.click(await screen.findByRole('button', { name: button }));
  expect(grantRequested).toBe(true);
  expect(openedBeforeGrant).toBe(true);
  await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  expect(openTab).toHaveBeenCalledWith('about:blank', '_blank');
  expect(tab.opener).toBeNull();
  expect(entry.href).toBe(url);
  expect(entry.rel).toBe('noreferrer');
});

// Access can be revoked after the button appears. Report the denial and close
// the waiting tab so the user is not left on a blank page.
test('uses the controller IDE availability and requests a fresh entry grant', async () => {
  const close = jest.fn();
  jest.spyOn(window, 'open').mockReturnValue({ close } as unknown as Window);
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null, canOpenIde: true,
    });
    if (input === '/api/v1/workspaces/workspace-one/entry-grants' && init?.method === 'POST') return {
      ok: false, status: 403, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
      json: async () => ({ type: 'about:blank', title: 'Workspace service access denied', status: 403,
        code: 'workspace.entry_denied', requestId: 'ref' }),
    } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Open IDE' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Workspace service access denied');
  expect(close).toHaveBeenCalledTimes(1);
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/entry-grants', expect.objectContaining({
    method: 'POST', body: '{"service":"ide"}', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'fixture' },
  }));
});

// If the browser blocks the new tab, explain how to retry without issuing a grant.
test('guides the user when the browser blocks the IDE tab', async () => {
  jest.spyOn(window, 'open').mockReturnValue(null);
  const fetch = jest.fn(async (input: RequestInfo | URL) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, observedState: 'running', currentOperationId: null, canOpenIde: true,
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Open IDE' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Allow pop-ups for ShiftBox');
  expect(fetch.mock.calls.map(([url]) => url)).not.toContain('/api/v1/workspaces/workspace-one/entry-grants');
});

// A recovered tag clears the warning without changing the workspace's recorded startup image.
test('keeps cached image freshness separate from running state', async () => {
  const cached = 'sha256:cached-original';
  const newer = 'sha256:newer-upstream';
  let recovered = false;
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) : json({
    ...workspace, observedState: 'running', currentOperationId: null, lifecycle: lifecycle('ready'),
    imageCache: {
      warningActive: !recovered, updateAvailable: recovered,
      references: [{
        origin: 'mcr.microsoft.com', repository: 'example/dev', tag: 'stable',
        resolvedDigest: cached, verifiedDigest: recovered ? newer : cached,
        warningActive: !recovered,
      }],
    },
  }));
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByText('The upstream registry could not be contacted. Using cached version.')).toBeVisible();
  expect(within(screen.getByRole('region', { name: 'Workspace controls' })).getAllByText('Ready')).toHaveLength(1);
  recovered = true;
  await user.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await screen.findByText('A newer upstream image is available.')).toBeVisible();
  expect(screen.queryByText('The upstream registry could not be contacted. Using cached version.')).not.toBeInTheDocument();
  expect(screen.getByText(cached)).toBeVisible();
  expect(screen.getByText(newer)).toBeVisible();
});

// Reloading an overdue workspace shows its last confirmed chip and original
// timeout. A later verified snapshot settles while preserving Stepper history.
test('reopens a timed-out workspace and preserves history after late success', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const operationId = 'late-stop';
  const timeout = { occurredAt: '2026-11-18T18:00:00Z', reason: 'The provider has yet to verify stop.' };
  let completed = false;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === `/api/v1/workspaces/${workspace.id}`) return json({
      ...workspace, desiredState: 'stopped', observedState: completed ? 'stopped' : 'running',
      currentOperationId: completed ? null : operationId,
      lifecycle: completed ? lifecycle('stopped', null, operationId) : { ...lifecycle('ready', 'stopped', operationId), mode: 'timed_out' },
    });
    if (input === `/api/v1/operations/${operationId}`) return json({
      id: operationId, status: completed ? 'succeeded' : 'waiting', version: completed ? 2 : 1,
      steps: [{ key: 'stop', index: 0, label: 'Verify provider stop', status: completed ? 'succeeded' : 'active', timeout }],
    });
    throw new Error(`unexpected request: ${input}`);
  });

  const first = render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const controls = within(await screen.findByRole('region', { name: 'Workspace controls' }));
  expect(controls.getByText('Ready')).toBeVisible();
  expect(controls.getByText('Timed out · Still checking')).toBeVisible();
  expect(controls.queryByRole('progressbar')).not.toBeInTheDocument();
  expect(await screen.findByRole('button', { name: 'Verify provider stop: Timed out · Still checking' })).toBeVisible();
  first.unmount();

  completed = true;
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const recovered = within(await screen.findByRole('region', { name: 'Workspace controls' }));
  expect(recovered.getByText('Stopped')).toBeVisible();
  expect(recovered.queryByText('Failed')).not.toBeInTheDocument();
  expect(await screen.findByRole('button', { name: 'Verify provider stop: Completed after timeout' })).toBeVisible();
  expect(screen.getByText(timeout.reason)).toBeVisible();
});

// A network interruption and a controller failure share one notice. The
// rendered connector and chips stay in place until a newer snapshot arrives.
test('freezes visible lifecycle progress through controller unavailability', async () => {
  jest.useFakeTimers();
  let outcome = 'transition';
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input !== '/api/v1/workspaces?excludeDeleted=true') throw new Error(`unexpected ${input}`);
    if (outcome === 'network') throw new TypeError('Network connection lost');
    if (outcome === 'controller') return problem(500, 'internal.error', 'Unable to complete the request');
    return json({ items: [{ ...workspace, lifecycle: outcome === 'ready'
      ? lifecycle('ready') : lifecycle('stopped', 'vm-running') }], nextCursor: null });
  });
  render(<Providers><Workspaces workspaceId={null} /></Providers>);

  const connector = await screen.findByRole('progressbar', { name: 'State transition in progress' });
  const refresh = screen.getByRole('button', { name: 'Refresh' });
  refresh.focus();
  outcome = 'network';
  fireEvent.click(refresh);
  const dialog = await screen.findByRole('dialog', { name: 'Controller unavailable' });
  expect(dialog).toBeVisible();
  expect(connector).toBeVisible();
  expect(connector.querySelector('[data-paused]')).not.toBeNull();
  const state = within(connector.parentElement!);
  expect(state.getByText('Stopped')).toBeVisible();
  expect(state.getByText('VM-running')).toBeVisible();

  outcome = 'controller';
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(screen.getAllByRole('dialog', { name: 'Controller unavailable' })).toEqual([dialog]);
  expect(connector).toBeVisible();

  outcome = 'ready';
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  await waitFor(() => expect(dialog).not.toBeInTheDocument());
  expect(within(screen.getByRole('row', { name: /Example workspace/ })).getByText('Ready')).toBeVisible();
  expect(connector).not.toBeInTheDocument();
  await waitFor(() => expect(refresh).toHaveFocus());
});

// The first failed session read has to recover even before the workspace view
// mounts. A genuine denial then dismisses the notice and exposes no old data.
test('recovers from an initial session outage and handles a real workspace denial', async () => {
  jest.useFakeTimers();
  let connected = false;
  let denied = false;
  globalThis.fetch = jest.fn(async input => {
    if (!connected) throw new TypeError('Network connection lost');
    if (input === '/api/v1/session') return json(session);
    if (denied) return problem(403, 'auth.forbidden', 'Workspace access denied');
    return json({ items: [workspace], nextCursor: null });
  });
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  expect(await screen.findByRole('dialog', { name: 'Controller unavailable' })).toBeVisible();
  expect(screen.queryByText(workspace.name)).not.toBeInTheDocument();

  connected = true;
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(await screen.findByRole('link', { name: workspace.name })).toBeVisible();
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

  connected = false;
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(await screen.findByRole('dialog', { name: 'Controller unavailable' })).toBeVisible();
  connected = true;
  denied = true;
  await act(async () => { await jest.advanceTimersByTimeAsync(3000); });
  expect(await screen.findByText('Workspace access denied')).toBeVisible();
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.queryByText(workspace.name)).not.toBeInTheDocument();
});

// An active deployment can switch to the stop workflow before or after the
// deadline, and a reload follows the accepted stop operation.
test.each(['transitioning', 'timed_out'])('stops an active deployment in the %s state', async mode => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const launchId = 'stalled-launch';
  const stopId = 'deployment-stop';
  let stopping = false;
  let version = workspace.version;
  const fetcher = jest.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (input === '/api/v1/session') return json(session);
    if (input === `/api/v1/workspaces/${workspace.id}`) return json({
      ...workspace, version, desiredState: stopping ? 'stopped' : 'running', observedState: 'starting',
      currentOperationId: stopping ? stopId : launchId, canInterruptDeployment: !stopping,
      lifecycle: stopping ? lifecycle('provisioned', 'stopped', stopId) : { ...lifecycle('provisioned', 'vm-running', launchId), mode },
    });
    if (input === `/api/v1/workspaces/${workspace.id}/actions/stop` && init?.method === 'POST') {
      if (mode === 'transitioning' && version === workspace.version) {
        version++;
        return problem(412, 'request.version_mismatch', 'Refresh before making this change');
      }
      stopping = true;
      return json({ id: stopId });
    }
    if (input === `/api/v1/operations/${launchId}`) return json({
      id: launchId, version: 1, status: 'waiting', currentStep: 'enroll',
      steps: [{ key: 'enroll', index: 0, label: 'Enroll the guest', status: 'active' }],
    });
    if (input === `/api/v1/operations/${stopId}`) return json({
      id: stopId, version: 1, status: 'running', currentStep: 'stop',
      steps: [{ key: 'stop', index: 0, label: 'Stop workspace', status: 'active' }],
    });
    throw new Error(`unexpected request: ${input}`);
  });
  globalThis.fetch = fetcher;
  const first = render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  await userEvent.setup().click(await screen.findByRole('button', { name: 'Stop deployment' }));
  expect(await screen.findByRole('button', { name: 'Stop workspace: In progress' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Stop deployment' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Enroll the guest: In progress' })).not.toBeInTheDocument();
  if (mode === 'transitioning') {
    const requests = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST').map(([, init]) => new Headers(init?.headers));
    expect(requests.map(headers => headers.get('If-Match'))).toEqual(['"4"', '"5"']);
    expect(requests[1].get('Idempotency-Key')).toBe(requests[0].get('Idempotency-Key'));
  }
  first.unmount();

  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByRole('button', { name: 'Stop workspace: In progress' })).toBeVisible();
});

// The inventory action skips the busy deployment gate and opens the new workflow.
test('switches inventory progress to shutdown when a deployment is stopped', async () => {
  mockMenuLayout();
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const launchId = 'inventory-launch';
  const stopId = 'inventory-stop';
  let current = { ...workspace, observedState: 'starting', currentOperationId: launchId,
    canInterruptDeployment: true, lifecycle: lifecycle('provisioned', 'vm-running', launchId) };
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces?excludeDeleted=true') return json({ items: [current], nextCursor: null });
    if (input === `/api/v1/workspaces/${workspace.id}`) return json(current);
    if (input === `/api/v1/workspaces/${workspace.id}/actions/stop` && init?.method === 'POST') {
      current = { ...current, desiredState: 'stopped', currentOperationId: stopId, canInterruptDeployment: false,
        lifecycle: lifecycle('provisioned', 'stopped', stopId) };
      return json({ id: stopId });
    }
    if (input === `/api/v1/operations/${stopId}`) return json({
      id: stopId, version: 1, status: 'waiting', currentStep: 'stop',
      steps: [{ key: 'stop', index: 0, label: 'Stop workspace', status: 'active' }],
    });
    throw new Error(`unexpected request: ${input}`);
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for Example workspace' }));
  const stop = await screen.findByRole('menuitem', { name: 'Stop deployment' });
  expect(stop).toBeEnabled();
  expect(screen.getByRole('menuitem', { name: 'Delete workspace' })).toBeDisabled();
  await user.click(stop);
  const workflow = within(await screen.findByRole('dialog', { name: 'Workspace operation' }));
  expect(await workflow.findByRole('button', { name: 'Stop workspace: In progress' })).toBeVisible();
});
