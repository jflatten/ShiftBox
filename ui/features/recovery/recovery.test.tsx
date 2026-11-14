import { TextEncoder } from 'node:util';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Backup } from './backup';
import { Restore } from './restore';
import { RecoveryReview } from './review';

const originalFetch = globalThis.fetch;
const originalTextEncoder = globalThis.TextEncoder;
const originalEventSource = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
const csrfToken = 'fixture-csrf-token';
const passphrase = 'disposable archive passphrase';
const archive = '/private/backup.age';
const reviewId = '0a8e2f1d-6928-4b5c-8bf3-3eee279e419b';
const operationId = '0a8e2f1d-6928-4b5c-8bf3-3eee279e419c';
const validationId = '0a8e2f1d-6928-4b5c-8bf3-3eee279e419d';

beforeAll(() => { globalThis.TextEncoder = TextEncoder; });
afterAll(() => { globalThis.TextEncoder = originalTextEncoder; });

afterEach(() => {
  globalThis.fetch = originalFetch;
  if (originalEventSource) Object.defineProperty(globalThis, 'EventSource', originalEventSource);
  else Reflect.deleteProperty(globalThis, 'EventSource');
});

function json(payload: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    json: async () => payload,
  } as Response;
}

function captureOperationEvents() {
  const listeners = new Map<string, EventListener>();
  class Source {
    addEventListener(key: string, listener: EventListener) { listeners.set(key, listener); }
    removeEventListener(key: string) { listeners.delete(key); }
    close() {}
  }
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: Source });
  return listeners;
}

// Starting a backup by keyboard focuses the first missing field. An accepted
// request loses its passphrase while the visible workflow tracks server progress.
test('explains backup scope, clears the passphrase, and orders server steps', async () => {
  const listeners = captureOperationEvents();
  const checkpoint = { key: 'checkpoint', index: 0, label: 'Capture management and identity data' };
  const encryption = { key: 'encrypt', index: 1, label: 'Encrypt and verify the archive' };
  const pending = { id: operationId, status: 'pending', steps: [
    { ...encryption, status: 'pending' }, { ...checkpoint, status: 'pending' },
  ] };
  let snapshot = { ...pending, status: 'running', currentStep: 'checkpoint', steps: [
    { ...encryption, status: 'pending' }, { ...checkpoint, status: 'active' },
  ] };
  let accept!: (response: Response) => void;
  const admission = new Promise<Response>(resolve => { accept = resolve; });
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/backups' && init?.method === 'POST') return admission;
    if (input === `/api/v1/operations/${operationId}`) return json(snapshot);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Backup csrfToken={csrfToken} /></Providers>);

  expect(screen.getByText(/workspace (VM )?disks/i)).toBeVisible();
  const destination = screen.getByLabelText(/destination/i);
  const password = screen.getByLabelText(/^passphrase/i);
  const confirmation = screen.queryByLabelText(/^confirm passphrase/i);
  const submit = screen.getByRole('button', { name: 'Create backup' });
  submit.focus();
  await user.keyboard('{Enter}');
  expect(destination).toHaveFocus();
  expect(fetch).not.toHaveBeenCalled();

  await user.type(destination, archive);
  await user.type(password, passphrase);
  if (confirmation) await user.type(confirmation, passphrase);
  await user.click(submit);
  expect(fetch).toHaveBeenCalledWith('/api/v1/backups', expect.objectContaining({
    method: 'POST',
    headers: expect.objectContaining({ 'Idempotency-Key': expect.any(String), 'X-CSRF-Token': csrfToken }),
  }));
  expect(JSON.parse(fetch.mock.calls[0][1]?.body as string)).toEqual({ destination: archive, password: passphrase });
  expect(screen.getByLabelText(/^passphrase/i)).toHaveValue('');
  if (confirmation) expect(screen.getByLabelText(/^confirm passphrase/i)).toHaveValue('');

  await act(async () => accept(json(pending, 202)));
  const capture = await screen.findByRole('button', { name: 'Capture management and identity data: In progress' });
  const encrypt = screen.getByRole('button', { name: 'Encrypt and verify the archive: Pending' });
  expect(capture).toHaveAttribute('aria-current', 'step');
  expect(capture.compareDocumentPosition(encrypt) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(encrypt).toBeDisabled();

  await waitFor(() => expect(listeners.has('operation.updated')).toBe(true));
  snapshot = { ...snapshot, currentStep: 'encrypt', steps: [
    { ...encryption, status: 'active' }, { ...checkpoint, status: 'succeeded' },
  ] };
  act(() => listeners.get('operation.updated')!(new Event('operation.updated')));
  expect(await screen.findByRole('button', { name: 'Encrypt and verify the archive: In progress' }))
    .toHaveAttribute('aria-current', 'step');

  // Restored or revoked authority must not retain the former live Stepper.
  act(() => listeners.get('authorization.revoked')!(new Event('authorization.revoked')));
  await waitFor(() => expect(screen.queryByRole('button', { name: /Encrypt and verify the archive:/i }))
    .not.toBeInTheDocument());
});

