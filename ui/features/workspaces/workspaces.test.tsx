import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Workspaces } from './workspaces';

const workspace = {
  id: 'workspace-one', ownerId: 'member', templateId: 'template-one', templateRevision: 2,
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

afterEach(() => {
  globalThis.fetch = originalFetch;
  if (originalEventSource) Object.defineProperty(globalThis, 'EventSource', originalEventSource);
  else Reflect.deleteProperty(globalThis, 'EventSource');
});

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

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
  expect(await screen.findByText('Failed — Verify the repair IDE')).toBeVisible();
  expect(screen.getByText('The repair IDE did not respond.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/repair', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'Idempotency-Key': expect.any(String) }),
  }));
});

// Repair is only offered with the controller's readiness decision and opens
// through a grant scoped to the repair service.
test('requests a repair entry grant for a verified repair workspace', async () => {
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
      id: 'operation-one', version: 3, status: 'failed', currentStep: 'start',
      steps: [{ key: 'start', index: 0, label: 'Start workspace', status: 'failed',
        detail: 'The guest could not prepare the repository.' }],
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByText('Running', { exact: true })).toBeVisible();
  expect(screen.getByText('Stopped', { exact: true })).toBeVisible();
  expect(screen.getByText('ubuntu-amd64-r5')).toBeVisible();
  expect(screen.getByText('template-one · Revision 2')).toBeVisible();
  expect(screen.getByText('Last activity').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.lastActivityAt);
  expect(screen.getByText('Retention deadline').nextElementSibling?.firstElementChild).toHaveAttribute('datetime', workspace.retentionDeadline);
  expect(await screen.findByText('Failed — Start workspace')).toBeVisible();
  expect(screen.getByText('The guest could not prepare the repository.')).toBeVisible();
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
    if (input === '/api/v1/workspaces') return json({ items: [workspace], nextCursor: 'cursor-one' });
    if (input === '/api/v1/workspaces?cursor=cursor-one') return json({ items: [], nextCursor: null });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} /></Providers>);
  expect(await screen.findByRole('link', { name: 'workspace-one' })).toHaveAttribute('href', '/workspaces/?id=workspace-one');
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('No workspaces on this page.')).toBeVisible();
});

// An admitted stop uses the displayed server version and waits for its
// durable operation before changing the displayed observed state.
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
  expect(await screen.findByText('Failed — Stop workspace workloads')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/actions/stop', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(screen.getAllByText('Running', { exact: true })).toHaveLength(2);
});

// Permanent deletion requires the exact workspace ID and explicit consent,
// then displays the provider verification step without changing observed state.
test('confirms workspace deletion and follows its durable operation', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  let deleting = false;
  const endpoint = '/api/v1/workspaces/workspace-one/actions/delete';
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json({
      ...workspace, desiredState: deleting ? 'deleted' : 'stopped', currentOperationId: deleting ? 'delete-op' : null,
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
  const confirm = dialog.getByRole('button', { name: 'Delete permanently' });
  const typedID = dialog.getByRole('textbox', { name: /workspace id/i });
  await user.type(typedID, workspace.id.toUpperCase());
  await user.click(dialog.getByRole('checkbox', { name: /permanent/i }));
  expect(confirm).toBeDisabled();
  await user.click(dialog.getByRole('checkbox', { name: /permanent/i }));
  await user.clear(typedID);
  await user.type(typedID, workspace.id);
  expect(confirm).toBeDisabled();
  await user.click(dialog.getByRole('checkbox', { name: /permanent/i }));
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
    if (input === '/api/v1/workspaces?owner=member') return json({ items: [workspace], nextCursor: 'cursor-one' });
    if (input === '/api/v1/workspaces?owner=member&cursor=cursor-one') return json({ items: [], nextCursor: null });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} ownerId="member" /></Providers>);
  expect(await screen.findByRole('link', { name: 'workspace-one' })).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('No workspaces on this page.')).toBeVisible();
});

// Launch requires review of the selected revision and preserves the reviewed
// request identity when capacity refusal leaves the form available for retry.
test('reviews a template revision and reports a capacity refusal', async () => {
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
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('insufficient CPU capacity');
  await user.click(screen.getByRole('button', { name: 'Back to templates' }));
  await user.click(screen.getByRole('button', { name: 'Review launch' }));
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  const submitted = fetch.mock.calls.filter(([, init]) => init?.method === 'POST');
  expect(submitted).toHaveLength(2);
  expect(submitted[0][1]?.body).toBe('{"templateId":"template-one","templateRevision":2}');
  expect(submitted[1][1]?.headers).toEqual(submitted[0][1]?.headers);
});

// Acceptance gives the browser a workspace link and a durable operation to
// display, even if the first operation read finds provisioning has failed.
test('shows the server operation after accepting a workspace launch', async () => {
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/templates') return json({ items: [launchTemplate], nextCursor: null });
    if (input === '/api/v1/workspaces' && init?.method === 'POST') return json({ id: 'launch-op', workspaceId: workspace.id });
    if (input === '/api/v1/operations/launch-op') return json({
      id: 'launch-op', version: 2, status: 'failed', currentStep: 'boot',
      steps: [{ key: 'boot', index: 0, label: 'Boot workspace', status: 'failed' }],
    });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Workspaces workspaceId={null} launch /></Providers>);
  await user.click(await screen.findByRole('radio', { name: /Example project/ }));
  await user.click(screen.getByRole('button', { name: 'Review launch' }));
  await user.click(screen.getByRole('button', { name: 'Launch workspace' }));
  expect(await screen.findByText('Failed — Boot workspace')).toBeVisible();
  expect(screen.getByRole('link', { name: 'View workspace' })).toHaveAttribute('href', '/workspaces/?id=workspace-one');
});

// A running VM can still be configuring its IDE. Once the controller allows
// entry, a later denial must report the lost access without opening a URL.
test('uses the controller IDE availability and requests a fresh entry grant', async () => {
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
  expect(fetch).toHaveBeenCalledWith('/api/v1/workspaces/workspace-one/entry-grants', expect.objectContaining({
    method: 'POST', body: '{"service":"ide"}', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'fixture' },
  }));
});
