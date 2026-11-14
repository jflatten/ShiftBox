import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Workspaces } from './workspaces';

const workspace = {
  id: 'workspace-one', ownerId: 'member', templateId: 'template-one', templateRevision: 2,
  desiredState: 'running', observedState: 'stopped', currentOperationId: 'operation-one', version: 4,
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

afterEach(() => { globalThis.fetch = originalFetch; });

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// Workspace detail keeps requested power separate from provider observations
// and shows the operation step and retained-data guidance.
test('shows the saved workspace state and workflow', async () => {
  globalThis.fetch = jest.fn(async (input) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/workspaces/workspace-one') return json(workspace);
    if (input === '/api/v1/operations/operation-one') return json({
      id: 'operation-one', version: 3, status: 'failed', currentStep: 'start',
      steps: [{ key: 'start', index: 0, label: 'Start workspace', status: 'failed' }],
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Workspaces workspaceId={workspace.id} /></Providers>);
  expect(await screen.findByText('Running', { exact: true })).toBeVisible();
  expect(screen.getByText('Stopped', { exact: true })).toBeVisible();
  expect(await screen.findByText('Failed — Start workspace')).toBeVisible();
  expect(screen.getByText(/Stop and start preserve/)).toBeVisible();
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