// Restore validation reports the durable validation ID and leaves activation
// to the protected local command.
test('clears the restore passphrase and shows the server validation finding', async () => {
  const operation = { id: operationId, status: 'succeeded', steps: [
    { key: 'validate', index: 0, label: 'Verify archive and replacement', status: 'succeeded', detail: validationId },
  ] };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/restores/validate' && init?.method === 'POST') return json(operation, 202);
    if (input === `/api/v1/operations/${operationId}`) return json(operation);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Restore csrfToken={csrfToken} /></Providers>);

  await user.type(screen.getByLabelText(/archive path/i), archive);
  await user.type(screen.getByLabelText(/replacement directory/i), '/private/replacement');
  await user.type(screen.getByLabelText(/^passphrase/i), passphrase);
  await user.click(screen.getByRole('button', { name: 'Validate restore' }));
  expect(screen.getByLabelText(/^passphrase/i)).toHaveValue('');
  expect(await screen.findByText(new RegExp(validationId))).toBeVisible();
  expect(screen.getByText(/shiftbox restore activate/i)).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Activate installation' })).not.toBeInTheDocument();
  const writes = fetch.mock.calls.filter(([, init]) => init?.method === 'POST');
  expect(writes).toHaveLength(1);
  expect(writes[0][0]).toBe('/api/v1/restores/validate');
  expect(JSON.parse(writes[0][1]?.body as string)).toMatchObject({ archivePath: archive, password: passphrase });
});

const review = {
  id: reviewId, restoreOperationId: operationId, version: 4,
  sections: { reviewedUserIds: ['admin'], workspaces: [] },
};
const reviewPath = `/api/v1/recovery/${reviewId}`;

// The identity step shows the restored person and records their review with
// the version the administrator inspected.
test('confirms the restored identity before continuing recovery review', async () => {
  const identity = {
    id: 'admin', primaryEmail: 'admin@example.test', role: 'administrator',
    membershipState: 'active', credentialMode: 'local',
  };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === reviewPath && init?.method === 'PATCH') return json({ ...review, state: 'provider_reconcile', version: 5 });
    if (input === reviewPath) return json({ ...review, state: 'identity_review', sections: {
      ...review.sections, reviewedUserIds: [], identities: [identity],
    } });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><RecoveryReview csrfToken={csrfToken} reviewId={reviewId} /></Providers>);
  await user.click(await screen.findByRole('checkbox', { name: /admin@example\.test/i }));
  await user.click(screen.getByRole('button', { name: 'Confirm identities' }));
  const patch = fetch.mock.calls.find(([, init]) => init?.method === 'PATCH');
  expect(patch?.[0]).toBe(reviewPath);
  expect(patch?.[1]?.headers).toEqual(expect.objectContaining({ 'If-Match': '"4"', 'X-CSRF-Token': csrfToken }));
  expect(JSON.parse(patch?.[1]?.body as string)).toEqual({
    section: 'identities', decisions: { userIds: ['admin'] },
  });
});

// A retained workspace with conflicting ownership keeps activation gated
// and surfaces the conflict for the recovery administrator.
test('shows provider conflict evidence and prevents activation', async () => {
  const fetch = jest.fn(async input => {
    if (input === reviewPath) return json({ ...review, state: 'blocked', sections: {
      ...review.sections, workspaces: [
        { workspaceId: 'retained-workspace', generation: 1, version: 2, match: 'ownership_conflict' },
      ],
    } });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><RecoveryReview csrfToken={csrfToken} reviewId={reviewId} /></Providers>);
  expect(await screen.findByText(/ownership conflict/i)).toBeVisible();
  const activate = screen.queryByRole('button', { name: 'Activate installation' });
  if (activate) expect(activate).toBeDisabled();
  expect(fetch.mock.calls.map(([url]) => url)).toEqual([reviewPath]);
});

// A ready review leaves activation to an explicit action by the recovery
// administrator, using the current review's protected endpoint.
test('submits activation only after explicit approval of a ready review', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === reviewPath) return json({ ...review, state: 'ready' });
    if (input === `${reviewPath}/activate` && init?.method === 'POST') {
      return json({ id: operationId, status: 'succeeded', steps: [] }, 202);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><RecoveryReview csrfToken={csrfToken} reviewId={reviewId} /></Providers>);
  const activate = await screen.findByRole('button', { name: 'Activate installation' });
  expect(activate).toBeEnabled();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
  await user.click(activate);
  await waitFor(() => expect(fetch).toHaveBeenCalledWith(`${reviewPath}/activate`, expect.objectContaining({
    method: 'POST',
    headers: expect.objectContaining({ 'Idempotency-Key': expect.any(String), 'X-CSRF-Token': csrfToken }),
  })));
});
