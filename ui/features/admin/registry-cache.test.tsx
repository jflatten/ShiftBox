import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Infrastructure } from './infrastructure';

const endpoint = '/api/v1/admin/image-cache';
const builtImagesEndpoint = endpoint + '/built-images';
const cacheURL = '/infrastructure/?section=registry-cache';
const operationId = 'registry-cache-operation';
const storagePath = '/var/lib/shiftbox/cache/registry';
const failure = 'The proposed storage location is unavailable.';
const administrator = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['infrastructure.view', 'infrastructure.manage'], csrfToken: 'fixture',
};
const infrastructure = {
  provider: { name: 'utm', version: '5.0.8', architecture: 'amd64', networkModes: ['shared'] },
  capacity: { cpuCount: 4, memoryMiB: 8192, diskMiB: 131072 },
  version: 1, conditions: [],
};
const settings = { enabled: false, storagePath, maxBytes: null, volumeHighPercent: 80, volumeLowPercent: 70 };
const builtImages = { enabled: true, version: 5, availability: 'available' };
const initial = {
  version: 1, desired: settings, effective: { ...settings, version: 1 },
  builtImages,
  activation: { phase: 'disabled', error: null },
  health: { state: 'disabled', observedAt: null, detail: null, guestEndpoint: null, capabilities: [] },
  usage: null, effectiveness: null, retiredLocations: [], cachedImages: [],
};
const originalFetch = globalThis.fetch;
const originalEventSource = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');

afterEach(() => {
  globalThis.fetch = originalFetch;
  if (originalEventSource) Object.defineProperty(globalThis, 'EventSource', originalEventSource);
  else Reflect.deleteProperty(globalThis, 'EventSource');
  window.history.replaceState(null, '', '/');
});

function json(data: unknown, status = 200) {
  return {
    ok: status < 300, status, json: async () => data,
    headers: new Headers({ 'Content-Type': 'application/json' }),
  } as Response;
}

function stubFetch(
  cache: (init?: RequestInit) => Response | Promise<Response>,
  current = administrator,
  operation?: unknown,
  updateBuiltImages?: (init?: RequestInit) => Response | Promise<Response>,
) {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (url, init) => {
    if (url === '/api/v1/session') return json(current);
    if (url === '/api/v1/admin/infrastructure') return json(infrastructure);
    if (url === '/api/v1/admin/resource-profiles') return json([]);
    if (url === endpoint) return cache(init);
    if (url === builtImagesEndpoint && updateBuiltImages) return updateBuiltImages(init);
    if (url === `/api/v1/operations/${operationId}` && operation) return json(operation);
    throw new Error(`Unexpected request to ${url}`);
  });
  globalThis.fetch = fetch;
  return fetch;
}

function openInfrastructure(url = '/infrastructure/') {
  window.history.replaceState(null, '', url);
  return render(<Providers><Infrastructure /></Providers>);
}

