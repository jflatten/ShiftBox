import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Audit } from './audit';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
const path = '/api/v1/admin/audit';
const session = { user: { id: 'operator', role: 'administrator', primaryEmail: 'admin@example.test' }, mode: 'normal',
  installationState: 'active', permissions: ['audit.list'], csrfToken: 'fixture' };
const event = { id: 'one', occurredAt: '2026-11-14T10:00:00Z', actorType: 'user', actorId: 'operator',
  action: 'user.approve', resourceType: 'user', resourceId: 'c95dfd94-59d1-4d31-8f2d-e7dfca2b83a4',
  outcome: 'allowed', reasonCode: 'membership_approved', metadata: { unrecognized: 'ignored fixture' } };
function json(payload: unknown, link?: string) {
  return { ok: true, status: 200, headers: new Headers(link ? { Link: link } : {}), json: async () => payload } as Response;
}

// Server filters survive cursor navigation; exact identifiers remain in expandable details.
test('filters and pages through audit events', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === path) return json([{ ...event, action: 'user.disable' }]);
    if (input === path + '?action=user.approve') return json([event], '</api/v1/admin/audit?action=user.approve&cursor=12>; rel="next"');
    if (input === path + '?action=user.approve&cursor=12') return json([{ ...event, id: 'two', reasonCode: 'older_event' }]);
    throw new Error('unexpected request ' + input);
  });
  const user = userEvent.setup();
  render(<Providers><Audit /></Providers>);
  expect(await screen.findByText('Disable user')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Filter full history' }));
  await user.type(await screen.findByRole('textbox', { name: 'Action code' }), 'user.approve');
  await user.click(screen.getByRole('button', { name: 'Apply filters' }));
  expect(await screen.findByText('Approve user')).toBeVisible();
  expect(screen.queryByText(event.resourceId)).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Show event details' }));
  expect(screen.getByText(event.resourceId)).toBeVisible();
  expect(screen.getByText(event.action)).toBeVisible();
  expect(screen.queryByText('ignored fixture')).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('Page 2')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled();
  await user.click(screen.getByRole('button', { name: 'Show event details' }));
  expect(screen.getByText('older_event')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Previous page' }));
  expect(await screen.findByText('Page 1')).toBeVisible();
});

// A direct visit cannot fetch audit data without current administrator authority.
test('checks the audit permission before requesting history', async () => {
  const fetch = jest.fn(async input => input === '/api/v1/session' ? json({ ...session, permissions: [] }) : json([]));
  globalThis.fetch = fetch;
  render(<Providers><Audit /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session']);
});

// Page search includes resolved user names and combines with outcome without claiming a global search.
test('searches the current page by user name and filters outcome', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['audit.list', 'user.list'] });
    if (input === '/api/v1/admin/users') return json([{ id: event.resourceId, displayName: 'Morgan Lee', primaryEmail: 'morgan@example.test' }]);
    return json([event, { ...event, id: 'two', action: 'user.disable', resourceId: 'other', outcome: 'denied' }]);
  });
  const user = userEvent.setup();
  render(<Providers><Audit /></Providers>);
  expect(await screen.findByText('Morgan Lee')).toBeVisible();
  await user.type(screen.getByRole('textbox', { name: 'Search this page' }), 'Morgan');
  expect(screen.getByText('Approve user')).toBeVisible();
  expect(screen.queryByText('Disable user')).toBeNull();
  await user.selectOptions(screen.getByRole('combobox', { name: 'Outcome on this page' }), 'denied');
  expect(screen.getByText('No matches on this page')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Clear page filters' }));
  expect(screen.getByText('Disable user')).toBeVisible();
});

// Invalid resource identifiers should produce field guidance, not a failing request to the controller.
test('validates resource filters before requesting history', async () => {
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json(session) : json([event]));
  const user = userEvent.setup();
  render(<Providers><Audit /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Filter full history' }));
  await user.type(await screen.findByRole('textbox', { name: 'Resource ID' }), 'not-an-id');
  await user.click(screen.getByRole('button', { name: 'Apply filters' }));
  expect(await screen.findByText('Enter a valid resource ID from event details.')).toBeVisible();
  expect(globalThis.fetch).not.toHaveBeenCalledWith(expect.stringContaining('?resourceId='), expect.anything());
});

// A failed refresh must not leave stale audit evidence presented as a successful current read.
test('shows a retry after a failed load and recovers', async () => {
  let failed = true;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (failed) throw new Error('offline');
    return json([event]);
  });
  const user = userEvent.setup();
  render(<Providers><Audit /></Providers>);
  expect(await screen.findByText('Unable to complete the request')).toBeVisible();
  expect(screen.queryByRole('table')).toBeNull();
  failed = false;
  await user.click(screen.getByRole('button', { name: 'Retry' }));
  expect(await screen.findByText('Approve user')).toBeVisible();
});

// Audit evidence remains usable when a deleted account cannot be resolved to a current name.
test('retains records when the user directory is unavailable', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['audit.list', 'user.list'] });
    if (input === '/api/v1/admin/users') throw new Error('unavailable');
    return json([{ ...event, actorId: 'missing-user' }]);
  });
  render(<Providers><Audit /></Providers>);
  const table = await screen.findByRole('table', { name: 'Audit events' });
  expect(within(table).getByText('User (name unavailable)')).toBeVisible();
  expect(screen.getByText('Approve user')).toBeVisible();
});
