import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Workspaces } from './workspaces';

const workspace = {
  id: 'workspace-one', name: 'Example workspace', ownerId: 'member', templateId: 'template-one', templateRevision: 2,
  desiredState: 'running', observedState: 'stopped', currentOperationId: 'operation-one', version: 4,
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
  jest.restoreAllMocks();
  globalThis.fetch = originalFetch;
  if (originalEventSource) Object.defineProperty(globalThis, 'EventSource', originalEventSource);
  else Reflect.deleteProperty(globalThis, 'EventSource');
});

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// Owners choose the inactivity shutdown period beside Start/Stop, with the
// server's current inactivity and retained-data guidance visible in that panel.
test('places keep-alive selection in the workspace controls', async () => {
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) : json({
    ...workspace, desiredState: 'running', observedState: 'running', currentOperationId: null,
    keepAlivePolicy: { maximumMinutes: 0, allowedMinutes: [30, 60, 120, 240, 480, 720, 0], defaultMinutes: 30, version: 1 },
    keepAliveMinutes: 30, keepAliveOverrideMinutes: null, canEditKeepAlive: true,
    inactivityMinutes: 5, inactivityShutdownAt: '2026-11-17T22:30:00Z',
  }));
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  const controls = await screen.findByRole('region', { name: 'Workspace controls' });
  expect(within(controls).getByRole('button', { name: 'Stop workspace' })).toBeVisible();
  expect(within(controls).getByRole('combobox', { name: 'Shutdown after inactivity' })).toHaveValue('Administrator default (30 minutes)');
  expect(within(controls).getByText(/Inactive for 5 minutes/)).toBeVisible();
  expect(within(controls).getByText(/Stop and start preserve project files/)).toBeVisible();
});

// Inventory rows show the observed state with Carbon colors and no input control.
test('shows workspace states as static Carbon-colored Mantine chips', async () => {
  const states = [
    ['Running', 'running', '#24a148'], ['Starting', 'starting', '#0043ce'],
    ['Unreachable', 'unreachable', '#ff832b'], ['Failed', 'failed', '#da1e28'],
    ['Stopped', 'stopped', '#6f6f6f'], ['Unknown', 'unknown', '#8a3ffc'],
  ];
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) : json({
    items: states.map(([label, observedState]) => ({ ...workspace, id: observedState, name: `${label} workspace`,
      observedState, desiredState: 'running', currentOperationId: observedState === 'starting' ? 'operation-one' : null })),
    nextCursor: null,
  }));
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const table = await screen.findByRole('table', { name: 'Workspaces' });
  for (const [label, , color] of states) {
    const row = within(table).getByRole('row', { name: new RegExp(`${label} workspace`) });
    const chip = within(row).getByText(label, { exact: true }).closest<HTMLElement>('.mantine-Chip-root');
    expect(chip?.style.getPropertyValue('--chip-bg')).toBe(color);
    expect(within(row).queryByText('Desired:')).not.toBeInTheDocument();
    expect(within(row).getByText(label, { exact: true })).toBeVisible();
    const cells = within(row).getAllByRole('cell');
    if (label === 'Starting') {
      expect(within(cells[1]).getByRole('status', { name: 'State transition in progress' })).toBeVisible();
      expect(within(cells[2]).getByText('Running')).toBeVisible();
      expect(within(cells.at(-1)!).queryByText('Operation in progress')).not.toBeInTheDocument();
    } else {
      expect(within(cells[1]).queryByRole('status')).not.toBeInTheDocument();
      expect(within(cells[2]).getByText('—')).toBeVisible();
    }
    expect(within(row).queryByRole('checkbox')).toBeNull();
  }
  expect(within(table).getByRole('columnheader', { name: 'Target State' })).toBeVisible();
});

// Workspace detail updates the read-only probe chip as the service fails and its report expires.
test('shows the latest code-server probe on workspace detail', async () => {
  let probes = [{ id: 'code-server', status: 'healthy' }];
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) :
    json({ ...workspace, observedState: 'running', currentOperationId: null, probes }));
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);

  const section = await screen.findByRole('region', { name: 'Probes' });
  expect(within(section).getByText('Code-server · Healthy')).toBeVisible();
  expect(within(section).queryByRole('checkbox')).not.toBeInTheDocument();
  probes = [{ id: 'code-server', status: 'error' }];
  await user.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await within(section).findByText('Code-server · Error')).toBeVisible();
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
  await user.selectOptions(screen.getByLabelText('Observed state'), 'stopped');
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces?owner=member&state=stopped&excludeDeleted=true', expect.anything()));
  expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled();
  await user.click(screen.getByRole('checkbox', { name: 'Show deleted workspaces' }));
  await user.selectOptions(screen.getByLabelText('Observed state'), 'deleted');
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces?owner=member&state=deleted', expect.anything()));
  await user.click(screen.getByRole('checkbox', { name: 'Show deleted workspaces' }));
  expect(screen.getByLabelText('Observed state')).toHaveValue('');
  expect(screen.queryByRole('option', { name: 'Deleted' })).not.toBeInTheDocument();
});