// Only an explicit save stages changes. A failed activation remains visible after
// reload while the prior storage path and generation remain effective.
test('saves a keyboard-edited draft and restores the failed activation on reload', async () => {
  class OperationStream extends EventTarget { close() {} }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: OperationStream });
  const nextPath = '/mnt/registry-cache';
  const desired = { ...settings, enabled: true, storagePath: nextPath, maxBytes: '33554432' };
  const pending = { ...initial, version: 2, desired, activation: { phase: 'draining', error: null, operationId } };
  const failed = { ...pending, activation: { phase: 'failed', error: failure, operationId } };
  let snapshot: unknown = initial;
  const requests: RequestInit[] = [];
  const operation = { id: operationId, type: 'activate_image_cache', status: 'running', version: 1, currentStep: 'drain', steps: [
    { key: 'validate', index: 0, label: 'Validate', status: 'succeeded' },
    { key: 'drain', index: 1, label: 'Finish active pulls', status: 'active' },
    { key: 'activate', index: 2, label: 'Activate', status: 'pending' },
  ] };
  const fetch = stubFetch(init => {
    if (init?.method === 'PUT') {
      requests.push(init);
      snapshot = pending;
      return json({ operationId }, 202);
    }
    return json(snapshot);
  }, administrator, operation);
  const user = userEvent.setup();
  const view = openInfrastructure();
  const tab = await screen.findByRole('tab', { name: 'Cache' });
  expect(fetch.mock.calls.some(([url]) => url === endpoint)).toBe(false);
  tab.focus();
  await user.keyboard('{Enter}');
  expect(tab).toHaveAttribute('aria-selected', 'true');
  expect(window.location.search).toBe('?section=registry-cache');

  const retention = await screen.findByRole('switch', { name: 'Retain cached image content' });
  const save = screen.getByRole('button', { name: 'Save cache settings' });
  expect(retention).not.toBeChecked();
  expect(save).toBeDisabled();
  retention.focus();
  await user.keyboard(' ');
  const path = screen.getByRole('textbox', { name: 'Cache storage location' });
  await user.clear(path);
  await user.type(path, nextPath);
  await user.type(screen.getByLabelText('Cache size (MiB)'), '32');
  await user.click(screen.getByRole('tab', { name: 'Platform' }));
  await user.click(tab);
  expect(path).toHaveValue(nextPath);
  expect(requests).toHaveLength(0);
  await user.click(save);

  expect(await screen.findByRole('button', { name: 'Finish active pulls: In progress' })).toHaveAttribute('aria-current', 'step');
  expect(requests).toHaveLength(1);
  expect(new Headers(requests[0].headers).get('If-Match')).toBe('"1"');
  expect(new Headers(requests[0].headers).get('X-CSRF-Token')).toBe('fixture');
  expect(JSON.parse(String(requests[0].body))).toEqual(desired);

  snapshot = failed;
  view.unmount();
  openInfrastructure(cacheURL);
  expect(await screen.findByRole('tab', { name: 'Cache', selected: true })).toBeVisible();
  expect(await screen.findByText(failure)).toBeVisible();
  expect(screen.getByRole('textbox', { name: 'Cache storage location' })).toHaveValue(nextPath);
  expect(screen.getByLabelText('Cache size (MiB)')).toHaveValue('32');
  expect(screen.getByText(storagePath, { exact: false })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Save cache settings' })).toBeEnabled();
});

// Query feedback distinguishes a pending network read, an error, and an
// installation that has no measurements. Retry issues only a GET.
test('loads, retries and discloses missing measurements without a mutation', async () => {
  const gate = Promise.withResolvers<Response>();
  const cache = jest.fn<Response | Promise<Response>, [RequestInit?]>()
    .mockImplementationOnce(() => gate.promise)
    .mockImplementation(() => json(initial));
  stubFetch(cache);
  const user = userEvent.setup();
  openInfrastructure(cacheURL);
  const panel = await screen.findByRole('tabpanel', { name: 'Cache' });
  expect(await within(panel).findByRole('status', { name: /loading registry cache/i })).toBeVisible();
  await act(async () => gate.resolve(json({}, 503)));
  expect(await within(panel).findByRole('alert')).toBeVisible();
  await user.click(within(panel).getByRole('button', { name: 'Retry' }));
  expect((await within(panel).findAllByText('Not measured')).length).toBeGreaterThan(0);
  expect(cache.mock.calls).toHaveLength(2);
  expect(cache.mock.calls.every(([init]) => !init?.method || init.method === 'GET')).toBe(true);
});

// A view-only administrator can read exact measured bytes and an explicitly
// unknown measurement. A fresh API denial immediately removes protected data.
test('limits view-only access and clears cache details after permission loss', async () => {
  let denied = false;
  stubFetch(() => denied ? json({}, 403) : json({ ...initial, cachedImages: [
    { reference: 'docker.io/library/busybox:1.37', digest: 'sha256:busybox', architecture: 'amd64', availability: 'complete',
      source: 'registry', addedAt: '2026-11-20T17:00:00Z', layerBytes: ['500000'] },
    { reference: 'mcr.microsoft.com/azurelinux/base/core:3.0', digest: 'sha256:azure', architecture: 'amd64', availability: 'partial',
      source: 'registry', addedAt: '2026-11-20T16:00:00Z', layerBytes: [null] },
  ], usage: {
    retainedBytes: '12345', temporaryBytes: '0', reservedBytes: '0', retiredBytes: '0',
    budgetBytes: null, volumeTotalBytes: '104857600', volumeUsedBytes: null,
    volumeAvailableBytes: null, activeTransfers: 0, lastEvictionAt: null, lastEvictedBytes: null,
    pressureReason: null, observedAt: '2026-11-17T12:00:00Z',
  } }), { ...administrator, permissions: ['infrastructure.view'] });
  const user = userEvent.setup();
  openInfrastructure(cacheURL);
  expect(await screen.findByText('12,345 bytes', { exact: false })).toBeVisible();
  expect(screen.getByRole('table', { name: 'Cached images' })).toBeVisible();
  expect(screen.getByText('docker.io/library/busybox:1.37')).toBeVisible();
  expect(screen.getByText('Complete')).toBeVisible();
  expect(screen.getByText('Partial')).toBeVisible();
  expect(screen.getAllByText('Not measured').length).toBeGreaterThan(0);
  expect(screen.queryByRole('button', { name: 'Save cache settings' })).not.toBeInTheDocument();
  denied = true;
  await user.click(screen.getByRole('button', { name: 'Refresh' }));
  await waitFor(() => expect(screen.queryByText('12,345 bytes', { exact: false })).not.toBeInTheDocument());
  expect(screen.queryByRole('table', { name: 'Cached images' })).not.toBeInTheDocument();
});