// A failed refresh must not present stale workspace state as current or leave
// the operator stranded without a retry control.
test('retries failed workspace lists and distinguishes filtered empty results', async () => {
  let failed = true;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (failed) throw new Error('Disconnected');
    return json({ items: [], nextCursor: null });
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  const retry = await screen.findByRole('button', { name: 'Retry' });
  expect(screen.queryByRole('table')).not.toBeInTheDocument();
  failed = false;
  await user.click(retry);
  expect(await screen.findByText('No workspaces yet.')).toBeVisible();
  await user.selectOptions(screen.getByLabelText('Observed state'), 'failed');
  expect(await screen.findByText('No workspaces match this filter.')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Clear filters' }));
  expect(screen.getByLabelText('Observed state')).toHaveValue('');
});

// Details identify the workspace by name and separate routine actions from
// previews and irreversible deletion without losing readiness gating.
test('organizes workspace controls by purpose', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    return json({ ...workspace, currentOperationId: null, observedState: 'running', canEditPreviewPorts: true });
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByRole('heading', { level: 1, name: 'Example workspace' })).toBeVisible();
  expect(within(screen.getByRole('region', { name: 'Workspace controls' })).getByRole('button', { name: 'Stop workspace' })).toBeVisible();
  expect(within(screen.getByRole('region', { name: 'Previews' })).getByLabelText('Preview ports')).toBeVisible();
  expect(within(screen.getByRole('region', { name: 'Delete workspace' })).getByRole('button', { name: 'Delete workspace' })).toBeVisible();
  expect(screen.queryByText(workspace.id, { exact: true })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Open IDE' })).not.toBeInTheDocument();
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
      ...workspace, currentOperationId: null, lastFailedOperationId: 'operation-one',
    });
    if (input === '/api/v1/operations/operation-one') return json({
      id: 'operation-one', version: 3, type: 'provision', status: 'failed', currentStep: 'configure',
      steps: [
        { key: 'boot', index: 0, label: 'Boot workspace', status: 'succeeded' },
        { key: 'configure', index: 1, label: 'Configure development container', status: 'failed',
          detail: 'The guest could not prepare the repository.' },
        { key: 'verify', index: 2, label: 'Verify IDE', status: 'pending' },
      ],
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByText('Running', { exact: true })).toBeVisible();
  expect(screen.getByText('Stopped', { exact: true })).toBeVisible();
  expect(screen.getByText('ubuntu-amd64-r5')).toBeVisible();
  expect(screen.getByText('Revision 2')).toBeVisible();
  expect(screen.getByText('Last activity').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.lastActivityAt);
  expect(screen.getByText('Retention deadline').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.retentionDeadline);
  const warning = await screen.findByRole('alert', { name: 'Workspace launch failed' });
  expect(warning).toHaveTextContent('Configure development container');
  expect(warning).toHaveTextContent('The guest could not prepare the repository.');
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
  await user.click(await screen.findByRole('button', { name: 'Stop workspace' }));
  expect(await screen.findByRole('alert', { name: 'Workspace operation failed' })).toHaveTextContent('Stop workspace workloads');
  expect(screen.getByRole('heading', { name: 'Workspace operation' })).toHaveFocus();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/stop', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(screen.getAllByText('Running', { exact: true })).toHaveLength(2);
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
  expect(screen.getByText('Stopped', { exact: true })).toBeVisible();
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
  await user.click(await screen.findByRole('radio', { name: /Example project/ }));
  await user.click(screen.getByRole('button', { name: 'Review launch' }));
  expect(screen.getByText('https://github.com/example/project')).toBeVisible();
  expect(screen.getByText('API_KEY')).toBeVisible();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
  await user.type(screen.getByRole('textbox', { name: /workspace name/i }), 'My development workspace');
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('insufficient CPU capacity');
  await user.click(screen.getByRole('button', { name: 'Back to projects' }));
  await user.click(screen.getByRole('button', { name: 'Review launch' }));
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  const submitted = fetch.mock.calls.filter(([, init]) => init?.method === 'POST');
  expect(submitted).toHaveLength(2);
  expect(submitted[0][1]?.body).toBe('{"templateId":"template-one","templateRevision":2,"name":"My development workspace"}');
  expect(submitted[1][1]?.headers).toEqual(submitted[0][1]?.headers);
});

// A failed launch explains the server's failure in a warning and keeps the
// completed and pending workflow steps available for inspection.
test('shows an inline warning and the Stepper after a workspace launch fails', async () => {
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/templates') return json({ items: [launchTemplate], nextCursor: null });
    if (input === '/api/v1/workspaces' && init?.method === 'POST') return json({ id: 'launch-op', workspaceId: workspace.id });
    if (input === '/api/v1/operations/launch-op') return json({
      id: 'launch-op', version: 2, status: 'failed', currentStep: 'configure',
      steps: [
        { key: 'boot', index: 0, label: 'Boot workspace', status: 'succeeded' },
        { key: 'configure', index: 1, label: 'Configure the development container', status: 'failed',
          detail: 'The guest could not prepare the repository with the owner’s Git authorization.',
          problem: { title: 'Repository preparation failed', detail: 'Check the GitHub connection and retry.' } },
        { key: 'verify', index: 2, label: 'Verify the browser IDE', status: 'pending' },
      ],
    });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} launch /></Providers>);
  await user.click(await screen.findByRole('radio', { name: /Example project/ }));
  await user.click(screen.getByRole('button', { name: 'Review launch' }));
  await user.type(screen.getByRole('textbox', { name: /workspace name/i }), 'My development workspace');
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  const warning = await screen.findByRole('alert', { name: 'Workspace launch failed' });
  expect(warning).toHaveTextContent('Configure the development container');
  expect(warning).toHaveTextContent('The guest could not prepare the repository with the owner’s Git authorization.');
  expect(warning).toHaveTextContent('Repository preparation failed');
  expect(warning).toHaveTextContent('Check the GitHub connection and retry.');
  expect(screen.getAllByRole('alert')).toHaveLength(1);
  expect(screen.getByRole('button', { name: 'Boot workspace: Completed' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Configure the development container: Failed' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Verify the browser IDE: Pending' })).toBeDisabled();
  expect(screen.getByRole('link', { name: 'View workspace' })).toHaveAttribute('href', '/workspaces/?id=workspace-one');
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