// A published build and its retained layers are visible in the table. Moving
// to page two reaches the final entry without sending a settings mutation.
test('shows build layer sizes and dates and pages 20 cached images at a time', async () => {
  stubFetch(() => json({ ...initial, cachedImages: Array.from({ length: 21 }, (_, index) => ({
    reference: index === 0 ? 'Python tools' : `example.com/lab:${index}`,
    digest: `sha256:${index}`, architecture: 'amd64', availability: 'complete',
    source: index === 0 ? 'devcontainer' : 'registry', addedAt: '2026-11-20T17:00:00Z',
    layerBytes: index === 0 ? ['2500000', '12000000'] : [],
  })) }));
  const user = userEvent.setup();
  openInfrastructure(cacheURL);
  const table = await screen.findByRole('table', { name: 'Cached images' });
  expect(within(table).getByRole('columnheader', { name: 'Date added' })).toBeVisible();
  expect(within(table).getAllByRole('row')).toHaveLength(21);
  const project = within(table).getByRole('row', { name: /Python tools/ });
  expect(within(project).getByText('Devcontainer build')).toBeVisible();
  expect(project.querySelector('time')).toHaveAttribute('dateTime', '2026-11-20T17:00:00Z');
  await user.click(within(project).getByRole('button', { name: '2 layers' }));
  expect(within(project).getByText('2.5 MB', { exact: false })).toBeVisible();
  expect(within(project).getByText('12 MB', { exact: false })).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Page 2' }));
  expect(within(table).getAllByRole('row')).toHaveLength(2);
  expect(within(table).getByText('example.com/lab:20')).toBeVisible();
  expect(screen.getByText('21–21 of 21 images')).toBeVisible();
});

// The size column shows retained layer totals with the layer details collapsed
// and distinguishes a partial image whose layer manifest is unavailable.
test('shows total retained image size without expanding its layers', async () => {
  const image = { architecture: 'amd64', source: 'registry', addedAt: '2026-11-20T17:00:00Z' };
  stubFetch(() => json({ ...initial, cachedImages: [
    { ...image, reference: 'Complete build', digest: 'sha256:complete', availability: 'complete', layerBytes: ['2500000', '12000000'] },
    { ...image, reference: 'Partial build', digest: 'sha256:partial', availability: 'partial', layerBytes: ['1250000', null] },
    { ...image, reference: 'Unknown build', digest: 'sha256:unknown', availability: 'partial', layerBytes: [] },
  ] }));
  openInfrastructure(cacheURL);
  const table = await screen.findByRole('table', { name: 'Cached images' });
  expect(within(table).getByRole('columnheader', { name: 'Total size (MB)' })).toBeVisible();
  const complete = within(table).getByRole('row', { name: /Complete build/ });
  expect(within(complete).getByRole('button', { name: '2 layers' })).toHaveAttribute('aria-expanded', 'false');
  expect(within(complete).getByRole('cell', { name: '14.5 MB' })).toBeVisible();
  const partial = within(table).getByRole('row', { name: /Partial build/ });
  expect(within(partial).getByRole('cell', { name: '1.25 MB' })).toBeVisible();
  const unknown = within(table).getByRole('row', { name: /Unknown build/ });
  expect(within(unknown).getAllByRole('cell', { name: 'Unavailable' })).toHaveLength(2);
});

// A member reaching the cache deep link must never cause the protected query,
// even if the session fixture incorrectly includes infrastructure permissions.
test('denies the registry cache deep link to a member', async () => {
  const fetch = stubFetch(() => json(initial), { ...administrator, user: { id: 'member', role: 'member' } });
  openInfrastructure(cacheURL);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(screen.queryByRole('tab', { name: 'Cache' })).not.toBeInTheDocument();
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(['/api/v1/session']);
});

// An administrator can find and turn off built-image caching by keyboard. An
// explicit save uses the independent version and the next read reflects it.
test('saves and reloads built devcontainer images independently of public retention', async () => {
  let saved = builtImages;
  const fetch = stubFetch(() => json({ ...initial, builtImages: saved }), administrator, undefined, () => {
    saved = { enabled: false, version: 6, availability: 'disabled' };
    return json(saved);
  });
  const user = userEvent.setup();
  const view = openInfrastructure();
  const tab = await screen.findByRole('tab', { name: 'Cache' });
  tab.focus();
  await user.keyboard('{Enter}');
  expect(tab).toHaveAttribute('aria-selected', 'true');
  const panel = screen.getByRole('tabpanel', { name: 'Cache' });
  const control = await within(panel).findByRole('switch', { name: 'Built devcontainer images' });
  const save = within(panel).getByRole('button', { name: 'Save built image settings' });
  const publicPath = within(panel).getByRole('textbox', { name: 'Cache storage location' });
  await user.clear(publicPath);
  await user.type(publicPath, '/mnt/public-cache-draft');
  expect(control).toBeChecked();
  expect(save).toBeDisabled();
  control.focus();
  await user.keyboard(' ');
  expect(control).not.toBeChecked();
  const warning = within(panel).getByText(/remov(?:e|es|ed).*stored built (?:devcontainer )?images/i);
  expect(warning).toBeVisible();
  expect(warning).toHaveTextContent(/organization/i);
  expect(fetch.mock.calls.filter(([url]) => url === builtImagesEndpoint)).toHaveLength(0);
  expect(within(panel).getByRole('switch', { name: 'Retain cached image content' })).not.toBeChecked();
  save.focus();
  await user.keyboard('{Enter}');
  await waitFor(() => expect(save).toBeDisabled());
  const writes = fetch.mock.calls.filter(([, init]) => init?.method === 'PUT');
  expect(writes).toHaveLength(1);
  const [url, request] = writes[0];
  expect(url).toBe(builtImagesEndpoint);
  const headers = new Headers(request?.headers);
  expect(headers.get('If-Match')).toBe('"5"');
  expect(headers.get('X-CSRF-Token')).toBe('fixture');
  expect(JSON.parse(String(request?.body))).toEqual({ enabled: false });
  expect(publicPath).toHaveValue('/mnt/public-cache-draft');

  view.unmount();
  openInfrastructure(cacheURL);
  expect(await screen.findByRole('tab', { name: 'Cache', selected: true })).toBeVisible();
  expect(await screen.findByRole('switch', { name: 'Built devcontainer images' })).not.toBeChecked();
  expect(screen.getByRole('button', { name: 'Save built image settings' })).toBeDisabled();
});

// A view-only administrator can see the setting and why it is unavailable,
// but cannot edit or save it.
test('shows built-image storage availability without offering view-only users a mutation', async () => {
  const fetch = stubFetch(() => json({ ...initial, builtImages: { ...builtImages, availability: 'unavailable' } }),
    { ...administrator, permissions: ['infrastructure.view'] });
  openInfrastructure(cacheURL);
  const panel = await screen.findByRole('tabpanel', { name: 'Cache' });
  expect(await within(panel).findByText('Built devcontainer images', { exact: true })).toBeVisible();
  expect(within(panel).getByText('Unavailable', { exact: true })).toBeVisible();
  const control = within(panel).queryByRole('switch', { name: 'Built devcontainer images' });
  if (control) expect(control).toBeDisabled();
  expect(within(panel).queryByRole('button', { name: 'Save built image settings' })).not.toBeInTheDocument();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);
});

// A stale write or offline save announces the failure and waits for an
// explicit next action instead of automatically sending another mutation.
test.each(['stale version', 'network failure'])('shows %s when saving built images', async failure => {
  const staleTitle = 'Refresh the built image settings before updating';
  const fetch = stubFetch(() => json(initial), administrator, undefined, () => {
    if (failure === 'network failure') throw new TypeError('fixture network failure');
    return {
      ...json({ type: 'about:blank', title: staleTitle, code: 'request.version_mismatch', status: 412, requestId: '' }, 412),
      headers: new Headers({ 'Content-Type': 'application/problem+json' }),
    };
  });
  const user = userEvent.setup();
  openInfrastructure(cacheURL);
  const control = await screen.findByRole('switch', { name: 'Built devcontainer images' });
  await user.click(control);
  await user.click(screen.getByRole('button', { name: 'Save built image settings' }));
  const feedback = await screen.findByText(failure === 'stale version' ? staleTitle : 'Unable to complete the request');
  expect(feedback.closest('[role="alert"]')).toBeVisible();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
});
